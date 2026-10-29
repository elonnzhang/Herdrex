package e2e

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/elonnzhang/herdrex/pkg/rex"
)

// TestAgentStatus reports agent states through herdr and checks what Rex
// gets: program_status_changed and the program_status method for every
// state, desktop_notification only when the agent needs attention or has
// finished, and program_status_removed once the agent lets go of the pane.
func TestAgentStatus(t *testing.T) {
	e := start(t)
	var r struct {
		Workspace hWorkspace `json:"workspace"`
		Root      hPane      `json:"root_pane"`
	}
	var front struct {
		Workspace hWorkspace `json:"workspace"`
	}
	e.h(t, &r, "workspace", "create", "--label", "agents", "--no-focus")
	// Another workspace in front, so the agent's pane is not being looked
	// at: Herdr then turns idle after work into done.
	e.h(t, &front, "workspace", "create", "--label", "front", "--focus")
	pane := r.Root.PaneID
	sid, block := e.ids.Rex("session", r.Workspace.WorkspaceID), e.ids.Rex("block", pane)
	// The bridge only reports status changes of panes it already knows, so
	// wait until both workspaces have reached it.
	e.created(t, sid, e.ids.Rex("session", front.Workspace.WorkspaceID))
	// The agent commands print nothing, so they cannot go through e.h.
	run := func(t *testing.T, args ...string) {
		t.Helper()
		if _, err := e.herdr.Run(t.Context(), args...); err != nil {
			t.Fatalf("herdr %v: %v", args, err)
		}
	}
	report := func(t *testing.T, state string) {
		t.Helper()
		run(t, "pane", "report-agent", "--source", "e2e", "--agent", "claude", "--state", state, pane)
	}
	status := func(t *testing.T) []string {
		var out struct {
			Records []struct {
				State string `json:"state"`
			} `json:"records"`
		}
		e.rex(t, &out, "api", "call", "-s", sid, "com.superlogical.terminal.program_status",
			fmt.Sprintf(`{"block_id":%q}`, block))
		var states []string
		for _, rec := range out.Records {
			states = append(states, rec.State)
		}
		return states
	}

	runSteps(t, []step{
		{"working", func(t *testing.T) {
			e.drain()
			report(t, "working")
			got := e.blockEvents(t, block, "program_status_changed")
			wantState(t, got, "working")
			noNotification(t, got)
			if s := status(t); !slices.Equal(s, []string{"working"}) {
				t.Errorf("program_status %v, want [working]", s)
			}
		}},
		{"blocked", func(t *testing.T) {
			e.drain()
			report(t, "blocked")
			got := e.blockEvents(t, block, "program_status_changed", "desktop_notification")
			wantState(t, got, "blocked")
			wantNotification(t, got, "claude needs attention", "agents")
		}},
		{"working again", func(t *testing.T) {
			e.drain()
			report(t, "working")
			got := e.blockEvents(t, block, "program_status_changed")
			wantState(t, got, "working")
			noNotification(t, got)
		}},
		{"done", func(t *testing.T) {
			e.drain()
			report(t, "idle")
			got := e.blockEvents(t, block, "program_status_changed", "desktop_notification")
			wantState(t, got, "done")
			wantNotification(t, got, "claude finished", "agents")
			if s := status(t); !slices.Equal(s, []string{"done"}) {
				t.Errorf("program_status %v, want [done]", s)
			}
		}},
		{"release", func(t *testing.T) {
			e.drain()
			// herdr takes the pane before the options here.
			run(t, "pane", "release-agent", pane, "--source", "e2e", "--agent", "claude")
			got := e.blockEvents(t, block, "program_status_removed")
			noNotification(t, got)
			if s := status(t); len(s) != 0 {
				t.Errorf("program_status %v, want none", s)
			}
		}},
	})
}

// blockEvents waits until block has had a block_event of each of the
// names, and returns the block's events seen so far, by name.
func (e *env) blockEvents(t *testing.T, block string, names ...string) map[string][]json.RawMessage {
	t.Helper()
	got := map[string][]json.RawMessage{}
	deadline := time.After(wait)
	for {
		if !slices.ContainsFunc(names, func(n string) bool { return got[n] == nil }) {
			return got
		}
		select {
		case ev := <-e.events:
			if ev.Type != "session_event" {
				continue
			}
			var se rex.SessionEvent
			var be rex.BlockEvent
			if json.Unmarshal(ev.Payload, &se) != nil || se.Event.Type != "block_event" ||
				json.Unmarshal(se.Event.Payload, &be) != nil || be.BlockID != block {
				continue
			}
			got[be.Name] = append(got[be.Name], be.Payload)
		case <-deadline:
			t.Fatalf("within %v, %s had block events %v, want %v", wait, block, keys(got), names)
		}
	}
}

// created waits for a session_created event of each of the sessions.
func (e *env) created(t *testing.T, sids ...string) {
	t.Helper()
	deadline := time.After(wait)
	for len(sids) > 0 {
		select {
		case ev := <-e.events:
			if ev.Type != "session_created" {
				continue
			}
			var p struct {
				Session struct {
					SessionID string `json:"session_id"`
				} `json:"session"`
			}
			if json.Unmarshal(ev.Payload, &p) == nil {
				sids = slices.DeleteFunc(sids, func(s string) bool { return s == p.Session.SessionID })
			}
		case <-deadline:
			t.Fatalf("no session_created for %v within %v", sids, wait)
		}
	}
}

func keys(m map[string][]json.RawMessage) []string {
	var out []string
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// wantState checks that the last program_status_changed has state.
func wantState(t *testing.T, got map[string][]json.RawMessage, state string) {
	t.Helper()
	all := got["program_status_changed"]
	var p struct {
		Record struct {
			State string `json:"state"`
		} `json:"record"`
	}
	if err := json.Unmarshal(all[len(all)-1], &p); err != nil || p.Record.State != state {
		t.Errorf("program_status_changed %s, want state %s", all[len(all)-1], state)
	}
}

func wantNotification(t *testing.T, got map[string][]json.RawMessage, title, body string) {
	t.Helper()
	var p struct {
		Title string `json:"title"`
		Body  string `json:"body"`
	}
	n := got["desktop_notification"]
	if len(n) != 1 || json.Unmarshal(n[0], &p) != nil || p.Title != title || p.Body != body {
		t.Errorf("desktop_notification %s, want one with title %q and body %q", n, title, body)
	}
}

func noNotification(t *testing.T, got map[string][]json.RawMessage) {
	t.Helper()
	if n := got["desktop_notification"]; n != nil {
		t.Errorf("desktop_notification %s, want none", n)
	}
}
