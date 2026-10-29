package e2e

import (
	"fmt"
	"math"
	"slices"
	"testing"

	"github.com/elonnzhang/herdrex/pkg/rex"
)

// Herdr's replies, cut down to what the checks read.

type hRect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type hWorkspace struct {
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
	ActiveTabID string `json:"active_tab_id"`
}

type hTab struct {
	TabID       string `json:"tab_id"`
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
}

type hPane struct {
	PaneID      string `json:"pane_id"`
	TabID       string `json:"tab_id"`
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
}

type hLayout struct {
	TabID         string `json:"tab_id"`
	Area          hRect  `json:"area"`
	FocusedPaneID string `json:"focused_pane_id"`
	Zoomed        bool   `json:"zoomed"`
	Panes         []struct {
		PaneID string `json:"pane_id"`
		Rect   hRect  `json:"rect"`
	} `json:"panes"`
	Splits []struct {
		Direction string  `json:"direction"`
		Ratio     float64 `json:"ratio"`
	} `json:"splits"`
}

func (e *env) workspaces(t *testing.T) []hWorkspace {
	t.Helper()
	var r struct {
		Workspaces []hWorkspace `json:"workspaces"`
	}
	e.h(t, &r, "workspace", "list")
	for _, w := range r.Workspaces {
		e.ids.Rex("session", w.WorkspaceID)
	}
	return r.Workspaces
}

// workspace is the Herdr workspace of a Rex session, or nil.
func (e *env) workspace(t *testing.T, sid string) *hWorkspace {
	t.Helper()
	for _, w := range e.workspaces(t) {
		if e.ids.Rex("session", w.WorkspaceID) == sid {
			return &w
		}
	}
	return nil
}

func (e *env) tabs(t *testing.T, ws string) []hTab {
	t.Helper()
	var r struct {
		Tabs []hTab `json:"tabs"`
	}
	e.h(t, &r, "tab", "list", "--workspace", ws)
	for _, x := range r.Tabs {
		e.ids.Rex("window", x.TabID)
	}
	return r.Tabs
}

func (e *env) panes(t *testing.T, ws string) []hPane {
	t.Helper()
	var r struct {
		Panes []hPane `json:"panes"`
	}
	e.h(t, &r, "pane", "list", "--workspace", ws)
	for _, p := range r.Panes {
		e.ids.Rex("block", p.PaneID)
	}
	return r.Panes
}

// pane is the Herdr pane of a Rex block in ws, or nil.
func (e *env) pane(t *testing.T, ws, block string) *hPane {
	t.Helper()
	for _, p := range e.panes(t, ws) {
		if e.ids.Rex("block", p.PaneID) == block {
			return &p
		}
	}
	return nil
}

// layout is the Herdr layout of a tab of ws.
func (e *env) layout(t *testing.T, ws, tab string) hLayout {
	t.Helper()
	for _, p := range e.panes(t, ws) {
		if p.TabID == tab {
			var r struct {
				Layout hLayout `json:"layout"`
			}
			e.h(t, &r, "pane", "layout", "--pane", p.PaneID)
			return r.Layout
		}
	}
	t.Fatalf("tab %s has no panes", tab)
	return hLayout{}
}

// view is session.view of a Rex session, read through rex.
func (e *env) view(t *testing.T, sid string) rex.View {
	t.Helper()
	var v rex.View
	e.rex(t, &v, "window", "ls", "-s", sid, "--json", "--short=false")
	return v
}

// sessions maps each Rex session to its label, read through rex.
func (e *env) sessions(t *testing.T) map[string]string {
	t.Helper()
	var r struct {
		Sessions []struct {
			SessionID string `json:"session_id"`
			Label     string `json:"label"`
		} `json:"sessions"`
	}
	e.rex(t, &r, "ls", "--json")
	out := map[string]string{}
	for _, s := range r.Sessions {
		out[s.SessionID] = s.Label
	}
	return out
}

// agree reports how a Rex session and its Herdr workspace differ: labels,
// windows and tabs, the active window, every tiled layout, and the blocks of
// panes that have a manual label. It expects no floating layers.
func (e *env) agree(t *testing.T, sid string) error {
	t.Helper()
	ws := e.workspace(t, sid)
	if ws == nil {
		return fmt.Errorf("no Herdr workspace for %s", sid)
	}
	v := e.view(t, sid)
	if v.Label != ws.Label {
		return fmt.Errorf("session label %q, workspace label %q", v.Label, ws.Label)
	}
	windows := map[string]rex.Window{}
	for _, w := range v.Windows {
		windows[w.WindowID] = w
	}
	tabs := e.tabs(t, ws.WorkspaceID)
	if len(tabs) != len(v.Windows) {
		return fmt.Errorf("%d windows, %d tabs", len(v.Windows), len(tabs))
	}
	labels := map[string]string{}
	for _, tab := range tabs {
		w, ok := windows[e.ids.Rex("window", tab.TabID)]
		if !ok {
			return fmt.Errorf("tab %s has no window", tab.TabID)
		}
		if w.Label != tab.Label {
			return fmt.Errorf("window label %q, tab %s label %q", w.Label, tab.TabID, tab.Label)
		}
		if err := e.sameLayout(w, e.layout(t, ws.WorkspaceID, tab.TabID)); err != nil {
			return fmt.Errorf("tab %s: %w", tab.TabID, err)
		}
		for _, b := range w.Layers[0].Blocks {
			labels[b.BlockID] = b.Label
		}
	}
	if v.ActiveWindowID == nil || *v.ActiveWindowID != e.ids.Rex("window", ws.ActiveTabID) {
		return fmt.Errorf("active window %v, active tab %s", deref(v.ActiveWindowID), ws.ActiveTabID)
	}
	for _, p := range e.panes(t, ws.WorkspaceID) {
		if got := labels[e.ids.Rex("block", p.PaneID)]; p.Label != "" && got != p.Label {
			return fmt.Errorf("block label %q, pane %s label %q", got, p.PaneID, p.Label)
		}
	}
	return nil
}

// sameLayout reports how a Rex window's tiled layer differs from the Herdr
// layout of its tab. Rects are compared with Herdr's cell rects, which are
// rounded, so they may differ by a cell and a half.
func (e *env) sameLayout(w rex.Window, l hLayout) error {
	if len(w.Layers) != 1 {
		return fmt.Errorf("%d layers, want only the tiled one", len(w.Layers))
	}
	rects := map[string][4]float64{}
	var splits []string
	var walk func(n *rex.Node)
	walk = func(n *rex.Node) {
		if n == nil {
			return
		}
		if n.Split == nil {
			rects[n.BlockID] = n.Rect
			return
		}
		splits = append(splits, fmt.Sprintf("%s %.4f", n.Split.Direction, n.Split.Ratio))
		walk(n.Split.Before)
		walk(n.Split.After)
	}
	walk(w.Layers[0].Layout)
	var want []string
	for _, s := range l.Splits {
		dir := map[string]string{
			"right": "horizontal",
			"down":  "vertical",
		}[s.Direction]
		want = append(want, fmt.Sprintf("%s %.4f", dir, s.Ratio))
	}
	slices.Sort(splits)
	slices.Sort(want)
	if !slices.Equal(splits, want) {
		return fmt.Errorf("splits %v, Herdr %v", splits, want)
	}
	if len(rects) != len(l.Panes) {
		return fmt.Errorf("%d blocks, %d panes", len(rects), len(l.Panes))
	}
	a := l.Area
	tol := [4]float64{1.5 / a.Width, 1.5 / a.Height, 1.5 / a.Width, 1.5 / a.Height}
	for _, p := range l.Panes {
		got, ok := rects[e.ids.Rex("block", p.PaneID)]
		if !ok {
			return fmt.Errorf("pane %s has no block", p.PaneID)
		}
		r := p.Rect
		want := [4]float64{(r.X - a.X) / a.Width, (r.Y - a.Y) / a.Height, r.Width / a.Width, r.Height / a.Height}
		for i := range got {
			if math.Abs(got[i]-want[i]) > tol[i] {
				return fmt.Errorf("pane %s: rect %.3f, Herdr %.3f", p.PaneID, got, want)
			}
		}
	}
	focused := e.ids.Rex("block", l.FocusedPaneID)
	if deref(w.FocusedBlockID) != focused {
		return fmt.Errorf("focused block %s, Herdr focused pane %s", deref(w.FocusedBlockID), l.FocusedPaneID)
	}
	if zoomed := deref(w.ZoomedBlockID); (zoomed != "") != l.Zoomed || l.Zoomed && zoomed != focused {
		return fmt.Errorf("zoomed block %q, Herdr zoomed %v on %s", zoomed, l.Zoomed, l.FocusedPaneID)
	}
	return nil
}

// rectOf is a pane's rect in Herdr's layout.
func rectOf(l hLayout, pane string) hRect {
	for _, p := range l.Panes {
		if p.PaneID == pane {
			return p.Rect
		}
	}
	return hRect{}
}

// hasSplit reports whether Herdr's layout has a split in dir with ratio.
func hasSplit(l hLayout, dir string, ratio float64) bool {
	for _, s := range l.Splits {
		if s.Direction == dir && math.Abs(s.Ratio-ratio) < 1e-6 {
			return true
		}
	}
	return false
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
