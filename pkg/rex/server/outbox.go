package server

import (
	"context"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/elonnzhang/herdrex/pkg/rex"
)

const (
	// maxBacklog is how many bytes may wait for a client before the server
	// closes it, as the real server does (docs/rex-api.md 2.6).
	maxBacklog   = 8 << 20
	writeTimeout = 5 * time.Second
)

// outbox queues a control connection's messages for the one goroutine that
// writes them, so a client that stops reading holds up only itself. It also
// numbers the connection's events: an event gets its seq as it is queued, so
// events leave in seq order.
type outbox struct {
	mu     sync.Mutex
	seq    uint64
	msgs   [][]byte
	size   int                  // bytes queued or being written, while open
	code   websocket.StatusCode // how the connection closes; 0 while open
	reason string
	wake   chan struct{}
}

func newOutbox() *outbox { return &outbox{wake: make(chan struct{}, 1)} }

// lastSeq is the seq of the last event queued.
func (o *outbox) lastSeq() uint64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.seq
}

// send queues a message.
func (o *outbox) send(b []byte) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.queueLocked(b)
}

// event queues an event with the connection's next seq and returns the seq.
// An event over the message limit closes the connection, because skipping it
// would leave a gap in seq (docs/rex-api.md 2.7).
func (o *outbox) event(payload any) uint64 {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.code != 0 {
		return o.seq
	}
	o.seq++
	b, err := marshal(map[string]any{"type": "event", "seq": o.seq, "payload": payload})
	switch {
	case err != nil:
		o.closeLocked(websocket.StatusInternalError, "event not encodable")
	case len(b) > rex.MaxMessageBytes:
		o.closeLocked(websocket.StatusMessageTooBig, "event exceeds message limit")
	default:
		o.queueLocked(b)
	}
	return o.seq
}

func (o *outbox) queueLocked(b []byte) {
	if o.code != 0 {
		return
	}
	if o.size+len(b) > maxBacklog {
		o.closeLocked(websocket.StatusPolicyViolation, "event backlog exceeded")
		return
	}
	o.msgs = append(o.msgs, b)
	o.size += len(b)
	o.signal()
}

// close drops what is queued and has the writer close the connection. The
// first close decides the code.
func (o *outbox) close(code websocket.StatusCode, reason string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.closeLocked(code, reason)
}

func (o *outbox) closeLocked(code websocket.StatusCode, reason string) {
	if o.code != 0 {
		return
	}
	o.code, o.reason, o.msgs = code, reason, nil
	o.signal()
}

func (o *outbox) signal() {
	select {
	case o.wake <- struct{}{}:
	default:
	}
}

// run writes the queued messages in order until the outbox closes, then
// closes the connection. A write that fails or takes longer than
// writeTimeout closes it too.
func (o *outbox) run(conn *websocket.Conn) {
	for {
		b, ok := o.next()
		if !ok {
			break
		}
		ctx, cancel := context.WithTimeout(context.Background(), writeTimeout)
		err := conn.Write(ctx, websocket.MessageText, b)
		cancel()
		o.mu.Lock()
		o.size -= len(b)
		o.mu.Unlock()
		if err != nil {
			o.close(websocket.StatusGoingAway, "write failed")
		}
	}
	o.mu.Lock()
	code, reason := o.code, o.reason
	o.mu.Unlock()
	conn.Close(code, reason)
}

// next waits for the next message; it reports false once the outbox closed.
func (o *outbox) next() ([]byte, bool) {
	for {
		o.mu.Lock()
		if o.code != 0 {
			o.mu.Unlock()
			return nil, false
		}
		if len(o.msgs) > 0 {
			b := o.msgs[0]
			o.msgs[0] = nil
			o.msgs = o.msgs[1:]
			o.mu.Unlock()
			return b, true
		}
		o.mu.Unlock()
		<-o.wake
	}
}
