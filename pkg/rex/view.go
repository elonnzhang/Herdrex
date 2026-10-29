package rex

// View is the result of session.view (docs/rex-api.md 3.1). Rects and bounds
// are fractions of the window's width and height.
type View struct {
	Revision       int      `json:"revision"`
	SessionID      string   `json:"session_id"`
	Label          string   `json:"label"`
	ActiveWindowID *string  `json:"active_window_id,omitempty"`
	Windows        []Window `json:"windows"`
}

type Window struct {
	WindowID       string  `json:"window_id"`
	Label          string  `json:"label"`
	FocusedBlockID *string `json:"focused_block_id,omitempty"`
	Layers         []Layer `json:"layers"`
	ZoomedBlockID  *string `json:"zoomed_block_id,omitempty"`
}

// Layer is a window layer. The first is tiled, the rest float.
type Layer struct {
	LayerID string     `json:"layer_id"`
	Kind    string     `json:"kind"`
	Bounds  [4]float64 `json:"bounds"`
	Layout  *Node      `json:"layout"`
	Blocks  []Block    `json:"blocks"`
}

// Node is a layout node: a block or a split.
type Node struct {
	Rect    [4]float64 `json:"rect"`
	BlockID string     `json:"block_id,omitempty"`
	Split   *Split     `json:"split,omitempty"`
}

// Split divides a node left and right ("horizontal") or top and bottom
// ("vertical"); Before is the left or top part.
type Split struct {
	SplitID   string  `json:"split_id"`
	Direction string  `json:"direction"`
	Ratio     float64 `json:"ratio"`
	Before    *Node   `json:"before"`
	After     *Node   `json:"after"`
}

type Block struct {
	BlockID     string `json:"block_id"`
	Label       string `json:"label"`
	CreatorName string `json:"creator_name"`
	Flavor      string `json:"flavor"`
	HostID      string `json:"host_id"`
	Owner       string `json:"owner,omitempty"`
}
