package cli

import (
	"context"
	"fmt"
)

// Session is a named persistent session (SessionInfo, src/session.rs).
type Session struct {
	Name            string `json:"name"` // "default" for the default session
	Default         bool   `json:"default"`
	Running         bool   `json:"running"`
	ConnectionError string `json:"connection_error"` // set when connecting failed other than refused or missing
	SocketPath      string `json:"socket_path"`
	SessionDir      string `json:"session_dir"`
}

// Sessions runs herdr session list --json: the default session first, then
// the named sessions under the config directory. HERDR_SOCKET_PATH does not
// apply; the default session's socket is the one in the config directory.
func (c *CLI) Sessions(ctx context.Context) ([]Session, error) {
	var v struct {
		Sessions []Session `json:"sessions"`
	}
	if err := c.RunJSON(ctx, &v, "session", "list", "--json"); err != nil {
		return nil, err
	}
	return v.Sessions, nil
}

// StopSession runs herdr session stop <name> --json, which stops the
// session's server and waits up to 15s for its sockets to close; name
// "default" is the default session. It returns the session afterwards.
func (c *CLI) StopSession(ctx context.Context, name string) (*Session, error) {
	return c.sessionCommand(ctx, "stop", name)
}

// DeleteSession runs herdr session delete <name> --json, which removes a
// stopped named session's directory. Deleting a session that does not exist
// succeeds; a running session or "default" is an error.
func (c *CLI) DeleteSession(ctx context.Context, name string) (*Session, error) {
	return c.sessionCommand(ctx, "delete", name)
}

// sessionCommand passes name after "--" so a name such as "-h" is not read
// as an option.
func (c *CLI) sessionCommand(ctx context.Context, verb, name string) (*Session, error) {
	var v struct {
		Session *Session `json:"session"`
	}
	if err := c.RunJSON(ctx, &v, "session", verb, "--json", "--", name); err != nil {
		return nil, err
	}
	if v.Session == nil {
		return nil, fmt.Errorf("herdr session %s: no session in output", verb)
	}
	return v.Session, nil
}
