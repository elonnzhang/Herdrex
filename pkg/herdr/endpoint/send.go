package endpoint

import (
	"errors"
	"fmt"

	"github.com/elonnzhang/herdrex/pkg/herdr/internal/wire"
)

// ClientMessage variants (frozen within endpoint generation 1).
const (
	cClipboardImage = 2
	cResize         = 12
	cPaneInput      = 13
	cPopupInput     = 14
	cRequest        = 15
	cHostTheme      = 17
	cFocus          = 18
	cMouseCapture   = 19
	cControl        = 20
)

// Herdr's surface geometry limits; it closes the connection for a resize
// beyond them (src/server/client_transport.rs:49).
const (
	maxSurfaceSide  = 4096
	maxSurfaceCells = 1_000_000
	maxCellPx       = 4096
)

// Herdr's limits on what a client sends; it closes the connection over them
// (src/server/client_transport.rs:1119, 1180, 1196). An input batch made of
// paste alone only gets a "Paste rejected" notice, but the paste is not
// delivered either, so the senders refuse it too.
const (
	maxInputEvents    = 4096     // events in one input batch, client_transport.rs:118
	maxInputText      = 1 << 20  // text bytes in one input batch, client_transport.rs:48
	maxClipboardImage = 16 << 20 // src/protocol/wire.rs:32
	maxPaletteColors  = 256
)

// ErrTooLarge reports a message over one of Herdr's limits. Nothing was sent.
var ErrTooLarge = errors.New("herdr: message too large")

func tooLarge(what string, n, limit int) error {
	return fmt.Errorf("%w: %d %s exceed Herdr's limit of %d", ErrTooLarge, n, what, limit)
}

func (c *Conn) send(e wire.Enc) error {
	if len(e) > wire.MaxGraphicsFrame { // Herdr's limit for client frames after the hello
		return tooLarge("message bytes", len(e), wire.MaxGraphicsFrame)
	}
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return wire.WriteFrame(c.nc, e)
}

// PaneInput sends input events to a pane, which must be in the tab this
// connection views; Herdr ignores input from inactive connections. It sends
// nothing and returns an error for an event Herdr cannot decode (a Kind,
// Code or Button that is not one of the constants), and ErrTooLarge for a
// batch of more than 4096 events (counting key repeats and vertically
// scrolled lines) or more than 1 MiB of text (counting each key repeat's
// GeneratedText and pastes): Herdr would close the connection.
func (c *Conn) PaneInput(paneID string, ev ...Input) error {
	return c.sendInput(cPaneInput, paneID, ev)
}

// PopupInput sends input events to the popup terminal shown over the tab,
// with the same checks as PaneInput.
func (c *Conn) PopupInput(terminalID string, ev ...Input) error {
	return c.sendInput(cPopupInput, terminalID, ev)
}

func (c *Conn) sendInput(tag uint64, target string, ev []Input) error {
	var events, text int
	for _, in := range ev {
		n, b, err := in.weight()
		if err != nil {
			return err
		}
		events += n
		text += b
	}
	if events > maxInputEvents {
		return tooLarge("input events", events, maxInputEvents)
	}
	if text > maxInputText {
		return tooLarge("input text bytes", text, maxInputText)
	}
	return c.send(encInput(tag, target, ev))
}

// Resize sets the surface size in cells and the cell size in pixels (0 when
// unknown). pixelMouse says mouse input carries exact pixel positions. Sizes
// Herdr would reject (a zero side, a side above 4096, more than a million
// cells or a cell side above 4096 pixels) return an error unsent.
func (c *Conn) Resize(cols, rows uint16, cellW, cellH uint32, pixelMouse bool) error {
	if cols == 0 || rows == 0 || cols > maxSurfaceSide || rows > maxSurfaceSide ||
		int(cols)*int(rows) > maxSurfaceCells || cellW > maxCellPx || cellH > maxCellPx {
		return fmt.Errorf("herdr: surface %d×%d with %d×%d pixel cells is outside Herdr's limits", cols, rows, cellW, cellH)
	}
	return c.send(encResize(cols, rows, cellW, cellH, pixelMouse))
}

// Focus reports whether the window showing the surface has focus.
func (c *Conn) Focus(focused bool) error {
	var e wire.Enc
	return c.send(*e.U(cFocus).Bool(focused))
}

// MouseCapture changes the Hello's MouseCapture preference.
func (c *Conn) MouseCapture(enabled bool) error {
	var e wire.Enc
	return c.send(*e.U(cMouseCapture).Bool(enabled))
}

// HostTheme reports a change to the host terminal's colors or appearance.
// It sends nothing and returns ErrTooLarge for more than 256 PaletteColors,
// and an error for an Appearance that is not one of the constants: Herdr
// would close the connection.
func (c *Conn) HostTheme(u ThemeUpdate) error {
	switch u := u.(type) {
	case PaletteColors:
		if len(u) > maxPaletteColors {
			return tooLarge("palette colors", len(u), maxPaletteColors)
		}
	case Appearance:
		if u > AppearanceLight {
			return fmt.Errorf("herdr: invalid appearance %d", u)
		}
	}
	var e wire.Enc
	e.U(cHostTheme)
	u.putTheme(&e)
	return c.send(e)
}

// ClipboardImage pastes an image from the client's clipboard into a pane or
// popup. ext is the file extension without the dot, such as "png". It sends
// nothing and returns ErrTooLarge for more than 16 MiB of data, for which
// Herdr would close the connection.
func (c *Conn) ClipboardImage(target ImageTarget, ext string, data []byte) error {
	if len(data) > maxClipboardImage {
		return tooLarge("clipboard image bytes", len(data), maxClipboardImage)
	}
	return c.send(encClipboardImage(target, ext, data))
}

// SendControl sends a named control message. Herdr ignores kinds it does not
// know.
func (c *Conn) SendControl(kind, data string) error {
	var e wire.Enc
	return c.send(*e.U(cControl).Str(kind).Str(data))
}

func encInput(tag uint64, target string, ev []Input) wire.Enc {
	var e wire.Enc
	e.U(tag).Str(target).U(uint64(len(ev)))
	for _, in := range ev {
		in.putInput(&e)
	}
	return e
}

func encResize(cols, rows uint16, cellW, cellH uint32, pixelMouse bool) wire.Enc {
	var e wire.Enc
	return *e.U(cResize).U(uint64(cellW)).U(uint64(cellH)).
		U(uint64(cols)).U(uint64(rows)).Bool(pixelMouse)
}

func encClipboardImage(target ImageTarget, ext string, data []byte) wire.Enc {
	var e wire.Enc
	e.U(cClipboardImage)
	target.put(&e)
	return *e.Str(ext).Bytes(data)
}

func encRequest(bootID, req string) wire.Enc {
	var e wire.Enc
	return *e.U(cRequest).Str(bootID).Str(req)
}
