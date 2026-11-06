package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"os/exec"
	"strings"
	"syscall"
	"time"

	"github.com/elonnzhang/herdrex/pkg/herdr"
	"github.com/elonnzhang/herdrex/pkg/util"
)

// remoteStarted is the line the start script prints once it has checked that
// it may start a server and has started one.
const remoteStarted = "herdrex-remote-started"

// remoteStartScript returns a sh script that runs a Herdr server on the remote
// host and cleans up after it. Its arguments are the API socket, the directory
// for the server's config and state (x.sock: x.d), and the client socket.
//
// The server runs in the foreground of the SSH session's script, and ends with
// it: when the SSH connection drops or ssh closes the script's stdin, a
// background cat sees the end of input and stops the server. The script then
// removes the sockets, and the directories it made. As startHerdr does, it
// leaves the sockets alone when the server failed rather than stopped: they
// may be another server's.
//
// Like pkg/herdr/cli.StartServer, it starts nothing that could touch the
// user's own Herdr: the socket and the new config directory must not overlap
// the user's config directory, no Herdr variable from the remote environment
// is passed on, and no server may already run at the socket. Symbolic links
// are not resolved here.
func remoteStartScript(dirs []string) string {
	return remoteHerdrPrelude(dirs) + `
sock=$1; dir=$2; client=$3
unset HERDR_SOCKET_PATH HERDR_CLIENT_SOCKET_PATH HERDR_CONFIG_PATH HERDR_SESSION
umask 077
refuse() { echo "refusing to start a Herdr server: $1" >&2; exit 2; }
for u in "$HOME/.config/herdr" "${XDG_CONFIG_HOME:-$HOME/.config}/herdr"; do
  case "$sock/" in "$u"/*) refuse "$sock is in your Herdr config directory $u";; esac
  case "$dir/" in "$u"/*) refuse "$dir is in your Herdr config directory $u";; esac
  case "$u/" in "$dir"/*) refuse "$dir contains your Herdr config directory $u";; esac
done
HERDR_SOCKET_PATH=$sock herdr status --json 2>/dev/null | grep -q '"running": *true' &&
  refuse "a Herdr server is already running at $sock"
parent=$(dirname -- "$sock"); pmade=; dmade=; code=1
[ -d "$parent" ] || pmade=1
[ -d "$dir" ] || dmade=1
mkdir -p -- "$parent" "$dir/config" "$dir/state" || exit 1
HERDR_SOCKET_PATH=$sock XDG_CONFIG_HOME=$dir/config XDG_STATE_HOME=$dir/state herdr server &
pid=$!
trap 'kill "$pid" 2>/dev/null' HUP INT TERM
exec 3<&0
(cat <&3; kill "$pid" 2>/dev/null) >/dev/null 2>&1 &
watch=$!
echo ` + remoteStarted + `
while kill -0 "$pid" 2>/dev/null; do wait "$pid"; code=$?; done
kill "$watch" 2>/dev/null
case $code in 0|143) rm -f -- "$sock" "$client" "$sock.agent";; esac
[ -z "$dmade" ] || rm -rf -- "$dir"
[ -z "$pmade" ] || rmdir -- "$parent" 2>/dev/null
exit $code
`
}

// remoteHerdr is a Herdr server that --start or --new started on the remote
// host, held by an ssh session running remoteStartScript.
type remoteHerdr struct {
	host  string
	sock  string
	cmd   *exec.Cmd
	stdin io.WriteCloser
	out   *util.TailWriter
	done  chan struct{} // closed when ssh has exited
	err   error         // from cmd.Wait; read after done is closed
}

// startRemoteHerdr starts a Herdr server at sock on the remote host and
// returns once herdr status there reports it running.
func startRemoteHerdr(ctx context.Context, sshPath string, base []string, host, sock string) (*remoteHerdr, error) {
	dir := herdrDir(sock)
	args := append(append([]string{}, base...), "-T", host,
		util.RemoteCommand(remoteStartScript(remoteHerdrDirs), sock, dir, herdr.ClientSocket(sock)))
	h := &remoteHerdr{host: host, sock: sock, done: make(chan struct{}), out: util.NewTailWriter(remoteStarted)}
	// Not CommandContext, and its own process group: Ctrl+C reaches herdrex,
	// which ends the server by closing the session's stdin in stop.
	h.cmd = exec.Command(sshPath, args...)
	h.cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	h.cmd.Stdout, h.cmd.Stderr = h.out, h.out
	var err error
	if h.stdin, err = h.cmd.StdinPipe(); err != nil {
		return nil, err
	}
	if err := h.cmd.Start(); err != nil {
		return nil, err
	}
	go func() {
		h.err = h.cmd.Wait()
		close(h.done)
	}()
	if err := h.waitRunning(ctx, sshPath, base); err != nil {
		h.stop()
		return nil, fmt.Errorf("start Herdr on %s at %s: %w", host, sock, err)
	}
	log.Printf("herdrex ssh: started a Herdr server on %s at %s (config and state in %s)", host, sock, dir)
	return h, nil
}

// waitRunning waits for the script to start the server, then for herdr status
// to report it, for up to 30s. It gives up as soon as ssh exits.
func (h *remoteHerdr) waitRunning(ctx context.Context, sshPath string, base []string) error {
	ctx, cancel := context.WithTimeoutCause(ctx, 30*time.Second, errors.New("not running after 30s"))
	defer cancel()
	select {
	case <-h.out.Marker():
	case <-h.done:
		return h.exited()
	case <-ctx.Done():
		return context.Cause(ctx)
	}
	tick := time.NewTicker(300 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-h.done:
			return h.exited()
		case <-ctx.Done():
			return context.Cause(ctx)
		case <-tick.C:
		}
		st, err := remoteStatus(ctx, sshPath, base, h.host, h.sock, io.Discard)
		if err == nil && st.Server.Running {
			return nil
		}
	}
}

// exited says why the ssh session ended, with the last of its output.
func (h *remoteHerdr) exited() error {
	msg := strings.TrimSpace(strings.ReplaceAll(h.out.String(), remoteStarted, ""))
	if msg == "" {
		return fmt.Errorf("the SSH session ended: %v", h.err)
	}
	return fmt.Errorf("the SSH session ended (%v): %s", h.err, msg)
}

// stop ends the server by closing the session's stdin, which the script reads
// as the signal to stop it and clean up, and waits for ssh to exit.
func (h *remoteHerdr) stop() {
	h.stdin.Close()
	select {
	case <-h.done:
	case <-time.After(30 * time.Second):
		h.cmd.Process.Kill()
		<-h.done
		log.Printf("herdrex ssh: the Herdr server on %s at %s did not stop in 30s; ssh killed, so the server and its files may remain", h.host, h.sock)
		return
	}
	log.Printf("herdrex ssh: stopped the Herdr server on %s at %s", h.host, h.sock)
}
