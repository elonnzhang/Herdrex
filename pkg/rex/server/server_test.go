package server_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/elonnzhang/herdrex/pkg/rex"
	"github.com/elonnzhang/herdrex/pkg/rex/client"
	rexsvr "github.com/elonnzhang/herdrex/pkg/rex/server"
	"github.com/elonnzhang/herdrex/pkg/rex/termstream"
)

const sid = "session:test"

// newServer has session.attach, session.attach_all, a "poke" method that
// sends a session event while it runs, and a terminal data source.
func newServer(t *testing.T) (*rexsvr.Server, string) {
	s := rexsvr.New()
	s.Handle("session.attach", func(ctx context.Context, c *rexsvr.Client, p json.RawMessage) (any, error) {
		s.Attach(c, sid)
		return map[string]any{"client_id": c.ID}, nil
	})
	s.Handle("session.attach_all", func(ctx context.Context, c *rexsvr.Client, p json.RawMessage) (any, error) {
		var a struct {
			EventScope string `json:"event_scope"`
		}
		json.Unmarshal(p, &a)
		return map[string]any{"sessions": []any{}}, s.AttachAll(c, a.EventScope)
	})
	s.Handle("poke", func(ctx context.Context, c *rexsvr.Client, p json.RawMessage) (any, error) {
		s.SessionEvent(sid, "session_view_changed", map[string]any{"revision": 2})
		s.SessionEvent(sid, "block_event", map[string]any{"block_id": "block:b", "name": "bell", "payload": map[string]any{}})
		return nil, nil
	})
	s.Handle("fail", func(ctx context.Context, c *rexsvr.Client, p json.RawMessage) (any, error) {
		return nil, errors.New("plain")
	})
	s.Handle("huge", func(ctx context.Context, c *rexsvr.Client, p json.RawMessage) (any, error) {
		return map[string]any{"x": strings.Repeat("a", rex.MaxMessageBytes)}, nil
	})
	s.Data = func(ctx context.Context, c *rexsvr.Client, r rexsvr.DataRequest) (func(context.Context, *rexsvr.DataStream), error) {
		if r.DataRefID != "terminal" {
			return nil, rex.NewError(rex.CodeNotFound, "no data source", 404)
		}
		return func(ctx context.Context, st *rexsvr.DataStream) {
			st.Send(ctx, termstream.BlankSnapshot)
			st.Send(ctx, termstream.Resize(st.Nonce, 80, 24, 8, 16, st.Client.ID))
			st.Send(ctx, []byte("hi"))
			st.Send(ctx, termstream.ChildExited(st.Nonce, 0))
			st.Close(rex.CloseBlockClosed, "block closed")
		}, nil
	}
	hs := httptest.NewServer(s)
	t.Cleanup(hs.Close)
	return s, hs.URL
}

func dial(t *testing.T, url, label string) *client.Conn {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := client.Dial(ctx, url, &client.Options{Kind: "cli", Label: label})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return c
}

// next waits for the next event, skipping the types in skip.
func next(t *testing.T, c *client.Conn, skip ...string) client.Event {
	t.Helper()
	for {
		select {
		case e, ok := <-c.Events():
			if !ok {
				t.Fatalf("events ended: %v", c.Err())
			}
			if !contains(skip, e.Type) {
				return e
			}
		case <-time.After(2 * time.Second):
			t.Fatal("no event")
		}
	}
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// eventName is the type of an event, or of the session event inside it.
func eventName(e client.Event) string {
	if e.Type != "session_event" {
		return e.Type
	}
	var se rex.SessionEvent
	json.Unmarshal(e.Payload, &se)
	return se.Event.Type
}

func TestWelcome(t *testing.T) {
	_, url := newServer(t)
	c := dial(t, url, "")
	w := c.Welcome
	if !strings.HasPrefix(w.Client.ID, "client:") || w.APIRevision != rex.APIRevision ||
		w.Limits.MaxMessageBytes != rex.MaxMessageBytes || len(w.Client.ReconnectToken) != 22 {
		t.Fatalf("welcome = %+v", w)
	}
}

func TestErrors(t *testing.T) {
	_, url := newServer(t)
	c := dial(t, url, "")
	var re *rex.Error
	if err := c.Call(ctx(t), "no.such", nil, nil); !errors.As(err, &re) || re.Code != rex.CodeMethodNotFound || re.Status != 404 {
		t.Fatalf("unknown method: %v", err)
	}
	if err := c.Call(ctx(t), "fail", nil, nil); !errors.As(err, &re) || re.Code != rex.CodeConflict {
		t.Fatalf("plain error: %v", err)
	}
	if err := c.Call(ctx(t), "huge", nil, nil); !errors.As(err, &re) || re.Code != rex.CodeTooLarge {
		t.Fatalf("huge result: %v", err)
	}
	r, err := c.Do(ctx(t), "poke", nil)
	if err != nil || r.Error != nil || string(r.Payload) != "{}" {
		t.Fatalf("nil result = %+v %v", r, err)
	}
}

// TestSeq checks the seq rules: events count from 1 per connection, and a
// response's seq is the last event queued before the request started, so
// the events its handler sends come after it.
func TestSeq(t *testing.T) {
	_, url := newServer(t)
	c := dial(t, url, "")
	r, err := c.Do(ctx(t), "session.attach", nil)
	if err != nil || r.Seq != 0 {
		t.Fatalf("attach = %+v %v", r, err)
	}
	if e := next(t, c); e.Seq != 1 || eventName(e) != "client_connected" {
		t.Fatalf("event = %d %s", e.Seq, eventName(e))
	}
	r, err = c.Do(ctx(t), "poke", nil)
	if err != nil || r.Seq != 1 {
		t.Fatalf("poke = %+v %v", r, err)
	}
	for _, want := range []string{"session_view_changed", "block_event"} {
		if e := next(t, c); e.Seq <= r.Seq || eventName(e) != want {
			t.Fatalf("event %d %s, want %s after seq %d", e.Seq, eventName(e), want, r.Seq)
		}
	}
}

// TestScopes checks who gets which events (docs/rex-api.md 2.5).
func TestScopes(t *testing.T) {
	_, url := newServer(t)
	sess, all, none := dial(t, url, "sess"), dial(t, url, "all"), dial(t, url, "none")
	if err := sess.Call(ctx(t), "session.attach_all", map[string]any{"event_scope": "session"}, nil); err != nil {
		t.Fatal(err)
	}
	if err := all.Call(ctx(t), "session.attach_all", map[string]any{"event_scope": "all"}, nil); err != nil {
		t.Fatal(err)
	}
	var re *rex.Error
	if err := none.Call(ctx(t), "session.attach_all", map[string]any{"event_scope": "x"}, nil); !errors.As(err, &re) || re.Code != rex.CodeBadRequest {
		t.Fatalf("bad scope: %v", err)
	}
	if err := none.Call(ctx(t), "poke", nil, nil); err != nil {
		t.Fatal(err)
	}
	if e := next(t, sess); eventName(e) != "session_view_changed" {
		t.Fatalf("session scope got %s", eventName(e))
	}
	if e := next(t, all); eventName(e) != "session_view_changed" {
		t.Fatalf("all scope got %s", eventName(e))
	}
	if e := next(t, all); eventName(e) != "block_event" {
		t.Fatalf("all scope got %s", eventName(e))
	}
	// A new client is announced to the "all" client only; then each of the
	// other two must see its own next event, not the block_event.
	late := dial(t, url, "late")
	if e := next(t, all); e.Type != "client_joined" {
		t.Fatalf("all scope got %s", e.Type)
	}
	late.Close()
	if e := next(t, all); e.Type != "client_left" {
		t.Fatalf("all scope got %s", e.Type)
	}
	if err := none.Call(ctx(t), "poke", nil, nil); err != nil {
		t.Fatal(err)
	}
	if e := next(t, sess); eventName(e) != "session_view_changed" {
		t.Fatalf("session scope got %s", eventName(e))
	}
	select {
	case e := <-none.Events():
		t.Fatalf("unattached client got %s", eventName(e))
	case <-time.After(50 * time.Millisecond):
	}
}

func TestClients(t *testing.T) {
	_, url := newServer(t)
	a, b := dial(t, url, "a"), dial(t, url, "b")
	if err := b.Call(ctx(t), "session.attach", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := a.Call(ctx(t), "client.set_label", map[string]any{"label": "renamed"}, nil); err != nil {
		t.Fatal(err)
	}
	var l struct {
		Clients []struct {
			ClientID   string         `json:"client_id"`
			Info       map[string]any `json:"info"`
			SessionIDs []string       `json:"session_ids"`
		} `json:"clients"`
	}
	if err := a.Call(ctx(t), "client.list", nil, &l); err != nil {
		t.Fatal(err)
	}
	if len(l.Clients) != 2 || l.Clients[0].Info["label"] != "renamed" || l.Clients[1].SessionIDs[0] != sid {
		t.Fatalf("client.list = %+v", l)
	}
	if err := a.Call(ctx(t), "client.list", map[string]any{"session_id": sid}, &l); err != nil || len(l.Clients) != 1 {
		t.Fatalf("client.list session = %+v %v", l, err)
	}
	var w struct {
		Principal map[string]any `json:"principal"`
	}
	if err := a.Call(ctx(t), "server.whoami", nil, &w); err != nil || w.Principal["kind"] != "anonymous" {
		t.Fatalf("whoami = %+v %v", w, err)
	}
	// b got client_connected when it attached; detaching ends the attachment.
	if err := b.Call(ctx(t), "session.detach", map[string]any{"session_id": sid}, nil); err != nil {
		t.Fatal(err)
	}
	if e := next(t, b); eventName(e) != "client_connected" {
		t.Fatalf("got %s", eventName(e))
	}
	if err := a.Call(ctx(t), "client.list", map[string]any{"session_id": sid}, &l); err != nil || len(l.Clients) != 0 {
		t.Fatalf("client.list after detach = %+v %v", l, err)
	}
}

func TestTerminal(t *testing.T) {
	_, url := newServer(t)
	c := dial(t, url, "")
	var re *rex.Error
	if _, err := c.Terminal(ctx(t), sid, "block:b"); !errors.As(err, &re) || re.Code != rex.CodeConflict {
		t.Fatalf("unattached data.connect: %v", err)
	}
	if err := c.Call(ctx(t), "session.attach", nil, nil); err != nil {
		t.Fatal(err)
	}
	st, err := c.Terminal(ctx(t), sid, "block:b")
	if err != nil {
		t.Fatal(err)
	}
	if st.Nonce == 0 || st.KeepMillis != 60000 {
		t.Fatalf("welcome nonce %d keep %d", st.Nonce, st.KeepMillis)
	}
	var kinds []string
	var output []byte
	for {
		parts, err := st.Read(ctx(t))
		var ce *client.CloseError
		if errors.As(err, &ce) {
			if ce.Code != rex.CloseBlockClosed {
				t.Fatalf("closed with %v", ce)
			}
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range parts {
			if p.Type != "" {
				kinds = append(kinds, p.Type)
			} else {
				output = append(output, p.Output...)
			}
		}
	}
	if strings.Join(kinds, ",") != "resize,child_exited" || string(output) != string(termstream.BlankSnapshot)+"hi" {
		t.Fatalf("messages %v, output %q", kinds, output)
	}
	var tk struct {
		Ticket string `json:"ticket"`
	}
	err = c.Call(ctx(t), "data.connect", map[string]any{"session_id": sid, "block_id": "block:b", "data_ref_id": "other"}, &tk)
	if !errors.As(err, &re) || re.Code != rex.CodeNotFound {
		t.Fatalf("unknown data source: %v", err)
	}
}
