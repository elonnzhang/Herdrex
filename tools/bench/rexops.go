package main

import (
	"context"
	"fmt"
	"time"
)

// changes times the methods that change the session, each ops times. Each
// loop leaves the layout as it found it: a | b in window win.
func (s *rexSession) changes(ctx context.Context, r *results) {
	timed := func(name string, f func() error) bool {
		t0 := time.Now()
		if err := f(); !r.note(name, err) {
			return false
		}
		r.add(name, time.Since(t0))
		return true
	}
	for i := range *ops {
		var sid string
		if !timed("create session", func() (err error) {
			sid, _, _, err = s.create(ctx, fmt.Sprintf("%s-%d", s.label, i))
			return err
		}) {
			break
		}
		time.Sleep(shellStart)
		if !timed("destroy session", func() error { return s.destroy(sid) }) {
			break
		}
	}
	r.timed("rename session", *ops, func() error {
		return s.call(ctx, "session.set_label", map[string]any{"label": s.label}, nil)
	})
	for i := range *ops {
		var w struct {
			WindowID string `json:"window_id"`
		}
		ok := timed("new window", func() error {
			return s.call(ctx, "session.new_window", map[string]any{"window_label": "w", "layout": shell, "focus": false}, &w)
		}) && timed("rename window", func() error {
			return s.call(ctx, "session.set_window_label", map[string]any{"window_id": w.WindowID, "label": fmt.Sprint("w", i)}, nil)
		}) && timed("focus window", func() error {
			return s.call(ctx, "session.focus_window", map[string]any{"window_id": w.WindowID}, nil)
		}) && timed("focus window", func() error {
			return s.call(ctx, "session.focus_window", map[string]any{"window_id": s.win}, nil)
		})
		time.Sleep(shellStart)
		if !ok || !timed("close window", func() error {
			return s.call(ctx, "session.close_window", map[string]any{"window_id": w.WindowID}, nil)
		}) {
			break
		}
	}
	for range *ops {
		var sp struct {
			BlockIDs []string `json:"block_ids"`
		}
		if !timed("split", func() error { return s.c.Call(ctx, "session.new_split", s.split(s.b), &sp) }) {
			break
		}
		time.Sleep(shellStart)
		if len(sp.BlockIDs) == 0 || !timed("close block", func() error {
			return s.call(ctx, "block.close", map[string]any{"block_id": sp.BlockIDs[0]}, nil)
		}) {
			break
		}
	}
	type step struct {
		name, method string
		p            func(i int) map[string]any
	}
	for _, m := range []step{
		{"resize split", "session.resize_pane", func(i int) map[string]any {
			return map[string]any{
				"direction":       "horizontal",
				"before_block_id": s.a,
				"after_block_id":  s.b,
				"ratio":           0.3 + 0.01*float64(i%10),
			}
		}},
		{"focus block", "session.focus_block", func(i int) map[string]any {
			return map[string]any{"block_id": []string{s.a, s.b}[i%2]}
		}},
		{"focus next pane", "session.focus_next_pane", func(int) map[string]any {
			return map[string]any{"wrap": true}
		}},
		{"focus direction", "session.focus_direction", func(i int) map[string]any {
			return map[string]any{"direction": []string{"left", "right"}[i%2], "wrap": true}
		}},
		{"rename block", "session.set_block_label", func(i int) map[string]any {
			return map[string]any{"block_id": s.b, "label": fmt.Sprint("b", i)}
		}},
		{"zoom block", "session.zoom_block", func(i int) map[string]any {
			return map[string]any{"block_id": s.b, "zoom": i%2 == 0}
		}},
		{"swap blocks", "session.swap_blocks", func(int) map[string]any {
			return map[string]any{"block_id": s.a, "other_block_id": s.b}
		}},
		{"float block", "session.float_block", func(int) map[string]any {
			return map[string]any{"block_id": s.b, "bounds": []float64{0.1, 0.1, 0.5, 0.5}}
		}},
	} {
		for i := range *ops {
			if !timed(m.name, func() error { return s.call(ctx, m.method, m.p(i), nil) }) {
				break
			}
			if m.name == "float block" && !timed("dock block", func() error {
				return s.call(ctx, "session.move_block", map[string]any{
					"block_id":        s.b,
					"anchor_block_id": s.a,
					"direction":       "horizontal",
					"side":            "after",
				}, nil)
			}) {
				break
			}
		}
	}
	s.call(ctx, "session.zoom_block", map[string]any{"block_id": s.b, "zoom": false}, nil)
}

// events times how long a change takes to reach another client.
func (s *rexSession) events(ctx context.Context, r *results, socket string) {
	label := func(i int) string { return fmt.Sprintf("ev-%d", i) }
	labelled := func(want string) func(evt) bool {
		return func(e evt) bool { return e.typ == "window_label_changed" && e.str("label") == want }
	}
	r.timedEach("event to other client", *ops, func(i int) error {
		s.watch.drain()
		if err := s.call(ctx, "session.set_window_label", map[string]any{"window_id": s.win, "label": label(i)}, nil); err != nil {
			return err
		}
		return s.watch.wait(labelled(label(i)))
	})
	if socket == "" {
		return
	}
	h := herdrClient(socket)
	r.timedEach("herdr change to rex event", *ops, func(i int) error {
		s.watch.drain()
		if err := h.Call(ctx, "tab.rename", map[string]any{"tab_id": s.herdrTab, "label": label(1000 + i)}, nil); err != nil {
			return err
		}
		return s.watch.wait(labelled(label(1000 + i)))
	})
	defer releaseAgent(ctx, h, s.herdrA)
	r.timedEach("agent status to client", *ops, func(i int) error {
		s.watch.drain()
		if err := reportAgent(ctx, h, s.herdrA, i); err != nil {
			return err
		}
		return s.watch.wait(func(e evt) bool {
			return e.typ == "block_event" &&
				e.str("block_id") == s.a &&
				e.str("name") == "program_status_changed" &&
				e.str("payload", "record", "state") == agentStates[i%len(agentStates)]
		})
	})
}
