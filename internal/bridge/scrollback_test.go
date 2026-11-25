package bridge

import (
	"bytes"
	"testing"

	"github.com/elonnzhang/herdrex/pkg/rex/client"
)

// A plain shell leaves the mouse to the client; a mouse TUI turns any-motion
// reports on, and turning them off restores the shell mode.
func TestScrollbackMouseModes(t *testing.T) {
	f, b, conn, sid := hubSetup(t)
	block := b.IDs.Rex("block", "w1:p1")
	stream := openTerm(t, conn, sid, block)
	ft := nextTerm(t, f)
	check := func(s *client.Stream, marker string, sequences ...string) {
		t.Helper()
		var out []byte
		for _, part := range readUntil(t, s, shows(marker)) {
			out = append(out, part.Output...)
		}
		for _, seq := range sequences {
			if !bytes.Contains(out, []byte(seq)) {
				t.Errorf("stream through %q missing %q", marker, seq)
			}
		}
	}
	check(stream, "hi", "\x1b[?1002l", "\x1b[?1003l", "\x1b[?1006l")
	ft.write(mouseOn)
	check(stream, "\x1b[?1003h", "\x1b[?1003h")
	if err := event(t, b, conn, "wheel", P{"delta_y": -1, "delta_mode": "line"}); err != nil {
		t.Fatal(err)
	}
	if m := nextMsg(t, ft); m.tag != 16 { // AttachMouse
		t.Fatalf("TUI wheel sent Herdr message %d, want AttachMouse", m.tag)
	}
	second := openTerm(t, attached(t, b, sid), sid, block)
	check(second, "hi", "\x1b[?1003h", "\x1b[?1006h")
	ft.write([]byte{8, 0, 0}) // MouseCapture{false, false}
	ft.frame(3, ft.hello)
	check(second, "hi", "\x1b[?1003l", "\x1b[?1006l")
}
