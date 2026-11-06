package util

import (
	"bytes"
	"errors"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestRemoteCommandQuoting(t *testing.T) {
	// The line runs through a login shell first, then sh -c: check both.
	out, err := exec.Command("sh", "-c", RemoteCommand(`printf '%s|%s' "$1" "$2"`, "it's", "a b")).Output()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(out), "it's|a b"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestTailWriter(t *testing.T) {
	w := NewTailWriter("READY")
	w.Write([]byte("noise REA"))
	select {
	case <-w.Marker():
		t.Fatal("marker closed before the text was complete")
	default:
	}
	w.Write([]byte("DY\n"))
	select {
	case <-w.Marker():
	default:
		t.Fatal("marker not closed after a marker split over two writes")
	}
	w.Write([]byte(strings.Repeat("x", 10000)))
	if got := len(w.String()); got != 4096 {
		t.Fatalf("kept %d bytes, want 4096", got)
	}
}

func TestLockDir(t *testing.T) {
	prefix := "herdrex-test-" + filepath.Base(t.TempDir()) + "-"
	dir, unlock, err := LockDir(prefix, "seed", "a.sock", "b.sock")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	if _, _, err := LockDir(prefix, "seed"); !errors.Is(err, ErrLocked) {
		t.Fatalf("second LockDir err = %v, want ErrLocked", err)
	}
	unlock()

	// A stale socket goes; anything else stays and is refused.
	ln, err := net.Listen("unix", filepath.Join(dir, "a.sock"))
	if err != nil {
		t.Fatal(err)
	}
	ln.(*net.UnixListener).SetUnlinkOnClose(false)
	ln.Close()
	if err := os.WriteFile(filepath.Join(dir, "b.sock"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := LockDir(prefix, "seed", "a.sock", "b.sock"); err == nil || !strings.Contains(err.Error(), "non-socket") {
		t.Fatalf("err = %v, want refusal to replace a non-socket", err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "a.sock")); !os.IsNotExist(err) {
		t.Fatalf("stale socket still there: %v", err)
	}
}

func TestMuxScriptReadyLine(t *testing.T) {
	if !strings.Contains(muxScript, ReadyLine) {
		t.Fatalf("muxScript does not print %q", ReadyLine)
	}
}

func TestTailWriterBoundedAndFindsSplitMarker(t *testing.T) {
	w := NewTailWriter("READY-MARK")
	w.Write(bytes.Repeat([]byte("x"), 100<<10)) // far more than it keeps, before the marker
	if n := len(w.String()); n > 4096 {
		t.Fatalf("kept %d bytes before the marker, want at most 4096", n)
	}
	w.Write([]byte("..READY-"))
	select {
	case <-w.Marker():
		t.Fatal("marker reported before it was complete")
	default:
	}
	w.Write([]byte("MARK.."))
	select {
	case <-w.Marker():
	default:
		t.Fatal("marker split between two writes was not found")
	}
}

func TestMuxStreamQueueIsBounded(t *testing.T) {
	s := &muxStream{}
	s.cond = sync.NewCond(&s.mu)
	chunk := make([]byte, 1<<20)
	for i := 0; i < muxMaxQueue>>20; i++ {
		if !s.push(chunk) {
			t.Fatalf("push %d refused before the queue was full", i)
		}
	}
	if s.push(chunk) {
		t.Fatal("push past muxMaxQueue accepted")
	}
	if len(s.queue) != 0 || !s.closed {
		t.Errorf("after overflow: %d chunks queued, closed %v; want none queued and closed", len(s.queue), s.closed)
	}
}
