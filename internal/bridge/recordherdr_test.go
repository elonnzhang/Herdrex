package bridge

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/elonnzhang/herdrex/pkg/rex/client"
)

// herdrCall is one request the bridge sent to the fake Herdr.
type herdrCall struct {
	Method string         `json:"method"`
	Params map[string]any `json:"params"`
}

// One workspace w1 with one tab w1:t1 holding one pane w1:p1.
const (
	oneSnapshot = `{"type":"session_snapshot","snapshot":{"workspaces":[{"workspace_id":"w1","active_tab_id":"w1:t1"}],"tabs":[{"workspace_id":"w1","tab_id":"w1:t1"}],"panes":[{"workspace_id":"w1","tab_id":"w1:t1","pane_id":"w1:p1"}]}}`
	oneLayout   = `{"type":"layout_export","layout":{"focused_pane_id":"w1:p1","root":{"type":"pane","pane_id":"w1:p1"}}}`
)

// recordHerdr serves Herdr's JSON API on a Unix socket and records every
// request. answer gives the result for a method; a method it leaves empty
// answers the one-pane snapshot and layout, or {}.
type recordHerdr struct {
	Socket string
	calls  chan herdrCall
}

func newRecordHerdr(t *testing.T, answer func(herdrCall) string) *recordHerdr {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "hxf") // Unix socket paths are short on macOS
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	f := &recordHerdr{Socket: filepath.Join(dir, "api.sock"), calls: make(chan herdrCall, 1024)}
	ln, err := net.Listen("unix", f.Socket)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			peer, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer peer.Close()
				var c herdrCall
				if json.NewDecoder(peer).Decode(&c) != nil {
					return
				}
				f.calls <- c
				result := ""
				if answer != nil {
					result = answer(c)
				}
				if result == "" {
					switch c.Method {
					case "session.snapshot":
						result = oneSnapshot
					case "layout.export":
						result = oneLayout
					default:
						result = `{}`
					}
				}
				fmt.Fprintf(peer, "{\"result\":%s}\n", result)
			}()
		}
	}()
	return f
}

// connect starts a bridge on the fake Herdr and a Rex client on it.
func (f *recordHerdr) connect(t *testing.T) (*Bridge, *client.Conn) {
	t.Helper()
	b := New(f.Socket)
	conn, _ := rexTestClient(t, b)
	return b, conn
}

// inputs returns the calls received within wait, leaving out the reads that
// carry no input. The fake records a call before it answers, so the calls a
// finished Rex request made are all there with a wait of 0.
func (f *recordHerdr) inputs(wait time.Duration) []herdrCall {
	var out []herdrCall
	add := func(c herdrCall) {
		switch c.Method {
		case "session.snapshot", "layout.export", "pane.get":
		default:
			out = append(out, c)
		}
	}
	deadline := time.After(wait)
	for {
		select {
		case c := <-f.calls:
			add(c)
			continue
		default:
		}
		select {
		case c := <-f.calls:
			add(c)
		case <-deadline:
			return out
		}
	}
}
