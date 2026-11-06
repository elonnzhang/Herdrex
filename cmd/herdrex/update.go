package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/elonnzhang/herdrex/pkg/util"
	"github.com/spf13/cobra"
)

// The ways herdrex is installed, which the README documents, and the commands
// that bring each one to the latest release.
const (
	channelBrew   = "brew"
	channelGo     = "go"
	channelScript = "script" // install.sh, which downloads a GitHub Release

	installScriptURL = "https://raw.githubusercontent.com/elonnzhang/Herdrex/main/install.sh"

	brewFormula = "elonnzhang/tap/herdrex"
	goPackage   = "github.com/elonnzhang/herdrex/cmd/herdrex@latest"
)

// updateSteps are the commands that update herdrex through channel, run in
// order, and the programs they need. installDir is where the running binary
// is, which install.sh is told to replace.
func updateSteps(channel, installDir string) (steps [][]string, needs []string, ok bool) {
	switch channel {
	case channelBrew:
		return [][]string{{"brew", "update"}, {"brew", "upgrade", brewFormula}}, []string{"brew"}, true
	case channelGo:
		return [][]string{{"go", "install", goPackage}}, []string{"go"}, true
	case channelScript:
		run := "HERDREX_INSTALL_DIR=" + util.ShellQuote(installDir) + " && export HERDREX_INSTALL_DIR && curl -fsSL " + util.ShellQuote(installScriptURL) + " | sh"
		return [][]string{{"sh", "-c", run}}, []string{"sh", "curl"}, true
	}
	return nil, nil, false
}

// scriptMarker is the file install.sh writes with the path of the binary it
// installed; it is $XDG_STATE_HOME/herdrex/install-path there too.
func scriptMarker(home string) string {
	state := os.Getenv("XDG_STATE_HOME")
	if state == "" {
		state = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(state, "herdrex", "install-path")
}

// detectChannel says how the herdrex binary at exe was installed, or "" when
// it cannot tell (a go build, a copied file). resolved is exe with symlinks
// followed: Homebrew links its bin directory to the Cellar. gobin and gopath
// are $GOBIN and $GOPATH; home is the user's home directory; script is the
// path install.sh recorded, or "".
func detectChannel(exe, resolved, gobin, gopath, home, script string) string {
	if strings.Contains(filepath.ToSlash(resolved), "/Cellar/herdrex/") {
		return channelBrew
	}
	if script != "" && exe == script {
		return channelScript
	}
	dir := filepath.Dir(exe)
	switch {
	case gobin != "":
		if dir == filepath.Clean(gobin) {
			return channelGo
		}
	default:
		gopaths := filepath.SplitList(gopath)
		if len(gopaths) == 0 && home != "" {
			gopaths = []string{filepath.Join(home, "go")}
		}
		for _, p := range gopaths {
			if dir == filepath.Join(p, "bin") {
				return channelGo
			}
		}
	}
	return ""
}

// installedChannel detects the channel of the running binary.
func installedChannel() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	resolved, err := filepath.EvalSymlinks(exe)
	if err != nil {
		resolved = exe
	}
	home, _ := os.UserHomeDir()
	var script string
	if data, err := os.ReadFile(scriptMarker(home)); err == nil {
		script = strings.TrimSpace(string(data))
	}
	return detectChannel(exe, resolved, os.Getenv("GOBIN"), os.Getenv("GOPATH"), home, script), nil
}

// runStep runs one update command with its output on out. It is a variable so
// tests can stand in for brew and go.
var runStep = func(ctx context.Context, argv []string, out io.Writer) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, out, out
	return cmd.Run()
}

// lookPath finds a command on PATH; a variable for tests.
var lookPath = exec.LookPath

// update runs the update steps of channel, or with dryRun only prints them.
// Every command must exist before the first one runs, so that a missing go
// does not leave brew half-updated.
func update(ctx context.Context, channel, installDir string, dryRun bool, out io.Writer) error {
	steps, needs, ok := updateSteps(channel, installDir)
	if !ok {
		return fmt.Errorf("unknown channel %q: want %s, %s or %s", channel, channelBrew, channelGo, channelScript)
	}
	for _, name := range needs {
		if _, err := lookPath(name); err != nil && !dryRun {
			return fmt.Errorf("update through %s needs %s on PATH: %w", channel, name, err)
		}
	}
	for _, argv := range steps {
		fmt.Fprintf(out, "$ %s\n", strings.Join(argv, " "))
		if dryRun {
			continue
		}
		if err := runStep(ctx, argv, out); err != nil {
			return fmt.Errorf("%s: %w", strings.Join(argv, " "), err)
		}
	}
	if !dryRun {
		fmt.Fprintln(out, "updated; a herdrex that is already running keeps the old version until you restart it (kill PID, then start it again)")
	}
	return nil
}

func updateCommand() *cobra.Command {
	var channel string
	var dryRun bool
	cmd := &cobra.Command{
		Use:     "update",
		Aliases: []string{"upgrade"},
		Short:   "Update herdrex to the latest release, through the way it was installed",
		Long: `update finds out how this herdrex was installed and runs that channel's own update:

  brew   brew update, then brew upgrade ` + brewFormula + `
  go     go install ` + goPackage + `
  script run install.sh, which downloads the latest GitHub Release into the directory of this binary

The channel is read from where the binary is: a Homebrew Cellar link means brew, the path install.sh recorded means script, $GOBIN or $GOPATH/bin means go. For a binary anywhere else, such as one built from source, give --channel. A herdrex that is already running is not touched; restart it to use the new version.`,
		Example: "  herdrex update\n  herdrex upgrade --dry-run\n  herdrex update --channel go",
		Args:    cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			if channel == "" {
				var err error
				if channel, err = installedChannel(); err != nil {
					return err
				}
				if channel == "" {
					return fmt.Errorf("cannot tell how this herdrex was installed; give --channel %s, %s or %s", channelBrew, channelGo, channelScript)
				}
			}
			fmt.Fprintf(cmd.OutOrStdout(), "herdrex %s, updating through %s\n", buildVersion(), channel)
			exe, err := os.Executable()
			if err != nil {
				return err
			}
			return update(cmd.Context(), channel, filepath.Dir(exe), dryRun, cmd.OutOrStdout())
		},
		SilenceUsage: true,
	}
	cmd.Flags().StringVar(&channel, "channel", "", "update through brew, go or script instead of detecting it from the binary's location")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print the commands without running them")
	return cmd
}
