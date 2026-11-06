package main

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

	"github.com/elonnzhang/herdrex/pkg/herdr/jsonapi"
)

// sshTransport is a running way of reaching the remote Herdr through two
// local sockets: an ssh -L forward, or a stdio proxy. done closes when it
// ends by itself; err then says why.
type sshTransport struct {
	mode string
	done chan struct{}
	err  error
	stop func()
}

// remoteReady is the line Herdr's own SSH client prints before exec'ing a
// bridge, so that login shell output before it is dropped
// (src/remote/attach.rs).
const remoteReady = "herdr-remote-output-ready:1"

// probeSocket checks that a forwarded API socket really reaches Herdr: some
// SSH relays accept -L but refuse every direct-streamlocal channel.
func probeSocket(ctx context.Context, sock string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	_, err := (&jsonapi.Client{Socket: sock}).Ping(ctx)
	return err
}

// startStdioProxy listens on the two local sockets and, for each connection,
// runs ssh with remote-api-bridge or remote-client-bridge and copies bytes
// both ways. ssh shares one authenticated connection through ControlMaster.
func startStdioProxy(ctx context.Context, sshPath string, base []string, host, binary, session, apiSock, clientSock string) (*sshTransport, error) {
	remote := func(sub string) string {
		cmd := "printf '\\n%s\\n' " + shellQuote(remoteReady) + "; exec " + shellQuote(binary)
		if session != "" {
			cmd += " --session " + shellQuote(session)
		}
		return cmd + " " + sub
	}
	ctx, cancel := context.WithCancel(ctx)
	t := &sshTransport{mode: "stdio", done: make(chan struct{})}
	var once sync.Once
	fail := func(err error) {
		once.Do(func() { t.err = err; close(t.done) })
	}
	var lns []net.Listener
	for _, p := range []struct{ sock, sub string }{
		{apiSock, "remote-api-bridge"},
		{clientSock, "remote-client-bridge"},
	} {
		os.Remove(p.sock)
		ln, err := net.Listen("unix", p.sock)
		if err != nil {
			cancel()
			for _, l := range lns {
				l.Close()
			}
			return nil, err
		}
		lns = append(lns, ln)
		args := append(append([]string{}, base...), "-T", host, remote(p.sub))
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

// proxyConn joins one local connection to one ssh process running a Herdr
// bridge, after dropping what the remote shell printed before remoteReady.
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
		log.Printf("herdrex ssh: %v", err)
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
			log.Printf("herdrex ssh: remote bridge ended before it was ready: %v %s", err, strings.TrimSpace(stderr.String()))
			return
		}
		if strings.TrimRight(line, "\r\n") == remoteReady {
			break
		}
	}
	done := make(chan struct{}, 2)
	go func() { io.Copy(stdin, c); stdin.Close(); done <- struct{}{} }()
	go func() { io.Copy(c, r); done <- struct{}{} }()
	<-done
}

// shellQuote quotes s for a POSIX shell.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// startForward asks the SSH control master, which the status probe opened,
// to forward the two sockets (ssh -O forward). A plain ssh -N would only hand
// the forwards to that master and exit, so the master is the tunnel: done
// closes when ssh -O check stops finding it.
func startForward(ctx context.Context, sshPath string, base []string, host, apiSock, clientSock, remoteAPI, remoteClient string) (*sshTransport, error) {
	fwd := []string{
		"-o", "StreamLocalBindUnlink=yes",
		"-L", apiSock + ":" + remoteAPI,
		"-L", clientSock + ":" + remoteClient,
	}
	ctl := func(op string, extra ...string) *exec.Cmd {
		args := append(append([]string{}, base...), extra...)
		return exec.Command(sshPath, append(args, "-O", op, host)...)
	}
	if out, err := ctl("forward", fwd...).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("ssh -O forward: %v %s", err, strings.TrimSpace(string(out)))
	}
	t := &sshTransport{mode: "forward", done: make(chan struct{})}
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
	for _, sock := range []string{apiSock, clientSock} {
		if err := waitForSocket(ctx, sock, t.done, &t.err); err != nil {
			t.stop()
			return nil, err
		}
	}
	return t, nil
}
