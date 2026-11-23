package bridge

import (
	"slices"
	"testing"

	"github.com/elonnzhang/herdrex/pkg/rex"
	"github.com/elonnzhang/herdrex/pkg/rex/termstream"
)

// The first stream of a pane opens one controlling connection at the layout
// size. Later streams share it, each getting a full frame. The owner's size
// reaches the PTY, and the last stream to leave restores the layout size and
// detaches.
func TestHubJoinLeave(t *testing.T) {
	f, b, conn, sid := hubSetup(t)
	block := b.IDs.Rex("block", "w1:p1")
	s1 := openTerm(t, conn, sid, block)
	ft := nextTerm(t, f)
	if !ft.control || ft.hello != cells(80, 24) {
		t.Fatalf("first connection: control %v at %+v, want control at 80x24", ft.control, ft.hello)
	}
	readUntil(t, s1, shows("hi"))

	s2 := openTerm(t, attached(t, b, sid), sid, block)
	if m := nextMsg(t, ft); m.tag != tagResize || m.size != cells(80, 24) {
		t.Fatalf("join sent %+v, want a same-size Resize", m)
	}
	readUntil(t, s2, shows("hi"))
	select {
	case extra := <-f.terms:
		t.Fatalf("a second stream opened another connection to %s", extra.pane)
	default:
	}

	call(t, conn, "block.method", P{"session_id": sid, "block_id": block, "method": "resize",
		"args": P{"columns": 100.0, "rows": 30.0}}, nil)
	if m := nextMsg(t, ft); m.tag != tagResize || m.size != cells(100, 30) {
		t.Fatalf("owner resize sent %+v, want Resize 100x30", m)
	}

	s2.Close()
	s1.Close()
	if m := nextMsg(t, ft); m.tag != tagResize || m.size != cells(80, 24) {
		t.Fatalf("last leave sent %+v, want Resize back to 80x24", m)
	}
	if m := nextMsg(t, ft); m.tag != tagDetach {
		t.Fatalf("last leave sent %+v, want Detach", m)
	}
	<-ft.closed
}

// When Herdr ends the attachment, streams close with block closed after
// child_exited if the pane is gone, and with reconnect required otherwise.
func TestHubRunEnds(t *testing.T) {
	for _, tc := range []struct {
		name   string
		end    func(*fakeTerm)
		code   int
		exited bool
	}{
		{"pane exited", func(ft *fakeTerm) { ft.end("terminal w1:p1 exited") }, rex.CloseBlockClosed, true},
		{"connection lost", func(ft *fakeTerm) { ft.c.Close() }, rex.CloseReconnectRequired, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, b, conn, sid := hubSetup(t)
			s := openTerm(t, conn, sid, b.IDs.Rex("block", "w1:p1"))
			ft := nextTerm(t, f)
			readUntil(t, s, shows("hi"))
			tc.end(ft)
			var code int
			parts := readUntil(t, s, closedWith(&code))
			exited := slices.ContainsFunc(parts, func(p termstream.Part) bool { return p.Type == "child_exited" })
			if code != tc.code || exited != tc.exited {
				t.Errorf("closed with %d, child_exited %v; want %d, %v", code, exited, tc.code, tc.exited)
			}
		})
	}
}
