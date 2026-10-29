package endpoint

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
	"slices"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/elonnzhang/herdrex/pkg/herdr/internal/wire"
	"github.com/elonnzhang/herdrex/pkg/herdr/jsonapi"
)

// TestMain fails the run if a connection's read loop outlives the tests.
func TestMain(m *testing.M) {
	code := m.Run()
	if code == 0 {
		if stack := leaked(); stack != "" {
			fmt.Fprintf(os.Stderr, "leaked read loop:\n%s\n", stack)
			code = 1
		}
	}
	os.Exit(code)
}

func leaked() string {
	buf := make([]byte, 1<<20)
	for range 200 {
		n := runtime.Stack(buf, true)
		if !bytes.Contains(buf[:n], []byte("endpoint.(*Conn).run")) {
			return ""
		}
		time.Sleep(10 * time.Millisecond)
	}
	return string(buf[:runtime.Stack(buf, true)])
}

// fake is the server end of one connection. Its methods end the handler
// goroutine on failure.
type fake struct {
	t  *testing.T
	nc net.Conn
	r  *bufio.Reader
}

// serve listens on a new socket and runs handle on the first connection in
// another goroutine. Cleanup waits for handle to return.
func serve(t *testing.T, handle func(f *fake)) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "hxe")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "c.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		nc, err := ln.Accept()
		ln.Close()
		if err != nil {
			return
		}
		defer nc.Close()
		handle(&fake{t: t, nc: nc, r: bufio.NewReader(nc)})
	}()
	t.Cleanup(func() {
		ln.Close()
		<-done
		os.RemoveAll(dir)
	})
	return path
}

func (f *fake) fail(format string, args ...any) {
	f.t.Errorf("server: "+format, args...)
	runtime.Goexit()
}

// read returns the next client frame.
func (f *fake) read() []byte {
	f.nc.SetReadDeadline(time.Now().Add(5 * time.Second))
	p, err := wire.ReadFrame(f.r, wire.MaxGraphicsFrame)
	if err != nil {
		f.fail("read: %v", err)
	}
	return p
}

// expectNothing checks that the client sends nothing for d.
func (f *fake) expectNothing(d time.Duration) {
	f.nc.SetReadDeadline(time.Now().Add(d))
	_, err := f.r.Peek(1)
	if !errors.Is(err, os.ErrDeadlineExceeded) {
		f.fail("expected no frame, got %v", err)
	}
}

// waitClose waits for the client to close the connection.
func (f *fake) waitClose() {
	f.nc.SetReadDeadline(time.Now().Add(10 * time.Second))
	if _, err := io.Copy(io.Discard, f.r); err != nil {
		f.fail("waiting for close: %v", err)
	}
}

func (f *fake) control() (kind, data string) {
	d := wire.NewDec(f.read())
	if tag := d.U32(); tag != cControl {
		f.fail("got message %d, want EndpointControl", tag)
	}
	kind, data = d.Str(), d.Str()
	if d.Done() != nil {
		f.fail("malformed EndpointControl")
	}
	return kind, data
}

func (f *fake) readHello() map[string]any {
	kind, data := f.control()
	if kind != kindHello {
		f.fail("got %s, want %s", kind, kindHello)
	}
	var h map[string]any
	if err := json.Unmarshal([]byte(data), &h); err != nil {
		f.fail("hello: %v", err)
	}
	return h
}

func (f *fake) sendRaw(p []byte) {
	if err := wire.WriteFrame(f.nc, p); err != nil {
		f.fail("write: %v", err)
	}
}

func (f *fake) send(m Message) { f.sendRaw(encodeMessage(m)) }

func (f *fake) welcome(w Welcome) {
	data, _ := json.Marshal(w)
	f.send(&Control{Kind: kindWelcome, Data: string(data)})
}

func testWelcome() Welcome {
	return Welcome{
		Generation: 1, ServerVersion: "0.9.5", SnapshotCodec: codecSnapshot,
		SurfaceCodec: codecSurface, InputCodec: codecInput, BlobCodec: codecBlob,
		Methods:      []string{"client_shell.surface.set", "pane.focus", "tab.create", "tab.focus"},
		Capabilities: []string{"surface_interest", "health_check"},
	}
}

// accept completes the handshake with testWelcome and a snapshot of boot-1.
func (f *fake) accept() {
	f.readHello()
	f.welcome(testWelcome())
	f.send(&Snapshot{BootID: "boot-1", Revision: 1})
}

type request struct {
	BootID string
	ID     string          `json:"id"`
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

func (f *fake) readRequest() request {
	d := wire.NewDec(f.read())
	if tag := d.U32(); tag != cRequest {
		f.fail("got message %d, want ClientShellEndpointRequest", tag)
	}
	r := request{BootID: d.Str()}
	data := d.Str()
	if d.Done() != nil {
		f.fail("malformed request")
	}
	if err := json.Unmarshal([]byte(data), &r); err != nil {
		f.fail("request %q: %v", data, err)
	}
	return r
}

// respond answers the request whose id data carries, in one chunk.
func (f *fake) respond(data string) {
	var head struct{ ID string }
	json.Unmarshal([]byte(data), &head)
	f.respondTo(head.ID, data)
}

func (f *fake) respondTo(id, data string) {
	f.send(&responseChunk{BootID: "boot-1", RequestID: id, Final: true, Data: []byte(data)})
}

func (f *fake) reply(id, result string) {
	f.respond(fmt.Sprintf(`{"id":%q,"result":%s}`, id, result))
}

func (f *fake) replyError(id, code string) {
	f.respond(fmt.Sprintf(`{"id":%q,"error":{"code":%q,"message":"m"}}`, id, code))
}

func dial(t *testing.T, path string, h Hello) *Conn {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, path, h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func next(t *testing.T, c *Conn) Message {
	t.Helper()
	select {
	case m, ok := <-c.Events():
		if !ok {
			t.Fatalf("events closed: %v", c.Err())
		}
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("no event")
	}
	return nil
}

// skipSnapshot reads the snapshot that Dial waited for.
func skipSnapshot(t *testing.T, c *Conn) {
	t.Helper()
	if m, ok := next(t, c).(*Snapshot); !ok {
		t.Fatalf("first event %T, want *Snapshot", m)
	}
}

func ctx5(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return ctx
}

func dialErr(t *testing.T, handle func(f *fake)) error {
	t.Helper()
	path := serve(t, handle)
	c, err := Dial(ctx5(t), path, Hello{})
	if err == nil {
		c.Close()
		t.Fatal("Dial succeeded")
	}
	return err
}

func TestHandshakeRejected(t *testing.T) {
	for _, code := range []string{"invalid_hello", "unsupported_generation", "no_common_core", "invalid_surface"} {
		t.Run(code, func(t *testing.T) {
			err := dialErr(t, func(f *fake) {
				f.readHello()
				w := testWelcome()
				w.Methods, w.Capabilities = nil, nil
				w.Error = &HandshakeError{Code: code, Message: "why"}
				f.welcome(w)
				f.waitClose()
			})
			var he *HandshakeError
			if !errors.As(err, &he) {
				t.Fatalf("got %T %v, want *HandshakeError", err, err)
			}
			if he.Code != code || he.Message != "why" || he.ServerVersion != "0.9.5" {
				t.Errorf("got %+v", he)
			}
			if !strings.Contains(err.Error(), code) {
				t.Errorf("message %q lacks the code", err)
			}
		})
	}
}

// The server accepted, but not with generation 1 and the v1 codecs.
func TestHandshakeMismatch(t *testing.T) {
	for _, tc := range []struct {
		code   string
		change func(*Welcome)
	}{
		{"unsupported_generation", func(w *Welcome) { w.Generation = 2 }},
		{"no_common_core", func(w *Welcome) { w.SurfaceCodec = "shell.surface.v2" }},
	} {
		err := dialErr(t, func(f *fake) {
			f.readHello()
			w := testWelcome()
			tc.change(&w)
			f.welcome(w)
			f.waitClose()
		})
		var he *HandshakeError
		if !errors.As(err, &he) || he.Code != tc.code {
			t.Errorf("got %v, want %s", err, tc.code)
		}
	}
}

func TestNoEndpoint(t *testing.T) {
	const reason = "expected TerminalHello or ClientShellHello as first message"
	err := dialErr(t, func(f *fake) {
		f.readHello()
		var e wire.Enc
		f.sendRaw(*e.U(sWelcome).U(22).U(0).Opt(true).Str(reason))
		f.waitClose()
	})
	var ne *NoEndpointError
	if !errors.As(err, &ne) || ne.Message != reason {
		t.Errorf("bincode Welcome: got %T %v", err, err)
	}

	err = dialErr(t, func(f *fake) { f.readHello() })
	if !errors.As(err, &ne) {
		t.Errorf("close before reply: got %T %v", err, err)
	}
	if !strings.Contains(err.Error(), "does not support the endpoint protocol") {
		t.Errorf("message %q", err)
	}
}

func TestHandshakeBadReplies(t *testing.T) {
	for _, tc := range []struct {
		name  string
		reply func(f *fake)
		want  string
	}{
		{"other control", func(f *fake) { f.send(&Control{Kind: kindSnapshot, Data: "{}"}) }, "expected endpoint.welcome.v1"},
		{"invalid welcome", func(f *fake) { f.send(&Control{Kind: kindWelcome, Data: "{"}) }, "invalid endpoint.welcome.v1"},
		{"other message", func(f *fake) { f.send(&Bell{Count: 1}) }, "got message 9"},
		{"malformed Welcome", func(f *fake) { f.sendRaw([]byte{sWelcome, 22, 5, 0}) }, "malformed"},
		{"malformed control", func(f *fake) { f.sendRaw([]byte{sControl, 9}) }, "malformed"},
		{"shutdown", func(f *fake) {
			f.welcome(testWelcome())
			f.send(&Shutdown{Reason: "live update in progress"})
		}, "live update in progress"},
		{"malformed after welcome", func(f *fake) {
			f.welcome(testWelcome())
			f.sendRaw([]byte{sBell})
		}, "malformed"},
		{"closed after welcome", func(f *fake) { f.welcome(testWelcome()) }, "EOF"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := dialErr(t, func(f *fake) {
				f.readHello()
				tc.reply(f)
			})
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("got %v, want %q", err, tc.want)
			}
		})
	}
}

func TestDialContext(t *testing.T) {
	silent := func(f *fake) {
		f.readHello()
		f.waitClose()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if _, err := Dial(ctx, serve(t, silent), Hello{}); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("deadline: got %v", err)
	}

	ctx, cancel = context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	if _, err := Dial(ctx, serve(t, silent), Hello{}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancel: got %v", err)
	}

	if _, err := Dial(ctx5(t), "/tmp/hxe-missing/c.sock", Hello{}); err == nil {
		t.Error("dialed a missing socket")
	}
}

func TestDialWaitsForSnapshot(t *testing.T) {
	release := make(chan struct{})
	completions := &AgentCompletions{BootID: "boot-7", Revision: 1, Completions: map[string]uint64{"w1:p1": 3}}
	path := serve(t, func(f *fake) {
		f.readHello()
		f.welcome(testWelcome())
		f.send(completions)
		<-release
		f.send(&Snapshot{BootID: "boot-7", Revision: 1})
		f.waitClose()
	})
	type result struct {
		c   *Conn
		err error
	}
	dialed := make(chan result, 1)
	go func() {
		c, err := Dial(ctx5(t), path, Hello{})
		dialed <- result{c, err}
	}()
	select {
	case r := <-dialed:
		t.Fatalf("Dial returned before the snapshot: %v", r.err)
	case <-time.After(150 * time.Millisecond):
	}
	close(release)
	r := <-dialed
	if r.err != nil {
		t.Fatal(r.err)
	}
	c := r.c
	defer c.Close()
	if c.BootID() != "boot-7" {
		t.Errorf("BootID %q", c.BootID())
	}
	if !reflect.DeepEqual(c.Welcome, testWelcome()) {
		t.Errorf("Welcome %+v", c.Welcome)
	}
	if m := next(t, c); !reflect.DeepEqual(m, completions) {
		t.Errorf("first event %+v", m)
	}
	if s, ok := next(t, c).(*Snapshot); !ok || s.BootID != "boot-7" {
		t.Errorf("second event %+v", s)
	}
	if c.Err() != nil {
		t.Errorf("Err %v while open", c.Err())
	}
}

func TestHelloContents(t *testing.T) {
	hellos := make(chan map[string]any, 1)
	path := serve(t, func(f *fake) {
		hellos <- f.readHello()
		f.welcome(testWelcome())
		f.send(&Snapshot{BootID: "b"})
		f.waitClose()
	})
	dial(t, path, Hello{})
	want := map[string]any{
		"generation": 1.0, "cell_width_px": 0.0, "cell_height_px": 0.0,
		"surface_size": map[string]any{"cols": 80.0, "rows": 24.0},
		"pixel_mouse":  false, "direct_graphics": false, "endpoint_keybindings": false,
		"mouse_capture": false, "surface_active": false,
		"surface_reuse": false, "surface_delta": false, "surface_scroll": false,
		"snapshot_codecs": []any{"shell.snapshot.v1"}, "surface_codecs": []any{"shell.surface.v1"},
		"input_codecs": []any{"shell.input.semantic.v1"}, "blob_codecs": []any{"shell.blob.v1"},
	}
	if got := <-hellos; !reflect.DeepEqual(got, want) {
		t.Errorf("zero Hello sent\n%v\nwant\n%v", got, want)
	}

	h := Hello{
		Cols: 120, CellWidthPx: 9, CellHeightPx: 18, PixelMouse: true,
		EndpointKeybindings: true, MouseCapture: true, SurfaceActive: true,
	}.json()
	if h.SurfaceSize != (surfaceSize{120, 24}) || h.CellWidthPx != 9 || h.CellHeightPx != 18 ||
		!h.PixelMouse || !h.EndpointKeybindings || !h.MouseCapture || !h.SurfaceActive || h.DirectGraphics {
		t.Errorf("Hello %+v", h)
	}
	if h := (Hello{Rows: 50}).json(); h.SurfaceSize != (surfaceSize{80, 50}) {
		t.Errorf("rows only: %+v", h.SurfaceSize)
	}
}

func TestCallChunks(t *testing.T) {
	path := serve(t, func(f *fake) {
		f.accept()
		r := f.readRequest()
		if r.BootID != "boot-1" || r.Method != "tab.create" || string(r.Params) != `{"label":"x"}` {
			f.fail("request %+v", r)
		}
		full := fmt.Sprintf(`{"id":%q,"result":{"type":"tab_info","tab":{"tab_id":"w1:t2"}}}`, r.ID)
		half := len(full) / 2
		f.send(&responseChunk{BootID: "boot-1", RequestID: r.ID, Data: []byte(full[:half])})
		f.send(&Bell{Count: 2})
		f.send(&responseChunk{BootID: "boot-1", RequestID: "other", Final: true, Data: []byte("{}")})
		f.send(&responseChunk{BootID: "boot-1", RequestID: r.ID, Final: true, Data: []byte(full[half:])})
		f.waitClose()
	})
	c := dial(t, path, Hello{})
	var out struct {
		Tab struct {
			ID string `json:"tab_id"`
		} `json:"tab"`
	}
	if err := c.Call(ctx5(t), "tab.create", map[string]string{"label": "x"}, &out); err != nil {
		t.Fatal(err)
	}
	if out.Tab.ID != "w1:t2" {
		t.Errorf("result %+v", out)
	}
	skipSnapshot(t, c)
	if m := next(t, c); !reflect.DeepEqual(m, &Bell{Count: 2}) {
		t.Errorf("event %+v, want the bell between the chunks", m)
	}
}

func TestCallSerialized(t *testing.T) {
	got1 := make(chan struct{})
	path := serve(t, func(f *fake) {
		f.accept()
		r1 := f.readRequest()
		close(got1)
		f.expectNothing(200 * time.Millisecond)
		f.reply(r1.ID, `{"type":"ok"}`)
		r2 := f.readRequest()
		if r1.Method != "pane.focus" || r2.Method != "tab.focus" || r1.ID == r2.ID {
			f.fail("requests %+v then %+v", r1, r2)
		}
		f.reply(r2.ID, `{"type":"ok"}`)
		f.waitClose()
	})
	c := dial(t, path, Hello{})
	errs := make(chan error, 2)
	go func() { errs <- c.Call(ctx5(t), "pane.focus", map[string]string{"pane_id": "w1:p1"}, nil) }()
	<-got1
	go func() { errs <- c.Call(ctx5(t), "tab.focus", map[string]string{"tab_id": "w1:t1"}, nil) }()
	for range 2 {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}
}

// A Call that gives up still holds the connection until Herdr answers it.
func TestCallAbandoned(t *testing.T) {
	late := make(chan struct{})
	path := serve(t, func(f *fake) {
		f.accept()
		r1 := f.readRequest()
		<-late
		f.reply(r1.ID, `{"type":"ok"}`)
		if r := f.readRequest(); r.Method != "tab.focus" {
			f.fail("third call sent %s", r.Method)
		} else {
			f.reply(r.ID, `{"type":"ok"}`)
		}
		f.waitClose()
	})
	c := dial(t, path, Hello{})
	short := func(d time.Duration) context.Context {
		ctx, cancel := context.WithTimeout(context.Background(), d)
		t.Cleanup(cancel)
		return ctx
	}
	if err := c.Call(short(100*time.Millisecond), "pane.focus", nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("abandoned call: %v", err)
	}
	if err := c.Call(short(50*time.Millisecond), "tab.create", nil, nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("waiting call: %v", err)
	}
	close(late)
	if err := c.Call(ctx5(t), "tab.focus", nil, nil); err != nil {
		t.Error(err)
	}
}

func TestCallNotSent(t *testing.T) {
	checked := make(chan struct{})
	path := serve(t, func(f *fake) {
		f.accept()
		f.expectNothing(150 * time.Millisecond)
		close(checked)
		f.waitClose()
	})
	c := dial(t, path, Hello{})
	var je *jsonapi.Error
	if err := c.Call(ctx5(t), "pane.read", nil, nil); !errors.As(err, &je) || je.Code != "unsupported_method" {
		t.Errorf("method outside Welcome.Methods: %v", err)
	}
	if err := c.Call(ctx5(t), "tab.create", map[string]any{"x": make(chan int)}, nil); err == nil {
		t.Error("unmarshalable params sent")
	}
	if err := c.Call(ctx5(t), "tab.create", map[string]string{"label": strings.Repeat("x", 1<<20)}, nil); err == nil {
		t.Error("oversized request sent")
	}
	<-checked
}

func TestCallErrors(t *testing.T) {
	path := serve(t, func(f *fake) {
		f.accept()
		r := f.readRequest()
		if string(r.Params) != "{}" {
			f.fail("nil params sent as %s", r.Params)
		}
		f.replyError(r.ID, "stale_boot")
		f.replyError(f.readRequest().ID, "surface_inactive")
		f.respondTo(f.readRequest().ID, `{"result":`)
		f.respondTo(f.readRequest().ID, `{"id":"someone-else","result":{"type":"ok"}}`)
		f.reply(f.readRequest().ID, `"not an object"`)
		f.waitClose()
	})
	c := dial(t, path, Hello{})
	for _, code := range []string{"stale_boot", "surface_inactive"} {
		var je *jsonapi.Error
		err := c.Call(ctx5(t), "tab.create", nil, nil)
		if !errors.As(err, &je) || je.Code != code || je.Message != "m" {
			t.Errorf("got %v, want %s", err, code)
		}
	}
	if err := c.Call(ctx5(t), "tab.create", nil, nil); err == nil || !strings.Contains(err.Error(), "invalid endpoint response") {
		t.Errorf("invalid JSON: %v", err)
	}
	if err := c.Call(ctx5(t), "tab.create", nil, nil); err == nil || !strings.Contains(err.Error(), "someone-else") {
		t.Errorf("mismatched id: %v", err)
	}
	var out struct{ Type string }
	if err := c.Call(ctx5(t), "tab.create", nil, &out); err == nil {
		t.Error("result into the wrong type decoded")
	}
}

func TestSetSurfaceActive(t *testing.T) {
	path := serve(t, func(f *fake) {
		f.accept()
		r := f.readRequest()
		if r.Method != "client_shell.surface.set" || string(r.Params) != `{"active":true}` {
			f.fail("request %+v", r)
		}
		f.reply(r.ID, `{"type":"client_shell_surface_set","active":true,"projection_revision":2}`)
		f.reply(f.readRequest().ID, `{"type":"client_shell_surface_set","active":true,"projection_revision":3}`)
		f.waitClose()
	})
	c := dial(t, path, Hello{})
	if err := c.SetSurfaceActive(ctx5(t), true); err != nil {
		t.Fatal(err)
	}
	if err := c.SetSurfaceActive(ctx5(t), false); err == nil {
		t.Error("a reply with the wrong state succeeded")
	}
}

func TestHealthPing(t *testing.T) {
	path := serve(t, func(f *fake) {
		f.accept()
		kind, token := f.control()
		if kind != kindPing || token == "" {
			f.fail("got %s %q", kind, token)
		}
		f.send(&Control{Kind: kindPong, Data: "unrelated"})
		f.send(&Control{Kind: kindPong, Data: token})
		f.control() // a ping that is never answered
		f.waitClose()
	})
	c := dial(t, path, Hello{})
	if err := c.HealthPing(ctx5(t)); err != nil {
		t.Fatal(err)
	}
	skipSnapshot(t, c)
	if m := next(t, c); !reflect.DeepEqual(m, &Control{Kind: kindPong, Data: "unrelated"}) {
		t.Errorf("unawaited pong: %+v", m)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := c.HealthPing(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("unanswered ping: %v", err)
	}
	c.mu.Lock()
	n := len(c.pings)
	c.mu.Unlock()
	if n != 0 {
		t.Errorf("%d pings still awaited", n)
	}

	c.Welcome.Capabilities = nil
	if err := c.HealthPing(ctx5(t)); err == nil || !strings.Contains(err.Error(), "health_check") {
		t.Errorf("without the capability: %v", err)
	}
}

// Every Message type arrives on Events as sent.
func TestEvents(t *testing.T) {
	msgs := []Message{
		&Shutdown{Reason: "detached"}, &Shutdown{},
		&Notify{Kind: NotifySystemToast, Message: "done", Body: "body"},
		&Clipboard{Data: "aGk="},
		&WindowTitle{Title: "t"}, &WindowTitle{},
		&MouseCaptureState{Enabled: true, SGRPixels: true},
		&Bell{Count: 300},
		&Surface{BootID: "boot-1", ProjectionRevision: 1, SurfaceRevision: 1, Frame: FrameData{
			Cells: []CellData{{Symbol: "a"}, {Symbol: "b"}}, Width: 2, Height: 1, Graphics: []byte("g"),
		}},
		&SurfacePatch{BootID: "boot-1", ProjectionRevision: 1, BaseSurfaceRevision: 1, SurfaceRevision: 2},
		&Notification{
			Kind: NeedsAttention, Title: "codex needs attention", Body: "repo · 1",
			Sound: SoundRequest, Agent: "codex", WorkspaceID: "w1", TabID: "w1:t1", PaneID: "w1:p1",
			Position: ToastTopRight,
		},
		&Notification{Kind: Custom, Title: "plain"},
		&ShellError{Message: "boom"},
		&KeyboardReportAll{Enabled: true},
		&AgentView{BootID: "boot-1", Revision: 2, View: json.RawMessage(`{"source":"s"}`)},
		&AgentCompletions{BootID: "boot-1", Revision: 2, Completions: map[string]uint64{"w1:p1": 1}},
		&Graphics{Bytes: []byte{1}},
		&Control{Kind: "endpoint.future.v9", Data: "{}"},
		&Other{Tag: 1},
		&Snapshot{BootID: "boot-2", Revision: 9},
	}
	path := serve(t, func(f *fake) {
		f.accept()
		for _, m := range msgs {
			f.send(m)
		}
		f.waitClose()
	})
	c := dial(t, path, Hello{})
	skipSnapshot(t, c)
	for _, want := range msgs {
		if got := next(t, c); !reflect.DeepEqual(got, want) {
			t.Errorf("got %#v, want %#v", got, want)
		}
	}
	if c.BootID() != "boot-2" {
		t.Errorf("BootID %q after a new snapshot", c.BootID())
	}
}

func TestEventsEnd(t *testing.T) {
	for _, tc := range []struct {
		name string
		send func(f *fake)
		want string
	}{
		{"server closed", func(f *fake) {}, "EOF"},
		{"malformed", func(f *fake) { f.sendRaw([]byte{sNotify, 3}) }, "malformed message 4"},
		{"trailing bytes", func(f *fake) { f.sendRaw([]byte{sBell, 1, 0}) }, "malformed"},
		{"bad tag", func(f *fake) { f.sendRaw([]byte{254}) }, "malformed"},
		{"bad snapshot", func(f *fake) { f.send(&Control{Kind: kindSnapshot, Data: "{"}) }, "invalid shell.snapshot.v1"},
		{"oversized", func(f *fake) { f.nc.Write([]byte{0, 0, 0, 3}) }, "exceeds"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := serve(t, func(f *fake) {
				f.accept()
				tc.send(f)
			})
			c := dial(t, path, Hello{})
			skipSnapshot(t, c)
			if m, ok := <-c.Events(); ok {
				t.Fatalf("got %T, want Events closed", m)
			}
			if err := c.Err(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("Err %v, want %q", err, tc.want)
			}
			if err := c.Call(ctx5(t), "tab.create", nil, nil); err == nil || !strings.Contains(err.Error(), "closed") {
				t.Errorf("Call after the end: %v", err)
			}
			if err := c.HealthPing(ctx5(t)); err == nil {
				t.Error("HealthPing after the end succeeded")
			}
		})
	}
}

func TestClose(t *testing.T) {
	const bells = 100 // more than Events buffers, so the read loop blocks
	requested := make(chan struct{})
	path := serve(t, func(f *fake) {
		f.accept()
		for range bells {
			f.send(&Bell{Count: 1})
		}
		f.read() // the Call and the HealthPing, in either order
		f.read()
		close(requested)
		f.waitClose()
	})
	c := dial(t, path, Hello{})
	errs := make(chan error, 2)
	go func() { errs <- c.Call(context.Background(), "tab.create", nil, nil) }()
	go func() { errs <- c.HealthPing(context.Background()) }()
	<-requested
	if err := c.Close(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := <-errs; !errors.Is(err, net.ErrClosed) {
			t.Errorf("pending call: %v", err)
		}
	}
	n := 0
	for range c.Events() {
		n++
	}
	if n == 0 || n > bells {
		t.Errorf("%d events buffered before Close", n)
	}
	if !errors.Is(c.Err(), net.ErrClosed) {
		t.Errorf("Err %v", c.Err())
	}
	if err := c.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
	if err := c.Focus(true); err == nil {
		t.Error("send after Close succeeded")
	}
	if err := c.Call(ctx5(t), "tab.create", nil, nil); !errors.Is(err, net.ErrClosed) {
		t.Errorf("Call after Close: %v", err)
	}
}

// Each sender writes the ClientMessage bincode that Herdr decodes.
func TestSenders(t *testing.T) {
	geo := &MouseGeometry{Cols: 80, Rows: 24, WidthPx: 640, HeightPx: 384}
	cases := []struct {
		name string
		send func(c *Conn) error
		want []byte
	}{
		{"PaneInput text and paste", func(c *Conn) error {
			return c.PaneInput("p", TextCommit("ab"), Paste("c"))
		}, []byte{13, 1, 'p', 2, 1, 2, 'a', 'b', 3, 1, 'c'}},
		{"PopupInput F key", func(c *Conn) error {
			return c.PopupInput("t", Key{
				Code: KeyF, F: 5, Mods: ModAlt, Kind: KeyRepeat,
				RepeatCount: 300, GeneratedText: "x",
			})
		}, []byte{14, 1, 't', 1, 0, 16, 5, 4, 1, 251, 0x2C, 0x01, 0, 1, 1, 'x', 0, 0, 0}},
		{"PaneInput Enter and wide char", func(c *Conn) error {
			return c.PaneInput("p", Key{Code: KeyEnter}, Key{Code: KeyChar, Char: '中', Mods: ModHyper | ModMeta})
		}, []byte{
			13, 1, 'p', 2, 0, 1, 0, 0, 0, 0, 0, 0, 0, 0,
			0, 15, 0xE4, 0xB8, 0xAD, 48, 0, 0, 0, 0, 0, 0, 0,
		}},
		{"PaneInput mouse", func(c *Conn) error {
			return c.PaneInput("p",
				Mouse{
					Kind: MouseDown, Button: ButtonRight, Column: 3, Row: 4, Pixels: true, X: 30, Y: 1000,
					Geometry: geo, Mods: ModShift,
				},
				Mouse{Kind: ScrollUp, Column: 1, Row: 2, Lines: 3})
		}, []byte{
			13, 1, 'p', 2,
			2, 0, 1, 1, 30, 251, 0xE8, 0x03, 3, 4, 1, 80, 24, 251, 0x80, 0x02, 251, 0x80, 0x01, 1, 0,
			2, 4, 0, 1, 2, 0, 0, 3,
		}},
		{
			"Resize", func(c *Conn) error { return c.Resize(80, 24, 8, 16, false) },
			[]byte{12, 8, 16, 80, 24, 0},
		},
		{"Focus", func(c *Conn) error { return c.Focus(true) }, []byte{18, 1}},
		{"MouseCapture", func(c *Conn) error { return c.MouseCapture(false) }, []byte{19, 0}},
		{"HostTheme default color", func(c *Conn) error {
			return c.HostTheme(DefaultColor{Background: true, Color: RGB{1, 2, 3}})
		}, []byte{17, 0, 1, 1, 2, 3}},
		{"HostTheme palette", func(c *Conn) error {
			return c.HostTheme(PaletteColors{{Index: 4, Color: RGB{10, 20, 30}}})
		}, []byte{17, 1, 1, 4, 10, 20, 30}},
		{
			"HostTheme appearance", func(c *Conn) error { return c.HostTheme(AppearanceLight) },
			[]byte{17, 2, 1},
		},
		{"ClipboardImage popup", func(c *Conn) error {
			return c.ClipboardImage(ImageTarget{Popup: true, ID: "t"}, "png", []byte{1, 2})
		}, []byte{2, 2, 1, 't', 3, 'p', 'n', 'g', 2, 1, 2}},
		{
			"SendControl", func(c *Conn) error { return c.SendControl("k", "d") },
			[]byte{20, 1, 'k', 1, 'd'},
		},
	}
	frames := make(chan []byte, len(cases))
	path := serve(t, func(f *fake) {
		f.accept()
		for range cases {
			frames <- f.read()
		}
		f.waitClose()
	})
	c := dial(t, path, Hello{})
	for _, tc := range cases {
		if err := tc.send(c); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := <-frames; !bytes.Equal(got, tc.want) {
			t.Errorf("%s:\n got %v\nwant %v", tc.name, got, tc.want)
		}
	}
	for _, s := range [][4]uint32{{0, 24, 8, 16}, {80, 0, 8, 16}, {4097, 1, 0, 0}, {1001, 1000, 0, 0}, {80, 24, 4097, 16}, {80, 24, 8, 4097}} {
		if err := c.Resize(uint16(s[0]), uint16(s[1]), s[2], s[3], false); err == nil {
			t.Errorf("Resize%v sent", s)
		}
	}
}

func testSurface() *Surface {
	cells := make([]CellData, 6)
	for i := range cells {
		cells[i].Symbol = " "
	}
	return &Surface{
		BootID: "b", ProjectionRevision: 2, SurfaceRevision: 5,
		Frame: FrameData{Cells: cells, Width: 3, Height: 2},
		Panes: []SurfacePane{{PaneID: "w1:p1", ContentRevision: 1}, {PaneID: "w1:p2", ContentRevision: 1}},
	}
}

func TestApply(t *testing.T) {
	good := func() *SurfacePatch {
		return &SurfacePatch{
			BootID: "b", ProjectionRevision: 2, BaseSurfaceRevision: 5, SurfaceRevision: 6,
			Rows:  []PatchRow{{X: 1, Y: 1, Cells: []CellData{{Symbol: "o"}, {Symbol: "k"}}}},
			Panes: []SurfacePane{{PaneID: "w1:p2", ContentRevision: 2}}, Cursor: &Cursor{X: 2, Y: 1, Visible: true},
		}
	}
	for _, tc := range []struct {
		name   string
		change func(p *SurfacePatch, s *Surface)
	}{
		{"boot", func(p *SurfacePatch, s *Surface) { p.BootID = "a" }},
		{"projection", func(p *SurfacePatch, s *Surface) { p.ProjectionRevision = 3 }},
		{"base revision", func(p *SurfacePatch, s *Surface) { p.BaseSurfaceRevision = 4 }},
		{"row too wide", func(p *SurfacePatch, s *Surface) { p.Rows[0].X = 2 }},
		{"row below", func(p *SurfacePatch, s *Surface) { p.Rows = append(p.Rows, PatchRow{Y: 2}) }},
		{"unknown pane", func(p *SurfacePatch, s *Surface) { p.Panes[0].PaneID = "w1:p9" }},
		{"cell count", func(p *SurfacePatch, s *Surface) { s.Frame.Cells = s.Frame.Cells[:5] }},
	} {
		s, p := testSurface(), good()
		tc.change(p, s)
		before := *s
		before.Frame.Cells = slices.Clone(s.Frame.Cells)
		before.Panes = slices.Clone(s.Panes)
		if err := s.Apply(p); err == nil {
			t.Errorf("%s: applied", tc.name)
		}
		if !reflect.DeepEqual(*s, before) {
			t.Errorf("%s: surface changed", tc.name)
		}
	}

	s := testSurface()
	if err := s.Apply(good()); err != nil {
		t.Fatal(err)
	}
	var row1 string
	for _, c := range s.Frame.Cells[3:] {
		row1 += c.Symbol
	}
	if row1 != " ok" || s.Frame.Cells[1].Symbol != " " || s.SurfaceRevision != 6 ||
		s.Panes[1].ContentRevision != 2 || s.Panes[0].ContentRevision != 1 ||
		*s.Frame.Cursor != (Cursor{X: 2, Y: 1, Visible: true}) {
		t.Errorf("applied surface %+v", s)
	}
}

func TestDecodeColor(t *testing.T) {
	for _, tc := range []struct {
		v    uint32
		want Color
	}{
		{0x00000000, Color{Kind: ColorNamed}},
		{0x00000002, Color{Kind: ColorNamed, Index: 2}},
		{0x00000010, Color{Kind: ColorNamed, Index: 16}},
		{0x00000011, Color{Kind: ColorNamed}}, // unknown named color: the default
		{0x010000DC, Color{Kind: ColorIndexed, Index: 220}},
		{0x02FF8000, Color{Kind: ColorRGB, R: 255, G: 128}},
		{0x03123456, Color{Kind: ColorNamed}}, // unknown tag: the default
	} {
		if got := DecodeColor(tc.v); got != tc.want {
			t.Errorf("DecodeColor(%#x) = %+v, want %+v", tc.v, got, tc.want)
		}
	}
}

// Surfaces with every optional part decode back to themselves.
func TestSurfaceRoundTrip(t *testing.T) {
	asset := GraphicsAssetKey{
		Source:     GraphicsSource{Kind: SourcePaneLayer, PaneID: "w1:p1", LayerID: "l"},
		ImageWidth: 1, ImageHeight: 1, Format: FormatPNG, DataLen: 3, DataFingerprint: 1 << 40,
	}
	popupKey := GraphicsAssetKey{Source: GraphicsSource{
		Kind:   SourceTerminal,
		Target: GraphicsTarget{Popup: true, ID: "t1"}, ImageID: 70000,
	}, Format: FormatRGB}
	s := &Surface{
		BootID: "b", ProjectionRevision: 1 << 33, SurfaceRevision: 300,
		Frame: FrameData{
			Cells: []CellData{{Symbol: "x", FG: 0x02010203, Hyperlink: ptr(uint32(1))}},
			Width: 1, Height: 1, Hyperlinks: []string{"a", "b"},
		},
		Panes: []SurfacePane{{
			PaneID: "w1:p1", ContentRevision: 9, Rect: Rect{0, 0, 10, 5},
			InnerRect: Rect{1, 1, 8, 3}, ScrollbarRect: &Rect{9, 1, 1, 3},
			Scroll:  &ScrollMetrics{OffsetFromBottom: 1, MaxOffsetFromBottom: 500, ViewportRows: 3},
			Focused: true, MouseReporting: true, SGRPixelMouse: true, AlternateScreenActive: true,
			PixelWidth: 640, PixelHeight: 480,
		}},
		Splits: []Split{{
			Direction: SplitVertical, Pos: 5, Area: Rect{5, 0, 1, 5}, HitRect: Rect{4, 0, 3, 5},
			Path: []bool{true, false},
		}},
		Popup: &Popup{
			TerminalID: "t1", Title: "pop", Width: &PopupSize{N: 300},
			Height: &PopupSize{Percent: true, N: 50}, Frame: FrameData{
				Width: 0, Height: 0,
				Cursor: &Cursor{Shape: 5},
			}, MouseReporting: true, PixelWidth: 1, PixelHeight: 2,
		},
		Graphics: GraphicsScene{
			Assets: []GraphicsAsset{{Key: asset, Data: []byte{1, 2, 3}}},
			Placements: []GraphicsPlacement{{
				Asset: popupKey, Z: -1 << 31, ScrollbackOffset: 7,
				XOffset: 1, YOffset: 2, SourceX: 3, SourceY: 4,
			}},
			RetainedAssets: []GraphicsAssetKey{asset, popupKey},
		},
	}
	e := encodeMessage(s)
	m, err := decodeMessage(e)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(m, s) {
		t.Errorf("decoded\n%#v\nwant\n%#v", m, s)
	}
	for n := range len(e) {
		if _, err := decodeMessage(e[:n]); err == nil {
			t.Fatalf("decoded %d of %d bytes", n, len(e))
		}
	}
	// The last "t1" is the popup target of the last retained asset; the byte
	// before it is its SurfaceGraphicsTarget variant.
	i := bytes.LastIndex(e, []byte{2, 't', '1'})
	bad := slices.Clone(e)
	bad[i-1] = 2
	if _, err := decodeMessage(bad); err == nil {
		t.Error("unknown graphics target decoded")
	}
}

// Messages that arrive before the first snapshot wait in Dial and still reach
// Events; Close ends a read loop blocked handing them over.
func TestDialQueue(t *testing.T) {
	const bells = 100
	path := serve(t, func(f *fake) {
		f.readHello()
		f.welcome(testWelcome())
		f.send(&responseChunk{BootID: "boot-1", RequestID: "stray", Final: true})
		for range bells {
			f.send(&Bell{Count: 1})
		}
		f.send(&Snapshot{BootID: "boot-1"})
		f.waitClose()
	})
	c := dial(t, path, Hello{})
	if m := next(t, c); !reflect.DeepEqual(m, &Bell{Count: 1}) {
		t.Errorf("first event %+v", m)
	}
	c.Close()
	n := 0
	for range c.Events() {
		n++
	}
	if n >= bells || !errors.Is(c.Err(), net.ErrClosed) {
		t.Errorf("%d more events, Err %v", n, c.Err())
	}
}

// brokenWrites fails every Write once broken is set.
type brokenWrites struct {
	net.Conn
	broken atomic.Bool
}

func (b *brokenWrites) Write(p []byte) (int, error) {
	if b.broken.Load() {
		return 0, syscall.EPIPE
	}
	return b.Conn.Write(p)
}

// A failed write fails the Call or HealthPing that made it, frees the
// connection for the next one, and leaves the read loop running.
func TestSendFails(t *testing.T) {
	path := serve(t, func(f *fake) {
		f.accept()
		f.waitClose()
	})
	nc, err := net.Dial("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	bw := &brokenWrites{Conn: nc}
	c, err := handshake(ctx5(t), bw, Hello{})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	bw.broken.Store(true)
	for _, method := range []string{"tab.create", "tab.focus"} {
		if err := c.Call(ctx5(t), method, nil, nil); !errors.Is(err, syscall.EPIPE) {
			t.Errorf("%s: %v", method, err)
		}
	}
	if err := c.HealthPing(ctx5(t)); !errors.Is(err, syscall.EPIPE) {
		t.Errorf("HealthPing: %v", err)
	}
	c.mu.Lock()
	n, k := len(c.pings), c.call
	c.mu.Unlock()
	if n != 0 || k != nil {
		t.Errorf("%d pings and call %v still awaited", n, k)
	}
	if c.Err() != nil {
		t.Errorf("read loop ended: %v", c.Err())
	}

	nc, err = net.Dial("unix", serve(t, func(f *fake) {}))
	if err != nil {
		t.Fatal(err)
	}
	defer nc.Close()
	bw = &brokenWrites{Conn: nc}
	bw.broken.Store(true)
	if _, err := handshake(ctx5(t), bw, Hello{}); !errors.Is(err, syscall.EPIPE) {
		t.Errorf("hello: %v", err)
	}
}

func TestSetSurfaceActiveUnavailable(t *testing.T) {
	c := &Conn{}
	var je *jsonapi.Error
	if err := c.SetSurfaceActive(ctx5(t), true); !errors.As(err, &je) || je.Code != "unsupported_method" {
		t.Errorf("got %v", err)
	}
}

func TestMessageTypes(t *testing.T) {
	for _, m := range []Message{
		&Snapshot{}, &Surface{}, &SurfacePatch{}, &Notification{},
		&MouseCaptureState{}, &KeyboardReportAll{}, &Bell{}, &Clipboard{}, &WindowTitle{}, &Notify{},
		&ShellError{}, &AgentView{}, &AgentCompletions{}, &Graphics{}, &Control{}, &Shutdown{},
		&Other{}, &responseChunk{},
	} {
		m.message()
	}
	defer func() {
		if recover() == nil {
			t.Error("encoding invalid JSON did not panic")
		}
	}()
	encodeMessage(&AgentView{View: json.RawMessage("{")})
}
