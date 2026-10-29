package bridge

import (
	"context"

	"github.com/elonnzhang/herdrex/pkg/herdr/jsonapi"
	"github.com/elonnzhang/herdrex/pkg/rex"
	rexsvr "github.com/elonnzhang/herdrex/pkg/rex/server"
)

// Herdr has no notion of block owners, so the bridge keeps them. The owner is
// the client whose size a block follows; Rex.app takes its blocks on attach
// and retries until session.own_block succeeds.

func (b *Bridge) owner(block string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.owners[block]
}

func (b *Bridge) ownerChanged(sid string, ev map[string]any) {
	b.srv.SessionEvent(sid, "block_owner_changed", ev)
}

func hOwnBlock(ctx context.Context, b *Bridge, c *rexsvr.Client, p P) (any, error) {
	sid := p.str("session_id")
	wid, err := b.herdrID(sid, "session")
	if err != nil {
		return nil, err
	}
	mode := p.str("mode")
	if mode != "take" && mode != "request" {
		return nil, rex.NewError("bad_request", `unknown mode "`+mode+`": expected "take" or "request"`, 400)
	}
	raw, _ := p["block_ids"].([]any)
	var ids []string
	for _, x := range raw {
		id, _ := x.(string)
		if _, err := b.herdrID(id, "block"); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if len(raw) == 0 { // no IDs means every block in the session
		snap, err := b.fresh(ctx)
		if err != nil {
			return nil, err
		}
		for _, x := range snap.Panes {
			if x.WorkspaceID == wid {
				ids = append(ids, b.IDs.Rex("block", x.ID))
			}
		}
	}
	out, changed := []map[string]any{}, false
	for _, id := range ids {
		b.mu.Lock()
		prev := b.owners[id]
		set := prev != c.ID && (mode == "take" || prev == "")
		if set {
			b.owners[id] = c.ID
		}
		owner := b.owners[id]
		b.mu.Unlock()
		if set {
			ev := map[string]any{"block_id": id, "owner": c.ID, "reason": mode}
			if prev != "" {
				ev["previous"] = prev
			}
			b.ownerChanged(sid, ev)
			changed = true
			if pane, err := b.herdrID(id, "block"); err == nil {
				b.applyOwnerSize(sid, id, pane)
			}
		}
		out = append(out, map[string]any{"block_id": id, "owner": owner})
	}
	if changed {
		b.bump(wid)
	}
	return map[string]any{"blocks": out}, nil
}

// release drops a disconnected client's blocks so other clients can own them.
func (b *Bridge) release(c *rexsvr.Client) {
	b.mu.Lock()
	var gone []string
	for id, o := range b.owners {
		if o == c.ID {
			gone = append(gone, id)
			delete(b.owners, id)
		}
	}
	b.mu.Unlock()
	touched := map[string]bool{}
	for _, id := range gone {
		pane, _ := b.IDs.Herdr(id)
		wid := b.paneWorkspace(pane)
		if wid == "" {
			continue
		}
		b.ownerChanged(b.IDs.Rex("session", wid), map[string]any{
			"block_id": id,
			"previous": c.ID,
			"reason":   "disconnect",
		})
		touched[wid] = true
	}
	for wid := range touched {
		b.bump(wid)
	}
}

// paneWorkspace finds a pane's workspace in the last polled snapshot.
func (b *Bridge) paneWorkspace(pane string) string {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.snap == nil {
		return ""
	}
	for _, x := range b.snap.Panes {
		if x.ID == pane {
			return x.WorkspaceID
		}
	}
	return ""
}

// setOwners fills blocks[].owner in a view.
func (b *Bridge) setOwners(v *rex.View) {
	for i := range v.Windows {
		for j := range v.Windows[i].Layers {
			bl := v.Windows[i].Layers[j].Blocks
			for k := range bl {
				bl[k].Owner = b.owner(bl[k].BlockID)
			}
		}
	}
}

func hHostsList(ctx context.Context, b *Bridge, c *rexsvr.Client, p P) (any, error) {
	return map[string]any{"hosts": []any{}, "hash": "", "diagnostics": []any{}}, nil
}

// hKeymap reports no server bindings or block actions; Rex.app keeps its
// defaults.
func hKeymap(ctx context.Context, b *Bridge, c *rexsvr.Client, p P) (any, error) {
	return map[string]any{
		"bindings":    []any{},
		"diagnostics": []any{},
		"actions":     []any{},
		"config": map[string]any{
			"path":        "",
			"exists":      false,
			"generation":  1,
			"instance_id": b.IDs.HostID,
			"hash":        "",
		},
	}, nil
}

// hDisownBlock gives up the caller's blocks. Blocks owned by others are left
// alone; the result lists each block with its owner after the call.
func hDisownBlock(ctx context.Context, b *Bridge, c *rexsvr.Client, p P) (any, error) {
	sid := p.str("session_id")
	wid, err := b.herdrID(sid, "session")
	if err != nil {
		return nil, err
	}
	if !c.Attached(sid) {
		return nil, rex.NewError("bad_request", "connection is not attached to this session", 400)
	}
	raw, _ := p["block_ids"].([]any)
	var ids []string
	for _, x := range raw {
		id, _ := x.(string)
		if _, err := b.herdrID(id, "block"); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if len(raw) == 0 {
		snap, err := b.fresh(ctx)
		if err != nil {
			return nil, err
		}
		for _, x := range snap.Panes {
			if x.WorkspaceID == wid {
				ids = append(ids, b.IDs.Rex("block", x.ID))
			}
		}
	}
	out, changed := []map[string]any{}, false
	for _, id := range ids {
		b.mu.Lock()
		mine := b.owners[id] == c.ID
		if mine {
			delete(b.owners, id)
		}
		owner := b.owners[id]
		b.mu.Unlock()
		if mine {
			b.ownerChanged(sid, map[string]any{"block_id": id, "previous": c.ID, "reason": "disown"})
			changed = true
		}
		item := map[string]any{"block_id": id}
		if owner != "" {
			item["owner"] = owner
		}
		out = append(out, item)
	}
	if changed {
		b.bump(wid)
	}
	return map[string]any{"blocks": out}, nil
}

// claim makes c the owner of blocks it just created. The refresh that first
// sees a block reports the ownership with block_created; if that refresh
// already ran, the ownership is reported here.
func (b *Bridge) claim(c *rexsvr.Client, ws string, blocks []string) {
	b.refreshMu.Lock()
	b.mu.Lock()
	var late []string
	for _, id := range blocks {
		if b.owners[id] != "" {
			continue
		}
		b.owners[id] = c.ID
		pane, _ := b.IDs.Herdr(id)
		if b.snap != nil && hasSnapPane(b.snap, pane) {
			late = append(late, id)
		}
	}
	b.mu.Unlock()
	b.refreshMu.Unlock()
	sid := b.IDs.Rex("session", ws)
	for _, id := range late {
		b.ownerChanged(sid, map[string]any{"block_id": id, "owner": c.ID, "reason": "create"})
	}
	if len(late) > 0 {
		b.bump(ws)
	}
}

func hasSnapPane(s *jsonapi.Snapshot, pane string) bool {
	for _, p := range s.Panes {
		if p.ID == pane {
			return true
		}
	}
	return false
}
