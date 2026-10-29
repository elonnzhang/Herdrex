package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestLive runs the real herdr, named by HERDR_TEST_BIN, against a server it
// starts in a fresh temporary directory:
//
//	HERDR_TEST_BIN=$(command -v herdr) go test -run Live -v ./pkg/herdr/cli/
func TestLive(t *testing.T) {
	herdrBin := os.Getenv("HERDR_TEST_BIN")
	if herdrBin == "" {
		t.Skip("HERDR_TEST_BIN is not set")
	}
	// Not t.TempDir: Unix socket paths must stay under 104 bytes on macOS.
	dir, err := os.MkdirTemp("/tmp", "hxc")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	config := filepath.Join(dir, "config")
	write(t, filepath.Join(config, "herdr", "config.toml"),
		"[update]\nversion_check = false\nmanifest_check = false\n")
	// Run herdr through a link in dir, so dir is in the command line of every
	// herdr process this test starts and pgrep -f dir finds them.
	bin := filepath.Join(dir, "herdr")
	if err := os.Symlink(herdrBin, bin); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(config, "herdr", "herdr.sock")
	c := &CLI{Bin: bin, Env: []string{
		"XDG_CONFIG_HOME=" + config,
		"XDG_STATE_HOME=" + filepath.Join(dir, "state"),
		"HERDR_SOCKET_PATH=" + socket,
	}}
	ctx := t.Context()

	t.Run("Refuses", func(t *testing.T) {
		// Isolate this process too, so a broken check cannot reach the
		// user's server or config.
		for _, kv := range c.Env {
			k, v, _ := strings.Cut(kv, "=")
			t.Setenv(k, v)
		}
		_, err := (&CLI{Bin: bin}).StartServer(ctx)
		if err == nil || !strings.Contains(err.Error(), "XDG_CONFIG_HOME") {
			t.Fatalf("StartServer with this process's XDG_CONFIG_HOME: %v", err)
		}
		for _, args := range [][]string{nil, {"server"}, {"session", "attach", "x"}} {
			if _, err := (&CLI{Bin: bin}).Run(ctx, args...); !errors.Is(err, ErrRefused) {
				t.Errorf("Run(%q) = %v, want ErrRefused", args, err)
			}
		}
	})
	if pids := pgrep(t, dir); len(pids) != 0 {
		t.Fatalf("processes before StartServer: %v", pids)
	}

	s, err := c.StartServer(ctx)
	if err != nil {
		t.Fatal(err)
	}
	pid := s.cmd.Process.Pid
	defer func() {
		s.Stop(context.Background())
		if pids := pgrep(t, dir); len(pids) != 0 {
			t.Errorf("processes left after Stop: %v", pids)
		}
	}()
	if pids := pgrep(t, dir); !slices.Contains(pids, pid) {
		t.Errorf("pgrep -f %s = %v, want the server %d", dir, pids, pid)
	}
	if _, err := c.StartServer(ctx); err == nil || !strings.Contains(err.Error(), "already running at "+socket) {
		t.Errorf("second StartServer: %v", err)
	}

	st, err := c.Status(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Server.Running || st.Server.Status != "running" || st.Server.Protocol != 22 ||
		st.Server.Socket != socket || st.Client.Protocol != 22 || st.Server.Capabilities == nil ||
		st.Server.Capabilities.EndpointProtocolGeneration != 1 {
		t.Errorf("Status = %+v", st)
	}

	sessions, err := c.Sessions(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) == 0 || sessions[0].Name != "default" || !sessions[0].Default ||
		!sessions[0].Running || sessions[0].SocketPath != socket {
		t.Errorf("Sessions = %+v", sessions)
	}

	schema, err := c.Schema(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(schema), `"protocol": 22`) {
		t.Errorf("schema starts %.100s", schema)
	}

	if v, err := c.Version(ctx); err != nil || v != "0.9.5" {
		t.Errorf("Version = %q, %v", v, err)
	}

	// Herdr's own error output.
	_, err = c.StopSession(ctx, "nope")
	var e *Error
	if !errors.As(err, &e) || e.Code != "session_stop_failed" || e.ExitCode != 1 {
		t.Errorf("StopSession(nope) = %v", err)
	}
	_, err = c.DeleteSession(ctx, "default")
	if !errors.As(err, &e) || e.Code != "session_delete_failed" || e.ExitCode != 1 {
		t.Errorf("DeleteSession(default) = %v", err)
	}
	if ss, err := c.DeleteSession(ctx, "nope"); err != nil || ss.Name != "nope" || ss.Running {
		t.Errorf("DeleteSession(nope) = %+v, %v", ss, err)
	}
	_, err = c.Run(ctx, "session", "list", "--bogus")
	if !errors.As(err, &e) || e.Code != "" || e.ExitCode != 2 || !strings.Contains(e.Stderr, "usage:") {
		t.Errorf("usage error = %#v", err)
	}

	begin := time.Now()
	if err := s.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if d := time.Since(begin); d > 5*time.Second {
		t.Errorf("Stop took %v, want under 5s", d)
	} else {
		t.Logf("Stop took %v", d)
	}
	waitGone(t, pid)
	st, err = c.Status(ctx)
	if err != nil || st.Server.Running {
		t.Errorf("Status after Stop = %+v, %v", st, err)
	}
}

func write(t *testing.T, path, data string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		t.Fatal(err)
	}
}
