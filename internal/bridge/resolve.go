package bridge

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/elonnzhang/herdrex/internal/model"
	"github.com/elonnzhang/herdrex/pkg/herdr/jsonapi"
	"github.com/elonnzhang/herdrex/pkg/rex"
	rexsvr "github.com/elonnzhang/herdrex/pkg/rex/server"
)

// target.resolve, with the real server's rules: a query is tried as a full
// ID, a label ignoring case, a window position, then the end of an ID (4 to
// 32 hex digits, dashes ignored), and the first step that matches anything
// wins. An empty query asks for a default. Messages copy the real server's.

type resolver struct {
	b        *Bridge
	s        *jsonapi.Snapshot
	kinds    []string
	searched []jsonapi.Workspace // sessions searched for windows and blocks
	window   string              // Herdr tab of scope.window, or ""
	ctxSID   string              // Herdr workspace of context.session_id, or ""
	ctxBlock string              // Herdr pane of context.block_id, or ""
	ctxCli   string
	clients  []*rexsvr.Client
}

type found = map[string]any

func hResolve(ctx context.Context, b *Bridge, c *rexsvr.Client, p P) (any, error) {
	kinds, err := targetKinds(p["kinds"])
	if err != nil {
		return nil, err
	}
	raw, _ := p["queries"].([]any)
	if len(raw) == 0 {
		return nil, rex.NewError("bad_request", "target resolution requires at least one query", 400)
	}
	queries := make([]string, len(raw))
	for i, q := range raw {
		queries[i], _ = q.(string)
		if hasControl(queries[i]) {
			return nil, rex.NewError("bad_request", fmt.Sprintf("queries[%d]: target %q contains a control character", i, queries[i]), 400)
		}
	}
	r, status, reason, cands, err := newResolver(ctx, b, p, kinds)
	if err != nil {
		return nil, err
	}
	if status != "" { // a failed scope fails every query the same way
		out := make([]found, len(queries))
		for i, q := range queries {
			out[i] = found{"query": q, "status": status, "reason": reason}
			if cands != nil {
				out[i]["candidates"] = cands
			}
		}
		return map[string]any{"results": out}, nil
	}

	out := make([]found, len(queries))
	for i, q := range queries {
		out[i] = r.resolve(q)
	}
	return map[string]any{"results": out}, nil
}

// newResolver checks scope and context and sets up the search. A scope that
// matches nothing or several things comes back as a status and reason.
func newResolver(ctx context.Context, b *Bridge, p P, kinds []string) (*resolver, string, string, []found, error) {
	scope, hasScope := p["scope"].(map[string]any)
	if _, ok := p["scope"]; ok && p["scope"] != nil {
		hasScope = true
	}
	sc := P(scope)
	if hasScope && has(kinds, "session") {
		return nil, "", "", nil, rex.NewError("bad_request", "session target requests do not accept a scope", 400)
	}
	if sc.str("window") != "" && (len(kinds) != 1 || kinds[0] != "block") {
		k := kinds[0]
		if k == "block" {
			k = kinds[1]
		}
		return nil, "", "", nil, rex.NewError("bad_request", k+" target requests do not accept a window scope", 400)
	}
	for _, k := range []string{"session", "window"} {
		if hasControl(sc.str(k)) {
			return nil, "", "", nil, rex.NewError("bad_request", fmt.Sprintf("scope.%s: target %q contains a control character", k, sc.str(k)), 400)
		}
	}
	s, err := b.fresh(ctx)
	if err != nil {
		return nil, "", "", nil, err
	}
	r := &resolver{b: b, s: s, kinds: kinds, ctxCli: p.obj("context").str("client_id")}
	cx := p.obj("context")
	if ws, ok := b.IDs.Herdr(cx.str("session_id")); ok && r.workspace(ws) != nil {
		r.ctxSID = ws
	}
	if pane, ok := b.IDs.Herdr(cx.str("block_id")); ok && r.paneOf(pane) != nil {
		r.ctxBlock = pane
	}
	if kinds[0] == "client" {
		r.clients = b.srv.Clients()
	}

	fail := func(status, reason string, cands []found) (*resolver, string, string, []found, error) {
		return nil, status, reason, cands, nil
	}
	switch q := sc.str("session"); {
	case q != "":
		m := r.sessionMatches(q)
		switch len(m) {
		case 0:
			return fail("not_found", fmt.Sprintf("session scope: session %q not found", q), nil)
		case 1:
			r.searched = m
		default:
			return fail("ambiguous", fmt.Sprintf("session scope: session %q is ambiguous", q), r.sessionItems(m))
		}
		if kinds[0] == "client" {
			sid := b.IDs.Rex("session", m[0].ID)
			var cs []*rexsvr.Client
			for _, o := range r.clients {
				if o.Attached(sid) {
					cs = append(cs, o)
				}
			}
			r.clients = cs
		}
	case r.ctxSID != "":
		r.searched = []jsonapi.Workspace{*r.workspace(r.ctxSID)}
	default:
		r.searched = s.Workspaces
	}
	if q := sc.str("window"); q != "" {
		m := r.find(q, []string{"window"})
		switch len(m) {
		case 0:
			return fail("not_found", fmt.Sprintf("window scope: window %q not found", q), nil)
		case 1:
			r.window, _ = b.IDs.Herdr(m[0]["window_id"].(string))
		default:
			return fail("ambiguous", fmt.Sprintf("window scope: window %q is ambiguous", q), m)
		}
	}
	return r, "", "", nil, nil
}

func targetKinds(v any) ([]string, error) {
	raw, _ := v.([]any)
	if len(raw) == 0 {
		return nil, rex.NewError("bad_request", "target requests require at least one kind", 400)
	}
	var kinds []string
	for i, k := range raw {
		s, _ := k.(string)
		switch s {
		case "session", "window", "block", "client":
		default:
			return nil, rex.NewError("bad_request", "target requests support sessions, windows, blocks, and clients only", 400)
		}
		if has(kinds, s) {
			return nil, rex.NewError("bad_request", fmt.Sprintf("kinds[%d] duplicates %q", i, s), 400)
		}
		kinds = append(kinds, s)
	}
	if has(kinds, "client") && len(kinds) > 1 {
		return nil, rex.NewError("bad_request", "client targets cannot be resolved together with sessions, windows, or blocks", 400)
	}
	return kinds, nil
}

func has(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

func hasControl(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

// kindList writes kinds the way the real server's reasons do:
// "block or window", "session, window, or block".
func kindList(kinds []string) string {
	switch len(kinds) {
	case 1:
		return kinds[0]
	case 2:
		return kinds[0] + " or " + kinds[1]
	}
	return strings.Join(kinds[:len(kinds)-1], ", ") + ", or " + kinds[len(kinds)-1]
}

func (r *resolver) resolve(q string) found {
	res := found{"query": q}
	var m []found
	reason := ""
	switch t := strings.TrimSpace(q); {
	case t == "":
		m, reason = r.empty()
	case fullID(t) != "":
		m, reason = r.byID(t)
	default:
		m = r.match(t, r.kinds)
		if len(m) == 0 && len(r.kinds) > 1 {
			reason = "no " + kindList(r.kinds) + " matched"
		}
	}
	switch {
	case len(m) == 0:
		res["status"] = "not_found"
	case len(m) == 1 && reason == "":
		res["status"], res["target"] = "resolved", m[0]
		return res
	default: // an empty query that needs a session picked has one candidate per session
		res["status"], res["candidates"] = "ambiguous", m
	}
	if reason != "" {
		res["reason"] = reason
	}
	return res
}

// empty is the default an empty query asks for: the most specific of block,
// window and session in kinds. A reason with matches makes it ambiguous.
func (r *resolver) empty() ([]found, string) {
	switch {
	case r.kinds[0] == "client":
		return r.emptyClient()
	case has(r.kinds, "block"):
		if r.window != "" {
			return r.focusedIn(r.window)
		}
		if r.ctxBlock != "" {
			if p := r.paneOf(r.ctxBlock); r.inSearch(p.WorkspaceID) {
				return []found{r.blockItem(*p)}, ""
			}
		}
		tab, m, why := r.emptyWindow()
		if tab == "" {
			return m, why
		}
		return r.focusedIn(tab)
	case has(r.kinds, "window"):
		tab, m, why := r.emptyWindow()
		if tab == "" {
			return m, why
		}
		return []found{r.windowItem(tab)}, ""
	}
	switch {
	case r.ctxSID != "":
		return r.sessionItems([]jsonapi.Workspace{*r.workspace(r.ctxSID)}), ""
	case len(r.s.Workspaces) == 0:
		return nil, "no sessions are available"
	case len(r.s.Workspaces) > 1:
		return r.sessionItems(r.s.Workspaces), "multiple sessions are available"
	}
	return r.sessionItems(r.s.Workspaces), ""
}

// emptyWindow picks the window holding context.block_id, else the active
// window of the one session searched. Without a tab it returns the result.
func (r *resolver) emptyWindow() (string, []found, string) {
	if r.ctxBlock != "" && len(r.searched) == 1 {
		if p := r.paneOf(r.ctxBlock); p.WorkspaceID == r.searched[0].ID {
			return p.TabID, nil, ""
		}
	}
	switch len(r.searched) {
	case 0:
		return "", nil, "no sessions are available"
	case 1:
		if t := r.searched[0].ActiveTabID; t != "" {
			return t, nil, ""
		}
		return "", nil, "session has no windows"
	}
	return "", r.sessionItems(r.searched), "multiple sessions are available. Pass -s to select one"
}

func (r *resolver) focusedIn(tab string) ([]found, string) {
	for _, l := range r.s.Layouts {
		if l.TabID == tab {
			if p := r.paneOf(l.FocusedPaneID); p != nil {
				return []found{r.blockItem(*p)}, ""
			}
		}
	}
	return nil, "window has no blocks"
}

func (r *resolver) emptyClient() ([]found, string) {
	var apps []found
	for _, c := range r.clients {
		if c.ID == r.ctxCli {
			return []found{clientItem(c)}, ""
		}
		kind, _ := c.Info()["kind"].(string)
		if kind == "app" {
			apps = append(apps, clientItem(c))
		}
	}
	switch len(apps) {
	case 0:
		return nil, "no app client is connected"
	case 1:
		return apps, ""
	}
	return apps, "multiple app clients are connected"
}

func (r *resolver) sessionItems(ws []jsonapi.Workspace) []found {
	out := make([]found, len(ws))
	for i, w := range ws {
		out[i] = found{"kind": "session", "session_id": r.b.IDs.Rex("session", w.ID), "label": w.Label}
	}
	return out
}

// ordinal is the tab's 1-based position in its workspace.
func (r *resolver) ordinal(tab string) (string, int) {
	n := 0
	for _, t := range r.s.Tabs {
		if t.WorkspaceID == r.tabWorkspace(tab) {
			n++
			if t.ID == tab {
				return t.WorkspaceID, n
			}
		}
	}
	return "", 0
}

func (r *resolver) tabWorkspace(tab string) string {
	for _, t := range r.s.Tabs {
		if t.ID == tab {
			return t.WorkspaceID
		}
	}
	return ""
}

// tabAt is the n-th tab of workspace ws, or "".
func (r *resolver) tabAt(ws string, n int) string {
	for _, t := range r.s.Tabs {
		if t.WorkspaceID == ws {
			if n--; n == 0 {
				return t.ID
			}
		}
	}
	return ""
}

func (r *resolver) windowItem(tab string) found {
	ws, n := r.ordinal(tab)
	label := ""
	for _, t := range r.s.Tabs {
		if t.ID == tab {
			label = t.Label
		}
	}
	f := found{
		"kind":       "window",
		"session_id": r.b.IDs.Rex("session", ws),
		"window_id":  r.b.IDs.Rex("window", tab),
		"label":      label, "ordinal": n,
	}
	if len(r.searched) > 1 {
		f["where"] = "in " + r.workspace(ws).Label
	}
	return f
}

func (r *resolver) blockItem(p jsonapi.Pane) found {
	_, n := r.ordinal(p.TabID)
	where := fmt.Sprintf("window %d", n)
	if len(r.searched) > 1 {
		where += " of " + r.workspace(p.WorkspaceID).Label
	}
	return found{
		"kind":       "block",
		"session_id": r.b.IDs.Rex("session", p.WorkspaceID),
		"window_id":  r.b.IDs.Rex("window", p.TabID),
		"block_id":   r.b.IDs.Rex("block", p.ID),
		"label":      model.PaneLabel(p),
		"where":      where,
	}
}

func clientItem(c *rexsvr.Client) found {
	info := c.Info()
	label, _ := info["label"].(string)
	name, _ := info["name"].(string)
	version, _ := info["version"].(string)
	return found{
		"kind":      "client",
		"client_id": c.ID,
		"label":     label,
		"where":     strings.TrimSpace(name + " " + version),
	}
}

// fullID is the kind named by a "<kind>:" prefix, or "".
func fullID(q string) string {
	for _, k := range []string{"session", "window", "block", "client"} {
		if strings.HasPrefix(q, k+":") {
			return k
		}
	}
	return ""
}

func (r *resolver) byID(q string) ([]found, string) {
	k := fullID(q)
	if !has(r.kinds, k) {
		if len(r.kinds) == 1 {
			return nil, fmt.Sprintf("%q is a %s ID, not a %s", q, k, r.kinds[0])
		}
		return nil, fmt.Sprintf("%q is a %s ID; expected %s", q, k, kindList(r.kinds))
	}
	if _, err := uuid.Parse(q[len(k)+1:]); err != nil {
		return nil, fmt.Sprintf("%q is not a valid %s ID", q, k)
	}
	return r.filter(k, func(id, _ string) bool { return id == q }), ""
}

// match runs the label, position and ID-end steps for kinds.
func (r *resolver) match(q string, kinds []string) []found {
	var m []found
	for _, k := range kinds {
		m = append(m, r.filter(k, func(_, label string) bool { return strings.EqualFold(label, q) })...)
	}
	if len(m) > 0 {
		return m
	}
	if n, err := strconv.Atoi(q); err == nil && n > 0 && q[0] != '+' && has(kinds, "window") {
		for _, w := range r.searched {
			if t := r.tabAt(w.ID, n); t != "" {
				m = append(m, r.windowItem(t))
			}
		}
		if len(m) > 0 {
			return m
		}
	}
	if end := strings.ToLower(strings.ReplaceAll(q, "-", "")); len(end) >= 4 && len(end) <= 32 && isHex(end) {
		for _, k := range kinds {
			m = append(m, r.filter(k, func(id, _ string) bool {
				return strings.HasSuffix(strings.ReplaceAll(id, "-", ""), end)
			})...)
		}
	}
	return m
}

func isHex(s string) bool {
	for _, c := range s {
		if !('0' <= c && c <= '9' || 'a' <= c && c <= 'f') {
			return false
		}
	}
	return true
}

// filter lists the searched things of kind k whose Rex ID and label pass ok.
func (r *resolver) filter(k string, ok func(id, label string) bool) []found {
	var m []found
	b := r.b
	switch k {
	case "session":
		for _, w := range r.s.Workspaces {
			if ok(b.IDs.Rex("session", w.ID), w.Label) {
				m = append(m, r.sessionItems([]jsonapi.Workspace{w})...)
			}
		}
	case "window":
		for _, t := range r.s.Tabs {
			if r.inSearch(t.WorkspaceID) && ok(b.IDs.Rex("window", t.ID), t.Label) {
				m = append(m, r.windowItem(t.ID))
			}
		}
	case "block":
		for _, p := range r.s.Panes {
			if r.inSearch(p.WorkspaceID) && (r.window == "" || p.TabID == r.window) &&
				ok(b.IDs.Rex("block", p.ID), model.PaneLabel(p)) {
				m = append(m, r.blockItem(p))
			}
		}
	case "client":
		for _, c := range r.clients {
			label, _ := c.Info()["label"].(string)
			if ok(c.ID, label) {
				m = append(m, clientItem(c))
			}
		}
	}
	return m
}

func (r *resolver) inSearch(ws string) bool {
	for _, w := range r.searched {
		if w.ID == ws {
			return true
		}
	}
	return false
}

func (r *resolver) workspace(ws string) *jsonapi.Workspace {
	for i := range r.s.Workspaces {
		if r.s.Workspaces[i].ID == ws {
			return &r.s.Workspaces[i]
		}
	}
	return nil
}

func (r *resolver) paneOf(id string) *jsonapi.Pane {
	for i := range r.s.Panes {
		if r.s.Panes[i].ID == id {
			return &r.s.Panes[i]
		}
	}
	return nil
}

// find looks q up among kinds without the empty-query and reason rules; it
// is how scopes are resolved.
func (r *resolver) find(q string, kinds []string) []found {
	if k := fullID(q); k != "" {
		if !has(kinds, k) {
			return nil
		}
		return r.filter(k, func(id, _ string) bool { return id == q })
	}
	return r.match(q, kinds)
}

func (r *resolver) sessionMatches(q string) []jsonapi.Workspace {
	var out []jsonapi.Workspace
	for _, f := range r.find(q, []string{"session"}) {
		ws, _ := r.b.IDs.Herdr(f["session_id"].(string))
		out = append(out, *r.workspace(ws))
	}
	return out
}

// hComplete is target.complete: labels, window positions and short IDs (the
// last six hex digits of an ID) that start with the prefix, grouped by kind
// in the order of kinds; within a kind labels sorted ignoring case, then
// positions in order, then short IDs sorted.
func hComplete(ctx context.Context, b *Bridge, c *rexsvr.Client, p P) (any, error) {
	kinds, err := targetKinds(p["kinds"])
	if err != nil {
		return nil, err
	}
	raw := p.str("prefix")
	if hasControl(raw) {
		return nil, rex.NewError("bad_request", fmt.Sprintf("target %q contains a control character", raw), 400)
	}
	prefix := strings.ToLower(strings.TrimSpace(raw))
	r, status, _, _, err := newResolver(ctx, b, p, kinds)
	if err != nil {
		return nil, err
	}
	out := []found{}
	if status != "" {
		return map[string]any{"completions": out}, nil
	}
	ids := prefix == "" || isHex(prefix)
	for _, k := range kinds {
		var labels, ords, shorts []found
		all := r.filter(k, func(string, string) bool { return true })
		for _, t := range all {
			if l, _ := t["label"].(string); l != "" && strings.HasPrefix(strings.ToLower(l), prefix) {
				labels = append(labels, found{"value": l, "form": "label", "target": t})
			}
			if n, ok := t["ordinal"].(int); ok && strings.HasPrefix(strconv.Itoa(n), prefix) {
				ords = append(ords, found{"value": strconv.Itoa(n), "form": "ordinal", "target": t})
			}
			if id := short(t[k+"_id"].(string)); ids && strings.HasPrefix(id, prefix) {
				shorts = append(shorts, found{"value": id, "form": "id", "target": t})
			}
		}
		sort.SliceStable(labels, func(i, j int) bool {
			return strings.ToLower(labels[i]["value"].(string)) < strings.ToLower(labels[j]["value"].(string))
		})
		sort.SliceStable(ords, func(i, j int) bool {
			return ords[i]["target"].(found)["ordinal"].(int) < ords[j]["target"].(found)["ordinal"].(int)
		})
		sort.SliceStable(shorts, func(i, j int) bool { return shorts[i]["value"].(string) < shorts[j]["value"].(string) })
		out = append(append(append(out, labels...), ords...), shorts...)
	}
	return map[string]any{"completions": out}, nil
}

// short is the last six hex digits of an ID.
func short(id string) string {
	h := strings.ReplaceAll(id, "-", "")
	if len(h) < 6 {
		return h
	}
	return h[len(h)-6:]
}
