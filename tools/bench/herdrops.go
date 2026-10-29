package main

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/elonnzhang/herdrex/pkg/herdr"
	"github.com/elonnzhang/herdrex/pkg/herdr/jsonapi"
	"github.com/elonnzhang/herdrex/pkg/herdr/term"
)

// changes times the Herdr methods behind rexSession.changes, with the same
// measure names. Each loop leaves a | b in tab tab.
func (s *herdrSession) changes(ctx context.Context, r *results) {
	timed := func(name string, f func() error) bool {
		t0 := time.Now()
		if err := f(); !r.note(name, err) {
			return false
		}
		r.add(name, time.Since(t0))
		return true
	}
	for i := range *ops {
		var ws string
		if !timed("create session", func() (err error) {
			ws, _, _, err = s.create(ctx, fmt.Sprintf("%s-%d", s.label, i))
			return err
		}) {
			break
		}
		time.Sleep(shellStart)
		if !timed("destroy session", func() error {
			return s.call(ctx, "workspace.close", map[string]any{"workspace_id": ws}, nil)
		}) {
			break
		}
	}
	r.timed("rename session", *ops, func() error {
		return s.call(ctx, "workspace.rename", map[string]any{"workspace_id": s.ws, "label": s.label}, nil)
	})
	for i := range *ops {
		var t struct {
			Tab jsonapi.Tab `json:"tab"`
		}
		ok := timed("new window", func() error {
			return s.call(ctx, "tab.create", map[string]any{"workspace_id": s.ws, "label": "w", "focus": false}, &t)
		}) && timed("rename window", func() error {
			return s.call(ctx, "tab.rename", map[string]any{"tab_id": t.Tab.ID, "label": fmt.Sprint("w", i)}, nil)
		}) && timed("focus window", func() error {
			return s.call(ctx, "tab.focus", map[string]any{"tab_id": t.Tab.ID}, nil)
		}) && timed("focus window", func() error {
			return s.call(ctx, "tab.focus", map[string]any{"tab_id": s.tab}, nil)
		})
		time.Sleep(shellStart)
		if !ok || !timed("close window", func() error {
			return s.call(ctx, "tab.close", map[string]any{"tab_id": t.Tab.ID}, nil)
		}) {
			break
		}
	}
	for range *ops {
		var pane string
		if !timed("split", func() (err error) {
			pane, err = s.split(ctx, s.b)
			return err
		}) {
			break
		}
		time.Sleep(shellStart)
		if !timed("close block", func() error {
			return s.call(ctx, "pane.close", map[string]any{"pane_id": pane}, nil)
		}) {
			break
		}
	}
	type step struct {
		name, method string
		p            func(i int) map[string]any
	}
	for _, m := range []step{
		{"resize split", "layout.set_split_ratio", func(i int) map[string]any {
			return map[string]any{
				"tab_id": s.tab,
				"path":   []bool{},
				"ratio":  0.3 + 0.01*float64(i%10),
			}
		}},
		{"focus block", "pane.focus", func(i int) map[string]any {
			return map[string]any{"pane_id": []string{s.a, s.b}[i%2]}
		}},
		{"focus direction", "pane.focus_direction", func(i int) map[string]any {
			return map[string]any{
				"pane_id":   []string{s.b, s.a}[i%2],
				"direction": []string{"left", "right"}[i%2],
			}
		}},
		{"rename block", "pane.rename", func(i int) map[string]any {
			return map[string]any{"pane_id": s.b, "label": fmt.Sprint("b", i)}
		}},
		{"zoom block", "pane.zoom", func(i int) map[string]any {
			return map[string]any{"pane_id": s.b, "mode": []string{"on", "off"}[i%2]}
		}},
		{"swap blocks", "pane.swap", func(int) map[string]any {
			return map[string]any{"source_pane_id": s.a, "target_pane_id": s.b}
		}},
	} {
		r.timedEach(m.name, *ops, func(i int) error { return s.call(ctx, m.method, m.p(i), nil) })
	}
	s.call(ctx, "pane.zoom", map[string]any{"pane_id": s.b, "mode": "off"}, nil)
}

// events times how long a change takes to reach a subscriber.
func (s *herdrSession) events(ctx context.Context, r *results) {
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ch, _, err := s.h.Subscribe(sctx, []string{"tab.renamed"})
	if !r.note("events.subscribe", err) {
		return
	}
	r.timedEach("event to other client", *ops, func(i int) error {
		want := fmt.Sprintf("ev-%d", i)
		if err := s.call(ctx, "tab.rename", map[string]any{"tab_id": s.tab, "label": want}, nil); err != nil {
			return err
		}
		return herdrEvent(ch, func(e jsonapi.Event) bool {
			return e.Name == "tab_renamed" && field(e.Data, "label") == want
		})
	})
	ach, _, err := s.h.SubscribeTo(sctx, []map[string]any{{"type": "pane.agent_status_changed", "pane_id": s.a}})
	if !r.note("events.subscribe", err) {
		return
	}
	defer releaseAgent(ctx, s.h, s.a)
	r.timedEach("agent status to client", *ops, func(i int) error {
		if err := reportAgent(ctx, s.h, s.a, i); err != nil {
			return err
		}
		return herdrEvent(ach, func(e jsonapi.Event) bool {
			return e.Name == "pane.agent_status_changed" && field(e.Data, "agent_status") == agentStates[i%len(agentStates)]
		})
	})
}

// field is the top-level string field k of an event's data, or "".
func field(data json.RawMessage, k string) string {
	var m map[string]any
	json.Unmarshal(data, &m)
	v, _ := m[k].(string)
	return v
}

// data times the terminal protocol, as the Herdr TUI uses it: taking
// control of a pane, then output, key echo and paste on pane a. The key
// echo is timed once more with pane.send_text, the bridge's input path.
func (s *herdrSession) data(ctx context.Context, r *results) error {
	pong, err := s.h.Ping(ctx)
	if err != nil {
		return err
	}
	dial := func() (*term.Conn, error) {
		return term.Dial(ctx, herdr.ClientSocket(s.socket), pong.Protocol, term.Size{
			Cols:         120,
			Rows:         40,
			CellWidthPx:  8,
			CellHeightPx: 16,
		})
	}
	r.timed("terminal attach", *ops, func() error {
		c, err := dial()
		if err != nil {
			return err
		}
		defer c.Close()
		if err := c.Control(s.b, false); err != nil {
			return err
		}
		for {
			m, err := c.Next()
			if err != nil {
				return err
			}
			switch m.(type) {
			case *term.Frame:
				return nil
			case *term.Shutdown:
				return fmt.Errorf("shutdown before the first frame")
			}
		}
	})
	c, err := dial()
	if err != nil {
		return fmt.Errorf("terminal: %w", err)
	}
	defer c.Close()
	if err := c.Control(s.a, false); err != nil {
		return fmt.Errorf("terminal control: %w", err)
	}
	out := make(chan []byte, 1024)
	go func() {
		defer close(out)
		for {
			m, err := c.Next()
			if err != nil {
				return
			}
			switch m := m.(type) {
			case *term.Frame:
				out <- m.Bytes
			case *term.Shutdown:
				return
			}
		}
	}()
	t := &terminal{
		out:   out,
		write: func(s string) error { return c.Input([]byte(s)) },
	}
	if err := t.settle(); err != nil {
		return err
	}
	t.measure(r, "key echo")
	t.write = func(text string) error {
		return s.call(ctx, "pane.send_text", map[string]any{"pane_id": s.a, "text": text}, nil)
	}
	t.withCat(r, func() {
		r.note("key echo (send_text)", t.each(r, "key echo (send_text)", *n, t.key))
	})
	return nil
}
