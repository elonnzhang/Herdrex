package bridge

import (
	"context"
	"encoding/json"
	"runtime"
	"runtime/debug"
	"time"

	"github.com/elonnzhang/herdrex/pkg/herdr/jsonapi"
	"github.com/elonnzhang/herdrex/pkg/rex"
	rexsvr "github.com/elonnzhang/herdrex/pkg/rex/server"
)

// Version is reported as server.version rex_version.
const Version = "herdrex/0.1"

type handler func(ctx context.Context, b *Bridge, c *rexsvr.Client, p P) (any, error)

// method serves a handler on the Rex server, with the payload as a P.
func (b *Bridge) method(h handler) rexsvr.Handler {
	return func(ctx context.Context, c *rexsvr.Client, payload json.RawMessage) (any, error) {
		var p P
		if len(payload) > 0 {
			json.Unmarshal(payload, &p)
		}
		return h(ctx, b, c, p)
	}
}

// P is a request payload with typed accessors.
type P map[string]any

func (p P) str(k string) string  { s, _ := p[k].(string); return s }
func (p P) num(k string) float64 { f, _ := p[k].(float64); return f }
func (p P) obj(k string) P       { m, _ := p[k].(map[string]any); return P(m) }

// herdrID resolves a Rex id to a Herdr id.
func (b *Bridge) herdrID(rexID, kind string) (string, error) {
	if h, ok := b.IDs.Herdr(rexID); ok {
		return h, nil
	}
	code := "not_found"
	if kind == "session" {
		code = "session_not_found"
	}
	return "", rex.NewError(code, "unknown "+kind+" "+rexID, 404)
}

// fresh reloads the snapshot and registers every id in it.
func (b *Bridge) fresh(ctx context.Context) (*jsonapi.Snapshot, error) {
	s, err := b.H.Snapshot(ctx)
	if err != nil {
		return nil, err
	}
	b.registerIDs(s)
	return s, nil
}

func (b *Bridge) status() map[string]any {
	b.mu.Lock()
	n := len(b.sigs)
	b.mu.Unlock()
	return map[string]any{
		"pid": 0, "instance_id": b.IDs.HostID, "host_id": b.IDs.HostID,
		"started_at": time.Now().UTC().Format(time.RFC3339), "exit_empty": false,
		"session_count": n, "api_revision": rex.APIRevision,
		"tailscale": map[string]any{"state": "disabled", "allow_tailnet_members": false},
		"config":    map[string]any{"generation": 1, "hash": ""},
	}
}

func (b *Bridge) sessions(ctx context.Context) ([]map[string]any, error) {
	s, err := b.fresh(ctx)
	if err != nil {
		return nil, err
	}
	out := []map[string]any{}
	for _, w := range s.Workspaces {
		out = append(out, map[string]any{"session_id": b.IDs.Rex("session", w.ID), "label": w.Label})
	}
	return out, nil
}

func (b *Bridge) view(ctx context.Context, sessionID string) (*rex.View, error) {
	wid, err := b.herdrID(sessionID, "session")
	if err != nil {
		return nil, err
	}
	s, err := b.fresh(ctx)
	if err != nil {
		return nil, err
	}
	var ws *jsonapi.Workspace
	for i := range s.Workspaces {
		if s.Workspaces[i].ID == wid {
			ws = &s.Workspaces[i]
		}
	}
	if ws == nil {
		return nil, rex.NewError("session_not_found", "unknown session "+sessionID, 404)
	}
	b.mu.Lock()
	rev := b.revs[wid]
	b.mu.Unlock()
	if rev == 0 {
		rev = b.rev0
	}
	v := &rex.View{Revision: rev, SessionID: sessionID, Label: ws.Label, Windows: []rex.Window{}}
	for _, t := range s.Tabs {
		if t.WorkspaceID != wid {
			continue
		}
		l, err := b.H.Layout(ctx, t.ID)
		if err != nil {
			return nil, err
		}
		v.Windows = append(v.Windows, b.IDs.Window(t, s.Panes, l, b.tabFloats(t.ID)))
	}
	b.setOwners(v)
	if ws.ActiveTabID != "" {
		a := b.IDs.Rex("window", ws.ActiveTabID)
		v.ActiveWindowID = &a
	}
	return v, nil
}

func (b *Bridge) attach(ctx context.Context, c *rexsvr.Client, ids []string) ([]map[string]any, error) {
	out := []map[string]any{}
	seen := map[string]bool{}
	for _, sid := range ids {
		if seen[sid] {
			continue
		}
		seen[sid] = true
		v, err := b.view(ctx, sid)
		if re, ok := err.(*rex.Error); ok && re.Code == "session_not_found" {
			// Rex skips unknown and repeated IDs. The app sends IDs it
			// remembers, which may belong to another server.
			continue
		}
		if err != nil {
			return nil, err
		}
		b.srv.Attach(c, sid)
		out = append(out, map[string]any{"session": map[string]any{"session_id": sid, "label": v.Label}, "view": v})
	}
	return out, nil
}

func hAttachAll(ctx context.Context, b *Bridge, c *rexsvr.Client, p P) (any, error) {
	if err := b.srv.AttachAll(c, p.str("event_scope")); err != nil {
		return nil, err
	}
	ss, err := b.sessions(ctx)
	return map[string]any{"sessions": ss}, err
}

func hAttach(ctx context.Context, b *Bridge, c *rexsvr.Client, p P) (any, error) {
	sid := p.str("session_id")
	if _, err := b.herdrID(sid, "session"); err != nil {
		return nil, err
	}
	r, err := b.attach(ctx, c, []string{sid})
	if err != nil {
		return nil, err
	}
	if len(r) == 0 {
		return nil, rex.NewError("session_not_found", "unknown session "+sid, 404)
	}
	return map[string]any{"client_id": c.ID, "view": r[0]["view"]}, nil
}

func hBootstrap(ctx context.Context, b *Bridge, c *rexsvr.Client, p P) (any, error) {
	if err := b.srv.AttachAll(c, p.str("event_scope")); err != nil {
		return nil, err
	}
	ss, err := b.sessions(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"client_id":        c.ID,
		"server_status":    b.status(),
		"attach_all":       map[string]any{"sessions": ss},
		"selected_session": nil,
	}
	if raw, ok := p["attach_session_ids"].([]any); ok {
		var ids []string
		for _, x := range raw {
			if s, ok := x.(string); ok {
				ids = append(ids, s)
			}
		}
		a, err := b.attach(ctx, c, ids)
		out["attached_sessions"] = a
		return out, err
	}
	pick, found := p.str("preferred_session_id"), false
	for _, s := range ss {
		if s["session_id"] == pick {
			found = true
		}
	}
	if !found && len(ss) > 0 {
		pick, found = ss[0]["session_id"].(string), true
	}
	if found {
		a, err := b.attach(ctx, c, []string{pick})
		if err != nil {
			return nil, err
		}
		out["selected_session"] = a[0]
	}
	return out, nil
}

func hList(ctx context.Context, b *Bridge, c *rexsvr.Client, p P) (any, error) {
	ss, err := b.sessions(ctx)
	return map[string]any{"sessions": ss}, err
}

func hView(ctx context.Context, b *Bridge, c *rexsvr.Client, p P) (any, error) {
	return b.view(ctx, p.str("session_id"))
}

func hStatus(ctx context.Context, b *Bridge, c *rexsvr.Client, p P) (any, error) {
	return b.status(), nil
}

func hVersion(ctx context.Context, b *Bridge, c *rexsvr.Client, p P) (any, error) {
	v := map[string]any{
		"rex_version":  Version,
		"platform":     runtime.GOOS + "_" + runtime.GOARCH,
		"commit":       "",
		"built_at":     "",
		"dirty":        false,
		"api_revision": rex.APIRevision,
	}
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				v["commit"] = s.Value
			case "vcs.time":
				v["built_at"] = s.Value
			case "vcs.modified":
				v["dirty"] = s.Value == "true"
			}
		}
	}
	return v, nil
}

func hCreators(ctx context.Context, b *Bridge, c *rexsvr.Client, p P) (any, error) {
	return map[string]any{"creators": []any{b.IDs.CreatorInfo()}}, nil
}
