// Package cli runs the herdr command as a child process (Herdr 0.9.5,
// cli-reference.mdx).
//
// Most herdr subcommands (workspace, tab, pane, agent and the rest) are thin
// wrappers over the JSON API; pkg/herdr/jsonapi calls the API directly,
// without a process per call. This package types only the commands that have
// no socket equivalent: --version, status, session list|stop|delete, api
// schema and herdr server. Run executes the other subcommands, including a
// plain herdr update, which never stops a running server (see Run).
//
// Nothing here starts a server or an interactive client in the user's
// Herdr. Run refuses the commands that would (see Run). StartServer starts a
// server only when its environment points the config directory and sockets
// away from the user's, and Server.Stop signals only that child (see
// StartServer).
//
// HERDR_ENV=1, which Herdr sets in its panes, does not block these commands;
// it only blocks launching or attaching the TUI.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// maxStderr is how much of the end of herdr's stderr an Error keeps.
const maxStderr = 4 << 10

// waitDelay bounds how long a run waits for stdout and stderr to close after
// herdr exits or is killed, in case a process it started holds them open. A
// variable so tests can shorten it.
var waitDelay = time.Second

// CLI runs a herdr executable with extra environment. The zero value runs
// HERDR_BIN_PATH or herdr from PATH.
type CLI struct {
	Bin     string   // empty: HERDR_BIN_PATH, then "herdr" on PATH
	Session string   // non-empty: prepend --session <name>
	Env     []string // appended to the child environment; later entries win
}

// Error is a herdr run that exited with a non-zero status. Code and Message
// come from the JSON error line Herdr prints on stderr, {"error":{"code",
// "message"}} (session commands) or a whole JSON API error response (API
// commands); both are empty when stderr has no such line.
type Error struct {
	Code, Message string
	ExitCode      int    // 2 for a usage error; -1 when killed by a signal
	Stderr        string // the last 4 KiB of stderr
}

// Error returns the code and message, or the exit status and the first line
// of stderr.
func (e *Error) Error() string {
	if e.Code != "" {
		return fmt.Sprintf("herdr: %s: %s", e.Code, e.Message)
	}
	msg := fmt.Sprintf("herdr: exit status %d", e.ExitCode)
	if e.ExitCode < 0 {
		msg = "herdr: killed by a signal"
	}
	if line := firstLine(e.Stderr); line != "" {
		msg += ": " + line
	}
	return msg
}

func newError(exitCode int, stderr string) *Error {
	e := &Error{ExitCode: exitCode, Stderr: stderr}
	lines := strings.Split(strings.TrimSpace(stderr), "\n")
	for i := len(lines) - 1; i >= 0; i-- {
		var v struct {
			Error *struct {
				Code    string `json:"code"`
				Message string `json:"message"`
			} `json:"error"`
		}
		if json.Unmarshal([]byte(lines[i]), &v) == nil && v.Error != nil && v.Error.Code != "" {
			e.Code, e.Message = v.Error.Code, v.Error.Message
			break
		}
	}
	return e
}

func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		if line = strings.TrimSpace(line); line != "" {
			return line
		}
	}
	return ""
}

// Run runs herdr with args and returns its standard output. A non-zero exit
// returns the output with an *Error. When ctx ends first, herdr is killed and
// the error wraps ctx.Err().
//
// Run refuses, with an error wrapping ErrRefused and without running
// anything, the commands that would start a server or attach an interactive
// client: herdr with no command, server (not its subcommands such as stop),
// server live-handoff, client, remote-client-bridge, session attach,
// terminal attach, agent attach, update --handoff, and --remote without a
// command. Use StartServer for a server.
//
// Plain update (without --handoff) runs: it replaces the installed herdr and
// never stops a running server. Run gives herdr no stdin (/dev/null), and
// every update prompt that would stop a server declines when stdin is not a
// terminal (src/update.rs:1177, :1303, :1460). When a running server needs a
// restart for the new version (one older than endpoint generation 1), herdr
// prints "Herdr was not updated." and installs nothing (src/update.rs:2285);
// servers on endpoint generation 1 keep running (session-state.mdx).
func (c *CLI) Run(ctx context.Context, args ...string) (stdout []byte, err error) {
	if err := refuse(c.argv(args)); err != nil {
		return nil, err
	}
	return c.exec(ctx, c.environ(), args)
}

// RunJSON runs herdr with args, as Run does, and decodes its standard output
// into out.
func (c *CLI) RunJSON(ctx context.Context, out any, args ...string) error {
	b, err := c.Run(ctx, args...)
	if err != nil {
		return err
	}
	return decode(b, out, c.argv(args))
}

func decode(b []byte, out any, argv []string) error {
	if err := json.Unmarshal(b, out); err != nil {
		return fmt.Errorf("herdr %s: decode output: %w", strings.Join(argv, " "), err)
	}
	return nil
}

// exec runs herdr with args and the whole environment env.
func (c *CLI) exec(ctx context.Context, env, args []string) ([]byte, error) {
	cmd := c.cmd(ctx, env, args)
	var out bytes.Buffer
	stderr := &tail{max: maxStderr}
	cmd.Stdout = &out
	cmd.Stderr = stderr
	err := cmd.Run()
	if errors.Is(err, exec.ErrWaitDelay) && ctx.Err() == nil {
		// herdr exited 0, but something it started kept stdout or stderr
		// open past waitDelay.
		err = nil
	}
	if err != nil {
		if ctx.Err() != nil {
			return out.Bytes(), fmt.Errorf("herdr %s: %w", strings.Join(cmd.Args[1:], " "), ctx.Err())
		}
		var exit *exec.ExitError
		if errors.As(err, &exit) {
			return out.Bytes(), newError(exit.ExitCode(), stderr.String())
		}
		return nil, err
	}
	return out.Bytes(), nil
}

// Version runs herdr --version and returns the version, such as "0.9.5".
func (c *CLI) Version(ctx context.Context) (string, error) {
	out, err := c.Run(ctx, "--version")
	if err != nil {
		return "", err
	}
	v, ok := strings.CutPrefix(strings.TrimSpace(string(out)), "herdr ")
	if !ok || v == "" {
		return "", fmt.Errorf("herdr --version: unexpected output %q", out)
	}
	return v, nil
}

// Schema runs herdr api schema --json: the JSON Schema of the socket API
// bundled with the binary. It needs no server.
func (c *CLI) Schema(ctx context.Context) (json.RawMessage, error) {
	var schema json.RawMessage
	if err := c.RunJSON(ctx, &schema, "api", "schema", "--json"); err != nil {
		return nil, err
	}
	return schema, nil
}

// argv is the command line after the program name. Herdr reads --session
// anywhere before "--" (src/session.rs:57), so it goes first, ahead of any
// "--" in args.
func (c *CLI) argv(args []string) []string {
	if c.Session != "" {
		return append([]string{"--session", c.Session}, args...)
	}
	return args
}

// environ is the child's environment: this process's, then Env.
func (c *CLI) environ() []string {
	return append(os.Environ(), c.Env...)
}

// cmd builds a herdr run with the whole environment env.
func (c *CLI) cmd(ctx context.Context, env, args []string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, c.bin(), c.argv(args)...)
	cmd.Env = env
	cmd.WaitDelay = waitDelay
	return cmd
}

func (c *CLI) bin() string {
	if c.Bin != "" {
		return c.Bin
	}
	if p := os.Getenv("HERDR_BIN_PATH"); p != "" {
		return p
	}
	return "herdr"
}

// lookupEnv returns the value env gives key; the last entry wins, as in
// os/exec.
func lookupEnv(env []string, key string) (string, bool) {
	for i := len(env) - 1; i >= 0; i-- {
		if k, v, ok := strings.Cut(env[i], "="); ok && k == key {
			return v, true
		}
	}
	return "", false
}

// tail keeps the last max bytes written to it.
type tail struct {
	max int
	b   []byte
}

func (t *tail) Write(p []byte) (int, error) {
	t.b = append(t.b, p...)
	if over := len(t.b) - t.max; over > 0 {
		t.b = append(t.b[:0], t.b[over:]...)
	}
	return len(p), nil
}

func (t *tail) String() string { return string(t.b) }
