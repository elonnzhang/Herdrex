// Package model maps Herdr state onto Rex's session/window/block view.
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

	"github.com/elonnzhang/herdrex/herdr"
)

var ns = uuid.MustParse("6f1c2f58-8f0e-4c0e-9d57-2d3f1b7a0001")

const (
	Creator = "com.superlogical.terminal"
	Flavor  = "com.superlogical.terminal.shell"
)

var HostID = uuid.NewSHA1(ns, []byte("host")).String()

// terminal_creator.json is the real Rex server's block_creator.list entry for
// Creator, cut down to the methods and events the bridge implements.
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

type Block struct {
	BlockID     string `json:"block_id"`
	Label       string `json:"label"`
	CreatorName string `json:"creator_name"`
	Flavor      string `json:"flavor"`
	HostID      string `json:"host_id"`
	Owner       string `json:"owner,omitempty"`
}

type Node struct {
	Rect    [4]float64 `json:"rect"`
	BlockID string     `json:"block_id,omitempty"`
	Split   *Split     `json:"split,omitempty"`
}

type Split struct {
	SplitID   string  `json:"split_id"`
	Direction string  `json:"direction"`
	Ratio     float64 `json:"ratio"`
	Before    *Node   `json:"before"`
	After     *Node   `json:"after"`
}

type Layer struct {
	LayerID string     `json:"layer_id"`
	Kind    string     `json:"kind"`
	Bounds  [4]float64 `json:"bounds"`
	Layout  *Node      `json:"layout"`
	Blocks  []Block    `json:"blocks"`
}

type Window struct {
	WindowID       string  `json:"window_id"`
	Label          string  `json:"label"`
	FocusedBlockID *string `json:"focused_block_id,omitempty"`
	Layers         []Layer `json:"layers"`
	ZoomedBlockID  *string `json:"zoomed_block_id,omitempty"`
}

type View struct {
	Revision       int      `json:"revision"`
	SessionID      string   `json:"session_id"`
	Label          string   `json:"label"`
	ActiveWindowID *string  `json:"active_window_id,omitempty"`
	Windows        []Window `json:"windows"`
}

func PaneLabel(p herdr.Pane) string {
	if p.Label != "" {
		return p.Label
	}
	if p.TerminalTitle != "" {
		return p.TerminalTitle
	}
	return p.ID
}

func (i *IDs) Block(p herdr.Pane) Block {
	return Block{BlockID: i.Rex("block", p.ID), Label: PaneLabel(p),
		CreatorName: Creator, Flavor: Flavor, HostID: HostID}
}

func (i *IDs) node(n *herdr.LayoutNode, r [4]float64, path string) *Node {
	if n.Type == "pane" {
		return &Node{Rect: r, BlockID: i.Rex("block", n.PaneID)}
	}
	x, y, w, h := r[0], r[1], r[2], r[3]
	dir := "vertical"
	r1, r2 := [4]float64{x, y, w, h * n.Ratio}, [4]float64{x, y + h*n.Ratio, w, h * (1 - n.Ratio)}
	if n.Direction == "right" {
		dir = "horizontal"
		r1, r2 = [4]float64{x, y, w * n.Ratio, h}, [4]float64{x + w*n.Ratio, y, w * (1 - n.Ratio), h}
	}
	return &Node{Rect: r, Split: &Split{SplitID: i.Rex("split", path), Direction: dir, Ratio: n.Ratio,
		Before: i.node(n.First, r1, path+"0"), After: i.node(n.Second, r2, path+"1")}}
}

func (i *IDs) Window(t herdr.Tab, panes []herdr.Pane, l *herdr.LayoutNode) Window {
	w := Window{WindowID: i.Rex("window", t.ID), Label: t.Label}
	var blocks []Block
	for _, p := range panes {
		if p.TabID == t.ID {
			blocks = append(blocks, i.Block(p))
		}
	}
	w.Layers = []Layer{{LayerID: i.Rex("layer", t.ID), Kind: "tiled", Bounds: [4]float64{0, 0, 1, 1},
		Layout: i.node(l, [4]float64{0, 0, 1, 1}, t.ID), Blocks: blocks}}
	if l.Focused != "" {
		f := i.Rex("block", l.Focused)
		w.FocusedBlockID = &f
		if l.Zoomed {
			w.ZoomedBlockID = &f
		}
	}
	return w
}
