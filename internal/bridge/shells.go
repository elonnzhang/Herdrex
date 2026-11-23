package bridge

import (
	"context"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/elonnzhang/herdrex/internal/model"
	"github.com/elonnzhang/herdrex/pkg/herdr/jsonapi"
	"github.com/elonnzhang/herdrex/pkg/herdr/term"
	"github.com/elonnzhang/herdrex/pkg/rex"
	rexsvr "github.com/elonnzhang/herdrex/pkg/rex/server"
	"github.com/elonnzhang/herdrex/pkg/rex/termstream"
)

// The herdrex shells (shell.go) as Rex blocks. b.shells holds the running
// ones; each sits alone in a floating layer of its tab (b.floats, with
// model.Float.Block set), and that layer goes when the shell does. Handlers
// that take a block id ask shellOf first and serve a shell here; Herdr never
// hears of it. Ownership, size requests and themes are kept as for a pane.

// focusMark is a shell that has the focus of its tab. pane is the Herdr
// pane the tab focused then: once Herdr focuses another, the shell has lost
// the focus.
type focusMark struct{ shell, pane string }

var errShellGone = rex.NewError("not_found", "block not found", 404)

// shellOf reports whether a Rex block id names a herdrex shell, and returns
// the shell while it runs. A closed shell comes back nil with true, so that
// callers answer not_found instead of asking Herdr.
func (b *Bridge) shellOf(blockID string) (*shellBlock, bool) {
	id, ok := b.IDs.Herdr(blockID)
	if !ok || !strings.HasPrefix(id, shellPrefix) || b.IDs.Rex("block", id) != blockID {
		return nil, false
	}
	return b.shell(id), true
}

// shell is the running shell with a Herdr-side id, or nil.
func (b *Bridge) shell(id string) *shellBlock {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.shells[id]
}

// sessionShells lists the running shells of a workspace.
func (b *Bridge) sessionShells(ws string) []*shellBlock {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []*shellBlock
	for _, sh := range b.shells {
		if sh.ws == ws {
			out = append(out, sh)
		}
	}
	return out
}

// layoutBlock reads new_layer's layout, which herdrex can only fill with
// one terminal block. Messages for a malformed layout are the real server's.
func layoutBlock(p P) (P, error) {
	raw, ok := p["layout"]
	if !ok {
		return nil, rex.NewError("bad_request", `validating input: validating root: required: missing properties: ["layout"]`, 400)
	}
	n, _ := raw.(map[string]any)
	if n == nil {
		return nil, rex.NewError("bad_request", "input: validating: layout is required", 400)
	}
	blk, isBlock := n["block"].(map[string]any)
	_, isSplit := n["split"]
	switch {
	case isBlock == isSplit:
		return nil, rex.NewError("bad_request", "input: validating: layout must contain exactly one of block or split", 400)
	case isSplit:
		return nil, rex.NewError("bad_request", "herdrex floats one block per layer: layout must be a single block", 400)
	}
	if f := P(blk).str("flavor"); f != "" && f != rex.ShellFlavor {
		return nil, rex.NewError("not_found", `block flavor not found: "`+f+`"`, 404)
	}
	return P(blk), nil
}

// windowArg is the tab of window_id in workspace ws, or its active tab.
func (b *Bridge) windowArg(ctx context.Context, ws, windowID string) (string, *jsonapi.Snapshot, error) {
	s, err := b.fresh(ctx)
	if err != nil {
		return "", nil, err
	}
	if windowID != "" {
		tab, err := b.herdrID(windowID, "window")
		if err != nil {
			return "", nil, err
		}
		if !tabIn(s, tab, ws) {
			return "", nil, rex.NewError("not_found", "window is not in the session", 404)
		}
		return tab, s, nil
	}
	if w := workspace(s, ws); w != nil && w.ActiveTabID != "" {
		return w.ActiveTabID, s, nil
	}
	return "", nil, rex.NewError("conflict", "session has no windows", 409)
}

// shellCWD is where a new shell starts: the block's cwd option, else the
// directory of the tab's focused pane, else the home directory. A path that
// is not a directory here counts as none, as on the real server.
func shellCWD(option string, s *jsonapi.Snapshot, tab string) string {
	focused := ""
	for _, l := range s.Layouts {
		if l.TabID == tab {
			focused = l.FocusedPaneID
		}
	}
	dirs := []string{option}
	for _, p := range s.Panes {
		if p.ID == focused {
			dirs = append(dirs, pwdOf(p))
		}
	}
	home, _ := os.UserHomeDir()
	for _, d := range append(dirs, home) {
		if st, err := os.Stat(d); d != "" && err == nil && st.IsDir() {
			return d
		}
	}
	return "/"
}

// hNewLayer starts a herdrex shell in a new floating layer in front of the
// window. Herdr has no floating panes, so herdrex runs the shell itself.
func hNewLayer(ctx context.Context, b *Bridge, c *rexsvr.Client, p P) (any, error) {
	ws, err := b.herdrID(p.str("session_id"), "session")
	if err != nil {
		return nil, err
	}
	bounds, err := boundsArg(p)
	if err != nil {
		return nil, err
	}
	blk, err := layoutBlock(p)
	if err != nil {
		return nil, err
	}
	tab, s, err := b.windowArg(ctx, ws, p.str("window_id"))
	if err != nil {
		return nil, err
	}
	id := shellPrefix + uuid.NewString()
	label := strings.TrimSpace(blk.str("label"))
	if label == "" {
		label = "shell" // the real server's label: the flavor's name
	}
	sh := &shellBlock{id: id, ws: ws, tab: tab, layer: "layer:" + uuid.NewString(), block: rex.Block{
		BlockID: b.IDs.Rex("block", id), Label: label, CreatorName: rex.TerminalCreator,
		Flavor: rex.ShellFlavor, HostID: b.IDs.HostID,
	}}
	size := term.Size{Cols: 80, Rows: 24, CellWidthPx: defaultCellW, CellHeightPx: defaultCellH}
	if err := sh.start(shellCWD(leafCWD(P{"block": map[string]any(blk)}), s, tab), size, b.shellExited); err != nil {
		return nil, rex.NewError("conflict", "cannot start a shell: "+err.Error(), 409)
	}
	block := sh.block.BlockID
	b.mu.Lock()
	b.shells[id] = sh
	b.floats[tab] = append(b.floats[tab], model.Float{LayerID: sh.layer, PaneID: id, Bounds: bounds, Block: &sh.block})
	b.owners[block] = c.ID
	b.procs[block] = proc{gen: 2, pid: sh.pid, name: filepath.Base(sh.program)}
	b.mu.Unlock()
	if focus, _ := p["focus"].(bool); focus {
		if err := b.focusShell(ctx, sh); err != nil {
			b.removeShell(sh)
			sh.close()
			return nil, err
		}
	}
	// The real server's order [实测]: the block, its owner, its first
	// process_changed, the view, then the shell as the foreground.
	b.sessionEvent(ws, ev("block_created", map[string]any{"block_id": block}))
	b.sessionEvent(ws, ev("block_owner_changed", map[string]any{"block_id": block, "owner": c.ID, "reason": "create"}))
	sid := b.IDs.Rex("session", ws)
	b.blockEvent(sid, block, "process_changed", map[string]any{"generation": 1, "foreground": nil})
	rev := b.settle(ctx, ws)
	b.blockEvent(sid, block, "process_changed", map[string]any{"generation": 2, "foreground": shellProc(sh)})
	select {
	case <-sh.done: // it exited before it was registered, so its exit found nothing to remove
		b.shellExited(sh)
	default:
	}
	return map[string]any{"layer_id": sh.layer, "block_ids": []string{block}, "revision": rev}, nil
}

// focusShell makes the shell's window active and gives the shell its focus.
func (b *Bridge) focusShell(ctx context.Context, sh *shellBlock) error {
	if err := b.call(ctx, "tab.focus", map[string]any{"tab_id": sh.tab}, nil); err != nil {
		return err
	}
	l, err := b.H.Layout(ctx, sh.tab)
	if err != nil {
		return err
	}
	b.mu.Lock()
	b.shellFocus[sh.tab] = focusMark{shell: sh.id, pane: l.Focused}
	b.mu.Unlock()
	return nil
}

// focusedShell is the shell with the focus of a tab whose Herdr focus is on
// pane. A mark Herdr's focus has moved away from is dropped.
func (b *Bridge) focusedShell(tab, pane string) *shellBlock {
	b.mu.Lock()
	defer b.mu.Unlock()
	m, ok := b.shellFocus[tab]
	if ok && m.pane != pane {
		delete(b.shellFocus, tab)
	}
	if !ok || m.pane != pane {
		return nil
	}
	return b.shells[m.shell]
}

// focusedShellArg is the shell with the focus of window_id, or of the
// session's active window, or nil.
func (b *Bridge) focusedShellArg(ctx context.Context, p P) *shellBlock {
	ws, err := b.herdrID(p.str("session_id"), "session")
	if err != nil {
		return nil
	}
	tab, _, err := b.windowArg(ctx, ws, p.str("window_id"))
	if err != nil {
		return nil
	}
	l, err := b.H.Layout(ctx, tab)
	if err != nil {
		return nil
	}
	return b.focusedShell(tab, l.Focused)
}

// forgetShell drops a shell and everything kept for it: its layer, focus,
// owner, size requests, theme and process state. Callers hold mu.
func (b *Bridge) forgetShell(sh *shellBlock) {
	delete(b.shells, sh.id)
	fs := b.floats[sh.tab]
	for i, f := range fs {
		if f.PaneID == sh.id {
			fs = append(fs[:i:i], fs[i+1:]...)
			break
		}
	}
	if len(fs) == 0 {
		delete(b.floats, sh.tab)
	} else {
		b.floats[sh.tab] = fs
	}
	if b.shellFocus[sh.tab].shell == sh.id {
		delete(b.shellFocus, sh.tab)
	}
	block := sh.block.BlockID
	delete(b.owners, block)
	delete(b.desired, block)
	delete(b.themes, block)
	delete(b.procs, block)
}

// removeShell forgets a running shell, reporting whether it was still
// there: of a close and an exit that race, only one reports the block gone.
func (b *Bridge) removeShell(sh *shellBlock) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.shells[sh.id] != sh {
		return false
	}
	b.forgetShell(sh)
	return true
}

// closeShell ends a shell that a client closed, as block.close and
// session.close_layer do for a pane: block_closed, then the view.
func (b *Bridge) closeShell(sh *shellBlock) int {
	if !b.removeShell(sh) {
		return b.currentRev(sh.ws)
	}
	sh.close()
	b.sessionEvent(sh.ws, ev("block_closed", map[string]any{"block_id": sh.block.BlockID}))
	return b.bump(sh.ws)
}

// shellExited reports a shell that ended by itself, as Herdr's pane_exited
// is reported: child_exited, block_closed, then the view.
func (b *Bridge) shellExited(sh *shellBlock) {
	if !b.removeShell(sh) {
		return
	}
	sh.mu.Lock()
	code := sh.code
	sh.mu.Unlock()
	b.blockEvent(b.IDs.Rex("session", sh.ws), sh.block.BlockID, "child_exited", map[string]any{"exit_code": code})
	b.sessionEvent(sh.ws, ev("block_closed", map[string]any{"block_id": sh.block.BlockID}))
	b.bump(sh.ws)
}

// dropShells forgets the shells whose tab the snapshot no longer has, and
// returns them for the caller to close. Callers hold mu.
func (b *Bridge) dropShells(s *jsonapi.Snapshot) []*shellBlock {
	tabs := map[string]bool{}
	for _, t := range s.Tabs {
		tabs[t.ID] = true
	}
	var gone []*shellBlock
	for _, sh := range b.shells {
		if !tabs[sh.tab] {
			b.forgetShell(sh)
			gone = append(gone, sh)
		}
	}
	return gone
}

// Close ends every shell herdrex started and waits until they are gone.
func (b *Bridge) Close() {
	b.mu.Lock()
	var all []*shellBlock
	for _, sh := range b.shells {
		b.forgetShell(sh)
		all = append(all, sh)
	}
	b.mu.Unlock()
	for _, sh := range all {
		sh.close()
	}
	for _, sh := range all {
		select {
		case <-sh.done:
		case <-time.After(shellGrace + time.Second):
		}
	}
}

// shellProc describes the shell for process and process_changed. herdrex
// does not follow the PTY's foreground job, so the shell is both the child
// and the foreground.
func shellProc(sh *shellBlock) map[string]any {
	p := map[string]any{"pid": sh.pid, "user_id": os.Getuid(), "name": filepath.Base(sh.program),
		"path": sh.program, "argv0": sh.argv0}
	if u, err := user.Current(); err == nil {
		p["user"] = u.Username
	}
	return p
}

// shellPlacement is a list_blocks item for a shell.
func (b *Bridge) shellPlacement(sh *shellBlock) map[string]any {
	blk := sh.block
	m := map[string]any{
		"placement": "placed", "window_id": b.IDs.Rex("window", sh.tab),
		"block_id": blk.BlockID, "label": blk.Label, "creator_name": blk.CreatorName,
		"flavor": blk.Flavor, "host_id": blk.HostID,
	}
	if o := b.owner(blk.BlockID); o != "" {
		m["owner"] = o
	}
	return m
}

// shellMethod serves the terminal methods of a shell's block. Those that
// need a terminal emulator, which herdrex does not have, are not supported.
func (b *Bridge) shellMethod(ctx context.Context, c *rexsvr.Client, sh *shellBlock, p P) (any, error) {
	if sh == nil {
		return nil, errShellGone
	}
	args := p.obj("args")
	block := sh.block.BlockID
	switch m := strings.TrimPrefix(p.str("method"), rex.TerminalCreator+"."); m {
	case "write":
		data := args.str("data")
		if err := sh.write([]byte(data)); err != nil {
			return nil, errShellGone
		}
		return map[string]any{"bytes_written": len(data)}, nil
	case "title":
		return map[string]any{"title": ""}, nil
	case "pwd":
		return map[string]any{}, nil
	case "size":
		return termSize(ctx, b, block, sh.id)
	case "resize":
		return termResize(ctx, b, c, p.str("session_id"), block, sh.id, args)
	case "set_theme":
		b.mu.Lock()
		b.themes[block] = args
		b.mu.Unlock()
		sh.sendTheme(args)
		return map[string]any{}, nil
	case "process":
		home, _ := os.UserHomeDir()
		proc := shellProc(sh)
		return map[string]any{"last_exit": nil, "child": proc, "foreground": proc, "ancestors": nil, "home": home}, nil
	case "program_status":
		return map[string]any{"records": []any{}}, nil
	default:
		return nil, rex.NewError("method_not_found", "block method not supported by a herdrex shell: "+m, 404)
	}
}

// shellEvent serves block.event for a shell. Keys and pastes go to the PTY
// as an xterm would send them; mouse and wheel events are not supported.
func shellEvent(sh *shellBlock, e P) error {
	if sh == nil {
		return errShellGone
	}
	var in []byte
	switch e.str("type") {
	case "key":
		k := e.obj("payload")
		if inner := k.obj("key"); inner != nil {
			k = inner
		}
		if kp, ok := parseKey(k); ok {
			in = kp.bytes()
		}
	case "paste":
		in = []byte(e.obj("payload").str("text"))
	}
	if len(in) == 0 {
		return nil
	}
	if sh.write(in) != nil {
		return errShellGone
	}
	return nil
}

// shellKey types a client.key into a shell.
func shellKey(sh *shellBlock, k P) error {
	return shellEvent(sh, P{"type": "key", "payload": map[string]any(k)})
}

// shellStream sends a shell's output to one data stream: the size, the
// theme, the recent output, then what comes, until the shell ends. Unlike a
// Herdr pane's stream it leaves bracketed paste and autowrap to the
// program: the bytes are the program's own, modes included.
func (b *Bridge) shellStream(ctx context.Context, sh *shellBlock, st *rexsvr.DataStream) {
	block := sh.block.BlockID
	themed := false
	send := func(p []byte) bool { return st.Send(ctx, p) == nil }
	for m := range sh.messages(ctx) {
		ok := true
		switch {
		case m.size != nil:
			s := *m.size
			ok = send(termstream.Resize(st.Nonce, int(s.Cols), int(s.Rows), int(s.CellWidthPx), int(s.CellHeightPx), b.owner(block)))
			if t := b.theme(block); ok && !themed && t != nil {
				ok = send(termstream.Theme(st.Nonce, t))
			}
			themed = true
		case m.theme != nil:
			ok = send(termstream.Theme(st.Nonce, m.theme))
		case m.data != nil:
			ok = send(m.data)
		case m.end == endExited:
			send(termstream.ChildExited(st.Nonce, m.code))
			st.Close(rex.CloseBlockClosed, "block closed")
		case m.end == endClosed:
			st.Close(rex.CloseBlockClosed, "block closed")
		case m.end == endLagged:
			st.Close(rex.CloseReconnectRequired, "data reconnect required")
		}
		if !ok {
			return
		}
	}
}
