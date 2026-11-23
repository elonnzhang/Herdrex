package bridge

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/elonnzhang/herdrex/pkg/rex"
	"github.com/elonnzhang/herdrex/pkg/rex/client"
	"github.com/elonnzhang/herdrex/pkg/rex/termstream"
)

// A floating shell is a real /bin/sh in a PTY, behind a bridge over a fake
// Herdr with workspace w1, tab w1:t1 and pane w1:p1.
type floatShell struct {
	f      *fakeHerdr
	b      *Bridge
	conn   *client.Conn
	sid    string
	layer  string
	block  string
	pid    int
	opened []shown // the events new_layer sent
}

func newFloatShell(t *testing.T) *floatShell {
	t.Helper()
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("HOME", t.TempDir()) // no profile of the user's
	t.Setenv("ENV", "")
	f := newFakeHerdr(t)
	f.edit(func(s *fakeState) {
		s.addWorkspace("w1", "one")
		s.addTab("w1", "w1:t1", "w1:p1")
	})
	b, conn := newRexBridge(t, f)
	t.Cleanup(b.Close)
	fs := &floatShell{f: f, b: b, conn: conn, sid: b.IDs.Rex("session", "w1")}
	call(t, conn, "session.attach", P{"session_id": fs.sid}, nil)
	drain(t, conn)
	var r struct {
		LayerID  string   `json:"layer_id"`
		BlockIDs []string `json:"block_ids"`
		Revision int      `json:"revision"`
	}
	call(t, conn, "session.new_layer", P{
		"session_id": fs.sid, "bounds": []float64{0.2, 0.2, 0.6, 0.6},
		"layout": map[string]any{"block": map[string]any{"flavor": rex.ShellFlavor}},
	}, &r)
	if !strings.HasPrefix(r.LayerID, "layer:") || len(r.BlockIDs) != 1 || r.Revision == 0 {
		t.Fatalf("new_layer = %+v", r)
	}
	fs.layer, fs.block = r.LayerID, r.BlockIDs[0]
	fs.opened = drain(t, conn)
	var proc struct {
		Child struct {
			PID  int    `json:"pid"`
			Name string `json:"name"`
		} `json:"child"`
	}
	fs.method(t, "process", nil, &proc)
	if proc.Child.PID <= 0 || proc.Child.Name != "sh" {
		t.Fatalf("process child = %+v, want sh", proc.Child)
	}
	fs.pid = proc.Child.PID
	return fs
}

func (fs *floatShell) method(t *testing.T, m string, args map[string]any, out any) {
	t.Helper()
	if args == nil {
		args = map[string]any{}
	}
	call(t, fs.conn, "block.method", P{"session_id": fs.sid, "block_id": fs.block, "method": m, "args": args}, out)
}

// output joins a stream's program output.
func output(parts []termstream.Part) string {
	var b bytes.Buffer
	for _, p := range parts {
		b.Write(p.Output)
	}
	return b.String()
}

// gone waits for a process to be gone.
func gone(t *testing.T, pid int) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); syscall.Kill(pid, 0) == nil; time.Sleep(20 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatalf("process %d is still running", pid)
		}
	}
}

func TestNewLayerShell(t *testing.T) {
	fs := newFloatShell(t)
	if got, want := names(fs.opened), []string{
		"block_created", "block_owner_changed", "block_event/process_changed",
		"session_view_changed", "block_event/process_changed",
	}; !equal(got, want) {
		t.Errorf("events = %v, want %v", got, want)
	}

	var v rex.View
	call(t, fs.conn, "session.view", P{"session_id": fs.sid}, &v)
	ls := v.Windows[0].Layers
	if len(ls) != 2 || ls[0].Layout == nil || ls[0].Layout.BlockID != fs.b.IDs.Rex("block", "w1:p1") {
		t.Fatalf("layers = %+v, want the tiled pane and the shell's layer", ls)
	}
	fl := ls[1]
	if fl.LayerID != fs.layer || fl.Kind != "floating" || fl.Bounds != [4]float64{0.2, 0.2, 0.6, 0.6} ||
		len(fl.Blocks) != 1 || fl.Blocks[0].BlockID != fs.block || fl.Blocks[0].Owner != fs.conn.Welcome.Client.ID ||
		fl.Blocks[0].Label != "shell" || fl.Blocks[0].CreatorName != rex.TerminalCreator {
		t.Errorf("floating layer = %+v", fl)
	}

	var lb struct {
		Blocks []map[string]any `json:"blocks"`
	}
	call(t, fs.conn, "session.list_blocks", P{"session_id": fs.sid}, &lb)
	if len(lb.Blocks) != 2 || lb.Blocks[1]["block_id"] != fs.block || lb.Blocks[1]["window_id"] != fs.b.IDs.Rex("window", "w1:t1") {
		t.Errorf("list_blocks = %v, want the pane and the shell", lb.Blocks)
	}
	var res struct {
		Results []struct {
			Status string         `json:"status"`
			Target map[string]any `json:"target"`
		} `json:"results"`
	}
	call(t, fs.conn, "target.resolve", P{"kinds": []string{"block"}, "queries": []string{fs.block}}, &res)
	if res.Results[0].Status != "resolved" || res.Results[0].Target["block_id"] != fs.block {
		t.Errorf("target.resolve = %+v", res.Results)
	}

	// The fake Herdr's refreshes keep the layer.
	if err := fs.b.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	call(t, fs.conn, "session.view", P{"session_id": fs.sid}, &v)
	if len(v.Windows[0].Layers) != 2 {
		t.Errorf("after a refresh, layers = %+v", v.Windows[0].Layers)
	}
}

func equal(a, b []string) bool { return strings.Join(a, " ") == strings.Join(b, " ") }

// Input from write, block.event and client.key reaches the shell, and its
// output reaches the stream after a blank snapshot and the size.
func TestShellInputOutput(t *testing.T) {
	fs := newFloatShell(t)
	s := openTerm(t, fs.conn, fs.sid, fs.block)
	first := readUntil(t, s, func(p *termstream.Part, _ error) bool { return p != nil && p.Type == "resize" })
	if len(first) != 2 || !bytes.Equal(first[0].Output, termstream.BlankSnapshot) {
		t.Fatalf("stream starts with %d parts, want the blank snapshot and resize", len(first))
	}
	var size map[string]any
	json.Unmarshal(first[1].Message, &size)
	if size["cols"] != 80.0 || size["rows"] != 24.0 || size["owner"] != fs.conn.Welcome.Client.ID {
		t.Errorf("resize = %s, want 80x24 owned by the creator", first[1].Message)
	}

	fs.method(t, "write", map[string]any{"data": "echo w$((40+2))\r"}, nil)
	readUntil(t, s, shows("w42"))

	for _, c := range "echo k$((1+1))" {
		call(t, fs.conn, "block.event", P{"session_id": fs.sid, "block_id": fs.block, "event": map[string]any{
			"type": "key", "payload": map[string]any{"action": "press", "key": "unidentified", "text": string(c)},
		}}, nil)
	}
	call(t, fs.conn, "client.key", P{"session_id": fs.sid, "block_id": fs.block,
		"key": map[string]any{"action": "press", "key": "enter"}}, nil)
	readUntil(t, s, shows("k2"))
}

// The owner's resize sets the PTY size, which the shell sees.
func TestShellResize(t *testing.T) {
	fs := newFloatShell(t)
	s := openTerm(t, fs.conn, fs.sid, fs.block)
	fs.method(t, "resize", map[string]any{"columns": 100, "rows": 30, "cell_width_px": 9, "cell_height_px": 18}, nil)
	parts := readUntil(t, s, func(p *termstream.Part, _ error) bool {
		return p != nil && p.Type == "resize" && bytes.Contains(p.Message, []byte(`"cols":100`))
	})
	if !bytes.Contains(parts[len(parts)-1].Message, []byte(`"cell_width_px":9`)) {
		t.Errorf("resize = %s, want the client's cell size", parts[len(parts)-1].Message)
	}
	if evs := drain(t, fs.conn); len(evs) == 0 || evs[len(evs)-1].Name != "block_event/size_changed" {
		t.Errorf("events = %v, want size_changed", names(evs))
	}
	var size struct {
		Columns, Rows int
		Owner         string
	}
	fs.method(t, "size", nil, &size)
	if size.Columns != 100 || size.Rows != 30 || size.Owner != fs.conn.Welcome.Client.ID {
		t.Errorf("size = %+v", size)
	}
	fs.method(t, "write", map[string]any{"data": "stty size\r"}, nil)
	readUntil(t, s, shows("30 100"))
}

// A stream that opens late replays the output it missed.
func TestShellLateJoin(t *testing.T) {
	fs := newFloatShell(t)
	s := openTerm(t, fs.conn, fs.sid, fs.block)
	fs.method(t, "write", map[string]any{"data": "echo late-$((3*5))\r"}, nil)
	readUntil(t, s, shows("late-15"))
	other := attached(t, fs.b, fs.sid)
	readUntil(t, openTerm(t, other, fs.sid, fs.block), shows("late-15"))
}

func TestShellCloseLayer(t *testing.T) {
	fs := newFloatShell(t)
	s := openTerm(t, fs.conn, fs.sid, fs.block)
	readUntil(t, s, func(p *termstream.Part, _ error) bool { return p != nil && p.Type == "resize" })
	var r struct{ Revision int }
	call(t, fs.conn, "session.close_layer", P{"session_id": fs.sid, "layer_id": fs.layer}, &r)
	if got := names(drain(t, fs.conn)); !equal(got, []string{"block_closed", "session_view_changed"}) {
		t.Errorf("events = %v, want block_closed, session_view_changed", got)
	}
	code := 0
	parts := readUntil(t, s, closedWith(&code))
	if code != int(closeBlock) {
		t.Errorf("stream closed with %d, want 4000", code)
	}
	for _, p := range parts {
		if p.Type == "child_exited" {
			t.Errorf("a closed block's stream got child_exited")
		}
	}
	gone(t, fs.pid)
	var v rex.View
	call(t, fs.conn, "session.view", P{"session_id": fs.sid}, &v)
	if len(v.Windows[0].Layers) != 1 || v.Revision != r.Revision {
		t.Errorf("view after close_layer: revision %d (result %d), layers %+v", v.Revision, r.Revision, v.Windows[0].Layers)
	}
	var e *rex.Error
	if err := fs.conn.Call(t.Context(), "block.method", P{"session_id": fs.sid, "block_id": fs.block, "method": "size"}, nil); !errors.As(err, &e) || e.Code != "not_found" {
		t.Errorf("size of a closed shell: %v, want not_found", err)
	}
}

func TestShellBlockClose(t *testing.T) {
	fs := newFloatShell(t)
	var r struct {
		BlockID  string `json:"block_id"`
		Revision int
	}
	call(t, fs.conn, "block.close", P{"session_id": fs.sid, "block_id": fs.block}, &r)
	if r.BlockID != fs.block || r.Revision == 0 {
		t.Errorf("block.close = %+v", r)
	}
	gone(t, fs.pid)
}

// A shell that exits closes its block as a pane's program does.
func TestShellExit(t *testing.T) {
	fs := newFloatShell(t)
	s := openTerm(t, fs.conn, fs.sid, fs.block)
	readUntil(t, s, func(p *termstream.Part, _ error) bool { return p != nil && p.Type == "resize" })
	fs.method(t, "write", map[string]any{"data": "exit 3\r"}, nil)
	code := 0
	parts := readUntil(t, s, closedWith(&code))
	if code != int(closeBlock) {
		t.Errorf("stream closed with %d, want 4000", code)
	}
	if last := parts[len(parts)-1]; last.Type != "child_exited" || !bytes.Contains(last.Message, []byte(`"exit_code":3`)) {
		t.Errorf("last part = %q %s, want child_exited with code 3", last.Output, last.Message)
	}
	gone(t, fs.pid)
	var evs []shown
	for deadline := time.Now().Add(5 * time.Second); len(evs) < 3 && time.Now().Before(deadline); time.Sleep(20 * time.Millisecond) {
		evs = append(evs, drain(t, fs.conn)...)
	}
	if got := names(evs); !equal(got, []string{"block_event/child_exited", "block_closed", "session_view_changed"}) {
		t.Errorf("events = %v", got)
	}
	var v rex.View
	call(t, fs.conn, "session.view", P{"session_id": fs.sid}, &v)
	if len(v.Windows[0].Layers) != 1 {
		t.Errorf("layers after exit = %+v", v.Windows[0].Layers)
	}
}

// Closing the tab in Herdr closes its shells; the block closes after the
// view, as a closed tab's panes do.
func TestShellTabClosed(t *testing.T) {
	fs := newFloatShell(t)
	fs.f.edit(func(s *fakeState) {
		s.addTab("w1", "w1:t2", "w1:p2")
		s.snap.Workspaces[0].ActiveTabID = "w1:t2"
		s.snap.Tabs = s.snap.Tabs[1:]
		s.snap.Panes = s.snap.Panes[1:]
		s.snap.Layouts = s.snap.Layouts[1:]
		delete(s.layouts, "w1:t1")
	})
	if err := fs.b.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	got := names(drain(t, fs.conn))
	if len(got) < 2 || got[len(got)-1] != "block_closed" {
		t.Errorf("events = %v, want block_closed last", got)
	}
	gone(t, fs.pid)
}

// Close, as herdrex does when it stops, leaves no shell behind.
func TestShellBridgeClose(t *testing.T) {
	fs := newFloatShell(t)
	fs.b.Close()
	gone(t, fs.pid)
}

func TestNewLayerErrors(t *testing.T) {
	fs := newFloatShell(t)
	shell := map[string]any{"block": map[string]any{"flavor": rex.ShellFlavor}}
	bounds := []float64{0.1, 0.1, 0.5, 0.5}
	for _, tc := range []struct {
		name    string
		payload P
		code    string
		message string
	}{
		{"bounds outside", P{"bounds": []float64{0.5, 0.5, 0.6, 0.4}, "layout": shell}, "bad_request",
			"input: validating: bounds must be a finite normalized rectangle with positive size contained within the unit square"},
		{"bounds empty", P{"bounds": []float64{0, 0, 0, 0.4}, "layout": shell}, "bad_request",
			"input: validating: bounds must be a finite normalized rectangle with positive size contained within the unit square"},
		{"no layout", P{"bounds": bounds}, "bad_request",
			`validating input: validating root: required: missing properties: ["layout"]`},
		{"null layout", P{"bounds": bounds, "layout": nil}, "bad_request", "input: validating: layout is required"},
		{"empty layout", P{"bounds": bounds, "layout": map[string]any{}}, "bad_request",
			"input: validating: layout must contain exactly one of block or split"},
		{"split", P{"bounds": bounds, "layout": map[string]any{"split": map[string]any{"direction": "horizontal", "before": shell, "after": shell}}},
			"bad_request", "herdrex floats one block per layer: layout must be a single block"},
		{"flavor", P{"bounds": bounds, "layout": map[string]any{"block": map[string]any{"flavor": "nope.flavor"}}}, "not_found",
			`block flavor not found: "nope.flavor"`},
		{"window", P{"bounds": bounds, "layout": shell, "window_id": fs.b.IDs.Rex("session", "w1")}, "not_found", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tc.payload["session_id"] = fs.sid
			var e *rex.Error
			err := fs.conn.Call(t.Context(), "session.new_layer", tc.payload, nil)
			if !errors.As(err, &e) || e.Code != tc.code || tc.message != "" && e.Message != tc.message {
				t.Errorf("error = %v, want %s %q", err, tc.code, tc.message)
			}
		})
	}
}

// focus gives the shell its window's focus, and keys with no block go to it.
func TestNewLayerFocus(t *testing.T) {
	fs := newFloatShell(t)
	var r struct {
		BlockIDs []string `json:"block_ids"`
	}
	call(t, fs.conn, "session.new_layer", P{
		"session_id": fs.sid, "bounds": []float64{0.1, 0.1, 0.5, 0.5}, "focus": true,
		"layout": map[string]any{"block": map[string]any{"flavor": rex.ShellFlavor}},
	}, &r)
	var v rex.View
	call(t, fs.conn, "session.view", P{"session_id": fs.sid}, &v)
	if f := v.Windows[0].FocusedBlockID; f == nil || *f != r.BlockIDs[0] {
		t.Fatalf("focused block = %v, want the new shell %s", f, r.BlockIDs[0])
	}
	s := openTerm(t, fs.conn, fs.sid, r.BlockIDs[0])
	for _, c := range "echo f$((2+5))" {
		call(t, fs.conn, "client.key", P{"session_id": fs.sid, "key": map[string]any{"action": "press", "key": "unidentified", "text": string(c)}}, nil)
	}
	call(t, fs.conn, "client.key", P{"session_id": fs.sid, "key": map[string]any{"action": "press", "key": "enter"}}, nil)
	readUntil(t, s, shows("f7"))

	call(t, fs.conn, "session.focus_block", P{"session_id": fs.sid, "block_id": fs.b.IDs.Rex("block", "w1:p1")}, nil)
	call(t, fs.conn, "session.view", P{"session_id": fs.sid}, &v)
	if f := v.Windows[0].FocusedBlockID; f == nil || *f != fs.b.IDs.Rex("block", "w1:p1") {
		t.Errorf("focused block after focus_block = %v, want the pane", f)
	}
}
