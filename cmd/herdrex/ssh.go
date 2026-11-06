package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/elonnzhang/herdrex/pkg/herdr"
	"github.com/elonnzhang/herdrex/pkg/herdr/cli"
	"github.com/elonnzhang/herdrex/pkg/herdr/jsonapi"
	"github.com/elonnzhang/herdrex/pkg/util"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func sshCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:                "ssh [SSH_OPTIONS...] DESTINATION",
		Short:              "Serve a Herdr server on an SSH destination",
		Long:               "Forward the remote Herdr sockets over SSH and serve them locally. DESTINATION accepts an SSH config alias or a destination such as user@host; connection options are passed to ssh. Remote commands are not accepted because the SSH connection stays open for the tunnel.\n\nBy default it serves the Herdr server that herdr finds on the remote host. --herdr-socket PATH serves the one at that remote socket. --start --herdr-socket PATH starts a new server there, with its config and state in PATH's name with .d for its extension, and --new starts one under /tmp; both stop the server and remove its files on exit, and refuse to touch your own Herdr.",
		Example:            "  herdrex ssh HOST\n  herdrex ssh -p 2222 user@host\n  herdrex ssh --port 7390 user@host\n  herdrex ssh --lan HOST\n  herdrex ssh --herdr-socket /tmp/x.sock HOST\n  herdrex ssh --start --herdr-socket /tmp/x.sock HOST\n  herdrex ssh --new HOST",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
				return cmd.Help()
			}
			opts, err := parseSSHArgs(args)
			if err != nil {
				return err
			}
			ctx, stop := util.SignalContext(cmd.Context())
			defer stop()
			return runSSH(ctx, opts)
		},
	}
	// DisableFlagParsing leaves parseSSHArgs to parse them; these are for --help.
	var shown options
	f := cmd.Flags()
	flags(f, &shown, "", "")
	startFlags(f, &shown)
	f.Lookup("port").Usage = "local Rex bridge port; if omitted, use the first available port from 7388 upward"
	f.Lookup("herdr-socket").Usage = "API socket path on the remote host; if omitted, the one herdr finds there"
	for _, name := range []string{"ip", "listen"} {
		f.Lookup(name).Hidden = true // herdrex ssh serves on 127.0.0.1 and what --ts and --lan add
	}
	return cmd
}

// parseSSHArgs splits args into herdrex's own flags, which are the ones herdrex
// has (flags, startFlags) and are parsed by the same definitions, and ssh's
// options and destination, which are kept in order for ssh.
func parseSSHArgs(args []string) (opts sshOptionsArgs, err error) {
	fs := pflag.NewFlagSet("herdrex ssh", pflag.ContinueOnError)
	fs.SetOutput(io.Discard)
	flags(fs, &opts.options, "", "")
	startFlags(fs, &opts.options)

	valueOptions := "BbcDEeFikJLlmOoQpRSWw"
	var own []string
	args = rewriteLAN(args)
	for i := 0; i < len(args); i++ {
		arg := args[i]
		name, _, hasValue := strings.Cut(strings.TrimPrefix(arg, "--"), "=")
		switch f := fs.Lookup(name); {
		case arg == "--":
			i++
			if i == len(args) {
				return opts, fmt.Errorf("SSH destination is required")
			}
			if opts.host != "" {
				return opts, fmt.Errorf("remote commands are not supported")
			}
			opts.host = args[i]
		case strings.HasPrefix(arg, "--") && f != nil:
			own = append(own, arg)
			if !hasValue && f.NoOptDefVal == "" { // the value is the next argument
				i++
				if i == len(args) {
					return opts, fmt.Errorf("%s needs a value", arg)
				}
				own = append(own, args[i])
			}
		case strings.HasPrefix(arg, "-"):
			if opts.host != "" {
				return opts, fmt.Errorf("SSH options must come before the destination; %q came after it", arg)
			}
			if len(arg) < 2 {
				return opts, fmt.Errorf("invalid SSH option %q", arg)
			}
			if arg == "-f" || strings.HasPrefix(arg, "-f") {
				return opts, fmt.Errorf("SSH option -f cannot be used because the tunnel must remain attached")
			}
			if arg == "-N" {
				return opts, fmt.Errorf("Herdrex manages SSH option -N itself")
			}
			opts.sshArgs = append(opts.sshArgs, arg)
			if strings.ContainsRune(valueOptions, rune(arg[1])) && len(arg) == 2 {
				i++
				if i == len(args) {
					return opts, fmt.Errorf("SSH option %q needs a value", arg)
				}
				opts.sshArgs = append(opts.sshArgs, args[i])
			}
		default:
			if opts.host != "" {
				return opts, fmt.Errorf("remote commands are not supported; put SSH options before the destination")
			}
			opts.host = arg
		}
	}
	if opts.host == "" {
		return opts, fmt.Errorf("SSH destination is required")
	}
	if err := fs.Parse(own); err != nil {
		return opts, err
	}
	opts.autoPort = !fs.Changed("port")
	if fs.Changed("ip") || fs.Changed("listen") {
		return opts, fmt.Errorf("--ip and --listen are not supported by herdrex ssh: it serves on 127.0.0.1, and on the addresses --ts and --lan add")
	}
	return opts, opts.checkHerdr()
}

// checkHerdr checks the combination of --start, --new and --herdr-socket,
// which here names a socket on the remote host.
func (o sshOptionsArgs) checkHerdr() error {
	if err := checkStartFlags(o.start, o.temp, o.sock); err != nil {
		return err
	}
	if o.sock != "" && (!path.IsAbs(o.sock) || strings.Contains(o.sock, ":")) {
		return fmt.Errorf("--herdr-socket %q must be an absolute path on the remote host, without ':'", o.sock)
	}
	return nil
}

// sshOptionsArgs are herdrex ssh's own options, parsed beside the SSH ones.
// The embedded options are the flags it shares with herdrex; sock, start and
// temp (--herdr-socket, --start, --new) then apply to the remote host.
type sshOptionsArgs struct {
	options
	sshArgs  []string
	host     string
	autoPort bool // --port was not given: use the first free port from it upward
}

// remoteHerdrDirs are where Herdr is usually installed. A non-interactive SSH
// command gets a bare PATH (zsh reads neither .zshrc nor .zprofile for it), so
// these are appended to PATH, after the user's own entries so those still win.
var remoteHerdrDirs = []string{"$HOME/.local/bin", "$HOME/.cargo/bin", "$HOME/bin", "/opt/homebrew/bin", "/usr/local/bin"}

// remoteHerdrScript returns a sh script that runs `herdr status --json` with
// dirs added to PATH. Its first argument, if not empty, is the API socket to
// ask about; Herdr's other ways of choosing a socket are then unset.
func remoteHerdrScript(dirs []string) string {
	return remoteHerdrPrelude(dirs) +
		`if [ -n "$1" ]; then unset HERDR_CONFIG_PATH HERDR_CLIENT_SOCKET_PATH HERDR_SESSION; HERDR_SOCKET_PATH=$1; export HERDR_SOCKET_PATH; fi; ` +
		`exec herdr status --json`
}

// remoteHerdrPrelude puts dirs on the end of PATH and stops with status 127
// and a message when herdr is still not found.
func remoteHerdrPrelude(dirs []string) string {
	list := strings.Join(dirs, ":")
	return `PATH="$PATH:` + list + `"; ` +
		`command -v herdr >/dev/null 2>&1 || { echo "herdr not found on PATH or in ` + list + `" >&2; exit 127; }; `
}

// remoteStatus runs herdr status --json on the host over ssh, about the API
// socket sock, or the one Herdr finds when sock is empty. stderr gets what the
// remote command and ssh print.
func remoteStatus(ctx context.Context, sshPath string, base []string, host, sock string, stderr io.Writer) (*cli.Status, error) {
	args := append(append([]string{}, base...), "-T", host, util.RemoteCommand(remoteHerdrScript(remoteHerdrDirs), sock))
	cmd := exec.CommandContext(ctx, sshPath, args...)
	// No stdin: ssh would otherwise read the terminal, and startRemoteHerdr
	// runs this every 300ms while the user may be typing.
	cmd.Stderr = stderr
	data, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("read Herdr status on %s: %w", host, err)
	}
	var status cli.Status
	if err := json.Unmarshal(data, &status); err != nil {
		return nil, fmt.Errorf("decode Herdr status on %s: %w", host, err)
	}
	return &status, nil
}

// runSSH discovers the remote API socket, forwards it and its paired terminal
// socket to temporary local sockets, then runs the ordinary bridge against them.
func runSSH(ctx context.Context, o sshOptionsArgs) error {
	host, sshArgs := o.host, o.sshArgs
	if strings.HasPrefix(host, "-") {
		return fmt.Errorf("SSH destination must not start with '-'")
	}
	var lanIPs []netip.Addr
	if len(o.lan) > 0 {
		var err error
		if lanIPs, err = util.SelectLAN(o.lan); err != nil {
			return err
		}
	}
	listeners, err := util.ListenAutoPort(o.port, o.autoPort, lanIPs, o.ts)
	if err != nil {
		return err
	}
	defer func() {
		for _, ln := range listeners {
			ln.Close()
		}
	}()
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		return fmt.Errorf("find ssh: %w", err)
	}
	dir, err := os.MkdirTemp("", "herdrex-ssh-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	controlPath := filepath.Join("/tmp", filepath.Base(dir)+"-%C")
	sshOptions := []string{
		"-o", "ControlMaster=auto",
		"-o", "ControlPersist=600",
		"-o", "ControlPath=" + controlPath,
		"-o", "ServerAliveInterval=15",
		"-o", "ServerAliveCountMax=4",
		"-o", "StreamLocalBindUnlink=yes", // read by the master, which binds the forwards
	}
	base := append(append([]string{}, sshArgs...), sshOptions...)
	// Close the shared master connection on exit; ControlPersist would keep it.
	// Registered before the remote server's stop, so that runs first.
	defer exec.Command(sshPath, append(append([]string{}, base...), "-O", "exit", host)...).Run()

	socket := o.sock // asked about by every command run on the remote host
	if o.start || o.temp {
		if o.temp {
			if socket, err = newTempSocket(); err != nil {
				return err
			}
		}
		srv, err := startRemoteHerdr(ctx, sshPath, base, host, socket)
		if err != nil {
			return err
		}
		defer srv.stop()
	}
	status, err := remoteStatus(ctx, sshPath, base, host, socket, os.Stderr)
	if err != nil {
		return err
	}
	remoteSocket := status.Server.Socket
	if !status.Server.Running || remoteSocket == "" || !filepath.IsAbs(remoteSocket) {
		return fmt.Errorf("Herdr is not running on %s or did not report an absolute socket path", host)
	}
	remoteClientSocket := herdr.ClientSocket(remoteSocket)
	if strings.Contains(remoteSocket, ":") || strings.Contains(remoteClientSocket, ":") {
		return fmt.Errorf("remote socket paths containing ':' cannot be forwarded by OpenSSH")
	}

	socketDir, releaseSocketDir, err := util.LockDir("herdrex-", host+"\x00"+remoteSocket, "herdr.sock", "herdr-client.sock")
	if errors.Is(err, util.ErrLocked) {
		return fmt.Errorf("an SSH bridge for %s is already running", host)
	}
	if err != nil {
		return fmt.Errorf("local SSH socket directory: %w", err)
	}
	defer releaseSocketDir()
	localSocket := filepath.Join(socketDir, "herdr.sock")
	localClientSocket := herdr.ClientSocket(localSocket)
	defer os.Remove(localSocket)
	defer os.Remove(localClientSocket)
	tr, err := reachHerdr(ctx, sshPath, base, host, status, socket, localSocket)
	if err != nil {
		return err
	}
	defer tr.Stop()
	log.Printf("herdrex ssh: %s reached through SSH (%s)", host, tr.Mode())
	bridgeCtx, cancelBridge := context.WithCancel(ctx)
	go func() {
		select {
		case <-tr.Done():
			cancelBridge()
		case <-bridgeCtx.Done():
		}
	}()
	bridgeErr := run(bridgeCtx, options{
		sock:      localSocket,
		addr:      listeners[0].Addr().(*net.TCPAddr).AddrPort(),
		listeners: listeners,
		allow:     o.allow,
		allowFor:  o.allowFor,
		notify:    o.notify,
		addHost:   o.addHost,
		uploads:   sshUploads{ssh: sshPath, base: base, host: host}, // files go where the pane's programs run
	})
	cancelBridge()
	ended := false
	select {
	case <-tr.Done():
		ended = true
	default:
	}
	tr.Stop()
	if bridgeErr != nil {
		return bridgeErr
	}
	if ended && ctx.Err() == nil {
		return fmt.Errorf("SSH connection to %s ended: %v", host, cmp.Or(tr.Err(), errors.New("ssh exited")))
	}
	return nil
}

// reachHerdr serves the remote Herdr's two sockets at localSocket and its
// client socket, by the fastest transport that works. Unix socket forwarding
// is the fast path. Some SSH relays accept -L but refuse every forwarded
// connection, so each transport is probed. Otherwise every connection goes
// over one SSH session with a small Perl mux, and without Perl through Herdr's
// own stdio bridges, one SSH session per connection. socket is the remote API
// socket the user chose, if any, which the stdio bridges need to be told.
func reachHerdr(ctx context.Context, sshPath string, base []string, host string, status *cli.Status, socket, localSocket string) (*util.Tunnel, error) {
	remoteSocket := status.Server.Socket
	sockets := []util.Endpoint{
		{Local: localSocket, Remote: remoteSocket},
		{Local: herdr.ClientSocket(localSocket), Remote: herdr.ClientSocket(remoteSocket)},
	}
	steps := []struct {
		name  string
		start func() (*util.Tunnel, error)
	}{
		{"socket forwarding", func() (*util.Tunnel, error) { return util.Forward(ctx, sshPath, base, host, sockets) }},
		{"one-session mux", func() (*util.Tunnel, error) { return util.Mux(ctx, sshPath, base, host, sockets) }},
	}
	for _, step := range steps {
		tr, err := step.start()
		if err == nil {
			if err = probeSocket(ctx, localSocket); err == nil {
				return tr, nil
			}
			tr.Stop()
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		log.Printf("herdrex ssh: %s to %s failed (%v)", step.name, host, err)
	}

	bridge := func(sub string) string {
		cmd := ""
		if socket != "" {
			cmd = "env HERDR_SOCKET_PATH=" + util.ShellQuote(socket) + " "
		}
		cmd += util.ShellQuote(cmp.Or(status.Client.Binary, "herdr"))
		if status.Server.Session != "" {
			cmd += " --session " + util.ShellQuote(status.Server.Session)
		}
		return cmd + " " + sub
	}
	tr, err := util.Stdio(ctx, sshPath, base, host, []util.Endpoint{
		{Local: sockets[0].Local, Remote: bridge("remote-api-bridge")},
		{Local: sockets[1].Local, Remote: bridge("remote-client-bridge")},
	})
	if err != nil {
		return nil, fmt.Errorf("start SSH stdio bridge to %s: %w", host, err)
	}
	if err := probeSocket(ctx, localSocket); err != nil {
		tr.Stop()
		return nil, fmt.Errorf("reach Herdr on %s over SSH: %w", host, err)
	}
	return tr, nil
}

// probeSocket checks that a forwarded API socket really reaches Herdr: some
// SSH relays accept -L but refuse every direct-streamlocal channel.
func probeSocket(ctx context.Context, sock string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := (&jsonapi.Client{Socket: sock}).Ping(ctx)
	return err
}
