package server_test

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

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
	if !strings.HasPrefix(w.Client.ID, "client:") || w.APIRevision != rex.APIRevision || w.APIMinRevision != rex.APIMinRevision ||
		w.Limits.MaxMessageBytes != rex.MaxMessageBytes || len(w.Client.ReconnectToken) != 22 {
		t.Fatalf("welcome = %+v", w)
	}
}

func TestWelcomeNegotiatesRevision(t *testing.T) {
	_, url := newServer(t)
	for _, test := range []struct {
		name   string
		hello  string
		accept bool
	}{
		{"current Rex", `{"type":"hello","api_revision":18,"api_min_revision":0}`, true},
		{"limited future client", `{"type":"hello","api_revision":18,"api_min_revision":8}`, false},
		{"older client", `{"type":"hello","api_revision":6}`, false},
		{"missing revisions", `{"type":"hello"}`, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			conn, _, err := websocket.Dial(ctx(t), url+"/api/control", &websocket.DialOptions{Subprotocols: []string{rex.ControlProtocol}})
			if err != nil {
				t.Fatal(err)
			}
			defer conn.CloseNow()
			if err := conn.Write(ctx(t), websocket.MessageText, []byte(test.hello)); err != nil {
				t.Fatal(err)
			}
			_, message, err := conn.Read(ctx(t))
			if !test.accept {
				if closeErr, ok := errors.AsType[websocket.CloseError](err); !ok || closeErr.Code != websocket.StatusPolicyViolation || !strings.HasPrefix(closeErr.Reason, "no common API revision:") {
					t.Fatalf("read error = %v", err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var welcome rex.Welcome
			if err := json.Unmarshal(message, &welcome); err != nil || welcome.Type != "welcome" || welcome.APIRevision != 7 || welcome.APIServerRevision != 7 {
				t.Fatalf("welcome type = %s, revision = %d, server revision = %d, decode error = %v", welcome.Type, welcome.APIRevision, welcome.APIServerRevision, err)
			}
		})
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

// TestPrincipalTransport checks how server.whoami shows a caller on TCP and
// on a Unix socket, which has no network address.
func TestPrincipalTransport(t *testing.T) {
	_, tcpURL := newServer(t)
	dir, err := os.MkdirTemp("/tmp", "hxs") // t.TempDir is too long for a socket path on macOS
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "rex.sock"))
	if err != nil {
		t.Fatal(err)
	}
	hs := &http.Server{Handler: rexsvr.New()}
	go hs.Serve(ln)
	t.Cleanup(func() { hs.Close() })

	for _, tc := range []struct {
		url, transport string
		remote         bool
	}{
		{tcpURL, "tcp", true},
		{"unix://" + ln.Addr().String(), "unix", false},
	} {
		var w struct {
			Principal map[string]any `json:"principal"`
		}
		if err := dial(t, tc.url, "").Call(ctx(t), "server.whoami", nil, &w); err != nil {
			t.Fatal(err)
		}
		_, remote := w.Principal["remote_addr"]
		if w.Principal["kind"] != "anonymous" || w.Principal["transport"] != tc.transport || remote != tc.remote {
			t.Errorf("%s: principal %v, want an anonymous %s caller, remote_addr %v", tc.url, w.Principal, tc.transport, tc.remote)
		}
	}
}

// handshake opens a WebSocket on path at addr, sending host as Host and,
// when not empty, origin as Origin, and returns the response status.
func handshake(t *testing.T, network, addr, path, sub, host, origin string) int {
	t.Helper()
	conn, err := net.DialTimeout(network, addr, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(5 * time.Second))
	req := "GET " + path + " HTTP/1.1\r\nHost: " + host + "\r\nUpgrade: websocket\r\nConnection: Upgrade\r\n" +
		"Sec-WebSocket-Key: " + base64.StdEncoding.EncodeToString(make([]byte, 16)) + "\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Protocol: " + sub + "\r\n"
	if origin != "" {
		req += "Origin: " + origin + "\r\n"
	}
	if _, err := conn.Write([]byte(req + "\r\n")); err != nil {
		t.Fatal(err)
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), nil)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// TestRebindingHost checks that a browser handshake, one with Origin, is
// refused unless Host names an IP address or localhost: a page whose name
// rebinds to this machine sends its own name in both. Native clients send no
// Origin, and a Unix socket's callers may send any Host.
func TestRebindingHost(t *testing.T) {
	_, url := newServer(t)
	addr := strings.TrimPrefix(url, "http://")
	_, port, _ := net.SplitHostPort(addr)
	evil := "evil.example:" + port
	for _, tc := range []struct {
		name, path, sub, host, origin string
		want                          int
	}{
		{"control, rebound name", "/api/control", rex.ControlProtocol, evil, "http://" + evil, http.StatusForbidden},
		{"data, rebound name", "/api/data", rex.DataProtocol, evil, "http://" + evil, http.StatusForbidden},
		{"control, name without Origin", "/api/control", rex.ControlProtocol, evil, "", http.StatusSwitchingProtocols},
		{"control, IP without Origin", "/api/control", rex.ControlProtocol, addr, "", http.StatusSwitchingProtocols},
		{"control, IP", "/api/control", rex.ControlProtocol, addr, "http://" + addr, http.StatusSwitchingProtocols},
		{"data, IP", "/api/data", rex.DataProtocol, addr, "http://" + addr, http.StatusSwitchingProtocols},
		{"control, IPv6", "/api/control", rex.ControlProtocol, "[::1]:" + port, "http://[::1]:" + port, http.StatusSwitchingProtocols},
		{"control, localhost", "/api/control", rex.ControlProtocol, "localhost:" + port, "http://localhost:" + port, http.StatusSwitchingProtocols},
		{"control, a localhost name", "/api/control", rex.ControlProtocol, "rex.localhost:" + port, "http://rex.localhost:" + port, http.StatusSwitchingProtocols},
		{"control, localhost lookalike", "/api/control", rex.ControlProtocol, "evillocalhost:" + port, "http://evillocalhost:" + port, http.StatusForbidden},
	} {
		if got := handshake(t, "tcp", addr, tc.path, tc.sub, tc.host, tc.origin); got != tc.want {
			t.Errorf("%s: %d, want %d", tc.name, got, tc.want)
		}
	}

	dir, err := os.MkdirTemp("/tmp", "hxs")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "rex.sock"))
	if err != nil {
		t.Fatal(err)
	}
	hs := &http.Server{Handler: rexsvr.New()}
	go hs.Serve(ln)
	t.Cleanup(func() { hs.Close() })
	if got := handshake(t, "unix", ln.Addr().String(), "/api/control", rex.ControlProtocol, evil, "http://"+evil); got != http.StatusSwitchingProtocols {
		t.Errorf("Unix socket, rebound name: %d, want %d", got, http.StatusSwitchingProtocols)
	}
}
