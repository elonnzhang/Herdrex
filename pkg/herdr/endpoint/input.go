package endpoint

import "github.com/elonnzhang/herdrex/pkg/herdr/internal/wire"

// Input is one semantic input event for a pane or popup: Key, TextCommit,
// Mouse or Paste (ClientPaneInputEvent).
type Input interface{ putInput(e *wire.Enc) }

// Key is a key event. Herdr encodes it for the program's keyboard protocol.
type Key struct {
	Code             KeyCode
	Char             rune  // the character, for KeyChar
	F                uint8 // the function key number, for KeyF
	Mods             uint8 // Mod* bits
	Kind             KeyKind
	RepeatCount      uint16  // 0 counts as 1
	ShiftedCodepoint *uint32 // the character with Shift held, when known
	GeneratedText    string  // the text the key produced, if any
	TracksRelease    bool    // GeneratedText came from a physical key; no effect without it
	PhysicalKeyID    *uint32
	WindowsRecord    *WindowsKeyRecord
}

// KeyCode is a key, in Herdr's ClientKeyCode order.
type KeyCode uint8

const (
	KeyBackspace KeyCode = iota
	KeyEnter
	KeyLeft
	KeyRight
	KeyUp
	KeyDown
	KeyHome
	KeyEnd
	KeyPageUp
	KeyPageDown
	KeyTab
	KeyBackTab
	KeyDelete
	KeyInsert
	KeyEsc
	KeyChar // Key.Char holds the character
	KeyF    // Key.F holds the number
	KeyNull
)

// KeyKind is a press, repeat or release.
type KeyKind uint8

const (
	KeyPress KeyKind = iota
	KeyRepeat
	KeyRelease
)

// Modifier bits, as crossterm numbers them.
const (
	ModShift uint8 = 1 << iota
	ModCtrl
	ModAlt
	ModSuper
	ModHyper
	ModMeta
)

// WindowsKeyRecord is a Windows console KEY_EVENT_RECORD.
type WindowsKeyRecord struct {
	KeyDown         bool
	RepeatCount     uint16
	VirtualKeyCode  uint16
	VirtualScanCode uint16
	Unicode         uint16
	ControlKeyState uint32
}

// TextCommit is text to type, such as an input method's committed text.
type TextCommit string

// Paste is pasted text. Herdr brackets it when the program asked for that.
type Paste string

// Mouse is a mouse event. Column and Row count from 0 at the top-left cell
// of the pane's (or popup's) terminal. With Pixels set, X and Y give the
// pixel position too, which Herdr uses for programs in SGR pixel mode.
type Mouse struct {
	Kind        MouseKind
	Button      MouseButton // for MouseDown, MouseUp and MouseDrag
	Column, Row uint16
	Pixels      bool
	X, Y        uint32
	Geometry    *MouseGeometry
	Mods        uint8
	Lines       uint16 // rows to scroll; 0 counts as 1
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

// MouseButton is a button, in Herdr's ClientMouseButton order.
type MouseButton uint8

const (
	ButtonLeft MouseButton = iota
	ButtonRight
	ButtonMiddle
)

// MouseGeometry is the size of the area a pixel position refers to.
type MouseGeometry struct {
	Cols, Rows        uint16
	WidthPx, HeightPx uint32
}

func (k Key) putInput(e *wire.Enc) {
	e.U(0).U(uint64(k.Code))
	switch k.Code {
	case KeyChar:
		e.Char(k.Char)
	case KeyF:
		e.U8(k.F)
	}
	e.U8(k.Mods).U(uint64(k.Kind)).U(uint64(k.RepeatCount))
	putOptU(e, k.ShiftedCodepoint)
	putOptStr(e, k.GeneratedText)
	e.Bool(k.TracksRelease)
	putOptU(e, k.PhysicalKeyID)
	e.Opt(k.WindowsRecord != nil)
	if r := k.WindowsRecord; r != nil {
		e.Bool(r.KeyDown).U(uint64(r.RepeatCount)).U(uint64(r.VirtualKeyCode)).
			U(uint64(r.VirtualScanCode)).U(uint64(r.Unicode)).U(uint64(r.ControlKeyState))
	}
}

func (t TextCommit) putInput(e *wire.Enc) { e.U(1).Str(string(t)) }

func (m Mouse) putInput(e *wire.Enc) {
	e.U(2).U(uint64(m.Kind))
	if m.Kind <= MouseDrag {
		e.U(uint64(m.Button))
	}
	if m.Pixels { // ClientMousePosition::Pixels
		e.U(1).U(uint64(m.X)).U(uint64(m.Y))
	} else {
		e.U(0)
	}
	e.U(uint64(m.Column)).U(uint64(m.Row))
	e.Opt(m.Geometry != nil)
	if g := m.Geometry; g != nil {
		e.U(uint64(g.Cols)).U(uint64(g.Rows)).U(uint64(g.WidthPx)).U(uint64(g.HeightPx))
	}
	e.U8(m.Mods).U(uint64(m.Lines))
}

func (p Paste) putInput(e *wire.Enc) { e.U(3).Str(string(p)) }

// ThemeUpdate is a change to the host terminal's colors or appearance:
// DefaultColor, PaletteColors or Appearance (ClientHostThemeUpdate).
type ThemeUpdate interface{ putTheme(e *wire.Enc) }

// RGB is a 24-bit color.
type RGB struct{ R, G, B uint8 }

// DefaultColor is the host terminal's default foreground color, or its
// default background color when Background is set.
type DefaultColor struct {
	Background bool
	Color      RGB
}

// PaletteColors are changed entries of the host terminal's 256-color
// palette; Herdr closes the connection when there are more than 256.
type PaletteColors []PaletteColor

// PaletteColor is one palette entry.
type PaletteColor struct {
	Index uint8
	Color RGB
}

// Appearance is the host's dark or light mode.
type Appearance uint8

const (
	AppearanceDark Appearance = iota
	AppearanceLight
)

func (c DefaultColor) putTheme(e *wire.Enc) {
	var kind uint64 // ClientHostDefaultColorKind: Foreground 0, Background 1
	if c.Background {
		kind = 1
	}
	putRGB(e.U(0).U(kind), c.Color)
}

func (p PaletteColors) putTheme(e *wire.Enc) {
	e.U(1).U(uint64(len(p)))
	for _, c := range p {
		putRGB(e.U8(c.Index), c.Color)
	}
}

func (a Appearance) putTheme(e *wire.Enc) { e.U(2).U(uint64(a)) }

func putRGB(e *wire.Enc, c RGB) { e.U8(c.R).U8(c.G).U8(c.B) }

// ImageTarget is the terminal a pasted clipboard image goes to: a pane, or a
// popup terminal when Popup is set.
type ImageTarget struct {
	Popup bool
	ID    string // the pane ID or popup terminal ID
}

func (t ImageTarget) put(e *wire.Enc) {
	if t.Popup { // ClientClipboardImageTarget: DirectTerminal 0, Pane 1, Popup 2
		e.U(2)
	} else {
		e.U(1)
	}
	e.Str(t.ID)
}
