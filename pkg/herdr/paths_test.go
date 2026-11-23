package herdr

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestConfigDir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/x/cfg")
	t.Setenv("HOME", "/home/u")
	if got := ConfigDir(); got != "/x/cfg/herdr" {
		t.Fatalf("with XDG_CONFIG_HOME: %s", got)
	}
	t.Setenv("XDG_CONFIG_HOME", "")
	if got := ConfigDir(); got != "herdr" {
		t.Fatalf("with an empty XDG_CONFIG_HOME: %s", got)
	}
	unsetenv(t, "XDG_CONFIG_HOME")
	if got := ConfigDir(); got != "/home/u/.config/herdr" {
		t.Fatalf("with HOME: %s", got)
	}
	t.Setenv("HOME", "")
	if got := ConfigDir(); got != ".config/herdr" {
		t.Fatalf("with an empty HOME: %s", got)
	}
	unsetenv(t, "HOME")
	if got := ConfigDir(); got != filepath.Join(os.TempDir(), "herdr") {
		t.Fatalf("without HOME: %s", got)
	}
}

func TestSessionDir(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/c")
	for name, want := range map[string]string{
		"":        "/c/herdr",
		"default": "/c/herdr",
		"work":    "/c/herdr/sessions/work",
	} {
		if got := SessionDir(name); got != want {
			t.Errorf("SessionDir(%q) = %s, want %s", name, got, want)
		}
	}
}

func TestSocketPathOrder(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/c")
	t.Setenv("HERDR_SOCKET_PATH", "/override/api.sock")
	t.Setenv("HERDR_SESSION", "envsess")
	if got := SocketPath("explicit"); got != "/c/herdr/sessions/explicit/herdr.sock" {
		t.Fatalf("explicit session should win: %s", got)
	}
	if got := SocketPath(""); got != "/override/api.sock" {
		t.Fatalf("HERDR_SOCKET_PATH should beat HERDR_SESSION: %s", got)
	}
	t.Setenv("HERDR_SOCKET_PATH", "")
	if got := SocketPath(""); got != "" {
		t.Fatalf("an empty HERDR_SOCKET_PATH is still set: %q", got)
	}
	unsetenv(t, "HERDR_SOCKET_PATH")
	if got := SocketPath(""); got != "/c/herdr/sessions/envsess/herdr.sock" {
		t.Fatalf("HERDR_SESSION: %s", got)
	}
	for _, bad := range []string{"default", "a/b", "..", strings.Repeat("x", 65)} {
		t.Setenv("HERDR_SESSION", bad)
		if got := SocketPath(""); got != "/c/herdr/herdr.sock" {
			t.Fatalf("HERDR_SESSION=%q should fall back to the default: %s", bad, got)
		}
	}
	t.Setenv("HERDR_SESSION", "")
	if got := SocketPath(""); got != "/c/herdr/herdr.sock" {
		t.Fatalf("default: %s", got)
	}
}

func TestClientSocket(t *testing.T) {
	for in, want := range map[string]string{
		"/tmp/x/herdr.sock":     "/tmp/x/herdr-client.sock",
		"/tmp/x/api":            "/tmp/x/api-client.sock",
		"/tmp/x/my.api.sock":    "/tmp/x/my.api-client.sock",
		"/tmp/x/.sock":          "/tmp/x/.sock-client.sock",
		"relative/herdr.socket": "relative/herdr-client.sock",
	} {
		if got := ClientSocket(in); got != want {
			t.Errorf("ClientSocket(%q) = %s, want %s", in, got, want)
		}
	}
}

func unsetenv(t *testing.T, key string) {
	t.Setenv(key, "")
	os.Unsetenv(key)
}

func TestLocalClientSocket(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/c")
	unsetenv(t, "HERDR_SESSION")
	unsetenv(t, "HERDR_SOCKET_PATH")
	t.Setenv("HERDR_CLIENT_SOCKET_PATH", "/legacy/client.sock")
	if got := LocalClientSocket(SocketPath("")); got != "/legacy/client.sock" {
		t.Errorf("HERDR_CLIENT_SOCKET_PATH without HERDR_SOCKET_PATH: %s", got)
	}
	t.Setenv("HERDR_SESSION", "work")
	if got := LocalClientSocket(SocketPath("")); got != "/legacy/client.sock" {
		t.Errorf("HERDR_CLIENT_SOCKET_PATH should beat HERDR_SESSION: %s", got)
	}
	if got := LocalClientSocket("/elsewhere/herdr.sock"); got != "/elsewhere/herdr-client.sock" {
		t.Errorf("an API socket the environment does not name: %s", got)
	}
	t.Setenv("HERDR_SOCKET_PATH", "/api/herdr.sock")
	if got := LocalClientSocket(SocketPath("")); got != "/api/herdr-client.sock" {
		t.Errorf("HERDR_SOCKET_PATH should beat HERDR_CLIENT_SOCKET_PATH: %s", got)
	}
	unsetenv(t, "HERDR_SOCKET_PATH")
	unsetenv(t, "HERDR_CLIENT_SOCKET_PATH")
	if got := LocalClientSocket(SocketPath("")); got != "/c/herdr/sessions/work/herdr-client.sock" {
		t.Errorf("HERDR_SESSION: %s", got)
	}
}

func TestValidSessionName(t *testing.T) {
	for _, ok := range []string{"a", "work-1", "v0.9_x", strings.Repeat("x", 64)} {
		if !ValidSessionName(ok) {
			t.Errorf("%q should be valid", ok)
		}
	}
	for _, bad := range []string{"", ".", "..", "a b", "a/b", "é", strings.Repeat("x", 65)} {
		if ValidSessionName(bad) {
			t.Errorf("%q should be invalid", bad)
		}
	}
}
