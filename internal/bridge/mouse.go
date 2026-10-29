package bridge

import (
	"context"
	"fmt"
	"math"
	"strings"

	"github.com/elonnzhang/herdrex/herdr/term"
)

// Mouse and wheel block events, following the real server. When the
// program asked for mouse reports (Herdr says so with MouseCapture on a
// controlling terminal connection), events go to Herdr over that connection
// and Herdr encodes them. Otherwise a wheel moves the scrollback with
// pane.scroll, a left-button drag selects text and sends selection_copied,
// and other events are dropped. Without a controlling connection the bridge
// cannot tell whether the program wants reports and treats it as not.

func eventErr(kind, msg string) error {
	return newErr("bad_request", fmt.Sprintf("netapi: decoding args for %q: decoding %s event payload: %s", "block.event", kind, msg), 400)
}

// numField reads an optional number, failing the way Go's JSON decoder does.
func numField(p P, kind, typ, k string) (float64, error) {
	v, ok := p[k]
	if !ok || v == nil {
		return 0, nil
	}
	f, ok := v.(float64)
	if !ok {
		return 0, eventErr(kind, fmt.Sprintf("json: cannot unmarshal %s into Go struct field %s.%s of type float64", jsonKind(v), typ, k))
	}
	return f, nil
}

func jsonKind(v any) string {
	switch v.(type) {
	case string:
		return "string"
	case bool:
		return "bool"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	}
	return "number"
}

func mouseMods(s string) uint8 {
	var m uint8
	for _, x := range strings.Split(s, "+") {
		switch x {
		case "shift":
			m |= term.ModShift
		case "ctrl":
			m |= term.ModCtrl
		case "alt":
			m |= term.ModAlt
		case "super":
			m |= term.ModSuper
		}
	}
	return m
}

func cell(f float64) uint16 { return uint16(min(max(f, 0), math.MaxUint16)) }

var mouseButtons = map[string]uint8{"left": term.ButtonLeft, "right": term.ButtonRight, "middle": term.ButtonMiddle}

// Buttons four to seven are the wheel in xterm's numbering; the real server
// reports a press of four as a wheel-up. Releases of them report nothing.
var wheelButtons = map[string]term.MouseKind{"four": term.ScrollUp, "five": term.ScrollDown,
	"six": term.ScrollLeft, "seven": term.ScrollRight}

// mouseEvent sends a press, release or motion to a program that asked for
// mouse reports. Herdr knows three buttons; events for the others are
// dropped. Otherwise a left-button drag selects text, as on the real server.
func (b *Bridge) mouseEvent(ctx context.Context, pane string, p P) error {
	x, err := numField(p, "mouse", "MouseEvent", "x")
	if err != nil {
		return err
	}
	y, err := numField(p, "mouse", "MouseEvent", "y")
	if err != nil {
		return err
	}
	m := term.Mouse{Column: cell(x), Row: cell(y), Mods: mouseMods(p.str("mods"))}
	button, known := mouseButtons[p.str("button")]
	switch a := p.str("action"); a {
	case "press", "release":
		m.Kind, m.Button = term.MouseDown, button
		if a == "release" {
			m.Kind = term.MouseUp
		} else if k, ok := wheelButtons[p.str("button")]; ok {
			m.Kind, known = k, true
		}
	case "motion":
		m.Kind, known = term.MouseMoved, true
		for _, name := range []string{"left", "middle", "right"} {
			if held, _ := p.obj("buttons")[name].(bool); held {
				m.Kind, m.Button = term.MouseDrag, mouseButtons[name]
				break
			}
		}
	default:
		return eventErr("mouse", fmt.Sprintf("unknown mouse action %q", a))
	}
	if !known {
		return nil
	}
	h := b.hub(pane)
	if h != nil && h.control && h.reportsMouse() {
		return h.conn.Mouse(m)
	}
	if m.Kind <= term.MouseDrag && m.Button == term.ButtonLeft {
		return b.selectDrag(ctx, pane, m)
	}
	return nil
}

// drag is a left-button selection in progress. Rows count from the top of
// the scrollback, as pane.selection.read wants them.
type drag struct {
	top          uint32 // first visible row when the button went down
	row, col     uint32
	toRow, toCol uint32
	moved        bool
}

// selectDrag follows a left-button drag and, on release, reads the selected
// text and sends selection_copied. Like the real server, a drag forward
// includes both end cells and a drag backward leaves out the cell it started
// on; a click without motion selects nothing.
func (b *Bridge) selectDrag(ctx context.Context, pane string, m term.Mouse) error {
	b.mu.Lock()
	d := b.drags[pane]
	b.mu.Unlock()
	switch m.Kind {
	case term.MouseDown:
		top, err := b.viewTop(ctx, pane)
		if err != nil {
			return err
		}
		d = &drag{top: top, row: top + uint32(m.Row), col: uint32(m.Column)}
		d.toRow, d.toCol = d.row, d.col
		b.mu.Lock()
		b.drags[pane] = d
		b.mu.Unlock()
		return nil
	case term.MouseDrag:
		b.mu.Lock()
		if d != nil {
			d.toRow, d.toCol, d.moved = d.top+uint32(m.Row), uint32(m.Column), true
		}
		b.mu.Unlock()
		return nil
	}
	b.mu.Lock()
	delete(b.drags, pane)
	b.mu.Unlock()
	if d == nil || !d.moved {
		return nil
	}
	from, to := [2]uint32{d.row, d.col}, [2]uint32{d.toRow, d.toCol}
	if to[0] < from[0] || to[0] == from[0] && to[1] < from[1] {
		from, to = to, from
		if to[1] > 0 {
			to[1]--
		} else if to[0] > 0 {
			cols, _ := b.paneSize(ctx, pane)
			to = [2]uint32{to[0] - 1, uint32(max(cols-1, 0))}
		}
	}
	cols, _ := b.paneSize(ctx, pane)
	from[1], to[1] = min(from[1], uint32(max(cols-1, 0))), min(to[1], uint32(max(cols-1, 0)))
	var r struct {
		Text string `json:"text"`
	}
	if err := b.call(ctx, "pane.selection.read", map[string]any{"pane_id": pane,
		"anchor": map[string]any{"row": from[0], "col": from[1]},
		"cursor": map[string]any{"row": to[0], "col": to[1]}}, &r); err != nil {
		return nil // nothing selectable there
	}
	sid, ok := b.paneSession(pane)
	if !ok {
		return nil
	}
	ev := map[string]any{"bytes": len(r.Text)}
	if len(r.Text) <= maxClipboardText {
		ev["text"] = r.Text
	}
	b.blockEvent(sid, b.IDs.Rex("block", pane), "selection_copied", ev)
	return nil
}

// viewTop is the scrollback row shown at the top of the pane.
func (b *Bridge) viewTop(ctx context.Context, pane string) (uint32, error) {
	s, err := b.scrollInfo(ctx, pane)
	if err != nil || s == nil {
		return 0, err
	}
	return uint32(max(s.Max-s.Offset, 0)), nil
}

type scrollInfo struct {
	Offset int `json:"offset_from_bottom"`
	Max    int `json:"max_offset_from_bottom"`
}

func (b *Bridge) scrollInfo(ctx context.Context, pane string) (*scrollInfo, error) {
	var r struct {
		Pane struct {
			Scroll *scrollInfo `json:"scroll"`
		} `json:"pane"`
	}
	err := b.call(ctx, "pane.get", map[string]any{"pane_id": pane}, &r)
	return r.Pane.Scroll, err
}

// wheelEvent turns deltas into whole lines, keeping the remainder of pixel
// and fractional deltas for the next event.
func (b *Bridge) wheelEvent(ctx context.Context, pane string, p P) error {
	var d [4]float64
	for i, k := range []string{"delta_x", "delta_y", "x", "y"} {
		v, err := numField(p, "wheel", "WheelEvent", k)
		if err != nil {
			return err
		}
		d[i] = v
	}
	h := b.hub(pane)
	if h != nil && !h.control {
		h = nil
	}
	unit := 1.0
	switch mode := p.str("delta_mode"); mode {
	case "line":
	case "", "pixel":
		unit = 16
		if h != nil {
			if c := h.current().CellHeightPx; c > 0 {
				unit = float64(c)
			}
		}
	case "page":
		rows := 24
		if h != nil && h.current().Rows > 0 {
			rows = int(h.current().Rows)
		}
		unit = 1 / float64(rows)
	default:
		return eventErr("wheel", fmt.Sprintf("unknown wheel delta mode %q", mode))
	}
	b.mu.Lock()
	acc := b.wheel[pane] + d[1]/unit
	lines := math.Trunc(acc)
	if h != nil && h.reportsMouse() && d[1] != 0 {
		// Like the real server, a program gets one report per wheel event,
		// however far it scrolls.
		lines = math.Copysign(1, d[1])
		acc = lines
	}
	b.wheel[pane] = acc - lines
	b.mu.Unlock()
	m := term.Mouse{Column: cell(d[2]), Row: cell(d[3]), Mods: mouseMods(p.str("mods"))}
	if h == nil || !h.reportsMouse() {
		// Herdr would turn a wheel on the alternate screen into arrow keys;
		// the real server sends the program nothing, so only scroll back.
		if lines == 0 {
			return nil
		}
		return b.scrollBack(ctx, pane, int(lines))
	}
	if lines != 0 {
		m.Kind, m.Lines = term.ScrollDown, uint16(min(math.Abs(lines), math.MaxUint16))
		if lines < 0 {
			m.Kind = term.ScrollUp
		}
		if err := h.conn.Mouse(m); err != nil {
			return err
		}
	}
	if d[0] != 0 { // sideways only reaches programs that report the mouse
		m.Kind, m.Lines = term.ScrollRight, 1
		if d[0] < 0 {
			m.Kind = term.ScrollLeft
		}
		return h.conn.Mouse(m)
	}
	return nil
}

// scrollBack moves Herdr's scrollback by lines, down when positive.
func (b *Bridge) scrollBack(ctx context.Context, pane string, lines int) error {
	s, err := b.scrollInfo(ctx, pane)
	if err != nil || s == nil {
		return err
	}
	off := min(max(s.Offset-lines, 0), s.Max)
	if off == s.Offset {
		return nil
	}
	b.mu.Lock()
	b.scrolled[pane] = off > 0
	b.mu.Unlock()
	return b.call(ctx, "pane.scroll", map[string]any{"pane_id": pane, "offset_from_bottom": off}, nil)
}

// unscroll returns a pane the wheel scrolled back to the bottom before input
// reaches it. Typing in Herdr does this itself; input through its API does
// not.
func (b *Bridge) unscroll(ctx context.Context, pane string) {
	b.mu.Lock()
	up := b.scrolled[pane]
	delete(b.scrolled, pane)
	b.mu.Unlock()
	if up {
		b.call(ctx, "pane.scroll", map[string]any{"pane_id": pane, "offset_from_bottom": 0}, nil)
	}
}
