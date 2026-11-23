package wire

import (
	"bytes"
	"errors"
	"testing"
	"unicode/utf8"
)

// FuzzDec reads a payload with a sequence of readers that the input picks:
// the first byte says how many op bytes follow, and the rest is the payload.
// No input may panic, and once a read fails Err and Done report it.
func FuzzDec(f *testing.F) {
	var e Enc
	e.U(300).I(-5).U8(7).Bool(true).Opt(false).Str("héllo").Bytes([]byte{1, 2}).Char('中').U(1 << 40)
	f.Add(append([]byte{9, 1, 4, 0, 6, 7, 10, 9, 11, 1}, e...))
	f.Add([]byte{3, 2, 3, 5, 251, 0xff, 0xff, 252, 0, 0, 0, 0, 253})
	f.Add([]byte{2, 8, 9, 0xff, 'a'})
	f.Fuzz(func(t *testing.T, in []byte) {
		if len(in) == 0 {
			return
		}
		n := min(int(in[0]), len(in)-1)
		ops, payload := in[1:1+n], in[1+n:]
		d := NewDec(payload)
		failed := false
		for _, op := range ops {
			before := len(d.Rest())
			switch op % 12 {
			case 0:
				d.U8()
			case 1:
				d.U()
			case 2:
				d.U16()
			case 3:
				d.U32()
			case 4:
				d.I()
			case 5:
				d.I32()
			case 6:
				d.Bool()
			case 7:
				d.Opt()
			case 8:
				d.Len()
			case 9:
				if b := d.Bytes(); len(b) > before {
					t.Fatalf("Bytes returned %d bytes from %d", len(b), before)
				}
			case 10:
				d.Str()
			case 11:
				d.Char()
			}
			if len(d.Rest()) > before {
				t.Fatalf("op %d grew the rest from %d to %d bytes", op%12, before, len(d.Rest()))
			}
			if failed && d.Err() == nil {
				t.Fatal("Err cleared after a failed read")
			}
			failed = d.Err() != nil
		}
		if failed && !errors.Is(d.Done(), ErrMalformed) {
			t.Fatalf("Done = %v after a failed read", d.Done())
		}
	})
}

// FuzzRoundTrip checks that Dec reads back what Enc wrote.
func FuzzRoundTrip(f *testing.F) {
	f.Add(uint64(0), int64(0), byte(0), true, "", []byte(nil))
	f.Add(uint64(1<<64-1), int64(-1<<63), byte(255), false, "héllo", []byte{0, 251, 252, 253})
	f.Add(uint64(250), int64(1<<31), byte(1), true, "\x00", []byte("x"))
	f.Fuzz(func(t *testing.T, u uint64, i int64, b byte, ok bool, s string, p []byte) {
		var e Enc
		e.U(u).I(i).U8(b).Bool(ok).Bytes(p)
		valid := utf8.ValidString(s)
		if valid {
			e.Str(s)
		}
		d := NewDec(e)
		if got := d.U(); got != u {
			t.Errorf("U = %d, want %d", got, u)
		}
		if got := d.I(); got != i {
			t.Errorf("I = %d, want %d", got, i)
		}
		if got := d.U8(); got != b {
			t.Errorf("U8 = %d, want %d", got, b)
		}
		if got := d.Bool(); got != ok {
			t.Errorf("Bool = %v, want %v", got, ok)
		}
		if got := d.Bytes(); !bytes.Equal(got, p) {
			t.Errorf("Bytes = %x, want %x", got, p)
		}
		if valid {
			if got := d.Str(); got != s {
				t.Errorf("Str = %q, want %q", got, s)
			}
		}
		if err := d.Done(); err != nil {
			t.Errorf("Done: %v", err)
		}
	})
}
