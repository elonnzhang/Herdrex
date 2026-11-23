package bridge

import (
	"encoding/json"
	"slices"
	"testing"

	"github.com/elonnzhang/herdrex/pkg/herdr/jsonapi"
)

// The events a Herdr-side change brings, in the order the real server sends
// them (events.go): block_created first, then window and label events and
// block_closed, then session_view_changed, and last the block_closed of a
// window that closed. A block whose program exited closes before its window.
func TestDiffOrder(t *testing.T) {
	setup := func(s *fakeState) {
		s.addWorkspace("w1", "one")
		s.addTab("w1", "w1:t1", "w1:p1", "w1:p2")
		s.addTab("w1", "w1:t2", "w1:p3")
	}
	for _, tc := range []struct {
		name   string
		change func(*fakeState)
		exited string // a pane whose program exited first
		want   []string
	}{
		{"new pane", func(s *fakeState) {
			s.snap.Panes = append(s.snap.Panes, jsonapi.Pane{ID: "w1:p4", TabID: "w1:t1", WorkspaceID: "w1"})
		}, "", []string{"block_created", "block_event/process_changed", "session_view_changed"}},
		{"new tab", func(s *fakeState) {
			s.addTab("w1", "w1:t3", "w1:p4")
		}, "", []string{"block_created", "block_event/process_changed", "window_created", "session_view_changed"}},
		{"closed pane", func(s *fakeState) {
			s.snap.Panes = deletePane(s.snap.Panes, "w1:p2")
		}, "", []string{"block_closed", "session_view_changed"}},
		{"closed tab", func(s *fakeState) {
			s.snap.Tabs = s.snap.Tabs[:1]
			s.snap.Panes = deletePane(s.snap.Panes, "w1:p3")
		}, "", []string{"window_closed", "session_view_changed", "block_closed"}},
		{"exited in its own tab", func(s *fakeState) {
			s.snap.Tabs = s.snap.Tabs[:1]
			s.snap.Panes = deletePane(s.snap.Panes, "w1:p3")
		}, "w1:p3", []string{"block_event/child_exited", "block_closed", "window_closed", "session_view_changed"}},
		{"labels and focus", func(s *fakeState) {
			s.snap.Tabs[0].Label = "renamed"
			s.snap.Workspaces[0].Label = "uno"
			s.snap.Workspaces[0].ActiveTabID = "w1:t2"
		}, "", []string{"window_label_changed", "active_window_changed", "session_label_changed", "session_view_changed"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newFakeHerdr(t)
			f.edit(setup)
			b, conn := newRexBridge(t, f)
			call(t, conn, "session.attach", P{"session_id": b.IDs.Rex("session", "w1")}, nil)
			drain(t, conn)
			if tc.exited != "" {
				b.paneExited(json.RawMessage(`{"pane_id":"` + tc.exited + `"}`))
			}
			f.edit(tc.change)
			if err := b.Refresh(t.Context()); err != nil {
				t.Fatal(err)
			}
			if got := names(drain(t, conn)); !slices.Equal(got, tc.want) {
				t.Errorf("events %v, want %v", got, tc.want)
			}
		})
	}
}

func deletePane(ps []jsonapi.Pane, id string) []jsonapi.Pane {
	return slices.DeleteFunc(ps, func(p jsonapi.Pane) bool { return p.ID == id })
}
