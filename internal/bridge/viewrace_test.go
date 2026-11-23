package bridge

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/elonnzhang/herdrex/pkg/rex"
)

// While Watch is subscribed, session.view joins the last refresh's snapshot
// with each tab's live layout. Herdr may have changed in between: a closed
// tab is left out, and the layout and the blocks keep only the panes both
// know of, until the next refresh. Other layout errors still fail the view.
func TestViewBetweenRefreshes(t *testing.T) {
	f := newFakeHerdr(t)
	f.edit(func(s *fakeState) {
		s.addWorkspace("w1", "one")
		s.addTab("w1", "w1:t1", "w1:p1", "w1:p2")
		s.addTab("w1", "w1:t2", "w1:p3")
	})
	b, conn := newRexBridge(t, f)
	b.watching.Store(true) // as while Watch holds its subscription
	sid := b.IDs.Rex("session", "w1")
	f.edit(func(s *fakeState) {
		delete(s.layouts, "w1:t2")                 // the tab closed
		s.layouts["w1:t1"] = row("w1:p1", "w1:p4") // p2 closed, p4 opened
		s.layouts["w1:t1"].Focused = "w1:p1"
	})
	for _, method := range []string{"session.view", "session.attach"} {
		var r struct {
			rex.View
			Nested *rex.View `json:"view"`
		}
		call(t, conn, method, P{"session_id": sid}, &r)
		v := &r.View
		if r.Nested != nil {
			v = r.Nested
		}
		if len(v.Windows) != 1 || v.Windows[0].WindowID != b.IDs.Rex("window", "w1:t1") {
			t.Fatalf("%s: windows %+v, want only w1:t1", method, v.Windows)
		}
		l := v.Windows[0].Layers[0]
		var blocks []string
		for _, x := range l.Blocks {
			blocks = append(blocks, x.BlockID)
		}
		want := []string{b.IDs.Rex("block", "w1:p1")}
		if l.Layout == nil || l.Layout.BlockID != want[0] || !slices.Equal(blocks, want) {
			t.Errorf("%s: layout %+v with blocks %v, want the leaf and block of w1:p1 only", method, l.Layout, blocks)
		}
	}
	f.edit(func(s *fakeState) { s.layoutErr = "internal_error" })
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := conn.Call(ctx, "session.view", P{"session_id": sid}, nil); err == nil {
		t.Error("session.view hid a layout.export failure")
	}
}
