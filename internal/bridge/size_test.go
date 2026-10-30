package bridge

import (
	"bytes"
	"context"
	"testing"
	"time"

	"github.com/elonnzhang/herdrex/pkg/herdr/term"
	rexsvr "github.com/elonnzhang/herdrex/pkg/rex/server"
)

// Every resize call is followed by a full frame from Herdr: the PTY resize
// brings one when the size changes, and a same-size Resize otherwise.
func TestResizeRepaints(t *testing.T) {
	conn, sent := recordingTerminal(t)
	b := New("/tmp/hx-no-herdr.sock")
	const pane, block = "w1:p1", "block:1"
	h := &hub{
		b: b, pane: pane, conn: conn, control: true, done: make(chan struct{}),
		subs: map[*sub]struct{}{}, size: term.Size{Cols: 80, Rows: 24, CellWidthPx: 8, CellHeightPx: 16},
	}
	b.hubs[pane] = h
	a, other := &rexsvr.Client{ID: "client:a"}, &rexsvr.Client{ID: "client:b"}
	resize := func(c *rexsvr.Client, cols, rows float64) []byte {
		t.Helper()
		if _, err := termResize(context.Background(), b, c, "session:1", block, pane,
			P{"columns": cols, "rows": rows, "cell_width_px": 8.0, "cell_height_px": 16.0}); err != nil {
			t.Fatal(err)
		}
		conn.Detach() // marks the end of what the call sent
		var got []byte
		for {
			select {
			case m := <-sent:
				if bytes.Equal(m, []byte{4}) {
					return got
				}
				got = append(got, m...)
			case <-time.After(2 * time.Second):
				t.Fatalf("sent %v, then nothing", got)
			}
		}
	}
	for _, tc := range []struct {
		name       string
		c          *rexsvr.Client
		cols, rows float64
		want       []byte // Resize: cols, rows, cell width and height, pixel mouse
	}{
		{"same size", a, 80, 24, []byte{3, 80, 24, 8, 16, 0}},
		{"new size", a, 100, 30, []byte{3, 100, 30, 8, 16, 0}},
		{"not the owner", other, 120, 40, []byte{3, 100, 30, 8, 16, 0}},
	} {
		if tc.name == "not the owner" {
			b.owners[block] = a.ID
		}
		if got := resize(tc.c, tc.cols, tc.rows); !bytes.Equal(got, tc.want) {
			t.Errorf("%s: sent %v, want %v", tc.name, got, tc.want)
		}
	}
}
