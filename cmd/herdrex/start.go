package main

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/elonnzhang/herdrex/pkg/herdr"
	"github.com/elonnzhang/herdrex/pkg/herdr/cli"
)

// herdrServer is a Herdr server that --start started for this run.
type herdrServer struct {
	srv  *cli.Server
	sock string
	dir  string   // its config and state
	made []string // the directories this run made, innermost last
}

// startHerdr starts a Herdr server whose API socket is sock, with its config
// and state in sock's name with .d for its extension, such as
// /tmp/herdr/x.sock and /tmp/herdr/x.d. pkg/herdr/cli refuses to start one
// that could touch your own Herdr's config or socket.
func startHerdr(ctx context.Context, sock string) (*herdrServer, error) {
	dir := strings.TrimSuffix(sock, filepath.Ext(sock)) + ".d"
	h := &herdrServer{sock: sock, dir: dir}
	for _, d := range []string{filepath.Dir(sock), dir} {
		if _, err := os.Stat(d); os.IsNotExist(err) {
			h.made = append(h.made, d)
		}
	}
	for _, d := range []string{filepath.Join(dir, "config"), filepath.Join(dir, "state")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return nil, err
		}
	}
	c := &cli.CLI{Env: []string{
		"HERDR_SOCKET_PATH=" + sock,
		"XDG_CONFIG_HOME=" + filepath.Join(dir, "config"),
		"XDG_STATE_HOME=" + filepath.Join(dir, "state"),
	}}
	srv, err := c.StartServer(ctx)
	if err != nil {
		// Only the directories: the sockets may be another server's.
		h.removeDirs()
		return nil, err
	}
	h.srv = srv
	log.Printf("started herdr server at %s (config and state in %s)", sock, dir)
	return h, nil
}

// stop stops the server, and with clean, removes the sockets it leaves and
// the directories this run made.
func (h *herdrServer) stop(ctx context.Context, clean bool) error {
	if err := h.srv.Stop(ctx); err != nil {
		return fmt.Errorf("stop herdr server: %w", err)
	}
	log.Printf("stopped herdr server")
	if clean {
		return h.clean()
	}
	return nil
}

// clean removes, once the server has stopped, the sockets it leaves and the
// directories this run made.
func (h *herdrServer) clean() error {
	var errs []error
	for _, f := range []string{h.sock, herdr.ClientSocket(h.sock), h.sock + ".agent"} {
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
			errs = append(errs, err)
		}
	}
	return errors.Join(append(errs, h.removeDirs())...)
}

// removeDirs removes the directories this run made. It leaves one that
// held something before; the parent of the socket only goes when nothing
// else is left in it.
func (h *herdrServer) removeDirs() error {
	var errs []error
	for _, d := range slices.Backward(h.made) {
		var err error
		if d == h.dir {
			err = os.RemoveAll(d)
		} else {
			err = os.Remove(d) // fails, as it should, when not empty
		}
		if err == nil {
			log.Printf("removed %s", d)
		} else if !os.IsNotExist(err) && d == h.dir {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
