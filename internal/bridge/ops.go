package bridge

import (
	"context"
	"log"
	"net/url"
	"strings"

	"github.com/elonnzhang/herdrinrex/herdr"
	"github.com/elonnzhang/herdrinrex/internal/model"
)

// call wraps a Herdr call, turning Herdr errors into Rex errors.
func (b *Bridge) call(ctx context.Context, method string, params, out any) error {
	err := b.H.Call(ctx, method, params, out)
	if he, ok := err.(*herdr.Error); ok {
		code := "conflict"
		if he.Code == "pane_not_found" || he.Code == "tab_not_found" || he.Code == "workspace_not_found" {
			code = "not_found"
		}
		return newErr(code, he.Message, 400)
	}
	return err
}

// settle refreshes after a change so clients get session_view_changed, and
// returns the workspace's revision for the response. Some changes, such as
// zoom, are not in the snapshot signature, so an unchanged signature still
// bumps the revision.
func (b *Bridge) settle(ctx context.Context, ws string) int {
	b.mu.Lock()
	before := b.revs[ws]
	b.mu.Unlock()
	if err := b.Refresh(ctx); err != nil {
		log.Printf("refresh: %v", err)
	}
	b.mu.Lock()
	r := b.revs[ws]
	_, ok := b.sigs[ws]
	b.mu.Unlock()
	if !ok { // workspace is gone
		return before
	}
	if r == before {
		return b.bump(ws)
	}
	return r
}

// revised runs a Herdr call and answers {revision}.
func (b *Bridge) revised(ctx context.Context, ws, method string, params map[string]any) (any, error) {
	if err := b.call(ctx, method, params, nil); err != nil {
		return nil, err
	}
	return map[string]any{"revision": b.settle(ctx, ws)}, nil
}

// paneOf finds a pane in a fresh snapshot.
func (b *Bridge) paneOf(ctx context.Context, pane string) (herdr.Pane, *herdr.Snapshot, error) {
	s, err := b.fresh(ctx)
	if err != nil {
		return herdr.Pane{}, nil, err
	}
	for _, x := range s.Panes {
		if x.ID == pane {
			return x, s, nil
		}
	}
	return herdr.Pane{}, s, newErr("not_found", "block is not in any window", 404)
}

// blockOrFocused resolves block_id, or the focused pane of the session when it is left out.
func (b *Bridge) blockOrFocused(ctx context.Context, p P) (herdr.Pane, error) {
	if id := p.str("block_id"); id != "" {
		h, err := b.herdrID(id, "block")
		if err != nil {
			return herdr.Pane{}, err
		}
		x, _, err := b.paneOf(ctx, h)
		return x, err
	}
	ws, err := b.herdrID(p.str("session_id"), "session")
	if err != nil {
		return herdr.Pane{}, err
	}
	s, err := b.fresh(ctx)
	if err != nil {
		return herdr.Pane{}, err
	}
	tab := ""
	for _, w := range s.Workspaces {
		if w.ID == ws {
			tab = w.ActiveTabID
		}
	}
	if wid := p.str("window_id"); wid != "" {
		if tab, err = b.herdrID(wid, "window"); err != nil {
			return herdr.Pane{}, err
		}
	}
	l, err := b.H.Layout(ctx, tab)
	if err != nil {
		return herdr.Pane{}, err
	}
	for _, x := range s.Panes {
		if x.ID == l.Focused {
			return x, nil
		}
	}
	return herdr.Pane{}, newErr("conflict", "session has no focused block", 409)
}

func hCreate(ctx context.Context, b *Bridge, c *Client, p P) (any, error) {
	params := map[string]any{"focus": false}
	if l := p.str("label"); l != "" {
		params["label"] = l
	}
	var r struct {
		Workspace herdr.Workspace `json:"workspace"`
		Tab       herdr.Tab       `json:"tab"`
		Root      herdr.Pane      `json:"root_pane"`
	}
	if err := b.call(ctx, "workspace.create", params, &r); err != nil {
		return nil, err
	}
	sid := b.IDs.Rex("session", r.Workspace.ID)
	b.claim(c, r.Workspace.ID, []string{b.IDs.Rex("block", r.Root.ID)})
	rev := b.settle(ctx, r.Workspace.ID)
	return map[string]any{"session_id": sid, "initial_windows": []any{map[string]any{
		"window_id": b.IDs.Rex("window", r.Tab.ID), "block_ids": []string{b.IDs.Rex("block", r.Root.ID)},
		"revision": rev}}}, nil
}

func hDestroy(ctx context.Context, b *Bridge, c *Client, p P) (any, error) {
	w, err := b.herdrID(p.str("session_id"), "session")
	if err != nil {
		return nil, err
	}
	if err := b.call(ctx, "workspace.close", map[string]any{"workspace_id": w}, nil); err != nil {
		return nil, err
	}
	b.settle(ctx, w)
	return map[string]any{}, nil
}

func hSetLabel(ctx context.Context, b *Bridge, c *Client, p P) (any, error) {
	w, err := b.herdrID(p.str("session_id"), "session")
	if err != nil {
		return nil, err
	}
	return b.revised(ctx, w, "workspace.rename", map[string]any{"workspace_id": w, "label": p.str("label")})
}

func hNewWindow(ctx context.Context, b *Bridge, c *Client, p P) (any, error) {
	w, err := b.herdrID(p.str("session_id"), "session")
	if err != nil {
		return nil, err
	}
	layout := p.obj("layout")
	if layout == nil {
		layout = P{"block": map[string]any{"flavor": model.Flavor}}
	}
	if err := checkLayout(layout); err != nil {
		return nil, err
	}
	focus := true
	if f, ok := p["focus"].(bool); ok {
		focus = f
	}
	params := map[string]any{"workspace_id": w, "focus": focus}
	if l := p.str("window_label"); l != "" {
		params["label"] = l
	}
	if cwd := leafCWD(layout); cwd != "" {
		params["cwd"] = cwd
	}
	var r struct {
		Tab  herdr.Tab  `json:"tab"`
		Root herdr.Pane `json:"root_pane"`
	}
	if err := b.call(ctx, "tab.create", params, &r); err != nil {
		return nil, err
	}
	blocks, err := b.fill(ctx, r.Root.ID, layout)
	if err != nil {
		return nil, err
	}
	b.claim(c, w, blocks)
	return map[string]any{"window_id": b.IDs.Rex("window", r.Tab.ID),
		"block_ids": blocks, "revision": b.settle(ctx, w)}, nil
}

// checkLayout validates a Rex layout tree: block nodes need the terminal
// flavor, split nodes need a direction and both sides.
func checkLayout(n P) error {
	if blk := n.obj("block"); blk != nil {
		if f := blk.str("flavor"); f != "" && f != model.Flavor {
			return newErr("not_found", "no creator provides flavor "+f, 404)
		}
		return nil
	}
	sp := n.obj("split")
	if sp == nil {
		return newErr("bad_request", "layout node needs block or split", 400)
	}
	if d := sp.str("direction"); d != "horizontal" && d != "vertical" {
		return newErr("bad_request", `split direction must be "horizontal" or "vertical"`, 400)
	}
	if r, ok := sp["ratio"].(float64); ok && (r <= 0 || r >= 1) {
		return newErr("bad_request", "ratio must be between 0 and 1", 400)
	}
	if sp.obj("before") == nil || sp.obj("after") == nil {
		return newErr("bad_request", "split needs before and after", 400)
	}
	if err := checkLayout(sp.obj("before")); err != nil {
		return err
	}
	return checkLayout(sp.obj("after"))
}

// leafCWD is the cwd option of the layout's first block. A file URL, the
// form pwd reports, is turned back into a path.
func leafCWD(n P) string {
	for n.obj("split") != nil {
		n = n.obj("split").obj("before")
	}
	cwd := n.obj("block").obj("options").str("cwd")
	if u, err := url.Parse(cwd); err == nil && u.Scheme == "file" {
		return u.Path
	}
	return cwd
}

// fill builds a checked layout in the space of pane, which already exists
// and becomes the layout's first block. It returns the blocks in order.
func (b *Bridge) fill(ctx context.Context, pane string, n P) ([]string, error) {
	if blk := n.obj("block"); blk != nil {
		if l := strings.TrimSpace(blk.str("label")); l != "" {
			if err := b.call(ctx, "pane.rename", map[string]any{"pane_id": pane, "label": l}, nil); err != nil {
				return nil, err
			}
		}
		return []string{b.IDs.Rex("block", pane)}, nil
	}
	sp := n.obj("split")
	ratio := 0.5
	if r, ok := sp["ratio"].(float64); ok {
		ratio = r
	}
	other, err := b.splitPane(ctx, pane, sp.str("direction"), ratio, leafCWD(sp.obj("after")), false)
	if err != nil {
		return nil, err
	}
	first, err := b.fill(ctx, pane, sp.obj("before"))
	if err != nil {
		return nil, err
	}
	second, err := b.fill(ctx, other, sp.obj("after"))
	return append(first, second...), err
}

// splitPane puts a new pane right of (horizontal) or below (vertical) pane.
// Herdr's ratio, like Rex's, is the share of the first part.
func (b *Bridge) splitPane(ctx context.Context, pane, direction string, ratio float64, cwd string, focus bool) (string, error) {
	dir := "down"
	if direction == "horizontal" {
		dir = "right"
	}
	params := map[string]any{"target_pane_id": pane, "direction": dir, "focus": focus, "ratio": ratio}
	if cwd != "" {
		params["cwd"] = cwd
	}
	var r struct {
		Pane herdr.Pane `json:"pane"`
	}
	err := b.call(ctx, "pane.split", params, &r)
	return r.Pane.ID, err
}

// tabOp runs a tab method on window_id, or on the active window when it is left out.
func tabOp(method string, withLabel bool) handler {
	return func(ctx context.Context, b *Bridge, c *Client, p P) (any, error) {
		ws, err := b.herdrID(p.str("session_id"), "session")
		if err != nil {
			return nil, err
		}
		var t string
		if wid := p.str("window_id"); wid != "" {
			if t, err = b.herdrID(wid, "window"); err != nil {
				return nil, err
			}
		} else {
			s, err := b.fresh(ctx)
			if err != nil {
				return nil, err
			}
			for _, w := range s.Workspaces {
				if w.ID == ws {
					t = w.ActiveTabID
				}
			}
		}
		params := map[string]any{"tab_id": t}
		if withLabel {
			params["label"] = p.str("label")
		}
		return b.revised(ctx, ws, method, params)
	}
}

func hFocusBlock(ctx context.Context, b *Bridge, c *Client, p P) (any, error) {
	x, err := b.blockOrFocused(ctx, p)
	if err != nil {
		return nil, err
	}
	if b.call(ctx, "pane.focus", map[string]any{"pane_id": x.ID}, nil) != nil {
		if err := b.call(ctx, "tab.focus", map[string]any{"tab_id": x.TabID}, nil); err != nil {
			return nil, err
		}
	}
	return map[string]any{"revision": b.settle(ctx, x.WorkspaceID)}, nil
}

// hNewSplit splits the anchor block and builds layout in the new part.
// Herdr always puts the new pane right of or below the anchor, so side
// "before" swaps the two panes before the layout is built.
func hNewSplit(ctx context.Context, b *Bridge, c *Client, p P) (any, error) {
	if p.str("anchor_id") != "" && p.str("window_id") != "" {
		return nil, newErr("bad_request", "send anchor_id or window_id, not both", 400)
	}
	if a := p.str("anchor_id"); strings.HasPrefix(a, "split:") {
		return nil, newErr("bad_request", "Herdr can only split a block, not a split", 400)
	}
	if d := p.str("direction"); d != "horizontal" && d != "vertical" {
		return nil, newErr("bad_request", `direction must be "horizontal" or "vertical"`, 400)
	}
	if s := p.str("side"); s != "before" && s != "after" {
		return nil, newErr("bad_request", `side must be "before" or "after"`, 400)
	}
	layout := p.obj("layout")
	if layout == nil {
		return nil, newErr("bad_request", "layout is required", 400)
	}
	if err := checkLayout(layout); err != nil {
		return nil, err
	}
	ratio := 0.5
	if r, ok := p["ratio"].(float64); ok {
		if r <= 0 || r >= 1 {
			return nil, newErr("bad_request", "ratio must be between 0 and 1", 400)
		}
		ratio = r
	}
	anchor, err := b.blockOrFocused(ctx, P{"block_id": p.str("anchor_id"), "session_id": p.str("session_id"),
		"window_id": p.str("window_id")})
	if err != nil {
		return nil, err
	}
	focus, _ := p["focus"].(bool)
	pane, err := b.splitPane(ctx, anchor.ID, p.str("direction"), ratio, leafCWD(layout), focus)
	if err != nil {
		return nil, err
	}
	if p.str("side") == "before" {
		if err := b.call(ctx, "pane.swap", map[string]any{"source_pane_id": anchor.ID, "target_pane_id": pane}, nil); err != nil {
			return nil, err
		}
	}
	blocks, err := b.fill(ctx, pane, layout)
	if err != nil {
		return nil, err
	}
	b.claim(c, anchor.WorkspaceID, blocks)
	return map[string]any{"anchor_id": b.IDs.Rex("block", anchor.ID), "block_ids": blocks,
		"split_ids": []string{}, "revision": b.settle(ctx, anchor.WorkspaceID)}, nil
}

func hBlockClose(ctx context.Context, b *Bridge, c *Client, p P) (any, error) {
	x, err := b.blockOrFocused(ctx, p)
	if err != nil {
		return nil, err
	}
	if err := b.call(ctx, "pane.close", map[string]any{"pane_id": x.ID}, nil); err != nil {
		return nil, err
	}
	return map[string]any{"block_id": b.IDs.Rex("block", x.ID), "revision": b.settle(ctx, x.WorkspaceID)}, nil
}

func hSetBlockLabel(ctx context.Context, b *Bridge, c *Client, p P) (any, error) {
	if p.str("block_id") == "" {
		return nil, newErr("bad_request", "block_id is required", 400)
	}
	x, err := b.blockOrFocused(ctx, p)
	if err != nil {
		return nil, err
	}
	params := map[string]any{"pane_id": x.ID}
	if l := strings.TrimSpace(p.str("label")); l != "" { // no label clears Herdr's manual label
		params["label"] = l
	}
	return b.revised(ctx, x.WorkspaceID, "pane.rename", params)
}

func hZoom(ctx context.Context, b *Bridge, c *Client, p P) (any, error) {
	x, err := b.blockOrFocused(ctx, p)
	if err != nil {
		return nil, err
	}
	mode := "toggle"
	if z, ok := p["zoom"].(bool); ok {
		mode = map[bool]string{true: "on", false: "off"}[z]
	}
	var r struct {
		Zoom struct {
			Zoomed bool `json:"zoomed"`
		} `json:"zoom"`
	}
	if err := b.call(ctx, "pane.zoom", map[string]any{"pane_id": x.ID, "mode": mode}, &r); err != nil {
		return nil, err
	}
	return map[string]any{"block_id": b.IDs.Rex("block", x.ID), "zoomed": r.Zoom.Zoomed,
		"revision": b.settle(ctx, x.WorkspaceID)}, nil
}

func hSwap(ctx context.Context, b *Bridge, c *Client, p P) (any, error) {
	a, err := b.herdrID(p.str("block_id"), "block")
	if err != nil {
		return nil, err
	}
	o, err := b.herdrID(p.str("other_block_id"), "block")
	if err != nil {
		return nil, err
	}
	x, _, err := b.paneOf(ctx, a)
	if err != nil {
		return nil, err
	}
	return b.revised(ctx, x.WorkspaceID, "pane.swap", map[string]any{"source_pane_id": a, "target_pane_id": o})
}

// placement is a list_blocks item. Like Rex, it leaves owner out when the
// block has none.
func (b *Bridge) placement(p herdr.Pane) map[string]any {
	blk := b.IDs.Block(p)
	m := map[string]any{"placement": "placed", "window_id": b.IDs.Rex("window", p.TabID),
		"block_id": blk.BlockID, "label": blk.Label, "creator_name": blk.CreatorName,
		"flavor": blk.Flavor, "host_id": model.HostID}
	if o := b.owner(blk.BlockID); o != "" {
		m["owner"] = o
	}
	return m
}

func hListBlocks(ctx context.Context, b *Bridge, c *Client, p P) (any, error) {
	w, err := b.herdrID(p.str("session_id"), "session")
	if err != nil {
		return nil, err
	}
	s, err := b.fresh(ctx)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, x := range s.Panes {
		if x.WorkspaceID == w {
			out = append(out, b.placement(x))
		}
	}
	return map[string]any{"blocks": out}, nil
}

func hDescribeBlock(ctx context.Context, b *Bridge, c *Client, p P) (any, error) {
	pane, err := b.herdrID(p.str("block_id"), "block")
	if err != nil {
		return nil, err
	}
	s, err := b.fresh(ctx)
	if err != nil {
		return nil, err
	}
	for _, x := range s.Panes {
		if x.ID == pane {
			return map[string]any{"block": b.placement(x), "api": model.CreatorInfo()["api"]}, nil
		}
	}
	return nil, newErr("not_found", "block not found", 404)
}

// hasPane reports whether the subtree holds the pane.
func hasPane(n *herdr.LayoutNode, pane string) bool {
	if n == nil {
		return false
	}
	if n.Type == "pane" || n.First == nil {
		return n.PaneID == pane
	}
	return hasPane(n.First, pane) || hasPane(n.Second, pane)
}

// splitPath finds the innermost split in dir with before in its first
// side and after in its second. The path uses false for first, true for second.
func splitPath(n *herdr.LayoutNode, dir, before, after string) ([]bool, bool) {
	if n == nil || n.First == nil {
		return nil, false
	}
	if pth, ok := splitPath(n.First, dir, before, after); ok {
		return append([]bool{false}, pth...), true
	}
	if pth, ok := splitPath(n.Second, dir, before, after); ok {
		return append([]bool{true}, pth...), true
	}
	if n.Direction == dir && hasPane(n.First, before) && hasPane(n.Second, after) {
		return []bool{}, true
	}
	return nil, false
}

// hResizePane moves a border between two blocks. Rex names the border by
// the blocks on each side. Herdr names it by a path in the layout tree.
func hResizePane(ctx context.Context, b *Bridge, c *Client, p P) (any, error) {
	before, err := b.herdrID(p.str("before_block_id"), "block")
	if err != nil {
		return nil, err
	}
	after, err := b.herdrID(p.str("after_block_id"), "block")
	if err != nil {
		return nil, err
	}
	ratio := p.num("ratio")
	if before == after || ratio <= 0 || ratio >= 1 {
		return nil, newErr("bad_request", "need two different blocks and 0 < ratio < 1", 400)
	}
	s, err := b.fresh(ctx)
	if err != nil {
		return nil, err
	}
	var tab, ws string
	for _, pn := range s.Panes {
		if pn.ID == before {
			tab, ws = pn.TabID, pn.WorkspaceID
		}
	}
	if tab == "" {
		return nil, newErr("not_found", "block is not in any window", 404)
	}
	root, err := b.H.Layout(ctx, tab)
	if err != nil {
		return nil, err
	}
	dir := "down"
	if p.str("direction") == "horizontal" {
		dir = "right"
	}
	pth, ok := splitPath(root, dir, before, after)
	if !ok {
		return nil, newErr("bad_request", "no split between these blocks", 400)
	}
	if err := b.call(ctx, "layout.set_split_ratio", map[string]any{"tab_id": tab, "path": pth, "ratio": ratio}, nil); err != nil {
		return nil, err
	}
	return map[string]any{"revision": b.bump(ws)}, nil
}
