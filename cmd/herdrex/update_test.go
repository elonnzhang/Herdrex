package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestDetectChannel(t *testing.T) {
	for _, tc := range []struct {
		name                                    string
		exe, resolved, gobin, gopath, h, script string
		want                                    string
	}{
		{"brew link", "/opt/homebrew/bin/herdrex", "/opt/homebrew/Cellar/herdrex/0.3.0/bin/herdrex", "", "", "/Users/a", "", "brew"},
		{"intel brew", "/usr/local/bin/herdrex", "/usr/local/Cellar/herdrex/0.3.0/bin/herdrex", "", "", "/Users/a", "", "brew"},
		{"default go bin", "/Users/a/go/bin/herdrex", "/Users/a/go/bin/herdrex", "", "", "/Users/a", "", "go"},
		{"GOPATH", "/work/gp/bin/herdrex", "/work/gp/bin/herdrex", "", "/other:/work/gp", "/Users/a", "", "go"},
		{"GOBIN", "/opt/tools/herdrex", "/opt/tools/herdrex", "/opt/tools", "", "/Users/a", "", "go"},
		{"GOBIN set hides the default", "/Users/a/go/bin/herdrex", "/Users/a/go/bin/herdrex", "/opt/tools", "", "/Users/a", "", ""},
		{"built from source", "/Users/a/code/Herdrex/bin/herdrex", "/Users/a/code/Herdrex/bin/herdrex", "", "", "/Users/a", "", ""},
		{"install.sh", "/usr/local/bin/herdrex", "/usr/local/bin/herdrex", "", "", "/Users/a", "/usr/local/bin/herdrex", "script"},
		{"install.sh recorded another path", "/usr/local/bin/herdrex", "/usr/local/bin/herdrex", "", "", "/Users/a", "/home/a/.local/bin/herdrex", ""},
		{"copied", "/usr/local/bin/herdrex", "/usr/local/bin/herdrex", "", "", "/Users/a", "", ""},
	} {
		if got := detectChannel(tc.exe, tc.resolved, tc.gobin, tc.gopath, tc.h, tc.script); got != tc.want {
			t.Errorf("%s: %q, want %q", tc.name, got, tc.want)
		}
	}
}

// stubUpdate replaces the command runner and lookup, and returns what ran.
func stubUpdate(t *testing.T, missing string, failOn string) *[]string {
	t.Helper()
	oldRun, oldLook := runStep, lookPath
	t.Cleanup(func() { runStep, lookPath = oldRun, oldLook })
	var ran []string
	runStep = func(_ context.Context, argv []string, _ io.Writer) error {
		line := strings.Join(argv, " ")
		ran = append(ran, line)
		if line == failOn {
			return errors.New("exit status 1")
		}
		return nil
	}
	lookPath = func(name string) (string, error) {
		if name == missing {
			return "", errors.New("not found")
		}
		return "/bin/" + name, nil
	}
	return &ran
}

func TestUpdateRunsTheChannelsSteps(t *testing.T) {
	for channel, want := range map[string][]string{
		"brew":   {"brew update", "brew upgrade elonnzhang/tap/herdrex"},
		"go":     {"go install github.com/elonnzhang/herdrex/cmd/herdrex@latest"},
		"script": {"sh -c HERDREX_INSTALL_DIR='/opt/x' && export HERDREX_INSTALL_DIR && curl -fsSL 'https://raw.githubusercontent.com/elonnzhang/Herdrex/main/install.sh' | sh"},
	} {
		ran := stubUpdate(t, "", "")
		if err := update(t.Context(), channel, "/opt/x", false, &bytes.Buffer{}); err != nil {
			t.Fatalf("%s: %v", channel, err)
		}
		if strings.Join(*ran, ";") != strings.Join(want, ";") {
			t.Errorf("%s ran %v, want %v", channel, *ran, want)
		}
	}
}

func TestUpdateDryRunRunsNothing(t *testing.T) {
	ran := stubUpdate(t, "brew", "") // even brew missing is only printed
	var out bytes.Buffer
	if err := update(t.Context(), "brew", "", true, &out); err != nil {
		t.Fatal(err)
	}
	if len(*ran) != 0 || !strings.Contains(out.String(), "$ brew upgrade elonnzhang/tap/herdrex") {
		t.Errorf("ran %v, printed %q", *ran, out.String())
	}
}

func TestUpdateFailures(t *testing.T) {
	ran := stubUpdate(t, "go", "")
	if err := update(t.Context(), "go", "", false, &bytes.Buffer{}); err == nil || len(*ran) != 0 {
		t.Errorf("missing go: error %v, ran %v", err, *ran)
	}
	ran = stubUpdate(t, "", "brew update")
	if err := update(t.Context(), "brew", "", false, &bytes.Buffer{}); err == nil || len(*ran) != 1 {
		t.Errorf("failing brew update: error %v, ran %v; want it to stop after the first step", err, *ran)
	}
	if err := update(t.Context(), "apt", "", false, &bytes.Buffer{}); err == nil {
		t.Error("unknown channel accepted")
	}
}

func TestUpdateCommandNeedsAChannelForUnknownInstall(t *testing.T) {
	ran := stubUpdate(t, "", "")
	cmd := command()
	cmd.SetArgs([]string{"upgrade", "--channel", "go", "--dry-run"})
	cmd.SetOut(&bytes.Buffer{})
	if err := cmd.Execute(); err != nil || len(*ran) != 0 {
		t.Errorf("upgrade alias: error %v, ran %v", err, *ran)
	}
}
