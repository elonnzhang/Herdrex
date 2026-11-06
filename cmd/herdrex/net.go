package main

import (
	"fmt"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
)

// lanBareValue is the no-value marker for the optional --lan flag.
const lanBareValue = "bare"

// lanRanges are the private addresses a LAN address comes from: RFC 1918 and
// the IPv6 unique local block.
var lanRanges = []netip.Prefix{
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("fc00::/7"),
}

// lan reports whether a is a private address, the kind --lan serves.
// Tailscale's IPv6 range is inside the ULA block, so it is not a LAN address;
// --ts serves it.
func lan(a netip.Addr) bool {
	a = a.Unmap()
	if tailscale(a) {
		return false
	}
	for _, p := range lanRanges {
		if p.Contains(a) {
			return true
		}
	}
	return false
}

// interfaceAddrs lists this machine's unicast addresses. It is a variable so
// tests can stand in for the machine's interfaces.
var interfaceAddrs = func() ([]netip.Addr, error) {
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

func lanSelector(selector string) bool {
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

// selectLAN resolves --lan selectors to local LAN addresses. A bare --lan
// selects en0; selectors can name an IPv4 first octet, ipv6, an IP or a CIDR.
func selectLAN(selectors []string) ([]netip.Addr, error) {
	if len(selectors) == 0 {
		return nil, fmt.Errorf("--lan: give an address selector or use bare --lan for en0")
	}
	local, err := interfaceAddrs()
	if err != nil {
		return nil, fmt.Errorf("--lan: %w", err)
	}
	candidates := make([]netip.Addr, 0, len(local))
	for _, a := range local {
		if lan(a) && !slices.Contains(candidates, a) {
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
		if selector == lanBareValue || strings.EqualFold(strings.TrimSpace(selector), "en0") {
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
		if selector == lanBareValue || strings.EqualFold(strings.TrimSpace(selector), "en0") {
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

func parseLANSelection(selection string) ([]netip.Addr, error) {
	local, err := interfaceAddrs()
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
		if !lan(a) || !slices.Contains(local, a) {
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

// listenAddrs serves all local LAN addresses and, when requested, this
// machine's Tailscale addresses. Kept for the network selection tests.
func listenAddrs(base netip.AddrPort, wantLAN, wantTS bool) ([]netip.AddrPort, error) {
	var lanAddrs []netip.Addr
	if wantLAN {
		local, err := interfaceAddrs()
		if err != nil {
			return nil, fmt.Errorf("--lan: %w", err)
		}
		for _, a := range local {
			if lan(a) && !slices.Contains(lanAddrs, a) {
				lanAddrs = append(lanAddrs, a)
			}
		}
		if len(lanAddrs) == 0 {
			return nil, fmt.Errorf("--lan: this machine has no LAN address")
		}
	}
	return listenSelectedAddrs(base, lanAddrs, wantTS)
}

// listenSelectedAddrs serves base, the LAN addresses selected by --lan and,
// when requested, this machine's Tailscale addresses.
func listenSelectedAddrs(base netip.AddrPort, lanAddrs []netip.Addr, wantTS bool) ([]netip.AddrPort, error) {
	baseIP := base.Addr().Unmap()
	switch {
	case baseIP.IsLoopback(), tailscale(baseIP):
	case lan(baseIP) && len(lanAddrs) > 0:
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
		local, err := interfaceAddrs()
		if err != nil {
			return nil, fmt.Errorf("--ts: %w", err)
		}
		var found []netip.Addr
		for _, a := range local {
			if tailscale(a) && !slices.Contains(found, a) {
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
