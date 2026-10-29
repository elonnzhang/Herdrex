package bridge

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/url"
	"os"
	"time"

	"github.com/elonnzhang/herdrex/pkg/herdr/jsonapi"
)

// Events that Refresh derives from snapshot changes besides the layout ones
// in events.go: session order, and the terminal block events the real server
// sends from its own emulator (title, pwd, program status, process).

// registerIDs gives every object in the snapshot a Rex id, so ids in events
// resolve back to Herdr.
func (b *Bridge) registerIDs(s *jsonapi.Snapshot) {
	for _, w := range s.Workspaces {
		b.IDs.Rex("session", w.ID)
	}
	for _, t := range s.Tabs {
		b.IDs.Rex("window", t.ID)
	}
	for _, p := range s.Panes {
		b.IDs.Rex("block", p.ID)
	}
}

func workspaceOrder(s *jsonapi.Snapshot) []string {
	out := make([]string, len(s.Workspaces))
	for i, w := range s.Workspaces {
		out[i] = w.ID
	}
	return out
}

// sessionMoves sends session_moved for each move that turns the old order of
// surviving workspaces into the new one. Workspaces on the longest run that
// kept its order stay put; the rest move, each just before its successor.
func (b *Bridge) sessionMoves(s *jsonapi.Snapshot) {
	cur := workspaceOrder(s)
	old := b.order
	b.order = cur
	pos := map[string]int{}
	for i, id := range old {
		pos[id] = i
	}
	var seq []string // surviving workspaces in new order
	for _, id := range cur {
		if _, ok := pos[id]; ok {
			seq = append(seq, id)
		}
	}
	keep := longestRun(seq, pos)
	for i := len(seq) - 1; i >= 0; i-- {
		if keep[seq[i]] {
			continue
		}
		p := map[string]any{"session_id": b.IDs.Rex("session", seq[i])}
		if i+1 < len(seq) {
			p["before_session_id"] = b.IDs.Rex("session", seq[i+1])
		}
		b.srv.Emit("session_moved", p)
	}
}

// longestRun returns a longest subsequence of seq whose old positions rise.
func longestRun(seq []string, pos map[string]int) map[string]bool {
	n := len(seq)
	length, prev := make([]int, n), make([]int, n)
	best := -1
	for i := range seq {
		length[i], prev[i] = 1, -1
		for j := 0; j < i; j++ {
			if pos[seq[j]] < pos[seq[i]] && length[j]+1 > length[i] {
				length[i], prev[i] = length[j]+1, j
			}
		}
		if best < 0 || length[i] > length[best] {
			best = i
		}
	}
	keep := map[string]bool{}
	for i := best; i >= 0; i = prev[i] {
		keep[seq[i]] = true
	}
	return keep
}

// proc is what process_changed last reported for a block.
type proc struct {
	gen  int
	pid  int
	name string
}

// paneEvents compares each surviving pane with its previous state and sends
// title_changed, pwd_changed, program_status_changed/_removed and, when the
// title or directory changed, process_changed if the foreground moved.
// Herdr does not say which process set a title, so owner is left out, as
// the Rex schema allows.
func (b *Bridge) paneEvents(ctx context.Context, old, cur *jsonapi.Snapshot) {
	if old == nil {
		return
	}
	before := map[string]jsonapi.Pane{}
	for _, p := range old.Panes {
		before[p.ID] = p
	}
	for _, p := range cur.Panes {
		o, ok := before[p.ID]
		if !ok {
			continue
		}
		sid, block := b.IDs.Rex("session", p.WorkspaceID), b.IDs.Rex("block", p.ID)
		if o.TerminalTitle != p.TerminalTitle {
			b.blockEvent(sid, block, "title_changed", map[string]any{"title": p.TerminalTitle})
		}
		if pwdOf(o) != pwdOf(p) && pwdOf(p) != "" {
			b.blockEvent(sid, block, "pwd_changed", map[string]any{"pwd": pwdURL(pwdOf(p))})
		}
		if o.AgentStatus != p.AgentStatus {
			if rec := statusRecord(p); rec != nil {
				b.blockEvent(sid, block, "program_status_changed",
					map[string]any{"id": "", "record": rec, "reason": "report"})
			} else if statusRecord(o) != nil {
				b.blockEvent(sid, block, "program_status_removed",
					map[string]any{"ids": []string{""}, "reason": "foreground"})
			}
			if what := attention(p.AgentStatus); b.Notify && what != "" {
				b.blockEvent(sid, block, "desktop_notification",
					map[string]any{"title": agentName(p) + " " + what, "body": paneContext(cur, p)})
			}
		}
		if o.TerminalTitle != p.TerminalTitle || pwdOf(o) != pwdOf(p) {
			b.processChanged(ctx, sid, block, p.ID)
		}
	}
}

func pwdOf(p jsonapi.Pane) string {
	if p.ForegroundCWD != "" {
		return p.ForegroundCWD
	}
	return p.CWD
}

var hostname, _ = os.Hostname()

// pwdURL writes a directory the way OSC 7 reports it and Rex passes it on:
// file://<host><path>.
func pwdURL(dir string) string {
	return (&url.URL{Scheme: "file", Host: hostname, Path: dir}).String()
}

// statusRecord is the program_status root record for Herdr's agent status,
// or nil when Herdr detected no agent.
func statusRecord(p jsonapi.Pane) map[string]any {
	switch p.AgentStatus {
	case "idle", "working", "blocked", "done":
		return map[string]any{"state": p.AgentStatus, "updated_at": time.Now().UTC().Format(time.RFC3339Nano)}
	}
	return nil
}

// attention is what an agent's new status asks of the user, in the words of
// Herdr's toasts: blocked needs attention, done (finished, not yet seen) has
// finished. Other statuses need nothing.
func attention(status string) string {
	switch status {
	case "blocked":
		return "needs attention"
	case "done":
		return "finished"
	}
	return ""
}

func agentName(p jsonapi.Pane) string {
	for _, n := range []string{p.DisplayAgent, p.Agent} {
		if n != "" {
			return n
		}
	}
	return "agent"
}

// paneContext says where a pane is, as Herdr's toasts do: the workspace, and
// the tab when the workspace has more than one.
func paneContext(s *jsonapi.Snapshot, p jsonapi.Pane) string {
	var ws, tab string
	tabs := 0
	for _, w := range s.Workspaces {
		if w.ID == p.WorkspaceID {
			ws = w.Label
		}
	}
	for _, t := range s.Tabs {
		if t.WorkspaceID == p.WorkspaceID {
			tabs++
			if t.ID == p.TabID {
				tab = t.Label
			}
		}
	}
	if tabs > 1 && tab != "" {
		return ws + " · " + tab
	}
	return ws
}

// processChanged sends process_changed when the foreground process differs
// from the last one reported for the block.
func (b *Bridge) processChanged(ctx context.Context, sid, block, pane string) {
	r, err := termProcess(ctx, b, pane)
	if err != nil {
		return
	}
	res := r.(map[string]any)
	fg, _ := res["foreground"].(map[string]any)
	pid, name := 0, ""
	if fg != nil {
		pid, _ = fg["pid"].(int)
		name, _ = fg["name"].(string)
	}
	b.mu.Lock()
	last, seen := b.procs[block]
	if seen && last.pid == pid && last.name == name {
		b.mu.Unlock()
		return
	}
	payload := map[string]any{"generation": last.gen + 1, "foreground": res["foreground"]}
	b.procs[block] = proc{gen: last.gen + 1, pid: pid, name: name}
	b.mu.Unlock()
	b.blockEvent(sid, block, "process_changed", payload)
}

// paneSession is the Rex session of a Herdr pane, from the last snapshot.
func (b *Bridge) paneSession(pane string) (string, bool) {
	s := b.snapshot()
	if s == nil {
		return "", false
	}
	for _, p := range s.Panes {
		if p.ID == pane {
			return b.IDs.Rex("session", p.WorkspaceID), true
		}
	}
	return "", false
}

// paneBell sends bell for a BEL that Herdr forwarded from the pane.
func (b *Bridge) paneBell(pane string) {
	if sid, ok := b.paneSession(pane); ok {
		b.blockEvent(sid, b.IDs.Rex("block", pane), "bell", map[string]any{})
	}
}

// maxClipboardText is the largest text clipboard_written carries; larger
// copies report only their size, as the Rex schema allows.
const maxClipboardText = 64 << 10

// paneClipboard sends clipboard_written for an OSC 52 copy. Herdr passes the
// data base64-encoded.
func (b *Bridge) paneClipboard(pane, data string) {
	sid, ok := b.paneSession(pane)
	if !ok {
		return
	}
	raw, err := base64.StdEncoding.DecodeString(data)
	if err != nil {
		return
	}
	b.mu.Lock()
	b.clips++
	id := b.clips
	b.mu.Unlock()
	p := map[string]any{"id": id, "bytes": len(raw)}
	if len(raw) > 0 && len(raw) <= maxClipboardText {
		p["text"] = string(raw)
	}
	b.blockEvent(sid, b.IDs.Rex("block", pane), "clipboard_written", p)
}

// theme is the last set_theme of a block, or nil.
func (b *Bridge) theme(block string) map[string]any {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.themes[block]
}

// paneExited sends child_exited when Herdr reports that a pane's program
// ended, and marks the pane so its block_closed comes first, as on the real
// server. Herdr reports no exit status, so the code is 0.
func (b *Bridge) paneExited(data json.RawMessage) {
	var d struct {
		PaneID string `json:"pane_id"`
	}
	if json.Unmarshal(data, &d) != nil || d.PaneID == "" {
		return
	}
	sid, ok := b.paneSession(d.PaneID)
	if !ok {
		return
	}
	b.mu.Lock()
	b.exited[d.PaneID] = true
	b.mu.Unlock()
	b.blockEvent(sid, b.IDs.Rex("block", d.PaneID), "child_exited", map[string]any{"exit_code": 0})
}

// takeExited reports whether the pane's program exited, forgetting it.
func (b *Bridge) takeExited(pane string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	ok := b.exited[pane]
	delete(b.exited, pane)
	return ok
}
