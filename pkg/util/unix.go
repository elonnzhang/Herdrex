package util

import (
	"errors"
	"fmt"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"time"
)

// ListenUnix serves on the Unix socket at path, an absolute path, in a
// directory it creates with mode 0700 when missing. The socket gets mode
// 0600 from the moment it exists, so only this user and root can connect;
// that is the socket's access control, as the --allow list is TCP's. A
// socket left there by a process that is gone is replaced; one that still
// answers belongs to another server and is left alone. Closing the listener
// removes the socket.
func ListenUnix(path string) (net.Listener, error) {
	if !filepath.IsAbs(path) {
		return nil, fmt.Errorf("unix socket %q: want an absolute path", path)
	}
	if max := maxSocketPath(); len(path) > max {
		return nil, fmt.Errorf("unix socket %s: the path is %d bytes; %s allows at most %d", path, len(path), runtime.GOOS, max)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("unix socket %s: %w", path, err)
	}
	if err := removeStaleSocket(path); err != nil {
		return nil, err
	}
	old := syscall.Umask(0o177) // no window in which others may connect
	ln, err := net.Listen("unix", path)
	syscall.Umask(old)
	if err != nil {
		return nil, fmt.Errorf("unix socket %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return nil, fmt.Errorf("unix socket %s: %w", path, err)
	}
	return ln, nil
}

// removeStaleSocket removes a socket at path that nothing listens on. It
// refuses a file that is not a socket, and a socket that answers.
func removeStaleSocket(path string) error {
	fi, err := os.Lstat(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("unix socket %s: %w", path, err)
	}
	if fi.Mode().Type() != fs.ModeSocket {
		return fmt.Errorf("unix socket %s: the path exists and is not a socket", path)
	}
	c, err := net.DialTimeout("unix", path, time.Second)
	if err == nil {
		c.Close()
		return fmt.Errorf("unix socket %s: another server listens there", path)
	}
	if !errors.Is(err, syscall.ECONNREFUSED) {
		return fmt.Errorf("unix socket %s: %w", path, err)
	}
	if err := os.Remove(path); err != nil {
		return fmt.Errorf("unix socket %s: removing the stale socket: %w", path, err)
	}
	return nil
}

// maxSocketPath is the longest socket path the kernel takes: sun_path less
// its terminating NUL.
func maxSocketPath() int {
	if runtime.GOOS == "linux" {
		return 107
	}
	return 103
}

// OnUnixSocket reports whether r came in on a Unix socket listener.
func OnUnixSocket(r *http.Request) bool {
	a, ok := r.Context().Value(http.LocalAddrContextKey).(net.Addr)
	return ok && a.Network() == "unix"
}
