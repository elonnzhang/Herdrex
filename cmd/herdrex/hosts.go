package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"
)

// defaultHost is the host name --add-host gives the bridge.
const defaultHost = "herdr"

// hostChange is what addHost did to the Rex hosts, so that undo can put
// them back as they were.
type hostChange struct {
	label  string
	added  bool     // the host did not exist
	before []string // the endpoints it had, when addHost replaced them
}

// addHost makes the Rex host called name reach url. It adds the host, or,
// when one of that name (ignoring case, as rex does) has other endpoints,
// points it at url alone.
func addHost(ctx context.Context, name, url string) (hostChange, error) {
	out, err := rex(ctx, "hosts", "list", "--json")
	if err != nil {
		return hostChange{}, err
	}
	var list struct {
		Hosts []struct {
			Host struct {
				Label     string   `json:"label"`
				Endpoints []string `json:"endpoints"`
			} `json:"host"`
		} `json:"hosts"`
	}
	if err := json.Unmarshal(out, &list); err != nil {
		return hostChange{}, fmt.Errorf("rex hosts list: %w", err)
	}
	for _, h := range list.Hosts {
		if !strings.EqualFold(h.Host.Label, name) {
			continue
		}
		c := hostChange{label: h.Host.Label}
		if slices.Equal(h.Host.Endpoints, []string{url}) {
			log.Printf("rex host %s already reaches %s", c.label, url)
			return c, nil
		}
		if _, err := rex(ctx, "hosts", "edit", c.label, "--endpoint", url); err != nil {
			return hostChange{}, err
		}
		c.before = h.Host.Endpoints
		log.Printf("rex host %s now reaches %s (was %s)", c.label, url, strings.Join(c.before, " "))
		return c, nil
	}
	if _, err := rex(ctx, "hosts", "add", name, url); err != nil {
		return hostChange{}, err
	}
	log.Printf("added rex host %s at %s", name, url)
	return hostChange{label: name, added: true}, nil
}

// undo puts the Rex hosts back as they were before addHost: it removes a
// host addHost added and gives back the endpoints of one it changed.
func (c hostChange) undo(ctx context.Context) error {
	switch {
	case c.added:
		if _, err := rex(ctx, "hosts", "remove", c.label); err != nil {
			return err
		}
		log.Printf("removed rex host %s", c.label)
	case len(c.before) > 0:
		args := []string{"hosts", "edit", c.label}
		for _, e := range c.before {
			args = append(args, "--endpoint", e)
		}
		if _, err := rex(ctx, args...); err != nil {
			return err
		}
		log.Printf("rex host %s reaches %s again", c.label, strings.Join(c.before, " "))
	}
	return nil
}

// rex runs the rex command line and returns its output, or an error that
// carries what it printed on stderr.
func rex(ctx context.Context, args ...string) ([]byte, error) {
	bin, err := rexBin()
	if err != nil {
		return nil, err
	}
	out, err := exec.CommandContext(ctx, bin, args...).Output()
	if ee, ok := errors.AsType[*exec.ExitError](err); ok {
		return nil, fmt.Errorf("rex %s: %v: %s", strings.Join(args, " "), err, strings.TrimSpace(string(ee.Stderr)))
	}
	if err != nil {
		return nil, fmt.Errorf("rex %s: %w", strings.Join(args, " "), err)
	}
	return out, nil
}

// rexBin finds the rex command line once: rex on PATH, else where Rex.app
// installs it (rexPaths). PATH is often without it outside a Rex terminal,
// and Homebrew's rex is another program, so each candidate must answer
// rex version as Rex's does.
var rexBin = sync.OnceValues(func() (string, error) {
	var tried []string
	cands := rexPaths()
	onPath, err := exec.LookPath("rex")
	if err == nil {
		cands = append([]string{onPath}, cands...)
	}
	for _, p := range cands {
		if _, err := os.Stat(p); err != nil {
			continue
		}
		if isRex(p) {
			if p != onPath {
				log.Printf("using rex at %s", p)
			}
			return p, nil
		}
		tried = append(tried, p)
	}
	if len(tried) > 0 {
		return "", fmt.Errorf("found no Rex command line; %s is not Rex's rex", strings.Join(tried, ", "))
	}
	return "", errors.New("found no rex command line on PATH or in Rex.app; install Rex.app or add its rex to PATH")
})

// rexPaths are where Rex.app keeps its rex: the server it installs in
// Application Support, and the helper inside the app bundle.
func rexPaths() []string {
	const helper = "Contents/Helpers/Rex Server.rex/Contents/MacOS/rex"
	var dirs []string
	if home, err := os.UserHomeDir(); err == nil {
		dirs = append(dirs, filepath.Join(home, "Library/Application Support/com.superlogical.rex/RexServer/Versions/Current/Rex Server.app/Contents/MacOS/rex"))
		for _, app := range []string{"Rex.app", "Rex Beta.app"} {
			dirs = append(dirs, filepath.Join(home, "Applications", app, helper))
		}
	}
	for _, app := range []string{"Rex.app", "Rex Beta.app"} {
		dirs = append(dirs, filepath.Join("/Applications", app, helper))
	}
	return dirs
}

// isRex reports whether bin is Rex's rex: its rex version prints "rex ".
func isRex(bin string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "version").Output()
	return err == nil && strings.HasPrefix(string(out), "rex ")
}
