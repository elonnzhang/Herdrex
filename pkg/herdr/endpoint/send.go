package endpoint

import (
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

func (c *Conn) send(e wire.Enc) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	return wire.WriteFrame(c.nc, e)
}

// PaneInput sends input events to a pane, which must be in the tab this
// connection views; Herdr ignores input from inactive connections. Herdr
// closes the connection for a batch of more than 4096 events (counting
// repeats and scrolled lines) or more than 1 MiB of text.
func (c *Conn) PaneInput(paneID string, ev ...Input) error {
	return c.send(encInput(cPaneInput, paneID, ev))
}

// PopupInput sends input events to the popup terminal shown over the tab,
// with the same limits as PaneInput.
func (c *Conn) PopupInput(terminalID string, ev ...Input) error {
	return c.send(encInput(cPopupInput, terminalID, ev))
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
func (c *Conn) HostTheme(u ThemeUpdate) error {
	var e wire.Enc
	e.U(cHostTheme)
	u.putTheme(&e)
	return c.send(e)
}

// ClipboardImage pastes an image from the client's clipboard into a pane or
// popup. ext is the file extension without the dot, such as "png". Herdr
// closes the connection for more than 16 MiB of data.
func (c *Conn) ClipboardImage(target ImageTarget, ext string, data []byte) error {
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
