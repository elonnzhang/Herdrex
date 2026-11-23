package client_test

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/elonnzhang/herdrex/pkg/rex/client"
)

// These tests show how rex reaches the server of an ssh://USER@HOST host,
// with this machine standing in for the other one. They skip unless
// REX_TEST_BIN names Rex's rex. Each run starts its own Rex server with HOME
// under /tmp/rxssh*, and puts a fake ssh (TestSSHHelper) first on PATH, so
// no real ssh, Rex home or Rex server is used.
//
//   - rex __internal dial-stdio copies its stdin and stdout to the Rex server
//     on its own machine, byte for byte. It reaches only a Unix socket or a
//     named pipe there, never a TCP address.
//   - rex __internal ssh-tunnel, which Rex.app runs, keeps a local Unix socket
//     open to an ssh:// host's server until its stdin closes. In "forward"
//     mode that socket is ssh -N -L to the remote server's socket. When the
//     remote server listens only on a named pipe, as on Windows, it is in
//     "stdio" mode: each connection runs dial-stdio there over ssh.
//   - ssh://USER@HOST/PATH names the remote socket: rex runs nothing on the
//     remote host and forwards straight to PATH.

// sshEnv is a Rex server standing in for the one on the remote host, and a
// fake ssh that runs the remote commands here, against that server.
type sshEnv struct {
	rex    string // Rex's rex
	dir    string // under /tmp: bin/ssh, unix/rex, pipe/rex, remote/, ssh.log
	remote string // HOME on the stand-in remote host
	extra  string // a second socket of the server there, outside that HOME
	hostID string // host_id of the server there
}

func startSSHEnv(t *testing.T) *sshEnv {
	t.Helper()
	bin := os.Getenv("REX_TEST_BIN")
	if bin == "" {
		t.Skip("set REX_TEST_BIN to Rex's rex")
	}
	// Not t.TempDir: Unix socket paths must stay under 104 bytes on macOS.
	dir, err := os.MkdirTemp("/tmp", "rxssh")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	t.Setenv("REXSSH_DIR", dir)
	t.Setenv("REXSSH_REX", bin)
	e := &sshEnv{rex: bin, dir: dir, remote: filepath.Join(dir, "remote"), extra: filepath.Join(dir, "extra.sock")}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"bin", "unix", "pipe", "remote"} {
		if err := os.Mkdir(filepath.Join(dir, d), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	// bin/ssh is the fake ssh; unix/rex is Rex's rex, and pipe/rex is a rex
	// whose server reports a named pipe. The fake ssh picks one by host.
	helper := func(role string) string {
		return fmt.Sprintf("#!/bin/sh\nREXSSH_HELPER=%s exec %s -test.run='^TestSSHHelper$' -- \"$@\"\n", role, shellQuote(self))
	}
	if err := os.WriteFile(filepath.Join(dir, "bin", "ssh"), []byte(helper("ssh")), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "pipe", "rex"), []byte(helper("rex")), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(bin, filepath.Join(dir, "unix", "rex")); err != nil {
		t.Fatal(err)
	}

	logf, err := os.Create(filepath.Join(dir, "server.log"))
	if err != nil {
		t.Fatal(err)
	}
	srv := exec.Command(bin, "server", "run", "--restore=false", "--extra-listen", "unix://"+e.extra)
	srv.Env = sshEnviron(e.remote, "")
	srv.Stdout, srv.Stderr = logf, logf
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		srv.Process.Signal(syscall.SIGTERM)
		srv.Wait()
		logf.Close()
	})
	for deadline := time.Now().Add(15 * time.Second); ; {
		st := exec.Command(bin, "--autostart=false", "server", "status", "--json")
		st.Env = srv.Env
		out, err := st.Output()
		var status struct {
			HostID string `json:"host_id"`
		}
		if err == nil && json.Unmarshal(out, &status) == nil && status.HostID != "" {
			e.hostID = status.HostID
			return e
		}
		if time.Now().After(deadline) {
			log, _ := os.ReadFile(logf.Name())
			t.Fatalf("the Rex server did not start: %v\n%s", err, log)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// checkServer dials sock with the Go client, and checks that the server
// that answers is the one on the stand-in remote host.
func (e *sshEnv) checkServer(t *testing.T, sock string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	u := (&url.URL{Scheme: "unix", Path: sock}).String()
	c, err := client.Dial(ctx, u, &client.Options{Kind: "cli", Name: "rex-go-test"})
	if err != nil {
		t.Fatalf("dial %s: %v", u, err)
	}
	defer c.Close()
	var st struct {
		HostID string `json:"host_id"`
	}
	if err := c.Call(ctx, "server.status", nil, &st); err != nil {
		t.Fatal(err)
	}
	if st.HostID != e.hostID {
		t.Errorf("server.status through %s: host_id %q, want the remote server's %q", sock, st.HostID, e.hostID)
	}
}

// TestDialStdio serves a Unix socket where each connection is one
// rex __internal dial-stdio, as ssh-tunnel's stdio mode does on the remote
// host, and checks that the Go client reaches the server through it. A TCP
// address, such as a herdrex bridge's, is refused.
func TestDialStdio(t *testing.T) {
	e := startSSHEnv(t)
	sock := filepath.Join(e.dir, "stdio.sock")
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: sock, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	go func() {
		for {
			c, err := ln.AcceptUnix()
			if err != nil {
				return
			}
			// The connection itself is dial-stdio's stdin and stdout, as
			// sshd would give it the channel.
			f, err := c.File()
			c.Close()
			if err != nil {
				continue
			}
			cmd := exec.Command(e.rex, "--autostart=false", "__internal", "dial-stdio")
			cmd.Env = sshEnviron(e.remote, "")
			cmd.Stdin, cmd.Stdout = f, f
			if err := cmd.Start(); err == nil {
				go cmd.Wait()
			}
			f.Close()
		}
	}()
	e.checkServer(t, sock)

	cmd := exec.Command(e.rex, "-S", "http://127.0.0.1:7388", "--autostart=false", "__internal", "dial-stdio")
	cmd.Env = sshEnviron(e.remote, "")
	out, err := cmd.CombinedOutput()
	if err == nil || !strings.Contains(string(out), "only connects to a Unix socket or named pipe") {
		t.Errorf("dial-stdio to a TCP address: %v, %q; want it refused", err, out)
	}
}

// TestSSHTunnel runs rex __internal ssh-tunnel as Rex.app does, and checks
// that the socket it reports reaches the remote host's server: through
// ssh -L when that server has a Unix socket, through dial-stdio over ssh when
// it reports only a named pipe. An endpoint with a path, ssh://HOST/PATH,
// runs nothing on the remote host and forwards straight to the socket PATH,
// so it reaches any server on a Unix socket there, not only Rex's.
func TestSSHTunnel(t *testing.T) {
	e := startSSHEnv(t)
	for _, tc := range []struct {
		name, host, path string
		mode             string
		ssh              string // what the fake ssh must have been asked to do
		rexThere         bool   // whether rex runs on the remote host
	}{
		{"forward", "ssh://tester@unixhost", "", "forward", "-L ", true},
		{"stdio", "ssh://tester@winhost", "", "stdio", "rex __internal dial-stdio", true},
		{"path", "ssh://tester@pathhost", e.extra, "forward", "-L ", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := filepath.Join(e.dir, "local-"+tc.name) // fresh: rex caches what it learns per host
			if err := os.Mkdir(home, 0o700); err != nil {
				t.Fatal(err)
			}
			cmd := exec.Command(e.rex, "__internal", "ssh-tunnel", "--no-prompt", tc.host+tc.path)
			cmd.Env = sshEnviron(home, filepath.Join(e.dir, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"))
			errf, err := os.Create(filepath.Join(home, "ssh-tunnel.err"))
			if err != nil {
				t.Fatal(err)
			}
			defer errf.Close()
			cmd.Stderr = errf
			stderr := func() string { b, _ := os.ReadFile(errf.Name()); return string(b) }
			stdin, err := cmd.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdout, err := cmd.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := cmd.Start(); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { cmd.Process.Kill() })
			lines := make(chan string)
			go func() {
				defer close(lines)
				for sc := bufio.NewScanner(stdout); sc.Scan(); {
					lines <- sc.Text()
				}
			}()

			var ready struct {
				Type, Mode, Network, Socket, Host string
				RemoteSocket                      string `json:"remote_socket"`
			}
			nextLine(t, lines, &ready, stderr)
			if ready.Type != "ready" || ready.Mode != tc.mode || ready.Network != "unix" {
				t.Fatalf("first line %+v, want a ready line in mode %s on a Unix socket", ready, tc.mode)
			}
			if tc.path != "" && ready.RemoteSocket != tc.path {
				t.Errorf("remote_socket %q, want the endpoint's path %q", ready.RemoteSocket, tc.path)
			}
			e.checkServer(t, ready.Socket)
			log := e.sshLog(t)
			if !strings.Contains(log, tc.host+": "+tc.ssh) {
				t.Errorf("ssh was not asked for %q:\n%s", tc.ssh, log)
			}
			if ran := strings.Contains(log, tc.host+": rex ") || strings.Contains(log, tc.host+": sh "); ran != tc.rexThere {
				t.Errorf("rex ran on the remote host: %v, want %v:\n%s", ran, tc.rexThere, log)
			}

			stdin.Close() // Rex.app closes it to end the tunnel
			// Other lines, such as {"type":"state"} saying why stdio mode
			// was chosen, may come before the exit line.
			var exit struct{ Type, Reason string }
			for exit.Type != "exit" {
				nextLine(t, lines, &exit, stderr)
				if exit.Type != "exit" {
					t.Logf("ssh-tunnel: %+v", exit)
				}
			}
			for range lines {
			}
			if err := cmd.Wait(); err != nil {
				t.Errorf("ssh-tunnel: %v\n%s", err, stderr())
			}
		})
	}
}

// nextLine decodes the next line ssh-tunnel prints into v.
func nextLine(t *testing.T, lines <-chan string, v any, stderr func() string) {
	t.Helper()
	select {
	case l, ok := <-lines:
		if !ok {
			t.Fatalf("ssh-tunnel ended its output\n%s", stderr())
		}
		if err := json.Unmarshal([]byte(l), v); err != nil {
			t.Fatalf("ssh-tunnel printed %q: %v", l, err)
		}
	case <-time.After(20 * time.Second):
		t.Fatalf("ssh-tunnel printed nothing in 20s\n%s", stderr())
	}
}

// TestSSHHelper is not a test. The tests run this binary as ssh, and as the
// rex of a remote host whose server listens on a named pipe.
func TestSSHHelper(t *testing.T) {
	role := os.Getenv("REXSSH_HELPER")
	if role == "" {
		t.Skip("run by TestSSHTunnel as ssh and rex")
	}
	var args []string
	if i := slices.Index(os.Args, "--"); i >= 0 {
		args = os.Args[i+1:]
	}
	switch role {
	case "ssh":
		os.Exit(fakeSSH(args))
	case "rex":
		os.Exit(pipeRex(args))
	}
	os.Exit(2)
}

// fakeSSH does what ssh would, as if the remote host were this machine: it
// answers -O control commands, serves -L LOCAL:REMOTE between two Unix
// sockets, and runs a remote command with sh -c under the remote HOME.
func fakeSSH(args []string) int {
	dir := os.Getenv("REXSSH_DIR")
	logf, err := os.OpenFile(filepath.Join(dir, "ssh.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake ssh:", err)
		return 255
	}
	var forward, control string
	i := 0
	for ; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			i++
			break
		}
		if !strings.HasPrefix(a, "-") {
			break
		}
		if len(a) != 2 || !strings.ContainsRune("bcDEeFIiJLlmOopQRSWw", rune(a[1])) {
			continue // a flag without a value
		}
		i++
		if i == len(args) {
			fmt.Fprintf(os.Stderr, "fake ssh: %s needs a value\n", a)
			return 255
		}
		switch a {
		case "-L":
			forward = args[i]
		case "-O":
			control = args[i]
		}
	}
	if i >= len(args) {
		fmt.Fprintln(os.Stderr, "fake ssh: no destination")
		return 255
	}
	dest, cmd := args[i], strings.Join(args[i+1:], " ")
	switch {
	case control != "":
		fmt.Fprintf(logf, "%s: -O %s\n", dest, control)
		return 0
	case forward != "":
		fmt.Fprintf(logf, "%s: -L %s\n", dest, forward)
		return relay(forward)
	case cmd == "":
		fmt.Fprintln(os.Stderr, "fake ssh: no remote command")
		return 255
	}
	fmt.Fprintf(logf, "%s: %s\n", dest, cmd)
	rexDir := "unix"
	if u, err := url.Parse(dest); err == nil && u.Hostname() == "winhost" {
		rexDir = "pipe"
	}
	env := sshEnviron(filepath.Join(dir, "remote"), filepath.Join(dir, rexDir)+":/usr/bin:/bin")
	err = syscall.Exec("/bin/sh", []string{"sh", "-c", cmd}, env)
	fmt.Fprintln(os.Stderr, "fake ssh:", err)
	return 255
}

// relay serves spec, LOCAL:REMOTE, as ssh -N -L does: every connection to
// the socket LOCAL is carried to the socket REMOTE, until ssh is killed.
func relay(spec string) int {
	local, remote, ok := strings.Cut(spec, ":")
	if !ok {
		fmt.Fprintf(os.Stderr, "fake ssh: -L %s: want LOCAL:REMOTE\n", spec)
		return 255
	}
	os.Remove(local) // StreamLocalBindUnlink=yes
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: local, Net: "unix"})
	if err != nil {
		fmt.Fprintln(os.Stderr, "fake ssh:", err)
		return 255
	}
	for {
		c, err := ln.AcceptUnix()
		if err != nil {
			return 255
		}
		go func() {
			defer c.Close()
			r, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: remote, Net: "unix"})
			if err != nil {
				return
			}
			defer r.Close()
			done := make(chan struct{})
			go func() { io.Copy(r, c); r.CloseWrite(); close(done) }()
			io.Copy(c, r)
			c.CloseWrite()
			<-done
		}()
	}
}

// pipeRex is the remote host's rex, but its server status reports a named
// pipe, as on Windows, which ssh -L cannot forward.
func pipeRex(args []string) int {
	bin := os.Getenv("REXSSH_REX")
	if !slices.Equal(args, []string{"server", "status", "--json"}) && !slices.Equal(args, []string{"server", "start", "--json"}) {
		err := syscall.Exec(bin, append([]string{bin}, args...), os.Environ())
		fmt.Fprintln(os.Stderr, "rex:", err)
		return 255
	}
	cmd := exec.Command(bin, args...)
	cmd.Stderr = os.Stderr
	out, err := cmd.Output()
	if err != nil {
		return 1
	}
	var st map[string]any
	if err := json.Unmarshal(out, &st); err != nil {
		fmt.Fprintln(os.Stderr, "rex:", err)
		return 1
	}
	const pipe = "npipe:////./pipe/rex"
	st["endpoint"] = pipe
	ls, _ := st["listeners"].([]any)
	for _, l := range ls {
		if m, ok := l.(map[string]any); ok {
			m["endpoint"], m["transport"], m["addresses"] = pipe, "npipe", []string{`\\.\pipe\rex`}
		}
	}
	if err := json.NewEncoder(os.Stdout).Encode(st); err != nil {
		return 1
	}
	return 0
}

// sshEnviron is this process's environment with HOME at home, PATH at path
// unless it is empty, and without the REX_ and XDG_ variables, which would
// point rex at the user's server, config or state. REXSSH_DIR and
// REXSSH_REX, which startSSHEnv sets, tell the helpers where the test keeps
// its files and Rex's rex.
func sshEnviron(home, path string) []string {
	var out []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "REX_") || strings.HasPrefix(kv, "XDG_") || strings.HasPrefix(kv, "HOME=") ||
			strings.HasPrefix(kv, "REXSSH_HELPER=") || (path != "" && strings.HasPrefix(kv, "PATH=")) {
			continue
		}
		out = append(out, kv)
	}
	out = append(out, "HOME="+home)
	if path != "" {
		out = append(out, "PATH="+path)
	}
	return out
}

// shellQuote quotes s for sh.
func shellQuote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// sshLog is what the fake ssh was asked to do, one line per call.
func (e *sshEnv) sshLog(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(e.dir, "ssh.log"))
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return string(b)
}
