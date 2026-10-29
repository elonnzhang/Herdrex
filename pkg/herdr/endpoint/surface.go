package endpoint

import (
	"fmt"
	"slices"

	"github.com/elonnzhang/herdrex/pkg/herdr/internal/wire"
)

// Surface is the rendered content of the tab this connection views, without
// Herdr's sidebar and tab bar (PaneSurfaceFrame). Only active connections
// receive it. SurfacePatch messages update it in between; see Apply.
type Surface struct {
	BootID             string
	ProjectionRevision uint64 // the snapshot revision whose layout produced it
	SurfaceRevision    uint64
	Frame              FrameData
	Panes              []SurfacePane
	Splits             []Split
	Popup              *Popup
	Graphics           GraphicsScene
}

// FrameData is a grid of cells in row-major order; len(Cells) is
// Width*Height.
type FrameData struct {
	Cells      []CellData
	Width      uint16
	Height     uint16
	Cursor     *Cursor
	Hyperlinks []string // OSC 8 URIs, indexed by CellData.Hyperlink
	Graphics   []byte   // kitty graphics bytes to apply after the text
}

// CellData is one cell. FG and BG are packed colors; see DecodeColor.
// Modifier holds ratatui's style bits (bold 1, dim 2, italic 4, underlined 8,
// slow blink 16, rapid blink 32, reversed 64, hidden 128, crossed out 256)
// and the underline style in bits 12 to 15. Skip marks a cell that
// diff-based rendering leaves alone.
type CellData struct {
	Symbol    string
	FG, BG    uint32
	Modifier  uint16
	Skip      bool
	Hyperlink *uint32
}

// Cursor is the cursor position and its shape as a DECSCUSR parameter (0 the
// terminal default, 1 to 6 blinking or steady block, underline and bar).
type Cursor struct {
	X, Y    uint16
	Visible bool
	Shape   uint8
}

// SurfacePane is one pane's place in the surface.
type SurfacePane struct {
	PaneID                string
	ContentRevision       uint64
	Rect                  Rect // with the border
	InnerRect             Rect // the terminal
	ScrollbarRect         *Rect
	Scroll                *ScrollMetrics
	Focused               bool
	MouseReporting        bool // the program asked for mouse reports
	SGRPixelMouse         bool
	AlternateScreenActive bool
	PixelWidth            uint32
	PixelHeight           uint32
}

// ScrollMetrics is a pane's scrollback position, in rows.
type ScrollMetrics struct {
	OffsetFromBottom    uint64
	MaxOffsetFromBottom uint64
	ViewportRows        uint64
}

// Rect is a rectangle of cells relative to the surface.
type Rect struct{ X, Y, Width, Height uint16 }

// Split is a draggable split handle between panes.
type Split struct {
	Direction SplitDirection
	Pos       uint16
	Area      Rect
	HitRect   Rect
	Path      []bool // the split's place in the layout tree
}

type SplitDirection uint8

const (
	SplitHorizontal SplitDirection = iota
	SplitVertical
)

// Popup is the popup terminal shown over the tab.
type Popup struct {
	TerminalID     string
	Title          string
	Width, Height  *PopupSize
	Frame          FrameData
	MouseReporting bool
	SGRPixelMouse  bool
	PixelWidth     uint32
	PixelHeight    uint32
}

// PopupSize is a popup dimension in cells, or in percent when Percent is set
// (then N is at most 255).
type PopupSize struct {
	Percent bool
	N       uint16
}

// GraphicsScene is the surface's images. Placements lists every placement;
// Assets carries only image bytes this connection has not received before.
type GraphicsScene struct {
	Assets         []GraphicsAsset
	Placements     []GraphicsPlacement
	RetainedAssets []GraphicsAssetKey // uploaded images still live outside this scene
}

type GraphicsAsset struct {
	Key  GraphicsAssetKey
	Data []byte
}

// GraphicsAssetKey identifies an image. It is comparable, so callers can
// keep the bytes from GraphicsScene.Assets in a map by it; this package does
// not cache them.
type GraphicsAssetKey struct {
	Source          GraphicsSource
	ImageWidth      uint32
	ImageHeight     uint32
	Format          GraphicsFormat
	DataLen         uint64
	DataFingerprint uint64
}

// GraphicsSource is where an image comes from: a kitty image of a pane or
// popup terminal (SourceTerminal), or a layer of a pane (SourcePaneLayer).
type GraphicsSource struct {
	Kind    SourceKind
	Target  GraphicsTarget // SourceTerminal
	ImageID uint32         // SourceTerminal
	PaneID  string         // SourcePaneLayer
	LayerID string         // SourcePaneLayer
}

type SourceKind uint8

const (
	SourceTerminal SourceKind = iota
	SourcePaneLayer
)

// GraphicsTarget is a pane, or a popup terminal when Popup is set.
type GraphicsTarget struct {
	Popup bool
	ID    string // the pane ID or popup terminal ID
}

type GraphicsFormat uint8

const (
	FormatRGB GraphicsFormat = iota
	FormatRGBA
	FormatPNG
)

// GraphicsPlacement is one placement of an image, already clipped to the
// surface.
type GraphicsPlacement struct {
	Asset              GraphicsAssetKey
	LogicalPlacementID uint32
	X, Y               uint16
	Cols, Rows         uint32
	SourceX, SourceY   uint32
	SourceWidth        uint32
	SourceHeight       uint32
	XOffset, YOffset   uint32
	Z                  int32
	ScrollbackOffset   uint32
}

// SurfacePatch changes cells of the surface with revision
// BaseSurfaceRevision (PaneSurfacePatch).
type SurfacePatch struct {
	BootID              string
	ProjectionRevision  uint64
	BaseSurfaceRevision uint64
	SurfaceRevision     uint64
	Rows                []PatchRow
	Panes               []SurfacePane // the panes whose content changed
	Cursor              *Cursor
}

// PatchRow replaces len(Cells) cells of row Y, from column X.
type PatchRow struct {
	X, Y  uint16
	Cells []CellData
}

// Apply updates s with p: the changed cells, the changed panes (matched by
// PaneID) and the cursor. It returns an error and leaves s unchanged unless p
// has the same BootID and ProjectionRevision, follows s.SurfaceRevision, and
// every row and pane fits s. A patch that fails can be dropped; the next full
// Surface replaces s.
func (s *Surface) Apply(p *SurfacePatch) error {
	switch {
	case p.BootID != s.BootID:
		return fmt.Errorf("herdr: patch for boot %q applied to boot %q", p.BootID, s.BootID)
	case p.ProjectionRevision != s.ProjectionRevision:
		return fmt.Errorf("herdr: patch for projection %d applied to projection %d", p.ProjectionRevision, s.ProjectionRevision)
	case p.BaseSurfaceRevision != s.SurfaceRevision:
		return fmt.Errorf("herdr: patch based on surface %d applied to surface %d", p.BaseSurfaceRevision, s.SurfaceRevision)
	}
	w, h := int(s.Frame.Width), int(s.Frame.Height)
	if len(s.Frame.Cells) != w*h {
		return fmt.Errorf("herdr: surface has %d cells for %d×%d", len(s.Frame.Cells), w, h)
	}
	for _, r := range p.Rows {
		if int(r.X)+len(r.Cells) > w || int(r.Y) >= h {
			return fmt.Errorf("herdr: patch row at %d,%d with %d cells outside %d×%d", r.X, r.Y, len(r.Cells), w, h)
		}
	}
	at := make([]int, len(p.Panes))
	for i, np := range p.Panes {
		at[i] = slices.IndexFunc(s.Panes, func(sp SurfacePane) bool { return sp.PaneID == np.PaneID })
		if at[i] < 0 {
			return fmt.Errorf("herdr: patch for pane %s not in the surface", np.PaneID)
		}
	}
	for _, r := range p.Rows {
		copy(s.Frame.Cells[int(r.Y)*w+int(r.X):], r.Cells)
	}
	for i, j := range at {
		s.Panes[j] = p.Panes[i]
	}
	s.Frame.Cursor = p.Cursor
	s.SurfaceRevision = p.SurfaceRevision
	return nil
}

// Color is an unpacked CellData color.
type Color struct {
	Kind ColorKind
	// Index is the named color for ColorNamed: 0 the terminal default, n from
	// 1 to 16 ANSI color n-1 (black … bright white). For ColorIndexed it is
	// the palette index.
	Index   uint8
	R, G, B uint8 // ColorRGB
}

type ColorKind uint8

const (
	ColorNamed ColorKind = iota
	ColorIndexed
	ColorRGB
)

// DecodeColor unpacks a cell color (src/protocol/wire.rs:1510):
// 0x000000XX is named color XX, 0x010000XX palette index XX, and 0x02RRGGBB
// an RGB color. Anything else is the default color, as Herdr reads it.
func DecodeColor(v uint32) Color {
	switch v >> 24 {
	case 0:
		if v&0xFF <= 16 {
			return Color{Kind: ColorNamed, Index: uint8(v)}
		}
	case 1:
		return Color{Kind: ColorIndexed, Index: uint8(v)}
	case 2:
		return Color{Kind: ColorRGB, R: uint8(v >> 16), G: uint8(v >> 8), B: uint8(v)}
	}
	return Color{Kind: ColorNamed}
}

func getSurface(d *wire.Dec) *Surface {
	return &Surface{
		BootID:             d.Str(),
		ProjectionRevision: d.U(),
		SurfaceRevision:    d.U(),
		Frame:              getFrame(d),
		Panes:              getSlice(d, getPane),
		Splits:             getSlice(d, getSplit),
		Popup:              getPopup(d), Graphics: getScene(d),
	}
}

func putSurface(e *wire.Enc, s *Surface) {
	e.Str(s.BootID).U(s.ProjectionRevision).U(s.SurfaceRevision)
	putFrame(e, &s.Frame)
	putSlice(e, s.Panes, putPane)
	putSlice(e, s.Splits, putSplit)
	putPopup(e, s.Popup)
	putScene(e, &s.Graphics)
}

func getPatch(d *wire.Dec) *SurfacePatch {
	return &SurfacePatch{
		BootID:              d.Str(),
		ProjectionRevision:  d.U(),
		BaseSurfaceRevision: d.U(),
		SurfaceRevision:     d.U(),
		Rows:                getSlice(d, getPatchRow),
		Panes:               getSlice(d, getPane), Cursor: getCursor(d),
	}
}

func putPatch(e *wire.Enc, p *SurfacePatch) {
	e.Str(p.BootID).U(p.ProjectionRevision).U(p.BaseSurfaceRevision).U(p.SurfaceRevision)
	putSlice(e, p.Rows, putPatchRow)
	putSlice(e, p.Panes, putPane)
	putCursor(e, p.Cursor)
}

func getPatchRow(d *wire.Dec) PatchRow {
	return PatchRow{X: d.U16(), Y: d.U16(), Cells: getSlice(d, getCell)}
}

func putPatchRow(e *wire.Enc, r PatchRow) {
	e.U(uint64(r.X)).U(uint64(r.Y))
	putSlice(e, r.Cells, putCell)
}

func getFrame(d *wire.Dec) FrameData {
	return FrameData{
		Cells:      getSlice(d, getCell),
		Width:      d.U16(),
		Height:     d.U16(),
		Cursor:     getCursor(d),
		Hyperlinks: getSlice(d, (*wire.Dec).Str),
		Graphics:   getBytes(d),
	}
}

func putFrame(e *wire.Enc, f *FrameData) {
	putSlice(e, f.Cells, putCell)
	e.U(uint64(f.Width)).U(uint64(f.Height))
	putCursor(e, f.Cursor)
	putSlice(e, f.Hyperlinks, putStr)
	e.Bytes(f.Graphics)
}

func getCell(d *wire.Dec) CellData {
	c := CellData{Symbol: d.Str(), FG: d.U32(), BG: d.U32(), Modifier: d.U16(), Skip: d.Bool()}
	c.Hyperlink = getOptU32(d)
	return c
}

func putCell(e *wire.Enc, c CellData) {
	e.Str(c.Symbol).U(uint64(c.FG)).U(uint64(c.BG)).U(uint64(c.Modifier)).Bool(c.Skip)
	putOptU(e, c.Hyperlink)
}

func getCursor(d *wire.Dec) *Cursor {
	if !d.Opt() {
		return nil
	}
	return &Cursor{X: d.U16(), Y: d.U16(), Visible: d.Bool(), Shape: d.U8()}
}

func putCursor(e *wire.Enc, c *Cursor) {
	e.Opt(c != nil)
	if c != nil {
		e.U(uint64(c.X)).U(uint64(c.Y)).Bool(c.Visible).U8(c.Shape)
	}
}

func getRect(d *wire.Dec) Rect {
	return Rect{X: d.U16(), Y: d.U16(), Width: d.U16(), Height: d.U16()}
}

func putRect(e *wire.Enc, r Rect) {
	e.U(uint64(r.X)).U(uint64(r.Y)).U(uint64(r.Width)).U(uint64(r.Height))
}

func getPane(d *wire.Dec) SurfacePane {
	p := SurfacePane{PaneID: d.Str(), ContentRevision: d.U(), Rect: getRect(d), InnerRect: getRect(d)}
	if d.Opt() {
		r := getRect(d)
		p.ScrollbarRect = &r
	}
	if d.Opt() {
		p.Scroll = &ScrollMetrics{OffsetFromBottom: d.U(), MaxOffsetFromBottom: d.U(), ViewportRows: d.U()}
	}
	p.Focused, p.MouseReporting, p.SGRPixelMouse, p.AlternateScreenActive = d.Bool(), d.Bool(), d.Bool(), d.Bool()
	p.PixelWidth, p.PixelHeight = d.U32(), d.U32()
	return p
}

func putPane(e *wire.Enc, p SurfacePane) {
	e.Str(p.PaneID).U(p.ContentRevision)
	putRect(e, p.Rect)
	putRect(e, p.InnerRect)
	e.Opt(p.ScrollbarRect != nil)
	if p.ScrollbarRect != nil {
		putRect(e, *p.ScrollbarRect)
	}
	e.Opt(p.Scroll != nil)
	if m := p.Scroll; m != nil {
		e.U(m.OffsetFromBottom).U(m.MaxOffsetFromBottom).U(m.ViewportRows)
	}
	e.Bool(p.Focused).Bool(p.MouseReporting).Bool(p.SGRPixelMouse).Bool(p.AlternateScreenActive).
		U(uint64(p.PixelWidth)).U(uint64(p.PixelHeight))
}

func getSplit(d *wire.Dec) Split {
	return Split{
		Direction: SplitDirection(variant(d, 2)),
		Pos:       d.U16(),
		Area:      getRect(d),
		HitRect:   getRect(d),
		Path:      getSlice(d, (*wire.Dec).Bool),
	}
}

func putSplit(e *wire.Enc, s Split) {
	e.U(uint64(s.Direction)).U(uint64(s.Pos))
	putRect(e, s.Area)
	putRect(e, s.HitRect)
	putSlice(e, s.Path, func(e *wire.Enc, b bool) { e.Bool(b) })
}

func getPopup(d *wire.Dec) *Popup {
	if !d.Opt() {
		return nil
	}
	p := &Popup{TerminalID: d.Str(), Title: d.Str(), Width: getPopupSize(d), Height: getPopupSize(d), Frame: getFrame(d)}
	p.MouseReporting, p.SGRPixelMouse = d.Bool(), d.Bool()
	p.PixelWidth, p.PixelHeight = d.U32(), d.U32()
	return p
}

func putPopup(e *wire.Enc, p *Popup) {
	e.Opt(p != nil)
	if p == nil {
		return
	}
	e.Str(p.TerminalID).Str(p.Title)
	putPopupSize(e, p.Width)
	putPopupSize(e, p.Height)
	putFrame(e, &p.Frame)
	e.Bool(p.MouseReporting).Bool(p.SGRPixelMouse).U(uint64(p.PixelWidth)).U(uint64(p.PixelHeight))
}

// ClientShellPopupSize: Cells(u16) 0, Percent(u8) 1.
func getPopupSize(d *wire.Dec) *PopupSize {
	if !d.Opt() {
		return nil
	}
	if variant(d, 2) == 0 {
		return &PopupSize{N: d.U16()}
	}
	return &PopupSize{Percent: true, N: uint16(d.U8())}
}

func putPopupSize(e *wire.Enc, s *PopupSize) {
	e.Opt(s != nil)
	switch {
	case s == nil:
	case s.Percent:
		e.U(1).U8(uint8(s.N))
	default:
		e.U(0).U(uint64(s.N))
	}
}

func getScene(d *wire.Dec) GraphicsScene {
	return GraphicsScene{
		Assets:         getSlice(d, getAsset),
		Placements:     getSlice(d, getPlacement),
		RetainedAssets: getSlice(d, getAssetKey),
	}
}

func putScene(e *wire.Enc, g *GraphicsScene) {
	putSlice(e, g.Assets, putAsset)
	putSlice(e, g.Placements, putPlacement)
	putSlice(e, g.RetainedAssets, putAssetKey)
}

func getAsset(d *wire.Dec) GraphicsAsset {
	return GraphicsAsset{Key: getAssetKey(d), Data: getBytes(d)}
}

func putAsset(e *wire.Enc, a GraphicsAsset) {
	putAssetKey(e, a.Key)
	e.Bytes(a.Data)
}

func getAssetKey(d *wire.Dec) GraphicsAssetKey {
	return GraphicsAssetKey{
		Source:          getSource(d),
		ImageWidth:      d.U32(),
		ImageHeight:     d.U32(),
		Format:          GraphicsFormat(variant(d, 3)),
		DataLen:         d.U(),
		DataFingerprint: d.U(),
	}
}

func putAssetKey(e *wire.Enc, k GraphicsAssetKey) {
	putSource(e, k.Source)
	e.U(uint64(k.ImageWidth)).U(uint64(k.ImageHeight)).U(uint64(k.Format)).U(k.DataLen).U(k.DataFingerprint)
}

// SurfaceGraphicsSource: Terminal {target, image_id} 0, PaneLayer {pane_id,
// layer_id} 1. SurfaceGraphicsTarget: Pane {pane_id} 0, Popup {terminal_id} 1.
func getSource(d *wire.Dec) GraphicsSource {
	if variant(d, 2) == 1 {
		return GraphicsSource{Kind: SourcePaneLayer, PaneID: d.Str(), LayerID: d.Str()}
	}
	t := GraphicsTarget{Popup: variant(d, 2) == 1, ID: d.Str()}
	return GraphicsSource{Kind: SourceTerminal, Target: t, ImageID: d.U32()}
}

func putSource(e *wire.Enc, s GraphicsSource) {
	if s.Kind == SourcePaneLayer {
		e.U(1).Str(s.PaneID).Str(s.LayerID)
		return
	}
	e.U(0)
	if s.Target.Popup {
		e.U(1)
	} else {
		e.U(0)
	}
	e.Str(s.Target.ID).U(uint64(s.ImageID))
}

func getPlacement(d *wire.Dec) GraphicsPlacement {
	return GraphicsPlacement{
		Asset:              getAssetKey(d),
		LogicalPlacementID: d.U32(),
		X:                  d.U16(),
		Y:                  d.U16(),
		Cols:               d.U32(),
		Rows:               d.U32(),
		SourceX:            d.U32(),
		SourceY:            d.U32(),
		SourceWidth:        d.U32(),
		SourceHeight:       d.U32(),
		XOffset:            d.U32(),
		YOffset:            d.U32(),
		Z:                  d.I32(),
		ScrollbackOffset:   d.U32(),
	}
}

func putPlacement(e *wire.Enc, p GraphicsPlacement) {
	putAssetKey(e, p.Asset)
	e.U(uint64(p.LogicalPlacementID)).U(uint64(p.X)).U(uint64(p.Y)).U(uint64(p.Cols)).U(uint64(p.Rows)).
		U(uint64(p.SourceX)).U(uint64(p.SourceY)).U(uint64(p.SourceWidth)).U(uint64(p.SourceHeight)).
		U(uint64(p.XOffset)).U(uint64(p.YOffset)).I(int64(p.Z)).U(uint64(p.ScrollbackOffset))
}
