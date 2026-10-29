package main

import (
	"context"
	"fmt"
	"time"

	"github.com/elonnzhang/herdrex/pkg/herdr/jsonapi"
)

// herdrSession is the workspace Herdr is measured in: tab tab holds panes a
// and b side by side.
type herdrSession struct {
	h       *jsonapi.Client
	socket  string
	ws, tab string
	a, b    string
	label   string
}

// herdrBench measures Herdr itself: its JSON API for the operations, as the
// herdr command line uses it, and its terminal protocol for input and
// output, as the Herdr TUI uses it. Measures that match one of rexBench
// have its name.
func herdrBench(ctx context.Context, r *results, socket string) error {
	s := &herdrSession{
		h:      herdrClient(socket),
		socket: socket,
		label:  fmt.Sprintf("bench-%d", time.Now().UnixNano()),
	}
	var err error
	s.ws, s.tab, s.a, err = s.create(ctx, s.label)
	if err != nil {
		return err
	}
	defer s.call(context.Background(), "workspace.close", map[string]any{"workspace_id": s.ws}, nil)
	if s.b, err = s.split(ctx, s.a); err != nil {
		return err
	}
	s.reads(ctx, r)
	s.changes(ctx, r)
	s.events(ctx, r)
	return s.data(ctx, r)
}

func (s *herdrSession) call(ctx context.Context, method string, p map[string]any, out any) error {
	return s.h.Call(ctx, method, p, out)
}

// create makes a workspace in the background and returns its tab and pane.
func (s *herdrSession) create(ctx context.Context, label string) (ws, tab, pane string, err error) {
	var created struct {
		Workspace jsonapi.Workspace `json:"workspace"`
		Root      jsonapi.Pane      `json:"root_pane"`
	}
	err = s.call(ctx, "workspace.create", map[string]any{
		"label": label,
		"focus": false,
	}, &created)
	if err != nil {
		return "", "", "", fmt.Errorf("workspace.create: %w", err)
	}
	return created.Workspace.ID, created.Root.TabID, created.Root.ID, nil
}

// split puts a pane right of pane.
func (s *herdrSession) split(ctx context.Context, pane string) (string, error) {
	var sp struct {
		Pane jsonapi.Pane `json:"pane"`
	}
	err := s.call(ctx, "pane.split", map[string]any{
		"target_pane_id": pane,
		"direction":      "right",
		"focus":          false,
	}, &sp)
	if err != nil {
		return "", fmt.Errorf("pane.split: %w", err)
	}
	return sp.Pane.ID, nil
}

// reads times the methods that only read. "view session" is what the
// herdr command line needs for one workspace's tabs and layout.
func (s *herdrSession) reads(ctx context.Context, r *results) {
	for _, m := range []struct {
		name, method string
		p            map[string]any
	}{
		{"status", "ping", nil},
		{"list sessions", "workspace.list", nil},
		{"snapshot", "session.snapshot", nil},
		{"get workspace", "workspace.get", map[string]any{"workspace_id": s.ws}},
		{"list windows", "tab.list", map[string]any{"workspace_id": s.ws}},
		{"layout export", "layout.export", map[string]any{"tab_id": s.tab}},
		{"list blocks", "pane.list", map[string]any{"workspace_id": s.ws}},
		{"describe block", "pane.get", map[string]any{"pane_id": s.a}},
		{"process", "pane.process_info", map[string]any{"pane_id": s.a}},
	} {
		r.timed(m.name, *n, func() error { return s.call(ctx, m.method, m.p, nil) })
	}
	r.timed("view session", *n, func() error {
		for _, c := range []struct {
			method string
			p      map[string]any
		}{
			{"workspace.get", map[string]any{"workspace_id": s.ws}},
			{"tab.list", map[string]any{"workspace_id": s.ws}},
			{"layout.export", map[string]any{"tab_id": s.tab}},
		} {
			if err := s.call(ctx, c.method, c.p, nil); err != nil {
				return err
			}
		}
		return nil
	})
	r.timed("read screen", *n, func() error {
		_, err := s.h.ReadPane(ctx, s.a, "visible", "text")
		return err
	})
}
