package middleware

import (
	"log/slog"
	"net/http"
	"net/netip"
	"strings"

	chimw "github.com/go-chi/chi/v5/middleware"
)

// ClientIP returns middleware that records the client IP in the request
// context, where RateLimit reads it. hops is TRUSTED_PROXY_HOPS, the number of
// reverse proxies between the internet and this server: 0 uses the TCP peer
// address, and N > 0 uses the Nth X-Forwarded-For entry from the right.
// Headers set by the client are never trusted beyond that count, and
// X-Real-IP and True-Client-IP are never read.
//
// When no client IP can be found (a chain shorter than hops, or a garbage
// entry), the context stays empty, so RateLimit counts the request in its
// shared unknown bucket, and the request is logged with
// event=client_ip_missing. In production that event should never fire; if it
// does, hops is too high.
func ClientIP(hops int, log *slog.Logger) func(http.Handler) http.Handler {
	find := chimw.ClientIPFromRemoteAddr
	if hops > 0 {
		find = chimw.ClientIPFromXFFTrustedProxies(hops)
	}
	return func(next http.Handler) http.Handler {
		return find(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if !chimw.GetClientIPAddr(r.Context()).IsValid() {
				log.WarnContext(r.Context(), "no trusted client IP; using the shared unknown bucket",
					"event", "client_ip_missing",
					"trusted_proxy_hops", hops,
					"xff_entries", countXFFEntries(r.Header.Values("X-Forwarded-For")),
				)
			}
			next.ServeHTTP(w, r)
		}))
	}
}

// countXFFEntries counts the non-empty X-Forwarded-For entries across all of
// the header's values. It's logged instead of the chain itself, which holds
// client IPs.
func countXFFEntries(values []string) int {
	n := 0
	for _, v := range values {
		for entry := range strings.SplitSeq(v, ",") {
			if strings.TrimSpace(entry) != "" {
				n++
			}
		}
	}
	return n
}

// clientIPOf is the client IP [ClientIP] resolved for r (chi keeps it in the
// request context), unmapped from IPv4-in-IPv6. It's invalid when ClientIP
// found none or didn't run. Every reader of the client IP goes through it.
func clientIPOf(r *http.Request) netip.Addr {
	return chimw.GetClientIPAddr(r.Context()).Unmap()
}
