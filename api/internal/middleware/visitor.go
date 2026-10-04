package middleware

import (
	"context"
	"log/slog"
	"net/http"
	"net/netip"

	chimw "github.com/go-chi/chi/v5/middleware"
)

// VisitorIPHeader is the request header the web app's server sends the IP of
// the visitor whose page it's rendering in, next to its [ServerKeyHeader]
// (docs/design/607-per-visitor-web-limits.md).
const VisitorIPHeader = "X-Visitor-IP"

// Why [Visitor] ignored an X-Visitor-IP header, logged as reason with
// event=visitor_ip_invalid.
const (
	visitorReasonRepeated  = "repeated"
	visitorReasonUnparsed  = "unparsable"
	visitorReasonNotPublic = "not_public"
)

// visitorKey marks a request [Visitor] attributed to a visitor.
type visitorKey struct{}

// Visitor returns middleware that lets the web app's server make a request on
// a visitor's behalf. When callers puts r in [ClassWebServer] (a valid
// [ServerKeyHeader]) and r carries one [VisitorIPHeader] holding a public
// unicast address, that address replaces the client IP [ClientIP] recorded,
// and r is marked as the visitor's. A limiter with [TrustedCallers] then
// counts it in the visitor's own per-IP bucket, the one their browser's calls
// use, instead of the shared web-server bucket, and so does every other
// reader of the client IP.
//
// Without a valid key the header is never read, so a client can't choose the
// IP it's counted as. With a valid key, a header that is repeated, doesn't
// parse, or holds a private, loopback, link-local, multicast or unspecified
// address is ignored: the request keeps the web-server bucket, and it's
// logged as event=visitor_ip_invalid with a reason but never the value. A
// keyed request without the header is left alone, and so is every request
// when callers is nil (no trusted callers). Visitor runs right after
// ClientIP.
func Visitor(callers CallerClass, log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		if callers == nil {
			return next
		}
		// chi keeps the client IP's context key to itself: its header reader
		// is the only way to replace it. It reads the same single value
		// visitorIP has just checked.
		asVisitor := chimw.ClientIPFromHeader(VisitorIPHeader)(next)
		header := http.CanonicalHeaderKey(VisitorIPHeader)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			values := r.Header[header]
			if len(values) == 0 || callers(r) != ClassWebServer {
				next.ServeHTTP(w, r)
				return
			}
			if reason := visitorIP(values); reason != "" {
				log.WarnContext(r.Context(), "ignored an invalid visitor IP from the web server",
					"event", "visitor_ip_invalid", "reason", reason)
				next.ServeHTTP(w, r)
				return
			}
			asVisitor.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), visitorKey{}, true)))
		})
	}
}

// visitorIP checks an X-Visitor-IP header's values, and returns why they
// can't be used, or "" when they hold one public unicast address.
func visitorIP(values []string) string {
	if len(values) != 1 {
		return visitorReasonRepeated
	}
	// Parsed as chi's header reader parses it, untrimmed (the HTTP server
	// trims header values already), so the two never disagree.
	ip, err := netip.ParseAddr(values[0])
	if err != nil || ip.Zone() != "" {
		return visitorReasonUnparsed
	}
	if ip = ip.Unmap(); !ip.IsGlobalUnicast() || ip.IsPrivate() {
		return visitorReasonNotPublic
	}
	return ""
}

// fromVisitor reports whether [Visitor] attributed r to a visitor.
func fromVisitor(r *http.Request) bool {
	v, _ := r.Context().Value(visitorKey{}).(bool)
	return v
}
