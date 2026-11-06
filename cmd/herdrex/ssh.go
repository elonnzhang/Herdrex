package main

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/elonnzhang/herdrex/pkg/herdr"
	"github.com/elonnzhang/herdrex/pkg/herdr/cli"
	"github.com/spf13/cobra"
)

func sshCommand() *cobra.Command {
	var localPort int
	cmd := &cobra.Command{
		Use:                "ssh [SSH_OPTIONS...] DESTINATION",
		Short:              "Serve a Herdr server on an SSH destination",
		Long:               "Forward the remote Herdr sockets over SSH and serve them locally. DESTINATION accepts an SSH config alias or a destination such as user@host; connection options are passed to ssh. Remote commands are not accepted because the SSH connection stays open for the tunnel.",
		Example:            "  herdrex ssh mis\n  herdrex ssh -p 2222 user@host\n  herdrex ssh --port 7390 user@host\n  herdrex ssh --lan mis",
		DisableFlagParsing: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 1 && (args[0] == "-h" || args[0] == "--help") {
				return cmd.Help()
			}
			opts, err := parseSSHArgs(args)
			if err != nil {
				return err
			}
			ctx, stop := signalContext(cmd.Context())
			defer stop()
			return runSSH(ctx, opts)
		},
	}
	cmd.Flags().IntVar(&localPort, "port", 7388, "local Rex bridge port; if omitted, use the first available port from 7388 upward")
	return cmd
}

func parseSSHArgs(args []string) (opts sshOptionsArgs, err error) {
	opts.port, opts.autoPort = 7388, true
	valueOptions := "BbcDEeFikJLlmOoQpRSWw"
	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch {
		case arg == "--port":
			i++
			if i == len(args) {
				return opts, fmt.Errorf("--port needs a value")
			}
			opts.port, err = strconv.Atoi(args[i])
			if err != nil {
				return opts, fmt.Errorf("--port %q: %w", args[i], err)
			}
			opts.autoPort = false
		case strings.HasPrefix(arg, "--port="):
			opts.port, err = strconv.Atoi(strings.TrimPrefix(arg, "--port="))
			if err != nil {
				return opts, fmt.Errorf("%s: %w", arg, err)
			}
			opts.autoPort = false
		case arg == "--ts":
			opts.ts = true
		case arg == "--lan" || strings.HasPrefix(arg, "--lan="):
			value, has := strings.CutPrefix(arg, "--lan=")
			switch {
			case has:
			case i+1 < len(args) && lanSelector(args[i+1]):
				i++
				value = args[i]
			default:
				value = lanBareValue
			}
			opts.lan = append(opts.lan, value)
		case arg == "--allow" || strings.HasPrefix(arg, "--allow="):
			value, has := strings.CutPrefix(arg, "--allow=")
			if !has {
				i++
				if i == len(args) {
					return opts, fmt.Errorf("--allow needs a value")
				}
				value = args[i]
			}
			opts.allow = append(opts.allow, strings.Split(value, ",")...)
		case arg == "--":
			i++
			if i == len(args) {
				return opts, fmt.Errorf("SSH destination is required")
			}
			if opts.host != "" {
				return opts, fmt.Errorf("remote commands are not supported")
			}
			opts.host = args[i]
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
	return opts, nil
}

// sshOptionsArgs are herdrex ssh's own options, parsed beside the SSH ones.
type sshOptionsArgs struct {
	sshArgs  []string
	host     string
	port     int
	autoPort bool
	ts       bool
	lan      []string
	allow    []string
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
		if lanIPs, err = selectLAN(o.lan); err != nil {
			return err
		}
	}
	listeners, err := listenSSHAddrs(o.port, o.autoPort, lanIPs, o.ts)
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
	statusArgs := append(append([]string{}, sshArgs...), sshOptions...)
	statusArgs = append(statusArgs, "-T", host, "herdr", "status", "--json")
	statusCmd := exec.CommandContext(ctx, sshPath, statusArgs...)
	statusCmd.Stdin = os.Stdin
	statusCmd.Stderr = os.Stderr
	statusData, err := statusCmd.Output()
	if err != nil {
		return fmt.Errorf("read Herdr status on %s: %w", host, err)
	}
	var status cli.Status
	if err := json.Unmarshal(statusData, &status); err != nil {
		return fmt.Errorf("decode Herdr status on %s: %w", host, err)
	}
	remoteSocket := status.Server.Socket
	if !status.Server.Running || remoteSocket == "" || !filepath.IsAbs(remoteSocket) {
		return fmt.Errorf("Herdr is not running on %s or did not report an absolute socket path", host)
	}
	remoteClientSocket := herdr.ClientSocket(remoteSocket)
	if strings.Contains(remoteSocket, ":") || strings.Contains(remoteClientSocket, ":") {
		return fmt.Errorf("remote socket paths containing ':' cannot be forwarded by OpenSSH")
	}

	socketDir, releaseSocketDir, err := sshSocketDir(host, remoteSocket)
	if err != nil {
		return err
	}
	defer releaseSocketDir()
	localSocket := filepath.Join(socketDir, "herdr.sock")
	localClientSocket := herdr.ClientSocket(localSocket)
	defer os.Remove(localSocket)
	defer os.Remove(localClientSocket)
	base := append(append([]string{}, sshArgs...), sshOptions...)
	// Close the shared master connection on exit; ControlPersist would keep it.
	defer exec.Command(sshPath, append(append([]string{}, base...), "-O", "exit", host)...).Run()

	// Unix socket forwarding is the fast path. Some SSH relays accept -L but
	// refuse every forwarded connection, so probe it. Otherwise carry every
	// connection over one SSH session with a small Perl mux, and without Perl
	// run Herdr's own stdio bridges, one SSH session per connection.
	tr, err := startForward(ctx, sshPath, base, host, localSocket, localClientSocket, remoteSocket, remoteClientSocket)
	if err == nil {
		if perr := probeSocket(ctx, localSocket); perr != nil {
			log.Printf("herdrex ssh: socket forwarding to %s does not reach Herdr (%v)", host, perr)
			tr.stop()
			tr = nil
		}
	} else if ctx.Err() == nil {
		log.Printf("herdrex ssh: socket forwarding to %s failed (%v)", host, err)
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if tr == nil {
		tr, err = startMux(ctx, sshPath, base, host, localSocket, localClientSocket, remoteSocket, remoteClientSocket)
		if err == nil {
			if perr := probeSocket(ctx, localSocket); perr != nil {
				err = perr
				tr.stop()
				tr = nil
			}
		}
		if err != nil && ctx.Err() == nil {
			log.Printf("herdrex ssh: one-session mux to %s failed (%v); using one SSH session per connection", host, err)
		}
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if tr == nil {
		tr, err = startStdioProxy(ctx, sshPath, base, host, cmp.Or(status.Client.Binary, "herdr"), status.Server.Session, localSocket, localClientSocket)
		if err != nil {
			return fmt.Errorf("start SSH stdio bridge to %s: %w", host, err)
		}
		if err := probeSocket(ctx, localSocket); err != nil {
			tr.stop()
			return fmt.Errorf("reach Herdr on %s over SSH: %w", host, err)
		}
	}
	defer tr.stop()
	log.Printf("herdrex ssh: %s reached through SSH (%s)", host, tr.mode)
	bridgeCtx, cancelBridge := context.WithCancel(ctx)
	go func() {
		select {
		case <-tr.done:
			cancelBridge()
		case <-bridgeCtx.Done():
		}
	}()
	bridgeErr := run(bridgeCtx, options{
		sock:      localSocket,
		addr:      listeners[0].Addr().(*net.TCPAddr).AddrPort(),
		listeners: listeners,
		allow:     o.allow,
		notify:    true,
		uploads:   sshUploads{ssh: sshPath, base: base, host: host}, // files go where the pane's programs run
	})
	cancelBridge()
	ended := false
	select {
	case <-tr.done:
		ended = true
	default:
	}
	tr.stop()
	if bridgeErr != nil {
		return bridgeErr
	}
	if ended && ctx.Err() == nil {
		return fmt.Errorf("SSH connection to %s ended: %v", host, cmp.Or(tr.err, errors.New("ssh exited")))
	}
	return nil
}

func sshSocketDir(host, remoteSocket string) (string, func(), error) {
	seed := fmt.Sprintf("%d\x00%s\x00%s", os.Getuid(), host, remoteSocket)
	digest := sha256.Sum256([]byte(seed))
	dir := filepath.Join("/tmp", "herdrex-"+hex.EncodeToString(digest[:8]))
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", nil, fmt.Errorf("create local SSH socket directory: %w", err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", nil, fmt.Errorf("inspect local SSH socket directory: %w", err)
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || !ok || owner.Uid != uint32(os.Getuid()) || info.Mode().Perm()&0o077 != 0 {
		return "", nil, fmt.Errorf("local SSH socket directory %s is not a private directory owned by this user", dir)
	}
	lock, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return "", nil, fmt.Errorf("open local SSH socket lock: %w", err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return "", nil, fmt.Errorf("an SSH bridge for %s is already running", host)
	}
	unlock := func() {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	}
	for _, path := range []string{filepath.Join(dir, "herdr.sock"), filepath.Join(dir, "herdr-client.sock")} {
		info, err := os.Lstat(path)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			unlock()
			return "", nil, fmt.Errorf("inspect stale socket %s: %w", path, err)
		}
		if info.Mode()&os.ModeSocket == 0 {
			unlock()
			return "", nil, fmt.Errorf("refusing to replace non-socket at %s", path)
		}
		if err := os.Remove(path); err != nil {
			unlock()
			return "", nil, fmt.Errorf("remove stale socket %s: %w", path, err)
		}
	}
	return dir, unlock, nil
}

func listenSSHAddrs(start int, auto bool, lanIPs []netip.Addr, ts bool) ([]net.Listener, error) {
	if start < 1 || start > 65535 {
		return nil, fmt.Errorf("--port %d: want 1 to 65535", start)
	}
	last := start
	if auto {
		last = 65535
	}
	ip := netip.MustParseAddr("127.0.0.1")
	for port := start; port <= last; port++ {
		addrs, err := listenSelectedAddrs(netip.AddrPortFrom(ip, uint16(port)), lanIPs, ts)
		if err != nil {
			return nil, err
		}
		listeners, _, err := listenAll(addrs)
		if err == nil {
			return listeners, nil
		}
		if !auto || !errors.Is(err, syscall.EADDRINUSE) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("no available loopback port from %d through %d", start, last)
}

func waitForSocket(ctx context.Context, path string, tunnelDone <-chan struct{}, tunnelErr *error) error {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		info, err := os.Lstat(path)
		if err == nil && info.Mode()&os.ModeSocket != 0 {
			return nil
		}
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		select {
		case <-tunnelDone:
			if *tunnelErr == nil {
				return errors.New("SSH exited before creating the socket forward")
			}
			return *tunnelErr
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// signalContext is split out so the ssh subcommand can shut down both the
// tunnel and the bridge on the same interrupt.
func signalContext(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
}
