package bridge

import (
	"context"
	"os"
	"path/filepath"
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

// LocalUploads writes each file, private to the user, in a directory of its
// own under the temporary directory.
func TestLocalUploads(t *testing.T) {
	t.Setenv("TMPDIR", t.TempDir())
	paths := map[string]bool{}
	for range 2 {
		path, err := LocalUploads{}.Store(t.Context(), "a.png", []byte("aa"))
		if err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(path)
		info, _ := os.Stat(path)
		if err != nil || string(got) != "aa" || filepath.Base(path) != "a.png" || info.Mode().Perm() != 0o600 {
			t.Fatalf("%s holds %q %v", path, got, err)
		}
		paths[path] = true
	}
	if len(paths) != 2 {
		t.Fatal("two uploads of a.png share a path")
	}
}

// fakeStore records what it was asked to store, as a remote store would.
type fakeStore struct {
	got map[string][]byte
	err error
}

func (s *fakeStore) Store(_ context.Context, name string, data []byte) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	s.got[name] = data
	return "/remote/tmp/herdrex-upload-x/" + name, nil
}

func TestShellWord(t *testing.T) {
	for in, want := range map[string]string{
		"/tmp/herdrex-upload-1/a.png": "/tmp/herdrex-upload-1/a.png",
		"/tmp/d/a b.png":              "'/tmp/d/a b.png'",
		"/tmp/d/x;id;#":               "'/tmp/d/x;id;#'",
		"/tmp/d/it's.png":             `'/tmp/d/it'\''s.png'`,
		"/tmp/d/$(id).png":            "'/tmp/d/$(id).png'",
		"/tmp/d/图片.png":               "'/tmp/d/图片.png'",
	} {
		if got := shellWord(in); got != want {
			t.Errorf("shellWord(%q) = %s, want %s", in, got, want)
		}
	}
}

// A batch's count is bounded, since the bridge allocates a slot per file.
func TestParseUploadCountBound(t *testing.T) {
	const ref = "upload:3f2f8a54-1c1e-4b6a-9c5e-1d2f3a4b5c6d"
	args := P{"ref": ref, "name": "a", "size": float64(0), "batch": "b", "index": float64(0), "count": float64(1e9)}
	if _, err := parseUpload(args); err == nil {
		t.Fatal("a batch of 1e9 files parsed")
	}
	args["count"] = float64(maxUploadCount)
	if _, err := parseUpload(args); err != nil {
		t.Fatalf("a batch of %d files: %v", maxUploadCount, err)
	}
}
