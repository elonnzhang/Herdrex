package endpoint

import (
	"crypto/sha256"
	"encoding/hex"
	"reflect"
	"testing"

	"github.com/elonnzhang/herdrex/pkg/herdr/internal/wire"
)

// The frozen generation 1 payloads of Herdr 0.9.5's src/protocol/wire.rs
// tests (search encoded_sha256). Each value is built the way the Rust test
// builds it.

func ptr[T any](v T) *T { return &v }

func digest(e wire.Enc) string {
	sum := sha256.Sum256(e)
	return hex.EncodeToString(sum[:])
}

// Colors as color_to_u32 packs them (src/protocol/wire.rs:1516).
const (
	colReset   = 0x00
	colBlack   = 0x01
	colRed     = 0x02
	colGreen   = 0x03
	colYellow  = 0x04
	colBlue    = 0x05
	colMagenta = 0x06
	colCyan    = 0x07
)

// ratatui Modifier bits.
const (
	modBold       = 1
	modItalic     = 4
	modUnderlined = 8
	modReversed   = 64
)

func TestDigestClientShellResize(t *testing.T) { // wire.rs:1928
	got := digest(encResize(74, 29, 8, 16, true))
	want := "676d6376202750e72c45ff511e256b6154d3d20c0ee088d3792fa3a69d9704b9"
	if got != want {
		t.Errorf("digest %s, want %s", got, want)
	}
}

func TestDigestClientShellPaneInput(t *testing.T) { // wire.rs:2116
	rec := &WindowsKeyRecord{
		KeyDown: true, RepeatCount: 1, VirtualKeyCode: 0x37,
		VirtualScanCode: 0x08, Unicode: 0, ControlKeyState: 0x0008,
	}
	got := digest(encInput(cPaneInput, "w1:p2", []Input{
		Key{
			Code: KeyChar, Char: 'l', Mods: ModShift, Kind: KeyRelease, RepeatCount: 1,
			ShiftedCodepoint: ptr(uint32('L')), TracksRelease: true,
		},
		Key{
			Code: KeyChar, Char: '7', Mods: ModCtrl, Kind: KeyPress, RepeatCount: 1,
			TracksRelease: true, PhysicalKeyID: ptr(uint32(0x08)), WindowsRecord: rec,
		},
	}))
	want := "f558384bb53dfd2baf1fa72e1709d88be6891da79e51f88513905afc085065e6"
	if got != want {
		t.Errorf("digest %s, want %s", got, want)
	}
}

func TestDigestClientShellEndpointRequest(t *testing.T) { // wire.rs:2331
	got := digest(encRequest("boot-a", `{"id":"request-a","method":"session.snapshot","params":{}}`))
	want := "de5693585a01f6b0d5ee07c51b6ddf79ee9f67dbf183255822d31f35210f5ffb"
	if got != want {
		t.Errorf("digest %s, want %s", got, want)
	}
}

func TestDigestClipboardImage(t *testing.T) { // wire.rs:2366
	got := digest(encClipboardImage(ImageTarget{ID: "w1:p1"}, "png", []byte{0x89, 'P', 'N', 'G'}))
	want := "1c02110be0671faf318b3f4d8f507748d5a0a98cd474832669c5290d97ef19ab"
	if got != want {
		t.Errorf("digest %s, want %s", got, want)
	}
}

// The server-direction payloads also decode back to the values.
func TestDigestServerMessages(t *testing.T) {
	key := GraphicsAssetKey{
		Source:     GraphicsSource{Kind: SourceTerminal, Target: GraphicsTarget{ID: "w1:p1"}, ImageID: 7},
		ImageWidth: 2, ImageHeight: 1, Format: FormatRGBA, DataLen: 8, DataFingerprint: 42,
	}
	for _, tc := range []struct {
		name string
		m    Message
		want string
	}{
		{
			"ClientShellEndpointResponseChunk", // wire.rs:2347
			&responseChunk{
				BootID: "boot-a", RequestID: "request-a", Final: true,
				Data: []byte(`{"id":"request-a","result":{"type":"ok"}}`),
			},
			"bc14dbb5263d3097fe6d3e70a4b6d71aa9c2fa4ae3206d692a7182512bffdd1d",
		},
		{
			"PaneSurface", // wire.rs:2503
			&Surface{BootID: "boot-1", ProjectionRevision: 1, SurfaceRevision: 1, Frame: FrameData{
				Cells: []CellData{
					{Symbol: "H", FG: colRed, BG: colBlack, Modifier: modBold},
					{Symbol: "i", FG: colGreen, BG: colReset, Modifier: modItalic},
					{
						Symbol: "!", FG: 0x02FF8000, BG: 0x010000DC, Modifier: modBold | modUnderlined,
						Hyperlink: ptr(uint32(0)),
					},
					{Symbol: " ", FG: colReset, BG: colReset, Skip: true},
					{Symbol: "→", FG: colCyan, BG: colBlue, Modifier: modReversed},
					{Symbol: "🦀", FG: colYellow, BG: colMagenta},
				},
				Width: 3, Height: 2,
				Cursor:     &Cursor{X: 0, Y: 0, Visible: true, Shape: 6},
				Hyperlinks: []string{"https://example.com"},
			}},
			"7c016f7b21ddb5ac79212cf65a968b93eb292b5305b941263e89ffaa40158ee3",
		},
		{
			"PaneSurfacePatch", // wire.rs:2598
			&SurfacePatch{
				BootID: "boot-1", ProjectionRevision: 3, BaseSurfaceRevision: 7, SurfaceRevision: 8,
				Rows:   []PatchRow{{X: 2, Y: 4, Cells: []CellData{{Symbol: "x", FG: 1, BG: 2, Modifier: 3}}}},
				Cursor: &Cursor{X: 2, Y: 4, Visible: true, Shape: 2},
			},
			"0814b99a1dc6eaf7918424aa416c066509cbfb73b72344a809c27cde78cb6dbd",
		},
		{
			"PaneSurface with graphics", // wire.rs:2635
			&Surface{BootID: "boot-1", ProjectionRevision: 2, SurfaceRevision: 3, Graphics: GraphicsScene{
				Assets: []GraphicsAsset{{Key: key, Data: []byte{255, 0, 0, 255, 0, 255, 0, 255}}},
				Placements: []GraphicsPlacement{{
					Asset: key, LogicalPlacementID: 9, X: 1, Y: 2,
					Cols: 2, Rows: 1, SourceWidth: 2, SourceHeight: 1, Z: -1,
				}},
			}},
			"49c4efec0f1456c8ca4112ddf6ead1ab75d0224007576c2ccc18c3fca55a69f0",
		},
		{
			"Graphics", // wire.rs:2941
			&Graphics{Bytes: []byte("\x1b_Ga=d,d=A,q=2;\x1b\\")},
			"28a420f92e0e05e6760a8c140baf307c360c6d1b1aa68027481b324f87e22c44",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			e := encodeMessage(tc.m)
			if got := digest(e); got != tc.want {
				t.Errorf("digest %s, want %s", got, tc.want)
			}
			back, err := decodeMessage(e)
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(back, tc.m) {
				t.Errorf("decoded %+v, want %+v", back, tc.m)
			}
		})
	}
}
