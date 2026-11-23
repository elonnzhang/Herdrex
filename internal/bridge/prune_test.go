package bridge

import (
	"testing"
)

// What the bridge keeps per pane goes when the pane closes, and a closed
// workspace's revision goes with it.
func TestClosedPaneStateGoes(t *testing.T) {
	f := newFakeHerdr(t)
	f.edit(func(s *fakeState) {
		s.addWorkspace("w1", "one")
		s.addTab("w1", "w1:t1", "w1:p1", "w1:p2")
		s.addWorkspace("w2", "two")
		s.addTab("w2", "w2:t1", "w2:p1")
	})
	b, _ := newRexBridge(t, f)
	sizes := func() map[string]int {
		b.mu.Lock()
		defer b.mu.Unlock()
		return map[string]int{
			"procs": len(b.procs), "themes": len(b.themes), "owners": len(b.owners),
			"desired": len(b.desired), "statuses": len(b.statuses), "exited": len(b.exited),
			"wheel": len(b.wheel), "drags": len(b.drags), "scrolled": len(b.scrolled), "revs": len(b.revs),
		}
	}
	b.mu.Lock()
	for _, pane := range []string{"w1:p1", "w1:p2", "w2:p1"} {
		block := b.IDs.Rex("block", pane)
		b.procs[block] = proc{gen: 1}
		b.themes[block] = map[string]any{}
		b.owners[block] = "client:a"
		b.desired[block] = []desired{{client: "client:a"}}
		b.statuses[pane] = status{state: "idle"}
		b.exited[pane] = true
		b.wheel[pane] = 0.5
		b.drags[pane] = &drag{}
		b.scrolled[pane] = true
	}
	b.mu.Unlock()
	before := sizes()

	f.edit(func(s *fakeState) {
		s.snap.Workspaces = s.snap.Workspaces[:1]
		s.snap.Tabs = s.snap.Tabs[:1]
		s.snap.Panes = s.snap.Panes[:1] // w1:p2 and w2's pane closed
	})
	if err := b.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	for name, n := range sizes() {
		if want := 1; n != want {
			t.Errorf("%s: %d entries (%d before), want %d", name, n, before[name], want)
		}
	}
}
