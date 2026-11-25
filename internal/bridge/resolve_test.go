package bridge

import (
	"encoding/json/v2"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

func twoWorkspaceHerdr(t *testing.T) *fakeHerdr {
	t.Helper()
	f := newFakeHerdr(t)
	f.edit(func(s *fakeState) {
		for _, ws := range []string{"w1", "w2"} {
			s.addWorkspace(ws, ws)
			s.addTab(ws, ws+":t1", ws+":p1")
		}
	})
	return f
}

func TestRexBlockSessionCLI(t *testing.T) {
	binary := os.Getenv("REX_TEST_BIN")
	if binary == "" {
		t.Skip("set REX_TEST_BIN to the Rex CLI (preview.23 or newer)")
	}
	b := New(twoWorkspaceHerdr(t).api)
	if err := b.Refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(b.Handler())
	t.Cleanup(srv.Close)
	home := t.TempDir()
	block, session := b.IDs.Rex("block", "w2:p1"), b.IDs.Rex("session", "w2")
	other := b.IDs.Rex("block", "w1:p1")
	for _, tc := range []struct {
		caller string
		args   []string
	}{
		{other, []string{"session", "inspect", block, "--json"}},
		{block, []string{"whoami", "--json"}},
		{other, []string{"api", "call", "-s", block, "session.view"}},
	} {
		t.Run(tc.args[0], func(t *testing.T) {
			cmd := exec.CommandContext(t.Context(), binary, append([]string{"--autostart=false"}, tc.args...)...)
			cmd.Env = []string{"HOME=" + home, "REX_SERVER=" + srv.URL, "REX_BLOCK=" + tc.caller}
			out, err := cmd.CombinedOutput()
			if err != nil {
				t.Fatalf("%v: %s", err, out)
			}
			var r struct {
				SessionID string `json:"session_id"`
			}
			if err := json.Unmarshal(out, &r); err != nil || r.SessionID != session {
				t.Fatalf("session_id = %q, want %q (decode: %v)", r.SessionID, session, err)
			}
		})
	}
}

// whoami decodes server.version before it resolves the caller's pane.
func TestServerVersionBuildTime(t *testing.T) {
	conn, _ := rexTestClient(t, New("/tmp/hx-no-herdr.sock"))
	var result struct {
		Version string     `json:"rex_version"`
		BuiltAt *time.Time `json:"built_at"`
	}
	call(t, conn, "server.version", nil, &result)
	if result.Version != Version {
		t.Fatalf("got version %q, want %q", result.Version, Version)
	}
}

func TestBlockSessionTarget(t *testing.T) {
	f := twoWorkspaceHerdr(t)
	f.edit(func(s *fakeState) {
		s.addTab("w2", "w2:t2", "w2:p2", "w2:p3")
		s.snap.Tabs[len(s.snap.Tabs)-1].Label = "background"
	})
	b, conn := newRexBridge(t, f)
	s1, s2 := b.IDs.Rex("session", "w1"), b.IDs.Rex("session", "w2")
	block, stale := b.IDs.Rex("block", "w2:p1"), b.IDs.Rex("block", "closed")
	blockCtx, conflict := P{"block_id": block}, P{"session_id": s1, "block_id": block}
	blockScope := P{"session": block}
	type target struct {
		Kind      string `json:"kind"`
		SessionID string `json:"session_id"`
		BlockID   string `json:"block_id"`
		WindowID  string `json:"window_id"`
	}
	session1, session2 := target{Kind: "session", SessionID: s1}, target{Kind: "session", SessionID: s2}
	window2 := target{Kind: "window", SessionID: s2, WindowID: b.IDs.Rex("window", "w2:t1")}
	block2 := target{Kind: "block", SessionID: s2, BlockID: block, WindowID: window2.WindowID}
	upper := "block:" + strings.ToUpper(strings.TrimPrefix(block, "block:"))
	otherWindow := target{Kind: "window", SessionID: s2, WindowID: b.IDs.Rex("window", "w2:t2")}
	background := target{Kind: "block", SessionID: s2, BlockID: b.IDs.Rex("block", "w2:p3"), WindowID: otherWindow.WindowID}
	backgroundCtx := P{"block_id": background.BlockID}
	for _, tc := range []struct {
		name, query string
		kinds       []string
		ctx, scope  P
		want        target
		why         string
	}{
		{"session by block", block, []string{"session"}, nil, nil, session2, ""},
		{"session or window by block", block, []string{"window", "session"}, nil, nil, session2, ""},
		{"block still names itself", block, []string{"session", "block"}, nil, nil, block2, ""},
		{"uppercase UUID identifies session", upper, []string{"session"}, nil, nil, session2, ""},
		{"uppercase UUID identifies block", upper, []string{"session", "block"}, nil, nil, block2, ""},
		{"uppercase block still respects scope", upper, []string{"block"}, nil, P{"session": s1}, target{}, ""},
		{"empty window selects caller window", "", []string{"window"}, backgroundCtx, nil, otherWindow, ""},
		{"empty block selects caller pane", "", []string{"block"}, backgroundCtx, nil, background, ""},
		{"window scope selects its focused pane", "", []string{"block"}, backgroundCtx, P{"window": window2.WindowID}, block2, ""},
		{"block alone supplies session", "", []string{"session"}, blockCtx, nil, session2, ""},
		{"block overrides inherited session", "", []string{"session"}, conflict, nil, session2, ""},
		{"closed block falls back", "", []string{"session"}, P{"session_id": s1, "block_id": stale}, nil, session1, ""},
		{"window ordinal in block scope", "1", []string{"window"}, P{"session_id": s1}, blockScope, window2, ""},
		{"closed block query", stale, []string{"session"}, nil, nil, target{}, `no open block has ID "` + stale + `"`},
		{"malformed block query", "block:bad", []string{"session"}, nil, nil, target{}, `"block:bad" is not a valid block ID`},
		{"closed block scope", "1", []string{"window"}, blockCtx, P{"session": stale}, target{}, `session scope: no open block has ID "` + stale + `"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := P{"queries": []string{tc.query}, "kinds": tc.kinds, "context": tc.ctx}
			if tc.scope != nil {
				p["scope"] = tc.scope
			}
			var reply struct {
				Results []struct {
					Status, Reason string
					Target         target
				}
			}
			call(t, conn, "target.resolve", p, &reply)
			status := "resolved"
			if tc.want.Kind == "" {
				status = "not_found"
			}
			if len(reply.Results) != 1 || reply.Results[0].Status != status || reply.Results[0].Target != tc.want || reply.Results[0].Reason != tc.why {
				t.Errorf("got %+v, want %s %+v %q", reply.Results, status, tc.want, tc.why)
			}
		})
	}
	for _, tc := range []struct {
		name       string
		ctx, scope P
	}{
		{"complete in block scope", P{"session_id": s1}, blockScope},
		{"complete in block context", conflict, nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var reply struct {
				Completions []struct{ Target target }
			}
			call(t, conn, "target.complete", P{"kinds": []string{"window"}, "prefix": "w", "scope": tc.scope, "context": tc.ctx}, &reply)
			if len(reply.Completions) != 1 || reply.Completions[0].Target != window2 {
				t.Errorf("got %+v, want only %+v", reply.Completions, window2)
			}
		})
	}
}
