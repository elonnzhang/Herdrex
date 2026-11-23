package bridge

import (
	"fmt"
	"testing"
)

// client.key reaches Herdr as typed text or as a named chord. Modifiers in
// consumed_mods already made the text, so they are no chord, and a key in
// the middle of a composition types nothing until the composition ends.
func TestClientKey(t *testing.T) {
	f := newRecordHerdr(t, nil)
	b, conn := f.connect(t)
	block := b.IDs.Rex("block", "w1:p1")
	key := func(name, mods, consumed, text string, cp int, composing bool) map[string]any {
		return map[string]any{
			"key": name, "mods": mods, "consumed_mods": consumed, "text": text,
			"unshifted_codepoint": cp, "composing": composing, "action": "press",
		}
	}
	for _, tc := range []struct {
		name string
		key  map[string]any
		want string
	}{
		{"German Option+L types @", key("key_l", "alt", "alt", "@", 'l', false), `pane.send_text "@"`},
		{"shift makes a capital", key("key_a", "shift", "shift", "A", 'a', false), `pane.send_text "A"`},
		{"Option as Meta is a chord", key("key_l", "alt", "", "l", 'l', false), `pane.send_keys ["alt+l"]`},
		{"ctrl is a chord", key("key_c", "ctrl", "", "c", 'c', false), `pane.send_keys ["ctrl+c"]`},
		{"dead key with Option", key("key_e", "alt", "alt", "´", 'e', true), ``},
		{"dead key without mods", key("quote", "", "", "´", '\'', true), ``},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := conn.Call(t.Context(), "client.key", map[string]any{"block_id": block, "key": tc.key}, nil); err != nil {
				t.Fatal(err)
			}
			got := ""
			for _, c := range f.inputs(0) {
				switch c.Method {
				case "pane.send_text":
					got += fmt.Sprintf("%s %q", c.Method, c.Params["text"])
				case "pane.send_keys":
					got += fmt.Sprintf("%s %q", c.Method, c.Params["keys"])
				default:
					got += c.Method
				}
			}
			if got != tc.want {
				t.Errorf("Herdr got %s, want %s", got, tc.want)
			}
		})
	}
}
