package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestLAN(t *testing.T) {
	for _, tc := range []struct {
		addr string
		want bool
	}{
		{"192.168.1.2", true},
		{"10.0.0.5", true},
		{"172.16.0.1", true},
		{"172.31.255.254", true},
		{"172.32.0.1", false},
		{"100.122.109.110", false},
		{"127.0.0.1", false},
		{"8.8.8.8", false},
		{"fd00::1", true},
		{"fd7a:115c:a1e0::1", false},
		{"fe80::1", false},
		{"::ffff:192.168.1.2", true},
	} {
		if got := lan(netip.MustParseAddr(tc.addr)); got != tc.want {
			t.Errorf("lan(%s) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

func TestListenAddrs(t *testing.T) {
	old := interfaceAddrs
	t.Cleanup(func() { interfaceAddrs = old })
	interfaceAddrs = func() ([]netip.Addr, error) {
		return []netip.Addr{
			netip.MustParseAddr("192.168.1.5"),
			netip.MustParseAddr("100.64.0.9"),
			netip.MustParseAddr("127.0.0.1"),
			netip.MustParseAddr("8.8.8.8"),
		}, nil
	}

	for _, tc := range []struct {
		name    string
		base    string
		lan, ts bool
		want    []string
		wantErr bool
	}{
		{name: "loopback only", base: "127.0.0.1:7388", want: []string{"127.0.0.1:7388"}},
		{name: "lan", base: "127.0.0.1:7388", lan: true, want: []string{"127.0.0.1:7388", "192.168.1.5:7388"}},
		{name: "ts", base: "127.0.0.1:7388", ts: true, want: []string{"127.0.0.1:7388", "100.64.0.9:7388"}},
		{name: "both", base: "127.0.0.1:7388", lan: true, ts: true, want: []string{"127.0.0.1:7388", "192.168.1.5:7388", "100.64.0.9:7388"}},
		{name: "a LAN base needs --lan", base: "192.168.1.5:7388", wantErr: true},
		{name: "a LAN base with --lan is not served twice", base: "10.1.2.3:7000", lan: true, want: []string{"10.1.2.3:7000", "192.168.1.5:7000"}},
		{name: "a Tailscale base is allowed", base: "100.64.0.9:7388", want: []string{"100.64.0.9:7388"}},
		{name: "a public address is refused", base: "8.8.8.8:7388", lan: true, wantErr: true},
		{name: "every address is refused", base: "0.0.0.0:7388", lan: true, wantErr: true},
	} {
		got, err := listenAddrs(netip.MustParseAddrPort(tc.base), tc.lan, tc.ts)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: %v (error %v)", tc.name, got, err)
			continue
		}
		if tc.wantErr {
			continue
		}
		var gotStrings []string
		for _, a := range got {
			gotStrings = append(gotStrings, a.String())
		}
		if !slices.Equal(gotStrings, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, gotStrings, tc.want)
		}
	}
}

func TestListenAddrsMissing(t *testing.T) {
	old := interfaceAddrs
	t.Cleanup(func() { interfaceAddrs = old })
	interfaceAddrs = func() ([]netip.Addr, error) { return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil }
	for _, tc := range []struct{ lan, ts bool }{{lan: true}, {ts: true}} {
		if _, err := listenAddrs(netip.MustParseAddrPort("127.0.0.1:7388"), tc.lan, tc.ts); err == nil {
			t.Errorf("lan=%v ts=%v: no address found, no error", tc.lan, tc.ts)
		}
	}
}

func TestParseAllow(t *testing.T) {
	l, err := parseAllow([]string{"192.168.1.0/24, 10.0.0.5", "fd00::/8"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"192.168.1.0/24", "10.0.0.5/32", "fd00::/8"}
	if l.String() != strings.Join(want, ",") {
		t.Fatalf("parseAllow = %s, want %s", l, strings.Join(want, ","))
	}
	if !l.contains(netip.MustParseAddr("192.168.1.9")) || l.contains(netip.MustParseAddr("192.168.2.9")) {
		t.Error("192.168.1.0/24 does not hold 192.168.1.9 and not 192.168.2.9")
	}
	if !l.contains(netip.MustParseAddr("127.0.0.1")) {
		t.Error("loopback is not always allowed")
	}
	if _, err := parseAllow([]string{"192.168.1.0/33"}); err == nil {
		t.Error("a bad network parsed")
	}
	if l, err := parseAllow(nil); err != nil || l.contains(netip.MustParseAddr("8.8.8.8")) {
		t.Error("an empty list allows an address other than loopback")
	} else if !l.contains(netip.MustParseAddr("127.0.0.1")) {
		t.Error("an empty list does not allow loopback")
	}
}

func TestAllowMiddleware(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	for _, tc := range []struct {
		name   string
		allow  []string
		remote string
		want   int
	}{
		{"named network", []string{"192.168.1.0/24"}, "192.168.1.20:5555", http.StatusNoContent},
		{"other network", []string{"192.168.1.0/24"}, "10.0.0.2:5555", http.StatusForbidden},
		{"loopback with a list", []string{"192.168.1.0/24"}, "127.0.0.1:5555", http.StatusNoContent},
		{"tailnet with a list", []string{"192.168.1.0/24"}, "[fd7a:115c:a1e0::1]:5555", http.StatusNoContent},
		{"no --allow, tailnet", nil, "100.64.0.9:5555", http.StatusNoContent},
		{"no --allow, tailnet IPv6", nil, "[fd7a:115c:a1e0::1]:5555", http.StatusNoContent},
		{"no --allow, LAN ULA", nil, "[fd00::5]:5555", http.StatusForbidden},
		{"no --allow, LAN", nil, "192.168.1.20:5555", http.StatusForbidden},
		{"no --allow, loopback", nil, "127.0.0.1:5555", http.StatusNoContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, err := parseAllow(tc.allow)
			if err != nil {
				t.Fatal(err)
			}
			req := httptest.NewRequest("GET", "/api/control", nil)
			req.RemoteAddr = tc.remote
			rec := httptest.NewRecorder()
			l.middleware(ok).ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Errorf("%s: %d, want %d", tc.remote, rec.Code, tc.want)
			}
		})
	}
}

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
