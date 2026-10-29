package bridge

import (
	"context"
	"regexp"
	"strings"

	rexsvr "github.com/elonnzhang/herdrex/pkg/rex/server"
)

// Herdr's send_keys has no names for these, so send the escape sequences.
var rawKeys = map[string]string{
	"delete": "\x1b[3~", "home": "\x1b[H", "end": "\x1b[F",
	"page_up": "\x1b[5~", "page_down": "\x1b[6~", "insert": "\x1b[2~",
}

var herdrKeys = map[string]string{
	"enter": "enter", "numpad_enter": "enter", "tab": "tab", "backspace": "backspace",
	"space": "space", "escape": "esc", "arrow_up": "up", "arrow_down": "down",
	"arrow_left": "left", "arrow_right": "right",
}

var fnKey = regexp.MustCompile(`^f\d{1,2}$`)

// sendKey turns a Rex key event (the client.key "key" object) into Herdr input.
func (b *Bridge) sendKey(ctx context.Context, pane string, k P) error {
	if a := k.str("action"); a == "release" {
		return nil
	}
	mods := map[string]bool{}
	for _, m := range strings.Split(k.str("mods"), "+") {
		mods[m] = true
	}
	if mods["super"] {
		return nil
	}
	b.unscroll(ctx, pane)
	name := k.str("key")
	text := k.str("text")
	chord := mods["ctrl"] || mods["alt"]
	if text != "" && !chord {
		return b.call(ctx, "pane.send_text", map[string]any{"pane_id": pane, "text": text}, nil)
	}
	if raw, ok := rawKeys[name]; ok && !chord {
		return b.call(ctx, "pane.send_text", map[string]any{"pane_id": pane, "text": raw}, nil)
	}
	base := herdrKeys[name]
	if base == "" && fnKey.MatchString(name) {
		base = name
	}
	if base == "" {
		if cp := int(k.num("unshifted_codepoint")); cp > 0 {
			base = string(rune(cp))
		} else if strings.HasPrefix(name, "key_") {
			base = strings.TrimPrefix(name, "key_")
		} else if strings.HasPrefix(name, "digit_") {
			base = strings.TrimPrefix(name, "digit_")
		}
	}
	if base == "" {
		return nil
	}
	var parts []string
	for _, m := range []string{"ctrl", "alt", "shift"} {
		if mods[m] {
			parts = append(parts, m)
		}
	}
	key := strings.Join(append(parts, base), "+")
	return b.call(ctx, "pane.send_keys", map[string]any{"pane_id": pane, "keys": []string{key}}, nil)
}

// focusedPane finds the pane that has focus in a session's active window.
func (b *Bridge) focusedPane(ctx context.Context, sessionID string) (string, error) {
	wid, err := b.herdrID(sessionID, "session")
	if err != nil {
		return "", err
	}
	s, err := b.fresh(ctx)
	if err != nil {
		return "", err
	}
	for _, w := range s.Workspaces {
		if w.ID == wid && w.ActiveTabID != "" {
			l, err := b.H.Layout(ctx, w.ActiveTabID)
			if err != nil {
				return "", err
			}
			return l.Focused, nil
		}
	}
	return "", nil
}

func hClientKey(ctx context.Context, b *Bridge, c *rexsvr.Client, p P) (any, error) {
	pane := ""
	if id := p.str("block_id"); id != "" {
		h, err := b.herdrID(id, "block")
		if err != nil {
			return nil, err
		}
		pane = h
	} else {
		f, err := b.focusedPane(ctx, p.str("session_id"))
		if err != nil {
			return nil, err
		}
		pane = f
	}
	if pane == "" {
		return map[string]any{"handled": false}, nil
	}
	err := b.sendKey(ctx, pane, p.obj("key"))
	return map[string]any{"handled": false}, err
}
