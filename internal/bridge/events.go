package bridge

import (
	"github.com/elonnzhang/herdrex/pkg/herdr/jsonapi"
	"github.com/elonnzhang/herdrex/pkg/rex"
)

// Fine-grained session events, worked out by comparing two snapshots of one
// workspace. The order follows the real Rex server: block_created, then the
// window and label events and block_closed, then session_view_changed. Blocks
// of a closed window are the exception: their block_closed comes after
// session_view_changed (docs/rex-api.md 2.6), unless their program exited,
// which closes the block first and then its window. A new block is followed
// by its first process_changed and, when a client created it, by
// block_owner_changed, as on the real server.

type sessionDiff struct {
	pre, post []map[string]any // events before and after session_view_changed
}

func ev(name string, payload map[string]any) map[string]any {
	return map[string]any{"type": name, "payload": payload}
}

func (b *Bridge) diff(old, cur *jsonapi.Snapshot, wid string) sessionDiff {
	var d sessionDiff
	if old == nil || cur == nil {
		return d
	}
	oldPanes, curPanes := panesIn(old, wid), panesIn(cur, wid)
	for id := range curPanes {
		if _, ok := oldPanes[id]; !ok {
			d.pre = append(d.pre, b.created(id)...)
		}
	}
	oldTabs, curTabs := tabsIn(old, wid), tabsIn(cur, wid)
	for _, t := range cur.Tabs {
		if t.WorkspaceID != wid {
			continue
		}
		w := b.IDs.Rex("window", t.ID)
		if o, ok := oldTabs[t.ID]; !ok {
			d.pre = append(d.pre, ev("window_created", map[string]any{"window_id": w, "label": t.Label}))
		} else if o.Label != t.Label {
			d.pre = append(d.pre, ev("window_label_changed", map[string]any{"window_id": w, "label": t.Label}))
		}
	}
	for id := range oldTabs {
		if _, ok := curTabs[id]; !ok {
			d.pre = append(d.pre, ev("window_closed", map[string]any{"window_id": b.IDs.Rex("window", id)}))
		}
	}
	ow, cw := workspace(old, wid), workspace(cur, wid)
	if ow != nil && cw != nil {
		if ow.ActiveTabID != cw.ActiveTabID && cw.ActiveTabID != "" {
			d.pre = append(d.pre, ev("active_window_changed",
				map[string]any{"window_id": b.IDs.Rex("window", cw.ActiveTabID)}))
		}
		if ow.Label != cw.Label {
			d.pre = append(d.pre, ev("session_label_changed", map[string]any{"label": cw.Label}))
		}
	}
	var exited []map[string]any
	for id, tab := range oldPanes {
		if _, ok := curPanes[id]; ok {
			continue
		}
		e := ev("block_closed", map[string]any{"block_id": b.IDs.Rex("block", id)})
		if b.takeExited(id) { // the program ended: its block closes before its window
			exited = append(exited, e)
		} else if _, open := curTabs[tab]; open {
			d.pre = append(d.pre, e)
		} else {
			d.post = append(d.post, e)
		}
	}
	d.pre = append(exited, d.pre...)
	return d
}

// created is the events for a new pane: block_created, process_changed with
// generation 1 and no foreground yet, and the creator's ownership if any.
func (b *Bridge) created(pane string) []map[string]any {
	block := b.IDs.Rex("block", pane)
	out := []map[string]any{ev("block_created", map[string]any{"block_id": block})}
	b.mu.Lock()
	gen := b.procs[block].gen
	if gen == 0 {
		gen = 1
		b.procs[block] = proc{gen: gen}
	}
	owner := b.owners[block]
	b.mu.Unlock()
	if gen == 1 {
		out = append(out, ev("block_event", map[string]any{"block_id": block,
			"creator_name": rex.TerminalCreator, "name": "process_changed",
			"payload": map[string]any{"generation": 1, "foreground": nil}}))
	}
	if owner != "" {
		out = append(out, ev("block_owner_changed", map[string]any{
			"block_id": block, "owner": owner, "reason": "create"}))
	}
	return out
}

// panesIn maps each pane of the workspace to its tab.
func panesIn(s *jsonapi.Snapshot, wid string) map[string]string {
	out := map[string]string{}
	for _, p := range s.Panes {
		if p.WorkspaceID == wid {
			out[p.ID] = p.TabID
		}
	}
	return out
}

func tabsIn(s *jsonapi.Snapshot, wid string) map[string]jsonapi.Tab {
	out := map[string]jsonapi.Tab{}
	for _, t := range s.Tabs {
		if t.WorkspaceID == wid {
			out[t.ID] = t
		}
	}
	return out
}

func workspace(s *jsonapi.Snapshot, wid string) *jsonapi.Workspace {
	for i := range s.Workspaces {
		if s.Workspaces[i].ID == wid {
			return &s.Workspaces[i]
		}
	}
	return nil
}

// sessionEvent sends one session event to clients attached to the session,
// or for a block_event to those that want block events.
func (b *Bridge) sessionEvent(wid string, e map[string]any) {
	b.srv.SessionEvent(b.IDs.Rex("session", wid), e["type"].(string), e["payload"])
}
