package bridge

import (
	"context"
	"time"

	"github.com/elonnzhang/herdrex/pkg/herdr/term"
	"github.com/elonnzhang/herdrex/pkg/rex"
	rexsvr "github.com/elonnzhang/herdrex/pkg/rex/server"
)

// Terminal sizes follow Rex's rule: every client reports the size it wants
// with resize, and the terminal uses its owner's (docs/PROTOCOL.md §5.2).

const defaultCellW, defaultCellH = 8, 16

type desired struct {
	client  string
	size    term.Size
	updated time.Time
}

// layoutSize is the pane's size in Herdr's own layout.
func (b *Bridge) layoutSize(ctx context.Context, pane string) term.Size {
	cols, rows := b.paneSize(ctx, pane)
	return term.Size{Cols: uint16(cols), Rows: uint16(rows), CellWidthPx: defaultCellW, CellHeightPx: defaultCellH}
}

// record stores a client's request, newest first.
func (b *Bridge) record(block, client string, s term.Size) {
	b.mu.Lock()
	defer b.mu.Unlock()
	list := []desired{{client, s, time.Now()}}
	for _, d := range b.desired[block] {
		if d.client != client {
			list = append(list, d)
		}
	}
	b.desired[block] = list
}

func (b *Bridge) ownerDesired(block string) (term.Size, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	owner := b.owners[block]
	for _, d := range b.desired[block] {
		if d.client == owner || owner == "" {
			return d.size, true
		}
	}
	return term.Size{}, false
}

func (b *Bridge) clientDesired(block, client string) (term.Size, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, d := range b.desired[block] {
		if d.client == client {
			return d.size, true
		}
	}
	return term.Size{}, false
}

// forget drops a disconnected client's requests.
func (b *Bridge) forget(client string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for block, list := range b.desired {
		out := list[:0]
		for _, d := range list {
			if d.client != client {
				out = append(out, d)
			}
		}
		b.desired[block] = out
	}
}

// applyOwnerSize resizes the pane's terminal to what its owner asked for
// and reports whether the size changed.
func (b *Bridge) applyOwnerSize(sid, block, pane string) bool {
	h := b.hub(pane)
	s, ok := b.ownerDesired(block)
	if h == nil || !ok || h.current() == s {
		return false
	}
	h.apply(s)
	b.sizeChanged(sid, block, s, b.owner(block))
	return true
}

func (b *Bridge) sizeChanged(sid, block string, s term.Size, by string) {
	var owner any
	if by != "" {
		owner = map[string]any{}
	}
	b.blockEvent(sid, block, "size_changed",
		map[string]any{
			"columns":        s.Cols,
			"rows":           s.Rows,
			"cell_width_px":  s.CellWidthPx,
			"cell_height_px": s.CellHeightPx,
			"owner":          owner,
		})
}

func (b *Bridge) blockEvent(sid, block, name string, payload any) {
	b.srv.SessionEvent(sid, "block_event", map[string]any{
		"block_id": block, "creator_name": rex.TerminalCreator, "name": name, "payload": payload,
	})
}

func termResize(ctx context.Context, b *Bridge, c *rexsvr.Client, sid, block, pane string, args P) (any, error) {
	cols, okc := args["columns"].(float64)
	rows, okr := args["rows"].(float64)
	if !okc || !okr || cols < 1 || rows < 1 || cols > 65535 || rows > 65535 {
		return nil, rex.NewError("bad_request", "resize needs columns and rows between 1 and 65535", 400)
	}
	s := term.Size{Cols: uint16(cols), Rows: uint16(rows), CellWidthPx: defaultCellW, CellHeightPx: defaultCellH}
	if prev, ok := b.clientDesired(block, c.ID); ok { // cell size is optional: keep the last one
		s.CellWidthPx, s.CellHeightPx = prev.CellWidthPx, prev.CellHeightPx
	}
	if w, ok := args["cell_width_px"].(float64); ok && w > 0 {
		s.CellWidthPx = uint32(w)
	}
	if h, ok := args["cell_height_px"].(float64); ok && h > 0 {
		s.CellHeightPx = uint32(h)
	}
	b.record(block, c.ID, s)
	resized := false
	if o := b.owner(block); o == "" || o == c.ID {
		resized = b.applyOwnerSize(sid, block, pane)
	}
	// A client reports its size after its own grid changed, which may have
	// cut cells off its screen. Herdr repaints by itself only when the PTY
	// size changes.
	if h := b.hub(pane); h != nil && !resized {
		h.repaint()
	}
	return map[string]any{}, nil
}

func termSize(ctx context.Context, b *Bridge, block, pane string) (any, error) {
	s := b.layoutSize(ctx, pane)
	if h := b.hub(pane); h != nil {
		s = h.current()
	}
	var owner any
	if o := b.owner(block); o != "" {
		owner = o
	}
	b.mu.Lock()
	list := []map[string]any{}
	for _, d := range b.desired[block] {
		list = append(list, map[string]any{
			"client_id":  d.client,
			"columns":    d.size.Cols,
			"rows":       d.size.Rows,
			"updated_at": d.updated.UTC().Format(time.RFC3339Nano),
		})
	}
	b.mu.Unlock()
	return map[string]any{
		"columns":        s.Cols,
		"rows":           s.Rows,
		"cell_width_px":  s.CellWidthPx,
		"cell_height_px": s.CellHeightPx,
		"owner":          owner,
		"desired":        list,
	}, nil
}
