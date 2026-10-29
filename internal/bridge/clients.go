package bridge

import (
	"context"
	"net"
	"sort"
	"time"
)

// Client presence: session.list_clients, client.list, server.whoami,
// client.set_label, and the client_* events. The bridge listens on TCP
// loopback without authentication, so every principal is anonymous.

func principal(remote string) map[string]any {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	return map[string]any{"kind": "anonymous", "transport": "tcp", "display": host, "remote_addr": remote}
}

func stamp(t time.Time) string { return t.Format(time.RFC3339Nano) }

// describe is a client.list item. With sid set it is a session.list_clients
// item: connected_at is when the client attached and there are no session_ids.
func (c *Client) describe(sid string) map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := map[string]any{"client_id": c.ID, "principal": c.principal, "info": c.info,
		"connected_at": stamp(c.connectedAt), "connection_state": "connected"}
	if sid != "" {
		m["connected_at"] = stamp(c.attachedAt[sid])
		return m
	}
	var ids []string
	for id := range c.attached {
		ids = append(ids, id)
	}
	if len(ids) > 0 {
		m["session_ids"] = ids
	}
	return m
}

func (c *Client) isAttached(sid string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.attached[sid]
}

func (c *Client) wantsAll() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.eventAll
}

// sessionAttached records an attach and sends client_connected to the
// session, the attaching client included. A repeated attach sends nothing.
func (b *Bridge) sessionAttached(c *Client, sid string) {
	c.mu.Lock()
	again := c.attached[sid]
	c.attached[sid] = true
	if !again {
		c.attachedAt[sid] = time.Now()
	}
	at := c.attachedAt[sid]
	c.mu.Unlock()
	if !again {
		b.clientEvent(sid, "client_connected", map[string]any{
			"client_id": c.ID, "principal": c.principal, "connected_at": stamp(at)})
	}
}

// sessionDetached drops an attach and sends client_disconnected.
func (b *Bridge) sessionDetached(c *Client, sid string) {
	c.mu.Lock()
	was := c.attached[sid]
	delete(c.attached, sid)
	delete(c.attachedAt, sid)
	c.mu.Unlock()
	if was {
		b.clientEvent(sid, "client_disconnected", map[string]any{"client_id": c.ID, "principal": c.principal})
	}
}

func (b *Bridge) clientEvent(sid, name string, payload map[string]any) {
	ev := map[string]any{"type": "session_event", "payload": map[string]any{
		"session_id": sid, "event": map[string]any{"type": name, "payload": payload}}}
	for _, o := range b.clientList() {
		if o.wants(sid) {
			o.push(ev)
		}
	}
}

// joined tells event_scope "all" clients that c connected.
func (b *Bridge) joined(c *Client) {
	ev := map[string]any{"type": "client_joined", "payload": map[string]any{"client": c.describe("")}}
	for _, o := range b.clientList() {
		if o != c && o.wantsAll() {
			o.push(ev)
		}
	}
}

// left ends c's attachments and tells event_scope "all" clients it is gone.
func (b *Bridge) left(c *Client) {
	c.mu.Lock()
	var sids []string
	for sid := range c.attached {
		sids = append(sids, sid)
	}
	c.mu.Unlock()
	for _, sid := range sids {
		b.sessionDetached(c, sid)
	}
	ev := map[string]any{"type": "client_left", "payload": map[string]any{"client_id": c.ID}}
	for _, o := range b.clientList() {
		if o.wantsAll() {
			o.push(ev)
		}
	}
}

func hListClients(ctx context.Context, b *Bridge, c *Client, p P) (any, error) {
	sid := p.str("session_id")
	if _, err := b.herdrID(sid, "session"); err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, o := range b.clientsByAge() {
		if o.isAttached(sid) {
			out = append(out, o.describe(sid))
		}
	}
	return map[string]any{"clients": out}, nil
}

func hClientList(ctx context.Context, b *Bridge, c *Client, p P) (any, error) {
	sid := p.str("session_id")
	kinds := map[string]bool{}
	if raw, ok := p["kinds"].([]any); ok {
		for _, k := range raw {
			if s, ok := k.(string); ok {
				kinds[s] = true
			}
		}
	}
	out := []map[string]any{}
	for _, o := range b.clientsByAge() {
		if sid != "" && !o.isAttached(sid) {
			continue
		}
		d := o.describe("")
		if kind, _ := d["info"].(map[string]any)["kind"].(string); len(kinds) > 0 && !kinds[kind] {
			continue
		}
		out = append(out, d)
	}
	return map[string]any{"clients": out}, nil
}

func hWhoami(ctx context.Context, b *Bridge, c *Client, p P) (any, error) {
	return map[string]any{"principal": c.principal}, nil
}

func hSetClientLabel(ctx context.Context, b *Bridge, c *Client, p P) (any, error) {
	c.mu.Lock()
	info := map[string]any{}
	for k, v := range c.info {
		info[k] = v
	}
	if l := p.str("label"); l != "" {
		info["label"] = l
	} else {
		delete(info, "label")
	}
	c.info = info
	c.mu.Unlock()
	return map[string]any{}, nil
}

// clientsByAge lists connected clients, oldest first.
func (b *Bridge) clientsByAge() []*Client {
	cs := b.clientList()
	sort.Slice(cs, func(i, j int) bool { return cs[i].connectedAt.Before(cs[j].connectedAt) })
	return cs
}
