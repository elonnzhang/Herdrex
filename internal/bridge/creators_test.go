package bridge

import (
	"go/ast"
	"go/parser"
	"go/token"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"
)

// block_creator.list names every block event the bridge sends, and no other,
// so a new blockEvent call cannot leave the creator entry behind.
func TestCreatorEvents(t *testing.T) {
	_, conn := newRecordHerdr(t, nil).connect(t)
	var r struct {
		Creators []struct {
			API struct {
				Events map[string]any `json:"events"`
			} `json:"api"`
		} `json:"creators"`
	}
	if err := conn.Call(t.Context(), "block_creator.list", nil, &r); err != nil {
		t.Fatal(err)
	}
	if len(r.Creators) != 1 {
		t.Fatalf("%d creators, want 1", len(r.Creators))
	}
	listed := slices.Sorted(maps.Keys(r.Creators[0].API.Events))
	if got, want := listed, sentEvents(t); !slices.Equal(got, want) {
		t.Errorf("block_creator.list events %v, the bridge sends %v", got, want)
	}
}

// sentEvents returns the event names the package's code passes to blockEvent.
func sentEvents(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	names := map[string]bool{}
	fset := token.NewFileSet()
	for _, name := range files {
		if strings.HasSuffix(name, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, name, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "blockEvent" || len(call.Args) != 4 {
				return true
			}
			lit, ok := call.Args[2].(*ast.BasicLit)
			if !ok {
				t.Errorf("%s: blockEvent with a name that is not a literal", fset.Position(call.Pos()))
				return true
			}
			s, _ := strconv.Unquote(lit.Value)
			names[s] = true
			return true
		})
	}
	return slices.Sorted(maps.Keys(names))
}
