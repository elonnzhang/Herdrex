package jsonapi

// Pong is the ping result. Protocol is the direct terminal protocol version
// that term.Dial must send.
type Pong struct {
	Version      string       `json:"version"`
	Protocol     uint32       `json:"protocol"`
	Capabilities Capabilities `json:"capabilities"` // zero when the server sends none
}

// Capabilities are the optional features in ping (src/api/schema/server.rs:42).
type Capabilities struct {
	LiveHandoff          bool `json:"live_handoff"`
	DetachedServerDaemon bool `json:"detached_server_daemon"`
	SurfaceInterest      bool `json:"surface_interest"`
	HealthCheck          bool `json:"health_check"`
	SSHAgentRegistration bool `json:"ssh_agent_registration"`
	// EndpointProtocolGeneration is nil when the server has no endpoint protocol.
	EndpointProtocolGeneration *uint32 `json:"endpoint_protocol_generation"`
}

// Workspace is one workspace in a snapshot. AgentStatus is idle, working,
// blocked, done or unknown, as for Pane.
type Workspace struct {
	ID          string `json:"workspace_id"`
	Label       string `json:"label"`
	ActiveTabID string `json:"active_tab_id"` // the tab the workspace shows
	Focused     bool   `json:"focused"`
	AgentStatus string `json:"agent_status"`
}

// Tab is one tab in a snapshot.
type Tab struct {
	ID          string `json:"tab_id"`
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
	Focused     bool   `json:"focused"`
}

// Pane is one pane in a snapshot or pane.get. Fields Herdr sends as null
// decode as "".
type Pane struct {
	ID            string `json:"pane_id"`
	TabID         string `json:"tab_id"`
	WorkspaceID   string `json:"workspace_id"`
	Focused       bool   `json:"focused"`
	AgentStatus   string `json:"agent_status"`  // idle, working, blocked, done or unknown
	Agent         string `json:"agent"`         // detected agent, such as "claude"
	DisplayAgent  string `json:"display_agent"` // the agent's name as Herdr shows it
	CWD           string `json:"cwd"`
	ForegroundCWD string `json:"foreground_cwd"` // the foreground process's directory
	Label         string `json:"label"`          // set with pane.rename; empty when unset
	TerminalTitle string `json:"terminal_title"`
}

// Rect is a rectangle in character cells.
type Rect struct {
	X, Y, Width, Height int
}

// LayoutPane is where one pane sits in a TabLayout.
type LayoutPane struct {
	PaneID string `json:"pane_id"`
	Rect   Rect   `json:"rect"`
}

// TabLayout is one tab's layout in a snapshot, flattened to pane rectangles.
// Layout gives the split tree.
type TabLayout struct {
	TabID         string       `json:"tab_id"`
	WorkspaceID   string       `json:"workspace_id"`
	FocusedPaneID string       `json:"focused_pane_id"`
	Zoomed        bool         `json:"zoomed"`
	Panes         []LayoutPane `json:"panes"`
}

// Snapshot is the session.snapshot result, with the fields this package
// decodes; read the others with Call. FocusedPaneID is "" when no pane has
// focus.
type Snapshot struct {
	Workspaces    []Workspace `json:"workspaces"`
	Tabs          []Tab       `json:"tabs"`
	Panes         []Pane      `json:"panes"`
	Layouts       []TabLayout `json:"layouts"`
	FocusedPaneID string      `json:"focused_pane_id"`
}

// LayoutNode is a pane leaf (Type "pane") or a binary split (Type "split";
// Direction "right" = side by side, "down" = stacked; Ratio is First's share).
// Layout sets Focused, the tab's focused pane, and Zoomed on the root only.
type LayoutNode struct {
	Type      string      `json:"type"`
	PaneID    string      `json:"pane_id"`
	Direction string      `json:"direction"`
	Ratio     float64     `json:"ratio"`
	First     *LayoutNode `json:"first"`
	Second    *LayoutNode `json:"second"`
	Focused   string      `json:"-"`
	Zoomed    bool        `json:"-"`
}

// PaneReadParams are pane.read's parameters (src/api/schema/panes.rs:388).
// Its strip_ansi is left out: pane.read ignores it in Herdr 0.9.5
// (src/app/api/panes.rs:1530); Format decides whether escapes stay.
type PaneReadParams struct {
	PaneID string `json:"pane_id"`
	Source string `json:"source"`           // visible, recent, recent_unwrapped or detection
	Format string `json:"format,omitempty"` // text (the default) or ansi
	// Lines keeps the last Lines rows. 0 leaves Herdr's default: the whole
	// visible or detection text, or 80 rows of a recent source. Herdr reads at
	// most 1000 (src/app/api_helpers.rs:117).
	Lines uint32 `json:"lines,omitempty"`
}

// PaneReadResult is pane.read's result.
type PaneReadResult struct {
	PaneID      string `json:"pane_id"`
	WorkspaceID string `json:"workspace_id"`
	TabID       string `json:"tab_id"`
	Source      string `json:"source"`
	Format      string `json:"format"`
	Text        string `json:"text"`
	Revision    uint64 `json:"revision"`  // always 0 in Herdr 0.9.5
	Truncated   bool   `json:"truncated"` // the pane holds more rows than Text
}
