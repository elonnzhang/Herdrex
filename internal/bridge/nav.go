package bridge

import (
	"context"

	"github.com/elonnzhang/herdrex/pkg/herdr/jsonapi"
	"github.com/elonnzhang/herdrex/pkg/rex"
	rexsvr "github.com/elonnzhang/herdrex/pkg/rex/server"
)

// Navigation and ordering: focus_next/previous_pane and _window,
// focus_direction, session.move and session.move_window.

// activeTab is the session's workspace and its active tab. A session with
// no windows is a conflict, as on the real server.
func (b *Bridge) activeTab(ctx context.Context, sid string) (ws, tab string, s *jsonapi.Snapshot, err error) {
	if ws, err = b.herdrID(sid, "session"); err != nil {
		return
	}
	if s, err = b.fresh(ctx); err != nil {
		return
	}
	for _, w := range s.Workspaces {
		if w.ID == ws {
			tab = w.ActiveTabID
		}
	}
	if tab == "" {
		err = rex.NewError("conflict", "session has no windows", 409)
	}
	return
}

// revision refreshes and returns the workspace's revision. Unlike settle it
// does not bump an unchanged view, so a focus move that went nowhere keeps
// the revision, as on the real server.
func (b *Bridge) revision(ctx context.Context, ws string) int {
	b.refreshLogged(ctx)
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.revs[ws]
}

func leaves(n *jsonapi.LayoutNode, out []string) []string {
	if n == nil {
		return out
	}
	if n.First == nil {
		return append(out, n.PaneID)
	}
	return leaves(n.Second, leaves(n.First, out))
}

// step moves i by d within n items. Without wrap it stops at the ends.
func step(i, d, n int, wrap bool) int {
	j := i + d
	if j < 0 || j >= n {
		if !wrap {
			return i
		}
		j = (j%n + n) % n
	}
	return j
}

// focusPane moves focus through the active window's blocks in layout order.
func focusPane(d int) handler {
	return func(ctx context.Context, b *Bridge, c *rexsvr.Client, p P) (any, error) {
		ws, tab, _, err := b.activeTab(ctx, p.str("session_id"))
		if err != nil {
			return nil, err
		}
		l, err := b.H.Layout(ctx, tab)
		if err != nil {
			return nil, err
		}
		order := leaves(l, nil)
		i := 0
		for k, id := range order {
			if id == l.Focused {
				i = k
			}
		}
		wrap, _ := p["wrap"].(bool)
		if j := step(i, d, len(order), wrap); j != i {
			if err := b.call(ctx, "pane.focus", map[string]any{"pane_id": order[j]}, nil); err != nil {
				return nil, err
			}
		}
		return map[string]any{"revision": b.revision(ctx, ws)}, nil
	}
}

// focusWindow moves to the next or previous window of the session.
func focusWindow(d int) handler {
	return func(ctx context.Context, b *Bridge, c *rexsvr.Client, p P) (any, error) {
		ws, tab, s, err := b.activeTab(ctx, p.str("session_id"))
		if err != nil {
			return nil, err
		}
		var tabs []string
		i := 0
		for _, t := range s.Tabs {
			if t.WorkspaceID == ws {
				if t.ID == tab {
					i = len(tabs)
				}
				tabs = append(tabs, t.ID)
			}
		}
		wrap, _ := p["wrap"].(bool)
		if j := step(i, d, len(tabs), wrap); j != i {
			if err := b.call(ctx, "tab.focus", map[string]any{"tab_id": tabs[j]}, nil); err != nil {
				return nil, err
			}
		}
		return map[string]any{"revision": b.revision(ctx, ws)}, nil
	}
}

// hFocusDirection uses Herdr's neighbor search. With wrap, a move off the
// edge goes to the farthest block on the other side that shares a row or
// column with the focused one.
func hFocusDirection(ctx context.Context, b *Bridge, c *rexsvr.Client, p P) (any, error) {
	dir := p.str("direction")
	if dir != "left" && dir != "right" && dir != "up" && dir != "down" {
		return nil, rex.NewError("bad_request", `direction must be "left", "right", "up" or "down"`, 400)
	}
	ws, tab, s, err := b.activeTab(ctx, p.str("session_id"))
	if err != nil {
		return nil, err
	}
	l, err := b.H.Layout(ctx, tab)
	if err != nil {
		return nil, err
	}
	var r struct {
		Focus struct {
			Changed bool `json:"changed"`
		} `json:"focus"`
	}
	if err := b.call(ctx, "pane.focus_direction", map[string]any{"pane_id": l.Focused, "direction": dir}, &r); err != nil {
		return nil, err
	}
	if wrap, _ := p["wrap"].(bool); wrap && !r.Focus.Changed {
		if t := wrapTarget(s, tab, l.Focused, dir); t != "" {
			if err := b.call(ctx, "pane.focus", map[string]any{"pane_id": t}, nil); err != nil {
				return nil, err
			}
		}
	}
	return map[string]any{"revision": b.revision(ctx, ws)}, nil
}

// wrapTarget is the block a wrapped move from pane in dir lands on, or "".
func wrapTarget(s *jsonapi.Snapshot, tab, pane, dir string) string {
	var panes []jsonapi.LayoutPane
	for _, l := range s.Layouts {
		if l.TabID == tab {
			panes = l.Panes
		}
	}
	var src *jsonapi.Rect
	for i := range panes {
		if panes[i].PaneID == pane {
			src = &panes[i].Rect
		}
	}
	if src == nil {
		return ""
	}
	overlap := func(a0, a1, b0, b1 int) bool { return a0 < b1 && b0 < a1 }
	best, bestKey := "", 0
	for _, x := range panes {
		r := x.Rect
		var ok bool
		var key int // smaller is better
		switch dir {
		case "right":
			ok, key = overlap(r.Y, r.Y+r.Height, src.Y, src.Y+src.Height), r.X
		case "left":
			ok, key = overlap(r.Y, r.Y+r.Height, src.Y, src.Y+src.Height), -(r.X + r.Width)
		case "down":
			ok, key = overlap(r.X, r.X+r.Width, src.X, src.X+src.Width), r.Y
		case "up":
			ok, key = overlap(r.X, r.X+r.Width, src.X, src.X+src.Width), -(r.Y + r.Height)
		}
		if ok && (best == "" || key < bestKey) {
			best, bestKey = x.PaneID, key
		}
	}
	if best == pane {
		return ""
	}
	return best
}

// insertIndex is where Herdr's move must insert id so it ends up just
// before before, or last when before is empty.
func insertIndex(order []string, id, before string) (int, bool) {
	if before == "" {
		return len(order), true
	}
	for i, x := range order {
		if x == before {
			return i, true
		}
	}
	return 0, false
}

func hMove(ctx context.Context, b *Bridge, c *rexsvr.Client, p P) (any, error) {
	ws, err := b.herdrID(p.str("session_id"), "session")
	if err != nil {
		return nil, err
	}
	before := ""
	if id := p.str("before_session_id"); id != "" {
		if before, err = b.herdrID(id, "session"); err != nil {
			return nil, err
		}
	}
	s, err := b.fresh(ctx)
	if err != nil {
		return nil, err
	}
	i, ok := insertIndex(workspaceOrder(s), ws, before)
	if !ok {
		return nil, rex.NewError("session_not_found", "unknown session "+p.str("before_session_id"), 404)
	}
	if err := b.call(ctx, "workspace.move", map[string]any{"workspace_id": ws, "insert_index": i}, nil); err != nil {
		return nil, err
	}
	b.refreshLogged(ctx) // sends session_moved
	return map[string]any{}, nil
}

func hMoveWindow(ctx context.Context, b *Bridge, c *rexsvr.Client, p P) (any, error) {
	ws, tab, s, err := b.activeTab(ctx, p.str("session_id"))
	if err != nil {
		return nil, err
	}
	if id := p.str("window_id"); id != "" {
		if tab, err = b.herdrID(id, "window"); err != nil {
			return nil, err
		}
	}
	before := ""
	if id := p.str("before_window_id"); id != "" {
		if before, err = b.herdrID(id, "window"); err != nil {
			return nil, err
		}
	}
	var order []string
	for _, t := range s.Tabs {
		if t.WorkspaceID == ws {
			order = append(order, t.ID)
		}
	}
	i, ok := insertIndex(order, tab, before)
	if !ok {
		return nil, rex.NewError("not_found", "window is not in the session", 404)
	}
	return b.revised(ctx, ws, "tab.move", map[string]any{"tab_id": tab, "insert_index": i})
}
