package middleware_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	mw "github.com/justabill-org/justabill/api/internal/middleware"
)

const (
	currentKey = "current-key-0123456789abcdefghijklmnop"
	nextKey    = "next-key-0123456789abcdefghijklmnopqrst"
)

func TestServerKeys(t *testing.T) {
	both := mw.ServerKeys([]string{currentKey, nextKey})
	tests := []struct {
		name     string
		classify mw.CallerClass
		header   *string
		want     string
	}{
		{name: "current key", classify: both, header: new(currentKey), want: mw.ClassWebServer},
		{name: "next key during a rotation", classify: both, header: new(nextKey), want: mw.ClassWebServer},
		{name: "wrong key", classify: both, header: new("not-the-key"), want: mw.ClassIP},
		{name: "a prefix of the key", classify: both, header: new(currentKey[:10]), want: mw.ClassIP},
		{name: "the key plus more", classify: both, header: new(currentKey + "x"), want: mw.ClassIP},
		{name: "empty header", classify: both, header: new(""), want: mw.ClassIP},
		{name: "no header", classify: both, want: mw.ClassIP},
		{name: "no keys: feature off", classify: mw.ServerKeys(nil), header: new(currentKey), want: mw.ClassIP},
		{name: "blank keys are ignored", classify: mw.ServerKeys([]string{""}), header: new(""), want: mw.ClassIP},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			if tt.header != nil {
				req.Header.Set(mw.ServerKeyHeader, *tt.header)
			}
			if got := tt.classify(req); got != tt.want {
				t.Errorf("class = %q, want %q", got, tt.want)
			}
		})
	}
}

// sendWithKey sends one request from remoteAddr with key as X-Server-Key.
func sendWithKey(h http.Handler, remoteAddr, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = remoteAddr
	req.Header.Set(mw.ServerKeyHeader, key)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr
}

// rateLimitedClasses returns the class of every event=rate_limited line in logs.
func rateLimitedClasses(t *testing.T, logs *bytes.Buffer) []string {
	t.Helper()
	var classes []string
	for line := range strings.SplitSeq(strings.TrimSpace(logs.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		if rec["event"] == "rate_limited" {
			c, _ := rec["class"].(string)
			classes = append(classes, c)
		}
	}
	return classes
}

// The web app's server, on an IP whose per-IP bucket is full, still gets
// through with its key, and runs out only at its own limit; a wrong key is
// limited by IP.
func TestRateLimit_TrustedCallerHasItsOwnBucket(t *testing.T) {
	const webServerLimit = testLimit * 2
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	clock := &fakeClock{t: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)}
	h := limited(0, mw.NewTestRateLimit(testLimit, 100, clock.now,
		mw.LogAs(mw.ClassIP, log),
		mw.TrustedCallers(mw.ServerKeys([]string{currentKey}), webServerLimit)))

	const vercel = "76.76.21.21:443"
	exhaust(t, h, vercel)
	if rr := sendWithKey(h, vercel, "wrong-key"); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("wrong key: status %d, want 429 from the per-IP bucket", rr.Code)
	}
	for i := range webServerLimit {
		if rr := sendWithKey(h, vercel, currentKey); rr.Code != http.StatusOK {
			t.Fatalf("keyed request %d: status %d, want 200", i+1, rr.Code)
		}
	}
	rr := sendWithKey(h, vercel, currentKey)
	if rr.Code != http.StatusTooManyRequests || rr.Header().Get("Retry-After") == "" {
		t.Fatalf("keyed request %d: status %d, Retry-After %q; want 429 with Retry-After",
			webServerLimit+1, rr.Code, rr.Header().Get("Retry-After"))
	}
	// The web-server bucket is shared by the class, not per IP: another pod
	// or egress IP with the key draws on the same bucket.
	if rr = sendWithKey(h, "76.76.21.22:443", currentKey); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("keyed request from a second IP: status %d, want 429", rr.Code)
	}
	clock.t = clock.t.Add(time.Minute)
	if rr = sendWithKey(h, vercel, currentKey); rr.Code != http.StatusOK {
		t.Fatalf("after the window: status %d, want 200", rr.Code)
	}

	got := rateLimitedClasses(t, &logs)
	want := []string{mw.ClassIP, mw.ClassIP, mw.ClassWebServer, mw.ClassWebServer}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("rate_limited classes = %v, want %v", got, want)
	}
	if strings.Contains(logs.String(), currentKey) || strings.Contains(logs.String(), "wrong-key") {
		t.Errorf("a server key reached the logs: %s", logs.String())
	}
}

// A limiter named with LogAs logs its refusals under that class, and one
// without LogAs logs nothing.
func TestRateLimit_LogAs(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	exhaust(t, limited(0, mw.RateLimit(testLimit, mw.LogAs(mw.ClassReps, log))), "192.0.2.10:1")
	exhaust(t, limited(0, mw.RateLimit(testLimit)), "192.0.2.10:1")

	if got := rateLimitedClasses(t, &logs); len(got) != 1 || got[0] != mw.ClassReps {
		t.Errorf("rate_limited classes = %v, want [reps]", got)
	}
	if strings.Contains(logs.String(), "192.0.2.10") {
		t.Errorf("the client IP reached the logs: %s", logs.String())
	}
}
