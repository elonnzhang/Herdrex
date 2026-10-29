package e2e

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// TestRexToHerdr changes a session through rex and checks that Herdr has
// the change and that the two sides then agree.
func TestRexToHerdr(t *testing.T) {
	e := start(t)
	var sid, ws, b1, b2, b3, b4, win2, w1, w2, w3 string
	paneOf := func(t *testing.T, block string) hPane {
		t.Helper()
		p := e.pane(t, ws, block)
		if p == nil {
			t.Fatalf("no Herdr pane for %s", block)
		}
		return *p
	}
	layoutOf := func(t *testing.T, block string) hLayout {
		t.Helper()
		return e.layout(t, ws, paneOf(t, block).TabID)
	}
	rect := func(t *testing.T, block string) hRect {
		t.Helper()
		return rectOf(layoutOf(t, block), paneOf(t, block).PaneID)
	}
	// after checks want on Herdr, then that Rex agrees with Herdr.
	after := func(t *testing.T, want func() error) {
		t.Helper()
		if want != nil {
			must(t, want())
		}
		must(t, e.agree(t, sid))
	}
	split := func(t *testing.T, anchor, dir, ratio string) string {
		var r struct {
			BlockIDs []string `json:"block_ids"`
		}
		e.rex(t, &r, "split", "-s", sid, "-b", anchor, "--split", dir, "--ratio", ratio, "--focus=false", "--json")
		return r.BlockIDs[0]
	}

	runSteps(t, []step{
		{"new", func(t *testing.T) {
			var r struct {
				SessionID      string `json:"session_id"`
				InitialWindows []struct {
					BlockIDs []string `json:"block_ids"`
				} `json:"initial_windows"`
			}
			e.rex(t, &r, "new", "r2h", "--json")
			sid, b1 = r.SessionID, r.InitialWindows[0].BlockIDs[0]
			w := e.workspace(t, sid)
			if w == nil || w.Label != "r2h" {
				t.Fatalf("Herdr workspace %+v, want label r2h", w)
			}
			ws = w.WorkspaceID
			after(t, nil)
		}},
		{"session rename", func(t *testing.T) {
			e.rex(t, nil, "session", "rename", sid, "r2h-renamed")
			after(t, func() error {
				if l := e.workspace(t, sid).Label; l != "r2h-renamed" {
					return fmt.Errorf("workspace label %q", l)
				}
				return nil
			})
		}},
		{"split right", func(t *testing.T) {
			b2 = split(t, b1, "right", "30")
			after(t, func() error {
				if l := layoutOf(t, b1); !hasSplit(l, "right", 0.7) || rect(t, b2).X <= rect(t, b1).X {
					return fmt.Errorf("Herdr layout %+v, want %s right of %s at 0.7", l, b2, b1)
				}
				return nil
			})
		}},
		{"split below", func(t *testing.T) {
			b3 = split(t, b2, "below", "40")
			after(t, func() error {
				if l := layoutOf(t, b2); !hasSplit(l, "down", 0.6) || rect(t, b3).Y <= rect(t, b2).Y {
					return fmt.Errorf("Herdr layout %+v, want %s below %s at 0.6", l, b3, b2)
				}
				return nil
			})
		}},
		{"split left", func(t *testing.T) {
			b4 = split(t, b1, "left", "50")
			after(t, func() error {
				if rect(t, b4).X >= rect(t, b1).X {
					return fmt.Errorf("%s is not left of %s", b4, b1)
				}
				return nil
			})
		}},
		{"resize", func(t *testing.T) {
			e.rex(t, nil, "api", "call", "-s", sid, "session.resize_pane", fmt.Sprintf(
				`{"before_block_id":%q,"after_block_id":%q,"direction":"vertical","ratio":0.25}`, b2, b3))
			after(t, func() error {
				if l := layoutOf(t, b2); !hasSplit(l, "down", 0.25) {
					return fmt.Errorf("Herdr splits %v, want down 0.25", l.Splits)
				}
				return nil
			})
		}},
		{"block rename", func(t *testing.T) {
			e.rex(t, nil, "block", "rename", "-s", sid, b2, "logs")
			after(t, func() error {
				if l := paneOf(t, b2).Label; l != "logs" {
					return fmt.Errorf("pane label %q", l)
				}
				return nil
			})
		}},
		{"focus", func(t *testing.T) {
			e.rex(t, nil, "focus", "-s", sid, b3)
			after(t, func() error {
				if f := layoutOf(t, b3).FocusedPaneID; f != paneOf(t, b3).PaneID {
					return fmt.Errorf("Herdr focused pane %s", f)
				}
				return nil
			})
		}},
		{"zoom on", func(t *testing.T) {
			e.rex(t, nil, "zoom", "-s", sid, b3, "--on")
			after(t, func() error {
				if l := layoutOf(t, b3); !l.Zoomed || l.FocusedPaneID != paneOf(t, b3).PaneID {
					return fmt.Errorf("Herdr zoomed %v on %s", l.Zoomed, l.FocusedPaneID)
				}
				return nil
			})
		}},
		{"zoom off", func(t *testing.T) {
			e.rex(t, nil, "zoom", "-s", sid, b3, "--off")
			after(t, func() error {
				if layoutOf(t, b3).Zoomed {
					return fmt.Errorf("Herdr still zoomed")
				}
				return nil
			})
		}},
		{"swap", func(t *testing.T) {
			r1, r3 := rect(t, b1), rect(t, b3)
			e.rex(t, nil, "swap", "-s", sid, b1, b3)
			after(t, func() error {
				if rect(t, b1) != r3 || rect(t, b3) != r1 {
					return fmt.Errorf("rects of %s and %s did not swap", b1, b3)
				}
				return nil
			})
		}},
		{"move", func(t *testing.T) {
			n := len(e.panes(t, ws))
			e.rex(t, nil, "move", "-s", sid, b4, "below", b2)
			// A move within a tab may give the pane a new Herdr id, and so a
			// new block id: find the pane right below b2.
			above := rect(t, b2)
			l := layoutOf(t, b2)
			b4 = ""
			for _, p := range l.Panes {
				if p.Rect.X == above.X && p.Rect.Width == above.Width && p.Rect.Y == above.Y+above.Height {
					b4 = e.ids.Rex("block", p.PaneID)
				}
			}
			after(t, func() error {
				if b4 == "" || len(e.panes(t, ws)) != n {
					return fmt.Errorf("no pane right below %s in %+v", b2, l.Panes)
				}
				return nil
			})
		}},
		{"send", func(t *testing.T) {
			e.rex(t, nil, "send", "-s", sid, "-b", b1, "echo r2h-$((6*7))\r")
			eventually(t, func() error {
				out, err := e.herdr.Run(t.Context(), "pane", "read", paneOf(t, b1).PaneID)
				if err != nil || !strings.Contains(string(out), "r2h-42") {
					return fmt.Errorf("herdr pane read has no r2h-42 (%v):\n%s", err, out)
				}
				return nil
			})
		}},
		{"window new", func(t *testing.T) {
			var r struct {
				WindowID string   `json:"window_id"`
				BlockIDs []string `json:"block_ids"`
			}
			e.rex(t, &r, "window", "new", "-s", sid, "second", "--focus=false", "--json")
			win2, w1 = r.WindowID, r.BlockIDs[0]
			after(t, func() error {
				for _, tab := range e.tabs(t, ws) {
					if e.ids.Rex("window", tab.TabID) == win2 && tab.Label == "second" {
						return nil
					}
				}
				return fmt.Errorf("no Herdr tab labeled second for %s", win2)
			})
		}},
		{"window rename", func(t *testing.T) {
			e.rex(t, nil, "window", "rename", "-s", sid, win2, "third")
			after(t, nil)
		}},
		{"window focus", func(t *testing.T) {
			e.rex(t, nil, "window", "focus", "-s", sid, win2)
			after(t, func() error {
				if a := e.workspace(t, sid).ActiveTabID; e.ids.Rex("window", a) != win2 {
					return fmt.Errorf("Herdr active tab %s", a)
				}
				return nil
			})
		}},
		{"float and dock", func(t *testing.T) {
			w2 = split(t, w1, "right", "50")
			w3 = split(t, w2, "below", "50")
			before := layoutOf(t, w1)
			e.rex(t, nil, "api", "call", "-s", sid, "session.float_block", fmt.Sprintf(
				`{"block_id":%q,"bounds":[0.1,0.1,0.5,0.5]}`, w3))
			must(t, sameShape(before, layoutOf(t, w1)))
			for _, w := range e.view(t, sid).Windows {
				if w.WindowID == win2 && (len(w.Layers) != 2 || w.Layers[1].Layout.BlockID != w3) {
					t.Fatalf("window layers %+v, want %s floating", w.Layers, w3)
				}
			}
			e.rex(t, nil, "api", "call", "-s", sid, "session.move_block", fmt.Sprintf(
				`{"block_id":%q,"anchor_block_id":%q,"direction":"vertical","side":"after"}`, w3, w2))
			after(t, func() error { return sameShape(before, layoutOf(t, w1)) })
		}},
		{"block close", func(t *testing.T) {
			e.rex(t, nil, "block", "close", "-s", sid, b3)
			after(t, func() error {
				if e.pane(t, ws, b3) != nil {
					return fmt.Errorf("Herdr still has the pane of %s", b3)
				}
				return nil
			})
		}},
		{"window close", func(t *testing.T) {
			e.rex(t, nil, "window", "close", "-s", sid, win2)
			after(t, func() error {
				if n := len(e.tabs(t, ws)); n != 1 {
					return fmt.Errorf("%d Herdr tabs, want 1", n)
				}
				return nil
			})
		}},
		{"kill", func(t *testing.T) {
			e.rex(t, nil, "kill", sid)
			if w := e.workspace(t, sid); w != nil {
				t.Fatalf("Herdr still has workspace %s", w.WorkspaceID)
			}
		}},
	})
}

// sameShape reports how two Herdr layouts of a tab differ in splits and
// pane rects.
func sameShape(a, b hLayout) error {
	key := func(l hLayout) []string {
		var out []string
		for _, s := range l.Splits {
			out = append(out, fmt.Sprintf("split %s %.4f", s.Direction, s.Ratio))
		}
		for _, p := range l.Panes {
			out = append(out, fmt.Sprintf("pane %s %v", p.PaneID, p.Rect))
		}
		slices.Sort(out)
		return out
	}
	if ka, kb := key(a), key(b); !slices.Equal(ka, kb) {
		return fmt.Errorf("Herdr layout changed:\n%v\n%v", ka, kb)
	}
	return nil
}
