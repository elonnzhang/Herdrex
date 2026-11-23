package server_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/elonnzhang/herdrex/pkg/rex"
	"github.com/elonnzhang/herdrex/pkg/rex/client"
	rexsvr "github.com/elonnzhang/herdrex/pkg/rex/server"
)

// stalled connects a client that attaches to the session and then reads
// nothing, like a suspended phone.
func stalled(t *testing.T, s *rexsvr.Server, url string) *websocket.Conn {
	t.Helper()
	ws, _, err := websocket.Dial(ctx(t), url+"/api/control", &websocket.DialOptions{Subprotocols: []string{rex.ControlProtocol}})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ws.CloseNow() })
	ws.SetReadLimit(-1)
	hello := `{"type":"hello","api_revision":18,"api_min_revision":0,"client":{"label":"stalled"}}`
	if err := ws.Write(ctx(t), websocket.MessageText, []byte(hello)); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ws.Read(ctx(t)); err != nil {
		t.Fatal(err)
	}
	if err := ws.Write(ctx(t), websocket.MessageText, []byte(`{"type":"request","id":"1","method":"session.attach"}`)); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		for _, c := range s.Clients() {
			if c.Info()["label"] == "stalled" && c.Attached(sid) {
				return ws
			}
		}
	}
	t.Fatal("stalled client never attached")
	return nil
}

// flood sends n session events of about size bytes each.
func flood(s *rexsvr.Server, n, size int) {
	pad := strings.Repeat("a", size)
	for i := range n {
		s.SessionEvent(sid, "session_view_changed", map[string]any{"revision": i, "pad": pad})
	}
}

// floodEvents and floodSize are 32 MiB in all, more than the 8 MiB event
// backlog plus what the kernel buffers on loopback.
const floodEvents, floodSize = 128, 256 << 10

// A client that stops reading must hold up neither the code that sends events
// nor the other clients, which get every event in seq order. Each event is
// sent once the healthy client has the one before, so only the stalled
// client's queue grows.
func TestSlowClientDoesNotBlockOthers(t *testing.T) {
	s, url := newServer(t)
	stalled(t, s, url)
	ok := dial(t, url, "ok")
	if err := ok.Call(ctx(t), "session.attach", nil, nil); err != nil {
		t.Fatal(err)
	}
	seq := next(t, ok).Seq
	pad := strings.Repeat("a", floodSize)
	var slowest time.Duration
	for i := range floodEvents {
		start := time.Now()
		s.SessionEvent(sid, "session_view_changed", map[string]any{"revision": i, "pad": pad})
		slowest = max(slowest, time.Since(start))
		for {
			e := next(t, ok)
			if e.Seq != seq+1 {
				t.Fatalf("seq %d after %d", e.Seq, seq)
			}
			seq = e.Seq
			if eventName(e) == "client_disconnected" { // the stalled client was closed
				continue
			}
			var se rex.SessionEvent
			json.Unmarshal(e.Payload, &se)
			var p struct {
				Revision int `json:"revision"`
			}
			json.Unmarshal(se.Event.Payload, &p)
			if p.Revision != i {
				t.Fatalf("got revision %d, want %d", p.Revision, i)
			}
			break
		}
	}
	if slowest > 500*time.Millisecond {
		t.Fatalf("the slowest SessionEvent took %v", slowest)
	}
}

// A client whose events back up past 8 MiB is closed with 1008 "event
// backlog exceeded" (docs/rex-api.md 2.6). What it got before then has no
// gap in seq.
func TestEventBacklogCloses(t *testing.T) {
	s, url := newServer(t)
	ws := stalled(t, s, url)
	id := clientID(t, s, "stalled")
	flood(s, floodEvents, floodSize)
	var seq uint64
	for {
		_, msg, err := ws.Read(ctx(t))
		if ce, ok := errors.AsType[websocket.CloseError](err); ok {
			if ce.Code != websocket.StatusPolicyViolation || ce.Reason != "event backlog exceeded" {
				t.Fatalf("closed with %d %q", ce.Code, ce.Reason)
			}
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		var m rex.Response
		json.Unmarshal(msg, &m)
		if m.Type != "event" {
			continue
		}
		if m.Seq != seq+1 {
			t.Fatalf("seq %d after %d", m.Seq, seq)
		}
		seq = m.Seq
	}
	if seq >= floodEvents {
		t.Fatalf("got all %d events, want the backlog to overflow", seq)
	}
	waitGone(t, s, id)
}

// clientID is the ID of the connected client with the label.
func clientID(t *testing.T, s *rexsvr.Server, label string) string {
	t.Helper()
	for _, c := range s.Clients() {
		if c.Info()["label"] == label {
			return c.ID
		}
	}
	t.Fatalf("no client labeled %s", label)
	return ""
}

// An event over the message limit closes the connection with 1009 "event
// exceeds message limit" (docs/rex-api.md 2.7); skipping it would leave a
// gap in seq.
func TestOversizeEventCloses(t *testing.T) {
	s, url := newServer(t)
	c := dial(t, url, "")
	if err := c.Call(ctx(t), "session.attach", nil, nil); err != nil {
		t.Fatal(err)
	}
	next(t, c)
	s.SessionEvent(sid, "session_view_changed", map[string]any{"pad": strings.Repeat("a", rex.MaxMessageBytes)})
	for range c.Events() {
	}
	var ce *client.CloseError
	if err := c.Err(); !errors.As(err, &ce) || ce.Code != int(websocket.StatusMessageTooBig) || ce.Reason != "event exceeds message limit" {
		t.Fatalf("connection ended with %v", err)
	}
}

// Emit sends a top-level event it has no rule for, such as session_created,
// to clients attached to every session, whatever their event scope, and to
// no other.
func TestEmitToAttachAll(t *testing.T) {
	s, url := newServer(t)
	sess, all, one, none := dial(t, url, "sess"), dial(t, url, "all"), dial(t, url, "one"), dial(t, url, "none")
	if err := sess.Call(ctx(t), "session.attach_all", map[string]any{"event_scope": "session"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := all.Call(ctx(t), "session.attach_all", map[string]any{"event_scope": "all"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := one.Call(ctx(t), "session.attach", nil, nil); err != nil {
		t.Fatal(err)
	}
	s.Emit("session_created", map[string]any{"session_id": "session:new"})
	for _, c := range []*client.Conn{sess, all} {
		if e := next(t, c, "session_event"); e.Type != "session_created" {
			t.Fatalf("%s got %s", c.Welcome.Client.ID, e.Type)
		}
	}
	for _, c := range []*client.Conn{one, none} {
		for quiet := time.After(100 * time.Millisecond); ; {
			var e client.Event
			select {
			case e = <-c.Events():
			case <-quiet:
			}
			if e.Type == "" {
				break
			}
			if e.Type == "session_created" {
				t.Fatalf("%s got session_created", c.Welcome.Client.ID)
			}
		}
	}
}
