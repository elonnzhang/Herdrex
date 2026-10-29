package jsonapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"sync/atomic"
)

// MaxRequest is the longest request line Herdr reads, not counting the
// newline (src/api/server.rs:34). Herdr drops a longer one without a reply.
const MaxRequest = 1 << 20

// ErrTooLarge reports a request over MaxRequest. Nothing was sent.
var ErrTooLarge = errors.New("herdr: request too large")

// Error is an error reply from Herdr. Code is a string such as
// pane_not_found or events_lost (pkg/herdr/HERDR.md §3.6).
type Error struct{ Code, Message string }

func (e *Error) Error() string { return e.Code + ": " + e.Message }

// Client calls the JSON API on the socket at Socket. It keeps no connection
// open, so goroutines can share one. Don't copy a Client after first use.
type Client struct {
	Socket string
	n      atomic.Uint64
}

type response struct {
	Result json.RawMessage `json:"result"`
	Error  *Error          `json:"error"`
}

// typed is the type field that every result carries.
type typed struct {
	Type string `json:"type"`
}

// is checks that a result has the type Herdr sends for method.
func (t typed) is(method, want string) error {
	if t.Type != want {
		return fmt.Errorf("%s: result type %q, want %q", method, t.Type, want)
	}
	return nil
}

// marshal encodes v without HTML escaping, which would grow each <, > and &
// to six bytes. Herdr reads plain JSON.
func marshal(v any) ([]byte, error) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(b.Bytes(), []byte("\n")), nil
}

// request encodes one request line, newline included.
func request(id, method string, params any) ([]byte, error) {
	p, err := marshal(params)
	if err != nil {
		return nil, err
	}
	if string(p) == "null" { // nil, or a typed nil such as a nil map
		p = []byte("{}")
	}
	line, err := marshal(map[string]any{"id": id, "method": method, "params": json.RawMessage(p)})
	if err != nil {
		return nil, err
	}
	if len(line) > MaxRequest {
		return nil, fmt.Errorf("%w: %s request of %d bytes exceeds Herdr's %d byte limit",
			ErrTooLarge, method, len(line), MaxRequest)
	}
	return append(line, '\n'), nil
}

// decode returns the result in a reply line, or the error it carries.
func decode(method string, line []byte) (json.RawMessage, error) {
	var resp response
	if err := json.Unmarshal(line, &resp); err != nil {
		return nil, err
	}
	if resp.Error != nil {
		return nil, resp.Error
	}
	if len(resp.Result) == 0 || string(resp.Result) == "null" {
		return nil, fmt.Errorf("%s: reply has neither result nor error", method)
	}
	return resp.Result, nil
}

// Call sends one request and decodes the result into out (may be nil). Nil
// params go as {}. A Herdr error comes back as *Error, and a request over
// MaxRequest as ErrTooLarge. ctx bounds the whole call, so it also ends
// methods such as events.wait that reply late.
func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	req, err := request(fmt.Sprintf("b%d", c.n.Add(1)), method, params)
	if err != nil {
		return err
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.Socket)
	if err != nil {
		return err
	}
	defer conn.Close()
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	_, err = conn.Write(req)
	var line []byte
	if err == nil {
		line, err = bufio.NewReader(conn).ReadBytes('\n')
	}
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return err
	}
	result, err := decode(method, line)
	if err != nil || out == nil {
		return err
	}
	return json.Unmarshal(result, out)
}

// Event is one pushed event from events.subscribe. Name is the kind as
// pushed: tab_renamed for lifecycle events, pane.agent_status_changed for the
// filtered ones (pkg/herdr/HERDR.md §3.5). Data holds the raw payload; treat
// it as a hint to reread state, since events and snapshots share no sequence
// number.
type Event struct {
	Name string          `json:"event"`
	Data json.RawMessage `json:"data"`
}

// Subscribe opens an event subscription to the given types, such as
// "tab.renamed". It returns once Herdr confirms the subscription. Events
// arrive on the channel until ctx ends or the connection fails; then the
// channel closes and *errp holds the reason (events_lost among others).
func (c *Client) Subscribe(ctx context.Context, types []string) (<-chan Event, *error, error) {
	subs := make([]map[string]any, len(types))
	for i, t := range types {
		subs[i] = map[string]any{"type": t}
	}
	return c.SubscribeTo(ctx, subs)
}

// SubscribeTo is Subscribe with whole subscription objects, for events that
// need a filter, such as {"type": "pane.agent_status_changed", "pane_id": p}.
// Herdr ignores a second events.subscribe on a connection, so a changed set
// needs a new subscription.
func (c *Client) SubscribeTo(ctx context.Context, subs []map[string]any) (<-chan Event, *error, error) {
	req, err := request("sub", "events.subscribe", map[string]any{"subscriptions": subs})
	if err != nil {
		return nil, nil, err
	}
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.Socket)
	if err != nil {
		return nil, nil, err
	}
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	r := bufio.NewReader(conn)
	_, err = conn.Write(req)
	var line []byte
	if err == nil {
		line, err = r.ReadBytes('\n')
	}
	if err == nil {
		err = ack(line)
	}
	if err != nil {
		stop()
		conn.Close()
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		return nil, nil, err
	}
	out := make(chan Event, 256)
	var failure error
	go func() {
		defer close(out)
		defer stop()
		defer conn.Close()
		for {
			line, err := r.ReadBytes('\n')
			if err != nil {
				failure = err
				if ctx.Err() != nil {
					failure = ctx.Err()
				}
				return
			}
			var e struct {
				Event
				Error *Error `json:"error"`
			}
			if err := json.Unmarshal(line, &e); err != nil {
				continue
			}
			if e.Error != nil {
				failure = e.Error
				return
			}
			select {
			case out <- e.Event:
			case <-ctx.Done():
				failure = ctx.Err()
				return
			}
		}
	}()
	return out, &failure, nil
}

// ack checks the reply that confirms a subscription.
func ack(line []byte) error {
	result, err := decode("events.subscribe", line)
	if err != nil {
		return err
	}
	var r typed
	if err := json.Unmarshal(result, &r); err != nil {
		return err
	}
	return r.is("events.subscribe", "subscription_started")
}

// Ping reports the server's version, its direct terminal protocol version and
// its capabilities.
func (c *Client) Ping(ctx context.Context) (*Pong, error) {
	var r struct {
		typed
		Pong
	}
	if err := c.Call(ctx, "ping", nil, &r); err != nil {
		return nil, err
	}
	if err := r.is("ping", "pong"); err != nil {
		return nil, err
	}
	return &r.Pong, nil
}

// Snapshot fetches the whole workspace/tab/pane state.
func (c *Client) Snapshot(ctx context.Context) (*Snapshot, error) {
	var r struct {
		typed
		Snapshot *Snapshot `json:"snapshot"`
	}
	if err := c.Call(ctx, "session.snapshot", nil, &r); err != nil {
		return nil, err
	}
	if err := r.is("session.snapshot", "session_snapshot"); err != nil {
		return nil, err
	}
	if r.Snapshot == nil {
		return nil, errors.New("session.snapshot: no snapshot")
	}
	return r.Snapshot, nil
}

// Layout returns a tab's split tree from layout.export. The root also holds
// the tab's focused pane and zoom state.
func (c *Client) Layout(ctx context.Context, tabID string) (*LayoutNode, error) {
	var r struct {
		typed
		Layout struct {
			Root          *LayoutNode `json:"root"`
			FocusedPaneID string      `json:"focused_pane_id"`
			Zoomed        bool        `json:"zoomed"`
		} `json:"layout"`
	}
	if err := c.Call(ctx, "layout.export", map[string]any{"tab_id": tabID}, &r); err != nil {
		return nil, err
	}
	if err := r.is("layout.export", "layout_export"); err != nil {
		return nil, err
	}
	n := r.Layout.Root
	if n == nil {
		return nil, fmt.Errorf("layout.export %s: no root", tabID)
	}
	n.Focused, n.Zoomed = r.Layout.FocusedPaneID, r.Layout.Zoomed
	return n, nil
}

// PaneRead reads a pane's screen or scrollback with pane.read.
func (c *Client) PaneRead(ctx context.Context, p PaneReadParams) (*PaneReadResult, error) {
	var r struct {
		typed
		Read *PaneReadResult `json:"read"`
	}
	if err := c.Call(ctx, "pane.read", p, &r); err != nil {
		return nil, err
	}
	if err := r.is("pane.read", "pane_read"); err != nil {
		return nil, err
	}
	if r.Read == nil {
		return nil, errors.New("pane.read: no read")
	}
	return r.Read, nil
}

// ReadPane is PaneRead with Herdr's default length: the whole visible
// screen, or the last 80 rows of a recent source. It returns only the text.
func (c *Client) ReadPane(ctx context.Context, pane, source, format string) (string, error) {
	r, err := c.PaneRead(ctx, PaneReadParams{PaneID: pane, Source: source, Format: format})
	if err != nil {
		return "", err
	}
	return r.Text, nil
}
