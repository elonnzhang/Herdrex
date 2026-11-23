package bridge

import (
	"errors"
	"testing"

	"github.com/elonnzhang/herdrex/pkg/rex"
)

// A Rex id names one kind of object: the split, layer or session of a tab is
// not its window, so session.focus_window refuses them and leaves Herdr alone.
func TestWindowIDKind(t *testing.T) {
	f := newRecordHerdr(t, func(c herdrCall) string {
		if c.Method == "layout.export" {
			return `{"type":"layout_export","layout":{"focused_pane_id":"w1:p1","root":{"type":"split","direction":"right","ratio":0.5,` +
				`"first":{"type":"pane","pane_id":"w1:p1"},"second":{"type":"pane","pane_id":"w1:p2"}}}}`
		}
		return ""
	})
	b, conn := f.connect(t)
	session := b.IDs.Rex("session", "w1")
	if err := conn.Call(t.Context(), "session.view", map[string]any{"session_id": session}, nil); err != nil {
		t.Fatal(err)
	}
	for name, id := range map[string]string{
		"split":   b.IDs.SplitID("w1:t1", nil),
		"layer":   b.IDs.Rex("layer", "w1:t1"),
		"session": session,
	} {
		f.inputs(0)
		err := conn.Call(t.Context(), "session.focus_window", map[string]any{"session_id": session, "window_id": id}, nil)
		var re *rex.Error
		if !errors.As(err, &re) || re.Code != rex.CodeNotFound {
			t.Errorf("a %s id as window_id: %v, want not_found", name, err)
		}
		if calls := f.inputs(0); len(calls) != 0 {
			t.Errorf("a %s id as window_id sent Herdr %v", name, calls)
		}
	}
	err := conn.Call(t.Context(), "session.focus_window", map[string]any{"session_id": session, "window_id": b.IDs.Rex("window", "w1:t1")}, nil)
	if calls := f.inputs(0); err != nil || len(calls) != 1 || calls[0].Method != "tab.focus" || calls[0].Params["tab_id"] != "w1:t1" {
		t.Errorf("the window id: %v, Herdr got %v", err, calls)
	}
}
