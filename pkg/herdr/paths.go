// Package herdr holds the socket path rules that Herdr's clients share
// (pkg/herdr/HERDR.md §2). The protocols live in its subpackages: jsonapi for
// the JSON API on herdr.sock, term for the direct terminal protocol and
// endpoint for the endpoint protocol on herdr-client.sock, and cli for the
// herdr command.
//
// The rules follow Herdr 0.9.5 (src/config/io.rs, src/session.rs) on Unix;
// Windows named pipes are not supported.
package herdr

import (
	"os"
	"path/filepath"
	"strings"
)

// DefaultSession is the name Herdr accepts for the default session.
const DefaultSession = "default"

// ConfigDir is Herdr's config directory: $XDG_CONFIG_HOME/herdr, else
// $HOME/.config/herdr, else herdr in the temporary directory.
//
// A variable set to the empty string counts as set, so an empty
// XDG_CONFIG_HOME gives the relative path herdr: Herdr tests each one with
// env::var(...).is_ok() (src/config/io.rs:31, 63), and a client must find the
// server's sockets where the server put them. SocketPath and
// LocalClientSocket follow the same rule.
func ConfigDir() string {
	if dir, ok := os.LookupEnv("XDG_CONFIG_HOME"); ok {
		return filepath.Join(dir, "herdr")
	}
	if home, ok := os.LookupEnv("HOME"); ok {
		return filepath.Join(home, ".config", "herdr")
	}
	return filepath.Join(os.TempDir(), "herdr")
}

// SessionDir is the directory holding a session's sockets and state: the
// config directory for the default session ("" or "default"), otherwise
// sessions/<name> under it.
func SessionDir(name string) string {
	if name == "" || name == DefaultSession {
		return ConfigDir()
	}
	return filepath.Join(ConfigDir(), "sessions", name)
}

// SocketPath resolves the JSON API socket in Herdr's order: an explicit
// session name, then HERDR_SOCKET_PATH, then HERDR_SESSION, then the default
// session. An invalid HERDR_SESSION is ignored, as Herdr ignores it.
func SocketPath(session string) string {
	if session != "" {
		return filepath.Join(SessionDir(session), "herdr.sock")
	}
	if p, ok := os.LookupEnv("HERDR_SOCKET_PATH"); ok { // src/session.rs:179
		return p
	}
	if name := os.Getenv("HERDR_SESSION"); ValidSessionName(name) {
		return filepath.Join(SessionDir(name), "herdr.sock")
	}
	return filepath.Join(ConfigDir(), "herdr.sock")
}

// ClientSocket derives the client socket path from the API socket path, as
// Herdr does for HERDR_SOCKET_PATH (src/server/socket_paths.rs): the file
// name without its extension, plus "-client.sock", in the same directory.
// herdr.sock -> herdr-client.sock.
func ClientSocket(apiSocket string) string {
	base := filepath.Base(apiSocket)
	stem := strings.TrimSuffix(base, filepath.Ext(base))
	if stem == "" { // ".sock": Rust's file_stem keeps a leading dot
		stem = base
	}
	return filepath.Join(filepath.Dir(apiSocket), stem+"-client.sock")
}

// LocalClientSocket is the client socket of the Herdr server on this machine
// whose API socket is apiSocket. For the API socket that SocketPath("") finds,
// it resolves as Herdr does (src/server/socket_paths.rs:23): derived from
// HERDR_SOCKET_PATH when that is set, else HERDR_CLIENT_SOCKET_PATH when that
// is set, else beside the API socket. Any other apiSocket was named
// explicitly, as HERDR_SOCKET_PATH names one, and derives as ClientSocket.
func LocalClientSocket(apiSocket string) string {
	if _, ok := os.LookupEnv("HERDR_SOCKET_PATH"); !ok && apiSocket == SocketPath("") {
		if p, ok := os.LookupEnv("HERDR_CLIENT_SOCKET_PATH"); ok {
			return p
		}
	}
	return ClientSocket(apiSocket)
}

// ValidSessionName reports whether Herdr accepts name: 1 to 64 bytes of
// ASCII letters, digits, '.', '_' and '-', and not "." or "..".
func ValidSessionName(name string) bool {
	if name == "" || len(name) > 64 || name == "." || name == ".." {
		return false
	}
	for i := 0; i < len(name); i++ {
		c := name[i]
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' ||
			c == '.' || c == '_' || c == '-') {
			return false
		}
	}
	return true
}
