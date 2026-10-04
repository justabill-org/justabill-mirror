package middleware_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/justabill-org/justabill/api/internal/auth"
	"github.com/justabill-org/justabill/api/internal/auth/authtest"
	mw "github.com/justabill-org/justabill/api/internal/middleware"
)

const (
	testPerUser = 4
	testPerIP   = 2
)

// signedIn is a SignedInRateLimit over the fake tokens "alice" and "bob"
// (bob has no account yet), with a clock the test moves. verified counts the
// tokens authn was asked to check.
type signedIn struct {
	h        http.Handler
	now      time.Time
	verified int
}

func newSignedIn(t *testing.T) *signedIn {
	t.Helper()
	fake := authtest.New()
	fake.Add("alice", auth.Principal{UID: "uid-alice", Provider: "google.com"})
	fake.Add("bob", auth.Principal{UID: "uid-bob", Provider: "apple.com"})
	users := &countingUsers{byUID: map[string]string{"uid-alice": "user-alice"}}
	log := slog.New(slog.DiscardHandler)
	authn := mw.NewAuth(fake, users, log)

	s := &signedIn{now: time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)}
	counted := func(next http.Handler) http.Handler {
		inner := authn.Handler(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Header.Get("Authorization") != "" {
				s.verified++
			}
			inner.ServeHTTP(w, r)
		})
	}
	limit := mw.NewTestSignedInRateLimit(counted, testPerUser, testPerIP, func() time.Time { return s.now }, log)
	s.h = mw.ClientIP(0, log)(limit(okHandler()))
	return s
}

// send sends one request from ip with token (none when empty) and returns
// its status.
func (s *signedIn) send(ip, token string) int {
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.RemoteAddr = ip + ":1111"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	s.h.ServeHTTP(rr, req)
	return rr.Code
}

func TestSignedInRateLimitKeysVerifiedRequestsByIdentity(t *testing.T) {
	s := newSignedIn(t)
	for i := range testPerUser {
		if got := s.send("10.0.0.1", "alice"); got != http.StatusOK {
			t.Fatalf("alice's request %d = %d, want 200", i+1, got)
		}
	}
	if got := s.send("10.0.0.2", "alice"); got != http.StatusTooManyRequests {
		t.Errorf("alice from a second IP = %d, want 429: the bucket is hers, not the IP's", got)
	}
	// bob has a verified token but no account yet: keyed by his auth UID.
	for i := range testPerUser {
		if got := s.send("10.0.0.1", "bob"); got != http.StatusOK {
			t.Fatalf("bob's request %d behind alice's IP = %d, want 200", i+1, got)
		}
	}
	if got := s.send("10.0.0.1", "bob"); got != http.StatusTooManyRequests {
		t.Errorf("bob's request %d = %d, want 429", testPerUser+1, got)
	}
	// None of that spent the IP's bucket for requests without a valid token.
	if got := s.send("10.0.0.1", ""); got != http.StatusOK {
		t.Errorf("anonymous request after the signed-in ones = %d, want 200", got)
	}

	s.now = s.now.Add(time.Minute)
	if got := s.send("10.0.0.2", "alice"); got != http.StatusOK {
		t.Errorf("alice a minute later = %d, want 200", got)
	}
}

func TestSignedInRateLimitKeysOtherRequestsByIP(t *testing.T) {
	s := newSignedIn(t)
	if got := s.send("10.0.0.1", ""); got != http.StatusOK {
		t.Fatalf("anonymous request = %d, want 200", got)
	}
	if got := s.send("10.0.0.1", "forged"); got != http.StatusUnauthorized {
		t.Fatalf("forged token = %d, want 401", got)
	}
	verified := s.verified
	for _, token := range []string{"", "forged", "alice"} {
		if got := s.send("10.0.0.1", token); got != http.StatusTooManyRequests {
			t.Errorf("token %q from a full IP = %d, want 429", token, got)
		}
	}
	if s.verified != verified {
		t.Errorf("authn checked %d tokens from a full IP, want none", s.verified-verified)
	}
	if got := s.send("10.0.0.2", "forged"); got != http.StatusUnauthorized {
		t.Errorf("forged token from another IP = %d, want 401", got)
	}

	s.now = s.now.Add(time.Minute)
	if got := s.send("10.0.0.1", "alice"); got != http.StatusOK {
		t.Errorf("alice a minute later = %d, want 200", got)
	}
}

func TestSignedInRateLimitLogsClasses(t *testing.T) {
	var logs bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&logs, nil))
	fake := authtest.New()
	fake.Add("alice", auth.Principal{UID: "uid-alice"})
	authn := mw.NewAuth(fake, &countingUsers{byUID: map[string]string{}}, slog.New(slog.DiscardHandler))
	h := limited(0, mw.SignedInRateLimit(authn.Handler, 1, 1, log))
	for _, token := range []string{"alice", "alice", "", ""} {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.RemoteAddr = "10.0.0.1:1111"
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		h.ServeHTTP(httptest.NewRecorder(), req)
	}
	got, want := rateLimitedClasses(t, &logs), []string{mw.ClassUser, mw.ClassAccount}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("rate_limited classes = %v, want %v", got, want)
	}
	if strings.Contains(logs.String(), "10.0.0.1") || strings.Contains(logs.String(), "uid-alice") {
		t.Errorf("a client IP or auth UID reached the logs: %s", logs.String())
	}
}

// A concurrent burst of bad tokens from one IP gets at most perIP of them
// verified, even while every verification is still running.
func TestSignedInRateLimitReservesBeforeVerifying(t *testing.T) {
	const burst = 10
	var entered, refused atomic.Int32
	release := make(chan struct{})
	slowAuthn := func(http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			entered.Add(1)
			<-release
			w.WriteHeader(http.StatusUnauthorized)
		})
	}
	log := slog.New(slog.DiscardHandler)
	h := limited(0, mw.SignedInRateLimit(slowAuthn, testPerUser, testPerIP, log))

	var wg sync.WaitGroup
	for range burst {
		wg.Go(func() {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = "10.0.0.1:1111"
			req.Header.Set("Authorization", "Bearer forged")
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code == http.StatusTooManyRequests {
				refused.Add(1)
			}
		})
	}
	deadline := time.Now().Add(5 * time.Second)
	for entered.Load()+refused.Load() < burst && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	got := entered.Load()
	close(release)
	wg.Wait()
	if got != testPerIP || refused.Load() != burst-testPerIP {
		t.Errorf("burst of %d: %d verified, %d refused; want %d verified, the rest refused",
			burst, got, refused.Load(), testPerIP)
	}
}
