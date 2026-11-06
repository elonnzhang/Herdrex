package main

import (
	"bufio"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// fakeHerdr is a herdr that answers status by whether its socket file exists,
// and for server creates that file and runs until SIGTERM.
const fakeHerdr = `#!/bin/sh
case "$1" in
status)
  if [ -e "$HERDR_SOCKET_PATH" ]; then echo '{"server":{"running":true}}'; else echo '{"server":{"running":false}}'; fi ;;
server)
  trap 'exit 0' TERM
  : > "$HERDR_SOCKET_PATH"
  while :; do sleep 0.1; done ;;
esac
`

// remoteHome returns a HOME with fakeHerdr in .local/bin.
func remoteHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "herdr"), []byte(fakeHerdr), 0o755); err != nil {
		t.Fatal(err)
	}
	return home
}

// startScript runs remoteStartScript for sock the way an SSH session would.
func startScript(t *testing.T, home, sock string) (*exec.Cmd, *bufio.Scanner, func()) {
	t.Helper()
	dir := strings.TrimSuffix(sock, filepath.Ext(sock)) + ".d"
	cmd := exec.Command("sh", "-c", remoteStartScript([]string{"$HOME/.local/bin"}), "sh", sock, dir, filepath.Join(filepath.Dir(sock), "herdr-client.sock"))
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill() })
	return cmd, bufio.NewScanner(stdout), func() { stdin.Close() }
}

func waitExit(t *testing.T, cmd *exec.Cmd) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("script did not exit in 10s")
		return nil
	}
}

// waitFile waits for the fake server to create its socket, which it does
// after it handles SIGTERM.
func waitFile(t *testing.T, name string) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(50 * time.Millisecond) {
		if _, err := os.Stat(name); err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s was never created", name)
		}
	}
}

func TestRemoteStartScriptStartsAndCleansUp(t *testing.T) {
	home := remoteHome(t)
	parent := filepath.Join(t.TempDir(), "made") // does not exist yet
	sock := filepath.Join(parent, "x.sock")
	cmd, out, closeStdin := startScript(t, home, sock)
	if !out.Scan() || out.Text() != remoteStarted {
		t.Fatalf("first line = %q, want %q", out.Text(), remoteStarted)
	}
	waitFile(t, sock)
	if _, err := os.Stat(filepath.Join(parent, "x.d", "config")); err != nil {
		t.Fatalf("config directory: %v", err)
	}
	closeStdin()
	if err := waitExit(t, cmd); err != nil {
		t.Fatalf("script: %v", err)
	}
	if _, err := os.Stat(parent); !os.IsNotExist(err) {
		t.Fatalf("parent directory the script made is still there: %v", err)
	}
}

func TestRemoteStartScriptKeepsExistingParent(t *testing.T) {
	home := remoteHome(t)
	parent := t.TempDir()
	cmd, out, closeStdin := startScript(t, home, filepath.Join(parent, "x.sock"))
	if !out.Scan() {
		t.Fatal("no output")
	}
	waitFile(t, filepath.Join(parent, "x.sock"))
	closeStdin()
	if err := waitExit(t, cmd); err != nil {
		t.Fatalf("script: %v", err)
	}
	if entries, err := os.ReadDir(parent); err != nil || len(entries) != 0 {
		t.Fatalf("parent = %v, %v; want it kept and empty", entries, err)
	}
}

func TestRemoteStartScriptRefuses(t *testing.T) {
	home := remoteHome(t)
	running := filepath.Join(t.TempDir(), "x.sock")
	if err := os.WriteFile(running, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for name, sock := range map[string]string{
		"in your config directory": filepath.Join(home, ".config", "herdr", "x.sock"),
		"already running":          running,
	} {
		t.Run(name, func(t *testing.T) {
			cmd, _, closeStdin := startScript(t, home, sock)
			defer closeStdin()
			var exit *exec.ExitError
			if err := waitExit(t, cmd); !errors.As(err, &exit) || exit.ExitCode() != 2 {
				t.Fatalf("err = %v, want exit status 2", err)
			}
			if _, err := os.Stat(filepath.Join(home, ".config")); !os.IsNotExist(err) {
				t.Fatalf("refused, but created %s: %v", filepath.Join(home, ".config"), err)
			}
		})
	}
}

func TestRemoteHerdrScriptSocketArgument(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	fake := "#!/bin/sh\necho \"$HERDR_SOCKET_PATH/$HERDR_SESSION\"\n"
	if err := os.WriteFile(filepath.Join(dir, "herdr"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", "-c", remoteHerdrScript([]string{"$HOME/.local/bin"}), "sh", "/tmp/a b.sock")
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin", "HERDR_SESSION=other"}
	out, err := cmd.Output()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := strings.TrimSpace(string(out)), "/tmp/a b.sock/"; got != want {
		t.Fatalf("socket and session = %q, want %q", got, want)
	}
}

// A server that fails, instead of being stopped, leaves the socket: it may be
// another server's, as with startHerdr.
func TestRemoteStartScriptKeepsSocketOfFailedServer(t *testing.T) {
	home := remoteHome(t)
	crash := "#!/bin/sh\ncase \"$1\" in\nstatus) echo '{\"server\":{\"running\":false}}' ;;\nserver) : > \"$HERDR_SOCKET_PATH\"; exit 1 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(home, ".local", "bin", "herdr"), []byte(crash), 0o755); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(t.TempDir(), "run", "x.sock")
	cmd, _, _ := startScript(t, home, sock)
	var ee *exec.ExitError
	if err := waitExit(t, cmd); !errors.As(err, &ee) || ee.ExitCode() != 1 {
		t.Fatalf("script exit = %v, want status 1 from the server", err)
	}
	if _, err := os.Stat(sock); err != nil {
		t.Errorf("socket of a failed server was removed: %v", err)
	}
	if _, err := os.Stat(strings.TrimSuffix(sock, ".sock") + ".d"); !os.IsNotExist(err) {
		t.Errorf("the .d directory the script made is still there (%v)", err)
	}
}
