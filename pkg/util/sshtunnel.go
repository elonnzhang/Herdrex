package util

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Endpoint is one Unix socket to reach on a remote host: Local is the path of
// the socket to serve here. What Remote is depends on the transport: the path
// of the remote socket for Forward and Mux, the command that bridges a
// connection to it over stdio for Stdio.
type Endpoint struct{ Local, Remote string }

// Tunnel is a running way of reaching remote Unix sockets through local ones:
// an ssh -L forward, a one-session mux, or a stdio proxy. Done closes when it
// ends by itself; Err then says why.
type Tunnel struct {
	mode string
	done chan struct{}
	err  error
	stop func()
}

// Mode names the transport: "forward", "mux" or "stdio".
func (t *Tunnel) Mode() string { return t.mode }

// Done is closed when the tunnel has ended.
func (t *Tunnel) Done() <-chan struct{} { return t.done }

// Err says why the tunnel ended; read it after Done is closed.
func (t *Tunnel) Err() error { return t.err }

// Stop ends the tunnel.
func (t *Tunnel) Stop() { t.stop() }

// ReadyLine is the line a remote bridge prints before it speaks its protocol,
// the one Herdr's own SSH client waits for (src/remote/attach.rs), so that
// whatever a login shell prints first is dropped.
const ReadyLine = "herdr-remote-output-ready:1"

// Stdio listens on each endpoint's local socket and, for each connection, runs
// the endpoint's remote command over ssh and copies bytes both ways, after the
// command has printed ReadyLine. ssh shares one authenticated connection
// through ControlMaster.
func Stdio(ctx context.Context, sshPath string, base []string, host string, eps []Endpoint) (*Tunnel, error) {
	remote := func(cmd string) string {
		return "printf '\\n%s\\n' " + ShellQuote(ReadyLine) + "; exec " + cmd
	}
	ctx, cancel := context.WithCancel(ctx)
	t := &Tunnel{mode: "stdio", done: make(chan struct{})}
	var once sync.Once
	fail := func(err error) {
		once.Do(func() { t.err = err; close(t.done) })
	}
	var lns []net.Listener
	for _, ep := range eps {
		os.Remove(ep.Local)
		ln, err := net.Listen("unix", ep.Local)
		if err != nil {
			cancel()
			for _, l := range lns {
				l.Close()
			}
			return nil, err
		}
		lns = append(lns, ln)
		args := append(append([]string{}, base...), "-T", host, remote(ep.Remote))
		go func() {
			for {
				c, err := ln.Accept()
				if err != nil {
					if ctx.Err() == nil {
						fail(err)
					}
					return
				}
				go proxyConn(ctx, c, sshPath, args)
			}
		}()
	}
	t.stop = func() {
		cancel()
		for _, l := range lns {
			l.Close()
		}
		fail(nil)
	}
	return t, nil
}

// proxyConn joins one local connection to one ssh process running a remote
// bridge, after dropping what the remote shell printed before ReadyLine.
func proxyConn(ctx context.Context, c net.Conn, sshPath string, args []string) {
	defer c.Close()
	cmd := exec.CommandContext(ctx, sshPath, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Start(); err != nil {
		log.Printf("ssh: %v", err)
		return
	}
	defer func() {
		cmd.Process.Kill()
		cmd.Wait()
	}()
	r := bufio.NewReader(stdout)
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			log.Printf("ssh: remote bridge ended before it was ready: %v %s", err, strings.TrimSpace(stderr.String()))
			return
		}
		if strings.TrimRight(line, "\r\n") == ReadyLine {
			break
		}
	}
	done := make(chan struct{}, 2)
	go func() { io.Copy(stdin, c); stdin.Close(); done <- struct{}{} }()
	go func() { io.Copy(c, r); done <- struct{}{} }()
	<-done
}

// Forward asks the SSH control master, which an earlier ssh command opened,
// to forward the endpoints' sockets (ssh -O forward). A plain ssh -N would only hand
// the forwards to that master and exit, so the master is the tunnel: done
// closes when ssh -O check stops finding it.
func Forward(ctx context.Context, sshPath string, base []string, host string, eps []Endpoint) (*Tunnel, error) {
	fwd := []string{"-o", "StreamLocalBindUnlink=yes"}
	for _, ep := range eps {
		fwd = append(fwd, "-L", ep.Local+":"+ep.Remote)
	}
	ctl := func(op string, extra ...string) *exec.Cmd {
		args := append(append([]string{}, base...), extra...)
		return exec.Command(sshPath, append(args, "-O", op, host)...)
	}
	if out, err := ctl("forward", fwd...).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("ssh -O forward: %v %s", err, strings.TrimSpace(string(out)))
	}
	t := &Tunnel{mode: "forward", done: make(chan struct{})}
	ctx, cancel := context.WithCancel(ctx)
	var once sync.Once
	finish := func(err error) { once.Do(func() { t.err = err; close(t.done) }) }
	go func() {
		tick := time.NewTicker(5 * time.Second)
		defer tick.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-tick.C:
				if out, err := ctl("check").CombinedOutput(); err != nil && ctx.Err() == nil {
					finish(fmt.Errorf("SSH connection lost: %s", strings.TrimSpace(string(out))))
					return
				}
			}
		}
	}()
	t.stop = func() {
		cancel()
		ctl("cancel", fwd...).Run()
		finish(nil)
	}
	for _, ep := range eps {
		if err := WaitForSocket(ctx, ep.Local, t.done, &t.err); err != nil {
			t.stop()
			return nil, err
		}
	}
	return t, nil
}
