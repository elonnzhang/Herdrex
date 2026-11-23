package util

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// shortDir is a directory for sockets: t.TempDir is too long for a socket
// path on macOS.
func shortDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("/tmp", "hxu")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestListenUnix(t *testing.T) {
	path := filepath.Join(shortDir(t), "new", "rex.sock")
	ln, err := ListenUnix(path)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("socket mode %v, want 0600", fi.Mode().Perm())
	}
	if di, _ := os.Stat(filepath.Dir(path)); di.Mode().Perm() != 0o700 {
		t.Errorf("new directory mode %v, want 0700", di.Mode().Perm())
	}
	ln.Close()
	if _, err := os.Lstat(path); !os.IsNotExist(err) {
		t.Errorf("socket left after Close: %v", err)
	}
}

func TestListenUnixStale(t *testing.T) {
	path := filepath.Join(shortDir(t), "rex.sock")
	old, err := net.ListenUnix("unix", &net.UnixAddr{Name: path, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	old.SetUnlinkOnClose(false)
	old.Close() // the socket file stays, and nothing listens on it
	ln, err := ListenUnix(path)
	if err != nil {
		t.Fatalf("stale socket not replaced: %v", err)
	}
	ln.Close()
}

func TestListenUnixRefuses(t *testing.T) {
	dir := shortDir(t)
	live := filepath.Join(dir, "live.sock")
	other, err := net.Listen("unix", live)
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	file := filepath.Join(dir, "file")
	if err := os.WriteFile(file, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ path, want string }{
		{live, "another server listens there"},
		{file, "not a socket"},
		{"rel/rex.sock", "absolute path"},
		{"/tmp/" + strings.Repeat("x", 120), "at most"},
	} {
		if ln, err := ListenUnix(tc.path); err == nil {
			ln.Close()
			t.Errorf("ListenUnix(%q) succeeded", tc.path)
		} else if !strings.Contains(err.Error(), tc.want) {
			t.Errorf("ListenUnix(%q): %v, want %q", tc.path, err, tc.want)
		}
	}
	if c, err := net.Dial("unix", live); err != nil {
		t.Errorf("the other server's socket is gone: %v", err)
	} else {
		c.Close()
	}
	if b, _ := os.ReadFile(file); string(b) != "keep" {
		t.Errorf("the file was changed: %q", b)
	}
}

// TestAllowMiddlewareUnix serves through Middleware on a Unix socket and on
// loopback TCP. With no --allow, the socket's caller gets in; a request that
// shows neither a Unix listener nor an address is refused.
func TestAllowMiddlewareUnix(t *testing.T) {
	l, err := ParseAllow(nil)
	if err != nil {
		t.Fatal(err)
	}
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	path := filepath.Join(shortDir(t), "rex.sock")
	ln, err := ListenUnix(path)
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: l.Middleware(ok)}
	go srv.Serve(ln)
	t.Cleanup(func() { srv.Close() })

	c := &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, "unix", path)
		},
	}}
	resp, err := c.Get("http://rex/api/control")
	if err != nil {
		t.Fatal(err)
	}
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Errorf("Unix socket caller: %d, want %d", resp.StatusCode, http.StatusNoContent)
	}

	req := httptest.NewRequest("GET", "/api/control", nil)
	req.RemoteAddr = "" // what a Unix caller shows, but without the listener
	rec := httptest.NewRecorder()
	l.Middleware(ok).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Errorf("caller without an address: %d, want %d", rec.Code, http.StatusForbidden)
	}
}
