package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// fakeHerdr stands in for herdr in the tests.
const fakeHerdr = `#!/bin/sh
# Fake herdr for the cli tests. It logs each run to $FAKE_DIR and answers from
# files there: for the arguments "a b --c", a_b_--c.out is printed to stdout,
# a_b_--c.err to stderr, and a_b_--c.code is the exit status. Like Herdr, it
# takes a leading --session out and resolves the API socket from it, then
# HERDR_SOCKET_PATH, then XDG_CONFIG_HOME or HOME; "server" and
# "status server --json" act on that socket.
PATH=/usr/bin:/bin
d=$FAKE_DIR
echo "$0" >>"$d/who"
for a in "$@"; do printf '[%s]' "$a"; done >>"$d/args"
echo >>"$d/args"
echo "${FAKE_PROBE-unset}" >>"$d/env"
session=
if [ "$1" = --session ]; then session=$2; shift 2; fi
cfg=${XDG_CONFIG_HOME:-$HOME/.config}/herdr
if [ -n "$session" ] && [ "$session" != default ]; then
	sock=$cfg/sessions/$session/herdr.sock
elif [ -z "$session" ] && [ -n "$HERDR_SOCKET_PATH" ]; then
	sock=$HERDR_SOCKET_PATH
else
	sock=$cfg/herdr.sock
fi
echo "$*|$sock" >>"$d/sock"
pidfile=$d/server$(printf '%s' "$sock" | tr / _).pid
key=$(printf '%s' "$*" | tr ' ' _)
case $key in
sleep)
	echo $$ >"$d/sleep.pid"
	exec sleep 60
	;;
crash)
	kill -KILL $$
	;;
hold)
	# Exits 0 while a background child keeps stdout open for 2s.
	echo held
	sleep 2 &
	;;
server)
	if [ -f "$d/server.code" ]; then
		cat "$d/server.err" >&2
		exit "$(cat "$d/server.code")"
	fi
	echo $$ >"$d/server.pid"
	echo $$ >"$pidfile"
	if [ -f "$d/stubborn" ]; then trap '' TERM; else trap 'rm -f "$pidfile"; exit 0' TERM; fi
	while :; do sleep 0.1; done
	;;
server_stop)
	pid=$(cat "$pidfile" 2>/dev/null)
	if [ -z "$pid" ] || ! kill -0 "$pid" 2>/dev/null; then
		echo "server is not running or cannot be reached at $sock" >&2
		exit 1
	fi
	kill "$pid"
	;;
status_server_--json)
	pid=$(cat "$pidfile" 2>/dev/null)
	if [ -f "$d/foreign" ] || { [ ! -f "$d/never" ] && [ -n "$pid" ] && kill -0 "$pid" 2>/dev/null; }; then
		state='"status":"running","running":true'
	else
		state='"status":"not_running","running":false'
	fi
	echo "{$state,\"socket\":\"$sock\"}"
	;;
*)
	if [ -f "$d/$key.out" ]; then cat "$d/$key.out"; fi
	if [ -f "$d/$key.err" ]; then cat "$d/$key.err" >&2; fi
	if [ -f "$d/$key.code" ]; then exit "$(cat "$d/$key.code")"; fi
	;;
esac
`

// fake is a fake herdr executable and the directory it logs to and answers
// from.
type fake struct {
	t   *testing.T
	bin string
	dir string
}

func newFake(t *testing.T) *fake {
	return &fake{t: t, bin: writeFakeBin(t), dir: tempDir(t)}
}

func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "hxcfake")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

// writeFakeBin writes the fake as "herdr" in a directory of its own.
func writeFakeBin(t *testing.T) string {
	t.Helper()
	bin := filepath.Join(tempDir(t), "herdr")
	if err := os.WriteFile(bin, []byte(fakeHerdr), 0o755); err != nil {
		t.Fatal(err)
	}
	return bin
}

func (f *fake) cli() *CLI {
	return &CLI{Bin: f.bin, Env: []string{"FAKE_DIR=" + f.dir}}
}

// answer makes the fake print stdout and stderr and exit with code when run
// with args.
func (f *fake) answer(args, stdout, stderr string, code int) {
	key := filepath.Join(f.dir, strings.ReplaceAll(args, " ", "_"))
	f.write(key+".out", stdout)
	f.write(key+".err", stderr)
	f.write(key+".code", strconv.Itoa(code))
}

func (f *fake) write(path, data string) {
	f.t.Helper()
	if !filepath.IsAbs(path) {
		path = filepath.Join(f.dir, path)
	}
	if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// log returns the lines of one of the fake's logs: who, args, env (the
// FAKE_PROBE each run saw) or sock (each run's arguments and API socket).
func (f *fake) log(name string) []string {
	b, err := os.ReadFile(filepath.Join(f.dir, name))
	if err != nil {
		return nil
	}
	return strings.Split(strings.TrimSuffix(string(b), "\n"), "\n")
}

// pid returns the pid the fake wrote to name, waiting up to 10s for it.
func (f *fake) pid(name string) int {
	f.t.Helper()
	var pid int
	waitFor(f.t, name+" written", func() bool {
		b, err := os.ReadFile(filepath.Join(f.dir, name))
		if err != nil {
			return false
		}
		pid, err = strconv.Atoi(strings.TrimSpace(string(b)))
		return err == nil
	})
	return pid
}

// waitFor polls cond until it holds, failing the test after 10s.
func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(10 * time.Second); !cond(); {
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// waitGone waits until no process has pid. The tests only check processes
// that they started.
func waitGone(t *testing.T, pid int) {
	t.Helper()
	waitFor(t, "process "+strconv.Itoa(pid)+" to exit", func() bool {
		return syscall.Kill(pid, 0) == syscall.ESRCH
	})
}

// unsetenv unsets key for the test and restores it afterwards.
func unsetenv(t *testing.T, key string) {
	t.Setenv(key, "")
	os.Unsetenv(key)
}

// setTiming sets a timing variable for the test.
func setTiming(t *testing.T, v *time.Duration, d time.Duration) {
	old := *v
	*v = d
	t.Cleanup(func() { *v = old })
}

func TestBinLookup(t *testing.T) {
	f := newFake(t)
	fromBin, fromEnv, fromPath := writeFakeBin(t), writeFakeBin(t), writeFakeBin(t)
	t.Setenv("HERDR_BIN_PATH", fromEnv)
	t.Setenv("PATH", filepath.Dir(fromPath))
	c := f.cli()
	run := func(want string) {
		t.Helper()
		if _, err := c.Run(t.Context(), "--version"); err != nil {
			t.Fatal(err)
		}
		who := f.log("who")
		if got := who[len(who)-1]; got != want {
			t.Errorf("ran %s, want %s", got, want)
		}
	}
	c.Bin = fromBin
	run(fromBin)
	c.Bin = ""
	run(fromEnv)
	t.Setenv("HERDR_BIN_PATH", "")
	run(fromPath)
}

func TestMissingBin(t *testing.T) {
	c := &CLI{Bin: filepath.Join(tempDir(t), "herdr")}
	_, err := c.Run(t.Context(), "--version")
	if _, ok := err.(*Error); err == nil || ok {
		t.Fatalf("err = %v, want a start error", err)
	}
}

func TestSessionGoesFirst(t *testing.T) {
	f := newFake(t)
	c := f.cli()
	c.Session = "work"
	if _, err := c.Run(t.Context(), "pane", "run", "--", "echo", "--session"); err != nil {
		t.Fatal(err)
	}
	c.Session = ""
	if _, err := c.Run(t.Context(), "status", "--json"); err != nil {
		t.Fatal(err)
	}
	want := []string{"[--session][work][pane][run][--][echo][--session]", "[status][--json]"}
	if got := f.log("args"); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Errorf("args = %q, want %q", got, want)
	}
}

func TestEnv(t *testing.T) {
	f := newFake(t)
	t.Setenv("FAKE_PROBE", "inherited")
	c := f.cli()
	runs := [][]string{nil, {"FAKE_PROBE=env"}, {"FAKE_PROBE=first", "FAKE_PROBE=last"}}
	for _, env := range runs {
		c.Env = append([]string{"FAKE_DIR=" + f.dir}, env...)
		if _, err := c.Run(t.Context(), "--version"); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{"inherited", "env", "last"}
	if got := f.log("env"); strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("child saw FAKE_PROBE = %q, want %q", got, want)
	}
}

func TestRun(t *testing.T) {
	f := newFake(t)
	f.answer("config check", "line 1\n  line 2\n", "a warning\n", 0)
	out, err := f.cli().Run(t.Context(), "config", "check")
	if err != nil {
		t.Fatal(err)
	}
	if string(out) != "line 1\n  line 2\n" {
		t.Errorf("stdout = %q", out)
	}
}

func TestRunJSON(t *testing.T) {
	f := newFake(t)
	f.answer("pane get p1", `{"id":"cli:pane:get","result":{"pane":{"pane_id":"p1"}}}`+"\n", "", 0)
	f.answer("pane get p2", "not json\n", "", 0)
	var v struct {
		Result struct {
			Pane struct {
				ID string `json:"pane_id"`
			} `json:"pane"`
		} `json:"result"`
	}
	c := f.cli()
	if err := c.RunJSON(t.Context(), &v, "pane", "get", "p1"); err != nil {
		t.Fatal(err)
	}
	if v.Result.Pane.ID != "p1" {
		t.Errorf("decoded %+v", v)
	}
	err := c.RunJSON(t.Context(), &v, "pane", "get", "p2")
	if _, ok := err.(*Error); err == nil || ok || !strings.Contains(err.Error(), "decode output") {
		t.Errorf("bad JSON: err = %v", err)
	}
}

func TestErrors(t *testing.T) {
	f := newFake(t)
	sessionErr := `{"error":{"code":"session_stop_failed","message":"session x is not running"}}` + "\n"
	apiErr := `{"id":"cli:pane:list","error":{"code":"server_not_running","message":"no herdr server is running"}}` + "\n"
	big := strings.Repeat("x", 6000) + "\nlast line\n"
	f.answer("e session", "", sessionErr, 1)
	f.answer("e api", "partial\n", apiErr, 1)
	f.answer("e mixed", "", "warning: one\n"+sessionErr+"note: two\n", 1)
	f.answer("e usage", "", "usage: herdr session list [--json]\n", 2)
	f.answer("e big", "", big, 1)
	f.answer("e quiet", "", "", 3)
	tests := []struct {
		args          string
		code, message string
		exit          int
		text          string // Error()
		out           string
	}{
		{
			"e session", "session_stop_failed", "session x is not running", 1,
			"herdr: session_stop_failed: session x is not running", "",
		},
		{
			"e api", "server_not_running", "no herdr server is running", 1,
			"herdr: server_not_running: no herdr server is running", "partial\n",
		},
		{
			"e mixed", "session_stop_failed", "session x is not running", 1,
			"herdr: session_stop_failed: session x is not running", "",
		},
		{"e usage", "", "", 2, "herdr: exit status 2: usage: herdr session list [--json]", ""},
		{"e quiet", "", "", 3, "herdr: exit status 3", ""},
		{"crash", "", "", -1, "herdr: killed by a signal", ""},
	}
	c := f.cli()
	for _, tt := range tests {
		out, err := c.Run(t.Context(), strings.Fields(tt.args)...)
		e, ok := err.(*Error)
		if !ok {
			t.Errorf("%s: err = %v, want *Error", tt.args, err)
			continue
		}
		if e.Code != tt.code || e.Message != tt.message || e.ExitCode != tt.exit {
			t.Errorf("%s: got %+v", tt.args, e)
		}
		if e.Error() != tt.text {
			t.Errorf("%s: Error() = %q, want %q", tt.args, e.Error(), tt.text)
		}
		if string(out) != tt.out {
			t.Errorf("%s: stdout = %q, want %q", tt.args, out, tt.out)
		}
	}

	_, err := c.Run(t.Context(), "e", "usage")
	if e, _ := err.(*Error); e == nil || e.Stderr != "usage: herdr session list [--json]\n" {
		t.Errorf("non-JSON stderr: err = %#v", err)
	}
	_, err = c.Run(t.Context(), "e", "big")
	if e, _ := err.(*Error); e == nil || len(e.Stderr) != maxStderr ||
		!strings.HasSuffix(e.Stderr, "x\nlast line\n") || e.Code != "" {
		t.Errorf("long stderr: err = %v; want the last %d bytes", err, maxStderr)
	}
}

func TestVersion(t *testing.T) {
	f := newFake(t)
	c := f.cli()
	f.answer("--version", "herdr 0.9.5\n", "", 0)
	if v, err := c.Version(t.Context()); err != nil || v != "0.9.5" {
		t.Errorf("Version = %q, %v", v, err)
	}
	f.answer("--version", "0.9.5\n", "", 0)
	if v, err := c.Version(t.Context()); err == nil {
		t.Errorf("unexpected output: Version = %q, want an error", v)
	}
	f.answer("--version", "", "boom\n", 1)
	if _, err := c.Version(t.Context()); err == nil {
		t.Error("exit 1: want an error")
	}
}

func TestSchema(t *testing.T) {
	f := newFake(t)
	c := f.cli()
	f.answer("api schema --json", "{\n  \"protocol\": 22,\n  \"schemas\": {}\n}", "", 0)
	schema, err := c.Schema(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(schema), `"protocol": 22`) {
		t.Errorf("schema = %s", schema)
	}
	f.answer("api schema --json", "Herdr API schema\nprotocol: 22\n", "", 0)
	if _, err := c.Schema(t.Context()); err == nil {
		t.Error("summary text: want an error")
	}
}

func TestRunCanceled(t *testing.T) {
	f := newFake(t)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	errc := make(chan error, 1)
	c := f.cli()
	go func() {
		_, err := c.Run(ctx, "sleep")
		errc <- err
	}()
	pid := f.pid("sleep.pid")
	cancel()
	select {
	case err := <-errc:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want Canceled", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("Run did not return after ctx ended")
	}
	waitGone(t, pid)
}

func TestRunHeldOutput(t *testing.T) {
	setTiming(t, &waitDelay, 200*time.Millisecond)
	f := newFake(t)
	out, err := f.cli().Run(t.Context(), "hold")
	if err != nil || string(out) != "held\n" {
		t.Errorf("Run = %q, %v; want the output of the exit 0", out, err)
	}
}

func TestRunRefuses(t *testing.T) {
	f := newFake(t)
	c := f.cli()
	for _, args := range []string{
		"",
		"--session work",
		"--session=work",
		"--remote box",
		"--remote=box",
		"--remote-keybindings server --remote box",
		"--remote box --handoff",
		"--remote box client --help",
		"client --version",
		"server",
		"--session work server",
		"server --handoff-import /tmp/h.sock token",
		"server live-handoff",
		"client",
		"remote-client-bridge",
		"--session work remote-client-bridge --idle-timeout-v1",
		"session attach work",
		"terminal attach t1",
		"terminal attach t1 --takeover",
		"agent attach a1",
		"update --handoff",
		"--handoff update",
	} {
		_, err := c.Run(t.Context(), strings.Fields(args)...)
		if !errors.Is(err, ErrRefused) || !strings.Contains(err.Error(), "use ") {
			t.Errorf("Run(%q): err = %v, want ErrRefused", args, err)
		}
		var v any
		if err := c.RunJSON(t.Context(), &v, strings.Fields(args)...); !errors.Is(err, ErrRefused) {
			t.Errorf("RunJSON(%q): err = %v, want ErrRefused", args, err)
		}
	}
	c.Session = "work"
	if _, err := c.Run(t.Context()); !errors.Is(err, ErrRefused) {
		t.Errorf("Run() with Session: err = %v, want ErrRefused", err)
	}
	if args := f.log("args"); args != nil {
		t.Fatalf("refused commands ran herdr: %q", args)
	}

	c.Session = ""
	allowed := []string{
		"server stop",
		"--session work server stop",
		"server reload-config",
		"server reload-agent-manifests",
		"server agent-manifests --json",
		"server --help",
		"--version",
		"--help",
		"--handoff",
		"-- server",
		"--remote box --version",
		"--remote box status",
		"--version server",
		"server foo",
		"server --handoff",
		"status server --json",
		"session list --json",
		"session stop --json -- work",
		"terminal session observe t1",
		"agent list",
		"api snapshot",
		"update",
		"--machine box pane list",
	}
	for _, args := range allowed {
		if _, err := c.Run(t.Context(), strings.Fields(args)...); errors.Is(err, ErrRefused) {
			t.Errorf("Run(%q): %v", args, err)
		}
	}
	if got := f.log("args"); len(got) != len(allowed) {
		t.Errorf("ran herdr %d times, want %d", len(got), len(allowed))
	}
}

// TestRunStdinNotTerminal checks that herdr gets no stdin, which is what
// keeps a plain herdr update from stopping a running server (see Run): its
// prompts decline when stdin is not a terminal. With the test's own stdin
// passed through, read would block on a terminal and see no EOF.
func TestRunStdinNotTerminal(t *testing.T) {
	bin := filepath.Join(tempDir(t), "herdr")
	script := "#!/bin/sh\nif [ -t 0 ]; then echo tty; exit 0; fi\nread -r line\necho \"read=$?\"\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	out, err := (&CLI{Bin: bin}).Run(ctx, "update")
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(out)); got != "read=1" {
		t.Fatalf("herdr update saw stdin as %q, want EOF at once (read=1)", got)
	}
}
