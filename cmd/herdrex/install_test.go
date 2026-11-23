package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// installRoot is a scratch directory under /tmp for one install.sh run. It
// holds release/ (what HERDREX_DOWNLOAD_BASE points at), home/ and state/.
func installRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "hxinst")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(root) })
	for _, d := range []string{"release", "home", "state"} {
		if err := os.Mkdir(filepath.Join(root, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	return root
}

// fakeHerdrex is a herdrex that answers --version as cobra does.
func fakeHerdrex(version string) []byte {
	return []byte("#!/bin/sh\necho \"herdrex version " + version + "\"\n")
}

// writeExe writes an executable file at name, making its directory.
func writeExe(t *testing.T, name string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(name), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(name, data, 0o755); err != nil {
		t.Fatal(err)
	}
}

// publish puts the archive install.sh downloads for version on this machine,
// and its checksums.txt, in root/release.
func publish(t *testing.T, root, version string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	bin := fakeHerdrex(version)
	if err := tw.WriteHeader(&tar.Header{Name: "herdrex", Mode: 0o755, Size: int64(len(bin))}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(bin); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	asset := fmt.Sprintf("herdrex_%s_%s_%s.tar.gz", version, runtime.GOOS, runtime.GOARCH)
	release := filepath.Join(root, "release")
	if err := os.WriteFile(filepath.Join(release, asset), buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := fmt.Sprintf("%x  %s\n", sha256.Sum256(buf.Bytes()), asset)
	if err := os.WriteFile(filepath.Join(release, "checksums.txt"), []byte(sum), 0o644); err != nil {
		t.Fatal(err)
	}
}

// runInstall runs ../../install.sh with HOME, the state directory and the
// download base inside root, PATH set to path and the system directories,
// and env added. It returns the combined output.
func runInstall(t *testing.T, root, path string, env ...string) (string, error) {
	t.Helper()
	script, err := filepath.Abs(filepath.Join("..", "..", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("sh", script)
	cmd.Dir = root
	cmd.Env = append([]string{
		"HOME=" + filepath.Join(root, "home"),
		"XDG_STATE_HOME=" + filepath.Join(root, "state"),
		"PATH=" + path + ":/usr/bin:/bin",
		"HERDREX_DOWNLOAD_BASE=file://" + filepath.Join(root, "release"),
	}, env...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

// versionOf runs exe --version.
func versionOf(t *testing.T, exe string) string {
	t.Helper()
	out, err := exec.Command(exe, "--version").Output()
	if err != nil {
		t.Fatalf("%s --version: %v", exe, err)
	}
	return strings.TrimSpace(string(out))
}

func TestInstallReplacesAnotherVersionThatContainsTheWanted(t *testing.T) {
	root := installRoot(t)
	dir := filepath.Join(root, "bin")
	writeExe(t, filepath.Join(dir, "herdrex"), fakeHerdrex("v0.1.10"))
	publish(t, root, "v0.1.1")
	out, err := runInstall(t, root, dir, "HERDREX_VERSION=v0.1.1", "HERDREX_INSTALL_DIR="+dir)
	if err != nil {
		t.Fatalf("install.sh: %v\n%s", err, out)
	}
	if got := versionOf(t, filepath.Join(dir, "herdrex")); got != "herdrex version v0.1.1" {
		t.Fatalf("installed %q, want v0.1.1\n%s", got, out)
	}
}

func TestInstallSkipsTheSameVersion(t *testing.T) {
	root := installRoot(t)
	dir := filepath.Join(root, "bin")
	writeExe(t, filepath.Join(dir, "herdrex"), fakeHerdrex("v0.1.1"))
	// Nothing published: a download would fail.
	out, err := runInstall(t, root, dir, "HERDREX_VERSION=v0.1.1", "HERDREX_INSTALL_DIR="+dir)
	if err != nil {
		t.Fatalf("install.sh: %v\n%s", err, out)
	}
	if !strings.Contains(out, "already installed") {
		t.Fatalf("output %q, want it to say v0.1.1 is already installed", out)
	}
}

// brewInstall lays out a Homebrew install of herdrex under root: the binary in
// the Cellar and a relative symlink to it in root/bin, as brew links them.
func brewInstall(t *testing.T, root string) string {
	t.Helper()
	writeExe(t, filepath.Join(root, "Cellar", "herdrex", "0.1.0", "bin", "herdrex"), fakeHerdrex("v0.1.0"))
	bin := filepath.Join(root, "bin")
	if err := os.Mkdir(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../Cellar/herdrex/0.1.0/bin/herdrex", filepath.Join(bin, "herdrex")); err != nil {
		t.Fatal(err)
	}
	return bin
}

func TestInstallLeavesHomebrewsHerdrex(t *testing.T) {
	for name, env := range map[string]func(bin string) []string{
		"found on PATH":       func(string) []string { return nil },
		"HERDREX_INSTALL_DIR": func(bin string) []string { return []string{"HERDREX_INSTALL_DIR=" + bin} },
	} {
		t.Run(name, func(t *testing.T) {
			root := installRoot(t)
			bin := brewInstall(t, root)
			publish(t, root, "v0.1.1")
			out, err := runInstall(t, root, bin, append(env(bin), "HERDREX_VERSION=v0.1.1")...)
			if err == nil {
				t.Errorf("install.sh succeeded, want it to refuse a Homebrew install\n%s", out)
			}
			if !strings.Contains(out, "brew upgrade") {
				t.Errorf("output %q, want it to say to run brew upgrade", out)
			}
			if info, err := os.Lstat(filepath.Join(bin, "herdrex")); err != nil || info.Mode()&os.ModeSymlink == 0 {
				t.Errorf("Homebrew's symlink was replaced: %v %v", info.Mode(), err)
			}
		})
	}
}

func TestInstallBesideHomebrewInAnotherDirectory(t *testing.T) {
	root := installRoot(t)
	bin := brewInstall(t, root)
	other := filepath.Join(root, "other")
	publish(t, root, "v0.1.1")
	out, err := runInstall(t, root, bin, "HERDREX_VERSION=v0.1.1", "HERDREX_INSTALL_DIR="+other)
	if err != nil {
		t.Fatalf("install.sh: %v\n%s", err, out)
	}
	if got := versionOf(t, filepath.Join(other, "herdrex")); got != "herdrex version v0.1.1" {
		t.Fatalf("installed %q, want v0.1.1", got)
	}
	if got := versionOf(t, filepath.Join(bin, "herdrex")); got != "herdrex version v0.1.0" {
		t.Fatalf("Homebrew's herdrex is now %q, want it untouched", got)
	}
}
