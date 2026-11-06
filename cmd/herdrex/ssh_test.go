package main

import (
	"slices"
	"testing"
)

func TestParseSSHArgs(t *testing.T) {
	cases := []struct {
		name  string
		args  []string
		host  string
		port  int
		auto  bool
		ts    bool
		lan   []string
		allow []string
		err   bool
	}{
		{name: "alias", args: []string{"host"}, host: "host", port: 7388, auto: true},
		{name: "user@host", args: []string{"user@host"}, host: "user@host", port: 7388, auto: true},
		{name: "ssh option and value", args: []string{"-p", "2222", "user@host"}, host: "user@host", port: 7388, auto: true},
		{name: "lan after destination", args: []string{"host", "--lan"}, host: "host", port: 7388, auto: true, lan: []string{lanBareValue}},
		{name: "lan before destination", args: []string{"--lan", "host"}, host: "host", port: 7388, auto: true, lan: []string{lanBareValue}},
		{name: "lan selector", args: []string{"--lan", "192", "host"}, host: "host", port: 7388, auto: true, lan: []string{"192"}},
		{name: "lan inline and ts", args: []string{"host", "--lan=192", "--ts"}, host: "host", port: 7388, auto: true, ts: true, lan: []string{"192"}},
		{name: "port and allow", args: []string{"-p", "2222", "u@h", "--port", "7400", "--allow", "10.0.0.0/8,192.168.1.9"},
			host: "u@h", port: 7400, allow: []string{"10.0.0.0/8", "192.168.1.9"}},
		{name: "destination after --", args: []string{"--", "host"}, host: "host", port: 7388, auto: true},
		{name: "ssh option after destination", args: []string{"u@h", "-p", "2222"}, err: true},
		{name: "remote command", args: []string{"u@h", "echo", "hi"}, err: true},
		{name: "force", args: []string{"-f", "host"}, err: true},
		{name: "no destination", args: nil, err: true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parseSSHArgs(c.args)
			if c.err {
				if err == nil {
					t.Fatalf("want an error, got %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.host != c.host || got.port != c.port || got.autoPort != c.auto || got.ts != c.ts {
				t.Errorf("got host %q port %d auto %v ts %v, want %q %d %v %v",
					got.host, got.port, got.autoPort, got.ts, c.host, c.port, c.auto, c.ts)
			}
			if !slices.Equal(got.lan, c.lan) || !slices.Equal(got.allow, c.allow) {
				t.Errorf("got lan %q allow %q, want %q %q", got.lan, got.allow, c.lan, c.allow)
			}
		})
	}
}
