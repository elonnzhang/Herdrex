package model

import (
	"testing"

	"github.com/elonnzhang/herdrex/pkg/herdr/jsonapi"
)

// p1 | (p2 / p3), with p2 floating: the tiled layer keeps the root split
// and its ID, p3 takes the right half, and p2 gets a layer of its own.
func TestWindowFloat(t *testing.T) {
	ids := NewIDs()
	leaf := func(p string) *jsonapi.LayoutNode { return &jsonapi.LayoutNode{Type: "pane", PaneID: p} }
	root := &jsonapi.LayoutNode{Type: "split", Direction: "right", Ratio: 0.5, First: leaf("p1"),
		Second: &jsonapi.LayoutNode{Type: "split", Direction: "down", Ratio: 0.5, First: leaf("p2"), Second: leaf("p3")}}
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
	ids := NewIDs()
	root := &jsonapi.LayoutNode{Type: "pane", PaneID: "p1"}
	w := ids.Window(jsonapi.Tab{ID: "t1"}, []jsonapi.Pane{{ID: "p1", TabID: "t1"}}, root,
		[]Float{{LayerID: "layer:f", PaneID: "p1", Bounds: [4]float64{0, 0, 1, 1}}})
	if w.Layers[0].Layout != nil || len(w.Layers[0].Blocks) != 0 || w.Layers[0].Blocks == nil {
		t.Errorf("tiled layer = %+v, want null layout and empty blocks", w.Layers[0])
	}
}
