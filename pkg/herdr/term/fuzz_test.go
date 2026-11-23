package term

import (
	"errors"
	"testing"

	"github.com/elonnzhang/herdrex/pkg/herdr/internal/wire"
)

// FuzzNext decodes arbitrary server payloads. No input may panic, and a
// whole frame that does not decode is wire.ErrMalformed.
func FuzzNext(f *testing.F) {
	for _, p := range [][]byte{
		{0, 22, 1, 0},
		{0, 22, 1, 1, 3, 'b', 'a', 'd'},
		{1, 251, 0x2c, 0x01, 80, 24, 1, 3, 'a', 'b', 'c'},
		append([]byte{3, 1, 8}, "detached"...),
		append([]byte{5, 4}, "aGk="...),
		{6, 1, 2, 'h', 'i'},
		{7},
		{8, 1, 1},
		{9, 2},
		{16, 251, 0x2c, 0x01, 0},
		{20, 0, 0},
	} {
		f.Add(p)
	}
	f.Fuzz(func(t *testing.T, p []byte) {
		m, err := next(p)
		if err != nil && !errors.Is(err, wire.ErrMalformed) {
			t.Fatalf("error %v is not wire.ErrMalformed", err)
		}
		if err == nil && m == nil {
			t.Fatal("no message and no error")
		}
	})
}
