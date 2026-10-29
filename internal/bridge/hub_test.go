package bridge

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/elonnzhang/herdrex/pkg/herdr/term"
)

// Server messages in Herdr's bincode encoding, without the length prefix.
var (
	welcome      = []byte{0, 22, 1, 0}                  // Welcome{22, TerminalAnsi, None}
	mouseOn      = []byte{8, 1, 0}                      // MouseCapture{true, false}
	frame80x24   = []byte{1, 1, 80, 24, 1, 2, 'h', 'i'} // Terminal{1, 80, 24, full, "hi"}
	shutdownGone = []byte{3, 1, 4, 'g', 'o', 'n', 'e'}  // ServerShutdown{Some("gone")}
)

// fakeTerminal starts a Unix socket that answers the hello with a welcome,
// then writes msgs, then holds the connection until the test ends. It
// returns a dialled term.Conn.
func fakeTerminal(t *testing.T, msgs ...[]byte) *term.Conn {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "hxb") // Unix socket paths are short on macOS
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	ln, err := net.Listen("unix", filepath.Join(dir, "c.sock"))
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	t.Cleanup(func() { close(done); ln.Close() })
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		var n [4]byte
		if _, err := io.ReadFull(c, n[:]); err != nil {
			return
		}
		if _, err := io.CopyN(io.Discard, c, int64(binary.LittleEndian.Uint32(n[:]))); err != nil {
			return
		}
		for _, m := range append([][]byte{welcome}, msgs...) {
			c.Write(binary.LittleEndian.AppendUint32(nil, uint32(len(m))))
			c.Write(m)
		}
		<-done
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c, err := term.Dial(ctx, ln.Addr().String(), 22, term.Size{Cols: 80, Rows: 24})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestFirstFrame(t *testing.T) {
	c := fakeTerminal(t, mouseOn, frame80x24)
	f, mouse, err := firstFrame(context.Background(), c)
	if err != nil {
		t.Fatal(err)
	}
	if !mouse || f.Width != 80 || f.Height != 24 || !f.Full || string(f.Bytes) != "hi" {
		t.Fatalf("frame %+v, mouse %v", f, mouse)
	}
}

func TestFirstFrameShutdown(t *testing.T) {
	c := fakeTerminal(t, shutdownGone)
	if _, _, err := firstFrame(context.Background(), c); err == nil || err.Error() != "gone" {
		t.Fatalf("err = %v, want gone", err)
	}
}

func TestFirstFrameTimeout(t *testing.T) {
	defer func(d time.Duration) { firstFrameTimeout = d }(firstFrameTimeout)
	firstFrameTimeout = 100 * time.Millisecond
	c := fakeTerminal(t, mouseOn) // a frame never comes
	start := time.Now()
	_, _, err := firstFrame(context.Background(), c)
	if err == nil || !strings.Contains(err.Error(), "no frame within") {
		t.Fatalf("err = %v, want a timeout", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("returned after %v", d)
	}
	if _, err := c.Next(); err == nil {
		t.Fatal("connection still open after the timeout")
	}
}

func TestFirstFrameCancel(t *testing.T) {
	c := fakeTerminal(t)
	ctx, cancel := context.WithCancel(context.Background())
	time.AfterFunc(50*time.Millisecond, cancel)
	start := time.Now()
	if _, _, err := firstFrame(ctx, c); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Fatalf("returned after %v", d)
	}
}
