package util

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ShellQuote quotes s for a POSIX shell.
func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// RemoteCommand is the command line that runs script with sh on a remote
// host, with args as $1, $2 and so on. ssh joins its arguments with spaces and
// hands them to the remote login shell, so each part is quoted for it here.
func RemoteCommand(script string, args ...string) string {
	parts := []string{"sh", "-c", ShellQuote(script), "sh"}
	for _, a := range args {
		parts = append(parts, ShellQuote(a))
	}
	return strings.Join(parts, " ")
}

// SignalContext returns a context that ends on Ctrl+C or SIGTERM.
func SignalContext(parent context.Context) (context.Context, context.CancelFunc) {
	return signal.NotifyContext(parent, os.Interrupt, syscall.SIGTERM)
}

// ErrLocked is returned by LockDir when another process holds the directory.
var ErrLocked = errors.New("directory is locked by another process")

// LockDir returns a private directory under /tmp for the one process that
// holds it, named prefix and a digest of seed and the user, and a function
// that releases it. The directory is made with mode 0700 and must be owned by
// this user. LockDir takes an exclusive lock on its "lock" file, or returns
// ErrLocked, and removes a stale socket left at each of sockets (names in the
// directory), refusing to replace anything that is not a socket.
func LockDir(prefix, seed string, sockets ...string) (string, func(), error) {
	digest := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s", os.Getuid(), seed)))
	dir := filepath.Join("/tmp", prefix+hex.EncodeToString(digest[:8]))
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", nil, fmt.Errorf("create %s: %w", dir, err)
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return "", nil, fmt.Errorf("inspect %s: %w", dir, err)
	}
	owner, ok := info.Sys().(*syscall.Stat_t)
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() || !ok || owner.Uid != uint32(os.Getuid()) || info.Mode().Perm()&0o077 != 0 {
		return "", nil, fmt.Errorf("%s is not a private directory owned by this user", dir)
	}
	lock, err := os.OpenFile(filepath.Join(dir, "lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return "", nil, fmt.Errorf("open lock in %s: %w", dir, err)
	}
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		lock.Close()
		return "", nil, ErrLocked
	}
	unlock := func() {
		_ = syscall.Flock(int(lock.Fd()), syscall.LOCK_UN)
		_ = lock.Close()
	}
	for _, name := range sockets {
		path := filepath.Join(dir, name)
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

// WaitForSocket waits until a Unix socket exists at path. It gives up when
// done closes, with *doneErr if that is set, or when ctx ends.
func WaitForSocket(ctx context.Context, path string, done <-chan struct{}, doneErr *error) error {
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
		case <-done:
			if *doneErr == nil {
				return errors.New("SSH exited before creating the socket forward")
			}
			return *doneErr
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

// TailWriter keeps the last 4 KiB written to it, and closes the channel
// Marker returns once the text it was made with has been written. The marker
// is looked for in the kept tail, which is longer than the marker, so one cut
// between two writes is still found. It is safe for concurrent use.
type TailWriter struct {
	mu     sync.Mutex
	buf    []byte
	want   []byte
	marker chan struct{}
	seen   bool
}

// NewTailWriter returns a TailWriter that watches for marker.
func NewTailWriter(marker string) *TailWriter {
	return &TailWriter{want: []byte(marker), marker: make(chan struct{})}
}

// Marker returns a channel that is closed once the marker has been written.
func (w *TailWriter) Marker() <-chan struct{} { return w.marker }

func (w *TailWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	if !w.seen && bytes.Contains(w.buf, w.want) {
		w.seen = true
		close(w.marker)
	}
	if over := len(w.buf) - max(4096, len(w.want)); over > 0 {
		w.buf = w.buf[over:]
	}
	return len(p), nil
}

// String returns what it kept.
func (w *TailWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.buf)
}
