// Package term is a client for Herdr's direct terminal protocol: the binary
// protocol on the client socket that Herdr's own CLI uses to control or
// observe one terminal (pkg/herdr/HERDR.md §5).
//
// Herdr only promises compatibility within one installation: the hello must
// carry exactly the server's protocol version. Pass the protocol that the
// JSON API's ping reports. When the server speaks another version, as after
// an upgrade or a live handoff, Dial returns a *VersionError; dial again with
// its Server version.
//
// Next decodes Clipboard, WindowTitle and Bell, but Herdr 0.9.5 sends them
// only to its foreground shell client, which a direct terminal connection
// never is (HERDR.md §4.3).
package term

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net"
	"sync"

	"github.com/elonnzhang/herdrex/pkg/herdr/internal/wire"
)

// MaxFrame is the largest message Next accepts: Herdr's limit for server
// frames that carry images (src/protocol/wire.rs:29). Herdr 0.9.5 sends a
// direct terminal connection no images and skips its frames over 2 MiB
// (src/server/headless/render.rs:868, 938), so the higher limit is room for
// a Herdr that inlines kitty images in Terminal frames
// (src/server/render_stream.rs:136).
const MaxFrame = wire.MaxGraphicsFrame

// Herdr's limits on what a client sends. Over them, Herdr closes the
// connection, except that an Input holding exactly one bracketed paste only
// gets a "Paste rejected" Notify (src/server/client_transport.rs:1045,
// src/server/headless.rs:2107). Either way the data is not delivered, so the
// senders return ErrTooLarge instead of writing.
const (
	// MaxInput is the most bytes one Input carries (src/server/client_transport.rs:48).
	MaxInput = 1 << 20
	// MaxClipboardImage is the largest image ClipboardImage sends (src/protocol/wire.rs:32).
	MaxClipboardImage = 16 << 20
)

// ErrTooLarge reports a message over one of Herdr's size limits. Nothing was sent.
var ErrTooLarge = errors.New("herdr: message too large")

// Size is a terminal size and its cell size in pixels (0 when unknown).
type Size struct {
	Cols, Rows                uint16
	CellWidthPx, CellHeightPx uint32
	PixelMouse                bool
}

// Messages the server sends. Next returns one of these.
type (
	// Welcome is Herdr's answer to the hello. Error is set when Herdr refused
	// it; Dial turns that into an error.
	Welcome struct {
		Version  uint32
		Encoding uint32 // 0 SemanticFrame, 1 TerminalAnsi
		Error    string
	}
	// Frame is terminal output rendered by Herdr: escape sequences that
	// repaint the screen, a full repaint when Full is set.
	Frame struct {
		Seq           uint64
		Width, Height uint16
		Full          bool
		Bytes         []byte
	}
	// Shutdown ends the connection; Herdr closes it after sending this.
	// Reasons include "detached" and "terminal attach taken over".
	Shutdown struct{ Reason string }
	// Bell reports BEL characters from one PTY read.
	Bell struct{ Count uint16 }
	// Clipboard is an OSC 52 copy; Data is base64.
	Clipboard struct{ Data string }
	// WindowTitle is the title for the client's window; empty restores the default.
	WindowTitle struct{ Title string }
	// ReloadSoundConfig asks clients to reread their local config. Herdr
	// sends it to every client after it reloads its own config, on
	// server.reload_config or the reload action; it does not watch the file.
	ReloadSoundConfig struct{}
	// MouseCapture says whether the program asked for mouse reports. A
	// controlling client gets one when it attaches and on every change.
	MouseCapture struct{ Enabled, SGRPixels bool }
	// KeyboardProtocol is the keyboard mode the program asked for: kitty
	// keyboard flags (0 for legacy keys) and the xterm modifyOtherKeys level.
	// A controlling client gets one when it attaches and on every change.
	KeyboardProtocol struct {
		Flags           uint16
		ModifyOtherKeys uint8
	}
	// Other is a message this package does not decode.
	Other struct{ Tag uint32 }
)

// VersionError is Herdr refusing the hello because its protocol version is
// not Client. Server is the version Herdr speaks.
type VersionError struct {
	Client, Server uint32
	Message        string // Herdr's reason
}

// Error returns Herdr's reason.
func (e *VersionError) Error() string { return "herdr: " + e.Message }

// Client message variants (frozen within endpoint generation 1).
const (
	cHello     = 0
	cInput     = 1
	cClipImage = 2
	cResize    = 3
	cDetach    = 4
	cAttach    = 5
	cScroll    = 6
	cObserve   = 7
	cControl   = 8
	cMouse     = 16
)

// Server message variants.
const (
	sWelcome      = 0
	sTerminal     = 1
	sShutdown     = 3
	sClipboard    = 5
	sTitle        = 6
	sReloadSound  = 7
	sMouseCapture = 8
	sBell         = 9
	sKeyboard     = 16
)

// Conn is one connection to the client socket. The senders are safe for
// concurrent use; Next is not.
type Conn struct {
	c   net.Conn
	r   *bufio.Reader
	wmu sync.Mutex
}

// Dial connects and says hello. The connection is pending until Control,
// Attach or Observe picks a terminal. ctx bounds the handshake: when it ends
// first, Dial closes the connection and returns ctx.Err().
func Dial(ctx context.Context, socket string, protocol uint32, size Size) (*Conn, error) {
	var d net.Dialer
	nc, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, err
	}
	stop := context.AfterFunc(ctx, func() { nc.Close() })
	c := &Conn{c: nc, r: bufio.NewReaderSize(nc, 64<<10)}
	m, err := c.hello(protocol, size)
	if !stop() { // ctx ended and the connection is closed
		nc.Close()
		return nil, ctx.Err()
	}
	if err != nil {
		nc.Close()
		return nil, err
	}
	switch w := m.(type) {
	case *Welcome:
		if w.Error == "" {
			return c, nil
		}
		nc.Close()
		if w.Version != protocol { // Herdr answers a refused hello with its own version
			return nil, &VersionError{Client: protocol, Server: w.Version, Message: w.Error}
		}
		return nil, errors.New("herdr: " + w.Error)
	case *Shutdown:
		nc.Close()
		return nil, errors.New("herdr: " + w.Reason)
	}
	nc.Close()
	return nil, fmt.Errorf("herdr: unexpected first message %T", m)
}

// hello sends TerminalHello and reads Herdr's first message.
func (c *Conn) hello(protocol uint32, size Size) (any, error) {
	var e wire.Enc
	e.U(cHello).U(uint64(protocol))
	if err := c.send(*putSize(&e, size)); err != nil {
		return nil, err
	}
	return c.Next()
}

// Control attaches to a terminal (a pane ID such as "w1:p3" or a terminal ID)
// as its controlling client: Herdr sizes the PTY to this client and stops
// resizing it from the layout. Without takeover it fails if another client
// controls the terminal; the failure arrives as a Shutdown from Next.
func (c *Conn) Control(target string, takeover bool) error {
	var e wire.Enc
	return c.send(*e.U(cControl).Str(target).Bool(takeover))
}

// Attach is Control by terminal ID only, such as pane.get's terminal_id.
func (c *Conn) Attach(terminalID string, takeover bool) error {
	var e wire.Enc
	return c.send(*e.U(cAttach).Str(terminalID).Bool(takeover))
}

// Observe attaches read-only. It does not change the PTY size.
func (c *Conn) Observe(target string) error {
	var e wire.Enc
	return c.send(*e.U(cObserve).Str(target))
}

// Input writes bytes to the PTY as typed, at most MaxInput of them. Herdr
// scrolls back to the bottom first and passes a write that is exactly one
// bracketed paste as a paste.
func (c *Conn) Input(b []byte) error {
	if len(b) > MaxInput {
		return tooLarge("input", len(b), MaxInput)
	}
	var e wire.Enc
	return c.send(*e.U(cInput).Bytes(b))
}

// Resize sets the client size. For a controlling client this resizes the
// PTY; Herdr answers with a full frame even when the size is unchanged.
func (c *Conn) Resize(s Size) error {
	var e wire.Enc
	return c.send(*putSize(e.U(cResize), s))
}

// putSize writes the size fields that TerminalHello and Resize share.
func putSize(e *wire.Enc, s Size) *wire.Enc {
	return e.U(uint64(s.Cols)).U(uint64(s.Rows)).
		U(uint64(s.CellWidthPx)).U(uint64(s.CellHeightPx)).Bool(s.PixelMouse)
}

// ClipboardImage pastes an image into a controlled terminal: Herdr saves data
// to a temporary file and pastes the file's path. ext is the extension
// without a dot; Herdr knows png, jpg, jpeg, gif, webp and bmp and names
// anything else .png. data is at most MaxClipboardImage bytes.
func (c *Conn) ClipboardImage(ext string, data []byte) error {
	if len(data) > MaxClipboardImage {
		return tooLarge("clipboard image", len(data), MaxClipboardImage)
	}
	var e wire.Enc
	e.U(cClipImage).U(0) // ClientClipboardImageTarget::DirectTerminal
	return c.send(*e.Str(ext).Bytes(data))
}

// ScrollSource is what a Scroll came from, in Herdr's AttachScrollSource order.
type ScrollSource uint8

// Scroll sources.
const (
	Wheel ScrollSource = iota
	PageKey
)

// ScrollDirection is in Herdr's AttachScrollDirection order.
type ScrollDirection uint8

// Scroll directions.
const (
	Up ScrollDirection = iota
	Down
)

// Scroll is a wheel turn or a PageUp/PageDown press for a controlled
// terminal. Herdr turns a wheel into a mouse report when the program asked
// for them and into arrow keys on the alternate screen with mode 1007;
// otherwise it moves Herdr's scrollback by Lines (0 counts as 1). A page key
// moves the scrollback at a plain shell prompt and otherwise sends Input to
// the program.
type Scroll struct {
	Source    ScrollSource
	Input     []byte // PageKey: the key's bytes, such as "\x1b[5~"
	Direction ScrollDirection
	Lines     uint16
	// Column and Row are the pointer's cell for mouse reports, counting
	// from 0; nil sends none and Herdr uses 0.
	Column, Row *uint16
	Mods        uint8
}

// Scroll sends a scroll to a controlled terminal.
func (c *Conn) Scroll(s Scroll) error {
	if s.Source > PageKey || s.Direction > Down {
		return fmt.Errorf("herdr: invalid scroll source %d or direction %d", s.Source, s.Direction)
	}
	var e wire.Enc
	e.U(cScroll).U(uint64(s.Source))
	if s.Source == PageKey {
		e.Bytes(s.Input)
	}
	e.U(uint64(s.Direction)).U(uint64(s.Lines))
	putOptU16(&e, s.Column)
	putOptU16(&e, s.Row)
	return c.send(*e.U8(s.Mods))
}

func putOptU16(e *wire.Enc, v *uint16) {
	e.Opt(v != nil)
	if v != nil {
		e.U(uint64(*v))
	}
}

// MouseKind is what a mouse event did, in Herdr's ClientMouseKind order.
type MouseKind uint8

// Mouse event kinds.
const (
	MouseDown MouseKind = iota
	MouseUp
	MouseDrag
	MouseMoved
	ScrollUp
	ScrollDown
	ScrollLeft
	ScrollRight
)

// Mouse buttons, in Herdr's ClientMouseButton order.
const (
	ButtonLeft uint8 = iota
	ButtonRight
	ButtonMiddle
)

// Modifier bits, as crossterm numbers them.
const (
	ModShift uint8 = 1 << iota
	ModCtrl
	ModAlt
	ModSuper
)

// Mouse is one mouse event at a cell of the terminal, counting from 0.
// Button is used by MouseDown, MouseUp and MouseDrag; Lines by scrolls.
type Mouse struct {
	Kind        MouseKind
	Button      uint8
	Column, Row uint16
	Mods        uint8
	Lines       uint16
}

// Mouse sends a mouse event to a controlled terminal. Herdr encodes it for
// the program when the program asked for mouse reports. Otherwise a vertical
// scroll moves Herdr's scrollback (or sends arrows on the alternate screen)
// and other events are dropped. It returns an error and sends nothing when
// Kind is not a MouseKind constant, or when a MouseDown, MouseUp or MouseDrag
// has a Button other than ButtonLeft, ButtonRight or ButtonMiddle: Herdr
// could not decode the message and would close the connection.
func (c *Conn) Mouse(m Mouse) error {
	if m.Kind > ScrollRight || m.Kind <= MouseDrag && m.Button > ButtonMiddle {
		return fmt.Errorf("herdr: invalid mouse kind %d or button %d", m.Kind, m.Button)
	}
	var e wire.Enc
	e.U(cMouse).U(uint64(m.Kind))
	if m.Kind <= MouseDrag {
		e.U(uint64(m.Button))
	}
	e.U(0).U(uint64(m.Column)).U(uint64(m.Row)) // ClientMousePosition::Cell
	e.Opt(false)                                // no geometry
	e.U8(m.Mods)
	return c.send(*e.U(uint64(max(m.Lines, 1))))
}

// Detach ends the attachment. Herdr replies with Shutdown "detached" and
// closes the connection. The PTY keeps its last size.
func (c *Conn) Detach() error {
	var e wire.Enc
	return c.send(*e.U(cDetach))
}

// Close closes the connection without detaching. Herdr treats it as the
// client going away.
func (c *Conn) Close() error { return c.c.Close() }

func (c *Conn) send(e wire.Enc) error {
	if len(e) > wire.MaxGraphicsFrame { // Herdr's limit for client frames after the hello
		return tooLarge("message", len(e), wire.MaxGraphicsFrame)
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return wire.WriteFrame(c.c, e)
}

func tooLarge(what string, n, limit int) error {
	return fmt.Errorf("%w: %s of %d bytes exceeds Herdr's %d byte limit", ErrTooLarge, what, n, limit)
}

// Next reads one server message. It is not safe for concurrent use.
func (c *Conn) Next() (any, error) {
	p, err := wire.ReadFrame(c.r, MaxFrame)
	if err != nil {
		return nil, err
	}
	d := wire.NewDec(p)
	tag := d.U32()
	var m any
	switch tag {
	case sWelcome:
		w := &Welcome{Version: d.U32(), Encoding: d.U32()}
		if d.Opt() {
			w.Error = d.Str()
		}
		m = w
	case sTerminal:
		m = &Frame{Seq: d.U(), Width: d.U16(), Height: d.U16(),
			Full: d.Bool(), Bytes: d.Bytes()}
	case sShutdown:
		s := &Shutdown{}
		if d.Opt() {
			s.Reason = d.Str()
		}
		m = s
	case sClipboard:
		m = &Clipboard{Data: d.Str()}
	case sTitle:
		t := &WindowTitle{}
		if d.Opt() {
			t.Title = d.Str()
		}
		m = t
	case sReloadSound:
		m = &ReloadSoundConfig{}
	case sBell:
		m = &Bell{Count: d.U16()}
	case sMouseCapture:
		m = &MouseCapture{Enabled: d.Bool(), SGRPixels: d.Bool()}
	case sKeyboard:
		m = &KeyboardProtocol{Flags: d.U16(), ModifyOtherKeys: d.U8()}
	default:
		return &Other{Tag: tag}, nil
	}
	if err := d.Done(); err != nil {
		return nil, fmt.Errorf("herdr: malformed message %d", tag)
	}
	return m, nil
}
