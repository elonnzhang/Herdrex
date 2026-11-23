package model

import (
	"fmt"
	"slices"
	"testing"

	"github.com/elonnzhang/herdrex/pkg/herdr/jsonapi"
	"github.com/elonnzhang/herdrex/pkg/rex"
)

// Each Herdr server is a Rex host of its own: the same Herdr ids on two
// servers give different Rex ids, and the same server always gives the same.
func TestIDsPerHost(t *testing.T) {
	a, again, b := NewIDs("/tmp/a/herdr.sock"), NewIDs("/tmp/a/herdr.sock"), NewIDs("/tmp/b/herdr.sock")
	if a.HostID == b.HostID {
		t.Errorf("host ids are equal: %s", a.HostID)
	}
	for _, kind := range []string{"session", "window", "block"} {
		if x, y := a.Rex(kind, "w1"), b.Rex(kind, "w1"); x == y {
			t.Errorf("%s ids are equal across hosts: %s", kind, x)
		}
		if x, y := a.Rex(kind, "w1"), again.Rex(kind, "w1"); x != y || a.HostID != again.HostID {
			t.Errorf("%s ids changed for the same host: %s, %s", kind, x, y)
		}
	}
	if got := a.CreatorInfo()["host_id"]; got != a.HostID {
		t.Errorf("creator host_id %v, want %s", got, a.HostID)
	}
}

// p1 | (p2 / p3), with p2 floating: the tiled layer keeps the root split
// and its ID, p3 takes the right half, and p2 gets a layer of its own.
func TestWindowFloat(t *testing.T) {
	ids := NewIDs("/tmp/herdr.sock")
	leaf := func(p string) *jsonapi.LayoutNode { return &jsonapi.LayoutNode{Type: "pane", PaneID: p} }
	root := &jsonapi.LayoutNode{
		Type:      "split",
		Direction: "right",
		Ratio:     0.5,
		First:     leaf("p1"),
		Second:    &jsonapi.LayoutNode{Type: "split", Direction: "down", Ratio: 0.5, First: leaf("p2"), Second: leaf("p3")},
	}
	tab := jsonapi.Tab{ID: "t1"}
	panes := []jsonapi.Pane{{ID: "p1", TabID: "t1"}, {ID: "p2", TabID: "t1"}, {ID: "p3", TabID: "t1"}}

	plain := ids.Window(tab, panes, root, nil)
	w := ids.Window(tab, panes, root, []Float{{LayerID: "layer:f", PaneID: "p2", Bounds: [4]float64{0.1, 0.2, 0.5, 0.4}}})

	if len(w.Layers) != 2 {
		t.Fatalf("layers = %d, want 2", len(w.Layers))
	}
	tiled, fl := w.Layers[0], w.Layers[1]
	if tiled.Layout.Split == nil || tiled.Layout.Split.SplitID != plain.Layers[0].Layout.Split.SplitID {
		t.Fatalf("root split changed: %+v", tiled.Layout)
	}
	right := tiled.Layout.Split.After
	if right.BlockID != ids.Rex("block", "p3") || right.Rect != [4]float64{0.5, 0, 0.5, 1} {
		t.Errorf("right side = %+v, want p3 filling the right half", right)
	}
	if len(tiled.Blocks) != 2 {
		t.Errorf("tiled blocks = %d, want 2", len(tiled.Blocks))
	}
	if fl.Kind != "floating" || fl.LayerID != "layer:f" || fl.Layout.BlockID != ids.Rex("block", "p2") ||
		fl.Layout.Rect != fl.Bounds || len(fl.Blocks) != 1 {
		t.Errorf("floating layer = %+v", fl)
	}
}

// Floating every pane leaves the tiled layer with no layout and no blocks.
func TestWindowFloatAll(t *testing.T) {
	ids := NewIDs("/tmp/herdr.sock")
	root := &jsonapi.LayoutNode{Type: "pane", PaneID: "p1"}
	w := ids.Window(jsonapi.Tab{ID: "t1"}, []jsonapi.Pane{{ID: "p1", TabID: "t1"}}, root,
		[]Float{{LayerID: "layer:f", PaneID: "p1", Bounds: [4]float64{0, 0, 1, 1}}})
	if w.Layers[0].Layout != nil || len(w.Layers[0].Blocks) != 0 || w.Layers[0].Blocks == nil {
		t.Errorf("tiled layer = %+v, want null layout and empty blocks", w.Layers[0])
	}
}

// A layer with its own block shows that block, which no Herdr pane backs,
// and leaves the tiled layer as it was.
func TestWindowOwnBlock(t *testing.T) {
	ids := NewIDs("/tmp/herdr.sock")
	root := &jsonapi.LayoutNode{Type: "pane", PaneID: "p1"}
	own := rex.Block{BlockID: ids.Rex("block", "shell:x"), Label: "shell", CreatorName: rex.TerminalCreator, Flavor: rex.ShellFlavor, HostID: ids.HostID}
	w := ids.Window(jsonapi.Tab{ID: "t1"}, []jsonapi.Pane{{ID: "p1", TabID: "t1"}}, root,
		[]Float{{LayerID: "layer:s", PaneID: "shell:x", Bounds: [4]float64{0.2, 0.2, 0.6, 0.6}, Block: &own}})
	if len(w.Layers) != 2 || w.Layers[0].Layout == nil || w.Layers[0].Layout.BlockID != ids.Rex("block", "p1") {
		t.Fatalf("layers = %+v, want the tiled p1 and one floating layer", w.Layers)
	}
	fl := w.Layers[1]
	if fl.Kind != "floating" || fl.Layout.BlockID != own.BlockID || len(fl.Blocks) != 1 || fl.Blocks[0] != own {
		t.Errorf("floating layer = %+v, want the shell block", fl)
	}
}

// splits returns the split IDs of a layout tree, by path from the root.
func splits(n *rex.Node, path []bool, out map[string][]bool) {
	if n == nil || n.Split == nil {
		return
	}
	out[n.Split.SplitID] = path
	splits(n.Split.Before, append(slices.Clone(path), false), out)
	splits(n.Split.After, append(slices.Clone(path), true), out)
}

// Every split of a host has its own ID. Herdr numbers tabs in bijective base
// 32, so tab t1's split at path 1 and the root split of tab t11 must differ;
// SplitID names the same split as the view.
func TestSplitIDs(t *testing.T) {
	ids := NewIDs("/tmp/herdr.sock")
	leaf := func(p string) *jsonapi.LayoutNode { return &jsonapi.LayoutNode{Type: "pane", PaneID: p} }
	split := func(a, b *jsonapi.LayoutNode) *jsonapi.LayoutNode {
		return &jsonapi.LayoutNode{Type: "split", Direction: "right", Ratio: 0.5, First: a, Second: b}
	}
	layouts := map[string]*jsonapi.LayoutNode{
		"w1:t1":  split(leaf("w1:p1"), split(leaf("w1:p2"), split(leaf("w1:p3"), leaf("w1:p4")))),
		"w1:t11": split(leaf("w1:p5"), split(leaf("w1:p6"), leaf("w1:p7"))),
	}
	seen := map[string]string{}
	for tab, root := range layouts {
		var panes []jsonapi.Pane
		for _, p := range []string{"w1:p1", "w1:p2", "w1:p3", "w1:p4", "w1:p5", "w1:p6", "w1:p7"} {
			panes = append(panes, jsonapi.Pane{ID: p, TabID: tab})
		}
		w := ids.Window(jsonapi.Tab{ID: tab}, panes, root, nil)
		got := map[string][]bool{}
		splits(w.Layers[0].Layout, nil, got)
		for id, path := range got {
			where := fmt.Sprintf("%s at %v", tab, path)
			if other, ok := seen[id]; ok {
				t.Errorf("split %s at %s and %s", id, other, where)
			}
			seen[id] = where
			if s := ids.SplitID(tab, path); s != id {
				t.Errorf("SplitID(%s, %v) = %s, view has %s", tab, path, s, id)
			}
		}
	}
	if len(seen) != 5 {
		t.Errorf("%d split IDs, want 5", len(seen))
	}
}
