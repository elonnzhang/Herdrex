package e2e

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/elonnzhang/herdrex/pkg/rex/client"
)

// A client attaching after a burst of output must receive rows that no longer
// fit on the screen. It cannot reconstruct those rows from a screen repaint.
func TestShellStreamIncludesHistory(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("ZDOTDIR", t.TempDir())
	e := start(t)
	var session struct {
		ID      string `json:"session_id"`
		Windows []struct {
			Blocks []string `json:"block_ids"`
		} `json:"initial_windows"`
	}
	e.rex(t, &session, "new", "history", "--json")
	block := session.Windows[0].Blocks[0]
	e.rex(t, nil, "send", "-s", session.ID, "-b", block, `seq 1 240 | while read n; do printf 'HXROW%04d\n' "$n"; done`)
	e.rex(t, nil, "send-key", "-s", session.ID, "-b", block, "Enter")
	eventually(t, func() error {
		var screen struct{ Content string }
		e.rex(t, &screen, "block", "call", "-s", session.ID, "-b", block, "format", `{"format":"text","scrollback":false}`)
		if !strings.Contains(screen.Content, "HXROW0240") || strings.Contains(screen.Content, "HXROW0001") {
			return fmt.Errorf("waiting for row 240 on screen and row 1 off screen, got %q", screen.Content)
		}
		return nil
	})
	c, err := client.Dial(t.Context(), "http://"+e.addr, &client.Options{Kind: "app", Name: "history-test"})
	must(t, err)
	defer c.Close()
	must(t, c.Call(t.Context(), "session.attach", map[string]string{"session_id": session.ID}, nil))
	st, err := c.Terminal(t.Context(), session.ID, block)
	must(t, err)
	defer st.Close()
	ctx, cancel := context.WithTimeout(t.Context(), wait)
	defer cancel()
	var output bytes.Buffer
	for !bytes.Contains(output.Bytes(), []byte("HXROW0001")) || !bytes.Contains(output.Bytes(), []byte("HXROW0100")) {
		parts, err := st.Read(ctx)
		if err != nil {
			t.Fatalf("late join: received %d bytes, history rows 1 and 100 missing: %v", output.Len(), err)
		}
		for _, p := range parts {
			output.Write(p.Output)
		}
	}
	if bytes.Contains(output.Bytes(), []byte("\x1b[?1002h")) {
		t.Fatal("plain shell took ownership of drag selection")
	}
}
