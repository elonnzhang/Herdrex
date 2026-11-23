package util

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// listenAddrs serves all local LAN addresses and, when requested, this
// machine's Tailscale addresses.
func listenAddrs(base netip.AddrPort, wantLAN, wantTS bool) ([]netip.AddrPort, error) {
	var lanAddrs []netip.Addr
	if wantLAN {
		local, err := InterfaceAddrs()
		if err != nil {
			return nil, fmt.Errorf("--lan: %w", err)
		}
		for _, a := range local {
			if LAN(a) && !slices.Contains(lanAddrs, a) {
				lanAddrs = append(lanAddrs, a)
			}
		}
		if len(lanAddrs) == 0 {
			return nil, fmt.Errorf("--lan: this machine has no LAN address")
		}
	}
	return ListenSelected(base, lanAddrs, wantTS)
}

func TestTailscale(t *testing.T) {
	for _, tc := range []struct {
		addr string
		want bool
	}{
		{"100.64.0.1", true},
		{"100.122.109.110", true},
		{"100.127.255.255", true},
		{"100.128.0.1", false},
		{"100.63.255.255", false},
		{"192.168.1.2", false},
		{"0.0.0.0", false},
		{"fd7a:115c:a1e0::202c:6d6f", true},
		{"fd7a:115c:a1e1::1", false},
		{"::ffff:100.100.1.1", true},
		{"::", false},
	} {
		if got := Tailscale(netip.MustParseAddr(tc.addr)); got != tc.want {
			t.Errorf("Tailscale(%s) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

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
		if got := LAN(netip.MustParseAddr(tc.addr)); got != tc.want {
			t.Errorf("LAN(%s) = %v, want %v", tc.addr, got, tc.want)
		}
	}
}

func TestListenAddrs(t *testing.T) {
	old := InterfaceAddrs
	t.Cleanup(func() { InterfaceAddrs = old })
	InterfaceAddrs = func() ([]netip.Addr, error) {
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
	old := InterfaceAddrs
	t.Cleanup(func() { InterfaceAddrs = old })
	InterfaceAddrs = func() ([]netip.Addr, error) { return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil }
	for _, tc := range []struct{ lan, ts bool }{{lan: true}, {ts: true}} {
		if _, err := listenAddrs(netip.MustParseAddrPort("127.0.0.1:7388"), tc.lan, tc.ts); err == nil {
			t.Errorf("lan=%v ts=%v: no address found, no error", tc.lan, tc.ts)
		}
	}
}

func TestParseAllow(t *testing.T) {
	l, err := ParseAllow([]string{"192.168.1.0/24, 10.0.0.5", "fd00::/8"})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"192.168.1.0/24", "10.0.0.5/32", "fd00::/8"}
	if l.String() != strings.Join(want, ",") {
		t.Fatalf("ParseAllow = %s, want %s", l, strings.Join(want, ","))
	}
	if !l.Contains(netip.MustParseAddr("192.168.1.9")) || l.Contains(netip.MustParseAddr("192.168.2.9")) {
		t.Error("192.168.1.0/24 does not hold 192.168.1.9 and not 192.168.2.9")
	}
	if !l.Contains(netip.MustParseAddr("127.0.0.1")) {
		t.Error("loopback is not always allowed")
	}
	if _, err := ParseAllow([]string{"192.168.1.0/33"}); err == nil {
		t.Error("a bad network parsed")
	}
	if l, err := ParseAllow(nil); err != nil || l.Contains(netip.MustParseAddr("8.8.8.8")) {
		t.Error("an empty list allows an address other than loopback")
	} else if !l.Contains(netip.MustParseAddr("127.0.0.1")) {
		t.Error("an empty list does not allow loopback")
	}
}

// TestAllowMiddleware checks who gets in, by caller and by the address the
// caller reached. A caller in the Tailscale range gets in without --allow
// only on a Tailscale address: on a LAN address it may come from a carrier or
// VPC network that uses the same CGNAT range.
func TestAllowMiddleware(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	const lan, ts, ts6, loop = "192.168.1.2:7392", "100.100.0.1:7392", "[fd7a:115c:a1e0::2]:7392", "127.0.0.1:7392"
	for _, tc := range []struct {
		name          string
		allow         []string
		local, remote string
		want          int
	}{
		{"named network", []string{"192.168.1.0/24"}, lan, "192.168.1.20:5555", http.StatusNoContent},
		{"other network", []string{"192.168.1.0/24"}, lan, "10.0.0.2:5555", http.StatusForbidden},
		{"loopback with a list", []string{"192.168.1.0/24"}, loop, "127.0.0.1:5555", http.StatusNoContent},
		{"tailnet with a list", []string{"192.168.1.0/24"}, ts6, "[fd7a:115c:a1e0::1]:5555", http.StatusNoContent},
		{"no --allow, tailnet", nil, ts, "100.64.0.9:5555", http.StatusNoContent},
		{"no --allow, tailnet IPv6", nil, ts6, "[fd7a:115c:a1e0::1]:5555", http.StatusNoContent},
		{"no --allow, CGNAT on a LAN address", nil, lan, "100.64.0.9:5555", http.StatusForbidden},
		{"no --allow, tailnet IPv6 on a LAN address", nil, lan, "[fd7a:115c:a1e0::1]:5555", http.StatusForbidden},
		{"no --allow, CGNAT without a local address", nil, "", "100.64.0.9:5555", http.StatusForbidden},
		{"CGNAT named, on a LAN address", []string{"100.64.0.0/10"}, lan, "100.64.0.9:5555", http.StatusNoContent},
		{"no --allow, LAN ULA", nil, lan, "[fd00::5]:5555", http.StatusForbidden},
		{"no --allow, LAN", nil, lan, "192.168.1.20:5555", http.StatusForbidden},
		{"no --allow, loopback", nil, loop, "127.0.0.1:5555", http.StatusNoContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l, err := ParseAllow(tc.allow)
			if err != nil {
				t.Fatal(err)
			}
			conn, _ := net.Pipe()
			defer conn.Close()
			req := httptest.NewRequest("GET", "/api/control", nil)
			rctx := l.ConnContext(req.Context(), conn)
			if tc.local != "" {
				rctx = context.WithValue(rctx, http.LocalAddrContextKey, net.TCPAddrFromAddrPort(netip.MustParseAddrPort(tc.local)))
			}
			req = req.WithContext(rctx)
			req.RemoteAddr = tc.remote
			rec := httptest.NewRecorder()
			l.Middleware(ok).ServeHTTP(rec, req)
			if rec.Code != tc.want {
				t.Errorf("%s: %d, want %d", tc.remote, rec.Code, tc.want)
			}
		})
	}
}

// TestAllowMiddlewareNoConnContext checks that a caller only --allow admits
// is refused when the server does not set ConnContext, since its connection
// could not be closed when --allow expires.
func TestAllowMiddlewareNoConnContext(t *testing.T) {
	l, _ := ParseAllow([]string{"192.168.1.0/24"})
	req := httptest.NewRequest("GET", "/api/control", nil)
	req.RemoteAddr = "192.168.1.20:5555"
	rec := httptest.NewRecorder()
	l.Middleware(http.NotFoundHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusInternalServerError {
		t.Errorf("%d, want %d", rec.Code, http.StatusInternalServerError)
	}
}

func TestAllowAll(t *testing.T) {
	l, err := ParseAllow([]string{"ALL"})
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []string{"8.8.8.8", "192.168.1.2", "fd00::5", "::ffff:10.0.0.1"} {
		if !l.Contains(netip.MustParseAddr(a)) {
			t.Errorf("all does not allow %s", a)
		}
	}
	if l.String() != AllowAll {
		t.Errorf("String = %q, want %q", l.String(), AllowAll)
	}
}

func TestAllowExpires(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	l, err := ParseAllow([]string{"192.168.1.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	l.now = func() time.Time { return now }
	l.ExpireAfter(time.Hour)
	if want := now.Add(time.Hour); !l.Until().Equal(want) {
		t.Fatalf("Until = %v, want %v", l.Until(), want)
	}
	lan, loop, ts := netip.MustParseAddr("192.168.1.9"), netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("100.64.0.9")
	if !l.Contains(lan) {
		t.Error("LAN caller refused before the end")
	}
	now = now.Add(time.Hour) // the instant it ends
	if l.Contains(lan) {
		t.Error("LAN caller admitted after the end")
	}
	if !l.Contains(loop) || !l.Contains(ts) {
		t.Error("loopback or Tailscale refused after the end")
	}
	l.ExpireAfter(0)
	if !l.Until().IsZero() || !l.Contains(lan) {
		t.Error("ExpireAfter(0) did not remove the limit")
	}
}

func TestParseAllowMapped(t *testing.T) {
	l, err := ParseAllow([]string{"::ffff:10.0.0.0/104"})
	if err != nil {
		t.Fatal(err)
	}
	if !l.Contains(netip.MustParseAddr("10.1.2.3")) || l.Contains(netip.MustParseAddr("11.0.0.1")) {
		t.Errorf("::ffff:10.0.0.0/104 is %s; want 10.0.0.0/8", l)
	}
	// Shorter than the mapping, it has no IPv4 form: it must not be dropped quietly.
	for _, bad := range []string{"::ffff:10.0.0.0/8", "::ffff:0:0/80"} {
		if _, err := ParseAllow([]string{bad}); err == nil {
			t.Errorf("ParseAllow(%q) succeeded", bad)
		}
	}
}

func TestAllowMiddlewareExpired(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	l, _ := ParseAllow([]string{"192.168.1.0/24"})
	l.now = func() time.Time { return now }
	l.ExpireAfter(time.Minute)
	now = now.Add(time.Minute)
	req := httptest.NewRequest("GET", "/", nil)
	req.RemoteAddr = "192.168.1.9:1"
	rec := httptest.NewRecorder()
	l.Middleware(http.NotFoundHandler()).ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden || !strings.Contains(rec.Body.String(), "expired") {
		t.Errorf("%d %q; want 403 saying --allow has expired", rec.Code, rec.Body.String())
	}
}

func TestLANSelector(t *testing.T) {
	for s, want := range map[string]bool{
		"en0": true, "EN0": true, "ipv6": true, "192": true, "0": true, "255": true, "256": false, "-1": false,
		"192.168.1.5": true, "10.0.0.0/8": true, "fd00::/8": true, "host": false, "7388": false, "": false,
	} {
		if got := LANSelector(s); got != want {
			t.Errorf("LANSelector(%q) = %v, want %v", s, got, want)
		}
	}
}

func stubInterfaces(t *testing.T, addrs ...string) {
	t.Helper()
	old := InterfaceAddrs
	t.Cleanup(func() { InterfaceAddrs = old })
	InterfaceAddrs = func() ([]netip.Addr, error) {
		var out []netip.Addr
		for _, a := range addrs {
			out = append(out, netip.MustParseAddr(a))
		}
		return out, nil
	}
}

func TestSelectLAN(t *testing.T) {
	stubInterfaces(t, "192.168.1.5", "10.0.0.7", "fd00::9", "100.64.0.9", "127.0.0.1", "8.8.8.8")
	for _, tc := range []struct {
		name      string
		selectors []string
		want      []string
		wantErr   bool
	}{
		{name: "first octet", selectors: []string{"192"}, want: []string{"192.168.1.5"}},
		{name: "two octets, in order", selectors: []string{"10", "192"}, want: []string{"10.0.0.7", "192.168.1.5"}},
		{name: "ipv6", selectors: []string{"ipv6"}, want: []string{"fd00::9"}},
		{name: "cidr", selectors: []string{"10.0.0.0/8"}, want: []string{"10.0.0.7"}},
		{name: "address", selectors: []string{"192.168.1.5"}, want: []string{"192.168.1.5"}},
		{name: "repeated", selectors: []string{"192", "192.168.1.5"}, want: []string{"192.168.1.5"}},
		{name: "no match", selectors: []string{"172"}, wantErr: true},
		{name: "tailscale is not LAN", selectors: []string{"100"}, wantErr: true},
		{name: "none", wantErr: true},
	} {
		got, err := SelectLAN(tc.selectors)
		if (err != nil) != tc.wantErr {
			t.Errorf("%s: %v, error %v", tc.name, got, err)
			continue
		}
		var s []string
		for _, a := range got {
			s = append(s, a.String())
		}
		if !tc.wantErr && !slices.Equal(s, tc.want) {
			t.Errorf("%s: %v, want %v", tc.name, s, tc.want)
		}
	}
	stubInterfaces(t, "127.0.0.1", "8.8.8.8")
	if _, err := SelectLAN([]string{"192"}); err == nil {
		t.Error("a machine with no LAN address selected one")
	}
}

func TestParseLANSelection(t *testing.T) {
	stubInterfaces(t, "192.168.1.5", "10.0.0.7", "8.8.8.8")
	got, err := ParseLANSelection("192.168.1.5,10.0.0.7,192.168.1.5")
	if err != nil || len(got) != 2 {
		t.Fatalf("got %v, error %v; want two addresses", got, err)
	}
	for _, bad := range []string{"8.8.8.8", "192.168.1.99", "nonsense", ""} {
		if _, err := ParseLANSelection(bad); err == nil {
			t.Errorf("ParseLANSelection(%q) succeeded", bad)
		}
	}
}

func TestListenAutoPort(t *testing.T) {
	held, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	port := held.Addr().(*net.TCPAddr).Port

	if _, err := ListenAutoPort(port, false, nil, false); !errors.Is(err, syscall.EADDRINUSE) {
		t.Errorf("a busy port without auto: %v, want address in use", err)
	}
	lns, err := ListenAutoPort(port, true, nil, false)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, ln := range lns {
			ln.Close()
		}
	}()
	if got := lns[0].Addr().(*net.TCPAddr).Port; got <= port {
		t.Errorf("auto port gave %d, want above the busy %d", got, port)
	}
	for _, p := range []int{0, 65536} {
		if _, err := ListenAutoPort(p, true, nil, false); err == nil {
			t.Errorf("port %d accepted", p)
		}
	}
}

// peerListener hands out its connections as coming from remote to local, as
// a listener on another network would.
type peerListener struct {
	net.Listener
	remote, local net.Addr
}

func (l *peerListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return &peerConn{c, l.remote, l.local}, nil
}

type peerConn struct {
	net.Conn
	remote, local net.Addr
}

func (c *peerConn) RemoteAddr() net.Addr { return c.remote }
func (c *peerConn) LocalAddr() net.Addr  { return c.local }

// TestAllowExpiryClosesConnections opens WebSockets from the LAN, loopback
// and the tailnet, ends one LAN socket normally, and lets --allow expire: the
// open LAN socket is closed, the others stay open.
func TestAllowExpiryClosesConnections(t *testing.T) {
	now := time.Date(2026, 11, 6, 12, 0, 0, 0, time.UTC)
	l, err := ParseAllow([]string{"192.168.1.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	l.now = func() time.Time { return now }
	l.ExpireAfter(time.Hour)

	echo := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer c.CloseNow()
		for {
			typ, b, err := c.Read(r.Context())
			if err != nil {
				return
			}
			c.Write(r.Context(), typ, b)
		}
	})
	done := make(chan struct{}, 8)
	srv := &http.Server{ConnContext: l.ConnContext, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		l.Middleware(echo).ServeHTTP(w, r)
		done <- struct{}{}
	})}
	t.Cleanup(func() { srv.Close() })
	dial := func(remote, local string) *websocket.Conn {
		t.Helper()
		ln, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		addr := func(s string) net.Addr { return net.TCPAddrFromAddrPort(netip.MustParseAddrPort(s)) }
		go srv.Serve(&peerListener{ln, addr(remote), addr(local)})
		c, _, err := websocket.Dial(ctx(t), "ws://"+ln.Addr().String()+"/", nil)
		if err != nil {
			t.Fatalf("%s: %v", remote, err)
		}
		t.Cleanup(func() { c.CloseNow() })
		return c
	}
	open := func(c *websocket.Conn) bool {
		rctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if c.Write(rctx, websocket.MessageText, []byte("ping")) != nil {
			return false
		}
		_, b, err := c.Read(rctx)
		return err == nil && string(b) == "ping"
	}

	ended := dial("192.168.1.8:5555", "192.168.1.2:7392")
	lan := dial("192.168.1.9:5555", "192.168.1.2:7392")
	loop := dial("127.0.0.1:5555", "127.0.0.1:7392")
	ts := dial("100.64.0.9:5555", "100.100.0.1:7392")
	for name, c := range map[string]*websocket.Conn{"LAN": lan, "loopback": loop, "tailnet": ts} {
		if !open(c) {
			t.Fatalf("%s socket does not echo before --allow expires", name)
		}
	}
	ended.Close(websocket.StatusNormalClosure, "")
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("the server did not end the closed LAN socket")
	}

	now = now.Add(time.Hour)
	if n := l.CloseAllowed(); n != 1 {
		t.Errorf("CloseAllowed closed %d connections, want 1: the open LAN socket", n)
	}
	rctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if _, _, err := lan.Read(rctx); err == nil || rctx.Err() != nil {
		t.Errorf("the LAN socket is still open after --allow expired (read: %v)", err)
	}
	for name, c := range map[string]*websocket.Conn{"loopback": loop, "tailnet": ts} {
		if !open(c) {
			t.Errorf("%s socket was closed when --allow expired", name)
		}
	}
}

func ctx(t *testing.T) context.Context {
	c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	return c
}
