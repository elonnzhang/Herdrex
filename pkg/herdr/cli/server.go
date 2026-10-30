package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Timing of StartServer and Server.Stop; variables so tests can shorten them.
var (
	startTimeout = 10 * time.Second
	pollInterval = 100 * time.Millisecond
	termWait     = 10 * time.Second // after SIGTERM, before SIGKILL
)

// overrides are the variables that can point a server at another socket or
// config file than its config directory's.
var overrides = []string{"HERDR_SOCKET_PATH", "HERDR_CLIENT_SOCKET_PATH", "HERDR_CONFIG_PATH"}

// Server is a herdr server that StartServer started as a child process. It
// runs until Stop, regardless of the context passed to StartServer, and in
// a process group of its own, so a terminal's Ctrl+C does not reach it.
type Server struct {
	cli    CLI      // the executable and session StartServer used
	env    []string // the child's whole environment, fixed at StartServer
	cmd    *exec.Cmd
	stderr *tail
	done   chan struct{} // closed once the process has exited and been reaped
	err    error         // from cmd.Wait; read after done is closed
}

// StartServer runs herdr server in the foreground as a child process, the
// way cli-reference.mdx describes for supervised setups, and returns once
// herdr status server reports it running. It polls every 100ms for up to 10s.
//
// The child's environment is this process's plus c.Env, the last value of a
// variable winning. StartServer starts nothing unless that environment keeps
// the server away from the user's Herdr:
//   - XDG_CONFIG_HOME is set, and the config directory under it neither is,
//     contains nor lies in the user's: $XDG_CONFIG_HOME/herdr or
//     $HOME/.config/herdr as this process resolves them. Symbolic links are
//     resolved before comparing.
//   - HERDR_SOCKET_PATH, HERDR_CLIENT_SOCKET_PATH and HERDR_CONFIG_PATH do
//     not lie in the user's config directory, and each one this process has
//     (Herdr sets HERDR_SOCKET_PATH in its panes) is set to another path.
//   - herdr status server does not report a server running at the target.
//
// Set XDG_STATE_HOME as well to keep the server's plugin state and agent
// manifest cache out of the user's state directory. If the server does not
// come up, StartServer stops the child and reaps it before returning.
func (c *CLI) StartServer(ctx context.Context) (*Server, error) {
	env := c.environ()
	if err := checkIsolated(env); err != nil {
		return nil, fmt.Errorf("herdr server: not started: %w", err)
	}
	s := &Server{
		cli:    CLI{Bin: c.bin(), Session: c.Session},
		env:    env,
		stderr: &tail{max: maxStderr},
		done:   make(chan struct{}),
	}
	st, err := s.status(ctx)
	if err != nil {
		return nil, err
	}
	if st.Running {
		return nil, fmt.Errorf("herdr server: not started: a server is already running at %s", st.Socket)
	}

	// Not ctx: the server outlives the call.
	s.cmd = s.cli.cmd(context.Background(), env, []string{"server"})
	s.cmd.Stderr = s.stderr
	// Its own process group, so that Ctrl+C in the terminal reaches only
	// the caller, which then stops the server with Stop.
	s.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := s.cmd.Start(); err != nil {
		return nil, err
	}
	go func() {
		s.err = s.cmd.Wait()
		close(s.done)
	}()
	if err := s.waitRunning(ctx); err != nil {
		s.end(context.Background())
		return nil, err
	}
	return s, nil
}

// checkIsolated returns why a server with the environment env could use the
// user's Herdr files, or nil.
func checkIsolated(env []string) error {
	cfg, _ := lookupEnv(env, "XDG_CONFIG_HOME")
	if cfg == "" {
		return errors.New("Env must set XDG_CONFIG_HOME, or the server would use the user's config directory")
	}
	user := userConfigDirs()
	dir := filepath.Join(cfg, "herdr")
	for _, u := range user {
		if within(dir, u) || within(u, dir) {
			return fmt.Errorf("XDG_CONFIG_HOME=%s gives the server the config directory %s, which overlaps the user's config directory %s", cfg, dir, u)
		}
	}
	for _, key := range overrides {
		v, _ := lookupEnv(env, key)
		if own := os.Getenv(key); own != "" && (v == "" || resolve(v) == resolve(own)) {
			return fmt.Errorf("this process has %s=%s, so Env must set %s to another path", key, own, key)
		}
		if v == "" {
			continue
		}
		for _, u := range user {
			if within(v, u) {
				return fmt.Errorf("%s=%s is in the user's config directory %s", key, v, u)
			}
		}
	}
	return nil
}

// userConfigDirs returns the config directories Herdr would use for this
// process (src/config/io.rs:30): $XDG_CONFIG_HOME/herdr, and the fallback
// $HOME/.config/herdr, or herdr in the temporary directory without HOME.
func userConfigDirs() []string {
	var dirs []string
	if v := os.Getenv("XDG_CONFIG_HOME"); v != "" {
		dirs = append(dirs, filepath.Join(v, "herdr"))
	}
	if home := os.Getenv("HOME"); home != "" {
		dirs = append(dirs, filepath.Join(home, ".config", "herdr"))
	} else {
		dirs = append(dirs, filepath.Join(os.TempDir(), "herdr"))
	}
	return dirs
}

// within reports whether path is dir or lies under it, after making both
// absolute and resolving symbolic links in the parts that exist.
func within(path, dir string) bool {
	path, dir = resolve(path), resolve(dir)
	return path == dir || strings.HasPrefix(path, strings.TrimSuffix(dir, "/")+"/")
}

// resolve makes p absolute and resolves symbolic links in its longest
// existing prefix, keeping the rest as is.
func resolve(p string) string {
	p, err := filepath.Abs(p)
	if err != nil {
		return filepath.Clean(p)
	}
	rest := ""
	for {
		if r, err := filepath.EvalSymlinks(p); err == nil {
			return filepath.Join(r, rest)
		}
		parent := filepath.Dir(p)
		if parent == p {
			return filepath.Join(p, rest)
		}
		rest = filepath.Join(filepath.Base(p), rest)
		p = parent
	}
}

func (s *Server) status(ctx context.Context) (*ServerStatus, error) {
	var st ServerStatus
	args := []string{"status", "server", "--json"}
	b, err := s.cli.exec(ctx, s.env, args)
	if err != nil {
		return nil, err
	}
	if err := decode(b, &st, s.cli.argv(args)); err != nil {
		return nil, err
	}
	return &st, nil
}

func (s *Server) waitRunning(ctx context.Context) error {
	ctx, cancel := context.WithTimeoutCause(ctx, startTimeout,
		fmt.Errorf("not running after %v: %w", startTimeout, context.DeadlineExceeded))
	defer cancel()
	tick := time.NewTicker(pollInterval)
	defer tick.Stop()
	var last error
	for {
		select {
		case <-s.done:
			if err := s.exitErr(); err != nil {
				return err
			}
			return errors.New("herdr server: exited before it was running")
		case <-ctx.Done():
			if last != nil {
				return fmt.Errorf("herdr server: %w (last status error: %v)", context.Cause(ctx), last)
			}
			return fmt.Errorf("herdr server: %w", context.Cause(ctx))
		case <-tick.C:
		}
		st, err := s.status(ctx)
		switch {
		case err == nil && st.Running:
			return nil
		case err != nil && ctx.Err() == nil:
			last = err
		}
	}
}

// Stop sends the server SIGTERM, which Herdr handles like herdr server stop
// (src/platform/unix_common.rs:367, src/server/headless.rs:417), and waits
// for it to exit. It sends SIGKILL if the server is still running 10s later,
// or as soon as ctx ends. It signals only its own child, never whatever
// server the current environment names, and always reaps the child before
// returning. The error reports a kill, ctx ending, or a non-zero exit; Stop
// after the server has exited returns how it exited.
func (s *Server) Stop(ctx context.Context) error {
	return s.end(ctx)
}

func (s *Server) end(ctx context.Context) error {
	select {
	case <-s.done:
		return s.exitErr()
	default:
	}
	_ = s.cmd.Process.Signal(syscall.SIGTERM)
	timer := time.NewTimer(termWait)
	defer timer.Stop()
	var err error
	select {
	case <-s.done:
		return s.exitErr()
	case <-timer.C:
		err = fmt.Errorf("herdr server: still running %v after SIGTERM; killed", termWait)
	case <-ctx.Done():
		err = fmt.Errorf("herdr server: killed: %w", ctx.Err())
	}
	select {
	case <-s.done: // exited meanwhile
		return s.exitErr()
	default:
	}
	_ = s.cmd.Process.Kill()
	<-s.done
	return err
}

// exitErr reports how the exited process ended: nil for status 0.
func (s *Server) exitErr() error {
	var exit *exec.ExitError
	if errors.As(s.err, &exit) {
		return newError(exit.ExitCode(), s.stderr.String())
	}
	return s.err
}
