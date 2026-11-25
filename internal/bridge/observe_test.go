package bridge

import (
	"bytes"
	"slices"
	"testing"
)

// When another Herdr client controls the pane, the hub only observes: its
// size stays the layout's and resize only records the request. Herdr renders
// an observer's frames at the size the observer last sent, so the bridge
// never sends it the owner's.
func TestObserverKeepsLayoutSize(t *testing.T) {
	f, b, conn, sid := hubSetup(t)
	f.edit(func(s *fakeState) { s.external["w1:p1"] = true })
	block := b.IDs.Rex("block", "w1:p1")
	resize := func(cols, rows float64) {
		call(t, conn, "block.method", P{"session_id": sid, "block_id": block, "method": "resize",
			"args": P{"columns": cols, "rows": rows}}, nil)
	}
	resize(120, 40) // before the hub starts
	s := openTerm(t, conn, sid, block)
	if refused := nextTerm(t, f); !refused.control {
		t.Fatal("the hub did not try to control the pane first")
	}
	ft := nextTerm(t, f)
	if ft.control || ft.hello != cells(80, 24) {
		t.Errorf("observer: control %v at %+v, want observing at the layout's 80x24", ft.control, ft.hello)
	}
	for _, part := range readUntil(t, s, shows("hi")) {
		if bytes.Contains(part.Output, []byte("\x1b[?1002h")) || bytes.Contains(part.Output, []byte("\x1b[?1003h")) {
			t.Fatal("observer asked Rex to forward mouse input")
		}
	}
	drain(t, conn)

	resize(100, 30)
	resize(100, 30)
	for range 2 {
		if m := nextMsg(t, ft); m.tag != tagResize || m.size != cells(80, 24) {
			t.Errorf("resize sent the observer %+v, want a repaint at 80x24", m)
		}
	}
	var got struct {
		Columns, Rows int
	}
	call(t, conn, "block.method", P{"session_id": sid, "block_id": block, "method": "size"}, &got)
	if got.Columns != 80 || got.Rows != 24 {
		t.Errorf("size %dx%d, want 80x24", got.Columns, got.Rows)
	}
	if slices.Contains(names(drain(t, conn)), "block_event/size_changed") {
		t.Error("an observing hub reported size_changed")
	}
}
