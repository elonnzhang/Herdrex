package main

import (
	"fmt"
	"log"
	"net/http"
	"net/netip"
	"strings"
)

// allowList is the set of LAN networks that may use the bridge (--allow).
// Loopback and Tailscale callers always may: loopback is this machine, and a
// tailnet address is a device Tailscale has already authenticated and lets
// reach this one. Any other caller, a LAN one included, needs --allow.
type allowList []netip.Prefix

// parseAllow reads --allow's values: networks in CIDR form, bare addresses,
// comma-separated or repeated.
func parseAllow(values []string) (allowList, error) {
	var out allowList
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			part = strings.TrimSpace(part)
			if part == "" {
				continue
			}
			if p, err := netip.ParsePrefix(part); err == nil {
				if p.Addr().Is4In6() {
					p = netip.PrefixFrom(p.Addr().Unmap(), p.Bits()-96)
				}
				out = append(out, p.Masked())
				continue
			}
			a, err := netip.ParseAddr(part)
			if err != nil {
				return nil, fmt.Errorf("--allow %q is neither an address nor a network: %w", part, err)
			}
			a = a.Unmap()
			out = append(out, netip.PrefixFrom(a, a.BitLen()))
		}
	}
	return out, nil
}

// String renders the list as --allow would take it back.
func (l allowList) String() string {
	parts := make([]string, len(l))
	for i, p := range l {
		parts[i] = p.String()
	}
	return strings.Join(parts, ",")
}

// contains reports whether ip may connect. Loopback and Tailscale always
// may; any other address only when --allow names it.
func (l allowList) contains(ip netip.Addr) bool {
	ip = ip.Unmap()
	if ip.IsLoopback() || tailscale(ip) {
		return true
	}
	for _, p := range l {
		if p.Contains(ip) {
			return true
		}
	}
	return false
}

// middleware answers 403 to a connection from an address that is not allowed.
func (l allowList) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ip, err := clientAddr(r.RemoteAddr)
		if err != nil || !l.contains(ip) {
			log.Printf("herdrex: refused %s: --allow does not name it", r.RemoteAddr)
			http.Error(w, "forbidden: this address is not allowed; pass --allow", http.StatusForbidden)
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
