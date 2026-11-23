package bridge

import (
	"encoding/json"
	"slices"
	"testing"
)

// session.create sends, after session_created, the new session's root block
// as any new block: block_created, its first process_changed, and the
// creator's ownership.
func TestCreateSessionEvents(t *testing.T) {
	f := newFakeHerdr(t)
	f.edit(func(s *fakeState) {
		s.addWorkspace("w1", "one")
		s.addTab("w1", "w1:t1", "w1:p1")
	})
	b, conn := newRexBridge(t, f)
	var me struct {
		ClientID string `json:"client_id"`
	}
	call(t, conn, "session.attach", P{"session_id": b.IDs.Rex("session", "w1")}, &me)
	drain(t, conn)
	var r struct {
		SessionID string `json:"session_id"`
	}
	call(t, conn, "session.create", P{"label": "two"}, &r)
	evs := drain(t, conn)
	want := []string{"session_created", "block_created", "block_event/process_changed", "block_owner_changed", "session_view_changed"}
	if got := names(evs); !slices.Equal(got, want) {
		t.Fatalf("events %v, want %v", got, want)
	}
	root := b.IDs.Rex("block", "w2:p1")
	var created, owned struct {
		Block  string `json:"block_id"`
		Owner  string `json:"owner"`
		Reason string `json:"reason"`
	}
	json.Unmarshal(evs[1].Payload, &created)
	json.Unmarshal(evs[3].Payload, &owned)
	if created.Block != root || owned.Block != root || owned.Owner != me.ClientID || owned.Reason != "create" {
		t.Errorf("block_created %+v, block_owner_changed %+v; want block %s owned by %s for create",
			created, owned, root, me.ClientID)
	}
}
