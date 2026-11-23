package bridge

import (
	"bufio"
	"encoding/binary"
	"io"
	"net"
	"sync"
	"time"

	"github.com/elonnzhang/herdrex/pkg/herdr/term"
)

// fakeTerm is one connection to the fake client socket, after it picked a
// pane with Control or Observe.
type fakeTerm struct {
	pane    string
	control bool
	hello   term.Size // the size in TerminalHello
	msgs    chan termMsg
	closed  chan struct{} // the connection ended

	wmu sync.Mutex
	c   net.Conn
}

// termMsg is a message the bridge sent after Control or Observe.
type termMsg struct {
	tag  uint64 // 3 Resize, 4 Detach
	size term.Size
}

const (
	tagResize = 3
	tagDetach = 4
)

// terminal serves one client socket connection as Herdr does: frames at the
// client's size, a full frame for every Resize, and Shutdown "detached" for
// Detach, after detachDelay. A Control fails while another client controls the pane.
func (f *fakeHerdr) terminal(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	p, err := readMsg(r)
	if err != nil {
		return
	}
	d := &dec{p}
	d.u() // TerminalHello
	d.u() // protocol
	size := d.size()
	writeMsg(c, welcome)
	if p, err = readMsg(r); err != nil {
		return
	}
	d = &dec{p}
	ft := &fakeTerm{control: d.u() == 8, msgs: make(chan termMsg, 64), closed: make(chan struct{}), c: c}
	ft.pane, ft.hello = d.str(), size
	f.mu.Lock()
	refused := ft.control && (f.st.external[ft.pane] || f.st.controlled[ft.pane] > 0)
	if ft.control && !refused {
		f.st.controlled[ft.pane]++
	}
	delay, hold := f.st.firstFrame[ft.pane], f.st.detachDelay[ft.pane]
	f.mu.Unlock()
	// finish ends the attachment before Herdr's last word reaches the bridge.
	finish := sync.OnceFunc(func() {
		if ft.control && !refused {
			f.edit(func(s *fakeState) { s.controlled[ft.pane]-- })
		}
		close(ft.closed)
	})
	defer finish()
	f.terms <- ft
	if refused {
		finish()
		ft.end("terminal session control failed: terminal " + ft.pane + " already has an attached client")
		return
	}
	time.Sleep(delay)
	seq := uint64(1)
	ft.frame(seq, size)
	for {
		p, err := readMsg(r)
		if err != nil {
			return
		}
		d := &dec{p}
		m := termMsg{tag: d.u()}
		if m.tag == tagResize {
			m.size = d.size()
			size = m.size
			seq++
			ft.frame(seq, size)
		}
		ft.msgs <- m
		if m.tag == tagDetach {
			time.Sleep(hold)
			finish()
			ft.end("detached")
			return
		}
	}
}

// frame sends a full frame of the given size that shows "hi".
func (ft *fakeTerm) frame(seq uint64, s term.Size) {
	b := putU([]byte{1}, seq)
	b = putU(putU(b, uint64(s.Cols)), uint64(s.Rows))
	ft.write(append(b, 1, 2, 'h', 'i'))
}

// end sends ServerShutdown with reason and closes the connection.
func (ft *fakeTerm) end(reason string) {
	ft.write(append(putU([]byte{3, 1}, uint64(len(reason))), reason...))
	ft.c.Close()
}

func (ft *fakeTerm) write(p []byte) {
	ft.wmu.Lock()
	defer ft.wmu.Unlock()
	writeMsg(ft.c, p)
}

func readMsg(r io.Reader) ([]byte, error) {
	var n [4]byte
	if _, err := io.ReadFull(r, n[:]); err != nil {
		return nil, err
	}
	p := make([]byte, binary.LittleEndian.Uint32(n[:]))
	_, err := io.ReadFull(r, p)
	return p, err
}

func writeMsg(w io.Writer, p []byte) {
	w.Write(append(binary.LittleEndian.AppendUint32(nil, uint32(len(p))), p...))
}

// putU appends x in bincode's variable-length encoding.
func putU(b []byte, x uint64) []byte {
	switch {
	case x < 251:
		return append(b, byte(x))
	case x < 1<<16:
		return binary.LittleEndian.AppendUint16(append(b, 251), uint16(x))
	}
	return binary.LittleEndian.AppendUint32(append(b, 252), uint32(x))
}

// dec reads bincode values; past the end it reads zeros.
type dec struct{ b []byte }

func (d *dec) take(n int) []byte {
	n = min(n, len(d.b))
	p := d.b[:n]
	d.b = d.b[n:]
	return p
}

func (d *dec) u() uint64 {
	p := d.take(1)
	if len(p) == 0 {
		return 0
	}
	switch p[0] {
	case 251:
		return d.le(2)
	case 252:
		return d.le(4)
	}
	return uint64(p[0])
}

func (d *dec) le(n int) uint64 {
	var b [8]byte
	copy(b[:], d.take(n))
	return binary.LittleEndian.Uint64(b[:])
}

func (d *dec) str() string { return string(d.take(int(d.u()))) }

func (d *dec) size() term.Size {
	return term.Size{Cols: uint16(d.u()), Rows: uint16(d.u()),
		CellWidthPx: uint32(d.u()), CellHeightPx: uint32(d.u()), PixelMouse: d.u() == 1}
}
