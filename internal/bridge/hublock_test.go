package bridge

import (
	"testing"
	"time"

	"github.com/elonnzhang/herdrex/pkg/rex/client"
)

// A pane whose terminal is slow to start or to detach holds up only its own
// streams: calls on other panes answer at once.
func TestSlowPaneBlocksOnlyItself(t *testing.T) {
	const slow = 1500 * time.Millisecond
	sizeOfOther := func(t *testing.T, b *Bridge, conn *client.Conn, sid string) time.Duration {
		start := time.Now()
		call(t, conn, "block.method", P{"session_id": sid, "block_id": b.IDs.Rex("block", "w1:p2"), "method": "size"}, nil)
		return time.Since(start)
	}
	t.Run("first frame", func(t *testing.T) {
		f, b, conn, sid := hubSetup(t)
		f.edit(func(s *fakeState) { s.firstFrame["w1:p1"] = slow })
		go conn.Terminal(t.Context(), sid, b.IDs.Rex("block", "w1:p1"))
		nextTerm(t, f) // the hub is waiting for the first frame
		if d := sizeOfOther(t, b, conn, sid); d > slow/3 {
			t.Errorf("size of another pane took %v while one pane waited for its first frame", d)
		}
	})
	t.Run("detach", func(t *testing.T) {
		f, b, conn, sid := hubSetup(t)
		f.edit(func(s *fakeState) { s.detachDelay["w1:p1"] = slow })
		s := openTerm(t, conn, sid, b.IDs.Rex("block", "w1:p1"))
		ft := nextTerm(t, f)
		readUntil(t, s, shows("hi"))
		s.Close()
		for nextMsg(t, ft).tag != tagDetach { // the hub now waits for Herdr to answer
		}
		if d := sizeOfOther(t, b, conn, sid); d > slow/3 {
			t.Errorf("size of another pane took %v while one pane detached", d)
		}
	})
}

// A new stream for a pane that is still detaching waits for the old
// attachment to end, so Herdr never sees two controlling clients for it.
func TestRejoinWaitsForDetach(t *testing.T) {
	f, b, conn, sid := hubSetup(t)
	f.edit(func(s *fakeState) { s.detachDelay["w1:p1"] = 500 * time.Millisecond })
	block := b.IDs.Rex("block", "w1:p1")
	s := openTerm(t, conn, sid, block)
	old := nextTerm(t, f)
	readUntil(t, s, shows("hi"))
	s.Close()
	for nextMsg(t, old).tag != tagDetach {
	}
	again := openTerm(t, attached(t, b, sid), sid, block)
	ft := nextTerm(t, f)
	select {
	case <-old.closed:
	default:
		t.Fatal("the new stream connected before the old attachment ended")
	}
	if !ft.control {
		t.Fatal("the new stream fell back to observing")
	}
	readUntil(t, again, shows("hi"))
}
