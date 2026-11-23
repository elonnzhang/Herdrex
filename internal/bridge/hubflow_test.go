package bridge

import (
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/elonnzhang/herdrex/pkg/herdr/term"
	"github.com/elonnzhang/herdrex/pkg/rex/client"
	"github.com/elonnzhang/herdrex/pkg/rex/termstream"
)

// hubSetup is a bridge over one workspace with panes w1:p1 and w1:p2, and
// a Rex connection attached to it.
func hubSetup(t *testing.T) (*fakeHerdr, *Bridge, *client.Conn, string) {
	t.Helper()
	f := newFakeHerdr(t)
	f.edit(func(s *fakeState) {
		s.addWorkspace("w1", "one")
		s.addTab("w1", "w1:t1", "w1:p1", "w1:p2")
	})
	b, conn := newRexBridge(t, f)
	sid := b.IDs.Rex("session", "w1")
	call(t, conn, "session.attach", P{"session_id": sid}, nil)
	return f, b, conn, sid
}

// attached is another Rex connection attached to the session.
func attached(t *testing.T, b *Bridge, sid string) *client.Conn {
	t.Helper()
	conn, _ := rexTestClient(t, b)
	call(t, conn, "session.attach", P{"session_id": sid}, nil)
	return conn
}

func openTerm(t *testing.T, conn *client.Conn, sid, block string) *client.Stream {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	s, err := conn.Terminal(ctx, sid, block)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s
}

// readUntil reads s until a part satisfies done, and returns the parts read.
// It fails on the stream's end unless done accepts a nil part then.
func readUntil(t *testing.T, s *client.Stream, done func(*termstream.Part, error) bool) []termstream.Part {
	t.Helper()
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	var all []termstream.Part
	for {
		parts, err := s.Read(ctx)
		if err != nil {
			if done(nil, err) {
				return all
			}
			t.Fatalf("stream ended: %v", err)
		}
		for i := range parts {
			all = append(all, parts[i])
			if done(&parts[i], nil) {
				return all
			}
		}
	}
}

func shows(text string) func(*termstream.Part, error) bool {
	return func(p *termstream.Part, _ error) bool { return p != nil && bytes.Contains(p.Output, []byte(text)) }
}

func closedWith(code *int) func(*termstream.Part, error) bool {
	return func(p *termstream.Part, err error) bool {
		var ce *client.CloseError
		if errors.As(err, &ce) {
			*code = ce.Code
			return true
		}
		return false
	}
}

func nextTerm(t *testing.T, f *fakeHerdr) *fakeTerm {
	t.Helper()
	select {
	case ft := <-f.terms:
		return ft
	case <-time.After(5 * time.Second):
		t.Fatal("no terminal connection")
		return nil
	}
}

func nextMsg(t *testing.T, ft *fakeTerm) termMsg {
	t.Helper()
	select {
	case m := <-ft.msgs:
		return m
	case <-time.After(5 * time.Second):
		t.Fatal("no message on the terminal connection")
		return termMsg{}
	}
}

func cells(cols, rows uint16) term.Size {
	return term.Size{Cols: cols, Rows: rows, CellWidthPx: defaultCellW, CellHeightPx: defaultCellH}
}
