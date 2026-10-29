// Package wire is the framing and bincode encoding that Herdr's direct
// terminal protocol and endpoint protocol share on herdr-client.sock
// (pkg/herdr/HERDR.md §4.1).
//
// A frame is a little-endian u32 payload length followed by one message
// encoded with bincode 2's standard configuration through serde:
//   - u16, u32, u64 and usize are variable-length: a value below 251 is one
//     byte, otherwise a marker byte (251, 252, 253) and the value as a
//     little-endian u16, u32 or u64.
//   - signed integers are zigzag-encoded, then written as above.
//   - u8 is one raw byte; bool is one byte, 0 or 1.
//   - char is its UTF-8 bytes.
//   - strings and byte vectors are a length, then the bytes; sequences are a
//     length, then the elements.
//   - Option is a u8 tag, 0 for None and 1 for Some, then the value.
//   - an enum is its variant index as a variable-length u32, then the
//     variant's fields in declaration order. Structs are their fields in
//     order, with no names.
//
// Field and variant order is the wire format, so types that use this package
// must follow Herdr's declarations exactly (src/protocol/wire.rs).
package wire

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

const (
	// MaxFrame is Herdr's payload limit for ordinary messages and for the
	// handshake (src/protocol/wire.rs:24).
	MaxFrame = 2 << 20
	// MaxGraphicsFrame is Herdr's payload limit for messages that carry
	// images, in both directions after the handshake (src/protocol/wire.rs:29).
	MaxGraphicsFrame = 32 << 20
)

// ErrMalformed reports a payload that does not decode as the expected type.
var ErrMalformed = errors.New("herdr: malformed message")

// Frame returns payload with its length prefix.
func Frame(payload []byte) []byte {
	b := make([]byte, 4, 4+len(payload))
	binary.LittleEndian.PutUint32(b, uint32(len(payload)))
	return append(b, payload...)
}

// WriteFrame writes payload with its length prefix in one Write.
func WriteFrame(w io.Writer, payload []byte) error {
	_, err := w.Write(Frame(payload))
	return err
}

// ReadFrame reads one frame and returns its payload. A length above max is an
// error, and nothing past the length prefix is read.
func ReadFrame(r io.Reader, max int) ([]byte, error) {
	var h [4]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return nil, err
	}
	n := binary.LittleEndian.Uint32(h[:])
	if uint64(n) > uint64(max) {
		return nil, fmt.Errorf("herdr: frame of %d bytes exceeds the %d byte limit", n, max)
	}
	p := make([]byte, n)
	if _, err := io.ReadFull(r, p); err != nil {
		if errors.Is(err, io.EOF) {
			err = io.ErrUnexpectedEOF
		}
		return nil, err
	}
	return p, nil
}

// Enc appends bincode-encoded values. Each method returns e for chaining.
type Enc []byte

// U writes an unsigned integer wider than a byte (u16, u32, u64, usize, an
// enum variant index or a length).
func (e *Enc) U(x uint64) *Enc {
	switch {
	case x < 251:
		*e = append(*e, byte(x))
	case x < 1<<16:
		*e = binary.LittleEndian.AppendUint16(append(*e, 251), uint16(x))
	case x < 1<<32:
		*e = binary.LittleEndian.AppendUint32(append(*e, 252), uint32(x))
	default:
		*e = binary.LittleEndian.AppendUint64(append(*e, 253), x)
	}
	return e
}

// I writes a signed integer wider than a byte, zigzag-encoded.
func (e *Enc) I(x int64) *Enc { return e.U(uint64(x<<1) ^ uint64(x>>63)) }

// U8 writes one raw byte.
func (e *Enc) U8(b byte) *Enc { *e = append(*e, b); return e }

// Bool writes 1 or 0.
func (e *Enc) Bool(b bool) *Enc {
	if b {
		return e.U8(1)
	}
	return e.U8(0)
}

// Bytes writes a length and b.
func (e *Enc) Bytes(b []byte) *Enc { e.U(uint64(len(b))); *e = append(*e, b...); return e }

// Str writes a length and the string's bytes.
func (e *Enc) Str(s string) *Enc { e.U(uint64(len(s))); *e = append(*e, s...); return e }

// Char writes r as UTF-8.
func (e *Enc) Char(r rune) *Enc { *e = utf8.AppendRune(*e, r); return e }

// Opt writes an Option tag. Write the value after it when present is true.
func (e *Enc) Opt(present bool) *Enc { return e.Bool(present) }

// Dec reads bincode-encoded values from a payload. A short or malformed input
// marks the decoder bad; later reads return zero values. Check Err or Done
// after decoding.
type Dec struct {
	b   []byte
	bad bool
}

// NewDec decodes payload.
func NewDec(payload []byte) *Dec { return &Dec{b: payload} }

// Err returns ErrMalformed once any read has failed.
func (d *Dec) Err() error {
	if d.bad {
		return ErrMalformed
	}
	return nil
}

// Done returns ErrMalformed if any read failed or bytes remain: Herdr
// rejects trailing bytes (src/protocol/wire.rs:1699), and so do its clients.
func (d *Dec) Done() error {
	if d.bad || len(d.b) != 0 {
		return ErrMalformed
	}
	return nil
}

// Rest returns the bytes not yet read.
func (d *Dec) Rest() []byte { return d.b }

// Fail marks the decoder bad, for a value the caller found invalid.
func (d *Dec) Fail() { d.bad = true }

func (d *Dec) take(n int) []byte {
	if d.bad || n < 0 || len(d.b) < n {
		d.bad = true
		return make([]byte, max(n, 0))
	}
	p := d.b[:n]
	d.b = d.b[n:]
	return p
}

// U8 reads one raw byte.
func (d *Dec) U8() byte { return d.take(1)[0] }

// U reads a variable-length unsigned integer.
func (d *Dec) U() uint64 {
	switch x := d.U8(); x {
	case 251:
		return uint64(binary.LittleEndian.Uint16(d.take(2)))
	case 252:
		return uint64(binary.LittleEndian.Uint32(d.take(4)))
	case 253:
		return binary.LittleEndian.Uint64(d.take(8))
	case 254, 255: // u128 and reserved; Herdr's types have neither
		d.bad = true
		return 0
	default:
		return uint64(x)
	}
}

// U16 reads a u16, marking the decoder bad if the value does not fit.
func (d *Dec) U16() uint16 { return uint16(d.bounded(1<<16 - 1)) }

// U32 reads a u32, marking the decoder bad if the value does not fit.
func (d *Dec) U32() uint32 { return uint32(d.bounded(1<<32 - 1)) }

func (d *Dec) bounded(limit uint64) uint64 {
	x := d.U()
	if x > limit {
		d.bad = true
		return 0
	}
	return x
}

// I reads a zigzag-encoded signed integer.
func (d *Dec) I() int64 {
	x := d.U()
	return int64(x>>1) ^ -int64(x&1)
}

// I32 reads an i32, marking the decoder bad if the value does not fit.
func (d *Dec) I32() int32 {
	x := d.I()
	if x < -1<<31 || x > 1<<31-1 {
		d.bad = true
		return 0
	}
	return int32(x)
}

// Bool reads one byte that must be 0 or 1.
func (d *Dec) Bool() bool {
	switch d.U8() {
	case 0:
		return false
	case 1:
		return true
	}
	d.bad = true
	return false
}

// Opt reads an Option tag and reports whether a value follows.
func (d *Dec) Opt() bool { return d.Bool() }

// Len reads a sequence length. A length larger than the bytes left is
// malformed: every element of Herdr's sequences takes at least one byte, so
// this bounds allocations by the payload size.
func (d *Dec) Len() int {
	n := d.U()
	if n > uint64(len(d.b)) {
		d.bad = true
		return 0
	}
	return int(n)
}

// Bytes reads a length and that many bytes. The result aliases the payload.
func (d *Dec) Bytes() []byte {
	n := d.Len()
	if d.bad {
		return nil
	}
	return d.take(n)
}

// Str reads a length-prefixed string, which must be UTF-8.
func (d *Dec) Str() string {
	b := d.Bytes()
	if !utf8.Valid(b) {
		d.bad = true
		return ""
	}
	return string(b)
}

// Char reads one UTF-8 encoded char.
func (d *Dec) Char() rune {
	if d.bad || len(d.b) == 0 {
		d.bad = true
		return 0
	}
	r, n := utf8.DecodeRune(d.b)
	if r == utf8.RuneError && n <= 1 {
		d.bad = true
		return 0
	}
	d.b = d.b[n:]
	return r
}
