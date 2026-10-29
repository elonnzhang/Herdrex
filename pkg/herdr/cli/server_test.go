package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// isolate gives this test process a made-up home and no Herdr variables, so
// the user's config directory that StartServer protects is home/.config/herdr
// under a temporary directory. It returns home.
func isolate(t *testing.T) string {
	home := tempDir(t)
	t.Setenv("HOME", home)
	for _, key := range []string{"XDG_CONFIG_HOME", "HERDR_SOCKET_PATH", "HERDR_CLIENT_SOCKET_PATH", "HERDR_CONFIG_PATH", "HERDR_SESSION"} {
		unsetenv(t, key)
	}
	return home
}

// server returns a CLI that StartServer accepts: its own config directory
// and socket under f.dir.
func (f *fake) server() *CLI {
	isolate(f.t)
	c := f.cli()
	c.Env = append(c.Env, "XDG_CONFIG_HOME="+filepath.Join(f.dir, "iso"), "HERDR_SOCKET_PATH="+filepath.Join(f.dir, "herdr.sock"))
	return c
}

// start starts the fake server and makes sure it ends with the test.
func start(t *testing.T, c *CLI) *Server {
	t.Helper()
	s, err := c.StartServer(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Stop(context.Background()) })
	return s
}

// pidFile is the file the fake server writes its pid to for socket.
func pidFile(socket string) string {
	return "server" + strings.ReplaceAll(socket, "/", "_") + ".pid"
}

// waitNoFake waits until no process runs f's executable, which is in the
// command line of every fake herdr process.
func waitNoFake(t *testing.T, f *fake) {
	t.Helper()
	waitFor(t, "no fake herdr processes", func() bool { return len(pgrep(t, f.bin)) == 0 })
}

func TestStartServerRefuses(t *testing.T) {
	f := newFake(t)
	home := isolate(t)
	userCfg := filepath.Join(home, ".config")
	userXDG := filepath.Join(home, "xdg")
	iso := "XDG_CONFIG_HOME=" + filepath.Join(f.dir, "iso")
	if err := os.MkdirAll(filepath.Join(userXDG, "herdr"), 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(f.dir, "link")
	if err := os.Symlink(userXDG, link); err != nil {
		t.Fatal(err)
	}
	const environ = "<os.Environ>"
	tests := []struct {
		name    string
		process map[string]string // this process's environment
		env     []string          // CLI.Env after FAKE_DIR; environ expands to os.Environ()
		want    string
	}{
		{"no XDG_CONFIG_HOME", nil, nil, "must set XDG_CONFIG_HOME"},
		{"empty XDG_CONFIG_HOME", nil, []string{"XDG_CONFIG_HOME=/tmp/x", "XDG_CONFIG_HOME="}, "must set XDG_CONFIG_HOME"},
		{"os.Environ", map[string]string{"XDG_CONFIG_HOME": userXDG}, []string{environ, "HERDR_SOCKET_PATH=" + filepath.Join(f.dir, "herdr.sock")}, "the user's config directory"},
		{"same XDG_CONFIG_HOME", map[string]string{"XDG_CONFIG_HOME": userXDG}, []string{"XDG_CONFIG_HOME=" + userXDG + "/"}, "the user's config directory"},
		{"symlinked XDG_CONFIG_HOME", map[string]string{"XDG_CONFIG_HOME": userXDG}, []string{"XDG_CONFIG_HOME=" + link}, "the user's config directory"},
		{"HOME's config", nil, []string{"XDG_CONFIG_HOME=" + userCfg}, "the user's config directory"},
		{"HOME's config while XDG_CONFIG_HOME is set", map[string]string{"XDG_CONFIG_HOME": userXDG}, []string{"XDG_CONFIG_HOME=" + userCfg}, "the user's config directory"},
		{"inside the user's config", nil, []string{"XDG_CONFIG_HOME=" + filepath.Join(userCfg, "herdr", "sessions")}, "the user's config directory"},
		{"inherited socket", map[string]string{"HERDR_SOCKET_PATH": "/tmp/u/herdr.sock"}, []string{iso}, "HERDR_SOCKET_PATH"},
		{"inherited socket repeated", map[string]string{"HERDR_SOCKET_PATH": "/tmp/u/herdr.sock"}, []string{iso, "HERDR_SOCKET_PATH=/tmp/u/herdr.sock"}, "HERDR_SOCKET_PATH"},
		{"inherited socket respelled", map[string]string{"HERDR_SOCKET_PATH": "/tmp/u/herdr.sock"}, []string{iso, "HERDR_SOCKET_PATH=/tmp/u/../u/herdr.sock"}, "HERDR_SOCKET_PATH"},
		{"inherited client socket", map[string]string{"HERDR_CLIENT_SOCKET_PATH": "/tmp/u/c.sock"}, []string{iso, "HERDR_CLIENT_SOCKET_PATH=/tmp/u/c.sock"}, "HERDR_CLIENT_SOCKET_PATH"},
		{"inherited config file", map[string]string{"HERDR_CONFIG_PATH": "/tmp/u/config.toml"}, []string{iso}, "HERDR_CONFIG_PATH"},
		{"user's socket", nil, []string{iso, "HERDR_SOCKET_PATH=" + filepath.Join(userCfg, "herdr", "herdr.sock")}, "HERDR_SOCKET_PATH"},
		{"user's session socket", nil, []string{iso, "HERDR_SOCKET_PATH=" + filepath.Join(userCfg, "herdr", "sessions", "w", "herdr.sock")}, "HERDR_SOCKET_PATH"},
		{"user's XDG socket", map[string]string{"XDG_CONFIG_HOME": userXDG}, []string{iso, "HERDR_SOCKET_PATH=" + filepath.Join(userXDG, "herdr", "herdr.sock")}, "HERDR_SOCKET_PATH"},
		{"user's client socket", nil, []string{iso, "HERDR_CLIENT_SOCKET_PATH=" + filepath.Join(userCfg, "herdr", "herdr-client.sock")}, "HERDR_CLIENT_SOCKET_PATH"},
		{"user's config file", nil, []string{iso, "HERDR_CONFIG_PATH=" + filepath.Join(userCfg, "herdr", "config.toml")}, "HERDR_CONFIG_PATH"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for k, v := range tt.process {
				t.Setenv(k, v)
			}
			c := f.cli()
			for _, kv := range tt.env {
				if kv == environ {
					c.Env = append(c.Env, os.Environ()...)
				} else {
					c.Env = append(c.Env, kv)
				}
			}
			_, err := c.StartServer(t.Context())
			if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), "not started") {
				t.Errorf("err = %v, want one naming %q", err, tt.want)
			}
		})
	}
	if args := f.log("args"); args != nil {
		t.Errorf("refusals ran herdr: %q", args)
	}

	f.write("foreign", "")
	_, err := f.server().StartServer(t.Context())
	if err == nil || !strings.Contains(err.Error(), "already running at "+filepath.Join(f.dir, "herdr.sock")) {
		t.Errorf("foreign server: err = %v", err)
	}
	if args := f.log("args"); !slices.Equal(args, []string{"[status][server][--json]"}) {
		t.Errorf("foreign server: ran %q", args)
	}
}

func TestStartServerExits(t *testing.T) {
	f := newFake(t)
	f.write("server.code", "1")
	f.write("server.err", "error: herdr server is already running\napi socket: /fake/herdr.sock\n")
	_, err := f.server().StartServer(t.Context())
	e, ok := err.(*Error)
	if !ok || e.ExitCode != 1 || !strings.Contains(e.Stderr, "api socket: /fake/herdr.sock") ||
		e.Error() != "herdr: exit status 1: error: herdr server is already running" {
		t.Errorf("err = %#v", err)
	}
}

func TestStartServerTimeout(t *testing.T) {
	f := newFake(t)
	f.write("never", "")
	ctx, cancel := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer cancel()
	_, err := f.server().StartServer(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("ctx deadline: err = %v, want DeadlineExceeded", err)
	}
	waitGone(t, f.pid("server.pid"))

	f = newFake(t)
	f.write("never", "")
	setTiming(t, &startTimeout, 300*time.Millisecond)
	_, err = f.server().StartServer(t.Context())
	if !errors.Is(err, context.DeadlineExceeded) || !strings.Contains(err.Error(), "not running after 300ms") {
		t.Errorf("own timeout: err = %v, want DeadlineExceeded", err)
	}
	waitGone(t, f.pid("server.pid"))
}

func TestStartStop(t *testing.T) {
	f := newFake(t)
	c := f.server()
	c.Session = "iso"
	c.Env = append(c.Env, "FAKE_PROBE=probe")
	s := start(t, c)
	pid := s.cmd.Process.Pid
	c.Env = nil // the server keeps the environment it started with
	c.Session = ""
	if err := s.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitGone(t, pid)
	if err := s.Stop(t.Context()); err != nil {
		t.Errorf("second Stop: %v", err)
	}
	args := f.log("args")
	if !slices.Contains(args, "[--session][iso][server]") || !slices.Contains(args, "[--session][iso][status][server][--json]") {
		t.Errorf("args = %q", args)
	}
	for _, v := range f.log("env") {
		if v != "probe" {
			t.Errorf("a run saw FAKE_PROBE = %q", v)
		}
	}
}

// TestStopOwnServer changes HERDR_SOCKET_PATH after StartServer to a socket
// where another server runs. Stop must end its own server, not that one.
func TestStopOwnServer(t *testing.T) {
	f := newFake(t)
	isolate(t)
	c := f.cli()
	c.Env = append(c.Env, "XDG_CONFIG_HOME="+filepath.Join(f.dir, "iso"))
	s := start(t, c)
	pid := s.cmd.Process.Pid

	other := filepath.Join(f.dir, "other.sock")
	cmd := exec.Command(f.bin, "server")
	cmd.Env = append(os.Environ(), "FAKE_DIR="+f.dir, "HERDR_SOCKET_PATH="+other)
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cmd.Process.Kill()
		cmd.Wait()
	})
	otherPid := f.pid(pidFile(other))

	t.Setenv("HERDR_SOCKET_PATH", other)
	if err := s.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	waitGone(t, pid)
	if err := syscall.Kill(otherPid, 0); err != nil {
		t.Errorf("the other server %d: %v", otherPid, err)
	}
	for _, line := range f.log("args") {
		if strings.Contains(line, "[stop]") {
			t.Errorf("Stop ran %s", line)
		}
	}
}

func TestStopStubborn(t *testing.T) {
	setTiming(t, &termWait, 200*time.Millisecond)
	f := newFake(t)
	f.write("stubborn", "")
	s := start(t, f.server())
	pid := s.cmd.Process.Pid
	err := s.Stop(t.Context())
	if err == nil || !strings.Contains(err.Error(), "killed") {
		t.Errorf("err = %v", err)
	}
	waitGone(t, pid)
}

func TestStopAfterExit(t *testing.T) {
	f := newFake(t)
	s := start(t, f.server())
	if err := syscall.Kill(s.cmd.Process.Pid, syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	select {
	case <-s.done:
	case <-time.After(10 * time.Second):
		t.Fatal("server did not exit")
	}
	err := s.Stop(t.Context())
	if e, ok := err.(*Error); !ok || e.ExitCode != -1 {
		t.Errorf("Stop = %v, want the server's exit", err)
	}
}

func TestStopCanceled(t *testing.T) {
	f := newFake(t)
	f.write("stubborn", "")
	s := start(t, f.server())
	pid := s.cmd.Process.Pid
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := s.Stop(ctx); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want Canceled", err)
	}
	waitGone(t, pid)
	waitNoFake(t, f)
}

// pgrep lists the processes whose command line contains s.
func pgrep(t *testing.T, s string) []int {
	t.Helper()
	out, err := exec.Command("pgrep", "-f", s).Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return nil // no match
	}
	if err != nil {
		t.Fatal(err)
	}
	var pids []int
	for _, f := range strings.Fields(string(out)) {
		if pid, err := strconv.Atoi(f); err == nil && pid != os.Getpid() {
			pids = append(pids, pid)
		}
	}
	return pids
}
