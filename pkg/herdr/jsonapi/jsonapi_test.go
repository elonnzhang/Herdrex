package jsonapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// received is a request line as the fake server read it.
type received struct {
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
	line   string
}

// conn is one connection to the fake server, after its request line.
type conn struct {
	net.Conn
	r   *bufio.Reader
	req received
}

func (c *conn) send(line string) { io.WriteString(c.Conn, line+"\n") }

// result replies with v, compacted onto one line.
func (c *conn) result(v string) {
	var b bytes.Buffer
	if err := json.Compact(&b, []byte(v)); err != nil {
		panic(err)
	}
	c.send(`{"id":` + jstr(c.req.ID) + `,"result":` + b.String() + `}`)
}

func (c *conn) fail(code, msg string) {
	c.send(`{"id":` + jstr(c.req.ID) + `,"error":{"code":` + jstr(code) + `,"message":` + jstr(msg) + `}}`)
}

// hold reads until the client closes the connection, for at most 3 s. It
// returns what the client sent after its request line, and an error if the
// client kept the connection open.
func (c *conn) hold() (string, error) {
	c.SetReadDeadline(time.Now().Add(3 * time.Second))
	b, err := io.ReadAll(c.r)
	return string(b), err
}

// eof checks that the client closes the connection within 500 ms without
// sending more.
func (c *conn) eof() error {
	c.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	b, err := io.ReadAll(c.r)
	if err != nil {
		return fmt.Errorf("client kept the connection open: %w", err)
	}
	if len(b) > 0 {
		return fmt.Errorf("client sent %q after the request line", b)
	}
	return nil
}

func jstr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// fake is a Herdr JSON API server on a Unix socket. For each connection it
// reads the request line, records it, runs handle and then closes. Like
// Herdr, it closes without a reply when the line is over MaxRequest.
type fake struct {
	sock     string
	accepted atomic.Int32
	mu       sync.Mutex
	reqs     []received
}

func newFake(t *testing.T, handle func(c *conn)) *fake {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "hxj") // t.TempDir is too long for a macOS socket path
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	f := &fake{sock: filepath.Join(dir, "herdr.sock")}
	ln, err := net.Listen("unix", f.sock)
	if err != nil {
		t.Fatal(err)
	}
	var wg sync.WaitGroup
	t.Cleanup(wg.Wait)
	t.Cleanup(func() { ln.Close() })
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			nc, err := ln.Accept()
			if err != nil {
				return
			}
			f.accepted.Add(1)
			wg.Add(1)
			go func() {
				defer wg.Done()
				defer nc.Close()
				c := &conn{Conn: nc, r: bufio.NewReader(nc)}
				line, err := c.r.ReadString('\n')
				if err != nil || len(line) > MaxRequest+1 {
					return
				}
				c.req.line = line
				if err := json.Unmarshal([]byte(line), &c.req); err != nil {
					t.Errorf("request %q: %v", line, err)
				}
				f.mu.Lock()
				f.reqs = append(f.reqs, c.req)
				f.mu.Unlock()
				handle(c)
			}()
		}
	}()
	return f
}

func (f *fake) client() *Client { return &Client{Socket: f.sock} }

func (f *fake) requests() []received {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]received(nil), f.reqs...)
}

// only returns the one request the fake read.
func (f *fake) only(t *testing.T) received {
	t.Helper()
	rs := f.requests()
	if len(rs) != 1 {
		t.Fatalf("server read %d requests, want 1", len(rs))
	}
	return rs[0]
}

func testCtx(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func TestCallResult(t *testing.T) {
	f := newFake(t, func(c *conn) {
		c.result(`{"type":"tab_info","tab":{"tab_id":"w1:t2","workspace_id":"w1","number":2,"label":"logs","focused":false,"pane_count":1,"agent_status":"idle"}}`)
	})
	var out struct {
		Type string `json:"type"`
		Tab  Tab    `json:"tab"`
	}
	if err := f.client().Call(testCtx(t), "tab.get", map[string]any{"tab_id": "w1:t2"}, &out); err != nil {
		t.Fatal(err)
	}
	if want := (Tab{ID: "w1:t2", WorkspaceID: "w1", Label: "logs"}); out.Type != "tab_info" || out.Tab != want {
		t.Errorf("got %+v", out)
	}
	if r := f.only(t); r.Method != "tab.get" || string(r.Params) != `{"tab_id":"w1:t2"}` {
		t.Errorf("request %s", r.line)
	}
}

func TestCallNilOut(t *testing.T) {
	f := newFake(t, func(c *conn) { c.result(`{"type":"ok"}`) })
	if err := f.client().Call(testCtx(t), "tab.close", map[string]any{"tab_id": "w1:t2"}, nil); err != nil {
		t.Fatal(err)
	}
}

func TestCallNilParams(t *testing.T) {
	f := newFake(t, func(c *conn) { c.result(`{"type":"ok"}`) })
	var nilMap map[string]any // a typed nil marshals to null, which Herdr rejects
	for _, p := range []any{nil, nilMap} {
		if err := f.client().Call(testCtx(t), "ping", p, nil); err != nil {
			t.Fatal(err)
		}
	}
	for _, r := range f.requests() {
		if string(r.Params) != "{}" {
			t.Errorf("params = %s, want {}", r.Params)
		}
	}
}

func TestCallOneLinePerConnection(t *testing.T) {
	rest := make(chan error, 3)
	f := newFake(t, func(c *conn) {
		c.result(`{"type":"pong","version":"0.9.5","protocol":22}`)
		rest <- c.eof()
	})
	cl := f.client()
	for range 3 {
		if err := cl.Call(testCtx(t), "ping", nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if n := f.accepted.Load(); n != 3 {
		t.Errorf("3 calls used %d connections", n)
	}
	ids := map[string]bool{}
	for _, r := range f.requests() {
		if !strings.HasSuffix(r.line, "}\n") || r.Method != "ping" {
			t.Errorf("request line %q", r.line)
		}
		if r.ID == "" || ids[r.ID] {
			t.Errorf("id %q is empty or reused", r.ID)
		}
		ids[r.ID] = true
	}
	for range 3 {
		if err := <-rest; err != nil {
			t.Error(err)
		}
	}
}

func TestCallCloses(t *testing.T) {
	for name, reply := range map[string]string{
		"result":    `{"id":"b1","result":{"type":"ok"}}`,
		"error":     `{"id":"b1","error":{"code":"pane_not_found","message":"pane not found"}}`,
		"no result": `{"id":"b1"}`,
		"not json":  `herdr`,
	} {
		t.Run(name, func(t *testing.T) {
			closed := make(chan error, 1)
			f := newFake(t, func(c *conn) {
				c.send(reply)
				closed <- c.eof()
			})
			// Background never ends, so only Call itself can close the connection.
			f.client().Call(context.Background(), "ping", nil, nil)
			if err := <-closed; err != nil {
				t.Error(err)
			}
		})
	}
}

// countCtx is a context that never ends and counts the context.AfterFunc
// registrations on it that were not stopped. context.AfterFunc uses a
// context's own AfterFunc method when it has one and is not a stdlib
// cancelable context.
type countCtx struct {
	context.Context
	done          chan struct{}
	added, active atomic.Int32
}

func newCountCtx() *countCtx {
	return &countCtx{Context: context.Background(), done: make(chan struct{})}
}

func (c *countCtx) Done() <-chan struct{} { return c.done }

func (c *countCtx) AfterFunc(func()) func() bool {
	c.added.Add(1)
	c.active.Add(1)
	var once sync.Once
	return func() bool {
		stopped := false
		once.Do(func() { stopped = true; c.active.Add(-1) })
		return stopped
	}
}

// check reports a test failure unless every registration was stopped.
func (c *countCtx) check(t *testing.T) {
	t.Helper()
	if c.added.Load() == 0 {
		t.Error("nothing registered a context.AfterFunc")
	}
	if n := c.active.Load(); n != 0 {
		t.Errorf("%d context.AfterFunc registrations left on ctx", n)
	}
}

func TestCallStopsAfterFunc(t *testing.T) {
	for name, reply := range map[string]string{
		"result":   `{"id":"b1","result":{"type":"ok"}}`,
		"error":    `{"id":"b1","error":{"code":"pane_not_found","message":"pane not found"}}`,
		"no reply": ``,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t, func(c *conn) {
				if reply != "" {
					c.send(reply)
				}
			})
			ctx := newCountCtx()
			f.client().Call(ctx, "ping", nil, nil)
			ctx.check(t)
		})
	}
}

// padTo returns ping params that make a fresh Client's first request line
// exactly n bytes long, newline not counted.
func padTo(t *testing.T, n int) map[string]any {
	t.Helper()
	base, err := request("b1", "ping", map[string]any{"pad": ""})
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{"pad": strings.Repeat("x", n-(len(base)-1))}
}

func TestCallRequestLimit(t *testing.T) {
	f := newFake(t, func(c *conn) { c.result(`{"type":"pong","version":"0.9.5","protocol":22}`) })
	if err := f.client().Call(testCtx(t), "ping", padTo(t, MaxRequest), nil); err != nil {
		t.Fatalf("request of MaxRequest bytes: %v", err)
	}
	if r := f.only(t); len(r.line) != MaxRequest+1 {
		t.Errorf("sent %d bytes, want %d", len(r.line), MaxRequest+1)
	}
	err := f.client().Call(testCtx(t), "ping", padTo(t, MaxRequest+1), nil)
	if !errors.Is(err, ErrTooLarge) {
		t.Fatalf("err = %v, want ErrTooLarge", err)
	}
	if s := err.Error(); !strings.Contains(s, "ping") || !strings.Contains(s, "1048576") {
		t.Errorf("error %q does not name the method and the limit", s)
	}
	if n := f.accepted.Load(); n != 1 {
		t.Errorf("dialed for a request over the limit")
	}
}

func TestCallNoHTMLEscape(t *testing.T) {
	f := newFake(t, func(c *conn) { c.result(`{"type":"ok"}`) })
	// Escaped, each of <, > and & takes 6 bytes, and this paste goes over MaxRequest.
	paste := strings.Repeat("<>&", 300<<10)
	err := f.client().Call(testCtx(t), "pane.send_text", map[string]any{"pane_id": "w1:p1", "text": paste}, nil)
	if err != nil {
		t.Fatal(err)
	}
	r := f.only(t)
	if len(r.line) > len(paste)+100 || !strings.Contains(r.line, `"text":"<>&<>&`) {
		t.Errorf("request of %d bytes escapes HTML: %.80s", len(r.line), r.line)
	}
}

func TestCallNoResult(t *testing.T) {
	for _, line := range []string{
		`{"id":"b1"}`,
		`{"id":"b1","result":null}`,
		`{"id":"b1","result":null,"error":null}`,
	} {
		f := newFake(t, func(c *conn) { c.send(line) })
		var out struct{}
		for _, o := range []any{nil, &out} {
			err := f.client().Call(testCtx(t), "tab.close", nil, o)
			if err == nil || !strings.Contains(err.Error(), "tab.close") {
				t.Errorf("%s: err = %v, want an error naming tab.close", line, err)
			}
			if _, ok := err.(*Error); ok {
				t.Errorf("%s: got a Herdr error for a reply without one", line)
			}
		}
	}
}

func TestCallReadBuffer(t *testing.T) {
	f := newFake(t, func(c *conn) { c.result(`{"type":"ok"}`) })
	cl, ctx := f.client(), testCtx(t)
	const n = 20
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	for range n {
		if err := cl.Call(ctx, "pane.send_text", map[string]any{"pane_id": "w1:p1", "text": "a"}, nil); err != nil {
			t.Fatal(err)
		}
	}
	runtime.ReadMemStats(&after)
	// The fake's allocations count too; a 1 MiB reader per call is far above this.
	if per := (after.TotalAlloc - before.TotalAlloc) / n; per > 256<<10 {
		t.Errorf("one call allocates %d bytes", per)
	}
}

func TestCallError(t *testing.T) {
	for name, line := range map[string]string{
		"echoed id": `{"id":"b1","error":{"code":"pane_not_found","message":"pane not found"}}`,
		"empty id":  `{"id":"","error":{"code":"pane_not_found","message":"pane not found"}}`,
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t, func(c *conn) { c.send(line) })
			var out struct{}
			err := f.client().Call(testCtx(t), "pane.get", map[string]any{"pane_id": "w1:p9"}, &out)
			he, ok := err.(*Error) // internal/bridge asserts the type directly
			if !ok {
				t.Fatalf("err = %#v, want *Error", err)
			}
			if he.Code != "pane_not_found" || he.Message != "pane not found" {
				t.Errorf("got %+v", he)
			}
			if s := he.Error(); s != "pane_not_found: pane not found" {
				t.Errorf("Error() = %q", s)
			}
		})
	}
}

func TestCallContext(t *testing.T) {
	expired, stop := context.WithCancel(context.Background())
	stop()
	for name, tc := range map[string]struct {
		ctx  func() (context.Context, context.CancelFunc)
		want error
	}{
		"deadline": {func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 50*time.Millisecond)
		}, context.DeadlineExceeded},
		"cancel": {func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			time.AfterFunc(50*time.Millisecond, cancel)
			return ctx, cancel
		}, context.Canceled},
		"canceled before": {func() (context.Context, context.CancelFunc) { return expired, stop }, context.Canceled},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t, func(c *conn) { c.hold() }) // no reply, like a long events.wait
			ctx, cancel := tc.ctx()
			defer cancel()
			start := time.Now()
			if err := f.client().Call(ctx, "events.wait", nil, nil); !errors.Is(err, tc.want) {
				t.Errorf("err = %v, want %v", err, tc.want)
			}
			if d := time.Since(start); d > time.Second {
				t.Errorf("returned after %v", d)
			}
		})
	}
}

func TestCallNoReply(t *testing.T) {
	f := newFake(t, func(c *conn) {}) // closes unanswered, as on a transport error
	if err := f.client().Call(testCtx(t), "ping", nil, nil); !errors.Is(err, io.EOF) {
		t.Errorf("err = %v, want EOF", err)
	}
}

func TestCallDialError(t *testing.T) {
	c := &Client{Socket: "/tmp/hxj-missing/herdr.sock"}
	var oe *net.OpError
	if err := c.Call(testCtx(t), "ping", nil, nil); !errors.As(err, &oe) {
		t.Errorf("err = %v, want a dial error", err)
	}
}

func TestCallBadParams(t *testing.T) {
	f := newFake(t, func(c *conn) { c.result(`{"type":"ok"}`) })
	var ue *json.UnsupportedTypeError
	if err := f.client().Call(testCtx(t), "ping", map[string]any{"f": func() {}}, nil); !errors.As(err, &ue) {
		t.Errorf("err = %v, want an encoding error", err)
	}
	if n := f.accepted.Load(); n != 0 {
		t.Errorf("dialed %d times for a request it cannot encode", n)
	}
}

func TestCallBadReply(t *testing.T) {
	var se *json.SyntaxError
	var ue *json.UnmarshalTypeError
	for name, tc := range map[string]struct {
		line   string
		target any
	}{
		"not json":     {`herdr`, &se},
		"wrong result": {`{"id":"b1","result":{"type":"tab_info","tab":"w1:t1"}}`, &ue},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t, func(c *conn) { c.send(tc.line) })
			var out struct {
				Tab Tab `json:"tab"`
			}
			if err := f.client().Call(testCtx(t), "tab.get", nil, &out); !errors.As(err, tc.target) {
				t.Errorf("err = %#v, want %T", err, tc.target)
			}
		})
	}
}

const started = `{"type":"subscription_started"}`

// drain reads events until the channel closes.
func drain(t *testing.T, events <-chan Event) []Event {
	t.Helper()
	var got []Event
	timeout := time.After(3 * time.Second)
	for {
		select {
		case e, ok := <-events:
			if !ok {
				return got
			}
			got = append(got, e)
		case <-timeout:
			t.Fatal("the event channel did not close")
		}
	}
}

func TestSubscribe(t *testing.T) {
	f := newFake(t, func(c *conn) {
		c.result(started)
		c.send(`{"event":"tab_created","data":{"type":"tab_created","tab":{"tab_id":"w1:t2"}}}`)
		c.send(`not json`) // skipped
		c.send(`{"event":"tab_closed","data":{"type":"tab_closed","tab_id":"w1:t2","workspace_id":"w1"}}`)
	}) // then closes, as a stopping server does
	events, failure, err := f.client().Subscribe(testCtx(t), []string{"tab.created", "tab.closed"})
	if err != nil {
		t.Fatal(err)
	}
	r := f.only(t)
	if r.Method != "events.subscribe" || r.ID == "" ||
		string(r.Params) != `{"subscriptions":[{"type":"tab.created"},{"type":"tab.closed"}]}` {
		t.Errorf("request %s", r.line)
	}
	want := []Event{
		{"tab_created", json.RawMessage(`{"type":"tab_created","tab":{"tab_id":"w1:t2"}}`)},
		{"tab_closed", json.RawMessage(`{"type":"tab_closed","tab_id":"w1:t2","workspace_id":"w1"}`)},
	}
	if got := drain(t, events); !reflect.DeepEqual(got, want) {
		t.Errorf("events %q, want %q", got, want)
	}
	if !errors.Is(*failure, io.EOF) {
		t.Errorf("failure = %v, want EOF", *failure)
	}
}

func TestSubscribeToCancel(t *testing.T) {
	closed := make(chan error, 1)
	f := newFake(t, func(c *conn) {
		c.result(started)
		c.send(`{"event":"pane.agent_status_changed","data":{"pane_id":"w1:p1","workspace_id":"w1","agent_status":"blocked"}}`)
		_, err := c.hold()
		closed <- err
	})
	ctx, cancel := context.WithCancel(testCtx(t))
	defer cancel()
	subs := []map[string]any{{"type": "pane.agent_status_changed", "pane_id": "w1:p1"}}
	events, failure, err := f.client().SubscribeTo(ctx, subs)
	if err != nil {
		t.Fatal(err)
	}
	if r := f.only(t); string(r.Params) != `{"subscriptions":[{"pane_id":"w1:p1","type":"pane.agent_status_changed"}]}` {
		t.Errorf("request %s", r.line)
	}
	if e := <-events; e.Name != "pane.agent_status_changed" || !strings.Contains(string(e.Data), `"blocked"`) {
		t.Errorf("event %s %s", e.Name, e.Data)
	}
	cancel()
	if got := drain(t, events); len(got) != 0 {
		t.Errorf("events after cancel: %q", got)
	}
	if !errors.Is(*failure, context.Canceled) {
		t.Errorf("failure = %v, want context.Canceled", *failure)
	}
	if err := <-closed; err != nil {
		t.Errorf("connection still open after cancel: %v", err)
	}
}

func TestSubscribeEventsLost(t *testing.T) {
	closed := make(chan error, 1)
	f := newFake(t, func(c *conn) {
		c.result(started)
		c.send(`{"event":"tab_created","data":{"type":"tab_created","tab":{"tab_id":"w1:t2"}}}`)
		c.fail("events_lost", "event subscription fell behind retained history; resubscribe and resync with session.snapshot")
		closed <- c.eof() // the client must stop without waiting for the close
	})
	events, failure, err := f.client().Subscribe(context.Background(), []string{"tab.created"})
	if err != nil {
		t.Fatal(err)
	}
	if got := drain(t, events); len(got) != 1 || got[0].Name != "tab_created" {
		t.Errorf("events %q", got)
	}
	var he *Error
	if !errors.As(*failure, &he) || he.Code != "events_lost" {
		t.Errorf("failure = %#v, want *Error events_lost", *failure)
	}
	if err := <-closed; err != nil {
		t.Error(err)
	}
}

func TestSubscribeAckError(t *testing.T) {
	closed := make(chan error, 1)
	f := newFake(t, func(c *conn) {
		c.fail("pane_not_found", "pane w1:p9 not found")
		closed <- c.eof()
	})
	subs := []map[string]any{{"type": "pane.agent_status_changed", "pane_id": "w1:p9"}}
	events, failure, err := f.client().SubscribeTo(context.Background(), subs)
	if he, ok := err.(*Error); !ok || he.Code != "pane_not_found" {
		t.Errorf("err = %#v, want *Error pane_not_found", err)
	}
	if events != nil || failure != nil {
		t.Error("got a channel with the error")
	}
	if err := <-closed; err != nil {
		t.Error(err)
	}
}

func TestSubscribeAckType(t *testing.T) {
	for _, line := range []string{
		`{"id":"sub","result":{"type":"ok"}}`,
		`{"id":"sub","result":{}}`,
		`{"id":"sub"}`,
	} {
		f := newFake(t, func(c *conn) { c.send(line) })
		_, _, err := f.client().Subscribe(testCtx(t), []string{"tab.created"})
		if err == nil || !strings.Contains(err.Error(), "events.subscribe") {
			t.Errorf("%s: err = %v, want an error naming events.subscribe", line, err)
		}
	}
}

func TestSubscribeStopsAfterFunc(t *testing.T) {
	t.Run("ended", func(t *testing.T) {
		f := newFake(t, func(c *conn) { c.result(started) })
		ctx := newCountCtx()
		events, _, err := f.client().Subscribe(ctx, []string{"tab.created"})
		if err != nil {
			t.Fatal(err)
		}
		drain(t, events)
		ctx.check(t)
	})
	t.Run("rejected", func(t *testing.T) {
		f := newFake(t, func(c *conn) { c.fail("invalid_request", "unknown subscription") })
		ctx := newCountCtx()
		if _, _, err := f.client().Subscribe(ctx, []string{"tab.bogus"}); err == nil {
			t.Fatal("no error")
		}
		ctx.check(t)
	})
}

func TestSubscribeNoAck(t *testing.T) {
	t.Run("closed", func(t *testing.T) {
		f := newFake(t, func(c *conn) {})
		if _, _, err := f.client().Subscribe(testCtx(t), []string{"tab.created"}); !errors.Is(err, io.EOF) {
			t.Errorf("err = %v, want EOF", err)
		}
	})
	t.Run("deadline", func(t *testing.T) {
		f := newFake(t, func(c *conn) { c.hold() })
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()
		start := time.Now()
		if _, _, err := f.client().Subscribe(ctx, []string{"tab.created"}); !errors.Is(err, context.DeadlineExceeded) {
			t.Errorf("err = %v, want deadline exceeded", err)
		}
		if d := time.Since(start); d > time.Second {
			t.Errorf("returned after %v", d)
		}
	})
}

func TestSubscribeRequestErrors(t *testing.T) {
	c := &Client{Socket: "/tmp/hxj-missing/herdr.sock"}
	var oe *net.OpError
	if _, _, err := c.Subscribe(testCtx(t), []string{"tab.created"}); !errors.As(err, &oe) {
		t.Errorf("missing socket: err = %v, want a dial error", err)
	}
	f := newFake(t, func(c *conn) { c.result(started) })
	var ue *json.UnsupportedTypeError
	if _, _, err := f.client().SubscribeTo(testCtx(t), []map[string]any{{"type": func() {}}}); !errors.As(err, &ue) {
		t.Errorf("bad subscription: err = %v, want an encoding error", err)
	}
	if n := f.accepted.Load(); n != 0 {
		t.Errorf("dialed %d times for a request it cannot encode", n)
	}
	subs := make([]map[string]any, MaxRequest/20)
	for i := range subs {
		subs[i] = map[string]any{"type": "pane.agent_status_changed", "pane_id": "w1:p1"}
	}
	if _, _, err := f.client().SubscribeTo(testCtx(t), subs); !errors.Is(err, ErrTooLarge) ||
		!strings.Contains(err.Error(), "events.subscribe") {
		t.Errorf("too many subscriptions: err = %v, want ErrTooLarge naming events.subscribe", err)
	}
	if n := f.accepted.Load(); n != 0 {
		t.Errorf("dialed %d times for a request over the limit", n)
	}
}

func TestPing(t *testing.T) {
	gen := uint32(1)
	for name, tc := range map[string]struct {
		result string
		want   Pong
	}{
		"capabilities": {
			`{"type":"pong","version":"0.9.5","protocol":22,"capabilities":{"live_handoff":true,` +
				`"detached_server_daemon":true,"endpoint_protocol_generation":1,"surface_interest":true,` +
				`"health_check":true,"ssh_agent_registration":true}}`,
			Pong{Version: "0.9.5", Protocol: 22, Capabilities: Capabilities{
				LiveHandoff:          true,
				DetachedServerDaemon: true, SurfaceInterest: true, HealthCheck: true,
				SSHAgentRegistration: true, EndpointProtocolGeneration: &gen,
			}},
		},
		"no endpoint generation": {
			`{"type":"pong","version":"0.9.5","protocol":22,"capabilities":{"live_handoff":true}}`,
			Pong{Version: "0.9.5", Protocol: 22, Capabilities: Capabilities{LiveHandoff: true}},
		},
		"null capabilities": {
			`{"type":"pong","version":"0.9.5","protocol":22,"capabilities":null}`,
			Pong{Version: "0.9.5", Protocol: 22},
		},
		"no capabilities": {
			`{"type":"pong","version":"0.8.0","protocol":20}`,
			Pong{Version: "0.8.0", Protocol: 20},
		},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t, func(c *conn) { c.result(tc.result) })
			p, err := f.client().Ping(testCtx(t))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(*p, tc.want) {
				t.Errorf("got %+v, want %+v", *p, tc.want)
			}
			if r := f.only(t); r.Method != "ping" || string(r.Params) != "{}" {
				t.Errorf("request %s", r.line)
			}
		})
	}
}

// The payloads follow docs/next/api/herdr-api.schema.json in Herdr 0.9.5,
// including fields the types leave out.
const snapshotResult = `{"type":"session_snapshot","snapshot":{"version":"0.9.5","protocol":22,
"focused_workspace_id":"w1","focused_tab_id":"w1:t1","focused_pane_id":"w1:p2",
"workspaces":[{"workspace_id":"w1","number":1,"label":"herdrex","focused":true,"pane_count":2,
 "tab_count":1,"active_tab_id":"w1:t1","agent_status":"working","tokens":{},"worktree":null}],
"tabs":[{"tab_id":"w1:t1","workspace_id":"w1","number":1,"label":"main","focused":true,
 "pane_count":2,"agent_status":"working"}],
"panes":[{"pane_id":"w1:p1","terminal_id":"t-1","workspace_id":"w1","tab_id":"w1:t1","focused":false,
 "agent_status":"unknown","revision":7,"agent":null,"display_agent":null,"cwd":"/src",
 "foreground_cwd":null,"label":null,"terminal_title":"zsh","scroll":null},
 {"pane_id":"w1:p2","terminal_id":"t-2","workspace_id":"w1","tab_id":"w1:t1","focused":true,
 "agent_status":"working","revision":12,"agent":"claude","display_agent":"Claude","cwd":"/src",
 "foreground_cwd":"/src/pkg","label":"agent","terminal_title":"claude","state_labels":{}}],
"layouts":[{"workspace_id":"w1","tab_id":"w1:t1","zoomed":false,
 "area":{"x":0,"y":0,"width":160,"height":48},"focused_pane_id":"w1:p2",
 "panes":[{"pane_id":"w1:p1","focused":false,"rect":{"x":0,"y":0,"width":80,"height":48}},
  {"pane_id":"w1:p2","focused":true,"rect":{"x":80,"y":0,"width":80,"height":48}}],
 "splits":[{"id":"s","direction":"right","ratio":0.5,"rect":{"x":0,"y":0,"width":160,"height":48}}]}],
"agents":[]}}`

func TestSnapshot(t *testing.T) {
	f := newFake(t, func(c *conn) { c.result(snapshotResult) })
	s, err := f.client().Snapshot(testCtx(t))
	if err != nil {
		t.Fatal(err)
	}
	want := &Snapshot{
		Workspaces: []Workspace{{ID: "w1", Label: "herdrex", ActiveTabID: "w1:t1", Focused: true, AgentStatus: "working"}},
		Tabs:       []Tab{{ID: "w1:t1", WorkspaceID: "w1", Label: "main", Focused: true}},
		Panes: []Pane{
			{ID: "w1:p1", TabID: "w1:t1", WorkspaceID: "w1", AgentStatus: "unknown", CWD: "/src", TerminalTitle: "zsh"},
			{
				ID: "w1:p2", TabID: "w1:t1", WorkspaceID: "w1", Focused: true, AgentStatus: "working", Agent: "claude",
				DisplayAgent: "Claude", CWD: "/src", ForegroundCWD: "/src/pkg", Label: "agent", TerminalTitle: "claude",
			},
		},
		Layouts: []TabLayout{{TabID: "w1:t1", WorkspaceID: "w1", FocusedPaneID: "w1:p2", Panes: []LayoutPane{
			{PaneID: "w1:p1", Rect: Rect{X: 0, Y: 0, Width: 80, Height: 48}},
			{PaneID: "w1:p2", Rect: Rect{X: 80, Y: 0, Width: 80, Height: 48}},
		}}},
		FocusedPaneID: "w1:p2",
	}
	if !reflect.DeepEqual(s, want) {
		t.Errorf("got %+v\nwant %+v", s, want)
	}
	if r := f.only(t); r.Method != "session.snapshot" || string(r.Params) != "{}" {
		t.Errorf("request %s", r.line)
	}
}

func TestLayout(t *testing.T) {
	f := newFake(t, func(c *conn) {
		c.result(`{"type":"layout_export","layout":{"workspace_id":"w1","tab_id":"w1:t1","zoomed":true,
		"focused_pane_id":"w1:p3","root":{"type":"split","direction":"right","ratio":0.5,
		"first":{"type":"pane","pane_id":"w1:p1","cwd":"/src","label":null,"command":null,"env":{}},
		"second":{"type":"split","direction":"down","ratio":0.25,
		 "first":{"type":"pane","pane_id":"w1:p2"},"second":{"type":"pane","pane_id":"w1:p3"}}}}}`)
	})
	n, err := f.client().Layout(testCtx(t), "w1:t1")
	if err != nil {
		t.Fatal(err)
	}
	want := &LayoutNode{
		Type: "split", Direction: "right", Ratio: 0.5, Focused: "w1:p3", Zoomed: true,
		First: &LayoutNode{Type: "pane", PaneID: "w1:p1"},
		Second: &LayoutNode{
			Type: "split", Direction: "down", Ratio: 0.25,
			First:  &LayoutNode{Type: "pane", PaneID: "w1:p2"},
			Second: &LayoutNode{Type: "pane", PaneID: "w1:p3"},
		},
	}
	if !reflect.DeepEqual(n, want) {
		t.Errorf("got %+v\nwant %+v", n, want)
	}
	if r := f.only(t); r.Method != "layout.export" || string(r.Params) != `{"tab_id":"w1:t1"}` {
		t.Errorf("request %s", r.line)
	}
}

func TestLayoutNoRoot(t *testing.T) {
	f := newFake(t, func(c *conn) {
		c.result(`{"type":"layout_export","layout":{"workspace_id":"w1","tab_id":"w1:t1","zoomed":false,"focused_pane_id":"w1:p1"}}`)
	})
	if n, err := f.client().Layout(testCtx(t), "w1:t1"); err == nil || n != nil {
		t.Errorf("got %+v, %v; want an error", n, err)
	}
}

// sameJSON reports whether a and b hold the same JSON value.
func sameJSON(a, b []byte) bool {
	var x, y any
	return json.Unmarshal(a, &x) == nil && json.Unmarshal(b, &y) == nil && reflect.DeepEqual(x, y)
}

const readResult = `{"type":"pane_read","read":{"pane_id":"w1:p1","workspace_id":"w1","tab_id":"w1:t1",
"source":"recent_unwrapped","format":"ansi","text":"\u001b[1m$\u001b[0m ls\n","revision":3,"truncated":true}}`

func TestReadPane(t *testing.T) {
	f := newFake(t, func(c *conn) { c.result(readResult) })
	text, err := f.client().ReadPane(testCtx(t), "w1:p1", "recent_unwrapped", "ansi")
	if err != nil {
		t.Fatal(err)
	}
	if text != "\x1b[1m$\x1b[0m ls\n" {
		t.Errorf("text = %q", text)
	}
	if r := f.only(t); r.Method != "pane.read" ||
		!sameJSON(r.Params, []byte(`{"format":"ansi","pane_id":"w1:p1","source":"recent_unwrapped"}`)) {
		t.Errorf("request %s", r.line)
	}
}

func TestPaneRead(t *testing.T) {
	for name, tc := range map[string]struct {
		p      PaneReadParams
		params string
	}{
		"all":      {PaneReadParams{PaneID: "w1:p1", Source: "recent", Format: "ansi", Lines: 1000}, `{"pane_id":"w1:p1","source":"recent","format":"ansi","lines":1000}`},
		"defaults": {PaneReadParams{PaneID: "w1:p1", Source: "recent"}, `{"pane_id":"w1:p1","source":"recent"}`},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t, func(c *conn) { c.result(readResult) })
			r, err := f.client().PaneRead(testCtx(t), tc.p)
			if err != nil {
				t.Fatal(err)
			}
			want := &PaneReadResult{
				PaneID: "w1:p1", WorkspaceID: "w1", TabID: "w1:t1", Source: "recent_unwrapped",
				Format: "ansi", Text: "\x1b[1m$\x1b[0m ls\n", Revision: 3, Truncated: true,
			}
			if !reflect.DeepEqual(r, want) {
				t.Errorf("got %+v\nwant %+v", r, want)
			}
			if req := f.only(t); req.Method != "pane.read" || !sameJSON(req.Params, []byte(tc.params)) {
				t.Errorf("request %s, want params %s", req.line, tc.params)
			}
		})
	}
}

// TestTypedResults checks that each typed helper rejects a result of another
// type, or one without its payload.
func TestTypedResults(t *testing.T) {
	for name, tc := range map[string]struct {
		call   func(*Client, context.Context) (any, error)
		result string
	}{
		"Ping other type":         {ping, `{"type":"ok","version":"0.9.5","protocol":22}`},
		"Ping no type":            {ping, `{"version":"0.9.5","protocol":22}`},
		"Snapshot other type":     {snapshot, `{"type":"ok","snapshot":{"workspaces":[]}}`},
		"Snapshot empty":          {snapshot, `{}`},
		"Snapshot no snapshot":    {snapshot, `{"type":"session_snapshot"}`},
		"Layout other type":       {layout, `{"type":"pane_layout","layout":{"root":{"type":"pane","pane_id":"w1:p1"}}}`},
		"PaneRead other type":     {paneRead, `{"type":"ok","read":{"text":"x"}}`},
		"PaneRead no read":        {paneRead, `{"type":"pane_read"}`},
		"ReadPane other type":     {readPane, `{"type":"ok","read":{"text":"x"}}`},
		"ReadPane no result type": {readPane, `{"read":{"text":"x"}}`},
	} {
		t.Run(name, func(t *testing.T) {
			f := newFake(t, func(c *conn) { c.result(tc.result) })
			v, err := tc.call(f.client(), testCtx(t))
			if err == nil {
				t.Fatalf("got %+v, want an error", v)
			}
			if _, ok := err.(*Error); ok {
				t.Errorf("got a Herdr error for a reply without one: %v", err)
			}
		})
	}
}

func ping(c *Client, ctx context.Context) (any, error)     { return c.Ping(ctx) }
func snapshot(c *Client, ctx context.Context) (any, error) { return c.Snapshot(ctx) }
func layout(c *Client, ctx context.Context) (any, error)   { return c.Layout(ctx, "w1:t1") }
func paneRead(c *Client, ctx context.Context) (any, error) {
	return c.PaneRead(ctx, PaneReadParams{PaneID: "w1:p1", Source: "recent"})
}

func readPane(c *Client, ctx context.Context) (any, error) {
	return c.ReadPane(ctx, "w1:p1", "recent", "text")
}

func TestTypedErrors(t *testing.T) {
	f := newFake(t, func(c *conn) { c.fail("server_unavailable", "server is shutting down") })
	cl, ctx := f.client(), testCtx(t)
	check := func(name string, err error, zero bool) {
		t.Helper()
		if he, ok := err.(*Error); !ok || he.Code != "server_unavailable" {
			t.Errorf("%s: err = %#v, want *Error server_unavailable", name, err)
		}
		if !zero {
			t.Errorf("%s: returned a value with the error", name)
		}
	}
	p, err := cl.Ping(ctx)
	check("Ping", err, p == nil)
	s, err := cl.Snapshot(ctx)
	check("Snapshot", err, s == nil)
	n, err := cl.Layout(ctx, "w1:t1")
	check("Layout", err, n == nil)
	text, err := cl.ReadPane(ctx, "w1:p1", "visible", "text")
	check("ReadPane", err, text == "")
	r, err := cl.PaneRead(ctx, PaneReadParams{PaneID: "w1:p1", Source: "visible"})
	check("PaneRead", err, r == nil)
}
