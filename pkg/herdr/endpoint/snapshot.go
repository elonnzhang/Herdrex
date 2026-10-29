package endpoint

import "encoding/json"

// Snapshot is the session projection the server pushes as JSON
// (shell.snapshot.v1, ClientShellSnapshot): one right after the handshake,
// then a new one with a higher Revision whenever this connection's view of
// the session changes. Inactive connections receive it too. Unknown fields
// are ignored, and absent optional strings are empty.
type Snapshot struct {
	BootID                      string               `json:"boot_id"` // changes whenever the server restarts
	Revision                    uint64               `json:"revision"`
	ConfigDiagnostic            string               `json:"config_diagnostic"`
	ProductAnnouncement         *ProductAnnouncement `json:"product_announcement"`
	UpdateAvailable             string               `json:"update_available"`
	UpdateInstallCommand        string               `json:"update_install_command"`
	ServerKeybindingsTOML       string               `json:"server_keybindings_toml"`
	LatestReleaseNotesAvailable bool                 `json:"latest_release_notes_available"`
	IntegrationUpdatesAvailable bool                 `json:"integration_updates_available"`
	WorktreeDirectory           string               `json:"worktree_directory"`
	ReleaseNotes                *ReleaseNotes        `json:"release_notes"`
	FocusedWorkspaceID          string               `json:"focused_workspace_id"`
	FocusedTabID                string               `json:"focused_tab_id"`
	FocusedPaneID               string               `json:"focused_pane_id"`
	TabBarRight                 []StatusSegment      `json:"tab_bar_right"`
	TabBarRightSeparator        string               `json:"tab_bar_right_separator"`
	AgentViewLabel              string               `json:"agent_view_label"`
	AgentOrder                  []string             `json:"agent_order"` // pane IDs
	Workspaces                  []Workspace          `json:"workspaces"`
	Tabs                        []Tab                `json:"tabs"`
	Panes                       []Pane               `json:"panes"`
	Agents                      []Agent              `json:"agents"`
	Commands                    []Command            `json:"commands"`
}

type ProductAnnouncement struct {
	Version string `json:"version"`
	ID      string `json:"id"`
	Title   string `json:"title"`
	Body    string `json:"body"`
	Preview bool   `json:"preview"`
}

type ReleaseNotes struct {
	Version string `json:"version"`
	Body    string `json:"body"`
	Preview bool   `json:"preview"`
}

// StatusSegment is one piece of text on the right of the tab bar.
type StatusSegment struct {
	Text   string `json:"text"`
	Accent bool   `json:"accent"`
}

type Workspace struct {
	ID              string      `json:"workspace_id"`
	ActiveTabID     string      `json:"active_tab_id"`
	NewWorkspaceCWD string      `json:"new_workspace_cwd"`
	Number          int         `json:"number"`
	Label           string      `json:"label"`
	CustomLabel     bool        `json:"custom_label"`
	Branch          string      `json:"branch"`
	GitAheadBehind  *[2]int     `json:"git_ahead_behind"` // commits ahead and behind the upstream
	Tokens          [][2]string `json:"tokens"`           // name and value pairs
	Worktree        *Worktree   `json:"worktree"`
	Focused         bool        `json:"focused"`
	AgentStatus     AgentStatus `json:"agent_status"`
}

type Worktree struct {
	Key              string `json:"key"`
	Label            string `json:"label"`
	IsLinkedWorktree bool   `json:"is_linked_worktree"`
}

type Tab struct {
	ID          string      `json:"tab_id"`
	WorkspaceID string      `json:"workspace_id"`
	Number      int         `json:"number"`
	Label       string      `json:"label"`
	CustomLabel bool        `json:"custom_label"`
	Zoomed      bool        `json:"zoomed"`
	Focused     bool        `json:"focused"`
	AgentStatus AgentStatus `json:"agent_status"`
}

type Pane struct {
	ID                    string `json:"pane_id"`
	WorkspaceID           string `json:"workspace_id"`
	TabID                 string `json:"tab_id"`
	Label                 string `json:"label"`
	CWD                   string `json:"cwd"`
	ForegroundCWD         string `json:"foreground_cwd"`
	Focused               bool   `json:"focused"`
	RightClickPassthrough bool   `json:"right_click_passthrough"`
}

type Agent struct {
	PaneID                string      `json:"pane_id"`
	WorkspaceID           string      `json:"workspace_id"`
	TabID                 string      `json:"tab_id"`
	Name                  string      `json:"name"`
	DisplayAgent          string      `json:"display_agent"`
	Agent                 string      `json:"agent"`
	Title                 string      `json:"title"`
	TerminalTitle         string      `json:"terminal_title"`
	TerminalTitleStripped string      `json:"terminal_title_stripped"`
	AgentStatus           AgentStatus `json:"agent_status"`
	StateChangeSeq        uint64      `json:"state_change_seq"`
	StateLabels           [][2]string `json:"state_labels"`
	Tokens                [][2]string `json:"tokens"`
	Focused               bool        `json:"focused"`
}

// Command is a custom command and its key bindings.
type Command struct {
	ID            string        `json:"command_id"`
	BindingLabel  string        `json:"binding_label"`
	BindingLabels []string      `json:"binding_labels"`
	Action        CommandAction `json:"action"`
	Description   string        `json:"description"`
}

// AgentStatus is an agent's state. A status this package does not know
// decodes as AgentUnknown.
type AgentStatus string

const (
	AgentIdle    AgentStatus = "idle"
	AgentWorking AgentStatus = "working"
	AgentBlocked AgentStatus = "blocked"
	AgentDone    AgentStatus = "done"
	AgentUnknown AgentStatus = "unknown"
)

func (s *AgentStatus) UnmarshalJSON(b []byte) error {
	var v string
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	switch st := AgentStatus(v); st {
	case AgentIdle, AgentWorking, AgentBlocked, AgentDone:
		*s = st
	default:
		*s = AgentUnknown
	}
	return nil
}

// CommandAction is what a custom command runs. An action this package does
// not know decodes as ActionUnknown.
type CommandAction string

const (
	ActionShell        CommandAction = "Shell"
	ActionPane         CommandAction = "Pane"
	ActionPopup        CommandAction = "Popup"
	ActionPluginAction CommandAction = "PluginAction"
	ActionUnknown      CommandAction = "Unknown"
)

func (a *CommandAction) UnmarshalJSON(b []byte) error {
	var v string
	if err := json.Unmarshal(b, &v); err != nil {
		return err
	}
	switch act := CommandAction(v); act {
	case ActionShell, ActionPane, ActionPopup, ActionPluginAction:
		*a = act
	default:
		*a = ActionUnknown
	}
	return nil
}

// AgentView is the agent view projection that accompanies snapshots
// (endpoint.agent-view.v1). View is the view as set with agent.view.set, or
// null when none is set.
type AgentView struct {
	BootID   string          `json:"boot_id"`
	Revision uint64          `json:"revision"` // the snapshot revision it belongs to
	View     json.RawMessage `json:"view"`
}

// AgentCompletions maps agent pane IDs to their completion counters
// (endpoint.agent-completions.v1).
type AgentCompletions struct {
	BootID      string            `json:"boot_id"`
	Revision    uint64            `json:"revision"`
	Completions map[string]uint64 `json:"completions"`
}
