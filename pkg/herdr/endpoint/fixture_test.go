package endpoint

import (
	"encoding/json"
	"os"
	"reflect"
	"testing"
)

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// Every field of Herdr's frozen hello has the same value in the hello this
// package sends for the same settings.
func TestHelloFixture(t *testing.T) {
	var fixture map[string]any
	if err := json.Unmarshal(readFixture(t, "endpoint-hello-v1.json"), &fixture); err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(Hello{
		Cols: 80, Rows: 24, CellWidthPx: 8, CellHeightPx: 16,
		PixelMouse: true, MouseCapture: true,
	}.json())
	var ours map[string]any
	json.Unmarshal(b, &ours)
	for k, v := range fixture {
		if !reflect.DeepEqual(ours[k], v) {
			t.Errorf("%s = %v, fixture has %v", k, ours[k], v)
		}
	}
}

func TestWelcomeFixture(t *testing.T) {
	var w Welcome
	if err := json.Unmarshal(readFixture(t, "endpoint-welcome-v1.json"), &w); err != nil {
		t.Fatal(err)
	}
	want := Welcome{
		Generation: 1, ServerVersion: "0.8.2", SnapshotCodec: "shell.snapshot.v1",
		SurfaceCodec: "shell.surface.v1", InputCodec: "shell.input.semantic.v1", BlobCodec: "shell.blob.v1",
		Methods: []string{"pane.focus"},
	}
	if !reflect.DeepEqual(w, want) {
		t.Errorf("got %+v, want %+v", w, want)
	}
	if err := w.check(); err != nil {
		t.Error(err)
	}

	var future Welcome
	if err := json.Unmarshal([]byte(`{"generation":1,"future_service":"v2","capabilities":["x"]}`), &future); err != nil {
		t.Fatal(err)
	}
	if future.Generation != 1 || len(future.Capabilities) != 1 {
		t.Errorf("got %+v", future)
	}
}

func TestSnapshotFixture(t *testing.T) {
	m, err := decodeControl(&Control{Kind: kindSnapshot, Data: string(readFixture(t, "endpoint-snapshot-v1.json"))})
	if err != nil {
		t.Fatal(err)
	}
	s := m.(*Snapshot)
	if s.BootID != "boot-v1" || s.Revision != 7 || s.ConfigDiagnostic != "endpoint warning" ||
		s.UpdateInstallCommand != "herdr update" || !s.LatestReleaseNotesAvailable ||
		s.FocusedPaneID != "w1:p1" || s.TabBarRightSeparator != " | " ||
		!reflect.DeepEqual(s.AgentOrder, []string{"w1:p1"}) {
		t.Errorf("top level: %+v", s)
	}
	if a := s.ProductAnnouncement; a == nil || a.ID != "announcement-v1" || a.Preview {
		t.Errorf("product_announcement %+v", a)
	}
	if n := s.ReleaseNotes; n == nil || n.Version != "1.0.1" || !n.Preview {
		t.Errorf("release_notes %+v", n)
	}
	if !reflect.DeepEqual(s.TabBarRight, []StatusSegment{{Text: "host", Accent: true}}) {
		t.Errorf("tab_bar_right %+v", s.TabBarRight)
	}
	ws := s.Workspaces[0]
	if ws.AgentStatus != AgentUnknown || ws.Number != 1 || ws.Branch != "main" ||
		*ws.GitAheadBehind != [2]int{1, 2} || ws.Tokens[0] != [2]string{"model", "opus"} ||
		ws.Worktree == nil || !ws.Worktree.IsLinkedWorktree {
		t.Errorf("workspace %+v", ws)
	}
	if tab := s.Tabs[0]; tab.AgentStatus != AgentWorking || tab.ID != "w1:t1" {
		t.Errorf("tab %+v", tab)
	}
	if p := s.Panes[0]; p.Label != "shell" || p.ForegroundCWD != "/repo" || !p.Focused {
		t.Errorf("pane %+v", p)
	}
	ag := s.Agents[0]
	if ag.AgentStatus != AgentBlocked || ag.StateChangeSeq != 9 || ag.DisplayAgent != "Claude" ||
		ag.StateLabels[0] != [2]string{"blocked", "waiting"} {
		t.Errorf("agent %+v", ag)
	}
	if c := s.Commands[0]; c.Action != ActionShell || c.BindingLabels[0] != "prefix+x" || c.Description != "Command" {
		t.Errorf("command %+v", c)
	}
}

func TestSnapshotFuture(t *testing.T) {
	var v map[string]any
	json.Unmarshal(readFixture(t, "endpoint-snapshot-v1.json"), &v)
	v["future_projection"] = map[string]any{"enabled": true}
	v["commands"] = []any{map[string]any{
		"command_id": "future", "binding_label": "x",
		"binding_labels": []string{"x"}, "action": "FutureAction", "description": nil,
	}}
	v["tabs"].([]any)[0].(map[string]any)["agent_status"] = "dreaming"
	b, _ := json.Marshal(v)
	m, err := decodeControl(&Control{Kind: kindSnapshot, Data: string(b)})
	if err != nil {
		t.Fatal(err)
	}
	s := m.(*Snapshot)
	if s.Commands[0].Action != ActionUnknown || s.Commands[0].Description != "" {
		t.Errorf("command %+v", s.Commands[0])
	}
	if s.Tabs[0].AgentStatus != AgentUnknown {
		t.Errorf("agent_status %q", s.Tabs[0].AgentStatus)
	}

	if _, err := decodeControl(&Control{Kind: kindSnapshot, Data: `{"revision":"x"}`}); err == nil {
		t.Error("invalid snapshot decoded")
	}
	if _, err := decodeControl(&Control{Kind: kindSnapshot, Data: `{"tabs":[{"agent_status":1}]}`}); err == nil {
		t.Error("numeric agent_status decoded")
	}
	if _, err := decodeControl(&Control{Kind: kindSnapshot, Data: `{"commands":[{"action":1}]}`}); err == nil {
		t.Error("numeric action decoded")
	}
}

func TestAgentControls(t *testing.T) {
	m, err := decodeControl(&Control{Kind: kindAgentView, Data: `{"boot_id":"b","revision":3,"view":{"source":"s"}}`})
	if err != nil {
		t.Fatal(err)
	}
	if v := m.(*AgentView); v.BootID != "b" || v.Revision != 3 || string(v.View) != `{"source":"s"}` {
		t.Errorf("agent view %+v", v)
	}
	m, err = decodeControl(&Control{Kind: kindCompletions, Data: `{"boot_id":"b","revision":4,"completions":{"w1:p1":2}}`})
	if err != nil {
		t.Fatal(err)
	}
	if c := m.(*AgentCompletions); c.Revision != 4 || c.Completions["w1:p1"] != 2 {
		t.Errorf("agent completions %+v", c)
	}
	if _, err := decodeControl(&Control{Kind: kindCompletions, Data: `[`}); err == nil {
		t.Error("invalid completions decoded")
	}
}
