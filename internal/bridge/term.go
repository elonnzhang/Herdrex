package bridge

import (
	"context"
	"log"
	"os"
	"os/user"
	"strings"

	"github.com/elonnzhang/herdrex/pkg/herdr/jsonapi"
	"github.com/elonnzhang/herdrex/pkg/rex"
	rexsvr "github.com/elonnzhang/herdrex/pkg/rex/server"
)

func (b *Bridge) pane(ctx context.Context, id string) (jsonapi.Pane, error) {
	var r struct {
		Pane jsonapi.Pane `json:"pane"`
	}
	err := b.call(ctx, "pane.get", map[string]any{"pane_id": id}, &r)
	return r.Pane, err
}

func (b *Bridge) paneSize(ctx context.Context, id string) (cols, rows int) {
	s, err := b.fresh(ctx)
	if err != nil {
		return 80, 24
	}
	for _, l := range s.Layouts {
		for _, x := range l.Panes {
			if x.PaneID == id {
				return x.Rect.Width, x.Rect.Height
			}
		}
	}
	return 80, 24
}

func hBlockMethod(ctx context.Context, b *Bridge, c *rexsvr.Client, p P) (any, error) {
	pane, err := b.herdrID(p.str("block_id"), "block")
	if err != nil {
		return nil, err
	}
	args := p.obj("args")
	switch strings.TrimPrefix(p.str("method"), rex.TerminalCreator+".") {
	case "format":
		format := "text"
		if f := args.str("format"); f == "vt" || f == "html" {
			format = "ansi"
		}
		src := "visible"
		if v, _ := args["scrollback"].(bool); v {
			src = "recent"
		}
		t, err := b.H.ReadPane(ctx, pane, src, format)
		return map[string]any{"content": t, "format": args.str("format")}, err
	case "write":
		data := args.str("data")
		b.unscroll(ctx, pane)
		if text, ok := pasted(data); ok {
			return map[string]any{"bytes_written": len(data)}, b.pasteInput(ctx, pane, data, text)
		}
		err := b.call(ctx, "pane.send_text", map[string]any{"pane_id": pane, "text": data}, nil)
		return map[string]any{"bytes_written": len(data)}, err
	case "title":
		x, err := b.pane(ctx, pane)
		return map[string]any{"title": x.TerminalTitle}, err
	case "pwd":
		x, err := b.pane(ctx, pane)
		if err != nil {
			return nil, err
		}
		if d := pwdOf(x); d != "" {
			return map[string]any{"pwd": pwdURL(d)}, nil
		}
		return map[string]any{}, nil
	case "size":
		return termSize(ctx, b, p.str("block_id"), pane)
	case "resize":
		return termResize(ctx, b, c, p.str("session_id"), p.str("block_id"), pane, args)
	case "clear", "reset": // Herdr cannot reset its emulator; clearing is the nearest it has
		return nil, b.call(ctx, "pane.clear", map[string]any{"pane_id": pane}, nil)
	case "set_theme": // Herdr keeps its own colors; streams relay the theme
		block := p.str("block_id")
		b.mu.Lock()
		b.themes[block] = args
		b.mu.Unlock()
		if h := b.hub(pane); h != nil {
			h.sendTheme(args)
		}
		return map[string]any{}, nil
	case "process":
		return termProcess(ctx, b, pane)
	case "program_status":
		x, err := b.pane(ctx, pane)
		records := []any{}
		switch x.AgentStatus { // "unknown" means no agent was detected
		case "idle", "working", "blocked", "done":
			records = append(records, map[string]any{"state": x.AgentStatus})
		}
		return map[string]any{"records": records}, err
	}
	return nil, rex.NewError("method_not_found", "block method not supported: "+p.str("method"), 404)
}

// terminalMethods are the block methods also served as top-level methods.
var terminalMethods = []string{
	"format", "write", "title", "pwd", "size", "resize", "set_theme",
	"process", "program_status", "clear", "reset",
}

// terminalMethod serves com.superlogical.terminal.<m>, whose payload is the
// block method's args with session_id and block_id beside them.
func terminalMethod(m string) handler {
	return func(ctx context.Context, b *Bridge, c *rexsvr.Client, p P) (any, error) {
		args := map[string]any{}
		for k, v := range p {
			if k != "session_id" && k != "block_id" {
				args[k] = v
			}
		}
		return hBlockMethod(ctx, b, c, P{
			"session_id": p.str("session_id"),
			"block_id":   p.str("block_id"),
			"method":     m,
			"args":       args,
		})
	}
}

// termProcess maps pane.process_info. Herdr reports neither user IDs nor
// exit status, so the bridge assumes it runs as the same user as Herdr and
// leaves last_exit null. Like Rex, it returns argv0 but no other arguments.
func termProcess(ctx context.Context, b *Bridge, pane string) (any, error) {
	var r struct {
		Info struct {
			ShellPID int `json:"shell_pid"`
			FgPGID   int `json:"foreground_process_group_id"`
			Procs    []struct {
				PID  int      `json:"pid"`
				Name string   `json:"name"`
				Argv []string `json:"argv"`
				CWD  string   `json:"cwd"`
			} `json:"foreground_processes"`
		} `json:"process_info"`
	}
	if err := b.call(ctx, "pane.process_info", map[string]any{"pane_id": pane}, &r); err != nil {
		return nil, err
	}
	uid := os.Getuid()
	name := ""
	if u, err := user.Current(); err == nil {
		name = u.Username
	}
	home, _ := os.UserHomeDir()
	var fg, child any
	for _, p := range r.Info.Procs {
		proc := map[string]any{"pid": p.PID, "user_id": uid, "name": p.Name}
		if name != "" {
			proc["user"] = name
		}
		if len(p.Argv) > 0 { // Herdr's argv0 field drops the login dash; argv[0] keeps it
			proc["argv0"] = p.Argv[0]
		}
		if p.CWD != "" {
			proc["cwd"] = p.CWD
		}
		if p.PID == r.Info.FgPGID || fg == nil {
			fg = proc
		}
		if p.PID == r.Info.ShellPID {
			child = proc
		}
	}
	return map[string]any{"last_exit": nil, "child": child, "foreground": fg, "ancestors": nil, "home": home}, nil
}

// hBlockEvent forwards keys, pastes, mouse and wheel events. Focus has no
// Herdr counterpart for a single pane and is accepted without effect.
func hBlockEvent(ctx context.Context, b *Bridge, c *rexsvr.Client, p P) (any, error) {
	pane, err := b.herdrID(p.str("block_id"), "block")
	if err != nil {
		return nil, err
	}
	ev := p.obj("event")
	switch ev.str("type") {
	case "paste":
		b.unscroll(ctx, pane)
		return nil, b.call(ctx, "pane.send_text",
			map[string]any{
				"pane_id": pane,
				"text":    ev.obj("payload").str("text"),
			}, nil)
	case "key":
		k := ev.obj("payload")
		if inner := k.obj("key"); inner != nil { // payload may wrap the key object
			k = inner
		}
		return nil, b.sendKey(ctx, pane, k)
	case "mouse":
		return nil, b.mouseEvent(ctx, pane, ev.obj("payload"))
	case "wheel":
		return nil, b.wheelEvent(ctx, pane, ev.obj("payload"))
	case "focus":
		return nil, nil
	}
	log.Printf("block.event %s payload=%v (not mapped)", ev.str("type"), ev["payload"])
	return nil, nil
}

var handlers map[string]handler

func init() {
	handlers = map[string]handler{
		"server.status":                 hStatus,
		"server.version":                hVersion,
		"session.list":                  hList,
		"session.view":                  hView,
		"session.attach":                hAttach,
		"session.attach_all":            hAttachAll,
		"session.bootstrap":             hBootstrap,
		"session.create":                hCreate,
		"session.destroy":               hDestroy,
		"session.set_label":             hSetLabel,
		"session.new_window":            hNewWindow,
		"session.close_window":          tabOp("tab.close", false),
		"session.set_window_label":      tabOp("tab.rename", true),
		"session.focus_window":          tabOp("tab.focus", false),
		"session.focus_block":           hFocusBlock,
		"session.new_split":             hNewSplit,
		"session.set_block_label":       hSetBlockLabel,
		"session.zoom_block":            hZoom,
		"session.swap_blocks":           hSwap,
		"session.resize_pane":           hResizePane,
		"session.list_blocks":           hListBlocks,
		"session.describe_block":        hDescribeBlock,
		"block.close":                   hBlockClose,
		"session.float_block":           hFloatBlock,
		"session.resize_layer":          hResizeLayer,
		"session.raise_layer":           restack(true),
		"session.lower_layer":           restack(false),
		"session.close_layer":           hCloseLayer,
		"session.move_block":            hMoveBlock,
		"block.method":                  hBlockMethod,
		"block.event":                   hBlockEvent,
		"block_creator.list":            hCreators,
		"target.resolve":                hResolve,
		"target.complete":               hComplete,
		"client.key":                    hClientKey,
		"session.own_block":             hOwnBlock,
		"hosts.list":                    hHostsList,
		"client.keymap":                 hKeymap,
		"session.disown_block":          hDisownBlock,
		"session.focus_next_pane":       focusPane(1),
		"session.focus_previous_pane":   focusPane(-1),
		"session.focus_next_window":     focusWindow(1),
		"session.focus_previous_window": focusWindow(-1),
		"session.focus_direction":       hFocusDirection,
		"session.move":                  hMove,
		"session.move_window":           hMoveWindow,
		"session.list_clients":          hListClients,
	}
	for _, m := range terminalMethods { // top-level form: com.superlogical.terminal.<m>
		handlers[rex.TerminalCreator+"."+m] = terminalMethod(m)
	}
}

const pasteStart, pasteEnd = "\x1b[200~", "\x1b[201~"

// pasted returns the text of a write that is exactly one bracketed paste,
// the form Herdr's attach input recognizes.
func pasted(data string) (string, bool) {
	rest, ok := strings.CutPrefix(data, pasteStart)
	if !ok {
		return "", false
	}
	text, after, ok := strings.Cut(rest, pasteEnd)
	return text, ok && after == ""
}

// pasteInput delivers a bracketed paste. Through the pane's controlling
// attach, Herdr marks it again only if the program asked for bracketed paste,
// as it does for its own attach client. pane.send_text would pass the markers
// to a program that never asked for them, so without that attach the text
// goes unmarked.
func (b *Bridge) pasteInput(ctx context.Context, pane, data, text string) error {
	if h := b.hub(pane); h != nil && h.control {
		return h.conn.Input([]byte(data))
	}
	return b.call(ctx, "pane.send_text", map[string]any{"pane_id": pane, "text": text}, nil)
}
