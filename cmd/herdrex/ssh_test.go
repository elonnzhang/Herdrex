package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/elonnzhang/herdrex/pkg/util"
)

func TestParseSSHArgs(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		host  string
		port  int
		auto  bool
		ts    bool
		lan   []string
		allow []string
		err   bool
	}{
		{name: "alias", args: []string{"host"}, host: "host", port: 7388, auto: true},
		{name: "user@host", args: []string{"user@host"}, host: "user@host", port: 7388, auto: true},
		{name: "ssh option and value", args: []string{"-p", "2222", "user@host"}, host: "user@host", port: 7388, auto: true},
		{name: "lan after destination", args: []string{"host", "--lan"}, host: "host", port: 7388, auto: true, lan: []string{util.LANBare}},
		{name: "lan before destination", args: []string{"--lan", "host"}, host: "host", port: 7388, auto: true, lan: []string{util.LANBare}},
		{name: "lan selector", args: []string{"--lan", "192", "host"}, host: "host", port: 7388, auto: true, lan: []string{"192"}},
		{name: "lan inline and ts", args: []string{"host", "--lan=192", "--ts"}, host: "host", port: 7388, auto: true, ts: true, lan: []string{"192"}},
		{name: "port and allow", args: []string{"-p", "2222", "u@h", "--port", "7400", "--allow", "10.0.0.0/8,192.168.1.9"},
			host: "u@h", port: 7400, allow: []string{"10.0.0.0/8", "192.168.1.9"}},
		{name: "destination after --", args: []string{"--", "host"}, host: "host", port: 7388, auto: true},
		{name: "ssh option after destination", args: []string{"u@h", "-p", "2222"}, err: true},
		{name: "remote command", args: []string{"u@h", "echo", "hi"}, err: true},
		{name: "force", args: []string{"-f", "host"}, err: true},
		{name: "no destination", args: nil, err: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseSSHArgs(c.args)
			if c.err {
				if err == nil {
					t.Fatalf("want an error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.host != c.host || got.port != c.port || got.autoPort != c.auto || got.ts != c.ts {
				t.Errorf("got host %q port %d auto %v ts %v, want %q %d %v %v",
					got.host, got.port, got.autoPort, got.ts, c.host, c.port, c.auto, c.ts)
			}
			if !slices.Equal(got.lan, c.lan) || !slices.Equal(got.allow, c.allow) {
				t.Errorf("got lan %q allow %q, want %q %q", got.lan, got.allow, c.lan, c.allow)
			}
		})
	}
}

// runRemoteHerdrScript runs the remote status script the way a non-interactive
// SSH login would: HOME set, PATH reduced to the system directories. Only
// $HOME/.local/bin is searched, so a Herdr installed on this machine cannot leak in.
func runRemoteHerdrScript(t *testing.T, home string) (string, error) {
	t.Helper()
	cmd := exec.Command("sh", "-c", remoteHerdrScript([]string{"$HOME/.local/bin"}))
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	return string(out) + stderr.String(), err
}

func TestRemoteHerdrScriptFindsHerdrOffPath(t *testing.T) {
	home := t.TempDir()
	dir := filepath.Join(home, ".local", "bin")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	fake := "#!/bin/sh\necho \"fake herdr $*\"\n"
	if err := os.WriteFile(filepath.Join(dir, "herdr"), []byte(fake), 0o755); err != nil {
		t.Fatal(err)
	}
	out, err := runRemoteHerdrScript(t, home)
	if err != nil {
		t.Fatalf("script failed: %v\n%s", err, out)
	}
	if want := "fake herdr status --json"; strings.TrimSpace(out) != want {
		t.Fatalf("output = %q, want %q", out, want)
	}
}

func TestRemoteHerdrScriptReportsMissingHerdr(t *testing.T) {
	out, err := runRemoteHerdrScript(t, t.TempDir())
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 127 {
		t.Fatalf("err = %v, want exit status 127", err)
	}
	if !strings.Contains(out, "herdr not found") {
		t.Fatalf("output = %q, want a 'herdr not found' message", out)
	}
}

func TestParseSSHHerdrFlags(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		sock  string
		start bool
		new   bool
		err   bool
	}{
		{name: "existing socket", args: []string{"host", "--herdr-socket", "/tmp/x.sock"}, sock: "/tmp/x.sock"},
		{name: "inline socket before destination", args: []string{"--herdr-socket=/tmp/x.sock", "host"}, sock: "/tmp/x.sock"},
		{name: "start", args: []string{"--start", "--herdr-socket", "/tmp/x.sock", "host"}, sock: "/tmp/x.sock", start: true},
		{name: "new", args: []string{"host", "--new"}, new: true},
		{name: "start needs a socket", args: []string{"--start", "host"}, err: true},
		{name: "new with socket", args: []string{"--new", "--herdr-socket", "/tmp/x.sock", "host"}, err: true},
		{name: "new with start", args: []string{"--new", "--start", "host"}, err: true},
		{name: "relative socket", args: []string{"--herdr-socket", "x.sock", "host"}, err: true},
		{name: "socket with colon", args: []string{"--herdr-socket", "/tmp/a:b.sock", "host"}, err: true},
		{name: "socket without value", args: []string{"host", "--herdr-socket"}, err: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			o, err := parseSSHArgs(c.args)
			if (err != nil) != c.err {
				t.Fatalf("err = %v, want error: %v", err, c.err)
			}
			if err == nil && (o.sock != c.sock || o.start != c.start || o.temp != c.new) {
				t.Fatalf("got socket=%q start=%v new=%v, want %q %v %v", o.sock, o.start, o.temp, c.sock, c.start, c.new)
			}
		})
	}
}

func TestParseSSHSharedFlags(t *testing.T) {
	o, err := parseSSHArgs([]string{"-p", "2222", "--add-host=x", "--notify=false", "host", "--port", "7400"})
	if err != nil {
		t.Fatal(err)
	}
	if o.addHost != "x" || o.notify || o.port != 7400 || o.autoPort || o.host != "host" || !slices.Equal(o.sshArgs, []string{"-p", "2222"}) {
		t.Fatalf("got addHost=%q notify=%v port=%d auto=%v host=%q ssh=%v", o.addHost, o.notify, o.port, o.autoPort, o.host, o.sshArgs)
	}
	if o, err = parseSSHArgs([]string{"host", "--add-host"}); err != nil || o.addHost != defaultHost {
		t.Fatalf("bare --add-host: addHost=%q err=%v, want %q", o.addHost, err, defaultHost)
	}
	for _, args := range [][]string{{"host", "--ip", "0.0.0.0"}, {"--listen=127.0.0.1:1", "host"}, {"host", "--port", "x"}} {
		if _, err := parseSSHArgs(args); err == nil {
			t.Errorf("parseSSHArgs(%v): no error", args)
		}
	}
}

func TestParseSSHAllowFor(t *testing.T) {
	o, err := parseSSHArgs([]string{"host", "--allow", "all", "--allow-for", "90m"})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(o.allow, []string{"all"}) || o.allowFor != 90*time.Minute {
		t.Fatalf("allow=%v allowFor=%v", o.allow, o.allowFor)
	}
}
