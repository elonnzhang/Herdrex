package bridge

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/elonnzhang/herdrex/pkg/rex"
	"github.com/elonnzhang/herdrex/pkg/rex/client"
)

// mouseHerdr is a fake Herdr whose pane w1:p1 shows scrollback row 90 at the
// top, 10 rows above the bottom, and whose selection reads as "hello".
func mouseHerdr(t *testing.T) (*recordHerdr, *Bridge, *client.Conn) {
	f := newRecordHerdr(t, func(c herdrCall) string {
		switch c.Method {
		case "pane.get":
			return `{"type":"pane_info","pane":{"pane_id":"w1:p1","workspace_id":"w1","tab_id":"w1:t1","scroll":{"offset_from_bottom":10,"max_offset_from_bottom":100}}}`
		case "pane.selection.read":
			return `{"type":"pane_selection","text":"hello"}`
		}
		return ""
	})
	b, conn := f.connect(t)
	return f, b, conn
}

// event sends a block.event to w1:p1.
func event(t *testing.T, b *Bridge, conn *client.Conn, typ string, payload map[string]any) error {
	t.Helper()
	return conn.Call(t.Context(), "block.event", map[string]any{
		"session_id": b.IDs.Rex("session", "w1"),
		"block_id":   b.IDs.Rex("block", "w1:p1"),
		"event":      map[string]any{"type": typ, "payload": payload},
	}, nil)
}

// describe turns Herdr calls into short strings, such as "scroll 9".
func describe(calls []herdrCall) []string {
	var out []string
	for _, c := range calls {
		switch c.Method {
		case "pane.scroll":
			out = append(out, fmt.Sprintf("scroll %v", c.Params["offset_from_bottom"]))
		case "pane.selection.read":
			a, z := c.Params["anchor"].(map[string]any), c.Params["cursor"].(map[string]any)
			out = append(out, fmt.Sprintf("select %v,%v-%v,%v", a["row"], a["col"], z["row"], z["col"]))
		default:
			out = append(out, c.Method)
		}
	}
	return out
}

func badRequest(err error) bool {
	var re *rex.Error
	return errors.As(err, &re) && re.Code == rex.CodeBadRequest
}

// Without a program that reports the mouse, a wheel moves Herdr's
// scrollback by whole lines. Pixels count 16 to a line and pages 24, and
// what does not make a line waits for the next event; sideways goes nowhere.
func TestWheelScrollsBack(t *testing.T) {
	f, b, conn := mouseHerdr(t)
	for _, tc := range []struct {
		name  string
		wheel map[string]any
		want  string
	}{
		{"half a line of pixels", map[string]any{"delta_y": 8, "delta_mode": "pixel"}, "[]"},
		{"the other half", map[string]any{"delta_y": 8}, "[scroll 9]"},
		{"three lines up", map[string]any{"delta_y": -3, "delta_mode": "line"}, "[scroll 13]"},
		{"a page down stops at the bottom", map[string]any{"delta_y": 1, "delta_mode": "page"}, "[scroll 0]"},
		{"sideways", map[string]any{"delta_x": 40, "delta_mode": "line"}, "[]"},
	} {
		if err := event(t, b, conn, "wheel", tc.wheel); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if got := fmt.Sprint(describe(f.inputs(0))); got != tc.want {
			t.Errorf("%s: Herdr got %s, want %s", tc.name, got, tc.want)
		}
	}
	for name, wheel := range map[string]map[string]any{
		"an unknown mode": {"delta_y": 1, "delta_mode": "furlong"},
		"a string delta":  {"delta_y": "1"},
		"an object for x": {"delta_y": 1, "x": map[string]any{}},
	} {
		if err := event(t, b, conn, "wheel", wheel); !badRequest(err) {
			t.Errorf("%s: %v, want bad_request", name, err)
		}
	}
}

// Without a program that reports the mouse, a left-button drag selects the
// text it covered, counting rows from the top of the scrollback, and sends
// selection_copied. Forward it holds both end cells; backward it leaves out
// the cell where it started. A click and the other buttons select nothing.
func TestDragSelects(t *testing.T) {
	f, b, conn := mouseHerdr(t)
	if err := conn.Call(t.Context(), "session.attach", map[string]any{"session_id": b.IDs.Rex("session", "w1")}, nil); err != nil {
		t.Fatal(err)
	}
	if err := b.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	drag := func(from, to [2]int, button string) {
		t.Helper()
		for _, m := range []map[string]any{
			{"action": "press", "button": button, "x": from[0], "y": from[1]},
			{"action": "motion", "buttons": map[string]any{button: true}, "x": to[0], "y": to[1]},
			{"action": "release", "button": button, "x": to[0], "y": to[1]},
		} {
			if err := event(t, b, conn, "mouse", m); err != nil {
				t.Fatal(err)
			}
		}
	}
	click := func(button string) {
		t.Helper()
		for _, a := range []string{"press", "release"} {
			if err := event(t, b, conn, "mouse", map[string]any{"action": a, "button": button, "x": 2, "y": 1}); err != nil {
				t.Fatal(err)
			}
		}
	}

	drag([2]int{2, 1}, [2]int{5, 3}, "left")
	if got, want := fmt.Sprint(describe(f.inputs(0))), "[select 91,2-93,5]"; got != want {
		t.Errorf("forward drag: Herdr got %s, want %s", got, want)
	}
	copied := selections(t, conn)
	if len(copied) != 1 || copied[0] != `{"bytes":5,"text":"hello"}` {
		t.Errorf("forward drag sent %v", copied)
	}
	drag([2]int{5, 3}, [2]int{2, 1}, "left")
	if got, want := fmt.Sprint(describe(f.inputs(0))), "[select 91,2-93,4]"; got != want {
		t.Errorf("backward drag: Herdr got %s, want %s", got, want)
	}
	click("left")
	drag([2]int{2, 1}, [2]int{5, 3}, "right")
	click("eight")
	if got := describe(f.inputs(0)); len(got) != 0 {
		t.Errorf("a click and other buttons sent Herdr %v", got)
	}
	if err := event(t, b, conn, "mouse", map[string]any{"action": "hover"}); !badRequest(err) {
		t.Errorf("an unknown action: %v, want bad_request", err)
	}
}

// selections returns the payloads of the selection_copied events that arrive
// within a short wait.
func selections(t *testing.T, conn *client.Conn) []string {
	t.Helper()
	var out []string
	deadline := time.After(300 * time.Millisecond)
	for {
		select {
		case e := <-conn.Events():
			var se rex.SessionEvent
			var be struct {
				Name    string          `json:"name"`
				Payload json.RawMessage `json:"payload"`
			}
			if e.Type != "session_event" || json.Unmarshal(e.Payload, &se) != nil ||
				se.Event.Type != "block_event" || json.Unmarshal(se.Event.Payload, &be) != nil || be.Name != "selection_copied" {
				continue
			}
			out = append(out, string(be.Payload))
		case <-deadline:
			return out
		}
	}
}
