package term

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/elonnzhang/herdrex/pkg/herdr"
	"github.com/elonnzhang/herdrex/pkg/herdr/internal/wire"
	"github.com/elonnzhang/herdrex/pkg/herdr/jsonapi"
)

// recorder is a net.Conn that keeps what is written to it. Only Write is used.
type recorder struct {
	net.Conn
	buf bytes.Buffer
}

func (r *recorder) Write(p []byte) (int, error) { return r.buf.Write(p) }

// sent calls send on a recording Conn and returns the one payload it wrote.
func sent(t *testing.T, send func(*Conn) error) []byte {
	t.Helper()
	var r recorder
	if err := send(&Conn{c: &r}); err != nil {
		t.Fatal(err)
	}
	p, err := wire.ReadFrame(&r.buf, MaxFrame)
	if err != nil {
		t.Fatal(err)
	}
	if r.buf.Len() != 0 {
		t.Fatalf("%d bytes after the frame", r.buf.Len())
	}
	return p
}

func TestHelloEncoding(t *testing.T) {
	var e wire.Enc
	e.U(cHello).U(22)
	putSize(&e, Size{Cols: 100, Rows: 30, CellWidthPx: 15, CellHeightPx: 36})
	if want := []byte{0, 22, 100, 30, 15, 36, 0}; !bytes.Equal(e, want) {
		t.Fatalf("hello = % x, want % x", []byte(e), want)
	}
}

func TestSenders(t *testing.T) {
	cases := []struct {
		name string
		send func(*Conn) error
		want []byte
	}{
		{
			"Input", func(c *Conn) error { return c.Input([]byte("hi")) },
			[]byte{1, 2, 'h', 'i'},
		},
		{"Resize", func(c *Conn) error {
			return c.Resize(Size{Cols: 300, Rows: 40, CellWidthPx: 8, CellHeightPx: 16, PixelMouse: true})
		}, []byte{3, 251, 0x2c, 0x01, 40, 8, 16, 1}},
		{
			"Control", func(c *Conn) error { return c.Control("w1:p3", true) },
			[]byte{8, 5, 'w', '1', ':', 'p', '3', 1},
		},
		{
			"Observe", func(c *Conn) error { return c.Observe("w1:p3") },
			[]byte{7, 5, 'w', '1', ':', 'p', '3'},
		},
		{
			"Attach", func(c *Conn) error { return c.Attach("t-9", false) },
			[]byte{5, 3, 't', '-', '9', 0},
		},
		{
			"Detach", func(c *Conn) error { return c.Detach() },
			[]byte{4},
		},
		// AttachMouse, Down(Right), Cell{10, 300}, no geometry, ctrl, 1 line
		{"MouseDown", func(c *Conn) error {
			return c.Mouse(Mouse{Kind: MouseDown, Button: ButtonRight, Column: 10, Row: 300, Mods: ModCtrl})
		}, []byte{16, 0, 1, 0, 10, 251, 0x2c, 0x01, 0, 2, 1}},
		{
			"MouseScroll", func(c *Conn) error { return c.Mouse(Mouse{Kind: ScrollUp, Column: 1, Row: 2, Lines: 3}) },
			[]byte{16, 4, 0, 1, 2, 0, 0, 3},
		},
		// Moved and the scroll kinds carry no button, so Button is not sent or checked.
		{"MouseMovedButton", func(c *Conn) error {
			return c.Mouse(Mouse{Kind: MouseMoved, Button: 9, Column: 4, Row: 5, Mods: ModShift})
		}, []byte{16, 3, 0, 4, 5, 0, 1, 1}},
		{
			"MouseScrollRightButton", func(c *Conn) error { return c.Mouse(Mouse{Kind: ScrollRight, Button: 9}) },
			[]byte{16, 7, 0, 0, 0, 0, 0, 1},
		},
		{
			"MouseDragMiddle", func(c *Conn) error { return c.Mouse(Mouse{Kind: MouseDrag, Button: ButtonMiddle}) },
			[]byte{16, 2, 2, 0, 0, 0, 0, 0, 1},
		},
		// AttachScroll, Wheel, Up, 3 lines, no column, no row, no modifiers
		{
			"ScrollWheel", func(c *Conn) error { return c.Scroll(Scroll{Source: Wheel, Direction: Up, Lines: 3}) },
			[]byte{6, 0, 0, 3, 0, 0, 0},
		},
		// the value of Herdr's client_attach_scroll_roundtrip test
		{"ScrollWheelAt", func(c *Conn) error {
			return c.Scroll(Scroll{
				Source: Wheel, Direction: Up, Lines: 3,
				Column: new(uint16(12)), Row: new(uint16(7)), Mods: ModAlt,
			})
		}, []byte{6, 0, 0, 3, 1, 12, 1, 7, 4}},
		{"ScrollWheelDown", func(c *Conn) error {
			return c.Scroll(Scroll{Source: Wheel, Direction: Down, Lines: 300, Row: new(uint16(0))})
		}, []byte{6, 0, 1, 251, 0x2c, 0x01, 0, 1, 0, 0}},
		// AttachScroll, PageKey{"\x1b[6~"}, Down, 20 lines, no position
		{"ScrollPageKey", func(c *Conn) error {
			return c.Scroll(Scroll{Source: PageKey, Input: []byte("\x1b[6~"), Direction: Down, Lines: 20})
		}, []byte{6, 1, 4, 0x1b, '[', '6', '~', 1, 20, 0, 0, 0}},
		// ClipboardImage, DirectTerminal, "png", 4 bytes
		{
			"ClipboardImage", func(c *Conn) error { return c.ClipboardImage("png", []byte{0x89, 'P', 'N', 'G'}) },
			[]byte{2, 0, 3, 'p', 'n', 'g', 4, 0x89, 'P', 'N', 'G'},
		},
	}
	for _, c := range cases {
		if got := sent(t, c.send); !bytes.Equal(got, c.want) {
			t.Errorf("%s = % x, want % x", c.name, got, c.want)
		}
	}
}

// TestClipboardImageLayout checks the field order against the digest Herdr
// pins for the same image sent to Pane("w1:p1") (src/protocol/wire.rs:2366).
func TestClipboardImageLayout(t *testing.T) {
	p := sent(t, func(c *Conn) error { return c.ClipboardImage("png", []byte{0x89, 'P', 'N', 'G'}) })
	var e wire.Enc
	e.U(cClipImage).U(1).Str("w1:p1") // ClientClipboardImageTarget::Pane
	sum := sha256.Sum256(append(e, p[2:]...))
	if got, want := hex.EncodeToString(sum[:]), "1c02110be0671faf318b3f4d8f507748d5a0a98cd474832669c5290d97ef19ab"; got != want {
		t.Fatalf("digest %s, want %s", got, want)
	}
}

func TestSendRejects(t *testing.T) {
	big := make([]byte, MaxClipboardImage+1)
	cases := []struct {
		name string
		send func(*Conn) error
		too  bool // ErrTooLarge
	}{
		{"Input", func(c *Conn) error { return c.Input(big[:MaxInput+1]) }, true},
		{"ClipboardImage", func(c *Conn) error { return c.ClipboardImage("png", big) }, true},
		{"Scroll frame", func(c *Conn) error {
			return c.Scroll(Scroll{Source: PageKey, Input: make([]byte, wire.MaxGraphicsFrame)})
		}, true},
		{"Scroll source", func(c *Conn) error { return c.Scroll(Scroll{Source: PageKey + 1}) }, false},
		{"Scroll direction", func(c *Conn) error { return c.Scroll(Scroll{Direction: Down + 1}) }, false},
		// Herdr fails to decode these and drops the connection.
		{"Mouse kind", func(c *Conn) error { return c.Mouse(Mouse{Kind: ScrollRight + 1}) }, false},
		{"MouseDown button", func(c *Conn) error { return c.Mouse(Mouse{Kind: MouseDown, Button: ButtonMiddle + 1}) }, false},
		{"MouseUp button", func(c *Conn) error { return c.Mouse(Mouse{Kind: MouseUp, Button: 255}) }, false},
		{"MouseDrag button", func(c *Conn) error { return c.Mouse(Mouse{Kind: MouseDrag, Button: 3}) }, false},
	}
	for _, c := range cases {
		var r recorder
		err := c.send(&Conn{c: &r})
		if err == nil || errors.Is(err, ErrTooLarge) != c.too {
			t.Errorf("%s: err = %v", c.name, err)
		}
		if r.buf.Len() != 0 {
			t.Errorf("%s wrote %d bytes", c.name, r.buf.Len())
		}
	}
	// The limits themselves are allowed.
	if p := sent(t, func(c *Conn) error { return c.Input(big[:MaxInput]) }); len(p) != 1+5+MaxInput {
		t.Errorf("Input of MaxInput bytes sent %d", len(p))
	}
	if p := sent(t, func(c *Conn) error { return c.ClipboardImage("png", big[:MaxClipboardImage]) }); len(p) != 2+4+5+MaxClipboardImage {
		t.Errorf("ClipboardImage of MaxClipboardImage bytes sent %d", len(p))
	}
}

// next decodes one framed payload.
func next(payload []byte) (any, error) {
	c := &Conn{r: bufio.NewReader(bytes.NewReader(wire.Frame(payload)))}
	return c.Next()
}

func TestNext(t *testing.T) {
	cases := []struct {
		name    string
		payload []byte
		want    any
	}{
		{"Welcome", []byte{0, 22, 1, 0}, &Welcome{Version: 22, Encoding: 1}},
		{"WelcomeError", []byte{0, 22, 1, 1, 3, 'b', 'a', 'd'}, &Welcome{Version: 22, Encoding: 1, Error: "bad"}},
		{
			"Terminal",
			[]byte{1, 251, 0x2c, 0x01, 80, 24, 1, 3, 'a', 'b', 'c'},
			&Frame{Seq: 300, Width: 80, Height: 24, Full: true, Bytes: []byte("abc")},
		},
		{"Shutdown", append([]byte{3, 1, 8}, "detached"...), &Shutdown{Reason: "detached"}},
		{"ShutdownNoReason", []byte{3, 0}, &Shutdown{}},
		{"Clipboard", append([]byte{5, 4}, "aGk="...), &Clipboard{Data: "aGk="}},
		{"WindowTitle", []byte{6, 1, 2, 'h', 'i'}, &WindowTitle{Title: "hi"}},
		{"WindowTitleReset", []byte{6, 0}, &WindowTitle{}},
		{"ReloadSoundConfig", []byte{7}, &ReloadSoundConfig{}},
		{"MouseCapture", []byte{8, 1, 0}, &MouseCapture{Enabled: true}},
		{"MouseCapturePixels", []byte{8, 1, 1}, &MouseCapture{Enabled: true, SGRPixels: true}},
		{"Bell", []byte{9, 2}, &Bell{Count: 2}},
		{"KeyboardProtocol", []byte{16, 31, 2}, &KeyboardProtocol{Flags: 31, ModifyOtherKeys: 2}},
		{"KeyboardProtocolWide", []byte{16, 251, 0x2c, 0x01, 0}, &KeyboardProtocol{Flags: 300}},
		{"Notify", []byte{4, 1, 0, 0, 0}, &Other{Tag: 4}},
		{"EndpointControl", []byte{20, 0, 0}, &Other{Tag: 20}},
	}
	for _, c := range cases {
		got, err := next(c.payload)
		if err != nil {
			t.Errorf("%s: %v", c.name, err)
		} else if !reflect.DeepEqual(got, c.want) {
			t.Errorf("%s = %#v, want %#v", c.name, got, c.want)
		}
	}
}

func TestNextMalformed(t *testing.T) {
	for name, p := range map[string][]byte{
		"empty":                       {},
		"truncated Terminal":          {1, 5, 80},
		"trailing byte":               {9, 2, 0},
		"ReloadSoundConfig with data": {7, 0},
		"KeyboardProtocol short":      {16, 31},
		"Bell over u16":               {9, 252, 0, 0, 1, 0},
		"bad bool":                    {8, 2, 0},
		"short string":                {5, 4, 'a'},
		"invalid UTF-8":               {5, 1, 0xff},
		"bad Option tag":              {3, 2},
	} {
		if m, err := next(p); err == nil {
			t.Errorf("%s: decoded %#v", name, m)
		}
	}
	c := &Conn{r: bufio.NewReader(strings.NewReader(""))}
	if _, err := c.Next(); err != io.EOF {
		t.Errorf("empty stream: %v, want EOF", err)
	}
}

// TestLargeFrames checks the 32 MiB limit: a Terminal frame with a large
// inline image decodes, and a longer length prefix is refused.
func TestLargeFrames(t *testing.T) {
	var e wire.Enc
	e.U(sTerminal).U(1).U(80).U(24).Bool(true).Bytes(bytes.Repeat([]byte{'x'}, 3<<20))
	m, err := next(e)
	if err != nil {
		t.Fatal(err)
	}
	if f, ok := m.(*Frame); !ok || len(f.Bytes) != 3<<20 {
		t.Fatalf("decoded %T", m)
	}
	var h [4]byte
	binary.LittleEndian.PutUint32(h[:], 32<<20+1)
	c := &Conn{r: bufio.NewReader(bytes.NewReader(h[:]))}
	if _, err := c.Next(); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("33 MiB frame: %v", err)
	}
}

// fakeServer accepts one connection on a Unix socket, reads the hello and
// hands both to serve. It returns the socket path.
func fakeServer(t *testing.T, serve func(hello []byte, nc net.Conn)) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "hxt") // macOS socket paths must stay under 104 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	t.Cleanup(func() { <-done })
	t.Cleanup(func() { ln.Close() })
	go func() {
		defer close(done)
		nc, err := ln.Accept()
		if err != nil {
			return
		}
		defer nc.Close()
		hello, err := wire.ReadFrame(nc, wire.MaxFrame)
		if err != nil {
			t.Errorf("reading hello: %v", err)
			return
		}
		serve(hello, nc)
	}()
	return sock
}

func welcome(version uint32, reason string) []byte {
	var e wire.Enc
	e.U(sWelcome).U(uint64(version)).U(1).Opt(reason != "")
	if reason != "" {
		e.Str(reason)
	}
	return e
}

func TestDialRefused(t *testing.T) {
	older := "client version 1 is older than server version 22; please upgrade your herdr client"
	handoff := "live update in progress; reconnect after handoff completes"
	var shutdown, bell wire.Enc
	shutdown.U(sShutdown).Opt(true).Str(handoff)
	bell.U(sBell).U(1)
	cases := []struct {
		name  string
		proto uint32
		reply []byte // nil closes without a reply
		check func(error) bool
	}{
		{"version", 1, welcome(22, older), func(err error) bool {
			var ve *VersionError
			return errors.As(err, &ve) && *ve == VersionError{Client: 1, Server: 22, Message: older} &&
				err.Error() == "herdr: "+older
		}},
		{"other error", 22, welcome(22, "expected TerminalHello"), func(err error) bool {
			var ve *VersionError
			return !errors.As(err, &ve) && err.Error() == "herdr: expected TerminalHello"
		}},
		{"shutdown", 22, shutdown, func(err error) bool { return err.Error() == "herdr: "+handoff }},
		{"unexpected", 22, bell, func(err error) bool { return strings.Contains(err.Error(), "*term.Bell") }},
		{"closed", 22, nil, func(err error) bool { return err == io.EOF }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			sock := fakeServer(t, func(hello []byte, nc net.Conn) {
				if want := []byte{0, byte(c.proto), 80, 24, 8, 16, 1}; !bytes.Equal(hello, want) {
					t.Errorf("hello = % x, want % x", hello, want)
				}
				if c.reply != nil {
					nc.Write(wire.Frame(c.reply))
				}
			})
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			conn, err := Dial(ctx, sock, c.proto, Size{Cols: 80, Rows: 24, CellWidthPx: 8, CellHeightPx: 16, PixelMouse: true})
			if conn != nil || err == nil || !c.check(err) {
				t.Fatalf("Dial = %v, %v", conn, err)
			}
		})
	}
}

// TestDialContext checks that Dial gives up when ctx ends during the
// handshake, against a server that reads the hello and never replies.
func TestDialContext(t *testing.T) {
	cases := []struct {
		name string
		ctx  func() (context.Context, context.CancelFunc)
		want error
	}{
		{"cancel", func() (context.Context, context.CancelFunc) {
			ctx, cancel := context.WithCancel(context.Background())
			time.AfterFunc(50*time.Millisecond, cancel)
			return ctx, cancel
		}, context.Canceled},
		{"deadline", func() (context.Context, context.CancelFunc) {
			return context.WithTimeout(context.Background(), 50*time.Millisecond)
		}, context.DeadlineExceeded},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			hold := make(chan struct{})
			sock := fakeServer(t, func(hello []byte, nc net.Conn) { <-hold })
			t.Cleanup(func() { close(hold) }) // runs before fakeServer's cleanups
			ctx, cancel := c.ctx()
			defer cancel()
			done := make(chan error, 1)
			go func() {
				conn, err := Dial(ctx, sock, 22, Size{Cols: 80, Rows: 24})
				if conn != nil {
					conn.Close()
				}
				done <- err
			}()
			select {
			case err := <-done:
				if !errors.Is(err, c.want) {
					t.Fatalf("Dial = %v, want %v", err, c.want)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("Dial still waiting 2s after ctx ended")
			}
		})
	}
}

// TestDialWelcome checks that a welcomed connection outlives Dial's deadline.
func TestDialWelcome(t *testing.T) {
	sock := fakeServer(t, func(hello []byte, nc net.Conn) {
		nc.Write(wire.Frame(welcome(22, "")))
		time.Sleep(200 * time.Millisecond)
		var e wire.Enc
		nc.Write(wire.Frame(*e.U(sBell).U(1)))
		if p, err := wire.ReadFrame(nc, wire.MaxFrame); err != nil || !bytes.Equal(p, []byte{cDetach}) {
			t.Errorf("after welcome read % x, %v; want Detach", p, err)
		}
	})
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	c, err := Dial(ctx, sock, 22, Size{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if m, err := c.Next(); err != nil || !reflect.DeepEqual(m, &Bell{Count: 1}) {
		t.Fatalf("Next = %#v, %v", m, err)
	}
	if err := c.Detach(); err != nil {
		t.Fatal(err)
	}
}

// live reads a real connection's messages in the background.
type live struct {
	t     *testing.T
	ctx   context.Context
	c     *Conn
	msgs  chan any // messages, then the read error
	mouse int      // MouseCapture messages seen
	keys  int      // KeyboardProtocol messages seen
}

func dialLive(t *testing.T, ctx context.Context, sock string, proto uint32, s Size) *live {
	t.Helper()
	c, err := Dial(ctx, sock, proto, s)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	l := &live{t: t, ctx: ctx, c: c, msgs: make(chan any, 4096)}
	go func() {
		for {
			m, err := c.Next()
			if err != nil {
				l.msgs <- err
				return
			}
			l.msgs <- m
		}
	}()
	return l
}

// wait returns the first message ok accepts. Any other Shutdown or a read
// error fails the test.
func (l *live) wait(what string, ok func(any) bool) any {
	l.t.Helper()
	for {
		select {
		case m := <-l.msgs:
			switch m.(type) {
			case *MouseCapture:
				l.mouse++
			case *KeyboardProtocol:
				l.keys++
			}
			if ok(m) {
				return m
			}
			switch m := m.(type) {
			case error:
				l.t.Fatalf("%s: %v", what, m)
			case *Shutdown:
				l.t.Fatalf("%s: shutdown %q", what, m.Reason)
			}
		case <-l.ctx.Done():
			l.t.Fatalf("%s: %v", what, l.ctx.Err())
		}
	}
}

func (l *live) frame(what string, ok func(*Frame) bool) *Frame {
	l.t.Helper()
	return l.wait(what, func(m any) bool { f, is := m.(*Frame); return is && ok(f) }).(*Frame)
}

func (l *live) shutdown(what, reason string) {
	l.t.Helper()
	l.wait(what, func(m any) bool { s, ok := m.(*Shutdown); return ok && s.Reason == reason })
}

func full(cols, rows uint16) func(*Frame) bool {
	return func(f *Frame) bool { return f.Full && f.Width == cols && f.Height == rows }
}

func showing(text string) func(*Frame) bool {
	return func(f *Frame) bool { return bytes.Contains(f.Bytes, []byte(text)) }
}

// TestLive runs against a real Herdr server whose API socket is
// HERDR_TEST_SOCKET. Use an isolated server (docs/TESTING.md): the test
// creates a workspace, resizes and scrolls its pane, and closes it.
func TestLive(t *testing.T) {
	sock := os.Getenv("HERDR_TEST_SOCKET")
	if sock == "" {
		t.Skip("HERDR_TEST_SOCKET not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	api := &jsonapi.Client{Socket: sock}
	var pong struct {
		Protocol uint32 `json:"protocol"`
	}
	if err := api.Call(ctx, "ping", nil, &pong); err != nil {
		t.Fatal(err)
	}
	client := herdr.ClientSocket(sock)

	_, err := Dial(ctx, client, 1, Size{Cols: 80, Rows: 24})
	if ve := (*VersionError)(nil); !errors.As(err, &ve) || ve.Client != 1 || ve.Server != pong.Protocol {
		t.Fatalf("Dial with version 1: %v; want a VersionError with server %d", err, pong.Protocol)
	}
	t.Logf("Dial with version 1: %v", err)

	type paneInfo struct {
		ID         string `json:"pane_id"`
		TerminalID string `json:"terminal_id"`
		Scroll     *struct {
			OffsetFromBottom uint64 `json:"offset_from_bottom"`
		} `json:"scroll"`
	}
	var ws struct {
		Workspace struct {
			ID string `json:"workspace_id"`
		} `json:"workspace"`
		RootPane paneInfo `json:"root_pane"`
	}
	if err := api.Call(ctx, "workspace.create", map[string]any{"label": "term-live"}, &ws); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := api.Call(ctx, "workspace.close", map[string]any{"workspace_id": ws.Workspace.ID}, nil); err != nil {
			t.Errorf("closing workspace %s: %v", ws.Workspace.ID, err)
		}
	})
	pane := ws.RootPane.ID
	get := func() paneInfo {
		var r struct {
			Pane paneInfo `json:"pane"`
		}
		if err := api.Call(ctx, "pane.get", map[string]any{"pane_id": pane}, &r); err != nil {
			t.Fatal(err)
		}
		return r.Pane
	}
	offset := func() uint64 {
		if p := get(); p.Scroll != nil {
			return p.Scroll.OffsetFromBottom
		}
		return 0
	}

	a := dialLive(t, ctx, client, pong.Protocol, Size{Cols: 70, Rows: 21, CellWidthPx: 15, CellHeightPx: 36})
	if err := a.c.Control(pane, false); err != nil {
		t.Fatal(err)
	}
	if f := a.frame("first frame", func(f *Frame) bool { return f.Full }); f.Width != 70 || f.Height != 21 {
		t.Fatalf("first frame %dx%d, want 70x21", f.Width, f.Height)
	}
	if err := a.c.Input([]byte("echo term-$((6*7))\r")); err != nil {
		t.Fatal(err)
	}
	a.frame("echo", showing("term-42"))
	if err := a.c.Resize(Size{Cols: 83, Rows: 40}); err != nil {
		t.Fatal(err)
	}
	a.frame("resize", full(83, 40))

	if err := a.c.Input([]byte("seq 1 200; echo seq-$((40+2))-done\r")); err != nil {
		t.Fatal(err)
	}
	a.frame("seq", showing("seq-42-done"))
	before := offset()
	if err := a.c.Scroll(Scroll{Source: Wheel, Direction: Up, Lines: 3}); err != nil {
		t.Fatal(err)
	}
	after := offset()
	for ; after <= before; after = offset() {
		if ctx.Err() != nil {
			t.Fatalf("scroll offset stayed at %d", after)
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Logf("scroll offset_from_bottom %d -> %d", before, after)
	if a.mouse == 0 || a.keys == 0 {
		t.Errorf("controller saw %d MouseCapture and %d KeyboardProtocol messages", a.mouse, a.keys)
	}
	if err := a.c.Detach(); err != nil {
		t.Fatal(err)
	}
	a.shutdown("detach", "detached")

	id := get().TerminalID
	if id == "" {
		t.Fatal("pane.get has no terminal_id")
	}
	t.Logf("pane %s, terminal %s", pane, id)
	b := dialLive(t, ctx, client, pong.Protocol, Size{Cols: 90, Rows: 30})
	if err := b.c.Attach(id, false); err != nil {
		t.Fatal(err)
	}
	b.frame("attach", full(90, 30))
	c := dialLive(t, ctx, client, pong.Protocol, Size{Cols: 60, Rows: 20})
	if err := c.c.Attach(id, true); err != nil {
		t.Fatal(err)
	}
	b.shutdown("takeover", "terminal attach taken over")
	c.frame("takeover", full(60, 20))

	o := dialLive(t, ctx, client, pong.Protocol, Size{Cols: 50, Rows: 10})
	if err := o.c.Observe(pane); err != nil {
		t.Fatal(err)
	}
	o.frame("observe", func(f *Frame) bool { return f.Full })
	if err := c.c.Detach(); err != nil {
		t.Fatal(err)
	}
	c.shutdown("detach", "detached")
}
