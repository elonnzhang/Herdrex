package bridge

import (
	"context"
	"math"
	"time"

	"github.com/google/uuid"

	"github.com/elonnzhang/herdrex/internal/model"
	"github.com/elonnzhang/herdrex/pkg/herdr/jsonapi"
	"github.com/elonnzhang/herdrex/pkg/rex"
	rexsvr "github.com/elonnzhang/herdrex/pkg/rex/server"
)

// Floating layers. Herdr has none, so the bridge keeps them: a floating
// pane stays where it is in Herdr's layout, and the Rex view moves it out
// of the tiled layer into a layer of its own (model.Float). Each layer holds
// one block. Herdr sees no change, so every change here bumps the revision.

// rebalanceWindow is how long after float_block, or after docking a block
// back where it was, a resize_pane from the same client counts as Rex.app's
// automatic rebalance. Rex.app sends one right after floating (it sets the
// split beside the gap to 0.5, on the real server too [实测]) and sometimes
// right after docking, so the tiled blocks lose the widths they had. The
// floating pane never left Herdr's layout, so the bridge keeps the old
// widths by not applying that one resize. A drag by the user sends many
// resizes; only the first, and only this soon, is dropped.
const rebalanceWindow = 500 * time.Millisecond

type floatMark struct {
	client string
	at     time.Time
}

// markReflow notes that client just floated or docked a block in tab.
func (b *Bridge) markReflow(tab, client string) {
	b.mu.Lock()
	b.floated[tab] = floatMark{client: client, at: time.Now()}
	b.mu.Unlock()
}

// rebalance reports, once, whether a resize_pane in tab by client follows
// its float or dock closely enough to be Rex.app's automatic rebalance.
func (b *Bridge) rebalance(tab, client string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	m, ok := b.floated[tab]
	delete(b.floated, tab)
	return ok && m.client == client && time.Since(m.at) <= rebalanceWindow
}

// tabFloats copies a tab's floating layers, back to front.
func (b *Bridge) tabFloats(tab string) []model.Float {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]model.Float(nil), b.floats[tab]...)
}

// pruneFloats drops layers whose pane closed or left its tab. Callers hold mu.
func (b *Bridge) pruneFloats(s *jsonapi.Snapshot) {
	where := map[string]string{}
	for _, p := range s.Panes {
		where[p.ID] = p.TabID
	}
	for tab, fs := range b.floats {
		var keep []model.Float
		for _, f := range fs {
			if where[f.PaneID] == tab {
				keep = append(keep, f)
			}
		}
		if len(keep) == 0 {
			delete(b.floats, tab)
		} else {
			b.floats[tab] = keep
		}
	}
}

// floatOf finds the floating layer of a pane.
func (b *Bridge) floatOf(tab, pane string) (model.Float, bool) {
	for _, f := range b.tabFloats(tab) {
		if f.PaneID == pane {
			return f, true
		}
	}
	return model.Float{}, false
}

// unfloat removes a pane's floating layer, reporting whether it had one.
func (b *Bridge) unfloat(tab, pane string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	fs := b.floats[tab]
	for i, f := range fs {
		if f.PaneID == pane {
			b.floats[tab] = append(fs[:i:i], fs[i+1:]...)
			return true
		}
	}
	return false
}

// currentRev is the workspace's revision, for calls that changed nothing.
func (b *Bridge) currentRev(ws string) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if r := b.revs[ws]; r != 0 {
		return r
	}
	return b.rev0
}

// boundsArg reads bounds as [x, y, w, h] or {x, y, w, h}. Like Rex, the
// rectangle must have positive size and fit inside the window.
func boundsArg(p P) ([4]float64, error) {
	var r [4]float64
	ok := false
	switch v := p["bounds"].(type) {
	case []any:
		if len(v) == 4 {
			ok = true
			for i, x := range v {
				f, isNum := x.(float64)
				ok = ok && isNum
				r[i] = f
			}
		}
	case map[string]any:
		ok = true
		for i, k := range []string{"x", "y", "w", "h"} {
			f, isNum := v[k].(float64)
			ok = ok && isNum
			r[i] = f
		}
	}
	const eps = 1e-9
	for _, x := range r {
		ok = ok && !math.IsNaN(x) && !math.IsInf(x, 0)
	}
	if !ok || r[2] <= 0 || r[3] <= 0 || r[0] < -eps || r[1] < -eps || r[0]+r[2] > 1+eps || r[1]+r[3] > 1+eps {
		return r, rex.NewError("bad_request", "input: validating: bounds must be a finite normalized rectangle "+
			"with positive size contained within the unit square", 400)
	}
	return r, nil
}

func hFloatBlock(ctx context.Context, b *Bridge, c *rexsvr.Client, p P) (any, error) {
	if _, err := b.herdrID(p.str("session_id"), "session"); err != nil {
		return nil, err
	}
	bounds, err := boundsArg(p)
	if err != nil {
		return nil, err
	}
	x, err := b.blockOrFocused(ctx, p)
	if err != nil {
		return nil, err
	}
	if f, ok := b.floatOf(x.TabID, x.ID); ok {
		return nil, rex.NewError("bad_request", "block "+b.IDs.Rex("block", x.ID)+
			" is already in floating layer "+f.LayerID, 400)
	}
	l, err := b.H.Layout(ctx, x.TabID)
	if err != nil {
		return nil, err
	}
	if l.Zoomed { // Rex ends the zoom
		if err := b.call(ctx, "pane.zoom", map[string]any{"pane_id": l.Focused, "mode": "off"}, nil); err != nil {
			return nil, err
		}
	}
	f := model.Float{LayerID: "layer:" + uuid.NewString(), PaneID: x.ID, Bounds: bounds}
	b.mu.Lock()
	b.floats[x.TabID] = append(b.floats[x.TabID], f)
	b.mu.Unlock()
	rev := b.settle(ctx, x.WorkspaceID)
	b.markReflow(x.TabID, c.ID)
	return map[string]any{"layer_id": f.LayerID, "revision": rev}, nil
}

// layerArg finds a floating layer of the session by the payload key. A tiled
// layer, which is a Herdr tab, fails with bad_request in Rex's words for op.
func (b *Bridge) layerArg(p P, key, op string) (ws, tab string, err error) {
	if ws, err = b.herdrID(p.str("session_id"), "session"); err != nil {
		return
	}
	id := p.str(key)
	s := b.snapshot()
	if t, ok := b.IDs.Herdr(id); ok && s != nil {
		for _, x := range s.Tabs {
			if x.ID == t && x.WorkspaceID == ws && b.IDs.Rex("layer", t) == id {
				return ws, t, rex.NewError("bad_request", "cannot "+op+" tiled layer "+id, 400)
			}
		}
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for t, fs := range b.floats {
		for _, f := range fs {
			if f.LayerID == id && s != nil && tabIn(s, t, ws) {
				return ws, t, nil
			}
		}
	}
	return ws, "", rex.NewError("not_found", "layer "+id+" not found", 404)
}

func tabIn(s *jsonapi.Snapshot, tab, ws string) bool {
	for _, t := range s.Tabs {
		if t.ID == tab {
			return t.WorkspaceID == ws
		}
	}
	return false
}

func hResizeLayer(ctx context.Context, b *Bridge, c *rexsvr.Client, p P) (any, error) {
	ws, tab, err := b.layerArg(p, "layer_id", "resize")
	if err != nil {
		return nil, err
	}
	bounds, err := boundsArg(p)
	if err != nil {
		return nil, err
	}
	changed := false
	b.mu.Lock()
	for i, f := range b.floats[tab] {
		if f.LayerID == p.str("layer_id") && f.Bounds != bounds {
			b.floats[tab][i].Bounds, changed = bounds, true
		}
	}
	b.mu.Unlock()
	if !changed {
		return map[string]any{"revision": b.currentRev(ws)}, nil
	}
	return map[string]any{"revision": b.bump(ws)}, nil
}

// restack moves a floating layer to the front (raise) or back, or just
// above or below relative_layer_id.
func restack(raise bool) handler {
	return func(ctx context.Context, b *Bridge, c *rexsvr.Client, p P) (any, error) {
		ws, tab, err := b.layerArg(p, "layer_id", "raise or lower")
		if err != nil {
			return nil, err
		}
		id, rel := p.str("layer_id"), p.str("relative_layer_id")
		if rel != "" {
			if rel == id {
				return nil, rex.NewError("bad_request", "relative_layer_id is the layer itself", 400)
			}
			_, rtab, err := b.layerArg(p, "relative_layer_id", "raise or lower")
			if err != nil {
				return nil, err
			}
			if rtab != tab {
				return nil, rex.NewError("bad_request", "layer "+rel+" is in another window", 400)
			}
		}
		b.mu.Lock()
		old := b.floats[tab]
		var moved model.Float
		rest := []model.Float{}
		for _, f := range old {
			if f.LayerID == id {
				moved = f
			} else {
				rest = append(rest, f)
			}
		}
		at := len(rest) // front
		if !raise {
			at = 0
		}
		for i, f := range rest {
			if f.LayerID == rel {
				at = i
				if raise {
					at = i + 1
				}
			}
		}
		next := append(append(append([]model.Float{}, rest[:at]...), moved), rest[at:]...)
		same := true
		for i := range next {
			same = same && next[i].LayerID == old[i].LayerID
		}
		b.floats[tab] = next
		b.mu.Unlock()
		if same {
			return map[string]any{"revision": b.currentRev(ws)}, nil
		}
		return map[string]any{"revision": b.bump(ws)}, nil
	}
}

// pathTo is the layout path to a pane, false for first and true for second.
func pathTo(n *jsonapi.LayoutNode, pane string) ([]bool, bool) {
	if n == nil {
		return nil, false
	}
	if n.Type == "pane" || n.First == nil {
		return []bool{}, n.PaneID == pane
	}
	if pth, ok := pathTo(n.First, pane); ok {
		return append([]bool{false}, pth...), true
	}
	if pth, ok := pathTo(n.Second, pane); ok {
		return append([]bool{true}, pth...), true
	}
	return nil, false
}

// atSpot reports whether placing pane beside anchor in dir ("right" or
// "down"), before or after it, is where pane already sits: the split
// holding pane runs in dir, has pane on that side, and has anchor on the
// other. Rex can only name one block as the anchor, so any block of the
// other side counts.
func atSpot(n *jsonapi.LayoutNode, pane, anchor, dir string, before bool) bool {
	if n == nil || n.Type == "pane" || n.First == nil {
		return false
	}
	if n.First.Type == "pane" && n.First.PaneID == pane {
		return n.Direction == dir && before && hasPane(n.Second, anchor)
	}
	if n.Second.Type == "pane" && n.Second.PaneID == pane {
		return n.Direction == dir && !before && hasPane(n.First, anchor)
	}
	return atSpot(n.First, pane, anchor, dir, before) || atSpot(n.Second, pane, anchor, dir, before)
}

// splitAbove is the Rex id of the split holding a pane, or "" for a root pane.
func (b *Bridge) splitAbove(ctx context.Context, tab, pane string) string {
	l, err := b.H.Layout(ctx, tab)
	if err != nil {
		return ""
	}
	pth, ok := pathTo(l, pane)
	if !ok || len(pth) == 0 {
		return ""
	}
	return b.IDs.SplitID(tab, pth[:len(pth)-1])
}

// movePane is pane.move, returning the pane's id afterwards.
func (b *Bridge) movePane(ctx context.Context, pane string, dest map[string]any, focus bool) (string, error) {
	var r struct {
		Move struct {
			Pane jsonapi.Pane `json:"pane"`
		} `json:"move_result"`
	}
	if err := b.call(ctx, "pane.move", map[string]any{"pane_id": pane, "destination": dest, "focus": focus}, &r); err != nil {
		return "", err
	}
	if r.Move.Pane.ID == "" {
		return pane, nil
	}
	return r.Move.Pane.ID, nil
}

// hMoveBlock places a block in a new split beside the anchor, which is how
// Rex.app docks a floating block. Herdr's pane.move refuses a move within
// the same tab, so such a move goes through a new tab and back. A floating
// block leaves its layer.
func hMoveBlock(ctx context.Context, b *Bridge, c *rexsvr.Client, p P) (any, error) {
	ws, err := b.herdrID(p.str("session_id"), "session")
	if err != nil {
		return nil, err
	}
	dir := "right"
	switch p.str("direction") {
	case "", "horizontal":
	case "vertical":
		dir = "down"
	default:
		return nil, rex.NewError("bad_request", `direction must be "horizontal" or "vertical"`, 400)
	}
	side := p.str("side")
	if side != "" && side != "before" && side != "after" {
		return nil, rex.NewError("bad_request", `side must be "before" or "after"`, 400)
	}
	ratio := 0.5
	if r, ok := p["ratio"].(float64); ok {
		ratio = r
	}
	if p.str("block_id") == p.str("anchor_block_id") || ratio <= 0 || ratio >= 1 {
		return nil, rex.NewError("bad_request", "need two different blocks and 0 < ratio < 1", 400)
	}
	x, err := b.blockOrFocused(ctx, P{"block_id": p.str("block_id"), "session_id": p.str("session_id")})
	if err != nil {
		return nil, err
	}
	a, err := b.blockOrFocused(ctx, P{"block_id": p.str("anchor_block_id"), "session_id": p.str("session_id")})
	if err != nil {
		return nil, err
	}
	if x.WorkspaceID != ws || a.WorkspaceID != ws {
		return nil, rex.NewError("not_found", "block is not in the session", 404)
	}
	if _, ok := b.floatOf(a.TabID, a.ID); ok {
		return nil, rex.NewError("bad_request", "Herdr floats one block per layer: the anchor floats", 400)
	}
	_, floating := b.floatOf(x.TabID, x.ID)
	if floating && x.TabID == a.TabID {
		if l, err := b.H.Layout(ctx, x.TabID); err == nil && atSpot(l, x.ID, a.ID, dir, side == "before") {
			// A floating pane never left its place in Herdr's layout. Docking
			// it back there only ends the float, which keeps the layout,
			// ratios included, as it was before.
			b.unfloat(x.TabID, x.ID)
			out := map[string]any{
				"created_split_id": b.splitAbove(ctx, x.TabID, x.ID),
				"revision":         b.settle(ctx, ws),
			}
			b.markReflow(x.TabID, c.ID)
			return out, nil
		}
	}
	removed := ""
	if !floating {
		removed = b.splitAbove(ctx, x.TabID, x.ID)
	}
	focus := false
	if l, err := b.H.Layout(ctx, x.TabID); err == nil {
		focus = l.Focused == x.ID
	}
	b.unfloat(x.TabID, x.ID)
	// Hold off refreshes until the pane is in place, so clients never see
	// the passing tab of a same-tab move.
	b.refreshMu.Lock()
	pane, err := b.dock(ctx, x, a, ws, dir, side == "before", ratio, focus)
	b.refreshMu.Unlock()
	if err != nil {
		return nil, err
	}
	out := map[string]any{"created_split_id": b.splitAbove(ctx, a.TabID, pane)}
	if removed != "" {
		out["removed_split_id"] = removed
	}
	out["revision"] = b.settle(ctx, ws)
	return out, nil
}

// dock moves pane x beside anchor a, returning x's pane id afterwards.
func (b *Bridge) dock(ctx context.Context, x, a jsonapi.Pane, ws, dir string, before bool, ratio float64, focus bool) (string, error) {
	pane, err := x.ID, error(nil)
	if x.TabID == a.TabID {
		if pane, err = b.movePane(ctx, pane, map[string]any{"type": "new_tab", "workspace_id": ws}, false); err != nil {
			return "", err
		}
	}
	pane, err = b.movePane(ctx, pane, map[string]any{
		"type": "tab", "tab_id": a.TabID, "split": dir,
		"target_pane_id": a.ID, "ratio": ratio,
	}, focus)
	if err != nil {
		return "", err
	}
	if before {
		err = b.call(ctx, "pane.swap", map[string]any{"source_pane_id": a.ID, "target_pane_id": pane}, nil)
	}
	return pane, err
}

// hCloseLayer closes the layer's block. Refresh drops the layer once the
// pane is gone.
func hCloseLayer(ctx context.Context, b *Bridge, c *rexsvr.Client, p P) (any, error) {
	ws, tab, err := b.layerArg(p, "layer_id", "close")
	if err != nil {
		return nil, err
	}
	for _, f := range b.tabFloats(tab) {
		if f.LayerID == p.str("layer_id") {
			if err := b.call(ctx, "pane.close", map[string]any{"pane_id": f.PaneID}, nil); err != nil {
				return nil, err
			}
		}
	}
	return map[string]any{"revision": b.settle(ctx, ws)}, nil
}
