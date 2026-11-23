package bridge

import (
	"slices"
	"testing"

	"github.com/elonnzhang/herdrex/pkg/rex"
)

// session.bootstrap issues data tickets for the visible blocks only: those
// of the window each session shows (Rex's active_window_id), and of those
// only the zoomed block when one is zoomed.
func TestBootstrapTicketsVisibleOnly(t *testing.T) {
	f := newFakeHerdr(t)
	f.edit(func(s *fakeState) {
		s.addWorkspace("w1", "one")
		s.addTab("w1", "w1:t1", "w1:p1", "w1:p2")
		s.addTab("w1", "w1:t2", "w1:p3")
	})
	b, _ := newRexBridge(t, f)
	request := P{"creator_name": rex.TerminalCreator, "data_ref_id": "terminal"}
	bootstrap := func() []string {
		conn, _ := rexTestClient(t, b)
		var r struct {
			Tickets []bootstrapTicket `json:"data_tickets"`
		}
		call(t, conn, "session.bootstrap", P{"event_scope": "session", "data_tickets": []any{request}}, &r)
		var out []string
		for _, x := range r.Tickets {
			pane, _ := b.IDs.Herdr(x.BlockID)
			out = append(out, pane)
		}
		slices.Sort(out)
		return out
	}
	if got, want := bootstrap(), []string{"w1:p1", "w1:p2"}; !slices.Equal(got, want) {
		t.Errorf("tickets for %v, want the active window's %v", got, want)
	}
	f.edit(func(s *fakeState) { s.layouts["w1:t1"].Zoomed = true })
	if got, want := bootstrap(), []string{"w1:p1"}; !slices.Equal(got, want) {
		t.Errorf("zoomed: tickets for %v, want the zoomed %v", got, want)
	}
}
