package bridge

import (
	"os"
	"strings"
	"testing"

	"github.com/elonnzhang/herdrex/pkg/rex"
)

func TestFileName(t *testing.T) {
	for _, name := range []string{"probe.png", "a b.png", "图片.png", ".hidden"} {
		if !fileName(name) {
			t.Errorf("fileName(%q) = false", name)
		}
	}
	for _, name := range []string{"", ".", "..", "/etc/passwd", "dir/a.png", `a\b.png`, "a\x00b.png", "a\nb.png"} {
		if fileName(name) {
			t.Errorf("fileName(%q) = true", name)
		}
	}
}

func TestParseUpload(t *testing.T) {
	const ref = "upload:3f2f8a54-1c1e-4b6a-9c5e-1d2f3a4b5c6d"
	good := P{"ref": ref, "name": "probe.png", "size": float64(3)}
	if s, err := parseUpload(good); err != nil || s.count != 1 || s.size != 3 {
		t.Fatalf("parseUpload(good) = %+v %v", s, err)
	}
	batch := P{"ref": ref, "name": "probe.png", "size": float64(3), "batch": "b", "index": float64(1), "count": float64(2)}
	if s, err := parseUpload(batch); err != nil || s.index != 1 || s.count != 2 {
		t.Fatalf("parseUpload(batch) = %+v %v", s, err)
	}
	for name, args := range map[string]P{
		"ref without upload:":   {"ref": "3f2f8a54-1c1e-4b6a-9c5e-1d2f3a4b5c6d", "name": "a", "size": float64(0)},
		"ref without a UUID":    {"ref": "upload:nope", "name": "a", "size": float64(0)},
		"a name with a slash":   {"ref": ref, "name": "a/b", "size": float64(0)},
		"a negative size":       {"ref": ref, "name": "a", "size": float64(-1)},
		"index without a batch": {"ref": ref, "name": "a", "size": float64(0), "index": float64(1)},
		"count without a batch": {"ref": ref, "name": "a", "size": float64(0), "count": float64(2)},
		"a batch without count": {"ref": ref, "name": "a", "size": float64(0), "batch": "b"},
		"index past count":      {"ref": ref, "name": "a", "size": float64(0), "batch": "b", "index": float64(2), "count": float64(2)},
	} {
		if _, err := parseUpload(args); err == nil {
			t.Errorf("parseUpload(%s) succeeded", name)
		} else if re, ok := err.(*rex.Error); !ok || re.Code != rex.CodeBadRequest {
			t.Errorf("parseUpload(%s) = %v", name, err)
		}
	}
}

// A batch types one path per file in index order, and only once every file
// has arrived.
func TestStore(t *testing.T) {
	b := &Bridge{uploads: map[string]*uploadBatch{}}
	u := &uploadBatch{key: "b", pane: "w1:p1", files: make([]*uploadFile, 2)}
	u.files[1] = &uploadFile{name: "b.png", size: 2, index: 1, got: []byte("bb")}
	u.files[0] = &uploadFile{name: "a.png", size: 2, index: 0, got: []byte("aa")}
	b.uploads[u.key] = u

	paths, err := b.store(u, u.files[1])
	if err != nil || paths != nil {
		t.Fatalf("half a batch: %v %v", paths, err)
	}
	paths, err = b.store(u, u.files[0])
	if err != nil {
		t.Fatal(err)
	}
	if len(paths) != 2 || !strings.HasSuffix(paths[0], "/a.png") || !strings.HasSuffix(paths[1], "/b.png") {
		t.Fatalf("paths %v", paths)
	}
	for i, want := range []string{"aa", "bb"} {
		got, err := os.ReadFile(paths[i])
		if err != nil || string(got) != want {
			t.Errorf("file %d = %q %v", i, got, err)
		}
		os.RemoveAll(paths[i][:strings.LastIndex(paths[i], "/")])
	}
	if _, ok := b.uploads[u.key]; ok {
		t.Fatal("a whole batch is still remembered")
	}
}
