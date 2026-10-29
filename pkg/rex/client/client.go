// Package client is a Rex client for rex.control.v1 and
// rex.data.terminal.v1 (docs/rex-api.md). It works with the real Rex server
// over its Unix socket and with any server on HTTP, such as the bridge.
package client

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/coder/websocket"

	"github.com/elonnzhang/herdrex/pkg/rex"
)

// Options fill the hello's client object.
type Options struct {
	Kind    string // app, cli, agent or other (the default)
	Name    string // defaults to "rex-go"
	Version string
	Label   string
}

// Event is an event message. Seq counts the connection's events from 1.
type Event struct {
	Seq uint64
	rex.Event
}

// CloseError is how the server closed a connection.
type CloseError struct {
	Code   int
	Reason string
}

func (e *CloseError) Error() string { return fmt.Sprintf("closed: %d %s", e.Code, e.Reason) }

// Conn is a control connection.
type Conn struct {
	// Welcome is the server's first message: the client ID, the limits and
	// the API revision of the connection.
	Welcome rex.Welcome

	base string       // ws:// or wss:// URL of the server
	http *http.Client // dials the Unix socket, or nil
	ws   *websocket.Conn
	n    atomic.Uint64

	mu      sync.Mutex
	cond    *sync.Cond
	pending map[string]chan rex.Response
	queue   []Event
	err     error // why the connection ended
	ended   bool
	closed  bool          // Close was called
	closing chan struct{} // closed by Close
	events  chan Event
	done    chan struct{}
}

// Dial connects to a server and does the hello. server is unix:///path for
// a Unix socket (as rex server status prints it), http://host:port or
// https://host:port.
func Dial(ctx context.Context, server string, opts *Options) (*Conn, error) {
	c := &Conn{pending: map[string]chan rex.Response{}, events: make(chan Event),
		closing: make(chan struct{}), done: make(chan struct{})}
	c.cond = sync.NewCond(&c.mu)
	u, err := url.Parse(server)
	if err != nil {
		return nil, err
	}
	switch u.Scheme {
	case "unix":
		path := u.Path
		c.base = "ws://rex"
		c.http = &http.Client{Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", path)
			}}}
	case "http", "ws":
		c.base = "ws://" + u.Host
	case "https", "wss":
		c.base = "wss://" + u.Host
	default:
		return nil, fmt.Errorf("rex server %q: want unix, http or https", server)
	}
	c.ws, _, err = websocket.Dial(ctx, c.base+"/api/control", &websocket.DialOptions{
		HTTPClient: c.http, Subprotocols: []string{rex.ControlProtocol}})
	if err != nil {
		return nil, err
	}
	if err := c.hello(ctx, opts); err != nil {
		c.ws.CloseNow()
		return nil, err
	}
	go c.read()
	go c.pump()
	return c, nil
}

func (c *Conn) hello(ctx context.Context, opts *Options) error {
	if opts == nil {
		opts = &Options{}
	}
	info := map[string]any{"kind": opts.Kind, "platform": platform(), "name": opts.Name}
	if opts.Kind == "" {
		info["kind"] = "other"
	}
	if opts.Name == "" {
		info["name"] = "rex-go"
	}
	if opts.Version != "" {
		info["version"] = opts.Version
	}
	if opts.Label != "" {
		info["label"] = opts.Label
	}
	b, _ := json.Marshal(rex.Hello{Type: "hello", APIRevision: rex.APIRevision, Client: info})
	if err := c.ws.Write(ctx, websocket.MessageText, b); err != nil {
		return err
	}
	_, data, err := c.ws.Read(ctx)
	if err != nil {
		return closeErr(err)
	}
	if err := json.Unmarshal(data, &c.Welcome); err != nil {
		return err
	}
	if c.Welcome.Type != "welcome" || c.Welcome.Protocol != rex.ControlProtocol {
		return fmt.Errorf("first message is %.100s, not a welcome", data)
	}
	limit := c.Welcome.Limits.MaxMessageBytes
	if limit <= 0 {
		limit = rex.MaxMessageBytes
	}
	c.ws.SetReadLimit(int64(limit))
	return nil
}

func platform() string {
	switch runtime.GOOS {
	case "darwin":
		return "macos"
	case "linux", "windows", "freebsd":
		return runtime.GOOS
	}
	return "other"
}

// Call sends a request and decodes the result into out, which may be nil.
// A failed request returns its *rex.Error.
func (c *Conn) Call(ctx context.Context, method string, payload, out any) error {
	r, err := c.Do(ctx, method, payload)
	if err != nil {
		return err
	}
	if r.Error != nil {
		return r.Error
	}
	if out != nil && len(r.Payload) > 0 {
		return json.Unmarshal(r.Payload, out)
	}
	return nil
}

// Do sends a request and returns the response, failed or not. To merge
// results and events, apply the result, drop events with seq up to the
// response's Seq, and apply later ones (docs/rex-api.md 2.6).
func (c *Conn) Do(ctx context.Context, method string, payload any) (*rex.Response, error) {
	id := strconv.FormatUint(c.n.Add(1), 10)
	req := map[string]any{"type": "request", "id": id, "method": method}
	if payload != nil {
		req["payload"] = payload
	}
	b, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	ch := make(chan rex.Response, 1)
	c.mu.Lock()
	if c.ended {
		err := c.err
		c.mu.Unlock()
		return nil, err
	}
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	if err := c.ws.Write(ctx, websocket.MessageText, b); err != nil {
		return nil, err
	}
	select {
	case r := <-ch:
		return &r, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-c.done:
		c.mu.Lock()
		defer c.mu.Unlock()
		return nil, c.err
	}
}

// Events delivers the connection's events in order. It closes after the
// connection ends and every event was taken, or at Close. Events queue
// without bound until taken.
func (c *Conn) Events() <-chan Event { return c.events }

// Err is why the connection ended, or nil while it is open.
func (c *Conn) Err() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.err
}

// Close ends the connection with a normal close.
func (c *Conn) Close() error {
	c.mu.Lock()
	if !c.closed {
		c.closed = true
		close(c.closing)
		c.cond.Broadcast()
	}
	c.mu.Unlock()
	return c.ws.Close(websocket.StatusNormalClosure, "")
}

func (c *Conn) read() {
	var err error
	for {
		var data []byte
		var typ websocket.MessageType
		typ, data, err = c.ws.Read(context.Background())
		if err != nil {
			break
		}
		if typ != websocket.MessageText {
			continue
		}
		var m rex.Response // an event has the same type, seq and payload fields
		if json.Unmarshal(data, &m) != nil {
			continue
		}
		switch m.Type { // other types are skipped, as the protocol asks
		case "response":
			c.mu.Lock()
			ch := c.pending[m.ID]
			c.mu.Unlock()
			if ch != nil {
				select { // buffered for one; a repeated id is dropped
				case ch <- m:
				default:
				}
			}
		case "event":
			var e rex.Event
			if json.Unmarshal(m.Payload, &e) != nil {
				continue
			}
			c.mu.Lock()
			c.queue = append(c.queue, Event{Seq: m.Seq, Event: e})
			c.cond.Broadcast()
			c.mu.Unlock()
		}
	}
	c.mu.Lock()
	c.err, c.ended = closeErr(err), true
	c.cond.Broadcast()
	c.mu.Unlock()
	close(c.done)
}

// pump hands queued events to Events, so a slow reader never holds up
// responses.
func (c *Conn) pump() {
	defer close(c.events)
	for {
		c.mu.Lock()
		for len(c.queue) == 0 && !c.ended && !c.closed {
			c.cond.Wait()
		}
		if c.closed || len(c.queue) == 0 {
			c.mu.Unlock()
			return
		}
		e := c.queue[0]
		c.queue = c.queue[1:]
		c.mu.Unlock()
		select {
		case c.events <- e:
		case <-c.closing:
			return
		}
	}
}

func closeErr(err error) error {
	var ce websocket.CloseError
	if errors.As(err, &ce) {
		return &CloseError{Code: int(ce.Code), Reason: ce.Reason}
	}
	return err
}
