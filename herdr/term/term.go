// Package term is a client for Herdr's direct terminal protocol: the binary
// protocol on the client socket that Herdr's own CLI uses to control or
// observe one terminal (see docs/herdr-api.md §8).
//
// Herdr only promises compatibility within one installation, so callers pass
// the protocol number that the JSON API's ping reports.
package term

import (
	"bufio"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
)

// MaxFrame is Herdr's limit for one message.
const MaxFrame = 2 << 20

// ClientSocket derives the client socket path from the API socket path:
// herdr.sock -> herdr-client.sock.
func ClientSocket(apiSocket string) string {
	if s, ok := strings.CutSuffix(apiSocket, ".sock"); ok {
		return s + "-client.sock"
	}
	return apiSocket + "-client"
}

// Size is a terminal size and its cell size in pixels (0 when unknown).
type Size struct {
	Cols, Rows                uint16
	CellWidthPx, CellHeightPx uint32
	PixelMouse                bool
}

// Messages the server sends. Next returns one of these.
type (
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
	Shutdown struct{ Reason string }
	// Bell reports BEL characters from one PTY read.
	Bell struct{ Count uint16 }
	// Clipboard is an OSC 52 copy; Data is base64.
	Clipboard struct{ Data string }
	// WindowTitle is the title for the client's window; empty restores the default.
	WindowTitle struct{ Title string }
	// MouseCapture says whether the program asked for mouse reports. A
	// controlling client gets one when it attaches and on every change.
	MouseCapture struct{ Enabled, SGRPixels bool }
	// Other is a message this package does not decode.
	Other struct{ Tag uint32 }
)

// Client message variants (frozen within endpoint generation 1).
const (
	cHello   = 0
	cInput   = 1
	cResize  = 3
	cDetach  = 4
	cObserve = 7
	cControl = 8
	cMouse   = 16
)

// Server message variants.
const (
	sWelcome      = 0
	sTerminal     = 1
	sShutdown     = 3
	sClipboard    = 5
	sTitle        = 6
	sMouseCapture = 8
	sBell         = 9
)

type Conn struct {
	c   net.Conn
	r   *bufio.Reader
	wmu sync.Mutex
}

// Dial connects and says hello. The connection is pending until Control or
// Observe picks a terminal.
func Dial(ctx context.Context, socket string, protocol uint32, size Size) (*Conn, error) {
	var d net.Dialer
	nc, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, err
	}
	c := &Conn{c: nc, r: bufio.NewReaderSize(nc, 64<<10)}
	if dl, ok := ctx.Deadline(); ok {
		nc.SetDeadline(dl)
	}
	var e enc
	e.v(cHello).v(uint64(protocol)).size(size)
	if err := c.send(e); err != nil {
		nc.Close()
		return nil, err
	}
	m, err := c.Next()
	if err != nil {
		nc.Close()
		return nil, err
	}
	nc.SetDeadline(timeZero)
	switch w := m.(type) {
	case *Welcome:
		if w.Error != "" {
			nc.Close()
			return nil, errors.New("herdr: " + w.Error)
		}
		return c, nil
	case *Shutdown:
		nc.Close()
		return nil, errors.New("herdr: " + w.Reason)
	}
	nc.Close()
	return nil, fmt.Errorf("herdr: unexpected first message %T", m)
}

// Control attaches to a terminal (a pane ID such as "w1:p3" or a terminal ID)
// as its controlling client: Herdr sizes the PTY to this client and stops
// resizing it from the layout. Without takeover it fails if another client
// controls the terminal; the failure arrives as a Shutdown from Next.
func (c *Conn) Control(target string, takeover bool) error {
	var e enc
	return c.send(*e.v(cControl).str(target).bool(takeover))
}

// Observe attaches read-only. It does not change the PTY size.
func (c *Conn) Observe(target string) error {
	var e enc
	return c.send(*e.v(cObserve).str(target))
}

// Input writes bytes to the PTY as typed.
func (c *Conn) Input(b []byte) error {
	var e enc
	return c.send(*e.v(cInput).bytes(b))
}

// Resize sets the client size. For a controlling client this resizes the
// PTY; Herdr answers with a full frame even when the size is unchanged.
func (c *Conn) Resize(s Size) error {
	var e enc
	return c.send(*e.v(cResize).size(s))
}

// MouseKind is what a mouse event did, in Herdr's ClientMouseKind order.
type MouseKind uint8

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
// and other events are dropped.
func (c *Conn) Mouse(m Mouse) error {
	var e enc
	e.v(cMouse).v(uint64(m.Kind))
	if m.Kind <= MouseDrag {
		e.v(uint64(m.Button))
	}
	e.v(0).v(uint64(m.Column)).v(uint64(m.Row)) // ClientMousePosition::Cell
	e.v(0)                                      // no geometry
	e = append(e, m.Mods)
	return c.send(*e.v(uint64(max(m.Lines, 1))))
}

// Detach ends the attachment. Herdr replies with Shutdown "detached" and
// closes the connection. The PTY keeps its last size.
func (c *Conn) Detach() error {
	var e enc
	return c.send(*e.v(cDetach))
}

func (c *Conn) Close() error { return c.c.Close() }

func (c *Conn) send(e enc) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	b := make([]byte, 4, 4+len(e))
	binary.LittleEndian.PutUint32(b, uint32(len(e)))
	_, err := c.c.Write(append(b, e...))
	return err
}

// Next reads one server message. It is not safe for concurrent use.
func (c *Conn) Next() (any, error) {
	var h [4]byte
	if _, err := io.ReadFull(c.r, h[:]); err != nil {
		return nil, err
	}
	n := binary.LittleEndian.Uint32(h[:])
	if n > MaxFrame {
		return nil, fmt.Errorf("herdr: frame of %d bytes exceeds limit", n)
	}
	p := make([]byte, n)
	if _, err := io.ReadFull(c.r, p); err != nil {
		return nil, err
	}
	d := dec{b: p}
	tag := uint32(d.v())
	var m any
	switch tag {
	case sWelcome:
		w := &Welcome{Version: uint32(d.v()), Encoding: uint32(d.v())}
		if d.u8() == 1 {
			w.Error = string(d.bytes())
		}
		m = w
	case sTerminal:
		m = &Frame{Seq: d.v(), Width: uint16(d.v()), Height: uint16(d.v()),
			Full: d.u8() == 1, Bytes: d.bytes()}
	case sShutdown:
		s := &Shutdown{}
		if d.u8() == 1 {
			s.Reason = string(d.bytes())
		}
		m = s
	case sClipboard:
		m = &Clipboard{Data: string(d.bytes())}
	case sTitle:
		t := &WindowTitle{}
		if d.u8() == 1 {
			t.Title = string(d.bytes())
		}
		m = t
	case sBell:
		m = &Bell{Count: uint16(d.v())}
	case sMouseCapture:
		m = &MouseCapture{Enabled: d.u8() == 1, SGRPixels: d.u8() == 1}
	default:
		return &Other{Tag: tag}, nil
	}
	if d.bad {
		return nil, fmt.Errorf("herdr: malformed message %d", tag)
	}
	return m, nil
}
