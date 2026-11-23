package bridge

import (
	"context"
	"fmt"
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

// keyPress is a Rex key event (the client.key "key" object) that types
// something: name is the physical key, text what the layout typed with it.
type keyPress struct {
	name, text       string
	ctrl, alt, shift bool
	codepoint        int // unshifted_codepoint
}

// parseKey reads a key event. A release, a super chord, or a key in an
// unfinished composition, such as a dead key, types nothing: the key that
// ends a composition carries the composed text. Ctrl and alt make a chord
// only when the layout did not use them for the text, so an Option key that
// types a character on a Mac layout types that character.
func parseKey(k P) (keyPress, bool) {
	if composing, _ := k["composing"].(bool); composing || k.str("action") == "release" {
		return keyPress{}, false
	}
	mods := map[string]bool{}
	for _, m := range strings.Split(k.str("mods"), "+") {
		mods[m] = true
	}
	if mods["super"] {
		return keyPress{}, false
	}
	for _, m := range strings.Split(k.str("consumed_mods"), "+") {
		if m == "ctrl" || m == "alt" {
			delete(mods, m)
		}
	}
	return keyPress{
		name: k.str("key"), text: k.str("text"),
		ctrl: mods["ctrl"], alt: mods["alt"], shift: mods["shift"],
		codepoint: int(k.num("unshifted_codepoint")),
	}, true
}

func (k keyPress) chord() bool { return k.ctrl || k.alt }

// base is the character of a letter, digit or symbol key without shift, or "".
func (k keyPress) base() string {
	switch {
	case k.codepoint > 0:
		return string(rune(k.codepoint))
	case strings.HasPrefix(k.name, "key_"):
		return strings.TrimPrefix(k.name, "key_")
	case strings.HasPrefix(k.name, "digit_"):
		return strings.TrimPrefix(k.name, "digit_")
	}
	return ""
}

// sendKey types a key event into a Herdr pane.
func (b *Bridge) sendKey(ctx context.Context, pane string, raw P) error {
	k, ok := parseKey(raw)
	if !ok {
		return nil
	}
	b.unscroll(ctx, pane)
	if k.text != "" && !k.chord() {
		return b.call(ctx, "pane.send_text", map[string]any{"pane_id": pane, "text": k.text}, nil)
	}
	if raw, ok := rawKeys[k.name]; ok && !k.chord() {
		return b.call(ctx, "pane.send_text", map[string]any{"pane_id": pane, "text": raw}, nil)
	}
	base := herdrKeys[k.name]
	if base == "" && fnKey.MatchString(k.name) {
		base = k.name
	}
	if base == "" {
		base = k.base()
	}
	if base == "" {
		return nil
	}
	var parts []string
	if k.ctrl {
		parts = append(parts, "ctrl")
	}
	if k.alt {
		parts = append(parts, "alt")
	}
	if k.shift {
		parts = append(parts, "shift")
	}
	key := strings.Join(append(parts, base), "+")
	return b.call(ctx, "pane.send_keys", map[string]any{"pane_id": pane, "keys": []string{key}}, nil)
}

// focusedPane finds the pane that has focus in a session's active window,
// or the herdrex shell that has it.
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
			if sh := b.focusedShell(w.ActiveTabID, l.Focused); sh != nil {
				return sh.id, nil
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
	if strings.HasPrefix(pane, shellPrefix) {
		sh := b.shell(pane)
		if sh == nil {
			return nil, errShellGone
		}
		return map[string]any{"handled": false}, shellKey(sh, p.obj("key"))
	}
	err := b.sendKey(ctx, pane, p.obj("key"))
	return map[string]any{"handled": false}, err
}

// A PTY takes the bytes an xterm sends. csiKeys end in their final byte,
// with ESC [ 1 ; <mods> before it when modified; tildeKeys are ESC [ <n> ~,
// or ESC [ <n> ; <mods> ~. f1 to f4 are SS3 keys when unmodified.
var (
	csiKeys = map[string]byte{
		"arrow_up": 'A', "arrow_down": 'B', "arrow_right": 'C', "arrow_left": 'D',
		"home": 'H', "end": 'F', "f1": 'P', "f2": 'Q', "f3": 'R', "f4": 'S',
	}
	tildeKeys = map[string]int{
		"insert": 2, "delete": 3, "page_up": 5, "page_down": 6,
		"f5": 15, "f6": 17, "f7": 18, "f8": 19, "f9": 20, "f10": 21, "f11": 23, "f12": 24,
	}
	plainKeys = map[string]string{
		"enter": "\r", "numpad_enter": "\r", "tab": "\t", "backspace": "\x7f",
		"escape": "\x1b", "space": " ",
	}
)

// bytes is what the key types into a PTY, or nil.
func (k keyPress) bytes() []byte {
	if k.text != "" && !k.chord() {
		return []byte(k.text)
	}
	mods := 1 // xterm's modifier parameter
	if k.shift {
		mods += 1
	}
	if k.alt {
		mods += 2
	}
	if k.ctrl {
		mods += 4
	}
	if f, ok := csiKeys[k.name]; ok {
		switch {
		case mods > 1:
			return fmt.Appendf(nil, "\x1b[1;%d%c", mods, f)
		case f >= 'P':
			return []byte{0x1b, 'O', f}
		}
		return []byte{0x1b, '[', f}
	}
	if n, ok := tildeKeys[k.name]; ok {
		if mods > 1 {
			return fmt.Appendf(nil, "\x1b[%d;%d~", n, mods)
		}
		return fmt.Appendf(nil, "\x1b[%d~", n)
	}
	var out []byte
	switch s, plain := plainKeys[k.name]; {
	case k.name == "tab" && k.shift:
		return []byte("\x1b[Z")
	case k.name == "space" && k.ctrl:
		out = []byte{0}
	case plain:
		out = []byte(s)
	default:
		c := k.text
		if c == "" || k.ctrl {
			c = k.base()
		}
		if c == "" {
			return nil
		}
		if k.ctrl && len(c) == 1 {
			switch {
			case c[0] >= 'a' && c[0] <= 'z', c[0] >= '@' && c[0] <= '_':
				c = string(c[0] & 0x1f)
			case c[0] == '?':
				c = "\x7f"
			}
		}
		out = []byte(c)
	}
	if k.alt {
		out = append([]byte{0x1b}, out...)
	}
	return out
}
