package term

import (
	"encoding/binary"
	"time"
)

var timeZero time.Time

// enc writes bincode 2 "standard" encoding: variable-length integers,
// one-byte bools, length-prefixed strings and byte vectors.
type enc []byte

func (e *enc) v(x uint64) *enc {
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

func (e *enc) bool(b bool) *enc {
	if b {
		*e = append(*e, 1)
	} else {
		*e = append(*e, 0)
	}
	return e
}

func (e *enc) bytes(b []byte) *enc { e.v(uint64(len(b))); *e = append(*e, b...); return e }
func (e *enc) str(s string) *enc   { return e.bytes([]byte(s)) }

func (e *enc) size(s Size) *enc {
	return e.v(uint64(s.Cols)).v(uint64(s.Rows)).
		v(uint64(s.CellWidthPx)).v(uint64(s.CellHeightPx)).bool(s.PixelMouse)
}

// dec reads the same encoding. A short or malformed input sets bad and
// yields zero values.
type dec struct {
	b   []byte
	bad bool
}

func (d *dec) take(n int) []byte {
	if d.bad || len(d.b) < n {
		d.bad = true
		return make([]byte, n)
	}
	p := d.b[:n]
	d.b = d.b[n:]
	return p
}

func (d *dec) u8() byte { return d.take(1)[0] }

func (d *dec) v() uint64 {
	switch x := d.u8(); x {
	case 251:
		return uint64(binary.LittleEndian.Uint16(d.take(2)))
	case 252:
		return uint64(binary.LittleEndian.Uint32(d.take(4)))
	case 253:
		return binary.LittleEndian.Uint64(d.take(8))
	case 254, 255: // u128 and reserved; Herdr does not send them
		d.bad = true
		return 0
	default:
		return uint64(x)
	}
}

func (d *dec) bytes() []byte {
	n := d.v()
	if n > uint64(len(d.b)) {
		d.bad = true
		return nil
	}
	return d.take(int(n))
}
