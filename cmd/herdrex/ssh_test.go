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

// ssh reads short options as getopt does: several in one argument, and an
// option that takes a value ends the argument or takes the next one.
func TestParseSSHShortOptions(t *testing.T) {
	for _, args := range [][]string{
		{"-vN", "host"}, {"-Nf", "host"}, {"-vf", "host"}, {"-fN", "host"}, {"-qvN", "host"},
	} {
		if got, err := parseSSHArgs(args); err == nil {
			t.Errorf("%q: accepted with ssh args %q, want -f and -N refused", args, got.sshArgs)
		}
	}
	for _, c := range []struct {
		args    []string
		sshArgs []string
	}{
		{[]string{"-vp", "2222", "host"}, []string{"-vp", "2222"}},
		{[]string{"-p2222", "host"}, []string{"-p2222"}},
		{[]string{"-vp2222", "host"}, []string{"-vp2222"}},
		{[]string{"-oNumberOfPasswordPrompts=1", "host"}, []string{"-oNumberOfPasswordPrompts=1"}},
		{[]string{"-o", "ForwardAgent=no", "-4v", "host"}, []string{"-o", "ForwardAgent=no", "-4v"}},
	} {
		got, err := parseSSHArgs(c.args)
		if err != nil {
			t.Errorf("%q: %v", c.args, err)
			continue
		}
		if got.host != "host" || !slices.Equal(got.sshArgs, c.sshArgs) {
			t.Errorf("%q: host %q ssh args %q, want host and %q", c.args, got.host, got.sshArgs, c.sshArgs)
		}
	}
}

// fakeSSH records the ControlPath it is given and the mode of the directory
// that holds it in $SSH_LOG, then fails as ssh does when it cannot connect.
const fakeSSH = `#!/bin/sh
for a; do
  case $a in
  ControlPath=*) p=${a#ControlPath=}; printf '%s %s\n' "$(ls -ldL "$(dirname "$p")" | cut -c1-10)" "$p" >> "$SSH_LOG" ;;
  esac
done
exit 255
`

func TestSSHControlPathIsPrivate(t *testing.T) {
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte(fakeSSH), 0o755); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(t.TempDir(), "ssh.log")
	t.Setenv("SSH_LOG", log)
	t.Setenv("PATH", bin+":/usr/bin:/bin")
	opts, err := parseSSHArgs([]string{"--port", "47388", "host"})
	if err != nil {
		t.Fatal(err)
	}
	opts.autoPort = true
	if err := runSSH(t.Context(), opts); err == nil {
		t.Fatal("runSSH succeeded against an ssh that fails")
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	line, _, _ := strings.Cut(string(data), "\n")
	mode, controlPath, _ := strings.Cut(line, " ")
	if mode != "drwx------" {
		t.Errorf("ControlPath %s is in a directory with mode %s, want drwx------", controlPath, mode)
	}
	// ssh expands %C to 40 hex digits and first binds the path with a
	// 17-byte suffix; sun_path holds 104 bytes on macOS, the NUL included.
	if n := len(strings.ReplaceAll(controlPath, "%C", strings.Repeat("0", 40))) + 17; n > 103 {
		t.Errorf("ControlPath %s is %d bytes when ssh binds it, want at most 103", controlPath, n)
	}
	if _, err := os.Stat(filepath.Dir(controlPath)); !os.IsNotExist(err) {
		t.Errorf("ControlPath directory %s is still there after runSSH: %v", filepath.Dir(controlPath), err)
	}
}
