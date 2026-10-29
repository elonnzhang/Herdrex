// Package e2e tests the bridge end to end through the herdr and rex command
// lines (AGENTS.md).
package e2e

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/elonnzhang/herdrex/internal/bridge"
	"github.com/elonnzhang/herdrex/internal/model"
	"github.com/elonnzhang/herdrex/pkg/herdr/cli"
	"github.com/elonnzhang/herdrex/pkg/rex"
	"github.com/elonnzhang/herdrex/pkg/rex/client"
)

// wait bounds every wait for the other side to catch up.
const wait = 5 * time.Second

// env is an isolated Herdr server, the bridge serving it, and the two
// command lines pointed at them.
type env struct {
	herdr  *cli.CLI
	rexBin string
	rexEnv []string
	addr   string
	ids    *model.IDs
	events chan rex.Event // every event of a connection attached to all sessions
}

// start brings up an env for t, or skips t when the binaries are not named.
func start(t *testing.T) *env {
	t.Helper()
	herdrBin, rexBin := os.Getenv("HERDR_TEST_BIN"), os.Getenv("REX_TEST_BIN")
	if herdrBin == "" || rexBin == "" {
		t.Skip("set HERDR_TEST_BIN and REX_TEST_BIN to the herdr and rex binaries")
	}
	// Not t.TempDir: Unix socket paths must stay under 104 bytes on macOS.
	dir, err := os.MkdirTemp("/tmp", "hxe2e")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	config := filepath.Join(dir, "config")
	if err := os.MkdirAll(filepath.Join(config, "herdr"), 0o755); err != nil {
		t.Fatal(err)
	}
	toml := "[update]\nversion_check = false\nmanifest_check = false\n"
	if err := os.WriteFile(filepath.Join(config, "herdr", "config.toml"), []byte(toml), 0o644); err != nil {
		t.Fatal(err)
	}
	socket := filepath.Join(config, "herdr", "herdr.sock")
	e := &env{
		herdr: &cli.CLI{
			Bin: herdrBin,
			Env: []string{
				"XDG_CONFIG_HOME=" + config,
				"XDG_STATE_HOME=" + filepath.Join(dir, "state"),
				"HERDR_SOCKET_PATH=" + socket,
			},
		},
		rexBin: rexBin,
		rexEnv: rexEnviron(filepath.Join(dir, "home")),
		ids:    model.NewIDs(socket),
		events: make(chan rex.Event, 1024),
	}
	srv, err := e.herdr.StartServer(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := srv.Stop(context.Background()); err != nil {
			t.Errorf("stop herdr server: %v", err)
		}
	})

	ctx, cancel := context.WithCancel(context.Background())
	b := bridge.New(socket)
	b.Notify = false
	if err := b.Refresh(ctx); err != nil {
		cancel()
		t.Fatal(err)
	}
	go b.Watch(ctx)
	go b.Poll(ctx, 5*time.Second)
	hs := httptest.NewServer(b.Handler())
	t.Cleanup(func() {
		cancel()
		hs.Close()
	})
	e.addr = strings.TrimPrefix(hs.URL, "http://")

	w, err := client.Dial(ctx, hs.URL, &client.Options{
		Kind: "cli",
		Name: "e2e-watch",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	if err := w.Call(ctx, "session.attach_all", map[string]any{"event_scope": "all"}, nil); err != nil {
		t.Fatal(err)
	}
	go func() {
		for ev := range w.Events() {
			select {
			case e.events <- ev.Event:
			default: // a test that does not read events must not block the connection
			}
		}
	}()
	return e
}

// rexEnviron is this process's environment with HOME at home and without
// the REX_ variables a Rex terminal sets, which would point rex at the
// user's server, session or block.
func rexEnviron(home string) []string {
	var out []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "REX_") || strings.HasPrefix(kv, "HOME=") {
			continue
		}
		out = append(out, kv)
	}
	return append(out, "HOME="+home)
}

// h runs herdr and decodes the result of its reply into out, which may be nil.
func (e *env) h(t *testing.T, out any, args ...string) {
	t.Helper()
	var reply struct {
		Result json.RawMessage `json:"result"`
	}
	if err := e.herdr.RunJSON(t.Context(), &reply, args...); err != nil {
		t.Fatalf("herdr %s: %v", strings.Join(args, " "), err)
	}
	if out == nil {
		return
	}
	if err := json.Unmarshal(reply.Result, out); err != nil {
		t.Fatalf("herdr %s: decode %s: %v", strings.Join(args, " "), reply.Result, err)
	}
}

// rex runs rex against the bridge and returns its output, decoded into out
// as JSON when out is not nil.
func (e *env) rex(t *testing.T, out any, args ...string) string {
	t.Helper()
	argv := append([]string{"--autostart=false", "--color=never", "-S", e.addr}, args...)
	cmd := exec.CommandContext(t.Context(), e.rexBin, argv...)
	cmd.Env = e.rexEnv
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	b, err := cmd.Output()
	if err != nil {
		t.Fatalf("rex %s: %v\n%s", strings.Join(args, " "), err, stderr.String())
	}
	if out != nil {
		if err := json.Unmarshal(b, out); err != nil {
			t.Fatalf("rex %s: decode %s: %v", strings.Join(args, " "), b, err)
		}
	}
	return string(b)
}

// drain drops the events received so far.
func (e *env) drain() {
	for {
		select {
		case <-e.events:
		default:
			return
		}
	}
}

// pushed waits until check passes, checking after each event the bridge
// sends. Call drain before the change, so that a change Herdr made reaches
// Rex by event and not only by the fresh snapshot each read takes.
func (e *env) pushed(t *testing.T, check func() error) {
	t.Helper()
	deadline := time.After(wait)
	err := fmt.Errorf("no event")
	for {
		select {
		case <-e.events:
			if err = check(); err == nil {
				return
			}
		case <-deadline:
			t.Fatalf("no matching state within %v of the last event: %v", wait, err)
		}
	}
}

// eventually polls check until it passes.
func eventually(t *testing.T, check func() error) {
	t.Helper()
	deadline := time.Now().Add(wait)
	for {
		err := check()
		if err == nil {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("after %v: %v", wait, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// must fails t with err, if any.
func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}
