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

	"github.com/skip2/go-qrcode"
	"github.com/spf13/cobra"
)

// version is set with -ldflags "-X main.version=..."; otherwise it comes
// from the module version that go install records.
var version = ""

const long = `> 

herdrex serves a Herdr server as a Rex host, so that Rex.app and the rex command line can use its workspaces as sessions, its tabs as windows and its panes as blocks.

The bridge has no authentication. On a loopback address only this machine can reach it. --ts adds this machine's Tailscale address; --lan uses en0 by default or selects LAN addresses from its values. Every tailnet device that Tailscale lets reach this machine may use the Tailscale address. A LAN address admits only this machine until --allow names the LAN networks that may connect; every other LAN caller is refused. Loopback always may connect.

--add-host adds the host for the rex command line. 
Rex.app does not read those hosts: add it in Rex.app under Settings > Remote Host, with the name and address herdrex prints when it starts.`

const example = `
  # serve the default Herdr on 127.0.0.1:7388 and add it to your Rex hosts
  # as "herdr"; then: rex -S herdr ls
  herdrex --add-host

  # serve a Herdr that is already running at another socket, on another port
  herdrex --herdr-socket /tmp/herdr/herdr.sock --port 7392

  # the same as --ip 100.64.0.9 --port 7388
  herdrex --listen 100.64.0.9:7388

  # start a new Herdr server at that socket, serve it, stop it on Ctrl+C
  herdrex --herdr-socket /tmp/herdr/x.sock --start --add-host=x

  # the same in the background, still running after the terminal closes
  herdrex --add-host --daemon

  # try it without touching your Herdr; Ctrl+C undoes everything
  herdrex test

  # serve it to your other devices over Tailscale and the LAN, as host "studio"
  herdrex --ts --lan --port 1234 --add-host=studio

  # the same, but only this machine and 192.168.1.0/24 may connect
  herdrex --lan --allow 192.168.1.0/24 --add-host=studio`

// options are the command's flags.
type options struct {
	sock    string
	ip      string       // the address to serve on, beside the ones --lan and --ts add
	port    int          // the port of every address
	listen  string       // ip:port, the older spelling of --ip with --port
	ts      bool         // also serve on this machine's Tailscale address
	lan     []string     // LAN selectors; bare --lan selects en0
	lanIPs  []netip.Addr // resolved local addresses passed to run and --daemon child
	allow   []string     // LAN networks that may connect; loopback and Tailscale always may
	notify  bool
	addHost string // "" leaves the Rex hosts alone
	start   bool   // start a Herdr server at sock
	cleanup bool   // on exit, undo the host change and remove the started server's files
	daemon  bool   // run in the background
	logFile string // where the background process logs; "" for daemonLog

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
			if o.start && !cmd.Flags().Changed("herdr-socket") {
				return fmt.Errorf("--start needs --herdr-socket: the path of the new server's socket")
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
			return run(cmd.Context(), o)
		},
	}
	cmd.AddCommand(sshCommand())
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
			o.lanIPs, err = parseLANSelection(selection)
			return err
		}
	}
	if !changedLAN {
		return nil
	}
	o.lanIPs, err = selectLAN(o.lan)
	return err
}

func normalizeLANArgs(args []string) []string {
	if len(args) > 0 && args[0] == "ssh" {
		return slices.Clone(args)
	}
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		if args[i] == "--" {
			return append(out, args[i:]...)
		}
		if args[i] != "--lan" {
			out = append(out, args[i])
			continue
		}
		if i+1 < len(args) && lanSelector(args[i+1]) {
			out = append(out, "--lan="+args[i+1])
			i++
			continue
		}
		out = append(out, "--lan="+lanBareValue)
	}
	return out
}

func unexpectedLANArg(args []string) (string, bool) {
	for _, arg := range args {
		if !lanSelector(arg) {
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
	f.StringVar(&o.ip, "ip", "127.0.0.1", "address to serve Rex on: a loopback address, a Tailscale address, or a LAN\naddress with --lan")
	f.IntVar(&o.port, "port", 7388, "port to serve Rex on, for every address")
	f.StringVar(&o.listen, "listen", "", "ip:port to serve Rex on, instead of --ip and --port")
	f.BoolVar(&o.ts, "ts", false, "also serve on this machine's Tailscale address, on --port")
	f.StringArrayVar(&o.lan, "lan", nil, "also serve on LAN addresses; bare --lan uses en0, values select by prefix (192, 172, 10), ipv6, IP or CIDR; repeat to add addresses")
	f.StringSliceVar(&o.allow, "allow", nil, "LAN addresses that may use the bridge, as networks in CIDR form or bare addresses,\ncomma-separated or repeated. Loopback and Tailscale callers always may; without\n--allow no LAN device may connect")
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
	addrs, err := listenSelectedAddrs(o.addr, o.lanIPs, o.ts)
	if err != nil {
		return err
	}
	allowed, err := parseAllow(o.allow)
	if err != nil {
		return err
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
		listeners, urls, err = listenAll(addrs)
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
	srv := &http.Server{Handler: allowed.middleware(b.Handler())}
	go func() { <-ctx.Done(); srv.Close() }()
	for _, u := range urls {
		log.Printf("herdrex listening on %s (herdr: %s)", u, o.sock)
	}
	for _, a := range addrs {
		if a.Addr().IsLoopback() {
			continue
		}
		switch {
		case tailscale(a.Addr()):
			log.Printf("herdrex: no authentication on %s; every tailnet device that can reach it can use every pane", a.Addr())
		case len(allowed) > 0:
			log.Printf("herdrex: on %s only %s may connect from the LAN; every other LAN caller is refused", a.Addr(), allowed)
		default:
			log.Printf("herdrex: %s is served, but without --allow no device on the LAN may use it", a.Addr())
		}
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

// listenAll serves on every address, and fails if any of them cannot be
// served: half a bridge is worse than none.
func listenAll(addrs []netip.AddrPort) ([]net.Listener, []string, error) {
	var listeners []net.Listener
	var urls []string
	for _, a := range addrs {
		ln, err := net.Listen("tcp", a.String())
		if err != nil {
			for _, l := range listeners {
				l.Close()
			}
			return nil, nil, fmt.Errorf("--listen %s: %w", a, err)
		}
		listeners = append(listeners, ln)
		urls = append(urls, "http://"+ln.Addr().String())
	}
	return listeners, urls, nil
}

// firstPublic picks the URL to hand out: the first one that is not loopback
// and that allowed admits, so that other devices can reach it and this
// machine, connecting to its own address from that address, is let in; the
// loopback one when there is no such URL, as without --allow.
func firstPublic(urls []string, allowed allowList) string {
	for _, u := range urls {
		ap, err := netip.ParseAddrPort(strings.TrimPrefix(u, "http://"))
		if err == nil && !ap.Addr().IsLoopback() && allowed.contains(ap.Addr()) {
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

// showQRCodes prints one address and its code for every address served,
// loopback included, then says when the LAN codes refuse every device.
func showQRCodes(urls []string, allowed allowList) {
	closed, loopback := 0, ""
	for _, url := range urls {
		showQRCodeGroup(url)
		ap, err := netip.ParseAddrPort(strings.TrimPrefix(url, "http://"))
		if err != nil {
			continue
		}
		if ap.Addr().IsLoopback() {
			loopback = url
		} else if !tailscale(ap.Addr()) && len(allowed) == 0 {
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

// qrCode renders url as a small QR code, if stderr is a terminal.
func qrCode(url string) (string, bool) {
	info, err := os.Stderr.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return "", false
	}
	code, err := qrcode.New(url, qrcode.Medium)
	if err != nil {
		log.Printf("cannot render QR code: %v", err)
		return "", false
	}
	return code.ToSmallString(false), true
}

// showQRCodeGroup prints one address and then its code, as one group, for
// each address a command serves.
func showQRCodeGroup(url string) {
	if code, ok := qrCode(url); ok {
		fmt.Fprintf(os.Stderr, "\n%s\n%s\n", url, code)
	}
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
