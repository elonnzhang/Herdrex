// Package bridge serves the Rex control and data protocols on top of Herdr.
package bridge

import (
	"context"
	"encoding/json"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/elonnzhang/herdrex/internal/model"
	"github.com/elonnzhang/herdrex/pkg/herdr/jsonapi"
	rexsvr "github.com/elonnzhang/herdrex/pkg/rex/server"
)

type Bridge struct {
	H   *jsonapi.Client
	IDs *model.IDs
	srv *rexsvr.Server // the Rex side: connections, attachments, events, data handshakes

	// Notify sends desktop_notification when an agent needs attention or
	// finishes, as Herdr's own toasts do.
	Notify bool

	refreshMu sync.Mutex    // serializes Refresh
	kick      chan struct{} // asks Watch for a refresh
	paneSet   chan struct{} // the snapshot's panes changed: WatchStatus resubscribes

	// Times since epoch, for skipping refreshes that one already made covers.
	// asked is when the last successful refresh asked Herdr for its snapshot
	// (guarded by refreshMu); stale is when Herdr last reported a change.
	epoch time.Time
	asked time.Duration
	stale atomic.Int64

	// watching is set while Watch holds an event subscription, so that the
	// snapshot follows Herdr within one refresh and reads can use it.
	watching atomic.Bool

	mu       sync.Mutex
	snap     *jsonapi.Snapshot
	sigs     map[string]string
	revs     map[string]int
	owners   map[string]string // Rex block id -> client id
	desired  map[string][]desired
	proto    uint32                    // Herdr protocol from ping, for the direct terminal protocol
	themes   map[string]map[string]any // Rex block id -> last set_theme arguments
	procs    map[string]proc           // Rex block id -> last process_changed state
	statuses map[string]status         // Herdr pane -> agent status and when the bridge first saw it
	exited   map[string]bool           // Herdr panes whose program exited, from pane_exited
	order    []string                  // Herdr workspace order, for session_moved
	clips    int                       // last clipboard_written id
	wheel    map[string]float64        // Herdr pane -> pixel scroll not yet a whole line
	drags    map[string]*drag          // Herdr pane -> left-button selection in progress
	scrolled map[string]bool           // Herdr panes the wheel left scrolled back
	floats   map[string][]model.Float  // Herdr tab -> floating layers, back to front (layers.go)
	floated  map[string]floatMark      // Herdr tab -> last float or dock, to skip Rex.app's rebalance

	hubMu sync.Mutex // guards hubs; taken before mu
	hubs  map[string]*hub

	// rev0 is the first revision of every workspace. It is the start time in
	// milliseconds, so revisions keep rising across bridge restarts: Rex.app
	// remembers the last revision per session and ignores lower ones.
	rev0 int

	// instance names this run of the bridge. Like the real server's, it is
	// new on every start, while the host id stays.
	instance string
}

// New serves the Herdr at socket. The socket's absolute path names the Herdr
// server, so its Rex ids survive restarts and differ from other bridges'.
func New(socket string) *Bridge {
	host, err := filepath.Abs(socket)
	if err != nil {
		host = socket
	}
	b := &Bridge{
		H:        &jsonapi.Client{Socket: socket},
		IDs:      model.NewIDs(host),
		srv:      rexsvr.New(),
		Notify:   true,
		kick:     make(chan struct{}, 1),
		paneSet:  make(chan struct{}, 1),
		sigs:     map[string]string{},
		revs:     map[string]int{},
		owners:   map[string]string{},
		desired:  map[string][]desired{},
		hubs:     map[string]*hub{},
		themes:   map[string]map[string]any{},
		procs:    map[string]proc{},
		statuses: map[string]status{},
		exited:   map[string]bool{},
		wheel:    map[string]float64{},
		drags:    map[string]*drag{},
		scrolled: map[string]bool{},
		floats:   map[string][]model.Float{},
		floated:  map[string]floatMark{},
		rev0:     int(time.Now().UnixMilli()),
		instance: uuid.NewString(),
		epoch:    time.Now(),
	}
	b.srv.Trace = os.Getenv("HERDREX_TRACE") != ""
	b.srv.Data = b.dataSource
	b.srv.OnDisconnect = func(c *rexsvr.Client) { // drop its blocks and size requests
		b.release(c)
		b.forget(c.ID)
	}
	for name, h := range handlers {
		b.srv.Handle(name, b.method(h))
	}
	return b
}

// Handler serves HTTP: /api/control and /api/data.
func (b *Bridge) Handler() http.Handler { return b.srv }

// signatures hashes each workspace's view-relevant state. Agent status, agent
// names and scroll data are left out because they do not change the Rex view.
func signatures(s *jsonapi.Snapshot) map[string]string {
	out := map[string]string{}
	for _, w := range s.Workspaces {
		part := struct {
			W jsonapi.Workspace
			T []jsonapi.Tab
			P []jsonapi.Pane
			L []jsonapi.TabLayout
		}{W: w}
		part.W.AgentStatus = ""
		for _, t := range s.Tabs {
			if t.WorkspaceID == w.ID {
				part.T = append(part.T, t)
			}
		}
		for _, p := range s.Panes {
			if p.WorkspaceID == w.ID {
				p.AgentStatus, p.Agent, p.DisplayAgent = "", "", ""
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
	return b.refreshAfter(ctx, -1)
}

// since is the time since b.epoch, on the monotonic clock.
func (b *Bridge) since() time.Duration { return time.Since(b.epoch) }

// refreshAfter is Refresh for changes Herdr made before t (a b.since time).
// It does nothing when a refresh that asked Herdr after t has succeeded,
// since that snapshot holds them: a Rex-side change is followed by Herdr's
// events for it, and the refresh the change made answers those too.
func (b *Bridge) refreshAfter(ctx context.Context, t time.Duration) error {
	b.refreshMu.Lock()
	defer b.refreshMu.Unlock()
	if b.asked > t && t >= 0 {
		return nil
	}
	asked := b.since()
	snap, err := b.H.Snapshot(ctx)
	if err != nil {
		return err
	}
	b.asked = asked
	sigs := signatures(snap)
	b.mu.Lock()
	old, prev, first := b.sigs, b.snap, b.snap == nil
	b.snap, b.sigs = snap, sigs
	b.pruneFloats(snap)
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
	if prev == nil || !samePanes(prev, snap) {
		select {
		case b.paneSet <- struct{}{}:
		default:
		}
	}
	if first {
		b.registerIDs(snap)
		b.order = workspaceOrder(snap)
		return nil
	}
	b.registerIDs(snap)
	for _, w := range snap.Workspaces {
		for _, id := range created {
			if w.ID == id {
				b.srv.Emit("session_created", map[string]any{
					"session": map[string]any{"session_id": b.IDs.Rex("session", id), "label": w.Label},
				})
			}
		}
	}
	for _, id := range destroyed {
		b.srv.Emit("session_destroyed", map[string]any{"session_id": b.IDs.Rex("session", id)})
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
	b.srv.SessionEvent(sid, "session_view_changed", map[string]any{"revision": rev})
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
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-b.kick:
			}
			select { // let the rest of a burst arrive
			case <-ctx.Done():
				return
			case <-time.After(watchSettle):
			}
			select {
			case <-b.kick:
			default:
			}
			// Every change reported so far; a later one kicks again.
			cctx, cancel := context.WithTimeout(ctx, 3*time.Second)
			if err := b.refreshAfter(cctx, time.Duration(b.stale.Load())); err != nil {
				log.Printf("refresh: %v", err)
			}
			cancel()
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
		b.watching.Store(true)
		for e := range events {
			if e.Name == "pane_exited" {
				b.paneExited(e.Data)
			}
			b.requestRefresh()
		}
		b.watching.Store(false)
		if ctx.Err() == nil {
			log.Printf("events.subscribe ended: %v", *failure)
		}
	}
}

// requestRefresh asks Watch for a refresh; requests during a burst merge.
func (b *Bridge) requestRefresh() {
	b.stale.Store(int64(b.since())) // before the kick, so that Watch reads it after
	select {
	case b.kick <- struct{}{}:
	default:
	}
}

// WatchStatus refreshes when an agent's status changes. Herdr sends no
// pane.updated for that, only pane.agent_status_changed, which must name a
// pane. One subscription carries every pane, and is reopened with the new set
// whenever panes come, go or move, as Herdr ignores a second events.subscribe
// on a connection. The refresh after each subscribe catches changes made
// while none listened.
func (b *Bridge) WatchStatus(ctx context.Context) {
	backoff := 100 * time.Millisecond
	for ctx.Err() == nil {
		select { // drop the signal this subscription already covers
		case <-b.paneSet:
		default:
		}
		var subs []map[string]any
		if s := b.snapshot(); s != nil {
			for _, p := range s.Panes {
				subs = append(subs, map[string]any{"type": "pane.agent_status_changed", "pane_id": p.ID})
			}
		}
		if len(subs) == 0 {
			select {
			case <-ctx.Done():
			case <-b.paneSet:
			}
			continue
		}
		sctx, cancel := context.WithCancel(ctx)
		events, failure, err := b.H.SubscribeTo(sctx, subs)
		if err != nil { // a pane may have gone since the snapshot
			cancel()
			log.Printf("status subscribe: %v", err)
			select {
			case <-ctx.Done():
			case <-time.After(backoff):
			}
			backoff = min(2*backoff, 5*time.Second)
			b.refreshLogged(ctx)
			continue
		}
		backoff = 100 * time.Millisecond
		b.requestRefresh()
	listen:
		for {
			select {
			case _, ok := <-events:
				if !ok {
					if ctx.Err() == nil {
						log.Printf("status subscription ended: %v", *failure)
					}
					break listen
				}
				b.requestRefresh()
			case <-b.paneSet:
				break listen
			}
		}
		cancel()
		for range events { // wait for the reader to stop
		}
	}
}

// samePanes says whether two snapshots hold the same pane IDs.
func samePanes(a, b *jsonapi.Snapshot) bool {
	if len(a.Panes) != len(b.Panes) {
		return false
	}
	ids := map[string]bool{}
	for _, p := range a.Panes {
		ids[p.ID] = true
	}
	for _, p := range b.Panes {
		if !ids[p.ID] {
			return false
		}
	}
	return true
}

// watchSettle is how long a refresh waits for the rest of an event burst.
const watchSettle = 8 * time.Millisecond

func (b *Bridge) snapshot() *jsonapi.Snapshot {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.snap
}

// current is the snapshot that session.view and session.list read. While
// Watch is subscribed it is the last refresh's, which Herdr's events keep up
// to date and which matches the revisions clients were told; a session.snapshot
// costs Herdr about 2 ms at 120 panes. Without the subscription it is fresh.
func (b *Bridge) current(ctx context.Context) (*jsonapi.Snapshot, error) {
	if s := b.snapshot(); s != nil && b.watching.Load() {
		return s, nil
	}
	return b.fresh(ctx)
}
