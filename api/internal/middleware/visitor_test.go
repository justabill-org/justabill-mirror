package middleware_test

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"

	mw "github.com/justabill-org/justabill/api/internal/middleware"
)

// vercelPeer is the web app's server: a Vercel egress address it shares
// with other people's renders.
const vercelPeer = "76.76.21.21:443"

// visitorRequest is a request from peer with key as X-Server-Key and every
// visitor as an X-Visitor-IP value, each left out when empty.
func visitorRequest(peer, key string, visitors ...string) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = peer
	if key != "" {
		req.Header.Set(mw.ServerKeyHeader, key)
	}
	for _, v := range visitors {
		req.Header.Add(mw.VisitorIPHeader, v)
	}
	return req
}

func TestVisitor(t *testing.T) {
	keys := mw.ServerKeys([]string{currentKey})
	tests := []struct {
		name      string
		noCallers bool // no trusted callers at all
		key       string
		visitors  []string
		wantIP    string // the client IP the next handler sees
		wantMark  bool
		reason    string // the visitor_ip_invalid reason logged, if any
	}{
		{name: "valid key and visitor", key: currentKey, visitors: []string{"203.0.113.7"},
			wantIP: "203.0.113.7", wantMark: true},
		{name: "IPv6 visitor", key: currentKey, visitors: []string{"2001:db8:aa:bb::7"},
			wantIP: "2001:db8:aa:bb::7", wantMark: true},
		{name: "IPv4-mapped visitor", key: currentKey, visitors: []string{"::ffff:203.0.113.7"},
			wantIP: "203.0.113.7", wantMark: true},
		{name: "wrong key", key: "wrong-key", visitors: []string{"203.0.113.7"}, wantIP: "76.76.21.21"},
		{name: "no key", visitors: []string{"203.0.113.7"}, wantIP: "76.76.21.21"},
		{name: "no trusted callers", noCallers: true, key: currentKey, visitors: []string{"203.0.113.7"},
			wantIP: "76.76.21.21"},
		{name: "valid key, no visitor", key: currentKey, wantIP: "76.76.21.21"},
		{name: "garbage", key: currentKey, visitors: []string{"not-an-ip"}, wantIP: "76.76.21.21",
			reason: "unparsable"},
		{name: "empty", key: currentKey, visitors: []string{""}, wantIP: "76.76.21.21", reason: "unparsable"},
		{name: "a list", key: currentKey, visitors: []string{"203.0.113.7, 198.51.100.1"},
			wantIP: "76.76.21.21", reason: "unparsable"},
		{name: "padded", key: currentKey, visitors: []string{" 203.0.113.7"}, wantIP: "76.76.21.21",
			reason: "unparsable"},
		{name: "with a zone", key: currentKey, visitors: []string{"2001:db8::7%eth0"}, wantIP: "76.76.21.21",
			reason: "unparsable"},
		{name: "repeated", key: currentKey, visitors: []string{"203.0.113.7", "203.0.113.8"},
			wantIP: "76.76.21.21", reason: "repeated"},
		{name: "private", key: currentKey, visitors: []string{"10.1.2.3"}, wantIP: "76.76.21.21",
			reason: "not_public"},
		{name: "private IPv6", key: currentKey, visitors: []string{"fd00::1"}, wantIP: "76.76.21.21",
			reason: "not_public"},
		{name: "loopback", key: currentKey, visitors: []string{"127.0.0.1"}, wantIP: "76.76.21.21",
			reason: "not_public"},
		{name: "IPv6 loopback", key: currentKey, visitors: []string{"::1"}, wantIP: "76.76.21.21",
			reason: "not_public"},
		{name: "unspecified", key: currentKey, visitors: []string{"0.0.0.0"}, wantIP: "76.76.21.21",
			reason: "not_public"},
		{name: "link-local", key: currentKey, visitors: []string{"fe80::1"}, wantIP: "76.76.21.21",
			reason: "not_public"},
		{name: "multicast", key: currentKey, visitors: []string{"224.0.0.1"}, wantIP: "76.76.21.21",
			reason: "not_public"},
		{name: "IPv4-mapped private", key: currentKey, visitors: []string{"::ffff:192.168.1.1"},
			wantIP: "76.76.21.21", reason: "not_public"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			callers := keys
			if tt.noCallers {
				callers = nil
			}
			var logs bytes.Buffer
			log := slog.New(slog.NewJSONHandler(&logs, nil))
			var gotIP string
			var gotMark bool
			next := http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				gotIP, gotMark = chimw.GetClientIP(r.Context()), mw.FromVisitor(r)
			})
			h := mw.ClientIP(0, log)(mw.Visitor(callers, log)(next))
			h.ServeHTTP(httptest.NewRecorder(), visitorRequest(vercelPeer, tt.key, tt.visitors...))

			if gotIP != tt.wantIP || gotMark != tt.wantMark {
				t.Errorf("client IP %q, visitor %v; want %q, %v", gotIP, gotMark, tt.wantIP, tt.wantMark)
			}
			checkVisitorLog(t, logs.String(), tt.reason, tt.visitors)
		})
	}
}

// checkVisitorLog checks that out is one visitor_ip_invalid line with reason,
// quoting none of visitors, or empty when reason is.
func checkVisitorLog(t *testing.T, out, reason string, visitors []string) {
	t.Helper()
	if reason == "" {
		if out != "" {
			t.Errorf("logged %s, want nothing", out)
		}
		return
	}
	want := fmt.Sprintf(`"event":"visitor_ip_invalid","reason":%q`, reason)
	if strings.Count(out, "\n") != 1 || !strings.Contains(out, want) {
		t.Errorf("logs = %s, want one line with %s", out, want)
	}
	for _, v := range visitors {
		if v != "" && strings.Contains(out, strings.TrimSpace(v)) {
			t.Errorf("the visitor value %q reached the logs: %s", v, out)
		}
	}
}

// visitorLimited is the public limiter as the router builds it: ClientIP,
// Visitor, then RateLimit with the web server as a trusted caller.
func visitorLimited(webServerLimit int, log *slog.Logger) http.Handler {
	keys := mw.ServerKeys([]string{currentKey})
	clock := &fakeClock{t: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)}
	limiter := mw.NewTestRateLimit(testLimit, 100, clock.now,
		mw.LogAs(mw.ClassIP, log), mw.TrustedCallers(keys, webServerLimit))
	return mw.ClientIP(0, log)(mw.Visitor(keys, log)(limiter(okHandler())))
}

// sendVisitor sends one request through h and returns its status.
func sendVisitor(h http.Handler, peer, key string, visitors ...string) *httptest.ResponseRecorder {
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, visitorRequest(peer, key, visitors...))
	return rr
}

// One visitor over their limit through the web server's key is refused while
// another visitor's render through the same key still gets through, and
// neither touches the web server's shared bucket.
func TestRateLimit_VisitorThroughServerKey(t *testing.T) {
	var logs bytes.Buffer
	h := visitorLimited(testLimit*10, slog.New(slog.NewJSONHandler(&logs, nil)))

	const a, b = "203.0.113.7", "198.51.100.9"
	for i := range testLimit {
		if rr := sendVisitor(h, vercelPeer, currentKey, a); rr.Code != http.StatusOK {
			t.Fatalf("visitor A request %d: status %d, want 200", i+1, rr.Code)
		}
	}
	rr := sendVisitor(h, vercelPeer, currentKey, a)
	if rr.Code != http.StatusTooManyRequests || rr.Header().Get("Retry-After") == "" {
		t.Fatalf("visitor A request %d: status %d, Retry-After %q; want 429 with Retry-After",
			testLimit+1, rr.Code, rr.Header().Get("Retry-After"))
	}
	if rr = sendVisitor(h, vercelPeer, currentKey, b); rr.Code != http.StatusOK {
		t.Fatalf("visitor B: status %d, want 200", rr.Code)
	}
	// Visitor A's own browser shares the bucket its renders used up.
	if rr = sendVisitor(h, a+":443", ""); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("visitor A's browser: status %d, want 429 from A's per-IP bucket", rr.Code)
	}
	// The web server's shared bucket and the Vercel address's bucket are untouched.
	for i := range testLimit {
		if rr = sendVisitor(h, vercelPeer, currentKey); rr.Code != http.StatusOK {
			t.Fatalf("keyed request %d without a visitor: status %d, want 200", i+1, rr.Code)
		}
		if rr = sendVisitor(h, vercelPeer, ""); rr.Code != http.StatusOK {
			t.Fatalf("unkeyed request %d from the Vercel address: status %d, want 200", i+1, rr.Code)
		}
	}

	got := rateLimitedClasses(t, &logs)
	if strings.Join(got, ",") != "ip,ip" {
		t.Errorf("rate_limited classes = %v, want [ip ip]", got)
	}
	if strings.Contains(logs.String(), a) || strings.Contains(logs.String(), b) {
		t.Errorf("a visitor IP reached the logs: %s", logs.String())
	}
}

// A keyed request without a visitor, or with an invalid one, counts in the
// web server's shared bucket, as before.
func TestRateLimit_KeyedWithoutVisitorSharesWebServerBucket(t *testing.T) {
	h := visitorLimited(testLimit, slog.New(slog.DiscardHandler))
	for i := range testLimit {
		visitors := []string{}
		if i%2 == 1 {
			visitors = append(visitors, "10.1.2.3")
		}
		if rr := sendVisitor(h, vercelPeer, currentKey, visitors...); rr.Code != http.StatusOK {
			t.Fatalf("keyed request %d: status %d, want 200", i+1, rr.Code)
		}
	}
	if rr := sendVisitor(h, "76.76.21.22:443", currentKey); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("keyed request %d: status %d, want 429 from the web-server bucket", testLimit+1, rr.Code)
	}
	// A valid visitor still gets through: their bucket is their own.
	if rr := sendVisitor(h, vercelPeer, currentKey, "203.0.113.7"); rr.Code != http.StatusOK {
		t.Fatalf("visitor: status %d, want 200", rr.Code)
	}
}

// Without a valid key, X-Visitor-IP is ignored: a client can't rotate it to
// get fresh buckets.
func TestRateLimit_VisitorIgnoredWithoutKey(t *testing.T) {
	h := visitorLimited(testLimit*10, slog.New(slog.DiscardHandler))
	for _, key := range []string{"", "wrong-key"} {
		peer := "192.0.2.20:1"
		if key != "" {
			peer = "192.0.2.21:1"
		}
		var rr *httptest.ResponseRecorder
		for i := range testLimit + 1 {
			rr = sendVisitor(h, peer, key, fmt.Sprintf("203.0.113.%d", i+1))
		}
		if rr.Code != http.StatusTooManyRequests {
			t.Fatalf("key %q: request %d = %d, want 429 from the client's own bucket", key, testLimit+1, rr.Code)
		}
	}
}

// IPv6 visitors are grouped by /64 like any other client.
func TestRateLimit_VisitorIPv6GroupedBy64(t *testing.T) {
	h := visitorLimited(testLimit*10, slog.New(slog.DiscardHandler))
	for i := range testLimit {
		sendVisitor(h, vercelPeer, currentKey, fmt.Sprintf("2001:db8:aa:bb::%x", i+1))
	}
	if rr := sendVisitor(h, vercelPeer, currentKey, "2001:db8:aa:bb:ffff::1"); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("same /64: status %d, want 429", rr.Code)
	}
	if rr := sendVisitor(h, vercelPeer, currentKey, "2001:db8:aa:bc::1"); rr.Code != http.StatusOK {
		t.Fatalf("next /64: status %d, want 200", rr.Code)
	}
}
