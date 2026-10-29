package herdr

type Workspace struct {
	ID          string `json:"workspace_id"`
	Label       string `json:"label"`
	ActiveTabID string `json:"active_tab_id"`
	Focused     bool   `json:"focused"`
	AgentStatus string `json:"agent_status"`
}

type Tab struct {
	ID          string `json:"tab_id"`
	WorkspaceID string `json:"workspace_id"`
	Label       string `json:"label"`
	Focused     bool   `json:"focused"`
}

type Pane struct {
	ID            string `json:"pane_id"`
	TabID         string `json:"tab_id"`
	WorkspaceID   string `json:"workspace_id"`
	Focused       bool   `json:"focused"`
	AgentStatus   string `json:"agent_status"`
	Agent         string `json:"agent"`         // detected agent, such as "claude"
	DisplayAgent  string `json:"display_agent"` // the agent's name as Herdr shows it
	CWD           string `json:"cwd"`
	ForegroundCWD string `json:"foreground_cwd"`
	Label         string `json:"label"` // set with pane.rename; empty when unset
	TerminalTitle string `json:"terminal_title"`
}

type Rect struct {
	X, Y, Width, Height int
}

type LayoutPane struct {
	PaneID string `json:"pane_id"`
	Rect   Rect   `json:"rect"`
}

type TabLayout struct {
	TabID         string       `json:"tab_id"`
	WorkspaceID   string       `json:"workspace_id"`
	FocusedPaneID string       `json:"focused_pane_id"`
	Zoomed        bool         `json:"zoomed"`
	Panes         []LayoutPane `json:"panes"`
}

type Snapshot struct {
	Workspaces    []Workspace `json:"workspaces"`
	Tabs          []Tab       `json:"tabs"`
	Panes         []Pane      `json:"panes"`
	Layouts       []TabLayout `json:"layouts"`
	FocusedPaneID string      `json:"focused_pane_id"`
}

// LayoutNode is a pane leaf or a binary split ("right" = side by side).
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
