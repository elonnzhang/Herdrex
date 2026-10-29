package jsonapi

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"
)

// The live tests run against the Herdr server whose API socket is
// $HERDR_TEST_SOCKET. Each test that changes anything works in a workspace it
// creates and closes, but use an isolated server all the same.
func liveClient(t *testing.T) (*Client, context.Context) {
	t.Helper()
	sock := os.Getenv("HERDR_TEST_SOCKET")
	if sock == "" {
		t.Skip("HERDR_TEST_SOCKET is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	t.Cleanup(cancel)
	return &Client{Socket: sock}, ctx
}

// created is the workspace.create result.
type created struct {
	Workspace Workspace `json:"workspace"`
	Tab       Tab       `json:"tab"`
	Root      Pane      `json:"root_pane"`
}

// liveWorkspace creates a workspace for this test and closes it when the test
// ends, so the test never changes one it didn't make.
func liveWorkspace(t *testing.T, c *Client, ctx context.Context) created {
	t.Helper()
	var r created
	if err := c.Call(ctx, "workspace.create", map[string]any{"label": "jsonapi-" + t.Name()}, &r); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := c.Call(cctx, "workspace.close", map[string]any{"workspace_id": r.Workspace.ID}, nil); err != nil {
			t.Errorf("workspace.close %s: %v", r.Workspace.ID, err)
		}
	})
	return r
}

func TestLivePing(t *testing.T) {
	c, ctx := liveClient(t)
	p, err := c.Ping(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if p.Protocol != 22 {
		t.Errorf("protocol %d, want 22", p.Protocol)
	}
	g := p.Capabilities.EndpointProtocolGeneration
	if g == nil {
		t.Fatal("no endpoint generation, want 1")
	}
	if *g != 1 {
		t.Errorf("endpoint generation %d, want 1", *g)
	}
	caps := p.Capabilities
	caps.EndpointProtocolGeneration = nil
	t.Logf("herdr %s, protocol %d, endpoint generation %d, %+v", p.Version, p.Protocol, *g, caps)
}

// TestLiveRequestLimit checks MaxRequest against Herdr: a line of MaxRequest
// bytes gets a reply, one byte more gets none.
func TestLiveRequestLimit(t *testing.T) {
	c, ctx := liveClient(t)
	fresh := &Client{Socket: c.Socket} // padTo assumes the first request's id
	if err := fresh.Call(ctx, "ping", padTo(t, MaxRequest), nil); err != nil {
		t.Fatalf("request of MaxRequest bytes: %v", err)
	}
	if err := fresh.Call(ctx, "ping", padTo(t, MaxRequest+1), nil); err == nil {
		t.Fatal("Call sent a request over MaxRequest")
	}

	conn, err := net.Dial("unix", c.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	line, err := marshal(map[string]any{"id": "b1", "method": "ping", "params": padTo(t, MaxRequest+1)})
	if err != nil || len(line) != MaxRequest+1 {
		t.Fatalf("built a line of %d bytes: %v", len(line), err)
	}
	conn.SetDeadline(time.Now().Add(10 * time.Second))
	conn.Write(append(line, '\n')) // Herdr may close before taking it all
	if b, err := io.ReadAll(conn); len(b) > 0 || err != nil {
		t.Errorf("Herdr answered a %d byte line: %q, %v", len(line), b, err)
	}
}

func TestLiveSnapshot(t *testing.T) {
	c, ctx := liveClient(t)
	ws := liveWorkspace(t, c, ctx)
	s, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var w, tab, pane, layout bool
	for _, x := range s.Workspaces {
		w = w || x.ID == ws.Workspace.ID && x.ActiveTabID == ws.Tab.ID
	}
	for _, x := range s.Tabs {
		tab = tab || x.ID == ws.Tab.ID && x.WorkspaceID == ws.Workspace.ID
	}
	for _, x := range s.Panes {
		pane = pane || x.ID == ws.Root.ID && x.TabID == ws.Tab.ID
	}
	for _, x := range s.Layouts {
		layout = layout || x.TabID == ws.Tab.ID && len(x.Panes) == 1 && x.Panes[0].PaneID == ws.Root.ID
	}
	if !w || !tab || !pane || !layout {
		t.Errorf("snapshot lacks the new workspace %+v (workspace %t, tab %t, pane %t, layout %t)",
			ws, w, tab, pane, layout)
	}
	t.Logf("%d workspaces, %d tabs, %d panes", len(s.Workspaces), len(s.Tabs), len(s.Panes))
}

func TestLiveTabCreated(t *testing.T) {
	c, ctx := liveClient(t)
	ws := liveWorkspace(t, c, ctx)
	sctx, stop := context.WithCancel(ctx)
	defer stop()
	events, failure, err := c.Subscribe(sctx, []string{"tab.created"})
	if err != nil {
		t.Fatal(err)
	}
	var made struct {
		Tab  Tab  `json:"tab"`
		Root Pane `json:"root_pane"`
	}
	params := map[string]any{"workspace_id": ws.Workspace.ID, "label": "jsonapi-live"}
	if err := c.Call(ctx, "tab.create", params, &made); err != nil {
		t.Fatal(err)
	}

	timeout := time.After(5 * time.Second)
	for got := false; !got; {
		select {
		case e, ok := <-events:
			if !ok {
				t.Fatalf("subscription ended: %v", *failure)
			}
			var d struct {
				Tab Tab `json:"tab"`
			}
			if err := json.Unmarshal(e.Data, &d); err != nil {
				t.Fatalf("event %s %s: %v", e.Name, e.Data, err)
			}
			got = e.Name == "tab_created" && d.Tab.ID == made.Tab.ID
		case <-timeout:
			t.Fatalf("no tab_created event for %s within 5 s", made.Tab.ID)
		}
	}

	// The new tab holds one pane, its root pane.
	l, err := c.Layout(ctx, made.Tab.ID)
	if err != nil {
		t.Fatal(err)
	}
	if l.Type != "pane" || l.PaneID != made.Root.ID || l.Focused != made.Root.ID {
		t.Errorf("layout %+v, want the single pane %s", l, made.Root.ID)
	}

	if err := c.Call(ctx, "tab.close", map[string]any{"tab_id": made.Tab.ID}, nil); err != nil {
		t.Fatalf("tab.close: %v", err)
	}
	s, err := c.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, tab := range s.Tabs {
		if tab.ID == made.Tab.ID {
			t.Errorf("tab %s still open after tab.close", tab.ID)
		}
	}
}

// TestLivePaneRead types into the workspace's pane, then reads it back. The
// command carries <, > and &, which the client sends unescaped.
func TestLivePaneRead(t *testing.T) {
	c, ctx := liveClient(t)
	pane := liveWorkspace(t, c, ctx).Root.ID
	input := map[string]any{"pane_id": pane, "text": "echo '<x&y>'; seq 1 300", "keys": []string{"Enter"}}
	if err := c.Call(ctx, "pane.send_input", input, nil); err != nil {
		t.Fatal(err)
	}
	all := PaneReadParams{PaneID: pane, Source: "recent", Lines: 1000}
	var r *PaneReadResult
	for deadline := time.Now().Add(10 * time.Second); ; {
		var err error
		if r, err = c.PaneRead(ctx, all); err != nil {
			t.Fatal(err)
		}
		if strings.Contains(r.Text, "\n300\n") {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no seq output within 10 s:\n%s", r.Text)
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !strings.Contains(r.Text, "\n<x&y>\n") {
		t.Errorf("no <x&y> line in the output:\n%s", r.Text)
	}
	if !strings.Contains(r.Text, "\n1\n") || r.Truncated {
		t.Errorf("1000 rows: truncated %t, has the first line %t", r.Truncated, strings.Contains(r.Text, "\n1\n"))
	}
	if r.PaneID != pane || r.Source != "recent" || r.Format != "text" {
		t.Errorf("result %s %s %s, want %s recent text", r.PaneID, r.Source, r.Format, pane)
	}

	r, err := c.PaneRead(ctx, PaneReadParams{PaneID: pane, Source: "recent", Lines: 10})
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(strings.TrimSuffix(r.Text, "\n"), "\n") + 1; n > 10 || !r.Truncated {
		t.Errorf("10 rows: got %d lines, truncated %t", n, r.Truncated)
	}

	// ReadPane keeps Herdr's default of 80 rows, which misses the first line.
	text, err := c.ReadPane(ctx, pane, "recent", "text")
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(text, "\n"); n > 80 || strings.Contains(text, "\n1\n") {
		t.Errorf("ReadPane recent: %d lines, has the first line %t", n, strings.Contains(text, "\n1\n"))
	}
}
