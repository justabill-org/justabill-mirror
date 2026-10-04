package middleware

import (
	"fmt"
	"net/http"
	"net/netip"
	"strings"
)

// AccessMode is ACCESS_MODE: who the API answers (docs/design/28-production.md, "How production
// stays private until go-public").
type AccessMode string

// The modes ACCESS_MODE accepts.
const (
	// AccessPublic answers everyone. It's the default.
	AccessPublic AccessMode = "public"
	// AccessPrivate answers only the web app's server (a valid [ServerKeyHeader]) and clients in
	// ACCESS_ALLOW_CIDRS; see [PrivateAccess].
	AccessPrivate AccessMode = "private"
)

// ParseAccessMode reads ACCESS_MODE. Empty is [AccessPublic]. Anything else unknown is an error
// rather than a silent public default, so a typo can't open a private deployment.
func ParseAccessMode(s string) (AccessMode, error) {
	switch AccessMode(strings.TrimSpace(s)) {
	case "", AccessPublic:
		return AccessPublic, nil
	case AccessPrivate:
		return AccessPrivate, nil
	default:
		return "", fmt.Errorf("ACCESS_MODE=%q: want %q or %q", s, AccessPublic, AccessPrivate)
	}
}

// ParseAllowCIDRs reads ACCESS_ALLOW_CIDRS: comma-separated CIDRs such as 203.0.113.0/24 or
// 2001:db8::/48. A bare address is that one host. A prefix of length 0 (0.0.0.0/0, ::/0) would
// let everyone in, so it's refused: that's ACCESS_MODE=public. Empty is no addresses.
func ParseAllowCIDRs(s string) ([]netip.Prefix, error) {
	var prefixes []netip.Prefix
	for i, entry := range strings.Split(s, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		p, err := parsePrefix(entry)
		if err != nil {
			return nil, fmt.Errorf("ACCESS_ALLOW_CIDRS entry %d %q: want a CIDR such as 203.0.113.0/24 "+
				"or an address", i+1, entry)
		}
		if p.Bits() == 0 {
			return nil, fmt.Errorf("ACCESS_ALLOW_CIDRS entry %d %q allows every address: "+
				"use ACCESS_MODE=public instead", i+1, entry)
		}
		prefixes = append(prefixes, p)
	}
	return prefixes, nil
}

// parsePrefix parses a CIDR, or a bare address as a single-host prefix, and masks it.
func parsePrefix(s string) (netip.Prefix, error) {
	if !strings.Contains(s, "/") {
		addr, err := netip.ParseAddr(s)
		if err != nil {
			return netip.Prefix{}, fmt.Errorf("parse address: %w", err)
		}
		addr = addr.Unmap()
		return netip.PrefixFrom(addr, addr.BitLen()), nil
	}
	p, err := netip.ParsePrefix(s)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("parse prefix: %w", err)
	}
	return p.Masked(), nil
}

// PrivateAccess returns middleware that serves a request only when callers puts it in a trusted
// class (the web app's server, with a valid [ServerKeyHeader]; see [ServerKeys]) or its client
// IP, from [ClientIP], falls in allow. Every other request gets the same bare 404 as a route that
// doesn't exist, with Cache-Control: no-store, so nothing says there's an API behind it.
//
// It must run after [ClientIP] and ahead of CORS and the rate limiters, whose headers and 429s
// would give the API away. The probes (/healthz, /readyz) are served ahead of the router, so they
// always answer.
func PrivateAccess(callers CallerClass, allow []netip.Prefix) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if trustedCaller(callers, r) || allowed(allow, clientIPOf(r)) {
				next.ServeHTTP(w, r)
				return
			}
			w.Header().Set("Cache-Control", "no-store")
			http.NotFound(w, r)
		})
	}
}

// trustedCaller reports whether callers puts r in a class other than [ClassIP].
func trustedCaller(callers CallerClass, r *http.Request) bool {
	return callers != nil && callers(r) != ClassIP
}

// allowed reports whether addr falls in one of prefixes. An unknown address never does.
func allowed(prefixes []netip.Prefix, addr netip.Addr) bool {
	if !addr.IsValid() {
		return false
	}
	for _, p := range prefixes {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}
