package term

import (
	"bytes"
	"context"
	"net"
	"os"
	"testing"
	"time"
)

func TestEncoding(t *testing.T) {
	var e enc
	e.v(cHello).v(22).size(Size{Cols: 100, Rows: 30, CellWidthPx: 15, CellHeightPx: 36})
	if want := []byte{0, 22, 100, 30, 15, 36, 0}; !bytes.Equal(e, want) {
		t.Fatalf("hello = % x, want % x", []byte(e), want)
	}
	e = nil
	e.v(300).v(70000).v(1 << 40)
	want := []byte{251, 0x2c, 0x01, 252, 0x70, 0x11, 0x01, 0x00, 253, 0, 0, 0, 0, 0, 1, 0, 0}
	if !bytes.Equal(e, want) {
		t.Fatalf("varints = % x, want % x", []byte(e), want)
	}
	d := dec{b: e}
	if a, b, c := d.v(), d.v(), d.v(); a != 300 || b != 70000 || c != 1<<40 || d.bad {
		t.Fatalf("decoded %d %d %d bad=%v", a, b, c, d.bad)
	}
	d = dec{b: []byte{5, 'a'}}
	if d.bytes(); !d.bad {
		t.Fatal("short string not detected")
	}
}

func TestMouseEncoding(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	c := &Conn{c: a}
	read := func(m Mouse) []byte {
		go c.Mouse(m)
		buf := make([]byte, 64)
		n, _ := b.Read(buf)
		return buf[:n]
	}
	// length, AttachMouse, Down(Right), Cell{10, 300}, no geometry, ctrl, 1 line
	got := read(Mouse{Kind: MouseDown, Button: ButtonRight, Column: 10, Row: 300, Mods: ModCtrl})
	if want := []byte{11, 0, 0, 0, 16, 0, 1, 0, 10, 251, 0x2c, 0x01, 0, 2, 1}; !bytes.Equal(got, want) {
		t.Fatalf("mouse = % x, want % x", got, want)
	}
	got = read(Mouse{Kind: ScrollUp, Column: 1, Row: 2, Lines: 3})
	if want := []byte{8, 0, 0, 0, 16, 4, 0, 1, 2, 0, 0, 3}; !bytes.Equal(got, want) {
		t.Fatalf("scroll = % x, want % x", got, want)
	}
}

func TestClientSocket(t *testing.T) {
	if got := ClientSocket("/tmp/x/herdr.sock"); got != "/tmp/x/herdr-client.sock" {
		t.Fatal(got)
	}
}

// TestLive runs against a real Herdr server. Set HERDR_TERM_TEST to
// "<api socket>,<pane id>,<protocol>", e.g. "/tmp/rexbridge/herdr.sock,w1:p3,22".
func TestLive(t *testing.T) {
	spec := os.Getenv("HERDR_TERM_TEST")
	if spec == "" {
		t.Skip("HERDR_TERM_TEST not set")
	}
	var sock, pane string
	var proto uint32
	parts := bytes.Split([]byte(spec), []byte(","))
	sock, pane = string(parts[0]), string(parts[1])
	for _, ch := range parts[2] {
		proto = proto*10 + uint32(ch-'0')
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := Dial(ctx, ClientSocket(sock), proto, Size{Cols: 70, Rows: 21, CellWidthPx: 15, CellHeightPx: 36})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	if err := c.Control(pane, false); err != nil {
		t.Fatal(err)
	}
	frame := func(cond func(*Frame) bool) *Frame {
		for {
			m, err := c.Next()
			if err != nil {
				t.Fatal(err)
			}
			switch f := m.(type) {
			case *Frame:
				if cond(f) {
					return f
				}
			case *Shutdown:
				t.Fatalf("shutdown: %s", f.Reason)
			}
		}
	}
	if f := frame(func(f *Frame) bool { return f.Full }); f.Width != 70 || f.Height != 21 {
		t.Fatalf("first frame %dx%d", f.Width, f.Height)
	}
	c.Input([]byte("echo term-$((6*7))\r"))
	frame(func(f *Frame) bool { return bytes.Contains(f.Bytes, []byte("term-42")) })
	c.Resize(Size{Cols: 83, Rows: 40})
	frame(func(f *Frame) bool { return f.Full && f.Width == 83 && f.Height == 40 })
	c.Detach()
	for {
		m, err := c.Next()
		if err != nil {
			t.Fatal(err)
		}
		if s, ok := m.(*Shutdown); ok {
			if s.Reason != "detached" {
				t.Fatalf("shutdown reason %q", s.Reason)
			}
			return
		}
	}
}
