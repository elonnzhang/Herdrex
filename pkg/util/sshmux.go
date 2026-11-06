package util

import (
	"bufio"
	"cmp"
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// muxScript runs on the remote host and multiplexes connections to Unix
// sockets over ssh's stdin and stdout, so that one SSH session
// carries every connection the bridge makes. Some SSH relays refuse socket
// forwarding and cap sessions per connection (sshd's MaxSessions, 10 by
// default), and the bridge opens a connection per API call and one per
// terminal, so a session per connection soon runs out.
//
// Frames both ways: type u8, stream u32, length u32, payload. Type 1 opens a
// stream (the payload is the index of the socket in the arguments), 2 carries
// data, 3 closes it. Perl is in the base system of almost every Unix host.
const muxScript = `use strict; use warnings; use IO::Socket::UNIX; use IO::Select;
$SIG{PIPE} = 'IGNORE';
my @path = @ARGV; binmode STDIN; binmode STDOUT;
my (%s, %id); my $sel = IO::Select->new(\*STDIN); my $in = '';
sub out { my ($t, $i, $p) = @_; $p = '' unless defined $p;
  my $b = pack('CNN', $t, $i, length $p) . $p; my $o = 0;
  while ($o < length $b) { my $n = syswrite(STDOUT, $b, length($b) - $o, $o); exit 0 unless defined $n; $o += $n } }
sub drop { my $i = shift; my $c = delete $s{$i} or return; $sel->remove($c); delete $id{fileno $c}; close $c }
sub put { my ($c, $p) = @_; my $o = 0;
  while ($o < length $p) { my $n = syswrite($c, $p, length($p) - $o, $o); return 0 unless defined $n; $o += $n } 1 }
my $r = "\nherdr-remote-output-ready:1\n"; syswrite(STDOUT, $r);
while (1) {
  for my $h ($sel->can_read) {
    if (fileno($h) == fileno(STDIN)) {
      my $n = sysread(STDIN, $in, 1 << 16, length $in); exit 0 unless $n;
      while (length $in >= 9) {
        my ($t, $i, $l) = unpack('CNN', $in); last if length $in < 9 + $l;
        my $p = substr($in, 9, $l); substr($in, 0, 9 + $l, '');
        if ($t == 1) {
          my $c = IO::Socket::UNIX->new(Peer => $path[ord $p]);
          if (!$c) { out(3, $i); next }
          binmode $c; $s{$i} = $c; $id{fileno $c} = $i; $sel->add($c);
        } elsif ($t == 2) {
          my $c = $s{$i} or next; if (!put($c, $p)) { drop($i); out(3, $i) }
        } elsif ($t == 3) { drop($i) }
      }
    } else {
      my $i = $id{fileno $h}; my $buf; my $n = sysread($h, $buf, 1 << 16);
      if (!$n) { drop($i); out(3, $i) } else { out(2, $i, $buf) }
    }
  }
}`

// The mux has no flow control, so what the remote sends faster than a local
// reader takes is queued; these bound it.
const (
	muxMaxFrame = 1 << 20 // the script sends at most 64 KiB per frame
	muxMaxQueue = 8 << 20 // per stream; a stream that falls further behind is closed
)

const (
	muxOpen  = 1
	muxData  = 2
	muxClose = 3
)

// muxStream is one local connection carried over the mux.
type muxStream struct {
	c      net.Conn
	mu     sync.Mutex
	cond   *sync.Cond
	queue  [][]byte // data from the remote, not yet written to c
	size   int      // bytes in queue
	closed bool
}

// push queues data for c; a slow local reader does not hold up the others. It
// reports false, and drops the queue and closes c, when c is more than
// muxMaxQueue behind.
func (s *muxStream) push(b []byte) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.size+len(b) > muxMaxQueue {
		s.queue, s.size, s.closed = nil, 0, true
		s.cond.Signal()
		return false
	}
	s.queue = append(s.queue, b)
	s.size += len(b)
	s.cond.Signal()
	return true
}

// end closes c once the queued data is written.
func (s *muxStream) end() {
	s.mu.Lock()
	s.closed = true
	s.cond.Signal()
	s.mu.Unlock()
}

func (s *muxStream) writeLoop() {
	defer s.c.Close()
	for {
		s.mu.Lock()
		for len(s.queue) == 0 && !s.closed {
			s.cond.Wait()
		}
		if len(s.queue) == 0 {
			s.mu.Unlock()
			return
		}
		b := s.queue[0]
		s.queue = s.queue[1:]
		s.size -= len(b)
		s.mu.Unlock()
		if _, err := s.c.Write(b); err != nil {
			return
		}
	}
}

// Mux runs muxScript in one SSH session and serves each endpoint's local
// socket through it, connecting to its Remote socket.
func Mux(ctx context.Context, sshPath string, base []string, host string, eps []Endpoint) (*Tunnel, error) {
	// One line: some SSH relays (relay-cli) drop a remote command that holds
	// a newline. The script has no comments, so joining its lines is safe.
	script := strings.ReplaceAll(muxScript, "\n", " ")
	remote := "exec perl -e " + ShellQuote(script)
	for _, ep := range eps {
		remote += " " + ShellQuote(ep.Remote)
	}
	ctx, cancel := context.WithCancel(ctx)
	cmd := exec.CommandContext(ctx, sshPath, append(append([]string{}, base...), "-T", host, remote)...)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, err
	}
	t := &Tunnel{mode: "mux", done: make(chan struct{})}
	var once sync.Once
	finish := func(err error) { once.Do(func() { t.err = err; close(t.done) }) }
	waited := make(chan struct{})
	go func() {
		err := cmd.Wait()
		if err != nil && stderr.Len() > 0 {
			err = fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
		}
		finish(err)
		close(waited)
	}()

	r := bufio.NewReaderSize(stdout, 64<<10)
	ready := make(chan error, 1)
	go func() {
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				ready <- fmt.Errorf("remote mux ended before it was ready: %v", err)
				return
			}
			if strings.TrimRight(line, "\r\n") == ReadyLine {
				ready <- nil
				return
			}
		}
	}()
	select {
	case err = <-ready:
	case <-time.After(30 * time.Second):
		err = fmt.Errorf("remote mux did not start in 30s")
	case <-ctx.Done():
		err = ctx.Err()
	}
	if err != nil {
		cancel()
		<-waited
		return nil, cmp.Or(err, t.err)
	}

	var (
		wmu     sync.Mutex
		mu      sync.Mutex
		streams = map[uint32]*muxStream{}
		next    uint32
	)
	send := func(typ byte, id uint32, p []byte) error {
		var h [9]byte
		h[0] = typ
		binary.BigEndian.PutUint32(h[1:], id)
		binary.BigEndian.PutUint32(h[5:], uint32(len(p)))
		wmu.Lock()
		defer wmu.Unlock()
		if _, err := stdin.Write(h[:]); err != nil {
			return err
		}
		_, err := stdin.Write(p)
		return err
	}
	remove := func(id uint32) *muxStream {
		mu.Lock()
		defer mu.Unlock()
		s := streams[id]
		delete(streams, id)
		return s
	}

	// Frames from the remote.
	go func() {
		var h [9]byte
		for {
			if _, err := io.ReadFull(r, h[:]); err != nil {
				cancel()
				return
			}
			id := binary.BigEndian.Uint32(h[1:])
			n := binary.BigEndian.Uint32(h[5:])
			if n > muxMaxFrame {
				cancel() // not the script's framing: stop rather than allocate it
				return
			}
			p := make([]byte, n)
			if _, err := io.ReadFull(r, p); err != nil {
				cancel()
				return
			}
			switch h[0] {
			case muxData:
				mu.Lock()
				s := streams[id]
				mu.Unlock()
				if s != nil && !s.push(p) {
					remove(id)
					send(muxClose, id, nil)
					log.Printf("ssh: a local reader fell %d MiB behind; its connection was closed", muxMaxQueue>>20)
				}
			case muxClose:
				if s := remove(id); s != nil {
					s.end()
				}
			}
		}
	}()

	var lns []net.Listener
	for target, ep := range eps {
		os.Remove(ep.Local)
		ln, err := net.Listen("unix", ep.Local)
		if err != nil {
			for _, l := range lns {
				l.Close()
			}
			cancel()
			<-waited
			return nil, err
		}
		lns = append(lns, ln)
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					return
				}
				s := &muxStream{c: c}
				s.cond = sync.NewCond(&s.mu)
				mu.Lock()
				next++
				id := next
				streams[id] = s
				mu.Unlock()
				go s.writeLoop()
				if err := send(muxOpen, id, []byte{byte(target)}); err != nil {
					remove(id)
					s.end()
					continue
				}
				go func() {
					buf := make([]byte, 32<<10)
					for {
						n, err := c.Read(buf)
						if n > 0 && send(muxData, id, buf[:n]) != nil {
							break
						}
						if err != nil {
							break
						}
					}
					if s := remove(id); s != nil {
						send(muxClose, id, nil)
						s.end()
					}
				}()
			}
		}()
	}
	// Close every local connection when the SSH session ends.
	go func() {
		<-t.done
		for _, l := range lns {
			l.Close()
		}
		mu.Lock()
		for id, s := range streams {
			delete(streams, id)
			s.end()
		}
		mu.Unlock()
	}()
	t.stop = func() {
		cancel()
		<-waited
	}
	return t, nil
}
