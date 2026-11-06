package main

import (
	"io"
	"net/netip"
	"slices"
	"testing"

	"github.com/spf13/cobra"
)

func TestResolveAddr(t *testing.T) {
	for _, tc := range []struct {
		args    []string
		want    string
		wantErr bool
	}{
		{args: nil, want: "127.0.0.1:7388"},
		{args: []string{"--port", "1234"}, want: "127.0.0.1:1234"},
		{args: []string{"--lan", "--port", "1234"}, want: "127.0.0.1:1234"},
		{args: []string{"--ip", "192.168.1.5", "--lan"}, want: "192.168.1.5:7388"},
		{args: []string{"--ip", "100.64.0.9"}, want: "100.64.0.9:7388"},
		{args: []string{"--listen", "100.64.0.9:7392"}, want: "100.64.0.9:7392"},
		{args: []string{"--listen", "100.64.0.9:7392", "--port", "1"}, wantErr: true},
		{args: []string{"--listen", "100.64.0.9:7392", "--ip", "1.2.3.4"}, wantErr: true},
		{args: []string{"--listen", "nonsense"}, wantErr: true},
		{args: []string{"--port", "70000"}, wantErr: true},
		{args: []string{"--ip", "nonsense"}, wantErr: true},
	} {
		cmd := command()
		cmd.SetArgs(normalizeLANArgs(tc.args))
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		var got netip.AddrPort
		var err error
		cmd.RunE = func(cmd *cobra.Command, _ []string) error {
			o := options{}
			o.ip, _ = cmd.Flags().GetString("ip")
			o.listen, _ = cmd.Flags().GetString("listen")
			o.port, _ = cmd.Flags().GetInt("port")
			got, err = resolveAddr(cmd, o)
			return nil
		}
		if e := cmd.Execute(); e != nil {
			t.Fatalf("%v: %v", tc.args, e)
		}
		if (err != nil) != tc.wantErr {
			t.Errorf("%v: %v (error %v), want error %v", tc.args, got, err, tc.wantErr)
			continue
		}
		if !tc.wantErr && got.String() != tc.want {
			t.Errorf("%v: %v, want %v", tc.args, got, tc.want)
		}
	}
}

func TestRewriteLAN(t *testing.T) {
	for _, tc := range []struct{ in, want []string }{
		{nil, []string{}},
		{[]string{"--lan"}, []string{"--lan=bare"}},
		{[]string{"--lan", "192"}, []string{"--lan=192"}},
		{[]string{"--lan", "--port", "1"}, []string{"--lan=bare", "--port", "1"}},
		{[]string{"--lan", "10", "--lan", "ipv6"}, []string{"--lan=10", "--lan=ipv6"}},
		{[]string{"--lan", "10.0.0.0/8", "--allow", "all"}, []string{"--lan=10.0.0.0/8", "--allow", "all"}},
		{[]string{"--lan=192", "--lan"}, []string{"--lan=192", "--lan=bare"}},
		{[]string{"--", "--lan", "192"}, []string{"--", "--lan", "192"}}, // after -- nothing is a flag
	} {
		if got := rewriteLAN(tc.in); !slices.Equal(got, tc.want) {
			t.Errorf("rewriteLAN(%v) = %v, want %v", tc.in, got, tc.want)
		}
	}
}
