// Package herdr is a client for the Herdr JSON socket API.
// Herdr serves one request per connection, so each call dials anew.
package herdr

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"sync/atomic"
)

type Error struct{ Code, Message string }

func (e *Error) Error() string { return e.Code + ": " + e.Message }

type Client struct {
	Socket string
	n      atomic.Uint64
}

type response struct {
	Result json.RawMessage `json:"result"`
	Error  *Error          `json:"error"`
}

// Call sends one request and decodes the result into out (may be nil).
func (c *Client) Call(ctx context.Context, method string, params, out any) error {
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.Socket)
	if err != nil {
		return err
	}
	defer conn.Close()
	if dl, ok := ctx.Deadline(); ok {
		conn.SetDeadline(dl)
	}
	if params == nil {
		params = map[string]any{}
	}
	req, _ := json.Marshal(map[string]any{
		"id": fmt.Sprintf("b%d", c.n.Add(1)), "method": method, "params": params})
	if _, err := conn.Write(append(req, '\n')); err != nil {
		return err
	}
	line, err := bufio.NewReaderSize(conn, 1<<20).ReadBytes('\n')
	if err != nil {
		return err
	}
	var resp response
	if err := json.Unmarshal(line, &resp); err != nil {
		return err
	}
	if resp.Error != nil {
		return resp.Error
	}
	if out != nil {
		return json.Unmarshal(resp.Result, out)
	}
	return nil
}

// Event is one pushed event from events.subscribe. Data holds the raw
// payload; treat it as a hint to reread state, since events and snapshots
// share no sequence number.
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
	var d net.Dialer
	conn, err := d.DialContext(ctx, "unix", c.Socket)
	if err != nil {
		return nil, nil, err
	}
	req, _ := json.Marshal(map[string]any{"id": "sub", "method": "events.subscribe",
		"params": map[string]any{"subscriptions": subs}})
	if _, err := conn.Write(append(req, '\n')); err != nil {
		conn.Close()
		return nil, nil, err
	}
	r := bufio.NewReaderSize(conn, 1<<20)
	line, err := r.ReadBytes('\n')
	if err == nil {
		var resp response
		if err = json.Unmarshal(line, &resp); err == nil && resp.Error != nil {
			err = resp.Error
		}
	}
	if err != nil {
		conn.Close()
		return nil, nil, err
	}
	out := make(chan Event, 256)
	var failure error
	stop := context.AfterFunc(ctx, func() { conn.Close() })
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

// Snapshot fetches the whole workspace/tab/pane state.
func (c *Client) Snapshot(ctx context.Context) (*Snapshot, error) {
	var r struct {
		Snapshot Snapshot `json:"snapshot"`
	}
	if err := c.Call(ctx, "session.snapshot", nil, &r); err != nil {
		return nil, err
	}
	return &r.Snapshot, nil
}

func (c *Client) Layout(ctx context.Context, tabID string) (*LayoutNode, error) {
	var r struct {
		Layout struct {
			Root          *LayoutNode `json:"root"`
			FocusedPaneID string      `json:"focused_pane_id"`
			Zoomed        bool        `json:"zoomed"`
		} `json:"layout"`
	}
	if err := c.Call(ctx, "layout.export", map[string]any{"tab_id": tabID}, &r); err != nil {
		return nil, err
	}
	n := r.Layout.Root
	n.Focused, n.Zoomed = r.Layout.FocusedPaneID, r.Layout.Zoomed
	return n, nil
}

// ReadPane returns the visible screen as text or ANSI.
func (c *Client) ReadPane(ctx context.Context, pane, source, format string) (string, error) {
	var r struct {
		Read struct {
			Text string `json:"text"`
		} `json:"read"`
	}
	err := c.Call(ctx, "pane.read", map[string]any{
		"pane_id": pane, "source": source, "format": format}, &r)
	return r.Read.Text, err
}
