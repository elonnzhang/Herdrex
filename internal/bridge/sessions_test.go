package bridge

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"
)

// Reordering workspaces sends session_moved for the fewest moves that turn
// the old order into the new one, each placing a session just before the one
// that follows it, or last when none does.
func TestSessionMoves(t *testing.T) {
	for _, tc := range []struct {
		from, to []string
		want     []string // session_moved as "moved<before" or "moved<"
	}{
		{[]string{"a", "b", "c", "d"}, []string{"b", "c", "d", "a"}, []string{"a<"}},
		{[]string{"a", "b", "c", "d"}, []string{"d", "a", "b", "c"}, []string{"d<a"}},
		{[]string{"a", "b", "c"}, []string{"c", "b", "a"}, []string{"a<", "b<a"}},
		{[]string{"a", "b", "c"}, []string{"c", "a"}, []string{"a<"}},
		{[]string{"a", "b"}, []string{"a", "b"}, nil},
	} {
		f := newFakeHerdr(t)
		order := func(ids []string) func(*fakeState) {
			return func(s *fakeState) {
				*s = fakeState{layouts: s.layouts, controlled: s.controlled, external: s.external}
				for _, id := range ids {
					s.addWorkspace(id, id)
					s.addTab(id, id+":t1", id+":p1")
				}
			}
		}
		f.edit(order(tc.from))
		b, conn := newRexBridge(t, f)
		drain(t, conn)
		f.edit(order(tc.to))
		if err := b.Refresh(t.Context()); err != nil {
			t.Fatal(err)
		}
		herdrID := map[string]string{"": ""}
		for _, id := range tc.from {
			herdrID[b.IDs.Rex("session", id)] = id
		}
		var got []string
		for _, e := range drain(t, conn) {
			if e.Name != "session_moved" {
				continue
			}
			var p struct {
				Session string `json:"session_id"`
				Before  string `json:"before_session_id"`
			}
			json.Unmarshal(e.Payload, &p)
			got = append(got, herdrID[p.Session]+"<"+herdrID[p.Before])
		}
		if !slices.Equal(got, tc.want) {
			t.Errorf("%v -> %v: moves %v, want %v", tc.from, tc.to, got, tc.want)
		}
	}
}

// longestRun keeps a longest run of seq whose old positions rise.
func TestLongestRun(t *testing.T) {
	pos := map[string]int{"a": 0, "b": 1, "c": 2, "d": 3, "e": 4}
	for _, tc := range []struct{ seq, want []string }{
		{nil, nil},
		{[]string{"c"}, []string{"c"}},
		{[]string{"a", "d", "b", "c"}, []string{"a", "b", "c"}},
		{[]string{"e", "a", "b", "c", "d"}, []string{"a", "b", "c", "d"}},
		{[]string{"b", "c", "d", "e", "a"}, []string{"b", "c", "d", "e"}},
	} {
		got := slices.Sorted(maps.Keys(longestRun(tc.seq, pos)))
		if !slices.Equal(got, tc.want) {
			t.Errorf("longestRun(%v) = %v, want %v", tc.seq, got, tc.want)
		}
	}
}
