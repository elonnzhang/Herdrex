package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/elonnzhang/herdrex/pkg/rex"
	"github.com/elonnzhang/herdrex/pkg/rex/client"
)

var shell = map[string]any{
	"block": map[string]any{
		"flavor": rex.ShellFlavor,
	},
}

// rexSession is the session a Rex target is measured in: window win holds
// blocks a and b side by side.
type rexSession struct {
	c        *client.Conn
	sid      string
	label    string
	win      string
	a, b     string
	watch    *watch // a second connection, attached to every session
	herdrTab string // with a Herdr socket: the Herdr tab of win
	herdrA   string // and the Herdr pane of a
}

// rexBench measures a Rex host through the Rex protocols, as Rex.app and
// the rex command line use them. socket, when set, is the Herdr behind a
// herdrex, for the measures that start on the Herdr side.
func rexBench(ctx context.Context, r *results, url, socket string) error {
	r.timed("connect", *ops, func() error {
		c, err := dial(ctx, url)
		if err == nil {
			c.Close()
		}
		return err
	})
	r.timed("connect + bootstrap", *ops, func() error {
		c, err := dial(ctx, url)
		if err != nil {
			return err
		}
		defer c.Close()
		return c.Call(ctx, "session.bootstrap", map[string]any{"event_scope": "all"}, nil)
	})

	c, err := dial(ctx, url)
	if err != nil {
		return err
	}
	defer c.Close()
	go func() { // keep the event queue moving
		for range c.Events() {
		}
	}()
	s := &rexSession{
		c:     c,
		label: fmt.Sprintf("bench-%d", time.Now().UnixNano()),
	}
	s.sid, s.win, s.a, err = s.create(ctx, s.label)
	if err != nil {
		return err
	}
	defer s.destroy(s.sid)
	var split struct {
		BlockIDs []string `json:"block_ids"`
	}
	if err := c.Call(ctx, "session.new_split", s.split(s.a), &split); err != nil {
		return fmt.Errorf("session.new_split: %w", err)
	}
	if len(split.BlockIDs) == 0 {
		return fmt.Errorf("session.new_split gave no block")
	}
	s.b = split.BlockIDs[0]
	w, err := dial(ctx, url)
	if err != nil {
		return err
	}
	defer w.Close()
	if err := w.Call(ctx, "session.attach_all", map[string]any{"event_scope": "all"}, nil); err != nil {
		return fmt.Errorf("session.attach_all: %w", err)
	}
	s.watch = newWatch(w.Events())
	if socket != "" {
		if s.herdrTab, s.herdrA, err = herdrPlace(ctx, socket, s.label); err != nil {
			return err
		}
	}

	s.reads(ctx, r)
	s.changes(ctx, r)
	s.events(ctx, r, socket)
	return s.data(ctx, r)
}

func dial(ctx context.Context, url string) (*client.Conn, error) {
	return client.Dial(ctx, url, &client.Options{
		Kind: "cli",
		Name: "bench",
	})
}

// create makes a session with one shell and attaches to it.
func (s *rexSession) create(ctx context.Context, label string) (sid, win, block string, err error) {
	var created struct {
		SessionID      string `json:"session_id"`
		InitialWindows []struct {
			WindowID string   `json:"window_id"`
			BlockIDs []string `json:"block_ids"`
		} `json:"initial_windows"`
	}
	err = s.c.Call(ctx, "session.create", map[string]any{
		"label": label,
		"initial_windows": []any{
			map[string]any{"layout": shell},
		},
	}, &created)
	if err != nil {
		return "", "", "", fmt.Errorf("session.create: %w", err)
	}
	if len(created.InitialWindows) == 0 || len(created.InitialWindows[0].BlockIDs) == 0 {
		return "", "", "", fmt.Errorf("session.create gave no block: %+v", created)
	}
	if err := s.c.Call(ctx, "session.attach", map[string]any{"session_id": created.SessionID}, nil); err != nil {
		return "", "", "", fmt.Errorf("session.attach: %w", err)
	}
	w := created.InitialWindows[0]
	return created.SessionID, w.WindowID, w.BlockIDs[0], nil
}

func (s *rexSession) destroy(sid string) error {
	return s.c.Call(context.Background(), "session.destroy", map[string]any{"session_id": sid}, nil)
}

// split is the session.new_split payload that puts a shell right of block.
func (s *rexSession) split(block string) map[string]any {
	return map[string]any{
		"session_id": s.sid,
		"anchor_id":  block,
		"direction":  "horizontal",
		"side":       "after",
		"layout":     shell,
	}
}

// call is a call with the session in its payload.
func (s *rexSession) call(ctx context.Context, method string, p map[string]any, out any) error {
	q := map[string]any{"session_id": s.sid}
	for k, v := range p {
		q[k] = v
	}
	return s.c.Call(ctx, method, q, out)
}

// terminal calls a terminal method of block a.
func (s *rexSession) terminal(ctx context.Context, method string, args map[string]any) error {
	if args == nil {
		args = map[string]any{}
	}
	return s.call(ctx, "block.method", map[string]any{
		"block_id": s.a,
		"method":   method,
		"args":     args,
	}, nil)
}

// reads times the methods that only read.
func (s *rexSession) reads(ctx context.Context, r *results) {
	for _, m := range []struct {
		name, method string
		p            map[string]any
	}{
		{"status", "server.status", nil},
		{"version", "server.version", nil},
		{"list sessions", "session.list", nil},
		{"view session", "session.view", map[string]any{}},
		{"list blocks", "session.list_blocks", map[string]any{}},
		{"describe block", "session.describe_block", map[string]any{"block_id": s.a}},
		{"list clients", "session.list_clients", map[string]any{}},
		{"list creators", "block_creator.list", nil},
		{"resolve target", "target.resolve", map[string]any{"kinds": []string{"block"}, "queries": []string{s.a}}},
	} {
		r.timed(m.name, *n, func() error {
			if m.p == nil {
				return s.c.Call(ctx, m.method, nil, nil)
			}
			return s.call(ctx, m.method, m.p, nil)
		})
	}
	for _, m := range []struct {
		name, method string
		args         map[string]any
	}{
		{"read screen", "format", map[string]any{"format": "text"}},
		{"terminal title", "title", nil},
		{"terminal pwd", "pwd", nil},
		{"terminal size", "size", nil},
		{"process", "process", nil},
		{"program status", "program_status", nil},
	} {
		r.timed(m.name, *n, func() error { return s.terminal(ctx, m.method, m.args) })
	}
}

// sessionEvent is the inner event of a session_event, or false.
func sessionEvent(e rex.Event) (rex.Event, bool) {
	var se rex.SessionEvent
	if e.Type != "session_event" || json.Unmarshal(e.Payload, &se) != nil {
		return rex.Event{}, false
	}
	return se.Event, true
}
