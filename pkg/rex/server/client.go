package server

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"sort"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/elonnzhang/herdrex/pkg/rex"
)

// Client is one control connection.
type Client struct {
	ID          string
	s           *Server
	conn        *websocket.Conn
	principal   map[string]any
	connectedAt time.Time

	wmu sync.Mutex // serializes writes, so events go out in seq order

	mu        sync.Mutex
	seq       uint64
	info      map[string]any       // the hello's client object; replaced, never changed in place
	attached  map[string]time.Time // session id -> when it attached
	attachAll bool
	eventAll  bool // attach_all with event_scope "all"
}

// Info is the client object from the hello, with the label client.set_label
// last set. Callers must not change it.
func (c *Client) Info() map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.info
}

func (c *Client) Principal() map[string]any { return c.principal }

// Attached reports whether the client attached to the session itself, by
// session.attach or session.bootstrap. Attaching to every session does not
// count: data connections and block ownership need a session attachment.
func (c *Client) Attached(sid string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.attached[sid]
	return ok
}

// Describe is the client as a client.list item. With sid set it is a
// session.list_clients item: connected_at is when the client attached and
// there are no session_ids.
func (c *Client) Describe(sid string) map[string]any {
	c.mu.Lock()
	defer c.mu.Unlock()
	m := map[string]any{
		"client_id":        c.ID,
		"principal":        c.principal,
		"info":             c.info,
		"connected_at":     stamp(c.connectedAt),
		"connection_state": "connected",
	}
	if sid != "" {
		m["connected_at"] = stamp(c.attached[sid])
		return m
	}
	var ids []string
	for id := range c.attached {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	if len(ids) > 0 {
		m["session_ids"] = ids
	}
	return m
}

// wants: events of a session, other than block_event, go to clients
// attached to it or to every session.
func (c *Client) wants(sid string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.attached[sid]
	return c.attachAll || ok
}

// wantsBlockEvents: block_event goes to clients attached to the session or
// attached to everything with event_scope "all".
func (c *Client) wantsBlockEvents(sid string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	_, ok := c.attached[sid]
	return c.eventAll || ok
}

func (c *Client) isAttachAll() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.attachAll
}

func (c *Client) wantsAll() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.eventAll
}

func (c *Client) currentSeq() uint64 {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.seq
}

func (c *Client) write(v any) error {
	b, err := marshal(v)
	if err != nil {
		return err
	}
	return c.send(b)
}

func (c *Client) send(b []byte) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return c.sendLocked(b)
}

func (c *Client) sendLocked(b []byte) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return c.conn.Write(ctx, websocket.MessageText, b)
}

// push sends an event with the connection's next seq. Seq and write happen
// under one lock, so events go out in seq order.
func (c *Client) push(payload any) {
	c.wmu.Lock()
	c.mu.Lock()
	c.seq++
	seq := c.seq
	c.mu.Unlock()
	if c.s.Trace {
		log.Printf("event client=%s seq=%d %v", c.ID, seq, payload)
	}
	b, err := marshal(map[string]any{"type": "event", "seq": seq, "payload": payload})
	if err == nil {
		err = c.sendLocked(b)
	}
	c.wmu.Unlock()
	if err != nil { // a skipped event would leave a gap in seq
		c.conn.Close(websocket.StatusGoingAway, "write failed")
	}
}

// The server does no authentication, so every principal is anonymous.
func principal(remote string) map[string]any {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	return map[string]any{"kind": "anonymous", "transport": "tcp", "display": host, "remote_addr": remote}
}

func stamp(t time.Time) string { return t.Format(time.RFC3339Nano) }

// AttachAll records session.attach_all or the attach_all part of
// session.bootstrap. The scope must be "session" or "all".
func (s *Server) AttachAll(c *Client, scope string) error {
	if scope != "all" && scope != "session" {
		return rex.NewError(rex.CodeBadRequest, `event_scope must be "all" or "session"`, 400)
	}
	c.mu.Lock()
	c.attachAll = true
	c.eventAll = c.eventAll || scope == "all"
	c.mu.Unlock()
	return nil
}

// Attach records that c attached to the session. The first attach sends
// client_connected to the session, the attaching client included.
func (s *Server) Attach(c *Client, sid string) {
	c.mu.Lock()
	at, again := c.attached[sid]
	if !again {
		at = time.Now()
		c.attached[sid] = at
	}
	c.mu.Unlock()
	if !again {
		s.SessionEvent(sid, "client_connected",
			map[string]any{
				"client_id":    c.ID,
				"principal":    c.principal,
				"connected_at": stamp(at),
			})
	}
}

// Detach ends c's attachment to the session and sends client_disconnected.
func (s *Server) Detach(c *Client, sid string) {
	c.mu.Lock()
	_, was := c.attached[sid]
	delete(c.attached, sid)
	c.mu.Unlock()
	if was {
		s.SessionEvent(sid, "client_disconnected", map[string]any{"client_id": c.ID, "principal": c.principal})
	}
}

// joined tells event_scope "all" clients that c connected.
func (s *Server) joined(c *Client) {
	ev := map[string]any{"type": "client_joined", "payload": map[string]any{"client": c.Describe("")}}
	for _, o := range s.Clients() {
		if o != c && o.wantsAll() {
			o.push(ev)
		}
	}
}

// left ends c's attachments and tells event_scope "all" clients it is gone.
func (s *Server) left(c *Client) {
	c.mu.Lock()
	var sids []string
	for sid := range c.attached {
		sids = append(sids, sid)
	}
	c.mu.Unlock()
	for _, sid := range sids {
		s.Detach(c, sid)
	}
	s.Emit("client_left", map[string]any{"client_id": c.ID})
}

func whoami(ctx context.Context, c *Client, payload json.RawMessage) (any, error) {
	return map[string]any{"principal": c.principal}, nil
}

// setLabel sets the client's info.label; an empty label removes it.
func setLabel(ctx context.Context, c *Client, payload json.RawMessage) (any, error) {
	var p struct {
		Label string `json:"label"`
	}
	json.Unmarshal(payload, &p)
	c.mu.Lock()
	defer c.mu.Unlock()
	info := map[string]any{}
	for k, v := range c.info {
		info[k] = v
	}
	if p.Label != "" {
		info["label"] = p.Label
	} else {
		delete(info, "label")
	}
	c.info = info
	return map[string]any{}, nil
}

// clientList lists clients, optionally only those attached to session_id
// and those whose info.kind is in kinds.
func (s *Server) clientList(ctx context.Context, c *Client, payload json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
		Kinds     []any  `json:"kinds"`
	}
	json.Unmarshal(payload, &p)
	kinds := map[string]bool{}
	for _, k := range p.Kinds {
		if k, ok := k.(string); ok {
			kinds[k] = true
		}
	}
	out := []map[string]any{}
	for _, o := range s.Clients() {
		if p.SessionID != "" && !o.Attached(p.SessionID) {
			continue
		}
		d := o.Describe("")
		if kind, _ := d["info"].(map[string]any)["kind"].(string); len(kinds) > 0 && !kinds[kind] {
			continue
		}
		out = append(out, d)
	}
	return map[string]any{"clients": out}, nil
}

func (s *Server) detach(ctx context.Context, c *Client, payload json.RawMessage) (any, error) {
	var p struct {
		SessionID string `json:"session_id"`
	}
	json.Unmarshal(payload, &p)
	s.Detach(c, p.SessionID)
	return nil, nil
}
