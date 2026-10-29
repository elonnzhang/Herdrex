package wire

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestUnsignedVarint(t *testing.T) {
	var e Enc
	e.U(0).U(250).U(251).U(300).U(70000).U(1 << 40)
	want := []byte{0, 250, 251, 251, 0, 251, 0x2c, 0x01, 252, 0x70, 0x11, 0x01, 0x00,
		253, 0, 0, 0, 0, 0, 1, 0, 0}
	if !bytes.Equal(e, want) {
		t.Fatalf("encoded % x, want % x", []byte(e), want)
	}
	d := NewDec(e)
	for _, w := range []uint64{0, 250, 251, 300, 70000, 1 << 40} {
		if got := d.U(); got != w {
			t.Fatalf("decoded %d, want %d", got, w)
		}
	}
	if err := d.Done(); err != nil {
		t.Fatal(err)
	}
}

func TestReservedVarintMarkers(t *testing.T) {
	for _, b := range []byte{254, 255} {
		d := NewDec([]byte{b, 0, 0})
		d.U()
		if d.Err() == nil {
			t.Errorf("marker %d accepted", b)
		}
	}
}

func TestSignedZigzag(t *testing.T) {
	cases := []struct {
		v    int64
		want []byte
	}{
		{0, []byte{0}}, {-1, []byte{1}}, {1, []byte{2}}, {-2, []byte{3}},
		{125, []byte{250}}, {-126, []byte{251, 251, 0}},
		{-1 << 31, []byte{252, 0xff, 0xff, 0xff, 0xff}},
	}
	for _, c := range cases {
		var e Enc
		e.I(c.v)
		if !bytes.Equal(e, c.want) {
			t.Errorf("I(%d) = % x, want % x", c.v, []byte(e), c.want)
		}
		d := NewDec(e)
		if got := d.I(); got != c.v || d.Done() != nil {
			t.Errorf("decoded %d, want %d", got, c.v)
		}
	}
	d := NewDec(*(&Enc{}).I(1 << 31))
	if d.I32(); d.Err() == nil {
		t.Error("I32 accepted 1<<31")
	}
}

func TestBoundedUnsigned(t *testing.T) {
	d := NewDec(*(&Enc{}).U(1 << 16))
	if d.U16(); d.Err() == nil {
		t.Error("U16 accepted 65536")
	}
	d = NewDec(*(&Enc{}).U(1<<32 - 1))
	if got := d.U32(); got != 1<<32-1 || d.Done() != nil {
		t.Errorf("U32 = %d", got)
	}
}

func TestBytesStringsChars(t *testing.T) {
	var e Enc
	e.U8(7).Bool(true).Bool(false).Str("héllo").Bytes([]byte{1, 2}).Char('l').Char('中').Opt(false).Opt(true).U(5)
	want := []byte{7, 1, 0, 6, 'h', 0xc3, 0xa9, 'l', 'l', 'o', 2, 1, 2, 'l', 0xe4, 0xb8, 0xad, 0, 1, 5}
	if !bytes.Equal(e, want) {
		t.Fatalf("encoded % x, want % x", []byte(e), want)
	}
	d := NewDec(e)
	if d.U8() != 7 || !d.Bool() || d.Bool() || d.Str() != "héllo" ||
		!bytes.Equal(d.Bytes(), []byte{1, 2}) || d.Char() != 'l' || d.Char() != '中' ||
		d.Opt() || !d.Opt() || d.U() != 5 {
		t.Fatal("round trip mismatch")
	}
	if err := d.Done(); err != nil {
		t.Fatal(err)
	}
}

func TestMalformedInput(t *testing.T) {
	cases := map[string]func(*Dec){
		"short string":      func(d *Dec) { d.Str() },
		"bool 2":            func(d *Dec) { d.Bool() },
		"invalid utf-8 str": func(d *Dec) { d.Str() },
		"invalid char":      func(d *Dec) { d.Char() },
		"empty char":        func(d *Dec) { d.Char() },
		"huge length":       func(d *Dec) { d.Len() },
		"short u64":         func(d *Dec) { d.U() },
	}
	inputs := map[string][]byte{
		"short string":      {5, 'a'},
		"bool 2":            {2},
		"invalid utf-8 str": {1, 0xff},
		"invalid char":      {0xff},
		"empty char":        {},
		"huge length":       {253, 0, 0, 0, 0, 0, 0, 0, 1, 0},
		"short u64":         {253, 1, 2},
	}
	for name, read := range cases {
		d := NewDec(inputs[name])
		read(d)
		if d.Err() == nil {
			t.Errorf("%s: accepted", name)
		}
		// Later reads keep returning zero values without panicking.
		if d.U() != 0 || d.Str() != "" {
			t.Errorf("%s: read after failure returned data", name)
		}
	}
}

func TestDoneRejectsTrailingBytes(t *testing.T) {
	d := NewDec([]byte{1, 2})
	d.U8()
	if !errors.Is(d.Done(), ErrMalformed) {
		t.Fatal("trailing byte accepted")
	}
	if len(d.Rest()) != 1 {
		t.Fatalf("rest = % x", d.Rest())
	}
	d.Fail()
	if d.Err() == nil {
		t.Fatal("Fail did not mark the decoder bad")
	}
}

func TestFrames(t *testing.T) {
	var buf bytes.Buffer
	if err := WriteFrame(&buf, []byte{9, 8, 7}); err != nil {
		t.Fatal(err)
	}
	if want := []byte{3, 0, 0, 0, 9, 8, 7}; !bytes.Equal(buf.Bytes(), want) {
		t.Fatalf("frame = % x", buf.Bytes())
	}
	p, err := ReadFrame(&buf, MaxFrame)
	if err != nil || !bytes.Equal(p, []byte{9, 8, 7}) {
		t.Fatalf("read %v % x", err, p)
	}
	if _, err := ReadFrame(&buf, MaxFrame); err != io.EOF {
		t.Fatalf("empty reader: %v", err)
	}
	if _, err := ReadFrame(bytes.NewReader([]byte{5, 0, 0, 0, 1}), MaxFrame); err != io.ErrUnexpectedEOF {
		t.Fatalf("truncated payload: %v", err)
	}
}

func TestFrameLimit(t *testing.T) {
	over := Frame(nil)
	over[0], over[1], over[2], over[3] = 1, 0, 0x20, 0 // 2 MiB + 1
	_, err := ReadFrame(bytes.NewReader(over), MaxFrame)
	if err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("2 MiB + 1 under MaxFrame: %v", err)
	}
	big := make([]byte, MaxFrame+1)
	p, err := ReadFrame(bytes.NewReader(Frame(big)), MaxGraphicsFrame)
	if err != nil || len(p) != len(big) {
		t.Fatalf("2 MiB + 1 under MaxGraphicsFrame: %v", err)
	}
}
