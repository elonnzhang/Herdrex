// Package bridge serves the Rex control and data protocols on top of Herdr.
package bridge

import (
	"context"
	"encoding/json"
	"log"
	"sync"
	"time"

	"github.com/elonnzhang/herdrex/herdr"
	"github.com/elonnzhang/herdrex/internal/model"
)

type Bridge struct {
	H   *herdr.Client
	IDs *model.IDs

	refreshMu sync.Mutex // serializes Refresh

	mu       sync.Mutex
	snap     *herdr.Snapshot
	sigs     map[string]string
	revs     map[string]int
	clients  map[*Client]struct{}
	tickets  map[string]ticket
	owners   map[string]string // Rex block id -> client id
	desired  map[string][]desired
	proto    uint32                    // Herdr protocol from ping, for the direct terminal protocol
	themes   map[string]map[string]any // Rex block id -> last set_theme arguments
	procs    map[string]proc           // Rex block id -> last process_changed state
	exited   map[string]bool           // Herdr panes whose program exited, from pane_exited
	order    []string                  // Herdr workspace order, for session_moved
	clips    int                       // last clipboard_written id
	wheel    map[string]float64        // Herdr pane -> pixel scroll not yet a whole line
	drags    map[string]*drag          // Herdr pane -> left-button selection in progress
	scrolled map[string]bool           // Herdr panes the wheel left scrolled back

	hubMu sync.Mutex // guards hubs; taken before mu
	hubs  map[string]*hub

	// rev0 is the first revision of every workspace. It is the start time in
	// milliseconds, so revisions keep rising across bridge restarts: Rex.app
	// remembers the last revision per session and ignores lower ones.
	rev0 int
}

func New(socket string) *Bridge {
	return &Bridge{H: &herdr.Client{Socket: socket}, IDs: model.NewIDs(),
		sigs: map[string]string{}, revs: map[string]int{},
		clients: map[*Client]struct{}{}, tickets: map[string]ticket{}, owners: map[string]string{},
		desired: map[string][]desired{}, hubs: map[string]*hub{},
		themes: map[string]map[string]any{}, procs: map[string]proc{}, exited: map[string]bool{},
		wheel: map[string]float64{}, drags: map[string]*drag{}, scrolled: map[string]bool{}, rev0: int(time.Now().UnixMilli())}
}

// signatures hashes each workspace's view-relevant state. Agent status and
// scroll data are left out because they do not change the Rex view.
func signatures(s *herdr.Snapshot) map[string]string {
	out := map[string]string{}
	for _, w := range s.Workspaces {
		part := struct {
			W herdr.Workspace
			T []herdr.Tab
			P []herdr.Pane
			L []herdr.TabLayout
		}{W: w}
		part.W.AgentStatus = ""
		for _, t := range s.Tabs {
			if t.WorkspaceID == w.ID {
				part.T = append(part.T, t)
			}
		}
		for _, p := range s.Panes {
			if p.WorkspaceID == w.ID {
				p.AgentStatus = ""
				part.P = append(part.P, p)
			}
		}
		for _, l := range s.Layouts {
			if l.WorkspaceID == w.ID {
				part.L = append(part.L, l)
			}
		}
		b, _ := json.Marshal(part)
		out[w.ID] = string(b)
	}
	return out
}

// Refresh reads Herdr state and emits Rex events for anything that changed.
// Calls are serialized so that two refreshes never diff against the same
// old snapshot or apply snapshots out of order.
func (b *Bridge) Refresh(ctx context.Context) error {
	b.refreshMu.Lock()
	defer b.refreshMu.Unlock()
	snap, err := b.H.Snapshot(ctx)
	if err != nil {
		return err
	}
	sigs := signatures(snap)
	b.mu.Lock()
	old, prev, first := b.sigs, b.snap, b.snap == nil
	b.snap, b.sigs = snap, sigs
	var created, changed []string
	var destroyed []string
	for id, s := range sigs {
		if _, ok := old[id]; !ok {
			b.revs[id] = b.rev0
			created = append(created, id)
		} else if old[id] != s {
			b.revs[id]++
			changed = append(changed, id)
		}
	}
	for id := range old {
		if _, ok := sigs[id]; !ok {
			destroyed = append(destroyed, id)
		}
	}
	b.mu.Unlock()
	if first {
		b.registerIDs(snap)
		b.order = workspaceOrder(snap)
		return nil
	}
	b.registerIDs(snap)
	for _, w := range snap.Workspaces {
		for _, id := range created {
			if w.ID == id {
				b.emitAll(map[string]any{"type": "session_created", "payload": map[string]any{
					"session": map[string]any{"session_id": b.IDs.Rex("session", id), "label": w.Label}}})
			}
		}
	}
	for _, id := range destroyed {
		sid := b.IDs.Rex("session", id)
		msg := map[string]any{"type": "session_destroyed", "payload": map[string]any{"session_id": sid}}
		for _, c := range b.clientList() {
			if c.wants(sid) {
				c.push(msg)
			}
		}
	}
	b.sessionMoves(snap)
	b.paneEvents(ctx, prev, snap)
	for _, id := range changed {
		d := b.diff(prev, snap, id)
		for _, e := range d.pre {
			b.sessionEvent(id, e)
		}
		b.viewChanged(id)
		for _, e := range d.post {
			b.sessionEvent(id, e)
		}
	}
	return nil
}

// bump raises a workspace's view revision and tells clients. Used after
// changes that the snapshot signature cannot see, such as split ratios.
func (b *Bridge) bump(wid string) int {
	b.mu.Lock()
	b.revs[wid]++
	rev := b.revs[wid]
	b.mu.Unlock()
	b.viewChanged(wid)
	return rev
}

func (b *Bridge) viewChanged(wid string) {
	sid := b.IDs.Rex("session", wid)
	b.mu.Lock()
	rev := b.revs[wid]
	b.mu.Unlock()
	ev := map[string]any{"type": "session_event", "payload": map[string]any{
		"session_id": sid, "event": map[string]any{
			"type": "session_view_changed", "payload": map[string]any{"revision": rev}}}}
	for _, c := range b.clientList() {
		if c.wants(sid) {
			c.push(ev)
		}
	}
}

func (b *Bridge) emitAll(ev map[string]any) {
	for _, c := range b.clientList() {
		if c.isAttachAll() {
			c.push(ev)
		}
	}
}

func (b *Bridge) clientList() []*Client {
	b.mu.Lock()
	defer b.mu.Unlock()
	out := make([]*Client, 0, len(b.clients))
	for c := range b.clients {
		out = append(out, c)
	}
	return out
}

// Poll refreshes on an interval until ctx ends.
func (b *Bridge) Poll(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			b.refreshLogged(ctx)
		}
	}
}

func (b *Bridge) refreshLogged(ctx context.Context) {
	cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	if err := b.Refresh(cctx); err != nil {
		log.Printf("refresh: %v", err)
	}
}

// watchTypes are the Herdr events that can change the Rex view.
var watchTypes = []string{
	"workspace.created", "workspace.updated", "workspace.renamed", "workspace.moved",
	"workspace.reordered", "workspace.closed", "workspace.focused",
	"tab.created", "tab.closed", "tab.focused", "tab.renamed", "tab.moved",
	"pane.created", "pane.closed", "pane.updated", "pane.focused", "pane.moved", "pane.exited",
	"layout.updated",
}

// Watch refreshes whenever Herdr reports a change, so Herdr-side edits reach
// Rex clients within milliseconds instead of at the next poll. Events only
// say that state is stale (docs/herdr-api.md 6.3): a burst of them leads to
// one refresh. When the subscription fails it resubscribes and refreshes,
// since events may have been lost.
func (b *Bridge) Watch(ctx context.Context) {
	kick := make(chan struct{}, 1)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-kick:
			}
			select { // let the rest of a burst arrive
			case <-ctx.Done():
				return
			case <-time.After(watchSettle):
			}
			select {
			case <-kick:
			default:
			}
			b.refreshLogged(ctx)
		}
	}()
	backoff := 100 * time.Millisecond
	for ctx.Err() == nil {
		events, failure, err := b.H.Subscribe(ctx, watchTypes)
		if err != nil {
			log.Printf("events.subscribe: %v", err)
			select {
			case <-ctx.Done():
			case <-time.After(backoff):
			}
			backoff = min(2*backoff, 5*time.Second)
			continue
		}
		backoff = 100 * time.Millisecond
		b.refreshLogged(ctx) // catch up on anything missed while unsubscribed
		for e := range events {
			if e.Name == "pane_exited" {
				b.paneExited(e.Data)
			}
			select {
			case kick <- struct{}{}:
			default:
			}
		}
		if ctx.Err() == nil {
			log.Printf("events.subscribe ended: %v", *failure)
		}
	}
}

// watchSettle is how long a refresh waits for the rest of an event burst.
const watchSettle = 8 * time.Millisecond

func (b *Bridge) snapshot() *herdr.Snapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.snap
}
