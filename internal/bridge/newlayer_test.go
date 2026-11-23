package bridge

import (
	"errors"
	"testing"

	"github.com/elonnzhang/herdrex/pkg/rex"
)

// new_layer splits a Herdr pane off the tab and floats it, so the floating
// block is a Herdr pane: it runs where Herdr runs and takes every pane method.
func TestNewLayer(t *testing.T) {
	f := newFakeHerdr(t)
	f.edit(func(s *fakeState) {
		s.addWorkspace("w1", "one")
		s.addTab("w1", "w1:t1", "w1:p1")
		s.snap.Panes[0].CWD = "/srv/work"
	})
	b, conn := newRexBridge(t, f)
	sid := b.IDs.Rex("session", "w1")
	call(t, conn, "session.attach", P{"session_id": sid}, nil)
	drain(t, conn)

	var r struct {
		LayerID  string   `json:"layer_id"`
		BlockIDs []string `json:"block_ids"`
		Revision int      `json:"revision"`
	}
	call(t, conn, "session.new_layer", P{
		"session_id": sid, "bounds": map[string]any{"x": 0.2, "y": 0.2, "w": 0.6, "h": 0.6}, "focus": true,
		"layout": map[string]any{"block": map[string]any{"flavor": rex.ShellFlavor, "label": "scratch"}},
	}, &r)
	pane := b.IDs.Rex("block", "w1:p2")
	if len(r.BlockIDs) != 1 || r.BlockIDs[0] != pane || r.Revision == 0 {
		t.Fatalf("new_layer = %+v, want block %s", r, pane)
	}
	f.mu.Lock()
	splits := f.st.splits
	f.mu.Unlock()
	if len(splits) != 1 || splits[0]["target_pane_id"] != "w1:p1" || splits[0]["cwd"] != "/srv/work" {
		t.Errorf("pane.split = %v, want a split of w1:p1 in its directory", splits)
	}

	var v rex.View
	call(t, conn, "session.view", P{"session_id": sid}, &v)
	ls := v.Windows[0].Layers
	if len(ls) != 2 || ls[0].Layout == nil || ls[0].Layout.BlockID != b.IDs.Rex("block", "w1:p1") {
		t.Fatalf("layers = %+v, want the tiled pane and the new layer", ls)
	}
	if fl := ls[1]; fl.LayerID != r.LayerID || fl.Kind != "floating" || fl.Bounds != [4]float64{0.2, 0.2, 0.6, 0.6} ||
		len(fl.Blocks) != 1 || fl.Blocks[0].BlockID != pane || fl.Blocks[0].Owner != conn.Welcome.Client.ID {
		t.Errorf("floating layer = %+v", fl)
	}

	// A second layer splits a tiled pane, not the floating one.
	call(t, conn, "session.new_layer", P{
		"session_id": sid, "bounds": []float64{0.1, 0.1, 0.3, 0.3},
		"layout": map[string]any{"block": map[string]any{"flavor": rex.ShellFlavor}},
	}, nil)
	f.mu.Lock()
	splits = f.st.splits
	f.mu.Unlock()
	if len(splits) != 2 || splits[1]["target_pane_id"] != "w1:p1" {
		t.Errorf("second pane.split = %v, want a split of the tiled w1:p1", splits[len(splits)-1])
	}
	call(t, conn, "session.view", P{"session_id": sid}, &v)
	if len(v.Windows[0].Layers) != 3 {
		t.Errorf("layers = %+v, want the tiled one and two floating", v.Windows[0].Layers)
	}
}

func TestNewLayerErrors(t *testing.T) {
	f := newFakeHerdr(t)
	f.edit(func(s *fakeState) {
		s.addWorkspace("w1", "one")
		s.addTab("w1", "w1:t1", "w1:p1")
	})
	b, conn := newRexBridge(t, f)
	sid := b.IDs.Rex("session", "w1")
	shell := map[string]any{"block": map[string]any{"flavor": rex.ShellFlavor}}
	bounds := []float64{0.1, 0.1, 0.5, 0.5}
	for _, tc := range []struct {
		name    string
		payload P
		code    string
		message string
	}{
		{"bounds outside", P{"bounds": []float64{0.5, 0.5, 0.6, 0.4}, "layout": shell}, "bad_request",
			"input: validating: bounds must be a finite normalized rectangle with positive size contained within the unit square"},
		{"no layout", P{"bounds": bounds}, "bad_request",
			`validating input: validating root: required: missing properties: ["layout"]`},
		{"null layout", P{"bounds": bounds, "layout": nil}, "bad_request", "input: validating: layout is required"},
		{"empty layout", P{"bounds": bounds, "layout": map[string]any{}}, "bad_request",
			"input: validating: layout must contain exactly one of block or split"},
		{"split", P{"bounds": bounds, "layout": map[string]any{"split": map[string]any{"direction": "horizontal", "before": shell, "after": shell}}},
			"bad_request", "herdrex floats one block per layer: layout must be a single block"},
		{"flavor", P{"bounds": bounds, "layout": map[string]any{"block": map[string]any{"flavor": "nope.flavor"}}}, "not_found",
			`block flavor not found: "nope.flavor"`},
		{"window", P{"bounds": bounds, "layout": shell, "window_id": b.IDs.Rex("session", "w1")}, "not_found", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.payload["session_id"] = sid
			var e *rex.Error
			err := conn.Call(t.Context(), "session.new_layer", tc.payload, nil)
			if !errors.As(err, &e) || e.Code != tc.code || tc.message != "" && e.Message != tc.message {
				t.Errorf("error = %v, want %s %q", err, tc.code, tc.message)
			}
		})
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.st.splits) != 0 {
		t.Errorf("a refused new_layer split %v", f.st.splits)
	}
}
