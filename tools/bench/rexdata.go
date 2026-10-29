package main

import (
	"context"
	"fmt"
)

// data times the terminal streams: opening one, then output, key echo and
// paste on block a, its input written with block.method write as Rex.app
// writes it.
func (s *rexSession) data(ctx context.Context, r *results) error {
	r.timed("terminal attach", *ops, func() error {
		st, err := s.c.Terminal(ctx, s.sid, s.b)
		if err != nil {
			return err
		}
		defer st.Close()
		_, err = st.Read(ctx) // the snapshot
		return err
	})
	st, err := s.c.Terminal(ctx, s.sid, s.a)
	if err != nil {
		return fmt.Errorf("terminal: %w", err)
	}
	defer st.Close()
	out := make(chan []byte, 1024)
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		defer close(out)
		for {
			parts, err := st.Read(sctx)
			if err != nil {
				return
			}
			for _, p := range parts {
				if len(p.Output) > 0 {
					out <- p.Output
				}
			}
		}
	}()
	t := &terminal{
		out: out,
		write: func(text string) error {
			return s.terminal(ctx, "write", map[string]any{"data": text})
		},
	}
	if err := t.settle(); err != nil {
		return err
	}
	t.measure(r, "key echo")
	// client.key is what Rex.app sends first for each key press, so that
	// the server may handle it; only the round trip is timed.
	t.withCat(r, func() {
		r.timedEach("client.key", *n, func(i int) error {
			k := keyOf(i)
			return s.c.Call(ctx, "client.key", map[string]any{
				"session_id": s.sid,
				"block_id":   s.a,
				"key":        keyPress(k),
			}, nil)
		})
	})
	return nil
}

// keyPress is the client.key "key" object of pressing letter k.
func keyPress(k string) map[string]any {
	return map[string]any{
		"action":              "press",
		"key":                 "key_" + k,
		"mods":                "",
		"text":                k,
		"unshifted_codepoint": int(k[0]),
	}
}
