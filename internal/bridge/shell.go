package bridge

import (
	"context"
	"errors"
	"iter"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"

	"github.com/elonnzhang/herdrex/pkg/herdr/term"
	"github.com/elonnzhang/herdrex/pkg/rex"
)

const (
	shellPrefix = "shell:"  // Herdr-side id of a herdrex shell: shell:<uuid>
	shellRing   = 256 << 10 // recent output a stream that joins late replays
	shellQueue  = 256       // messages buffered per stream before it is dropped
	shellGrace  = 2 * time.Second
	shellDrain  = 200 * time.Millisecond // output read after the shell exits
)

// shellEnd is how a shell's streams end.
type shellEnd int

const (
	endRunning shellEnd = iota
	endExited           // the program exited by itself: child_exited, then 4000
	endClosed           // a client or Herdr closed the block: 4000
	endLagged           // this stream fell behind: 1013, and it reconnects
)

// shellMsg is one message to a stream: new output, a new size, a theme, or
// the end.
type shellMsg struct {
	data  []byte
	size  *term.Size
	theme map[string]any
	end   shellEnd
	code  int // exit code, with end == endExited
}

type shellSub struct{ out chan shellMsg }

// A shellBlock is a shell that herdrex runs itself, in a PTY on its own
// machine, for a floating layer that session.new_layer asked for. Herdr has
// no floating panes and never sees it. The shell's output bytes go to the
// Rex data streams as they are; the client's emulator draws them.
//
// Lock order: the bridge's hubMu, then its mu, then a shell's mu.
type shellBlock struct {
	// Set by the bridge before start, then fixed.
	id    string // shell:<uuid>
	ws    string // the Herdr workspace and tab it floats in
	tab   string
	layer string    // its floating layer
	block rex.Block // its Rex block

	// Set by start, then fixed.
	program string // the program's path
	argv0   string
	cwd     string // where it started
	pid     int
	pty     *os.File
	done    chan struct{} // closed once the process is reaped and its output read

	mu   sync.Mutex
	size term.Size
	ring ring
	subs map[*shellSub]struct{}
	end  shellEnd
	code int
}

// shellProgram is $SHELL if it names an executable, else bash, else sh.
func shellProgram() (string, error) {
	for _, name := range []string{os.Getenv("SHELL"), "bash", "sh"} {
		if name == "" {
			continue
		}
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", errors.New("no shell found: set SHELL, or install bash or sh")
}

// start runs an interactive login shell in a new PTY of the given size, in
// its own session so that the PTY is its controlling terminal and its
// process group gets SIGWINCH and SIGHUP. exit runs once the process is
// gone and its output read. The caller fills in the rest of sh first.
func (sh *shellBlock) start(cwd string, size term.Size, exit func(*shellBlock)) error {
	program, err := shellProgram()
	if err != nil {
		return err
	}
	sh.program, sh.argv0, sh.cwd = program, "-"+filepath.Base(program), cwd
	sh.done, sh.size, sh.ring, sh.subs = make(chan struct{}), size, ring{max: shellRing}, map[*shellSub]struct{}{}
	cmd := &exec.Cmd{Path: program, Args: []string{sh.argv0}, Dir: cwd,
		Env: append(os.Environ(), "TERM=xterm-256color", "COLORTERM=truecolor")}
	if sh.pty, err = pty.StartWithSize(cmd, winsize(size)); err != nil {
		return err
	}
	sh.pid = cmd.Process.Pid
	read := make(chan struct{})
	go func() {
		defer close(read)
		buf := make([]byte, 32<<10)
		for {
			n, err := sh.pty.Read(buf)
			if n > 0 {
				sh.output(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()
	go func() {
		err := cmd.Wait()
		code := 0
		if ee := (*exec.ExitError)(nil); errors.As(err, &ee) {
			code = ee.ExitCode()
			if ws, ok := ee.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
				code = 128 + int(ws.Signal())
			}
		}
		syscall.Kill(-sh.pid, syscall.SIGKILL) // what is left of its process group
		select {
		case <-read:
		case <-time.After(shellDrain): // a background program still holds the PTY
		}
		sh.pty.Close()
		sh.finish(endExited, code)
		close(sh.done)
		exit(sh)
	}()
	return nil
}

func winsize(s term.Size) *pty.Winsize {
	return &pty.Winsize{Cols: s.Cols, Rows: s.Rows,
		X: uint16(min(uint32(s.Cols)*s.CellWidthPx, 65535)), Y: uint16(min(uint32(s.Rows)*s.CellHeightPx, 65535))}
}

// output keeps p for late joiners and queues it for every stream.
func (sh *shellBlock) output(p []byte) {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	sh.ring.write(p)
	sh.broadcast(shellMsg{data: append([]byte(nil), p...)})
}

// broadcast queues m for every stream, dropping a stream whose queue is
// full. Callers hold mu.
func (sh *shellBlock) broadcast(m shellMsg) {
	for s := range sh.subs {
		select {
		case s.out <- m:
		default:
			delete(sh.subs, s)
			s.last(shellMsg{end: endLagged})
		}
	}
}

// last queues the final message of a stream that is no longer subscribed,
// making room for it if the queue is full. Callers hold the shell's mu, so
// nothing else fills the queue meanwhile.
func (s *shellSub) last(m shellMsg) {
	if len(s.out) == cap(s.out) {
		select {
		case <-s.out:
		default:
		}
	}
	s.out <- m
}

// finish ends every stream the first time the shell ends, and reports
// whether this call ended it.
func (sh *shellBlock) finish(end shellEnd, code int) bool {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if sh.end != endRunning {
		return false
	}
	sh.end, sh.code = end, code
	for s := range sh.subs {
		delete(sh.subs, s)
		s.last(shellMsg{end: end, code: code})
	}
	return true
}

// close ends the shell's streams and hangs up its process group, killing it
// if it is still there after shellGrace. It returns at once.
func (sh *shellBlock) close() {
	if !sh.finish(endClosed, 0) {
		return
	}
	syscall.Kill(-sh.pid, syscall.SIGHUP)
	go func() {
		select {
		case <-sh.done:
		case <-time.After(shellGrace):
			syscall.Kill(-sh.pid, syscall.SIGKILL)
		}
	}()
}

func (sh *shellBlock) write(p []byte) error {
	if sh.ended() {
		return errShellClosed
	}
	_, err := sh.pty.Write(p)
	return err
}

var errShellClosed = errors.New("block closed")

func (sh *shellBlock) ended() bool {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	return sh.end != endRunning
}

func (sh *shellBlock) current() term.Size {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	return sh.size
}

// resize sets the PTY size, which sends the shell SIGWINCH, and reports
// whether it changed.
func (sh *shellBlock) resize(s term.Size) bool {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	if sh.size == s || sh.end != endRunning {
		return false
	}
	if pty.Setsize(sh.pty, winsize(s)) != nil {
		return false
	}
	sh.size = s
	sh.broadcast(shellMsg{size: &s})
	return true
}

func (sh *shellBlock) sendTheme(t map[string]any) {
	sh.mu.Lock()
	defer sh.mu.Unlock()
	sh.broadcast(shellMsg{theme: t})
}

// messages yields what a stream sends: the size, the recent output, then
// each change, until the shell ends, the stream falls behind or ctx ends.
func (sh *shellBlock) messages(ctx context.Context) iter.Seq[shellMsg] {
	return func(yield func(shellMsg) bool) {
		s := &shellSub{out: make(chan shellMsg, shellQueue)}
		sh.mu.Lock()
		size, replay, end, code := sh.size, sh.ring.bytes(), sh.end, sh.code
		if end == endRunning {
			sh.subs[s] = struct{}{}
		}
		sh.mu.Unlock()
		defer func() {
			sh.mu.Lock()
			delete(sh.subs, s)
			sh.mu.Unlock()
		}()
		if !yield(shellMsg{size: &size}) || len(replay) > 0 && !yield(shellMsg{data: replay}) {
			return
		}
		if end != endRunning {
			yield(shellMsg{end: end, code: code})
			return
		}
		for {
			select {
			case <-ctx.Done():
				return
			case m := <-s.out:
				if !yield(m) || m.end != endRunning {
					return
				}
			}
		}
	}
}

// ring keeps the last max bytes written to it.
type ring struct {
	max  int
	buf  []byte
	next int // where the next byte goes, once buf is full
}

func (r *ring) write(p []byte) {
	if len(p) >= r.max {
		r.buf, r.next = append(r.buf[:0], p[len(p)-r.max:]...), 0
		return
	}
	if room := r.max - len(r.buf); room > 0 {
		n := min(room, len(p))
		r.buf, p = append(r.buf, p[:n]...), p[n:]
	}
	for len(p) > 0 {
		n := copy(r.buf[r.next:], p)
		p, r.next = p[n:], (r.next+n)%r.max
	}
}

// bytes copies what the ring holds, oldest first.
func (r *ring) bytes() []byte {
	return append(append([]byte(nil), r.buf[r.next:]...), r.buf[:r.next]...)
}
