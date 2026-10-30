package bridge

import (
	"testing"
	"time"

	"github.com/elonnzhang/herdrex/pkg/herdr/jsonapi"
)

// A status record keeps the updated_at of when the bridge first saw the
// status, so program_status and the events agree. A new status, or one that
// comes back after the agent left, gets a new time.
func TestStatusRecordTime(t *testing.T) {
	b := New("/tmp/hx-no-herdr.sock")
	at := func(state string) string {
		t.Helper()
		rec := b.statusRecord(jsonapi.Pane{ID: "w1:p1", AgentStatus: state})
		if rec == nil {
			return ""
		}
		if rec["state"] != state {
			t.Fatalf("record %v, want state %s", rec, state)
		}
		s, _ := rec["updated_at"].(string)
		if _, err := time.Parse(time.RFC3339Nano, s); err != nil {
			t.Fatalf("updated_at %q: %v", s, err)
		}
		return s
	}
	tick := func() { time.Sleep(2 * time.Millisecond) }

	working := at("working")
	tick()
	if again := at("working"); again != working {
		t.Errorf("working again: updated_at %s, want %s", again, working)
	}
	tick()
	done := at("done")
	if done == working {
		t.Errorf("done kept working's updated_at %s", done)
	}
	if got := at("unknown"); got != "" {
		t.Errorf("unknown gave a record with updated_at %s", got)
	}
	tick()
	if back := at("done"); back == done {
		t.Errorf("done after the agent left kept updated_at %s", back)
	}
}
