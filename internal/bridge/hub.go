package bridge

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/elonnzhang/herdrex/pkg/herdr"
	"github.com/elonnzhang/herdrex/pkg/herdr/jsonapi"
	"github.com/elonnzhang/herdrex/pkg/herdr/term"
	"github.com/elonnzhang/herdrex/pkg/rex"
)

// A hub holds the one direct terminal connection to a Herdr pane and fans
// its frames out to every Rex data stream of that pane (docs/PROTOCOL.md §5.1).

const (
	hubQueue      = 256 // frames buffered per stream before it is dropped
	closeBlock    = websocket.StatusCode(4000)
	closeReconnct = websocket.StatusCode(1013)
)

var traceFrames = os.Getenv("HERDREX_TRACE") != ""

type streamMsg struct {
	frame  *term.Frame
	theme  map[string]any       // set_theme arguments to send as a theme APC
	mouse  bool                 // the program turned mouse reports on or off
	code   websocket.StatusCode // set on the last message
	why    string
	exited bool // the program exited: send child_exited before closing
}

type sub struct {
	out      chan streamMsg
	waitFull bool
}

type hub struct {
	b       *Bridge
	pane    string
	conn    *term.Conn
	control bool // false: observing because another client controls the pane
	done    chan struct{}

	mu    sync.Mutex
	subs  map[*sub]struct{}
	size  term.Size
	mouse bool // the program asked for mouse reports (known only when controlling)
}

func (b *Bridge) protocol(ctx context.Context) (uint32, error) {
	b.mu.Lock()
	p := b.proto
	b.mu.Unlock()
	if p != 0 {
		return p, nil
	}
	var r struct {
		Protocol uint32 `json:"protocol"`
	}
	if err := b.H.Call(ctx, "ping", nil, &r); err != nil {
		return 0, err
	}
	b.mu.Lock()
	b.proto = r.Protocol
	b.mu.Unlock()
	return r.Protocol, nil
}

// join adds a stream to the pane's hub, starting the hub if needed.
func (b *Bridge) join(ctx context.Context, pane, block string) (*hub, *sub, error) {
	b.hubMu.Lock()
	defer b.hubMu.Unlock()
	h := b.hubs[pane]
	if h == nil {
		var first *term.Frame
		var err error
		h, first, err = b.startHub(ctx, pane, block)
		if err != nil {
			return nil, nil, err
		}
		b.hubs[pane] = h
		s := &sub{out: make(chan streamMsg, hubQueue)}
		h.subs[s] = struct{}{}
		s.out <- streamMsg{frame: first}
		go h.run()
		return h, s, nil
	}
	s := &sub{out: make(chan streamMsg, hubQueue), waitFull: true}
	h.mu.Lock()
	h.subs[s] = struct{}{}
	size := h.size
	h.mu.Unlock()
	h.conn.Resize(size) // Herdr answers with a full frame for the newcomer
	return h, s, nil
}

func (b *Bridge) startHub(ctx context.Context, pane, block string) (*hub, *term.Frame, error) {
	proto, err := b.protocol(ctx)
	if err != nil {
		return nil, nil, err
	}
	size := b.layoutSize(ctx, pane)
	if d, ok := b.ownerDesired(block); ok {
		size = d
	}
	sock := herdr.ClientSocket(b.H.Socket)
	for _, control := range []bool{true, false} {
		dctx, cancel := context.WithTimeout(ctx, 3*time.Second)
		c, err := term.Dial(dctx, sock, proto, size)
		cancel()
		if err != nil {
			return nil, nil, err
		}
		if control {
			err = c.Control(pane, false)
		} else {
			err = c.Observe(pane)
		}
		var f *term.Frame
		var mouse bool
		if err == nil {
			f, mouse, err = firstFrame(ctx, c)
		}
		if err == nil {
			log.Printf("terminal %s: attached (control=%v) %dx%d", pane, control, f.Width, f.Height)
			if !control {
				size.Cols, size.Rows = f.Width, f.Height
			}
			return &hub{b: b, pane: pane, conn: c, control: control, done: make(chan struct{}),
				subs: map[*sub]struct{}{}, size: size, mouse: mouse}, f, nil
		}
		c.Close()
		if !control || !strings.Contains(err.Error(), "already has an attached client") {
			return nil, nil, err
		}
		log.Printf("terminal %s: controlled elsewhere, observing", pane)
	}
	panic("unreachable")
}

// firstFrameTimeout bounds the wait for the first frame. join holds hubMu
// meanwhile, and Herdr holds back a terminal's frames while its program has
// synchronized output (mode 2026) on (src/server/headless/render.rs:774), so
// a program that never turns it off would otherwise stop every pane's hub
// from starting.
var firstFrameTimeout = 5 * time.Second

// firstFrame waits for the first frame, noting the mouse capture state
// Herdr may send before it. It closes c and fails when ctx ends or no frame
// comes within firstFrameTimeout.
func firstFrame(ctx context.Context, c *term.Conn) (*term.Frame, bool, error) {
	ctx, cancel := context.WithTimeoutCause(ctx, firstFrameTimeout,
		fmt.Errorf("no frame within %v", firstFrameTimeout))
	defer cancel()
	stop := context.AfterFunc(ctx, func() { c.Close() })
	defer stop()
	mouse := false
	for {
		m, err := c.Next()
		if err != nil {
			if ctx.Err() != nil {
				return nil, false, context.Cause(ctx)
			}
			return nil, false, err
		}
		switch m := m.(type) {
		case *term.Frame:
			if !stop() { // ctx ended as the frame arrived and c is closed
				return nil, false, context.Cause(ctx)
			}
			return m, mouse, nil
		case *term.MouseCapture:
			mouse = m.Enabled
		case *term.Shutdown:
			return nil, false, errors.New(m.Reason)
		}
	}
}

// run forwards frames until Herdr ends the attachment.
func (h *hub) run() {
	reason := "connection lost"
	for {
		m, err := h.conn.Next()
		if err != nil {
			break
		}
		if s, ok := m.(*term.Shutdown); ok {
			reason = s.Reason
			break
		}
		// Herdr 0.9.5 sends bells and clipboard writes only to its foreground
		// shell client, which a terminal attachment never is, so these do not
		// arrive yet. They are forwarded for a Herdr that sends them.
		if _, ok := m.(*term.Bell); ok {
			h.b.paneBell(h.pane)
			continue
		}
		if c, ok := m.(*term.Clipboard); ok {
			h.b.paneClipboard(h.pane, c.Data)
			continue
		}
		if mc, ok := m.(*term.MouseCapture); ok {
			h.mu.Lock()
			h.mouse = mc.Enabled
			for s := range h.subs { // a dropped notice is caught up at the next frame
				select {
				case s.out <- streamMsg{mouse: true}:
				default:
				}
			}
			h.mu.Unlock()
			continue
		}
		f, ok := m.(*term.Frame)
		if !ok {
			continue
		}
		h.mu.Lock()
		if traceFrames {
			log.Printf("terminal %s: frame seq=%d %dx%d full=%v %dB subs=%d", h.pane, f.Seq, f.Width, f.Height, f.Full, len(f.Bytes), len(h.subs))
		}
		for s := range h.subs {
			if s.waitFull && !f.Full {
				continue
			}
			s.waitFull = false
			select {
			case s.out <- streamMsg{frame: f}:
			default: // too slow: a closed channel makes it reconnect
				delete(h.subs, s)
				close(s.out)
			}
		}
		h.mu.Unlock()
	}
	h.conn.Close()
	close(h.done) // before hubMu: leave holds it while waiting for done
	h.b.hubMu.Lock()
	if h.b.hubs[h.pane] == h {
		delete(h.b.hubs, h.pane)
	}
	h.b.hubMu.Unlock()
	code, why, exited := closeReconnct, "data reconnect required", false
	if reason == "detached" {
		return
	}
	// Herdr says "terminal <id> exited" only once the pane is gone. Other
	// endings need a look at the pane.
	gone := strings.HasPrefix(reason, "terminal ") && strings.HasSuffix(reason, " exited")
	if !gone {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		_, err := h.b.pane(ctx, h.pane)
		cancel()
		gone = err != nil && isPaneGone(err)
	}
	if gone {
		code, why, exited = closeBlock, "block closed", true
	}
	log.Printf("terminal %s: ended (%s)", h.pane, reason)
	h.mu.Lock()
	for s := range h.subs {
		delete(h.subs, s)
		select {
		case s.out <- streamMsg{code: code, why: why, exited: exited}:
		default:
		}
		close(s.out)
	}
	h.mu.Unlock()
}

// leave removes a stream. The last one restores the layout size, which
// Herdr does not do on detach, and detaches.
func (b *Bridge) leave(h *hub, s *sub) {
	b.hubMu.Lock()
	defer b.hubMu.Unlock()
	h.mu.Lock()
	delete(h.subs, s)
	n := len(h.subs)
	h.mu.Unlock()
	if n > 0 || b.hubs[h.pane] != h {
		return
	}
	delete(b.hubs, h.pane)
	if h.control {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		size := b.layoutSize(ctx, h.pane)
		cancel()
		h.conn.Resize(size)
	}
	h.conn.Detach()
	select { // wait so a new Control does not race the old attachment
	case <-h.done:
	case <-time.After(2 * time.Second):
		h.conn.Close()
	}
	log.Printf("terminal %s: detached", h.pane)
}

// apply resizes the PTY. Observing hubs only change their own view.
func (h *hub) apply(s term.Size) {
	h.mu.Lock()
	if h.size == s {
		h.mu.Unlock()
		return
	}
	h.size = s
	h.mu.Unlock()
	if h.control {
		h.conn.Resize(s)
	}
}

// sendTheme queues a theme change for every stream of the hub.
func (h *hub) sendTheme(t map[string]any) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for s := range h.subs {
		select {
		case s.out <- streamMsg{theme: t}:
		default:
		}
	}
}

// Herdr's frames repaint the screen but leave out terminal modes, so the
// client's emulator never sees the program ask for the mouse and keeps
// clicks for selection. While the program reports the mouse, streams turn on
// any-motion SGR reports in the client; Herdr drops the events the program's
// own mode does not ask for (1000 gets no motion, 1002 only drags).
var mouseModes = map[bool][]byte{
	true:  []byte("\x1b[?1003h\x1b[?1006h"),
	false: []byte("\x1b[?1003l\x1b[?1006l"),
}

// bracketedPaste stays on in the client, as Herdr's own attach client keeps
// it: the client marks every paste, and Herdr marks it again for the program
// only if the program asked for bracketed paste (see pasteInput).
var bracketedPaste = []byte("\x1b[?2004h")

// reportsMouse says whether mouse events go to the program.
func (h *hub) reportsMouse() bool {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.mouse
}

func (h *hub) current() term.Size {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.size
}

func (b *Bridge) hub(pane string) *hub {
	b.hubMu.Lock()
	defer b.hubMu.Unlock()
	return b.hubs[pane]
}

// isPaneGone reports a not-found error, from Herdr or as b.call rewrote it.
func isPaneGone(err error) bool {
	var he *jsonapi.Error
	var re *rex.Error
	return errors.As(err, &he) && strings.HasSuffix(he.Code, "not_found") ||
		errors.As(err, &re) && re.Code == "not_found"
}
