package bridge

import (
	"bytes"
	"fmt"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/elonnzhang/herdrex/pkg/herdr/term"
)

// A shell in a PTY echoes what it is sent, sees the size it is given, and is
// gone once closed; every stream hears of the end.
func TestShellBlock(t *testing.T) {
	t.Setenv("SHELL", "/bin/sh")
	t.Setenv("HOME", t.TempDir())
	t.Setenv("ENV", "")
	exits := make(chan *shellBlock, 1)
	sh := &shellBlock{id: shellPrefix + "test"}
	if err := sh.start(t.TempDir(), term.Size{Cols: 80, Rows: 24, CellWidthPx: 8, CellHeightPx: 16}, func(s *shellBlock) { exits <- s }); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(sh.close)
	msgs := make(chan shellMsg, 1024)
	go func() {
		defer close(msgs)
		for m := range sh.messages(t.Context()) {
			msgs <- m
		}
	}()
	var out strings.Builder
	await := func(what string, ok func(shellMsg) bool) {
		t.Helper()
		timeout := time.After(5 * time.Second)
		for {
			select {
			case m, open := <-msgs:
				if !open {
					t.Fatalf("stream ended before %s; output %q", what, out.String())
				}
				out.Write(m.data)
				if ok(m) {
					return
				}
			case <-timeout:
				t.Fatalf("no %s; output %q", what, out.String())
			}
		}
	}
	shown := func(text string) func(shellMsg) bool {
		return func(shellMsg) bool { return strings.Contains(out.String(), text) }
	}
	await("first size", func(m shellMsg) bool { return m.size != nil && m.size.Cols == 80 })
	sh.write([]byte("echo a$((20+1))\r"))
	await("echo output", shown("a21"))
	if !sh.resize(term.Size{Cols: 90, Rows: 20, CellWidthPx: 8, CellHeightPx: 16}) {
		t.Fatal("resize changed nothing")
	}
	await("new size", func(m shellMsg) bool { return m.size != nil && m.size.Cols == 90 })
	sh.write([]byte("stty size\r"))
	await("stty size", shown("20 90"))
	sh.close()
	await("end", func(m shellMsg) bool { return m.end == endClosed })
	select {
	case <-exits:
	case <-time.After(5 * time.Second):
		t.Fatal("exit never ran")
	}
	if err := syscall.Kill(sh.pid, 0); err == nil {
		t.Errorf("process %d still runs", sh.pid)
	}
}

func TestRing(t *testing.T) {
	r := ring{max: 8}
	var all []byte
	for i := range 20 {
		p := []byte(fmt.Sprintf("%d", i))
		r.write(p)
		all = append(all, p...)
		if want := all[max(0, len(all)-8):]; !bytes.Equal(r.bytes(), want) {
			t.Fatalf("after %q: ring %q, want %q", all, r.bytes(), want)
		}
	}
	r.write([]byte("0123456789abc"))
	if got := string(r.bytes()); got != "56789abc" {
		t.Errorf("after a long write: %q", got)
	}
}

func TestKeyBytes(t *testing.T) {
	for _, tc := range []struct {
		key  map[string]any
		want string
	}{
		{map[string]any{"key": "key_a", "text": "a"}, "a"},
		{map[string]any{"key": "key_l", "mods": "alt", "consumed_mods": "alt", "text": "@", "unshifted_codepoint": float64('l')}, "@"},
		{map[string]any{"key": "key_c", "mods": "ctrl", "text": "c", "unshifted_codepoint": float64('c')}, "\x03"},
		{map[string]any{"key": "key_x", "mods": "alt", "text": "x", "unshifted_codepoint": float64('x')}, "\x1bx"},
		{map[string]any{"key": "bracket_left", "mods": "ctrl", "unshifted_codepoint": float64('[')}, "\x1b"},
		{map[string]any{"key": "enter"}, "\r"},
		{map[string]any{"key": "backspace"}, "\x7f"},
		{map[string]any{"key": "tab", "mods": "shift"}, "\x1b[Z"},
		{map[string]any{"key": "arrow_up"}, "\x1b[A"},
		{map[string]any{"key": "arrow_left", "mods": "ctrl"}, "\x1b[1;5D"},
		{map[string]any{"key": "f1"}, "\x1bOP"},
		{map[string]any{"key": "f5", "mods": "shift"}, "\x1b[15;2~"},
		{map[string]any{"key": "delete"}, "\x1b[3~"},
		{map[string]any{"key": "key_a", "action": "release", "text": "a"}, ""},
		{map[string]any{"key": "quote", "text": "´", "composing": true}, ""},
	} {
		k, ok := parseKey(P(tc.key))
		got := ""
		if ok {
			got = string(k.bytes())
		}
		if got != tc.want {
			t.Errorf("%v types %q, want %q", tc.key, got, tc.want)
		}
	}
}
