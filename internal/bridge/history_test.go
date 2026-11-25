package bridge

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/elonnzhang/herdrex/pkg/rex/client"
	"github.com/elonnzhang/herdrex/pkg/rex/termstream"
)

// fakeScreen models what Herdr 0.9.5 reads from a pane on the primary
// screen: rows are every physical row, scrollback first, "" when blank.
// pane.read formats rows as Ghostty's VT formatter does, one "\r\n" between
// rows and trailing blank rows dropped; recent rows end at the cursor or the
// last viewport row with text (src/pane/terminal.rs,
// ghostty_recent_read_range).
type fakeScreen struct {
	rows   []string
	height int
	cursor int // viewport row
	offset int // rows another client scrolled the pane back
}

func (sc *fakeScreen) top() int { return max(len(sc.rows)-sc.height, 0) }

func formatted(rows []string) string {
	rows = slices.Clone(rows)
	for len(rows) > 0 && strings.TrimSpace(rows[len(rows)-1]) == "" {
		rows = rows[:len(rows)-1]
	}
	return strings.Join(rows, "\r\n")
}

func (sc *fakeScreen) recent(lines int) (string, bool) {
	end := len(sc.rows) - 1
	for r := end; r >= sc.top(); r-- {
		if strings.TrimSpace(sc.rows[r]) != "" {
			end = max(r, sc.top()+sc.cursor)
			break
		}
	}
	return formatted(sc.rows[max(end+1-lines, 0) : end+1]), len(sc.rows) > lines
}

// screenResult answers pane.get and pane.read for a pane with a screen.
func (s *fakeState) screenResult(method string, p map[string]any) (any, bool) {
	id, _ := p["pane_id"].(string)
	sc := s.screens[id]
	if sc == nil {
		return nil, false
	}
	switch method {
	case "pane.get":
		for _, x := range s.snap.Panes {
			if x.ID == id {
				raw, _ := json.Marshal(x)
				var pane map[string]any
				json.Unmarshal(raw, &pane)
				pane["scroll"] = map[string]any{"offset_from_bottom": sc.offset, "max_offset_from_bottom": sc.top(), "viewport_rows": sc.height}
				return map[string]any{"type": "pane_info", "pane": pane}, true
			}
		}
	case "pane.scroll":
		s.scrolls++
		return map[string]any{"type": "ok"}, true
	case "pane.read":
		s.reads++
		var text string
		truncated := false
		if p["source"] == "visible" {
			shown := sc.top() - sc.offset
			text = formatted(sc.rows[shown : shown+sc.height])
		} else {
			lines, _ := p["lines"].(float64)
			text, truncated = sc.recent(min(int(lines), 1000))
		}
		return map[string]any{"type": "pane_read", "read": map[string]any{"pane_id": id, "text": text, "truncated": truncated}}, true
	}
	return nil, false
}

// subscribeScroll holds a pane.scroll_changed subscription open, sending an
// event for every scrolled call.
func (f *fakeHerdr) subscribeScroll(c net.Conn, id string, p map[string]any) bool {
	subs, _ := p["subscriptions"].([]any)
	if len(subs) != 1 {
		return false
	}
	if m, _ := subs[0].(map[string]any); m["type"] != "pane.scroll_changed" {
		return false
	}
	ch := make(chan struct{}, 8)
	f.mu.Lock()
	f.scrollCh = append(f.scrollCh, ch)
	f.mu.Unlock()
	fmt.Fprintf(c, `{"id":%q,"result":{"type":"subscription_started"}}`+"\n", id)
	gone := make(chan struct{})
	go func() { // the bridge closes the connection when the hub ends
		io.Copy(io.Discard, c)
		close(gone)
	}()
	for {
		select {
		case <-gone:
			return true
		case <-ch:
			if _, err := c.Write([]byte(`{"event":"pane.scroll_changed","data":{}}` + "\n")); err != nil {
				return true
			}
		}
	}
}

func (f *fakeHerdr) scrolled() {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, ch := range f.scrollCh {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

func numbered(prefix string, from, to int) []string {
	var rows []string
	for i := from; i <= to; i++ {
		rows = append(rows, fmt.Sprintf("%s%02d", prefix, i))
	}
	return rows
}

// historyOf reads s until a history write ends, and returns the rows it
// wrote and whether it erased the scrollback first.
func historyOf(t *testing.T, s *client.Stream) ([]string, bool) {
	t.Helper()
	var out []byte
	readUntil(t, s, func(p *termstream.Part, _ error) bool {
		if p == nil {
			return false
		}
		out = append(out, p.Output...)
		i := bytes.Index(out, []byte("\x1b[?2026h\x1b[0m\x1b[r"))
		return i >= 0 && bytes.Contains(out[i:], []byte("hi")) // the full frame follows
	})
	out = out[bytes.Index(out, []byte("\x1b[?2026h\x1b[0m\x1b[r")):]
	reset := bytes.HasPrefix(out, []byte("\x1b[?2026h\x1b[0m\x1b[r\x1b[3J"))
	i := bytes.Index(out, []byte("\x1b[H\x1b[J"))
	if i < 0 {
		return nil, reset
	}
	body, _, _ := bytes.Cut(out[i+6:], []byte("\x1b[0m\n"))
	return strings.Split(string(body), "\x1b[0m\r\n"), reset
}

// A plain shell stream gets the rows above Herdr's screen in its scrollback,
// then only rows that scroll off later; a stream that joins later gets them
// all; a cleared history replaces the client's.
func TestShellHistory(t *testing.T) {
	f, b, conn, sid := hubSetup(t)
	block := b.IDs.Rex("block", "w1:p1")
	// The cursor sits on a blank row below the prompt, so Herdr's recent rows
	// end above the screen's last row. A blank row sits in the history.
	rows := append(numbered("row", 1, 10), "")
	rows = append(rows, numbered("row", 12, 40)...)
	f.edit(func(s *fakeState) {
		s.screens["w1:p1"] = &fakeScreen{rows: append(rows, "$ ls", ""), height: 24, cursor: 23}
	})
	stream := openTerm(t, conn, sid, block)
	got, reset := historyOf(t, stream)
	want := append(numbered("row", 1, 10), "")
	want = append(want, numbered("row", 12, 18)...) // 42 rows, 24 on screen
	if !slices.Equal(got, want) || reset {
		t.Fatalf("first history = %q (reset %v), want %q", got, reset, want)
	}

	f.edit(func(s *fakeState) {
		sc := s.screens["w1:p1"]
		sc.rows = append(append(sc.rows[:len(sc.rows)-2], numbered("out", 1, 5)...), "$ ", "")
	})
	f.scrolled()
	got, reset = historyOf(t, stream)
	if want := []string{"row19", "row20", "row21", "row22", "row23"}; !slices.Equal(got, want) || reset {
		t.Fatalf("new history = %q (reset %v), want %q", got, reset, want)
	}

	second := openTerm(t, attached(t, b, sid), sid, block)
	got, _ = historyOf(t, second)
	want = append(want[:0], numbered("row", 1, 10)...)
	want = append(append(want, ""), numbered("row", 12, 23)...)
	if !slices.Equal(got, want) {
		t.Fatalf("joining stream's history = %q, want %q", got, want)
	}

	f.edit(func(s *fakeState) {
		s.screens["w1:p1"].rows = append(numbered("new", 1, 30), "$ ", "")
	})
	f.scrolled()
	got, reset = historyOf(t, stream)
	if want := numbered("new", 1, 8); !slices.Equal(got, want) || !reset {
		t.Fatalf("history after clear = %q (reset %v), want %q with reset", got, reset, want)
	}

	// An alternate screen, like a clear, leaves Herdr no history: the
	// client erases its own. After the program the history comes back.
	saved := f.screen("w1:p1").rows
	f.edit(func(s *fakeState) { s.screens["w1:p1"].rows = append(numbered("tui", 1, 23), "") })
	f.scrolled()
	if got, reset = historyOf(t, stream); got != nil || !reset {
		t.Fatalf("history on the alternate screen = %q (reset %v), want only a reset", got, reset)
	}
	f.edit(func(s *fakeState) { s.screens["w1:p1"].rows = saved })
	f.scrolled()
	if got, _ = historyOf(t, stream); !slices.Equal(got, numbered("new", 1, 8)) {
		t.Fatalf("history after the program = %q", got)
	}

	// A clear through the bridge erases the client's history at once, and a
	// stream joining then gets none.
	f.edit(func(s *fakeState) { s.screens["w1:p1"].rows = []string{"$ "} })
	call(t, conn, "block.method", P{"session_id": sid, "block_id": block, "method": "clear"}, nil)
	if got, reset = historyOf(t, stream); got != nil || !reset {
		t.Fatalf("history after block clear = %q (reset %v), want only a reset", got, reset)
	}
	h := b.hub("w1:p1")
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.hist.tail) != 0 {
		t.Fatalf("hub kept %d rows after a clear", len(h.hist.tail))
	}
}

// A read under way when the bridge clears the pane does not bring the
// cleared rows back, for the streams or for one that joins later.
func TestClearDuringRead(t *testing.T) {
	f, b, conn, sid := hubSetup(t)
	block := b.IDs.Rex("block", "w1:p1")
	f.edit(func(s *fakeState) {
		s.screens["w1:p1"] = &fakeScreen{rows: append(numbered("row", 1, 30), "$ "), height: 24, cursor: 23}
	})
	stream := openTerm(t, conn, sid, block)
	historyOf(t, stream)
	hold := make(chan struct{})
	f.mu.Lock()
	f.readHold = hold
	f.mu.Unlock()
	f.edit(func(s *fakeState) {
		sc := s.screens["w1:p1"]
		sc.rows = append(append(sc.rows[:len(sc.rows)-1], numbered("secret", 1, 30)...), "$ ")
	})
	f.scrolled()
	select {
	case <-hold: // the read holding the secret rows is answered, not yet returned
	case <-time.After(5 * time.Second):
		t.Fatal("no history read")
	}
	f.edit(func(s *fakeState) { s.screens["w1:p1"].rows = []string{"$ "} })
	call(t, conn, "block.method", P{"session_id": sid, "block_id": block, "method": "clear"}, nil)
	if got, reset := historyOf(t, stream); got != nil || !reset {
		t.Fatalf("history after clear = %q (reset %v), want only a reset", got, reset)
	}
	hold <- struct{}{}
	h := b.hub("w1:p1")
	tail := func() []string {
		h.mu.Lock()
		defer h.mu.Unlock()
		return slices.Clone(h.hist.tail)
	}
	// The held read ends at once; the read the clear asked for waits
	// historyGap after it, and would erase what the held one left.
	time.Sleep(historyGap / 4)
	if got := tail(); len(got) != 0 {
		t.Fatalf("hub kept %q after a clear", got)
	}
	time.Sleep(3 * historyGap)
	if got := tail(); len(got) != 0 {
		t.Fatalf("hub kept %q after a clear", got)
	}
	late := openTerm(t, attached(t, b, sid), sid, block)
	var out []byte
	readUntil(t, late, func(p *termstream.Part, _ error) bool {
		if p != nil {
			out = append(out, p.Output...)
		}
		return bytes.Contains(out, []byte("hi"))
	})
	if bytes.Contains(out, []byte("secret")) {
		t.Fatalf("a stream joining after the clear got cleared rows: %q", out)
	}
}

func (f *fakeHerdr) screen(pane string) *fakeScreen {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.st.screens[pane]
}

// A history longer than one read is placed by the visible screen, also
// while another client keeps the pane scrolled back; nothing scrolls it for
// the stream.
func TestLongShellHistory(t *testing.T) {
	f, b, conn, sid := hubSetup(t)
	rows := append(numbered("r", 1001, 2200), numbered("s", 1, 23)...)
	f.edit(func(s *fakeState) {
		s.screens["w1:p1"] = &fakeScreen{rows: append(slices.Clone(rows), "$ "), height: 24, cursor: 23}
		s.screens["w1:p2"] = &fakeScreen{rows: append(slices.Clone(rows), "$ "), height: 24, cursor: 23, offset: 10}
	})
	stream := openTerm(t, conn, sid, b.IDs.Rex("block", "w1:p1"))
	got, _ := historyOf(t, stream)
	// 1224 rows, 24 on screen: the read of 1000 starts at row 224.
	if want := numbered("r", 1225, 2200); !slices.Equal(got, want) {
		t.Fatalf("first history: %d rows %q..%q, want %d rows %q..%q", len(got), got[0], got[len(got)-1], len(want), want[0], want[len(want)-1])
	}
	f.edit(func(s *fakeState) {
		sc := s.screens["w1:p1"]
		sc.rows = append(append(sc.rows[:len(sc.rows)-1], numbered("t", 1, 3)...), "$ ")
	})
	f.scrolled()
	if got, reset := historyOf(t, stream); !slices.Equal(got, []string{"s01", "s02", "s03"}) || reset {
		t.Fatalf("new history = %q (reset %v)", got, reset)
	}

	scrolled := openTerm(t, conn, sid, b.IDs.Rex("block", "w1:p2"))
	got, _ = historyOf(t, scrolled)
	if want := numbered("r", 1225, 2190); !slices.Equal(got, want) {
		t.Fatalf("history of a scrolled pane: %d rows ending %q, want %d ending %q", len(got), got[len(got)-1], len(want), want[len(want)-1])
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.st.scrolls != 0 {
		t.Fatal("opening a stream scrolled the pane")
	}
}

// Without scrollback, as on the alternate screen, the hub reads nothing.
func TestNoHistoryNoRead(t *testing.T) {
	f, b, conn, sid := hubSetup(t)
	f.edit(func(s *fakeState) {
		s.screens["w1:p1"] = &fakeScreen{rows: append(numbered("tui", 1, 23), ""), height: 24}
	})
	stream := openTerm(t, conn, sid, b.IDs.Rex("block", "w1:p1"))
	readUntil(t, stream, shows("hi"))
	f.scrolled()
	time.Sleep(3 * historyGap)
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.st.reads != 0 {
		t.Fatalf("pane.read called %d times for a pane without history", f.st.reads)
	}
}

func TestSplitHistory(t *testing.T) {
	visible := "a\r\n\r\n\x1b[1mb\x1b[0m\r\n   "
	split := func(recent, visible string) ([]string, bool) {
		return splitHistory(trimBlankRows(formattedRows(recent)), trimBlankRows(formattedRows(visible)), 1000, 2000, 0, 24)
	}
	for _, c := range []struct {
		recent string
		want   []string
		ok     bool
	}{
		{"h1\r\n\r\nh3\r\na\r\n\r\nb", []string{"h1", "", "h3"}, true},
		{"\x1b[31mh1\x1b[0m\r\na\r\n\r\nb\r\n", []string{"\x1b[31mh1\x1b[0m"}, true},
		{"a\r\n\r\nb", nil, true},
		{"h1\r\na\r\n\r\nc", nil, false}, // output came between the reads
		{"b", nil, false},
	} {
		got, ok := split(c.recent, visible)
		if ok != c.ok || !slices.Equal(got, c.want) {
			t.Errorf("splitHistory(%q) = %q, %v; want %q, %v", c.recent, got, ok, c.want, c.ok)
		}
	}
	if _, ok := split("h\r\n", "\r\n "); ok {
		t.Error("a blank screen gave a boundary")
	}
	// Scrolled back 2 rows on a 3-row screen at row 10: the read of 6 rows
	// ends at the cursor, row 11, so it covers rows 6 to 11 and the shown
	// rows 8 to 10 start at index 2.
	all := []string{"r6", "r7", "r8", "r9", "r10", "r11"}
	if got, ok := splitHistory(all, []string{"r8", "r9", "r10"}, 6, 10, 2, 3); !ok || !slices.Equal(got, []string{"r6", "r7"}) {
		t.Errorf("scrolled split = %q, %v", got, ok)
	}
}

func TestFindNew(t *testing.T) {
	rows := []string{"$ ls", "a", "$ ls", "a", "$ ls", "a"}
	anchor := []string{"$ ls", "a"}
	if got := findNew(rows, anchor, 4); got != 2 {
		t.Errorf("with growth 4: %d, want 2", got)
	}
	if got := findNew(rows, anchor, -30); got != 6 {
		t.Errorf("after a prune: %d, want the last match, 6", got)
	}
	if got := findNew(rows, []string{"x"}, 2); got != -1 {
		t.Errorf("missing anchor: %d", got)
	}
	tail := []string{"text", "", "", "", "", "", "", "", "", ""}
	if got := anchorRows(tail); len(got) != len(tail) {
		t.Errorf("anchorRows of blank rows = %q, want back to the row with text", got)
	}
	if got := anchorRows(make([]string, historyLines)); len(got) != historyAnchor {
		t.Errorf("anchorRows of a blank tail has %d rows, want %d", len(got), historyAnchor)
	}
}

func TestSanitizeRow(t *testing.T) {
	in := "\x1b[1;38:2::1:2:3ma\x1b]52;c;ZXZpbA==\x07b\x1b]8;;http://x\x1b\\c\x1b[2Jd\x07\u009be\x1b(Bf\x1b[?25l"
	if got, want := sanitizeRow(in), "\x1b[1;38:2::1:2:3mabcdef"; got != want {
		t.Errorf("sanitizeRow = %q, want %q", got, want)
	}
	if got := plainRow("\x1b[31mx  \x1b[0m  "); got != "x" {
		t.Errorf("plainRow = %q", got)
	}
}

func TestHistoryPushBytes(t *testing.T) {
	p := historyPush{reset: true, rows: []string{"a", "b"}}
	want := "\x1b[?2026h\x1b[0m\x1b[r\x1b[3J\x1b[H\x1b[Ja\x1b[0m\r\nb\x1b[0m\n\n\n"
	if got := string(p.bytes(3)); got != want {
		t.Errorf("bytes = %q, want %q", got, want)
	}
	var q historyPush
	q.add(historyPush{rows: []string{"x"}})
	q.add(historyPush{reset: true, rows: []string{"y"}})
	q.add(historyPush{rows: []string{"z"}})
	if !q.reset || !slices.Equal(q.rows, []string{"y", "z"}) {
		t.Errorf("add = %+v", q)
	}
}
