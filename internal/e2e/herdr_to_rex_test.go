package e2e

import (
	"fmt"
	"strings"
	"testing"
)

// step is one change in a scenario. Steps share state, so a scenario stops
// at the first failing step.
type step struct {
	name string
	run  func(t *testing.T)
}

func runSteps(t *testing.T, steps []step) {
	for _, s := range steps {
		if !t.Run(s.name, s.run) {
			return
		}
	}
}

// TestHerdrToRex changes a workspace through herdr and checks, after the
// bridge's events, that rex reads the same state.
func TestHerdrToRex(t *testing.T) {
	e := start(t)
	var sid, ws, tab1, tab2, p1, p2, p3 string
	block := func(pane string) string { return e.ids.Rex("block", pane) }
	// change runs op, then waits for an event after which want holds and
	// Rex agrees with Herdr.
	change := func(t *testing.T, op func(), want func() error) {
		t.Helper()
		e.drain()
		op()
		e.pushed(t, func() error {
			if want != nil {
				if err := want(); err != nil {
					return err
				}
			}
			return e.agree(t, sid)
		})
	}
	split := func(t *testing.T, pane, dir, ratio string) string {
		var r struct {
			Pane hPane `json:"pane"`
		}
		e.h(t, &r, "pane", "split", pane, "--direction", dir, "--ratio", ratio, "--no-focus")
		return r.Pane.PaneID
	}

	runSteps(t, []step{
		{"workspace create", func(t *testing.T) {
			change(t, func() {
				var r struct {
					Workspace hWorkspace `json:"workspace"`
					Tab       hTab       `json:"tab"`
					Root      hPane      `json:"root_pane"`
				}
				e.h(t, &r, "workspace", "create", "--label", "h2r", "--no-focus")
				ws, tab1, p1 = r.Workspace.WorkspaceID, r.Tab.TabID, r.Root.PaneID
				sid = e.ids.Rex("session", ws)
			}, func() error {
				if got := e.sessions(t)[sid]; got != "h2r" {
					return fmt.Errorf("session label %q, want h2r", got)
				}
				return nil
			})
		}},
		{"workspace rename", func(t *testing.T) {
			change(t, func() { e.h(t, nil, "workspace", "rename", ws, "h2r-renamed") }, func() error {
				if got := e.sessions(t)[sid]; got != "h2r-renamed" {
					return fmt.Errorf("session label %q, want h2r-renamed", got)
				}
				return nil
			})
		}},
		{"split right", func(t *testing.T) {
			change(t, func() { p2 = split(t, p1, "right", "0.3") }, nil)
		}},
		{"split down", func(t *testing.T) {
			change(t, func() { p3 = split(t, p2, "down", "0.6") }, nil)
		}},
		{"resize", func(t *testing.T) {
			change(t, func() {
				e.h(t, nil, "pane", "resize", "--pane", p1, "--direction", "right", "--amount", "0.1")
			}, func() error {
				if l := e.layout(t, ws, tab1); !hasSplit(l, "right", 0.4) {
					return fmt.Errorf("Herdr splits %v, want right 0.4", l.Splits)
				}
				return nil
			})
		}},
		{"rename pane", func(t *testing.T) {
			change(t, func() { e.h(t, nil, "pane", "rename", p2, "logs") }, func() error {
				for _, b := range e.view(t, sid).Windows[0].Layers[0].Blocks {
					if b.BlockID == block(p2) && b.Label != "logs" {
						return fmt.Errorf("block label %q, want logs", b.Label)
					}
				}
				return nil
			})
		}},
		{"focus", func(t *testing.T) {
			change(t, func() { e.h(t, nil, "pane", "focus", "--pane", p1, "--direction", "right") }, func() error {
				if f := e.layout(t, ws, tab1).FocusedPaneID; f == p1 {
					return fmt.Errorf("Herdr focus stayed on %s", f)
				}
				return nil
			})
		}},
		{"zoom on", func(t *testing.T) {
			change(t, func() { e.h(t, nil, "pane", "zoom", p3, "--on") }, func() error {
				if z := deref(e.view(t, sid).Windows[0].ZoomedBlockID); z != block(p3) {
					return fmt.Errorf("zoomed block %q, want %s", z, block(p3))
				}
				return nil
			})
		}},
		{"zoom off", func(t *testing.T) {
			change(t, func() { e.h(t, nil, "pane", "zoom", p3, "--off") }, nil)
		}},
		{"swap", func(t *testing.T) {
			before := rectOf(e.layout(t, ws, tab1), p3)
			change(t, func() { e.h(t, nil, "pane", "swap", "--source-pane", p1, "--target-pane", p3) }, func() error {
				if got := rectOf(e.layout(t, ws, tab1), p1); got != before {
					return fmt.Errorf("Herdr rect of %s %v, want %v", p1, got, before)
				}
				return nil
			})
		}},
		{"output", func(t *testing.T) {
			if _, err := e.herdr.Run(t.Context(), "pane", "run", p1, "echo h2r-$((6*7))"); err != nil { // prints nothing
				t.Fatal(err)
			}
			eventually(t, func() error {
				out := e.rex(t, nil, "capture", "-s", sid, "-b", block(p1), "--trim")
				if !strings.Contains(out, "h2r-42") {
					return fmt.Errorf("rex capture has no h2r-42:\n%s", out)
				}
				return nil
			})
		}},
		{"tab create", func(t *testing.T) {
			change(t, func() {
				var r struct {
					Tab hTab `json:"tab"`
				}
				e.h(t, &r, "tab", "create", "--workspace", ws, "--label", "second", "--focus")
				tab2 = r.Tab.TabID
			}, func() error {
				if n := len(e.view(t, sid).Windows); n != 2 {
					return fmt.Errorf("%d windows, want 2", n)
				}
				return nil
			})
		}},
		{"tab rename", func(t *testing.T) {
			change(t, func() { e.h(t, nil, "tab", "rename", tab2, "third") }, nil)
		}},
		{"tab focus", func(t *testing.T) {
			change(t, func() { e.h(t, nil, "tab", "focus", tab1) }, nil)
		}},
		{"pane close", func(t *testing.T) {
			change(t, func() { e.h(t, nil, "pane", "close", p3) }, func() error {
				if n := len(e.view(t, sid).Windows[0].Layers[0].Blocks); n != 2 {
					return fmt.Errorf("%d blocks, want 2", n)
				}
				return nil
			})
		}},
		{"tab close", func(t *testing.T) {
			change(t, func() { e.h(t, nil, "tab", "close", tab2) }, nil)
		}},
		{"workspace close", func(t *testing.T) {
			e.drain()
			e.h(t, nil, "workspace", "close", ws)
			e.pushed(t, func() error {
				if _, ok := e.sessions(t)[sid]; ok {
					return fmt.Errorf("rex still lists %s", sid)
				}
				return nil
			})
		}},
	})
}
