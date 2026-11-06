package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
)

// sshUploads stores uploaded files on the SSH host, where Herdr and the
// pane's programs run, so that the path the bridge pastes exists there. Each
// file goes over the shared ControlMaster connection as stdin to one remote
// command.
type sshUploads struct {
	ssh  string   // path of the ssh binary
	base []string // ssh options, ending with the ControlPath ones
	host string
}

// sshUploadMark starts the line that carries the remote path, so that what a
// login shell prints before the command runs is ignored.
const sshUploadMark = "herdrex-upload-path:"

// sshUploadScript makes a private directory in the remote temporary
// directory and writes stdin to $1 there. It runs under sh, whatever the
// remote login shell is, and holds no single quote so that it quotes as one
// word.
const sshUploadScript = `umask 077; d=$(mktemp -d "${TMPDIR:-/tmp}/herdrex-upload-XXXXXX") || exit 1; cat > "$d/$1" || exit 1; printf "\n` + sshUploadMark + `%s\n" "$d/$1"`

// Store writes data to name in a new private directory on the SSH host.
func (s sshUploads) Store(ctx context.Context, name string, data []byte) (string, error) {
	remote := "sh -c " + shellQuote(sshUploadScript) + " herdrex-upload " + shellQuote(name)
	args := append(append([]string{}, s.base...), "-T", s.host, remote)
	cmd := exec.CommandContext(ctx, s.ssh, args...)
	cmd.Stdin = bytes.NewReader(data)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("write %s on %s: %v %s", name, s.host, err, strings.TrimSpace(stderr.String()))
	}
	for line := range strings.Lines(string(out)) {
		if path, ok := strings.CutPrefix(strings.TrimRight(line, "\r\n"), sshUploadMark); ok && path != "" {
			return path, nil
		}
	}
	return "", fmt.Errorf("write %s on %s: the remote command printed no path", name, s.host)
}
