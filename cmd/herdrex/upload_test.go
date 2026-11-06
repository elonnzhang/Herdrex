package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/elonnzhang/herdrex/pkg/util"
)

// sshUploads writes the file through the remote shell and returns its path
// there, skipping what a login shell prints first. A fake ssh runs the remote
// command locally, as the SSH host would.
func TestSSHUploads(t *testing.T) {
	bin := t.TempDir()
	fake := filepath.Join(bin, "ssh")
	script := "#!/bin/sh\nfor a; do last=$a; done\necho 'Welcome to the host'\nexec /bin/sh -c \"$last\"\n"
	if err := os.WriteFile(fake, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	t.Setenv("TMPDIR", tmp)

	s := sshUploads{ssh: fake, base: []string{"-o", "ControlPath=/nonexistent"}, host: "host"}
	for _, name := range []string{"a.png", "x;id;#", "it's $(id).png", "图片.png"} {
		path, err := s.Store(t.Context(), name, []byte("data:"+name))
		if err != nil {
			t.Fatalf("%q: %v", name, err)
		}
		if filepath.Base(path) != name || !strings.HasPrefix(path, tmp) {
			t.Fatalf("%q: path %q is not %q under %s", name, path, name, tmp)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != "data:"+name {
			t.Fatalf("%q: file holds %q %v", name, got, err)
		}
		info, err := os.Stat(filepath.Dir(path))
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("%q: directory mode %v %v", name, info.Mode(), err)
		}
	}
}

func TestSSHUploadsFails(t *testing.T) {
	fake := filepath.Join(t.TempDir(), "ssh")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho 'Permission denied' >&2\nexit 255\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	s := sshUploads{ssh: fake, host: "host"}
	if _, err := s.Store(t.Context(), "a.png", []byte("x")); err == nil || !strings.Contains(err.Error(), "Permission denied") {
		t.Fatalf("err = %v", err)
	}
}

// The URL handed to Rex hosts is one the allow list lets in: a LAN one that
// --allow names, else the Tailscale one, which needs no --allow, else loopback.
func TestFirstPublic(t *testing.T) {
	urls := []string{"http://127.0.0.1:7388", "http://192.168.1.5:7388", "http://100.64.0.9:7388"}
	for _, tc := range []struct {
		allow []string
		want  string
	}{
		{nil, "http://100.64.0.9:7388"},
		{[]string{"192.168.1.0/24"}, "http://192.168.1.5:7388"},
		{[]string{"100.64.0.0/10"}, "http://100.64.0.9:7388"},
		{[]string{"10.0.0.0/8"}, "http://100.64.0.9:7388"},
	} {
		l, err := util.ParseAllow(tc.allow)
		if err != nil {
			t.Fatal(err)
		}
		if got := firstPublic(urls, l); got != tc.want {
			t.Errorf("--allow %v: %s, want %s", tc.allow, got, tc.want)
		}
	}
	if got := firstPublic([]string{"http://100.64.0.9:7388"}, util.AllowList{}); got != "http://100.64.0.9:7388" {
		t.Errorf("only a Tailscale URL: %s", got)
	}
}
