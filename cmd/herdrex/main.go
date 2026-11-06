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
	"strings"
	"syscall"
	"time"

	"github.com/elonnzhang/herdrex/internal/bridge"
	"github.com/elonnzhang/herdrex/pkg/herdr"
	"github.com/elonnzhang/herdrex/pkg/util"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

// version is set with -ldflags "-X main.version=..."; otherwise it comes
// from the module version that go install records.
var version = ""

const long = `> 

herdrex serves a Herdr server as a Rex host, so that Rex.app and the rex command line can use its workspaces as sessions, its tabs as windows and its panes as blocks.

The bridge has no authentication. On a loopback address only this machine can reach it. --ts adds this machine's Tailscale address; --lan uses en0 by default or selects LAN addresses from its values. Every tailnet device that Tailscale lets reach this machine may use the Tailscale address. A LAN address admits only this machine until --allow names the LAN networks that may connect (--allow all names every address); every other LAN caller is refused. --allow-for ends --allow after a duration. Loopback always may connect.

--add-host adds the host for the rex command line. 
Rex.app does not read those hosts: add it in Rex.app under Settings > Remote Host, with the name and address herdrex prints when it starts.`

const example = `
  # only herdrex，serve the default Herdr on 127.0.0.1:7388, you need to add host in Rex.app manually
  herdrex
  
  # serve the default Herdr on 127.0.0.1:7388 and add it to your Rex hosts
  # as "herdr"; then: rex -S herdr ls
  herdrex --add-host

  # serve a Herdr that is already running at another socket, on another port
  herdrex --herdr-socket /tmp/herdr/herdr.sock --port 7392

  # the same as --ip 100.64.0.9 --port 7388
  herdrex --listen 100.64.0.9:7388

  # start a new Herdr server at that socket, serve it, stop it on Ctrl+C
  herdrex --herdr-socket /tmp/herdr/x.sock --start --add-host=x

  # start a new Herdr server under /tmp, serve it, stop it and remove it on Ctrl+C
  herdrex --new --add-host=x

  # the same in the background, still running after the terminal closes
  herdrex --add-host --daemon

  # try it without touching your Herdr; Ctrl+C undoes everything
  herdrex test

  # serve it to your other devices over Tailscale and the LAN, as host "studio"
  herdrex --ts --lan --port 1234 --add-host=studio

  # the same, but only this machine and 192.168.1.0/24 may connect
  herdrex --lan --allow 192.168.1.0/24 --add-host=studio

  # let any LAN device connect for the next two hours only
  herdrex --lan --allow all --allow-for 2h`

// options are the command's flags.
type options struct {
	sock     string
	ip       string        // the address to serve on, beside the ones --lan and --ts add
	port     int           // the port of every address
	listen   string        // ip:port, the older spelling of --ip with --port
	ts       bool          // also serve on this machine's Tailscale address
	lan      []string      // LAN selectors; bare --lan selects en0
	lanIPs   []netip.Addr  // resolved local addresses passed to run and --daemon child
	allow    []string      // LAN networks that may connect, or "all"; loopback and Tailscale always may
	allowFor time.Duration // how long allow admits callers; 0 for no limit
	notify   bool
	addHost  string // "" leaves the Rex hosts alone
	start    bool   // start a Herdr server at sock
	temp     bool   // --new: sock is a fresh one under /tmp, and goes with the server on exit
	cleanup  bool   // on exit, undo the host change and remove the started server's files
	daemon   bool   // run in the background
	logFile  string // where the background process logs; "" for daemonLog

	addr      netip.AddrPort     // resolved by resolveAddr before run
	listeners []net.Listener     // sockets already bound by a subcommand; nil binds addr and the --lan, --ts ones
	uploads   bridge.UploadStore // where uploaded files go; nil for this machine's temporary directory
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
	cmd := command()
	cmd.SetArgs(normalizeLANArgs(os.Args[1:]))
	if err := cmd.Execute(); err != nil {
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
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 && cmd.Flags().Changed("lan") {
				arg, isSelector := unexpectedLANArg(args)
				if isSelector {
					return fmt.Errorf("unexpected LAN selector %q; give each one its own --lan, such as --lan=%s", arg, arg)
				}
				return fmt.Errorf("unexpected argument %q; give a host name as --add-host=NAME", arg)
			}
			if len(args) > 0 {
				return fmt.Errorf("unexpected argument %q; give a host name as --add-host=NAME", args[0])
			}
			return nil
		},
		Version:      buildVersion(),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			given := ""
			if cmd.Flags().Changed("herdr-socket") {
				given = o.sock
			}
			if err := checkStartFlags(o.start, o.temp, given); err != nil {
				return err
			}
			if o.logFile != "" && !o.daemon {
				return fmt.Errorf("--log needs --daemon")
			}
			if err := resolveNetworkOptions(cmd, &o); err != nil {
				return err
			}
			if o.daemon && os.Getenv(readyEnv) == "" {
				return background(o)
			}
			if o.temp { // after --daemon's parent: the child picks its own socket
				var err error
				if o.sock, err = newTempSocket(); err != nil {
					return err
				}
				o.start = true
			}
			return run(cmd.Context(), o)
		},
	}
	cmd.AddCommand(sshCommand())
	f := cmd.Flags()
	flags(f, &o, herdr.SocketPath(""), "")
	startFlags(f, &o)
	f.BoolVar(&o.daemon, "daemon", false, "run in the background, in a session of its own, until killed; kill PID stops it\nas Ctrl+C would")
	f.StringVar(&o.logFile, "log", "", "log file of --daemon (default <user cache dir>/herdrex/herdrex-PORT.log)")
	cmd.AddCommand(testCommand())
	cmd.AddCommand(updateCommand())
	return cmd
}

// background starts herdrex in the background with o's flags and returns
// once it serves.
func background(o options) error {
	file := o.logFile
	if file == "" {
		var err error
		if file, err = daemonLog(o.addr.Port()); err != nil {
			return err
		}
	}
	return daemonize(file, o.lanIPs)
}

// resolveAddr turns --listen, or --ip with --port, into the address to serve
// on. The two spellings do not mix: --listen already holds both parts.
func resolveAddr(cmd *cobra.Command, o options) (netip.AddrPort, error) {
	if o.listen != "" {
		if cmd.Flags().Changed("ip") || cmd.Flags().Changed("port") {
			return netip.AddrPort{}, fmt.Errorf("--listen holds the ip and the port; use it alone, or use --ip and --port")
		}
		addr, err := netip.ParseAddrPort(o.listen)
		if err != nil {
			return netip.AddrPort{}, fmt.Errorf("--listen: %w", err)
		}
		return addr, nil
	}
	ip, err := netip.ParseAddr(o.ip)
	if err != nil {
		return netip.AddrPort{}, fmt.Errorf("--ip: %w", err)
	}
	if o.port < 1 || o.port > 65535 {
		return netip.AddrPort{}, fmt.Errorf("--port %d: want 1 to 65535", o.port)
	}
	return netip.AddrPortFrom(ip.Unmap(), uint16(o.port)), nil
}

func resolveNetworkOptions(cmd *cobra.Command, o *options) error {
	addr, err := resolveAddr(cmd, *o)
	if err != nil {
		return err
	}
	o.addr = addr
	changedLAN := cmd.Flags().Changed("lan")
	if selection, ok := os.LookupEnv(daemonLANSelectionEnv); ok {
		os.Unsetenv(daemonLANSelectionEnv)
		if os.Getenv(readyEnv) != "" && changedLAN {
			o.lanIPs, err = util.ParseLANSelection(selection)
			return err
		}
	}
	if !changedLAN {
		return nil
	}
	o.lanIPs, err = util.SelectLAN(o.lan)
	return err
}

func normalizeLANArgs(args []string) []string {
	if len(args) > 0 && args[0] == "ssh" {
		return slices.Clone(args) // its own parser calls rewriteLAN
	}
	return rewriteLAN(args)
}

// rewriteLAN gives every --lan its value as --lan=VALUE: the selector that
// follows it, or util.LANBare for a bare --lan.
func rewriteLAN(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			return append(out, args[i:]...)
		}
		if args[i] != "--lan" {
			out = append(out, args[i])
			continue
		}
		if i+1 < len(args) && util.LANSelector(args[i+1]) {
			out = append(out, "--lan="+args[i+1])
			i++
			continue
		}
		out = append(out, "--lan="+util.LANBare)
	}
	return out
}

func unexpectedLANArg(args []string) (string, bool) {
	for _, arg := range args {
		if !util.LANSelector(arg) {
			return arg, false
		}
	}
	return args[len(args)-1], true
}

func testCommand() *cobra.Command {
	o := options{
		start:   true,
		cleanup: true,
	}
	cmd := &cobra.Command{
		Use:     "test",
		Short:   "Try Herdr in Rex: start a Herdr, serve it, add it to Rex hosts; Ctrl+C undoes it all",
		Long:    testLong,
		Example: "  herdrex test\n  rex -S " + testHost + " new   # in another terminal",
		Args: func(cmd *cobra.Command, args []string) error {
			if len(args) > 0 && cmd.Flags().Changed("lan") {
				arg, isSelector := unexpectedLANArg(args)
				if isSelector {
					return fmt.Errorf("unexpected LAN selector %q; give each one its own --lan, such as --lan=%s", arg, arg)
				}
				return fmt.Errorf("unexpected argument %q", arg)
			}
			return cobra.NoArgs(cmd, args)
		},
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if o.addHost == "" {
				return fmt.Errorf("--add-host must name a host")
			}
			if err := resolveNetworkOptions(cmd, &o); err != nil {
				return err
			}
			return run(cmd.Context(), o)
		},
	}
	flags(cmd.Flags(), &o, testSocket, testHost)
	return cmd
}

// startFlags adds --start and --new, which herdrex and herdrex ssh share.
func startFlags(f *pflag.FlagSet, o *options) {
	f.BoolVar(&o.start, "start", false, "start a new Herdr server at --herdr-socket (on the remote host with ssh), with its config and state beside it\n(x.sock: x.d), and stop it on exit")
	f.BoolVar(&o.temp, "new", false, "start a new Herdr server at a socket of its own under /tmp (/tmp/herdrex-XXXX/herdr.sock), serve it,\nand stop it and remove its files on exit")
}

// flags adds the flags that herdrex, herdrex test and herdrex ssh share.
func flags(f *pflag.FlagSet, o *options, sock, host string) {
	sockHelp := "Herdr API socket path; found as herdr finds it: $HERDR_SOCKET_PATH,\n$HERDR_SESSION, then the default session"
	hostHelp := "add the bridge to your Rex hosts (rex hosts add) as " + defaultHost + ", or as NAME with --add-host=NAME;\nthe list syncs to every device that follows your Rex home"
	if host != "" {
		sockHelp = "socket path of the Herdr server to start"
		hostHelp = "name of the Rex host to add for this run"
	}
	f.StringVar(&o.sock, "herdr-socket", sock, sockHelp)
	f.StringVar(&o.ip, "ip", "127.0.0.1", "address to serve Rex on: a loopback address, a Tailscale address, or a LAN\naddress with --lan")
	f.IntVar(&o.port, "port", 7388, "port to serve Rex on, for every address")
	f.StringVar(&o.listen, "listen", "", "ip:port to serve Rex on, instead of --ip and --port")
	f.BoolVar(&o.ts, "ts", false, "also serve on this machine's Tailscale address, on --port")
	f.StringArrayVar(&o.lan, "lan", nil, "also serve on LAN addresses; bare --lan uses en0, values select by prefix (192, 172, 10), ipv6, IP or CIDR; repeat to add addresses")
	f.StringSliceVar(&o.allow, "allow", nil, "LAN addresses that may use the bridge, as networks in CIDR form, bare addresses or\n\"all\" for every address, comma-separated or repeated. Loopback and Tailscale callers always may; without\n--allow no LAN device may connect; \"all\" admits every address")
	f.DurationVar(&o.allowFor, "allow-for", 0, "how long --allow admits LAN callers after start, such as 30m or 2h; after that only loopback and\nTailscale callers may connect. 0 means no limit")
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
	addrs, err := util.ListenSelected(o.addr, o.lanIPs, o.ts)
	if err != nil {
		return err
	}
	allowed, err := util.ParseAllow(o.allow)
	if err != nil {
		return err
	}
	if o.allowFor < 0 || (o.allowFor > 0 && allowed.Empty()) {
		return fmt.Errorf("--allow-for needs a positive duration and --allow to apply to")
	}
	allowed.ExpireAfter(o.allowFor)
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
		undo = append(undo, func(ctx context.Context) error { return h.stop(ctx, o.cleanup || o.temp) })
	}
	b := bridge.New(o.sock)
	b.Notify = o.notify
	b.Uploads = o.uploads
	if err := b.Refresh(ctx); err != nil {
		return fmt.Errorf("cannot reach Herdr at %s: %w", o.sock, err)
	}
	var listeners []net.Listener
	var urls []string
	if len(o.listeners) > 0 { // already bound by a subcommand, which serves these exact addresses
		listeners, urls = o.listeners, nil
		addrs = addrs[:0]
		for _, ln := range listeners {
			urls = append(urls, "http://"+ln.Addr().String())
			if ap, err := netip.ParseAddrPort(ln.Addr().String()); err == nil {
				addrs = append(addrs, ap)
			}
		}
	} else {
		listeners, urls, err = util.ListenAll(addrs)
		if err != nil {
			return err
		}
	}
	defer func() {
		for _, ln := range listeners {
			ln.Close()
		}
	}()
	url := firstPublic(urls, allowed)
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
	srv := &http.Server{Handler: allowed.Middleware(b.Handler())}
	go func() { <-ctx.Done(); srv.Close() }()
	for _, u := range urls {
		log.Printf("herdrex listening on %s (herdr: %s)", u, o.sock)
	}
	for _, a := range addrs {
		if a.Addr().IsLoopback() {
			continue
		}
		switch {
		case util.Tailscale(a.Addr()):
			log.Printf("herdrex: no authentication on %s; every tailnet device that can reach it can use every pane", a.Addr())
		case !allowed.Empty() && allowed.String() == util.AllowAll:
			log.Printf("herdrex: no authentication on %s; --allow all admits every device that can reach it%s", a.Addr(), allowedUntil(allowed))
		case !allowed.Empty():
			log.Printf("herdrex: on %s only %s may connect from the LAN%s; every other LAN caller is refused", a.Addr(), allowed, allowedUntil(allowed))
		default:
			log.Printf("herdrex: %s is served, but without --allow no device on the LAN may use it", a.Addr())
		}
	}
	if until := allowed.Until(); !until.IsZero() {
		timer := time.AfterFunc(time.Until(until), func() {
			log.Printf("herdrex: --allow has expired; LAN callers are refused from now on, loopback and Tailscale still may connect")
		})
		defer timer.Stop()
	}
	appHint(o.addHost, url)
	showQRCodes(urls, allowed)
	if o.cleanup {
		log.Printf("try `HERDR_SOCKET_PATH=%s herdr` in another terminal to watch the same Herdr in its own TUI", o.sock)
		log.Printf("try `rex -S %s ls`; Ctrl+C undoes it all, except a host added in Rex.app", o.addHost)
	}
	if ready != nil {
		ready.WriteString(readyMsg)
		ready.Close()
	}
	return serve(srv, listeners)
}

// firstPublic picks the URL to hand out: the first one that is not loopback
// and that allowed admits, so that other devices can reach it and this
// machine, connecting to its own address from that address, is let in; the
// loopback one when there is no such URL, as without --allow.
func firstPublic(urls []string, allowed util.AllowList) string {
	for _, u := range urls {
		ap, err := netip.ParseAddrPort(strings.TrimPrefix(u, "http://"))
		if err == nil && !ap.Addr().IsLoopback() && allowed.Contains(ap.Addr()) {
			return u
		}
	}
	for _, u := range urls {
		if ap, err := netip.ParseAddrPort(strings.TrimPrefix(u, "http://")); err == nil && ap.Addr().IsLoopback() {
			return u
		}
	}
	return urls[0]
}

// allowedUntil says, for a log line, when allowed stops admitting callers.
func allowedUntil(allowed util.AllowList) string {
	if until := allowed.Until(); !until.IsZero() {
		return " until " + until.Format("15:04:05")
	}
	return ""
}

// showQRCodes prints one address and its code for every address served,
// loopback included, then says when the LAN codes refuse every device.
func showQRCodes(urls []string, allowed util.AllowList) {
	closed, loopback := 0, ""
	for _, url := range urls {
		util.ShowQRCode(url)
		ap, err := netip.ParseAddrPort(strings.TrimPrefix(url, "http://"))
		if err != nil {
			continue
		}
		if ap.Addr().IsLoopback() {
			loopback = url
		} else if !util.Tailscale(ap.Addr()) && allowed.Empty() {
			closed++
		}
	}
	if closed > 0 && loopback != "" {
		log.Printf("herdrex: this machine can use %s; devices that scan a LAN code are refused until --allow names their network", loopback)
	}
}

// serve runs the server on every listener until one fails or they all stop.
func serve(srv *http.Server, listeners []net.Listener) error {
	errc := make(chan error, len(listeners))
	for _, ln := range listeners {
		go func(ln net.Listener) { errc <- srv.Serve(ln) }(ln)
	}
	var first error
	for range listeners {
		if err := <-errc; err != nil && err != http.ErrServerClosed && first == nil {
			first = err
			srv.Close()
		}
	}
	return first
}

// appHint tells how to add the bridge in Rex.app, which does not read the
// hosts that rex hosts add keeps.
func appHint(name, url string) {
	if name == "" {
		name = defaultHost
	}
	log.Printf("to use it in Rex.app: Settings > Remote Host, add a host named %s at %s", name, url)
}
