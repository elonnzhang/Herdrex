package main

import (
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/spf13/cobra"
)

func TestTailscale(t *testing.T) {
	for _, tc := range []struct {
		addr string
		want bool
	}{
		{"100.64.0.1", true},
		{"100.122.109.110", true},
		{"100.127.255.255", true},
		{"100.128.0.1", false},
		{"100.63.255.255", false},
		{"192.168.1.2", false},
		{"0.0.0.0", false},
		{"fd7a:115c:a1e0::202c:6d6f", true},
		{"fd7a:115c:a1e1::1", false},
		{"::ffff:100.100.1.1", true},
		{"::", false},
	} {
		if got := tailscale(netip.MustParseAddr(tc.addr)); got != tc.want {
			t.Errorf("tailscale(%s) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

func TestAddHostFlag(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		want    string
		wantErr bool
	}{
		{args: nil, want: ""},
		{args: []string{"--add-host"}, want: defaultHost},
		{args: []string{"--add-host=studio"}, want: "studio"},
		{args: []string{"--add-host", "studio"}, wantErr: true}, // the name needs =
	} {
		cmd := command()
		cmd.SetArgs(tc.args)
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		var got string
		cmd.RunE = func(cmd *cobra.Command, _ []string) error {
			got, _ = cmd.Flags().GetString("add-host")
			return nil
		}
		err := cmd.Execute()
		if (err != nil) != tc.wantErr || got != tc.want {
			t.Errorf("%v: %q, error %v; want %q, error %v", tc.args, got, err, tc.want, tc.wantErr)
		}
	}
}

// A --daemon child finds its ready descriptor in the environment, and
// takes it out so that what it starts does not see it.
func TestDaemonReady(t *testing.T) {
	t.Setenv(readyEnv, "")
	os.Unsetenv(readyEnv)
	if f := daemonReady(); f != nil {
		t.Fatalf("daemonReady() = %v outside a --daemon child", f)
	}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	t.Setenv(readyEnv, strconv.Itoa(int(w.Fd())))
	f := daemonReady()
	if f == nil {
		t.Fatal("daemonReady() = nil in a --daemon child")
	}
	if _, ok := os.LookupEnv(readyEnv); ok {
		t.Errorf("%s still set", readyEnv)
	}
	f.WriteString(readyMsg)
	f.Close()
	if msg, _ := io.ReadAll(r); string(msg) != readyMsg {
		t.Errorf("read %q, want %q", msg, readyMsg)
	}
}

// A start that fails must leave the socket alone: it may be another
// server's. Only the directories the start made go.
func TestStartHerdrFailureKeepsSocket(t *testing.T) {
	t.Setenv("HERDR_BIN_PATH", filepath.Join(t.TempDir(), "no-herdr"))
	dir := filepath.Join(t.TempDir(), "run")
	if err := os.Mkdir(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	sock := filepath.Join(dir, "x.sock")
	for _, f := range []string{sock, filepath.Join(dir, "x-client.sock")} {
		if err := os.WriteFile(f, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := startHerdr(t.Context(), sock); err == nil {
		t.Fatal("startHerdr with no herdr binary succeeded")
	}
	for _, f := range []string{sock, filepath.Join(dir, "x-client.sock")} {
		if _, err := os.Stat(f); err != nil {
			t.Errorf("%s: %v; a failed start must keep it", f, err)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "x.d")); !os.IsNotExist(err) {
		t.Errorf("x.d is still there (%v); the failed start made it", err)
	}
}

// clean, after the server stops, removes its sockets and the directories
// the run made, but not a directory that was there before.
func TestHerdrServerClean(t *testing.T) {
	base := t.TempDir()
	dir := filepath.Join(base, "x.d")
	sock := filepath.Join(base, "x.sock")
	for _, d := range []string{filepath.Join(dir, "config")} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{sock, filepath.Join(base, "x-client.sock"), filepath.Join(base, "keep")} {
		if err := os.WriteFile(f, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	h := &herdrServer{
		sock: sock,
		dir:  dir,
		made: []string{base, dir}, // base is not empty: it holds keep
	}
	if err := h.clean(); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(base)
	if len(entries) != 1 || entries[0].Name() != "keep" {
		t.Errorf("left %v, want only keep", entries)
	}
}
