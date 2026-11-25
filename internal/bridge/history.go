package bridge

import (
	"context"
	"errors"
	"log"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/elonnzhang/herdrex/pkg/herdr/jsonapi"
	"github.com/elonnzhang/herdrex/pkg/herdr/term"
)

// Herdr's frames repaint the visible screen cell by cell and never scroll it
// (src/protocol/render_ansi.rs:673), so the client's emulator keeps no
// scrollback of its own. Each hub copies the rows that scroll off Herdr's
// screen into its streams, so a plain shell scrolls and selects natively.
//
// A read takes pane.get for the scroll position, then pane.read of the
// recent rows as ANSI, which Herdr formats with Ghostty's formatter: one
// physical row per "\r\n", blank rows inside kept, trailing blank rows
// dropped (vendor/libghostty-vt/src/terminal/formatter.zig:89, 1216). When
// the read holds every row it starts at row 0 and pane.get's numbers place
// the boundary. Otherwise it ends at the cursor or the last row with text,
// whichever is lower (src/pane/terminal.rs, ghostty_recent_read_range), and a
// pane.read of the visible rows places it: their text must match the read
// there, which also rejects output that came between the reads.

const (
	historyLines     = 1000 // pane.read's cap (src/app/api_helpers.rs:117) and the rows a hub keeps
	historyAnchor    = 8    // pushed rows that must match to find where new rows start
	historyAnchorMax = 64   // the most rows an anchor reaches back for one with text
	historyGap       = 200 * time.Millisecond
	// historyIdle is how often frames alone cause a read. At Herdr's
	// scrollback cap old rows drop as new ones come, and the
	// pane.scroll_changed numbers can end where they started.
	historyIdle = 2 * time.Second
)

// historyPush is history a stream writes into its client just before a
// full frame.
type historyPush struct {
	reset bool     // erase the client's scrollback first
	rows  []string // sanitized physical rows, oldest first
}

func (p *historyPush) add(q historyPush) {
	if q.reset {
		*p = historyPush{reset: true}
	}
	p.rows = lastRows(append(p.rows, q.rows...))
}

func lastRows(rows []string) []string { return rows[max(len(rows)-historyLines, 0):] }

// bytes writes the rows at the top of a cleared screen and line-feeds the
// screen's height from the bottom, which pushes exactly the written rows
// into scrollback: feeds move the cursor down to the last row and then scroll
// one row each. The full frame that follows repaints the screen.
// Synchronized output, which that frame ends, hides the cleared screen.
func (p *historyPush) bytes(screenRows int) []byte {
	var b strings.Builder
	b.WriteString("\x1b[?2026h\x1b[0m\x1b[r")
	if p.reset {
		b.WriteString("\x1b[3J")
	}
	if len(p.rows) > 0 {
		b.WriteString("\x1b[H\x1b[J")
		for i, r := range p.rows {
			if i > 0 {
				b.WriteString("\x1b[0m\r\n")
			}
			b.WriteString(r)
		}
		b.WriteString("\x1b[0m")
		b.WriteString(strings.Repeat("\n", max(screenRows, 1)))
	}
	return []byte(b.String())
}

// keepHistory reads new history until the hub ends. Reads follow
// pane.scroll_changed, a stream joining before the first read, and frames
// when no read came for historyIdle, never closer than historyGap.
func (h *hub) keepHistory() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		<-h.done
		cancel()
	}()
	scrolled := make(chan struct{}, 1)
	go h.watchScroll(ctx, scrolled)
	timer := time.NewTimer(time.Hour)
	timer.Stop()
	var last, due time.Time
	schedule := func(after time.Duration) {
		at := last.Add(after)
		if now := time.Now(); at.Before(now) {
			at = now
		}
		if due.IsZero() || at.Before(due) {
			due = at
			timer.Reset(time.Until(at))
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-h.kick:
			schedule(historyGap)
		case <-scrolled:
			schedule(historyGap)
		case <-h.active:
			schedule(historyIdle)
		case <-timer.C:
			due = time.Time{}
			h.syncHistory(ctx)
			last = time.Now()
		}
	}
}

// watchScroll signals scrolled once subscribed, since Herdr's first sample
// sends nothing (src/api/subscriptions.rs:238), and on every change.
func (h *hub) watchScroll(ctx context.Context, scrolled chan<- struct{}) {
	signal := func() {
		select {
		case scrolled <- struct{}{}:
		default:
		}
	}
	backoff := 100 * time.Millisecond
	for ctx.Err() == nil {
		events, err := h.b.H.SubscribeTo(ctx, []map[string]any{{"type": "pane.scroll_changed", "pane_id": h.pane}})
		if err == nil {
			backoff = 100 * time.Millisecond
			signal()
			for _, err = range events {
				if err != nil {
					break
				}
				signal()
			}
		}
		if ctx.Err() != nil {
			return
		}
		if traceFrames {
			log.Printf("terminal %s: scroll subscription: %v", h.pane, err)
		}
		select {
		case <-ctx.Done():
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, 5*time.Second)
	}
}

// kickHistory asks for a read soon.
func (h *hub) kickHistory() {
	select {
	case h.kick <- struct{}{}:
	default:
	}
}

// historyState is what the hub pushed so far.
type historyState struct {
	tail []string // the rows pushed, oldest first, at most historyLines
	cols uint16   // the width they were read at; 0 before the first read
	rows uint16   // the screen height they were read at
	top  int      // the first row Herdr showed then, counted from the top of its scrollback
	// epoch counts the bridge's clears. A read begun before one must not
	// bring the cleared rows back.
	epoch uint64
}

// syncHistory reads Herdr's history and queues what the streams lack: the
// rows above the ones Herdr shows, which are the rows above its screen
// unless another client scrolled the pane back.
func (h *hub) syncHistory(ctx context.Context) {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	h.mu.Lock()
	st := h.hist
	h.mu.Unlock()
	s, err := h.b.scrollInfo(ctx, h.pane)
	if err != nil || s == nil || s.Offset < 0 || s.Offset > s.Max {
		return
	}
	size := h.current()
	top := s.Max - s.Offset
	if s.Max == 0 {
		// Herdr has no history: it was cleared, or the alternate screen,
		// which keeps none, is up. Either way the client must not keep rows
		// Herdr may have erased. The next read after the program ends pushes
		// the history again.
		if len(st.tail) > 0 {
			h.queueHistory(historyPush{reset: true}, size, top, st.epoch)
		}
		return
	}
	resized := st.cols != 0 && (st.cols != size.Cols || st.rows != size.Rows)
	var anchor []string
	if !resized {
		anchor = anchorRows(st.tail)
	}
	grew := top - st.top
	lines := historyLines
	if len(anchor) > 0 && grew >= 0 {
		lines = min(historyLines, int(size.Rows)+s.Offset+len(anchor)+grew+64)
	}
	rows, err := h.readHistory(ctx, lines, s.Max, s.Offset, int(size.Rows))
	if err != nil {
		return
	}
	start := -1
	if len(anchor) > 0 {
		start = findNew(rows, anchor, grew)
		if start < 0 && lines < historyLines {
			if rows, err = h.readHistory(ctx, historyLines, s.Max, s.Offset, int(size.Rows)); err != nil {
				return
			}
			start = findNew(rows, anchor, grew)
		}
	}
	push := historyPush{rows: rows}
	switch {
	case st.cols == 0:
	case start >= 0:
		push.rows = rows[start:]
	default:
		// A new width reflows Herdr's history, a taller screen takes rows
		// back from it, and a clear or a burst longer than one read leaves
		// the pushed rows out: start the client's history again.
		push.reset = true
	}
	h.queueHistory(push, size, top, st.epoch)
}

// queueHistory records push and gives it to every stream for its next full
// frame, unless the bridge cleared the pane since epoch.
func (h *hub) queueHistory(push historyPush, size term.Size, top int, epoch uint64) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if epoch != h.hist.epoch {
		return
	}
	if push.reset {
		h.hist.tail = nil
	}
	h.hist.tail = lastRows(append(slices.Clip(h.hist.tail), push.rows...))
	h.hist.cols, h.hist.rows, h.hist.top = size.Cols, size.Rows, top
	if !push.reset && len(push.rows) == 0 {
		return
	}
	for sub := range h.subs {
		if sub.history == nil {
			sub.history = &historyPush{}
		}
		sub.history.add(push)
		sub.waitFull = true
	}
	if len(h.subs) > 0 {
		go h.repaint() // Herdr answers with the full frame the rows go before
	}
}

// dropHistory erases the streams' history now, for a clear the bridge made.
func (h *hub) dropHistory() {
	h.mu.Lock()
	h.hist.epoch++
	epoch := h.hist.epoch
	h.mu.Unlock()
	h.queueHistory(historyPush{reset: true}, h.current(), 0, epoch)
	h.kickHistory()
}

// readHistory returns the sanitized rows above the first one Herdr shows,
// among the last lines rows. screen is the row at the top of Herdr's screen,
// and Herdr shows offset rows above it. While the whole history fits in
// lines rows, the read starts at row 0. Otherwise the visible rows place it.
func (h *hub) readHistory(ctx context.Context, lines, screen, offset, height int) ([]string, error) {
	top := screen - offset
	rec, err := h.b.H.PaneRead(ctx, jsonapi.PaneReadParams{PaneID: h.pane, Source: "recent", Format: "ansi", Lines: uint32(lines)})
	if err != nil {
		return nil, err
	}
	all := trimBlankRows(formattedRows(rec.Text))
	if !rec.Truncated {
		if top > lines { // The history shrank between pane.get and pane.read.
			return nil, errNoBoundary
		}
		// Rows past the last one with text are blank. Output since pane.get
		// only adds rows below top.
		rows := all[:min(top, len(all))]
		for len(rows) < top {
			rows = append(rows, "")
		}
		return rows, nil
	}
	vis, err := h.b.H.PaneRead(ctx, jsonapi.PaneReadParams{PaneID: h.pane, Source: "visible", Format: "ansi"})
	if err != nil {
		return nil, err
	}
	rows, ok := splitHistory(all, trimBlankRows(formattedRows(vis.Text)), lines, screen, offset, height)
	if !ok {
		if traceFrames {
			log.Printf("terminal %s: history boundary not found", h.pane)
		}
		return nil, errNoBoundary
	}
	return rows, nil
}

var errNoBoundary = errors.New("the recent rows do not hold the visible screen")

// splitHistory returns the rows of all, the last lines rows Herdr read, that
// come before shown, the rows Herdr shows; both have lost their trailing
// blank rows. The read ends at the cursor or the last row with text on the
// screen (src/pane/terminal.rs, ghostty_recent_read_range), which starts at
// row screen and is height rows tall. Unscrolled, all thus ends with shown.
// Scrolled back offset rows, shown starts where one of those ends puts it.
// It fails when shown is empty, as nothing then places the boundary, or when
// no place matches, as when output came between the reads.
func splitHistory(all, shown []string, lines, screen, offset, height int) ([]string, bool) {
	if len(shown) == 0 {
		return nil, false
	}
	match := func(at int) bool {
		return at >= 0 && at+len(shown) <= len(all) &&
			slices.EqualFunc(all[at:at+len(shown)], shown, func(a, b string) bool { return plainRow(a) == plainRow(b) })
	}
	if offset == 0 {
		if at := len(all) - len(shown); match(at) {
			return all[:at], true
		}
		return nil, false
	}
	for end := screen + height - 1; end >= screen; end-- {
		if at := screen - offset - max(end+1-lines, 0); match(at) {
			return all[:at], true
		}
	}
	return nil, false
}

// formattedRows splits Ghostty's VT formatter output into sanitized rows.
func formattedRows(s string) []string {
	if s == "" {
		return nil
	}
	rows := strings.Split(s, "\r\n")
	for i, r := range rows {
		rows[i] = sanitizeRow(r)
	}
	return rows
}

func trimBlankRows(rows []string) []string {
	for len(rows) > 0 && plainRow(rows[len(rows)-1]) == "" {
		rows = rows[:len(rows)-1]
	}
	return rows
}

// anchorRows is the end of the pushed rows that a later read must contain:
// historyAnchor rows, reaching back to a row with text so a run of blank
// rows cannot match anywhere, but no further than historyAnchorMax rows.
func anchorRows(tail []string) []string {
	start := max(len(tail)-historyAnchor, 0)
	for i := len(tail) - 1; i >= max(len(tail)-historyAnchorMax, 0); i-- {
		if plainRow(tail[i]) != "" {
			return tail[min(i, start):]
		}
	}
	return tail[start:]
}

// findNew returns the index in rows of the first row after anchor, or -1.
// The anchor is looked for first where Herdr's history growth puts it, then
// at its last place.
func findNew(rows, anchor []string, grew int) int {
	at := func(end int) bool {
		if end < len(anchor) || end > len(rows) {
			return false
		}
		return slices.EqualFunc(rows[end-len(anchor):end], anchor, func(a, b string) bool { return plainRow(a) == plainRow(b) })
	}
	if grew >= 0 && at(len(rows)-grew) {
		return len(rows) - grew
	}
	for end := len(rows); end >= len(anchor); end-- {
		if at(end) {
			return end
		}
	}
	return -1
}

// sanitizeRow keeps a formatted row's text and SGR sequences. Anything else
// a client would act on, such as OSC 52 or a title, is dropped, as are
// control characters.
func sanitizeRow(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			n := escapeLen(s[i:])
			if isSGR(s[i : i+n]) {
				b.WriteString(s[i : i+n])
			}
			i += n
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if r >= 0x20 && r != 0x7f && (r < 0x80 || r >= 0xa0) && r != utf8.RuneError {
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	return b.String()
}

// escapeLen is the length of the escape sequence at the start of s, or of s
// when it does not end.
func escapeLen(s string) int {
	if len(s) < 2 {
		return len(s)
	}
	switch s[1] {
	case '[':
		for i := 2; i < len(s); i++ {
			switch c := s[i]; {
			case c >= 0x40 && c <= 0x7e:
				return i + 1
			case c < 0x20 || c > 0x7e: // not a CSI byte: the sequence ends before it
				return i
			}
		}
		return len(s)
	case ']', 'P', 'X', '^', '_':
		for i := 2; i < len(s); i++ {
			if s[i] == 0x07 {
				return i + 1
			}
			if s[i] == 0x1b && i+1 < len(s) && s[i+1] == '\\' {
				return i + 2
			}
		}
		return len(s)
	}
	i := 1 // intermediates, then the final byte
	for i < len(s) && s[i] >= 0x20 && s[i] <= 0x2f {
		i++
	}
	return min(i+1, len(s))
}

func isSGR(seq string) bool {
	if len(seq) < 3 || seq[1] != '[' || seq[len(seq)-1] != 'm' {
		return false
	}
	return strings.Trim(seq[2:len(seq)-1], "0123456789;:") == ""
}

// plainRow is a row's text without escapes or trailing spaces.
func plainRow(s string) string {
	var b strings.Builder
	for i := 0; i < len(s); {
		if s[i] == 0x1b {
			i += escapeLen(s[i:])
			continue
		}
		b.WriteByte(s[i])
		i++
	}
	return strings.TrimRight(b.String(), " ")
}
