package main

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"
	"time"

	"github.com/elonnzhang/herdrex/pkg/herdr/jsonapi"
	"github.com/elonnzhang/herdrex/pkg/rex/client"
)

// evt is the inner event of a session_event, its payload decoded loosely.
type evt struct {
	typ string
	p   map[string]any
}

// str is the string at path in the payload, or "".
func (e evt) str(path ...string) string {
	var v any = e.p
	for _, k := range path {
		m, _ := v.(map[string]any)
		v = m[k]
	}
	s, _ := v.(string)
	return s
}

// watch holds the session events of a second connection.
type watch struct {
	ch chan evt
}

// newWatch keeps the session events from events. When the buffer is full
// it drops them, so a slow reader cannot stall the connection.
func newWatch(events <-chan client.Event) *watch {
	w := &watch{ch: make(chan evt, 4096)}
	go func() {
		defer close(w.ch)
		for e := range events {
			se, ok := sessionEvent(e.Event)
			if !ok {
				continue
			}
			var p map[string]any
			json.Unmarshal(se.Payload, &p)
			select {
			case w.ch <- evt{typ: se.Type, p: p}:
			default:
			}
		}
	}()
	return w
}

// drain drops the events received so far.
func (w *watch) drain() {
	for {
		select {
		case <-w.ch:
		default:
			return
		}
	}
}

// wait waits for an event that match accepts.
func (w *watch) wait(match func(evt) bool) error {
	deadline := time.After(*timeout)
	for {
		select {
		case e, ok := <-w.ch:
			if !ok {
				return fmt.Errorf("events ended")
			}
			if match(e) {
				return nil
			}
		case <-deadline:
			return fmt.Errorf("no matching event within %v", *timeout)
		}
	}
}

// herdrEvent waits on a Herdr subscription for an event that match accepts.
func herdrEvent(ch <-chan jsonapi.Event, match func(jsonapi.Event) bool) error {
	deadline := time.After(*timeout)
	for {
		select {
		case e, ok := <-ch:
			if !ok {
				return fmt.Errorf("herdr events ended")
			}
			if match(e) {
				return nil
			}
		case <-deadline:
			return fmt.Errorf("no matching herdr event within %v", *timeout)
		}
	}
}

func herdrClient(socket string) *jsonapi.Client { return &jsonapi.Client{Socket: socket} }

// herdrPlace finds, in the Herdr behind a herdrex, the tab of the workspace
// labelled label and its leftmost pane: the window and block a of a session
// that has just been split once.
func herdrPlace(ctx context.Context, socket, label string) (tab, pane string, err error) {
	snap, err := herdrClient(socket).Snapshot(ctx)
	if err != nil {
		return "", "", fmt.Errorf("session.snapshot: %w", err)
	}
	i := slices.IndexFunc(snap.Workspaces, func(w jsonapi.Workspace) bool { return w.Label == label })
	if i < 0 {
		return "", "", fmt.Errorf("no herdr workspace %q", label)
	}
	ws := snap.Workspaces[i]
	for _, l := range snap.Layouts {
		if l.WorkspaceID != ws.ID || len(l.Panes) == 0 {
			continue
		}
		left := slices.MinFunc(l.Panes, func(a, b jsonapi.LayoutPane) int { return a.Rect.X - b.Rect.X })
		return l.TabID, left.PaneID, nil
	}
	return "", "", fmt.Errorf("herdr workspace %q has no panes", label)
}

// agentStates are the states the agent measures report in turn. Both stay
// as they are on a pane nobody looks at, unlike idle, which becomes done.
var agentStates = []string{"working", "blocked"}

// agent is the agent the reports name. Herdr drops, within a few hundred
// milliseconds, a report for an agent it does not know, but keeps one for
// claude while the pane lives.
const agent = "claude"

// reportAgent reports the i-th agent state on pane.
func reportAgent(ctx context.Context, h *jsonapi.Client, pane string, i int) error {
	return h.Call(ctx, "pane.report_agent", map[string]any{
		"pane_id": pane,
		"source":  "bench",
		"agent":   agent,
		"state":   agentStates[i%len(agentStates)],
	}, nil)
}

func releaseAgent(ctx context.Context, h *jsonapi.Client, pane string) error {
	return h.Call(ctx, "pane.release_agent", map[string]any{
		"pane_id": pane,
		"source":  "bench",
		"agent":   agent,
	}, nil)
}
