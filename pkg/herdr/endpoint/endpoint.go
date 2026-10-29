// Package endpoint is a client for Herdr's endpoint protocol, generation 1:
// the protocol on herdr-client.sock that client-rendered Herdr shells use
// (pkg/herdr/HERDR.md §6). A connection receives session snapshots and
// notifications, can call the JSON API methods the server lists in its
// welcome (40 in Herdr 0.9.5), and, once its surface is active, receives the
// rendered content of the tab it views and sends semantic input to its panes.
//
// Generation 1 is Herdr's long-term compatibility floor, so this package does
// not depend on Herdr's private protocol version. It does not ask for the
// optional surface encodings (surface_reuse, surface_delta, surface_scroll)
// or direct graphics, so the server sends plain Surface and SurfacePatch
// messages and no GraphicsFile messages, which name files to read.
//
// Dial starts a goroutine that reads the connection. It hands Call results
// to the waiting Call and every other message to Events, in order and
// without dropping any. The caller must keep reading Events: while it does
// not, the goroutine blocks, and Call and HealthPing get no answers.
//
// An active connection changes the server's state for everyone: it claims
// the size of the tab it views and moves the session's focus (HERDR.md
// §6.7). Connect with the zero Hello, which is inactive, unless the client
// shows the surface.
package endpoint

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/elonnzhang/herdrex/pkg/herdr/internal/wire"
	"github.com/elonnzhang/herdrex/pkg/herdr/jsonapi"
)

// capHealthCheck is the capability HealthPing needs.
const capHealthCheck = "health_check"

// maxRequest is Herdr's limit on a request's JSON; it closes the connection
// for a larger one (src/server/client_commands.rs:10).
const maxRequest = 1 << 20

// Conn is an endpoint connection. Its methods are safe for concurrent use.
type Conn struct {
	// Welcome is the server's handshake reply. Do not modify it.
	Welcome Welcome

	nc        net.Conn
	r         *bufio.Reader
	wmu       sync.Mutex
	events    chan Message
	closing   chan struct{} // closed by Close
	done      chan struct{} // closed when the read loop has ended
	closeOnce sync.Once

	slot chan struct{} // held while a request is in flight
	n    atomic.Uint64

	mu     sync.Mutex
	bootID string
	call   *call
	pings  map[string]chan struct{}
	err    error // why the read loop ended
}

// call is the request in flight.
type call struct {
	id   string
	buf  []byte
	resp chan []byte // receives the whole response
}

// Dial connects to the client socket at socket (see herdr.ClientSocket) and
// performs the handshake. It returns once the server's first snapshot has
// arrived, so BootID is set; that snapshot is also the first *Snapshot on
// Events. ctx bounds the dial and the handshake only.
//
// A rejected hello is a *HandshakeError; a server without the endpoint
// protocol gives a *NoEndpointError.
func Dial(ctx context.Context, socket string, h Hello) (*Conn, error) {
	var d net.Dialer
	nc, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		return nil, err
	}
	c, err := handshake(ctx, nc, h)
	if err != nil {
		nc.Close()
		return nil, err
	}
	return c, nil
}

func handshake(ctx context.Context, nc net.Conn, h Hello) (*Conn, error) {
	// Ending ctx interrupts the reads; ctx.Err is set by then.
	stop := context.AfterFunc(ctx, func() { nc.SetDeadline(time.Unix(1, 0)) })
	c := &Conn{
		nc:      nc,
		r:       bufio.NewReaderSize(nc, 64<<10),
		events:  make(chan Message, 64),
		closing: make(chan struct{}),
		done:    make(chan struct{}),
		slot:    make(chan struct{}, 1),
		pings:   map[string]chan struct{}{},
	}
	queued, err := c.hello(h)
	if !stop() {
		return nil, ctx.Err()
	}
	if err != nil {
		return nil, err
	}
	go c.run(queued)
	return c, nil
}

// hello sends the hello, checks the welcome and reads up to the first
// snapshot. It returns the messages read after the welcome.
func (c *Conn) hello(h Hello) ([]Message, error) {
	data, _ := json.Marshal(h.json()) // plain fields; cannot fail
	if err := c.SendControl(kindHello, string(data)); err != nil {
		return nil, err
	}
	p, err := wire.ReadFrame(c.r, wire.MaxFrame)
	if err != nil {
		if closedEarly(err) {
			return nil, &NoEndpointError{Message: "the server closed the connection without a welcome"}
		}
		return nil, err
	}
	if err := c.readWelcome(p); err != nil {
		return nil, err
	}
	var queued []Message
	for {
		p, err := wire.ReadFrame(c.r, wire.MaxGraphicsFrame)
		if err != nil {
			return nil, err
		}
		m, err := decodeMessage(p)
		if err != nil {
			return nil, err
		}
		switch m := m.(type) {
		case *responseChunk:
			continue
		case *Shutdown:
			return nil, fmt.Errorf("herdr: server shut down: %s", m.Reason)
		case *Snapshot:
			c.bootID = m.BootID
			return append(queued, m), nil
		}
		queued = append(queued, m)
	}
}

func (c *Conn) readWelcome(p []byte) error {
	d := wire.NewDec(p)
	switch tag := d.U32(); tag {
	case sWelcome: // the direct terminal protocol's Welcome
		d.U32()       // version
		variant(d, 2) // encoding
		msg := getOptStr(d)
		if d.Done() != nil {
			return fmt.Errorf("%w %d", wire.ErrMalformed, tag)
		}
		return &NoEndpointError{Message: msg}
	case sControl:
		kind, data := d.Str(), d.Str()
		if d.Done() != nil {
			return fmt.Errorf("%w %d", wire.ErrMalformed, tag)
		}
		if kind != kindWelcome {
			return fmt.Errorf("herdr: expected %s, got %s", kindWelcome, kind)
		}
		if err := json.Unmarshal([]byte(data), &c.Welcome); err != nil {
			return fmt.Errorf("herdr: invalid %s: %w", kindWelcome, err)
		}
		return c.Welcome.check()
	default:
		return fmt.Errorf("herdr: expected %s, got message %d", kindWelcome, tag)
	}
}

// closedEarly reports whether err means the peer closed the connection.
func closedEarly(err error) bool {
	return errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) ||
		errors.Is(err, syscall.ECONNRESET)
}

// Events delivers every server message except Call results and the pongs
// that answer HealthPing. It closes when the connection ends; Err then says
// why.
func (c *Conn) Events() <-chan Message { return c.events }

// Err returns why Events closed: net.ErrClosed after Close, io.EOF when the
// server closed the connection, or the read or decode error. It is nil until
// the read loop ends.
func (c *Conn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// BootID returns the boot_id of the latest snapshot. Call sends it with
// each request; the server answers stale_boot when it has restarted since.
func (c *Conn) BootID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bootID
}

// Close closes the connection and waits for the read loop to end. Pending
// Calls and HealthPings return an error, and Events closes.
func (c *Conn) Close() error {
	c.closeOnce.Do(func() {
		close(c.closing)
		c.nc.Close()
	})
	<-c.done
	return nil
}

func (c *Conn) closedErr() error {
	return fmt.Errorf("herdr: endpoint connection closed: %w", c.Err())
}

func (c *Conn) run(queued []Message) {
	err := c.loop(queued)
	c.nc.Close()
	c.mu.Lock()
	c.err = err
	c.mu.Unlock()
	close(c.events)
	close(c.done)
}

func (c *Conn) loop(queued []Message) error {
	for _, m := range queued {
		if !c.deliver(m) {
			return net.ErrClosed
		}
	}
	for {
		p, err := wire.ReadFrame(c.r, wire.MaxGraphicsFrame)
		if err != nil {
			select {
			case <-c.closing:
				return net.ErrClosed
			default:
				return err
			}
		}
		m, err := decodeMessage(p)
		if err != nil {
			return err
		}
		if c.consume(m) {
			continue
		}
		if !c.deliver(m) {
			return net.ErrClosed
		}
	}
}

func (c *Conn) deliver(m Message) bool {
	select {
	case c.events <- m:
		return true
	case <-c.closing:
		return false
	}
}

// consume handles response chunks and awaited pongs, which Events does not
// carry, and notes each snapshot's boot ID.
func (c *Conn) consume(m Message) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	switch m := m.(type) {
	case *responseChunk:
		k := c.call
		if k == nil || k.id != m.RequestID {
			return true // the answer to a request nobody waits for
		}
		k.buf = append(k.buf, m.Data...)
		if m.Final {
			c.call = nil
			k.resp <- k.buf
			<-c.slot
		}
		return true
	case *Control:
		if ch, ok := c.pings[m.Data]; ok && m.Kind == kindPong {
			delete(c.pings, m.Data)
			close(ch)
			return true
		}
	case *Snapshot:
		c.bootID = m.BootID
	}
	return false
}

// Call invokes an API method through the endpoint and decodes the result
// into out (which may be nil). params is the method's parameters; nil sends
// {}. Errors from Herdr are *jsonapi.Error, among them stale_boot and
// surface_inactive (an inactive connection may only call
// client_shell.surface.set). A method missing from Welcome.Methods fails
// with the code Herdr would answer, unsupported_method, without being sent.
//
// Herdr runs one request per connection at a time, so Call waits for the
// previous one. A Call whose ctx ends returns at once, but the next Call
// still waits for Herdr to answer the abandoned request.
func (c *Conn) Call(ctx context.Context, method string, params, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !slices.Contains(c.Welcome.Methods, method) {
		return &jsonapi.Error{Code: "unsupported_method",
			Message: fmt.Sprintf("method %q is not available through this endpoint", method)}
	}
	if params == nil {
		params = struct{}{}
	}
	id := fmt.Sprintf("e%d", c.n.Add(1))
	req, err := json.Marshal(struct {
		ID     string `json:"id"`
		Method string `json:"method"`
		Params any    `json:"params"`
	}{id, method, params})
	if err != nil {
		return err
	}
	if len(req) > maxRequest {
		return fmt.Errorf("herdr: request of %d bytes exceeds the %d byte limit", len(req), maxRequest)
	}
	select {
	case c.slot <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return c.closedErr()
	}
	select {
	case <-c.done:
		<-c.slot
		return c.closedErr()
	default:
	}
	k := &call{id: id, resp: make(chan []byte, 1)}
	c.mu.Lock()
	c.call = k
	boot := c.bootID
	c.mu.Unlock()
	if err := c.send(encRequest(boot, string(req))); err != nil {
		c.mu.Lock()
		if c.call == k {
			c.call = nil
			<-c.slot
		}
		c.mu.Unlock()
		return err
	}
	var data []byte
	select {
	case data = <-k.resp:
	case <-ctx.Done():
		return ctx.Err()
	case <-c.done:
		return c.closedErr()
	}
	var r struct {
		ID     string          `json:"id"`
		Result json.RawMessage `json:"result"`
		Error  *jsonapi.Error  `json:"error"`
	}
	if err := json.Unmarshal(data, &r); err != nil {
		return fmt.Errorf("herdr: invalid endpoint response: %w", err)
	}
	if r.Error != nil {
		return r.Error
	}
	if r.ID != id {
		return fmt.Errorf("herdr: endpoint response id %q for request %q", r.ID, id)
	}
	if out != nil {
		return json.Unmarshal(r.Result, out)
	}
	return nil
}

// SetSurfaceActive turns the surface on or off (client_shell.surface.set).
// An active connection receives Surface and SurfacePatch messages, can send
// pane input and claims its tab's size; Resize sets the surface size first
// if the Hello's was not right.
func (c *Conn) SetSurfaceActive(ctx context.Context, active bool) error {
	var r struct {
		Active bool `json:"active"`
	}
	if err := c.Call(ctx, "client_shell.surface.set", map[string]bool{"active": active}, &r); err != nil {
		return err
	}
	if r.Active != active {
		return fmt.Errorf("herdr: client_shell.surface.set answered active=%v", r.Active)
	}
	return nil
}

// HealthPing sends a health check and waits for its pong. The server answers
// it without entering its state loop. It needs the health_check capability.
func (c *Conn) HealthPing(ctx context.Context) error {
	if !slices.Contains(c.Welcome.Capabilities, capHealthCheck) {
		return errors.New("herdr: server does not advertise " + capHealthCheck)
	}
	token := fmt.Sprintf("hx-%d", c.n.Add(1))
	ch := make(chan struct{})
	c.mu.Lock()
	c.pings[token] = ch
	c.mu.Unlock()
	forget := func() {
		c.mu.Lock()
		delete(c.pings, token)
		c.mu.Unlock()
	}
	if err := c.SendControl(kindPing, token); err != nil {
		forget()
		return err
	}
	select {
	case <-ch:
		return nil
	case <-ctx.Done():
		forget()
		return ctx.Err()
	case <-c.done:
		return c.closedErr()
	}
}
