package bridge

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/elonnzhang/herdrex/pkg/herdr"
	"github.com/elonnzhang/herdrex/pkg/herdr/jsonapi"
)

// fakeHerdr serves the parts of Herdr's JSON API and direct terminal
// protocol that the bridge uses, from state the test edits.
type fakeHerdr struct {
	t   *testing.T
	api string // the API socket; the client socket sits beside it

	mu       sync.Mutex
	st       fakeState
	terms    chan *fakeTerm  // each terminal connection once it picked a pane
	scrollCh []chan struct{} // open pane.scroll_changed subscriptions
	readHold chan struct{}   // when set, the next pane.read signals it once answered, then waits on it
}

type fakeState struct {
	snap        jsonapi.Snapshot
	layouts     map[string]*jsonapi.LayoutNode // tab -> split tree; missing is layout_not_found
	layoutErr   string                         // when set, every layout.export fails with this code
	firstFrame  map[string]time.Duration       // pane -> wait before the first frame
	detachDelay map[string]time.Duration       // pane -> wait before answering Detach
	controlled  map[string]int                 // pane -> open controlling connections
	external    map[string]bool                // panes another Herdr client controls
	splits      []map[string]any               // the params of every pane.split
	screens     map[string]*fakeScreen         // pane -> rows that pane.get and pane.read report
	reads       int                            // pane.read calls answered from screens
	scrolls     int                            // pane.scroll calls for panes with screens
}

func newFakeHerdr(t *testing.T) *fakeHerdr {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "hxf") // Unix socket paths are short on macOS
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	f := &fakeHerdr{t: t, api: filepath.Join(dir, "herdr.sock"), terms: make(chan *fakeTerm, 16)}
	f.st = fakeState{
		layouts:     map[string]*jsonapi.LayoutNode{},
		firstFrame:  map[string]time.Duration{},
		detachDelay: map[string]time.Duration{},
		controlled:  map[string]int{},
		external:    map[string]bool{},
		screens:     map[string]*fakeScreen{},
	}
	f.serve(f.api, f.answer)
	f.serve(herdr.ClientSocket(f.api), f.terminal)
	return f
}

func (f *fakeHerdr) serve(path string, handle func(net.Conn)) {
	ln, err := net.Listen("unix", path)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go handle(c)
		}
	}()
}

func (f *fakeHerdr) edit(change func(*fakeState)) {
	f.mu.Lock()
	defer f.mu.Unlock()
	change(&f.st)
}

// addWorkspace adds a workspace whose first tab is active.
func (s *fakeState) addWorkspace(id, label string) {
	s.snap.Workspaces = append(s.snap.Workspaces, jsonapi.Workspace{ID: id, Label: label})
}

// addTab adds a tab whose panes sit side by side, each 80x24.
func (s *fakeState) addTab(ws, tab string, panes ...string) {
	s.snap.Tabs = append(s.snap.Tabs, jsonapi.Tab{ID: tab, WorkspaceID: ws, Label: tab})
	for i := range s.snap.Workspaces {
		if w := &s.snap.Workspaces[i]; w.ID == ws && w.ActiveTabID == "" {
			w.ActiveTabID = tab
		}
	}
	l := jsonapi.TabLayout{TabID: tab, WorkspaceID: ws, FocusedPaneID: panes[0]}
	for i, p := range panes {
		s.snap.Panes = append(s.snap.Panes, jsonapi.Pane{ID: p, TabID: tab, WorkspaceID: ws})
		l.Panes = append(l.Panes, jsonapi.LayoutPane{PaneID: p, Rect: jsonapi.Rect{X: 80 * i, Width: 80, Height: 24}})
	}
	s.snap.Layouts = append(s.snap.Layouts, l)
	s.layouts[tab] = row(panes...)
	s.layouts[tab].Focused = panes[0]
}

// row is a layout of panes side by side.
func row(panes ...string) *jsonapi.LayoutNode {
	n := &jsonapi.LayoutNode{Type: "pane", PaneID: panes[len(panes)-1]}
	for _, p := range slices.Backward(panes[:len(panes)-1]) {
		n = &jsonapi.LayoutNode{Type: "split", Direction: "right", Ratio: 0.5,
			First: &jsonapi.LayoutNode{Type: "pane", PaneID: p}, Second: n}
	}
	return n
}

// answer serves one JSON API request.
func (f *fakeHerdr) answer(c net.Conn) {
	defer c.Close()
	line, err := bufio.NewReader(c).ReadBytes('\n')
	if err != nil {
		return
	}
	var req struct {
		ID     string         `json:"id"`
		Method string         `json:"method"`
		Params map[string]any `json:"params"`
	}
	if json.Unmarshal(line, &req) != nil {
		return
	}
	if req.Method == "events.subscribe" && f.subscribeScroll(c, req.ID, req.Params) {
		return
	}
	result, code := f.result(req.Method, req.Params)
	if req.Method == "pane.read" {
		f.mu.Lock()
		hold := f.readHold
		f.readHold = nil
		f.mu.Unlock()
		if hold != nil {
			hold <- struct{}{}
			<-hold
		}
	}
	reply := map[string]any{"id": req.ID, "result": result}
	if code != "" {
		reply = map[string]any{"id": req.ID, "error": map[string]any{"code": code, "message": req.Method + " failed"}}
	}
	out, _ := json.Marshal(reply)
	c.Write(append(out, '\n'))
}

func (f *fakeHerdr) result(method string, p map[string]any) (any, string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	s := &f.st
	if r, ok := s.screenResult(method, p); ok {
		return r, ""
	}
	switch method {
	case "ping":
		return map[string]any{"type": "pong", "version": "0.9.5", "protocol": 22}, ""
	case "session.snapshot":
		return map[string]any{"type": "session_snapshot", "snapshot": s.snap}, ""
	case "layout.export":
		tab, _ := p["tab_id"].(string)
		l := s.layouts[tab]
		if s.layoutErr != "" {
			return nil, s.layoutErr
		}
		if l == nil {
			return nil, "layout_not_found"
		}
		return map[string]any{"type": "layout_export", "layout": map[string]any{
			"root": l, "focused_pane_id": l.Focused, "zoomed": l.Zoomed}}, ""
	case "pane.get":
		id, _ := p["pane_id"].(string)
		for _, x := range s.snap.Panes {
			if x.ID == id {
				return map[string]any{"type": "pane_info", "pane": x}, ""
			}
		}
		return nil, "pane_not_found"
	case "pane.split":
		target, _ := p["target_pane_id"].(string)
		for _, x := range s.snap.Panes {
			if x.ID != target {
				continue
			}
			s.splits = append(s.splits, p)
			n := jsonapi.Pane{ID: fmt.Sprintf("%s:p%d", x.WorkspaceID, len(s.snap.Panes)+1), TabID: x.TabID, WorkspaceID: x.WorkspaceID}
			if cwd, _ := p["cwd"].(string); cwd != "" {
				n.CWD = cwd
			}
			s.snap.Panes = append(s.snap.Panes, n)
			for i := range s.snap.Layouts {
				if l := &s.snap.Layouts[i]; l.TabID == x.TabID {
					l.Panes = append(l.Panes, jsonapi.LayoutPane{PaneID: n.ID, Rect: jsonapi.Rect{Width: 80, Height: 24}})
				}
			}
			if l := s.layouts[x.TabID]; l != nil {
				s.layouts[x.TabID] = &jsonapi.LayoutNode{Type: "split", Direction: "right", Ratio: 0.5, Focused: l.Focused,
					First: l, Second: &jsonapi.LayoutNode{Type: "pane", PaneID: n.ID}}
			}
			return map[string]any{"type": "pane_info", "pane": n}, ""
		}
		return nil, "pane_not_found"
	case "workspace.create":
		n := len(s.snap.Workspaces) + 1
		ws := fmt.Sprintf("w%d", n)
		s.addWorkspace(ws, fmt.Sprint(p["label"]))
		s.addTab(ws, ws+":t1", ws+":p1")
		last := len(s.snap.Panes) - 1
		return map[string]any{"type": "workspace_created", "workspace": s.snap.Workspaces[n-1],
			"tab": s.snap.Tabs[len(s.snap.Tabs)-1], "root_pane": s.snap.Panes[last]}, ""
	}
	if strings.HasPrefix(method, "events.") {
		return nil, "unsupported"
	}
	return map[string]any{"type": "ok"}, ""
}
