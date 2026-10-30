// Command herdrex exposes a Herdr server as a Rex host.
package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/signal"
	"runtime/debug"
	"slices"
	"syscall"
	"time"

	"github.com/elonnzhang/herdrex/internal/bridge"
	"github.com/elonnzhang/herdrex/pkg/herdr"

	"github.com/spf13/cobra"
)

// version is set with -ldflags "-X main.version=..."; otherwise it comes
// from the module version that go install records.
var version = ""

const long = `> 

herdrex serves a Herdr server as a Rex host, so that Rex.app and the rex command line can use its workspaces as sessions, its tabs as windows and its panes as blocks.

The bridge has no authentication. On a loopback address only this machine can reach it; on a Tailscale address every device that your tailnet lets reach this one can read and type into every pane.

--add-host adds the host for the rex command line. 
Rex.app does not read those hosts: add it in Rex.app under Settings > Remote Host, with the name and address herdrex prints when it starts.`

const example = `
  # serve the default Herdr on 127.0.0.1:7388 and add it to your Rex hosts
  # as "herdr"; then: rex -S herdr ls
  herdrex --add-host

  # serve a Herdr that is already running at another socket
  herdrex --herdr-socket /tmp/herdr/herdr.sock --listen 127.0.0.1:7388

  # start a new Herdr server at that socket, serve it, stop it on Ctrl+C
  herdrex --herdr-socket /tmp/herdr/x.sock --start --add-host=x

  # the same in the background, still running after the terminal closes
  herdrex --add-host --daemon

  # try it without touching your Herdr; Ctrl+C undoes everything
  herdrex test

  # serve it to your other devices over Tailscale, as host "studio"
  herdrex --listen $(tailscale ip -4):7388 --add-host=studio`

// options are the command's flags.
type options struct {
	sock    string
	listen  string
	notify  bool
	addHost string // "" leaves the Rex hosts alone
	start   bool   // start a Herdr server at sock
	cleanup bool   // on exit, undo the host change and remove the started server's files
	daemon  bool   // run in the background
	logFile string // where the background process logs; "" for daemonLog
}

// Defaults of herdrex test.
const (
	testSocket = "/tmp/herdr/herdr-for-rex.sock"
	testHost   = "herdr-in-Rex"
)

const testLong = `test starts a Herdr server of its own, serves it as a Rex host and adds that host to your Rex hosts, so that you can try Herdr in Rex.app or with rex -S ` + testHost + ` without touching your Herdr. It runs in the foreground.

Rex.app does not read these hosts: add the host in Rex.app under 
Settings > Remote Host, as herdrex prints when it starts.

To watch the same Herdr in its own TUI while test runs, in another terminal:

  HERDR_SOCKET_PATH=` + testSocket + ` herdr

Run it only while test runs: without a server there, herdr starts one that uses your own Herdr config and that test does not stop. Ctrl+C in test stops the server, which ends that TUI too.

Ctrl+C removes the Rex host (or gives back the endpoints of one it changed), stops the Herdr server and removes its socket, config and state. 
A host added in Rex.app stays; remove it there.

It is herdrex --start --add-host with these defaults and that cleanup.`

func main() {
	if err := command().Execute(); err != nil {
		os.Exit(1)
	}
}

func command() *cobra.Command {
	var o options
	cmd := &cobra.Command{
		Use:     "herdrex",
		Short:   "Serve a Herdr server as a Rex host",
		Long:    long,
		Example: example,
		Args: func(_ *cobra.Command, args []string) error {
			if len(args) > 0 {
				return fmt.Errorf("unexpected argument %q; give a host name as --add-host=NAME", args[0])
			}
			return nil
		},
		Version:      buildVersion(),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if o.start && !cmd.Flags().Changed("herdr-socket") {
				return fmt.Errorf("--start needs --herdr-socket: the path of the new server's socket")
			}
			if o.logFile != "" && !o.daemon {
				return fmt.Errorf("--log needs --daemon")
			}
			if o.daemon && os.Getenv(readyEnv) == "" {
				return background(o)
			}
			return run(cmd.Context(), o)
		},
	}
	flags(cmd, &o, herdr.SocketPath(""), "")
	f := cmd.Flags()
	f.BoolVar(&o.start, "start", false, "start a new Herdr server at --herdr-socket, with its config and state beside it\n(x.sock: x.d), and stop it on exit")
	f.BoolVar(&o.daemon, "daemon", false, "run in the background, in a session of its own, until killed; kill PID stops it\nas Ctrl+C would")
	f.StringVar(&o.logFile, "log", "", "log file of --daemon (default <user cache dir>/herdrex/herdrex-PORT.log)")
	cmd.AddCommand(testCommand())
	return cmd
}

// background starts herdrex in the background with o's flags and returns
// once it serves.
func background(o options) error {
	addr, err := netip.ParseAddrPort(o.listen)
	if err != nil {
		return fmt.Errorf("--listen: %w", err)
	}
	file := o.logFile
	if file == "" {
		if file, err = daemonLog(addr.Port()); err != nil {
			return err
		}
	}
	return daemonize(file)
}

func testCommand() *cobra.Command {
	o := options{
		start:   true,
		cleanup: true,
	}
	cmd := &cobra.Command{
		Use:          "test",
		Short:        "Try Herdr in Rex: start a Herdr, serve it, add it to Rex hosts; Ctrl+C undoes it all",
		Long:         testLong,
		Example:      "  herdrex test\n  rex -S " + testHost + " new   # in another terminal",
		Args:         cobra.NoArgs,
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if o.addHost == "" {
				return fmt.Errorf("--add-host must name a host")
			}
			return run(cmd.Context(), o)
		},
	}
	flags(cmd, &o, testSocket, testHost)
	return cmd
}

// flags adds the flags that herdrex and herdrex test share.
func flags(cmd *cobra.Command, o *options, sock, host string) {
	f := cmd.Flags()
	sockHelp := "Herdr API socket path; found as herdr finds it: $HERDR_SOCKET_PATH,\n$HERDR_SESSION, then the default session"
	hostHelp := "add the bridge to your Rex hosts (rex hosts add) as " + defaultHost + ", or as NAME with --add-host=NAME;\nthe list syncs to every device that follows your Rex home"
	if host != "" {
		sockHelp = "socket path of the Herdr server to start"
		hostHelp = "name of the Rex host to add for this run"
	}
	f.StringVar(&o.sock, "herdr-socket", sock, sockHelp)
	f.StringVar(&o.listen, "listen", "127.0.0.1:7388", "ip:port to serve Rex on: a loopback or a Tailscale (100.64.0.0/10, fd7a:115c:a1e0::/48) address")
	f.BoolVar(&o.notify, "notify", true, "send a desktop notification when an agent needs attention or finishes")
	f.StringVar(&o.addHost, "add-host", host, hostHelp)
	if host == "" {
		f.Lookup("add-host").NoOptDefVal = defaultHost
	}
}

// buildVersion is version, or the module version go install records.
func buildVersion() string {
	if version != "" {
		return version
	}
	if bi, ok := debug.ReadBuildInfo(); ok && bi.Main.Version != "" {
		return bi.Main.Version
	}
	return "(devel)"
}

// run serves Rex until Ctrl+C or SIGTERM. What it set up on the way, a
// Herdr server and a Rex host change, it undoes when it returns, the host
// change only with o.cleanup.
func run(ctx context.Context, o options) (err error) {
	addr, err := netip.ParseAddrPort(o.listen)
	if err != nil {
		return fmt.Errorf("--listen: %w", err)
	}
	if !addr.Addr().IsLoopback() && !tailscale(addr.Addr()) {
		return fmt.Errorf("--listen must be a loopback or Tailscale ip:port; the bridge has no authentication")
	}
	if o.addHost != "" { // before anything starts that would need undoing
		if _, err := rexBin(); err != nil {
			return fmt.Errorf("--add-host: %w", err)
		}
	}
	ready := daemonReady()
	if ready != nil {
		defer ready.Close()
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Cleanup runs after ctx has ended, so it gets a context of its own.
	var undo []func(context.Context) error
	defer func() {
		cctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		for _, f := range slices.Backward(undo) {
			err = errors.Join(err, f(cctx))
		}
	}()
	if o.start {
		h, err := startHerdr(ctx, o.sock)
		if err != nil {
			return err
		}
		undo = append(undo, func(ctx context.Context) error { return h.stop(ctx, o.cleanup) })
	}
	b := bridge.New(o.sock)
	b.Notify = o.notify
	if err := b.Refresh(ctx); err != nil {
		return fmt.Errorf("cannot reach Herdr at %s: %w", o.sock, err)
	}
	ln, err := net.Listen("tcp", addr.String())
	if err != nil {
		return err
	}
	defer ln.Close()
	url := "http://" + ln.Addr().String()
	if o.addHost != "" {
		c, err := addHost(ctx, o.addHost, url)
		if err != nil {
			return fmt.Errorf("rex hosts: %w", err)
		}
		if o.cleanup {
			undo = append(undo, c.undo)
		}
	}
	go b.Watch(ctx)
	go b.WatchStatus(ctx)
	go b.Poll(ctx, 5*time.Second) // safety net if an event is missed
	srv := &http.Server{Handler: b.Handler()}
	go func() { <-ctx.Done(); srv.Close() }()
	log.Printf("herdrex listening on %s (herdr: %s)", url, o.sock)
	if !addr.Addr().IsLoopback() {
		log.Printf("herdrex: no authentication; every tailnet device that can reach %s can use every pane", addr.Addr())
	}
	appHint(o.addHost, url)
	if o.cleanup {
		log.Printf("try `HERDR_SOCKET_PATH=%s herdr` in another terminal to watch the same Herdr in its own TUI", o.sock)
		log.Printf("try `rex -S %s ls`; Ctrl+C undoes it all, except a host added in Rex.app", o.addHost)
	}
	if ready != nil {
		ready.WriteString(readyMsg)
		ready.Close()
	}
	if err := srv.Serve(ln); err != http.ErrServerClosed {
		return err
	}
	return nil
}

// appHint tells how to add the bridge in Rex.app, which does not read the
// hosts that rex hosts add keeps.
func appHint(name, url string) {
	if name == "" {
		name = defaultHost
	}
	log.Printf("to use it in Rex.app: Settings > Remote Host, add a host named %s at %s", name, url)
}

// tailnet holds the addresses Tailscale gives to devices.
var tailnet = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("fd7a:115c:a1e0::/48"),
}

// tailscale reports whether a is a Tailscale address.
func tailscale(a netip.Addr) bool {
	for _, p := range tailnet {
		if p.Contains(a.Unmap()) {
			return true
		}
	}
	return false
}
