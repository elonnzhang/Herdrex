package endpoint

import (
	"encoding/json"
	"errors"
	"os"
	"reflect"
	"testing"

	"github.com/elonnzhang/herdrex/pkg/herdr/internal/wire"
)

// FuzzDecodeMessage decodes arbitrary server payloads. No input may panic, a
// payload that is not a known message decodes as malformed or as invalid
// control JSON, and a message that decodes reencodes to one that decodes the
// same.
func FuzzDecodeMessage(f *testing.F) {
	for _, m := range []Message{
		testSurface(),
		&SurfacePatch{
			BootID: "b", ProjectionRevision: 2, BaseSurfaceRevision: 5, SurfaceRevision: 6,
			Rows:  []PatchRow{{X: 1, Y: 1, Cells: []CellData{{Symbol: "o"}, {Symbol: "k"}}}},
			Panes: []SurfacePane{{PaneID: "w1:p2", ContentRevision: 2}}, Cursor: &Cursor{X: 2, Y: 1, Visible: true},
		},
		&Notification{
			Kind: NeedsAttention, Title: "codex needs attention", Body: "repo · 1",
			Sound: SoundRequest, Agent: "codex", WorkspaceID: "w1", TabID: "w1:t1", PaneID: "w1:p1",
			Position: ToastTopRight,
		},
		&Notify{Kind: 2, Message: "m", Body: "b"},
		&Shutdown{Reason: "detached"},
		&Bell{Count: 300},
		&MouseCaptureState{Enabled: true, SGRPixels: true},
		&AgentCompletions{BootID: "boot-1", Revision: 2, Completions: map[string]uint64{"w1:p1": 1}},
		&Graphics{Bytes: []byte{1}},
		&responseChunk{BootID: "b", RequestID: "r1", Final: true, Data: []byte(`{"ok":true}`)},
	} {
		f.Add([]byte(encodeMessage(m)))
	}
	snapshot, err := os.ReadFile("testdata/endpoint-snapshot-v1.json")
	if err != nil {
		f.Fatal(err)
	}
	var e wire.Enc
	f.Add([]byte(*e.U(sControl).Str(kindSnapshot).Str(string(snapshot))))
	f.Fuzz(func(t *testing.T, p []byte) {
		m, err := decodeMessage(p)
		if err != nil {
			var se *json.SyntaxError
			var te *json.UnmarshalTypeError
			if !errors.Is(err, wire.ErrMalformed) && !errors.As(err, &se) && !errors.As(err, &te) {
				t.Fatalf("error %v is neither malformed nor invalid JSON", err)
			}
			return
		}
		if _, ok := m.(*Other); ok {
			return
		}
		again, err := decodeMessage(encodeMessage(m))
		if err != nil {
			t.Fatalf("%T reencoded does not decode: %v", m, err)
		}
		if !reflect.DeepEqual(again, m) {
			t.Fatalf("reencoded\n%#v\ndecodes as\n%#v", m, again)
		}
	})
}
