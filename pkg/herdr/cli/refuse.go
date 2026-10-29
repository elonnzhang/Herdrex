package cli

import (
	"errors"
	"fmt"
	"strings"
)

// ErrRefused is wrapped by the error Run and RunJSON return for a command
// that would start a server or attach an interactive client. Nothing runs.
var ErrRefused = errors.New("refused")

// refuse returns an error wrapping ErrRefused when argv, the arguments after
// the program name, would make herdr start a server or attach an interactive
// client, and nil otherwise.
func refuse(argv []string) error {
	why, alt := launches(argv)
	if why == "" {
		return nil
	}
	return fmt.Errorf("herdr %s: %w: it %s; %s", strings.Join(argv, " "), ErrRefused, why, alt)
}

// launches says what argv would start or attach, and what to use instead;
// both are empty for other commands. It follows Herdr 0.9.5's parsing in
// src/main.rs: --session (src/session.rs:57), --remote, --remote-keybindings
// and --handoff (src/remote/args.rs:48) are taken out anywhere before "--",
// and --handoff without --remote goes back at the end (src/remote/args.rs:113).
// The first argument left is the command. A leading --machine stays in place
// and Herdr only accepts API-backed commands after it (src/cli/target.rs:291).
func launches(argv []string) (why, alt string) {
	const useServer = "use StartServer to run an isolated server"
	const useTerm = "use pkg/herdr/term to control a terminal"
	var cmd []string
	remote, handoff := false, false
	for i := 0; i < len(argv); i++ {
		a := argv[i]
		switch {
		case a == "--":
			cmd = append(cmd, argv[i:]...)
			i = len(argv)
		case a == "--remote":
			remote = true
			i++ // and its value
		case a == "--session", a == "--remote-keybindings":
			i++
		case strings.HasPrefix(a, "--remote="):
			remote = true
		case strings.HasPrefix(a, "--session="), strings.HasPrefix(a, "--remote-keybindings="):
		case a == "--handoff":
			handoff = true
		default:
			cmd = append(cmd, a)
		}
	}
	if handoff && !remote {
		cmd = append(cmd, "--handoff")
	}
	if remote && len(cmd) == 0 {
		// src/main.rs:787: SSH attach to a remote TUI, which starts the
		// remote server through remote-client-bridge. With a command,
		// --remote is a usage error (src/main.rs:544) unless a flag such as
		// --help follows, and then the command runs locally as below.
		return "attaches the TUI of a remote Herdr over SSH", useServer
	}
	arg := func(i int) string {
		if i < len(cmd) {
			return cmd[i]
		}
		return ""
	}
	switch arg(0) {
	case "":
		// src/main.rs:797, src/server/autodetect.rs:305: spawns a detached
		// server daemon when none is running (:327), then the TUI (:339).
		return "launches the TUI and starts a detached server if none is running", useServer
	case "server":
		switch arg(1) {
		case "", "--handoff-import":
			// src/cli/server.rs:11, :18 leave these to src/main.rs:569.
			return "runs a server in the foreground", useServer
		case "live-handoff":
			// src/cli/server.rs:213: the running server starts a detached
			// replacement (src/server/handoff.rs:86).
			return "makes the running server start a detached replacement", useServer
		}
	case "client":
		// src/main.rs:574: the TUI client, even with --help or --version,
		// which src/main.rs only checks later (:606, :725).
		return "attaches the TUI", useServer
	case "remote-client-bridge":
		// src/main.rs:565, src/remote/host.rs:22: starts a detached server
		// when none is running (:61).
		return "starts a detached server if none is running", useServer
	case "session":
		if arg(1) == "attach" {
			// src/session.rs:37: becomes a bare launch in that session.
			return "launches the TUI and starts the session's server if none is running", useServer
		}
	case "terminal", "agent":
		if arg(1) == "attach" {
			// src/cli.rs:431, src/cli/agent.rs:62: the TUI client in
			// attach mode (src/client/startup.rs:9).
			return "attaches an interactive terminal client", useTerm
		}
	case "update":
		// Plain update is allowed: without a terminal on stdin it never
		// stops a running server (see Run).
		if handoff {
			// src/main.rs:580, src/update.rs:1233, :1397: hands every
			// running server off to a new detached server.
			return "hands running servers off to new detached servers", useServer
		}
	}
	return "", ""
}
