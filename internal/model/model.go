// Package model maps Herdr state onto Rex's session/window/block view
// (the types of package rex).
//
//	Herdr workspace -> Rex session
//	Herdr tab       -> Rex window
//	Herdr pane      -> Rex block
package model

import (
	_ "embed"
	"encoding/json"
	"sync"

	"github.com/google/uuid"

	"github.com/elonnzhang/herdrex/pkg/herdr/jsonapi"
	"github.com/elonnzhang/herdrex/pkg/rex"
)

var ns = uuid.MustParse("6f1c2f58-8f0e-4c0e-9d57-2d3f1b7a0001")

var HostID = uuid.NewSHA1(ns, []byte("host")).String()

// terminal_creator.json is the real Rex server's block_creator.list entry for
// the terminal creator, cut down to the methods and events the bridge implements.
//
//go:embed terminal_creator.json
var terminalCreator []byte

// CreatorInfo returns the block_creator.list entry for Herdr panes.
func CreatorInfo() map[string]any {
	var m map[string]any
	if err := json.Unmarshal(terminalCreator, &m); err != nil {
		panic(err)
	}
	m["host_id"] = HostID
	return m
}

// IDs gives each Herdr id a stable Rex id and remembers the reverse.
type IDs struct {
	mu  sync.RWMutex
	rev map[string]string
}

func NewIDs() *IDs { return &IDs{rev: map[string]string{}} }

func (i *IDs) Rex(kind, herdrID string) string {
	id := kind + ":" + uuid.NewSHA1(ns, []byte(kind+"/"+herdrID)).String()
	i.mu.Lock()
	i.rev[id] = herdrID
	i.mu.Unlock()
	return id
}

func (i *IDs) Herdr(rexID string) (string, bool) {
	i.mu.RLock()
	defer i.mu.RUnlock()
	h, ok := i.rev[rexID]
	return h, ok
}

func PaneLabel(p jsonapi.Pane) string {
	if p.Label != "" {
		return p.Label
	}
	if p.TerminalTitle != "" {
		return p.TerminalTitle
	}
	return p.ID
}

func (i *IDs) Block(p jsonapi.Pane) rex.Block {
	return rex.Block{
		BlockID:     i.Rex("block", p.ID),
		Label:       PaneLabel(p),
		CreatorName: rex.TerminalCreator,
		Flavor:      rex.ShellFlavor,
		HostID:      HostID,
	}
}

// Float is a floating layer kept by the bridge. Herdr has no floating
// panes: the pane stays in its tab's Herdr layout, and the Rex view leaves
// it out of the tiled layout and shows it in front, at Bounds.
type Float struct {
	LayerID string
	PaneID  string
	Bounds  [4]float64
}

// hidden reports whether every pane of the subtree floats.
func hidden(n *jsonapi.LayoutNode, floating map[string]bool) bool {
	if n.Type == "pane" {
		return floating[n.PaneID]
	}
	return hidden(n.First, floating) && hidden(n.Second, floating)
}

// node converts a Herdr layout subtree. A split with a floating side gives
// its whole rect to the other side. Split IDs come from the Herdr path, so
// they stay the same while panes float.
func (i *IDs) node(n *jsonapi.LayoutNode, r [4]float64, path string, floating map[string]bool) *rex.Node {
	if hidden(n, floating) {
		return nil
	}
	if n.Type == "pane" {
		return &rex.Node{Rect: r, BlockID: i.Rex("block", n.PaneID)}
	}
	if hidden(n.First, floating) {
		return i.node(n.Second, r, path+"1", floating)
	}
	if hidden(n.Second, floating) {
		return i.node(n.First, r, path+"0", floating)
	}
	x, y, w, h := r[0], r[1], r[2], r[3]
	dir := "vertical"
	r1, r2 := [4]float64{x, y, w, h * n.Ratio}, [4]float64{x, y + h*n.Ratio, w, h * (1 - n.Ratio)}
	if n.Direction == "right" {
		dir = "horizontal"
		r1, r2 = [4]float64{x, y, w * n.Ratio, h}, [4]float64{x + w*n.Ratio, y, w * (1 - n.Ratio), h}
	}
	return &rex.Node{Rect: r, Split: &rex.Split{
		SplitID:   i.Rex("split", path),
		Direction: dir,
		Ratio:     n.Ratio,
		Before:    i.node(n.First, r1, path+"0", floating),
		After:     i.node(n.Second, r2, path+"1", floating),
	}}
}

// SplitID is the Rex id of the split at a Herdr layout path: the tab id
// followed by 0 (first) or 1 (second) for each step from the root.
func (i *IDs) SplitID(tab string, path []bool) string {
	s := tab
	for _, second := range path {
		s += map[bool]string{false: "0", true: "1"}[second]
	}
	return i.Rex("split", s)
}

// Window builds a Rex window from a Herdr tab. floats are the tab's
// floating layers, back to front.
func (i *IDs) Window(t jsonapi.Tab, panes []jsonapi.Pane, l *jsonapi.LayoutNode, floats []Float) rex.Window {
	w := rex.Window{WindowID: i.Rex("window", t.ID), Label: t.Label}
	floating := map[string]bool{}
	for _, f := range floats {
		floating[f.PaneID] = true
	}
	blocks := []rex.Block{}
	byPane := map[string]rex.Block{}
	for _, p := range panes {
		if p.TabID != t.ID {
			continue
		}
		byPane[p.ID] = i.Block(p)
		if !floating[p.ID] {
			blocks = append(blocks, i.Block(p))
		}
	}
	w.Layers = []rex.Layer{{
		LayerID: i.Rex("layer", t.ID),
		Kind:    "tiled",
		Bounds:  [4]float64{0, 0, 1, 1},
		Layout:  i.node(l, [4]float64{0, 0, 1, 1}, t.ID, floating),
		Blocks:  blocks,
	}}
	for _, f := range floats {
		blk, ok := byPane[f.PaneID]
		if !ok {
			continue
		}
		w.Layers = append(w.Layers, rex.Layer{
			LayerID: f.LayerID,
			Kind:    "floating",
			Bounds:  f.Bounds,
			Layout:  &rex.Node{Rect: f.Bounds, BlockID: blk.BlockID},
			Blocks:  []rex.Block{blk},
		})
	}
	if l.Focused != "" {
		f := i.Rex("block", l.Focused)
		w.FocusedBlockID = &f
		if l.Zoomed {
			w.ZoomedBlockID = &f
		}
	}
	return w
}
