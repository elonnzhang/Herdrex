package bridge

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/elonnzhang/herdrex/pkg/rex"
	"github.com/elonnzhang/herdrex/pkg/rex/client"
)

// newRexBridge serves f through a bridge that has made its first refresh,
// and a Rex connection that receives every event.
func newRexBridge(t *testing.T, f *fakeHerdr) (*Bridge, *client.Conn) {
	t.Helper()
	b := New(f.api)
	if err := b.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	conn, _ := rexTestClient(t, b)
	call(t, conn, "session.attach_all", P{"event_scope": "all"}, nil)
	return b, conn
}

func call(t *testing.T, conn *client.Conn, method string, payload, out any) {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	if err := conn.Call(ctx, method, payload, out); err != nil {
		t.Fatalf("%s: %v", method, err)
	}
}

// shown is an event as a client sees it: a session event by its own type,
// a block event as block_event/<name>, with its innermost payload.
type shown struct {
	Name    string
	Payload json.RawMessage
}

// drained is the last event seq drain took from each connection.
var drained = struct {
	sync.Mutex
	seq map[*client.Conn]uint64
}{seq: map[*client.Conn]uint64{}}

// drain returns the events conn has been sent so far, leaving out the
// client events.
func drain(t *testing.T, conn *client.Conn) []shown {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	r, err := conn.Do(ctx, "server.status", nil)
	if err != nil {
		t.Fatal(err)
	}
	var out []shown
	drained.Lock()
	defer drained.Unlock()
	for seq := drained.seq[conn]; seq < r.Seq; {
		var e client.Event
		select {
		case e = <-conn.Events():
		case <-ctx.Done():
			t.Fatalf("events up to seq %d: got to %d", r.Seq, seq)
		}
		seq = e.Seq
		drained.seq[conn] = seq
		s := shown{e.Type, e.Payload}
		if e.Type == "session_event" {
			var se rex.SessionEvent
			json.Unmarshal(e.Payload, &se)
			s = shown{se.Event.Type, se.Event.Payload}
			if se.Event.Type == "block_event" {
				var be rex.BlockEvent
				json.Unmarshal(se.Event.Payload, &be)
				s = shown{"block_event/" + be.Name, be.Payload}
			}
		}
		switch s.Name {
		case "client_joined", "client_left", "client_connected", "client_disconnected":
			continue
		}
		out = append(out, s)
	}
	return out
}

func names(evs []shown) []string {
	out := make([]string, len(evs))
	for i, e := range evs {
		out[i] = e.Name
	}
	return out
}
