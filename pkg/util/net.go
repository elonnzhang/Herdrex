package util

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// LANBare is the no-value marker for the optional --lan flag.
const LANBare = "bare"

// lanRanges are the private addresses a LAN address comes from: RFC 1918 and
// the IPv6 unique local block.
var lanRanges = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("fc00::/7"),
}

// LAN reports whether a is a private address, the kind --lan serves.
// Tailscale's IPv6 range is inside the ULA block, so it is not a LAN address;
// --ts serves it.
func LAN(a netip.Addr) bool {
	a = a.Unmap()
	if Tailscale(a) {
		return false
	}
	for _, p := range lanRanges {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// InterfaceAddrs lists this machine's unicast addresses. It is a variable so
// tests can stand in for the machine's interfaces.
var InterfaceAddrs = func() ([]netip.Addr, error) {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil, err
	}
	var out []netip.Addr
	for _, i := range ifaces {
		addrs, err := i.Addrs()
		if err != nil {
			return nil, err
		}
		for _, a := range addrs {
			p, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			if ip, ok := netip.AddrFromSlice(p.IP); ok {
				out = append(out, ip.Unmap())
			}
		}
	}
	return out, nil
}

func LANSelector(selector string) bool {
	selector = strings.TrimSpace(selector)
	if strings.EqualFold(selector, "en0") || strings.EqualFold(selector, "ipv6") {
		return true
	}
	if _, err := netip.ParsePrefix(selector); err == nil {
		return true
	}
	if _, err := netip.ParseAddr(selector); err == nil {
		return true
	}
	first, err := strconv.Atoi(selector)
	return err == nil && first >= 0 && first <= 255
}

// SelectLAN resolves --lan selectors to local LAN addresses. A bare --lan
// selects en0; selectors can name an IPv4 first octet, ipv6, an IP or a CIDR.
func SelectLAN(selectors []string) ([]netip.Addr, error) {
	if len(selectors) == 0 {
		return nil, fmt.Errorf("--lan: give an address selector or use bare --lan for en0")
	}
	local, err := InterfaceAddrs()
	if err != nil {
		return nil, fmt.Errorf("--lan: %w", err)
	}
	candidates := make([]netip.Addr, 0, len(local))
	for _, a := range local {
		if LAN(a) && !slices.Contains(candidates, a) {
			candidates = append(candidates, a)
		}
	}
	slices.SortFunc(candidates, func(a, b netip.Addr) int { return a.Compare(b) })
	if len(candidates) == 0 {
		return nil, fmt.Errorf("--lan: this machine has no LAN address")
	}

	var selected []netip.Addr
	useEN0 := false
	for _, selector := range selectors {
		if selector == LANBare || strings.EqualFold(strings.TrimSpace(selector), "en0") {
			useEN0 = true
			break
		}
	}
	if useEN0 {
		a, err := defaultLAN(candidates)
		if err != nil {
			return nil, err
		}
		selected = append(selected, a)
	}
	for _, selector := range selectors {
		if selector == LANBare || strings.EqualFold(strings.TrimSpace(selector), "en0") {
			continue
		}
		selector = strings.TrimSpace(selector)
		matched := false
		for _, a := range candidates {
			if matchesLAN(a, selector) {
				matched = true
				if !slices.Contains(selected, a) {
					selected = append(selected, a)
				}
			}
		}
		if !matched {
			return nil, fmt.Errorf("--lan %q does not match a local LAN address", selector)
		}
	}
	return selected, nil
}

func ParseLANSelection(selection string) ([]netip.Addr, error) {
	local, err := InterfaceAddrs()
	if err != nil {
		return nil, fmt.Errorf("--lan: %w", err)
	}
	var selected []netip.Addr
	for _, part := range strings.Split(selection, ",") {
		a, err := netip.ParseAddr(part)
		if err != nil {
			return nil, fmt.Errorf("invalid internal LAN selection %q: %w", part, err)
		}
		a = a.Unmap()
		if !LAN(a) || !slices.Contains(local, a) {
			return nil, fmt.Errorf("internal LAN selection %q is no longer a local LAN address", part)
		}
		if !slices.Contains(selected, a) {
			selected = append(selected, a)
		}
	}
	return selected, nil
}

func matchesLAN(a netip.Addr, selector string) bool {
	if strings.EqualFold(selector, "ipv6") {
		return a.Is6()
	}
	if p, err := netip.ParsePrefix(selector); err == nil {
		return p.Contains(a)
	}
	if ip, err := netip.ParseAddr(selector); err == nil {
		return ip.Unmap() == a.Unmap()
	}
	first, err := strconv.Atoi(selector)
	if err != nil || first < 0 || first > 255 || !a.Is4() {
		return false
	}
	return int(a.As4()[0]) == first
}

func defaultLAN(candidates []netip.Addr) (netip.Addr, error) {
	choices := make([]string, len(candidates))
	for i, a := range candidates {
		choices[i] = "--lan=" + a.String()
	}
	selectHint := "select one explicitly with " + strings.Join(choices, " or ")
	iface, err := net.InterfaceByName("en0")
	if err != nil {
		return netip.Addr{}, fmt.Errorf("--lan: cannot find en0; %s", selectHint)
	}
	addrs, err := iface.Addrs()
	if err != nil {
		return netip.Addr{}, fmt.Errorf("--lan: list en0 addresses: %w", err)
	}
	var ipv6 netip.Addr
	for _, raw := range addrs {
		prefix, err := netip.ParsePrefix(raw.String())
		if err != nil {
			continue
		}
		a := prefix.Addr().Unmap()
		if !slices.Contains(candidates, a) {
			continue
		}
		if a.Is4() {
			return a, nil
		}
		if !ipv6.IsValid() {
			ipv6 = a
		}
	}
	if ipv6.IsValid() {
		return ipv6, nil
	}
	return netip.Addr{}, fmt.Errorf("--lan: en0 has no LAN address; %s", selectHint)
}

// ListenSelected serves base, the LAN addresses selected by --lan and,
// when requested, this machine's Tailscale addresses.
func ListenSelected(base netip.AddrPort, lanAddrs []netip.Addr, wantTS bool) ([]netip.AddrPort, error) {
	baseIP := base.Addr().Unmap()
	switch {
	case baseIP.IsLoopback(), Tailscale(baseIP):
	case LAN(baseIP) && len(lanAddrs) > 0:
	case baseIP.IsUnspecified():
		return nil, fmt.Errorf("--ip cannot name every address; name one address and use --lan or --ts for the others")
	default:
		return nil, fmt.Errorf("--ip %s: the bridge has no authentication; a LAN address needs --lan, otherwise name a loopback or Tailscale address", base)
	}

	out := []netip.AddrPort{base}
	seen := map[netip.AddrPort]bool{base: true}
	for _, a := range lanAddrs {
		ap := netip.AddrPortFrom(a, base.Port())
		if !seen[ap] {
			seen[ap] = true
			out = append(out, ap)
		}
	}
	if wantTS {
		local, err := InterfaceAddrs()
		if err != nil {
			return nil, fmt.Errorf("--ts: %w", err)
		}
		var found []netip.Addr
		for _, a := range local {
			if Tailscale(a) && !slices.Contains(found, a) {
				found = append(found, a)
			}
		}
		if len(found) == 0 {
			return nil, fmt.Errorf("--ts: this machine has no Tailscale address")
		}
		slices.SortFunc(found, func(a, b netip.Addr) int { return a.Compare(b) })
		for _, a := range found {
			ap := netip.AddrPortFrom(a, base.Port())
			if !seen[ap] {
				seen[ap] = true
				out = append(out, ap)
			}
		}
	}
	return out, nil
}

// tailnet holds the addresses Tailscale gives to devices.
var tailnet = []netip.Prefix{
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("fd7a:115c:a1e0::/48"),
}

// Tailscale reports whether a is a Tailscale address.
func Tailscale(a netip.Addr) bool {
	for _, p := range tailnet {
		if p.Contains(a.Unmap()) {
			return true
		}
	}
	return false
}

// ListenAll serves on every address, and fails if any of them cannot be
// served: half a bridge is worse than none.
func ListenAll(addrs []netip.AddrPort) ([]net.Listener, []string, error) {
	var listeners []net.Listener
	var urls []string
	for _, a := range addrs {
		ln, err := net.Listen("tcp", a.String())
		if err != nil {
			for _, l := range listeners {
				l.Close()
			}
			return nil, nil, fmt.Errorf("--listen %s: %w", a, err)
		}
		listeners = append(listeners, ln)
		urls = append(urls, "http://"+ln.Addr().String())
	}
	return listeners, urls, nil
}

// ListenAutoPort listens on 127.0.0.1 and the LAN and Tailscale addresses
// selected, at port start. With auto it moves up to the first port, through
// 65535, at which every address is free.
func ListenAutoPort(start int, auto bool, lanIPs []netip.Addr, ts bool) ([]net.Listener, error) {
	if start < 1 || start > 65535 {
		return nil, fmt.Errorf("--port %d: want 1 to 65535", start)
	}
	last := start
	if auto {
		last = 65535
	}
	ip := netip.MustParseAddr("127.0.0.1")
	for port := start; port <= last; port++ {
		addrs, err := ListenSelected(netip.AddrPortFrom(ip, uint16(port)), lanIPs, ts)
		if err != nil {
			return nil, err
		}
		listeners, _, err := ListenAll(addrs)
		if err == nil {
			return listeners, nil
		}
		if !auto || !errors.Is(err, syscall.EADDRINUSE) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("no available loopback port from %d through %d", start, last)
}

// AllowList is the set of networks that may use the bridge (--allow), and for
// how long. Loopback and Tailscale callers always may: loopback is this
// machine, and a tailnet address is a device Tailscale has already
// authenticated and lets reach this one. Any other caller, a LAN one included,
// needs --allow.
type AllowList struct {
	nets  []netip.Prefix
	until time.Time        // when the networks stop admitting; zero for never
	now   func() time.Time // time.Now, unless a test says otherwise
}

// AllowAll is the --allow value for every address.
const AllowAll = "all"

// allNets are the networks AllowAll stands for.
var allNets = []netip.Prefix{netip.MustParsePrefix("0.0.0.0/0"), netip.MustParsePrefix("::/0")}

// ParseAllow reads --allow's values: networks in CIDR form, bare addresses, or
// "all" for every address, comma-separated or repeated.
func ParseAllow(values []string) (AllowList, error) {
	var out AllowList
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if strings.EqualFold(part, AllowAll) {
				out.nets = append(out.nets, allNets...)
				continue
			}
			if p, err := netip.ParsePrefix(part); err == nil {
				if p.Addr().Is4In6() {
					// An IPv4-mapped network is the IPv4 one it maps; a prefix
					// shorter than the 96 bits of the mapping has no IPv4 form.
					if p.Bits() < 96 {
						return AllowList{}, fmt.Errorf("--allow %q: an IPv4-mapped network needs at least 96 prefix bits; write the IPv4 network instead", part)
					}
					p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
				}
				out.nets = append(out.nets, p.Masked())
				continue
			}
			a, err := netip.ParseAddr(part)
			if err != nil {
				return AllowList{}, fmt.Errorf("--allow %q is neither an address, a network nor %q: %w", part, AllowAll, err)
			}
			a = a.Unmap()
			out.nets = append(out.nets, netip.PrefixFrom(a, a.BitLen()))
		}
	}
	return out, nil
}

// Empty reports whether --allow named nothing.
func (l AllowList) Empty() bool { return len(l.nets) == 0 }

// ExpireAfter makes the networks admit callers for d from now, and no longer.
// Loopback and Tailscale callers are not affected. A d of 0 or less means no
// limit.
func (l *AllowList) ExpireAfter(d time.Duration) {
	if d <= 0 {
		l.until = time.Time{}
		return
	}
	l.until = l.clock().Add(d)
}

// Until is when the networks stop admitting callers; the zero time when they
// never do.
func (l AllowList) Until() time.Time { return l.until }

func (l AllowList) clock() time.Time {
	if l.now != nil {
		return l.now()
	}
	return time.Now()
}

// expired reports whether the networks have stopped admitting callers.
func (l AllowList) expired() bool { return !l.until.IsZero() && !l.clock().Before(l.until) }

// String renders the list as --allow would take it back.
func (l AllowList) String() string {
	if slices.Contains(l.nets, allNets[0]) && slices.Contains(l.nets, allNets[1]) {
		return AllowAll
	}
	parts := make([]string, len(l.nets))
	for i, p := range l.nets {
		parts[i] = p.String()
	}
	return strings.Join(parts, ",")
}

// Contains reports whether ip may connect. Loopback and Tailscale always
// may; any other address only when --allow names it, and has not expired.
func (l AllowList) Contains(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.IsLoopback() || Tailscale(ip) {
		return true
	}
	if l.expired() {
		return false
	}
	for _, p := range l.nets {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// Middleware answers 403 to a connection from an address that is not allowed.
func (l AllowList) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, err := clientAddr(r.RemoteAddr)
		if err != nil || !l.Contains(ip) {
			why, hint := "--allow does not name it", "this address is not allowed; pass --allow"
			if l.expired() {
				why, hint = "--allow has expired", "--allow has expired"
			}
			log.Printf("herdrex: refused %s: %s", r.RemoteAddr, why)
			http.Error(w, "forbidden: "+hint, http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// clientAddr is the address part of a listener's RemoteAddr.
func clientAddr(remote string) (netip.Addr, error) {
	if ap, err := netip.ParseAddrPort(remote); err == nil {
		return ap.Addr().Unmap(), nil
	}
	return netip.ParseAddr(remote)
}
