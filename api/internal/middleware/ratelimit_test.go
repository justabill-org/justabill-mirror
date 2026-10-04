package middleware_test

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	mw "github.com/justabill-org/justabill/api/internal/middleware"
)

const testLimit = 5

func okHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
}

// limited wraps a limiter in ClientIP, as the router does.
func limited(hops int, limiter func(http.Handler) http.Handler) http.Handler {
	return mw.ClientIP(hops, slog.New(slog.DiscardHandler))(limiter(okHandler()))
}

// sendFrom sends one request with the given peer address and X-Forwarded-For.
func sendFrom(h http.Handler, remoteAddr, xff string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = remoteAddr
	if xff != "" {
		req.Header.Set("X-Forwarded-For", xff)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// exhaust uses up the limit for one client and checks the next request is
// refused.
func exhaust(t *testing.T, h http.Handler, remoteAddr string) *httptest.ResponseRecorder {
	t.Helper()
	for i := range testLimit {
		if rr := sendFrom(h, remoteAddr, ""); rr.Code != http.StatusOK {
			t.Fatalf("request %d: expected status 200, got %d", i+1, rr.Code)
		}
	}
	rr := sendFrom(h, remoteAddr, "")
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("request %d: expected status 429, got %d", testLimit+1, rr.Code)
	}
	return rr
}

func TestRateLimit_OverLimit(t *testing.T) {
	rr := exhaust(t, limited(0, mw.RateLimit(testLimit)), "10.0.0.1:9999")

	var body map[string]string
	if err := json.NewDecoder(rr.Body).Decode(&body); err != nil {
		t.Fatalf("failed to decode response body: %v", err)
	}
	if body["error"] != "rate limit exceeded" {
		t.Fatalf("expected error 'rate limit exceeded', got %q", body["error"])
	}
	secs, err := strconv.Atoi(rr.Header().Get("Retry-After"))
	if err != nil || secs < 1 || secs > 60 {
		t.Fatalf("expected Retry-After between 1 and 60 seconds, got %q", rr.Header().Get("Retry-After"))
	}
}

func TestRateLimit_SeparateIPs(t *testing.T) {
	h := limited(0, mw.RateLimit(testLimit))
	exhaust(t, h, "192.168.1.1:1111")
	if rr := sendFrom(h, "192.168.1.2:2222", ""); rr.Code != http.StatusOK {
		t.Fatalf("second IP: expected status 200, got %d", rr.Code)
	}
}

func TestRateLimit_IgnoresSpoofedHeaders(t *testing.T) {
	h := limited(0, mw.RateLimit(testLimit))

	// A client rotating X-Real-IP and X-Forwarded-For must still share one bucket.
	var rr *httptest.ResponseRecorder
	for i := range testLimit + 1 {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "10.0.0.2:5555"
		ip := fmt.Sprintf("%d.%d.%d.%d", i+1, i+1, i+1, i+1)
		req.Header.Set("X-Real-IP", ip)
		req.Header.Set("True-Client-Ip", ip)
		req.Header.Set("X-Forwarded-For", ip)
		rr = httptest.NewRecorder()
		h.ServeHTTP(rr, req)
	}
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("expected status 429 after %d requests, got %d", testLimit+1, rr.Code)
	}
}

func TestRateLimit_BehindLoadBalancer(t *testing.T) {
	h := limited(2, mw.RateLimit(testLimit))

	// Spoofing the leftmost entry doesn't give the client a fresh bucket.
	for i := range testLimit {
		spoofed := fmt.Sprintf("6.6.6.%d, 198.51.100.1, %s", i, lbIP)
		if rr := sendFrom(h, "10.0.0.3:443", spoofed); rr.Code != http.StatusOK {
			t.Fatalf("request %d: expected status 200, got %d", i+1, rr.Code)
		}
	}
	if rr := sendFrom(h, "10.0.0.3:443", "7.7.7.7, 198.51.100.1, "+lbIP); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("first client: expected status 429, got %d", rr.Code)
	}
	// Another client behind the same load balancer has its own bucket.
	if rr := sendFrom(h, "10.0.0.3:443", "198.51.100.2, "+lbIP); rr.Code != http.StatusOK {
		t.Fatalf("second client: expected status 200, got %d", rr.Code)
	}
}

func TestRateLimit_MissingClientIPSharesUnknownBucket(t *testing.T) {
	h := limited(2, mw.RateLimit(testLimit))

	// Chains too short for the hop count never fall back to RemoteAddr or a
	// header, so different peers and spoofed entries all share one bucket.
	for i := range testLimit {
		if rr := sendFrom(
			h,
			fmt.Sprintf("10.0.1.%d:443", i),
			fmt.Sprintf("198.51.100.%d", i),
		); rr.Code != http.StatusOK {
			t.Fatalf("request %d: expected status 200, got %d", i+1, rr.Code)
		}
	}
	if rr := sendFrom(h, "10.0.1.99:443", ""); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("expected the shared unknown bucket to be exhausted, got %d", rr.Code)
	}
	// A request with a proper chain is unaffected.
	if rr := sendFrom(h, "10.0.0.3:443", "198.51.100.50, "+lbIP); rr.Code != http.StatusOK {
		t.Fatalf("valid client: expected status 200, got %d", rr.Code)
	}
}

func TestRateLimit_GroupsIPv6By64(t *testing.T) {
	h := limited(0, mw.RateLimit(testLimit))

	for i := range testLimit {
		addr := fmt.Sprintf("[2001:db8:aa:bb::%x]:443", i+1)
		if rr := sendFrom(h, addr, ""); rr.Code != http.StatusOK {
			t.Fatalf("request %d: expected status 200, got %d", i+1, rr.Code)
		}
	}
	if rr := sendFrom(h, "[2001:db8:aa:bb:ffff::1]:443", ""); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("same /64: expected status 429, got %d", rr.Code)
	}
	if rr := sendFrom(h, "[2001:db8:aa:bc::1]:443", ""); rr.Code != http.StatusOK {
		t.Fatalf("next /64: expected status 200, got %d", rr.Code)
	}
}

func TestRateLimit_V4MappedSharesIPv4Bucket(t *testing.T) {
	h := limited(0, mw.RateLimit(testLimit))
	for range testLimit {
		sendFrom(h, "192.0.2.4:1", "")
	}
	if rr := sendFrom(h, "[::ffff:192.0.2.4]:1", ""); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("v4-mapped: expected status 429, got %d", rr.Code)
	}
}

// fakeClock is a clock the test moves by hand.
type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func TestRateLimit_WindowResetAndRetryAfter(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)}
	h := limited(0, mw.NewTestRateLimit(testLimit, 100, clock.now))

	for range testLimit {
		sendFrom(h, "192.0.2.1:1", "")
	}
	clock.t = clock.t.Add(20*time.Second + 500*time.Millisecond)
	rr := sendFrom(h, "192.0.2.1:1", "")
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("expected status 429, got %d", rr.Code)
	}
	if got := rr.Header().Get("Retry-After"); got != "40" {
		t.Fatalf("expected Retry-After 40 (39.5 s rounded up), got %q", got)
	}

	clock.t = clock.t.Add(40 * time.Second)
	if rr = sendFrom(h, "192.0.2.1:1", ""); rr.Code != http.StatusOK {
		t.Fatalf("after the window: expected status 200, got %d", rr.Code)
	}
}

func TestRateLimit_EvictsOldestWindowAtCap(t *testing.T) {
	clock := &fakeClock{t: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)}
	const maxKeys = 3
	h := limited(0, mw.NewTestRateLimit(testLimit, maxKeys, clock.now))

	exhaust(t, h, "192.0.2.1:1") // oldest window
	for i := 2; i <= maxKeys; i++ {
		clock.t = clock.t.Add(time.Second)
		exhaust(t, h, fmt.Sprintf("192.0.2.%d:1", i))
	}

	// A new client at the cap evicts the oldest window, 192.0.2.1's, and only it.
	clock.t = clock.t.Add(time.Second)
	sendFrom(h, "192.0.2.100:1", "")
	if rr := sendFrom(h, "192.0.2.1:1", ""); rr.Code != http.StatusOK {
		t.Fatalf("evicted client: expected status 200, got %d", rr.Code)
	}
	// Re-adding 192.0.2.1 evicted the next oldest, 192.0.2.2; 192.0.2.3 is still limited.
	if rr := sendFrom(h, "192.0.2.3:1", ""); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("newer client: expected status 429, got %d", rr.Code)
	}
}

func TestRateLimit_RouteLimiterIsIndependent(t *testing.T) {
	public := mw.RateLimit(testLimit * 2)
	route := mw.RateLimit(testLimit)
	h := mw.ClientIP(0, slog.New(slog.DiscardHandler))(public(route(okHandler())))
	other := mw.ClientIP(0, slog.New(slog.DiscardHandler))(public(okHandler()))

	exhaust(t, h, "192.0.2.9:1")
	// The tighter route limit is spent, but the public bucket still has room.
	if rr := sendFrom(other, "192.0.2.9:1", ""); rr.Code != http.StatusOK {
		t.Fatalf("other route: expected status 200, got %d", rr.Code)
	}
}
