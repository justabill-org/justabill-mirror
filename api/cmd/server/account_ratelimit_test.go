package main

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/justabill-org/justabill/api/internal/auth"
	"github.com/justabill-org/justabill/api/internal/auth/authtest"
	"github.com/justabill-org/justabill/api/internal/handler"
	mw "github.com/justabill-org/justabill/api/internal/middleware"
	"github.com/justabill-org/justabill/db/model"
)

// sharedIP is one address many people share, such as a carrier NAT's, and
// otherIP another client's.
const (
	sharedIP = "203.0.113.9"
	otherIP  = "198.51.100.4"
)

// accountLimitRouter is the router with accounts on (App Check off) and two
// people with accounts, alice and bob. Its rate-limit logs go to logs.
func accountLimitRouter(t *testing.T, logs *bytes.Buffer) http.Handler {
	t.Helper()
	log := slog.New(slog.NewJSONHandler(logs, nil))
	users := &memUsers{byUID: map[string]*model.User{
		"uid-alice": {ID: "u-alice", AuthUID: "uid-alice"},
		"uid-bob":   {ID: "u-bob", AuthUID: "uid-bob"},
	}}
	fake := authtest.New()
	fake.Add("alice", auth.Principal{UID: "uid-alice", Provider: "google.com", AuthTime: time.Now()})
	fake.Add("bob", auth.Principal{UID: "uid-bob", Provider: "apple.com", AuthTime: time.Now()})
	authn := mw.NewAuth(fake, users, log)
	accounts := &accountMiddleware{authn: authn, appCheck: mw.AppCheck(nil, auth.AppCheckOff, log)}
	h := handler.New(nil, handler.WithUsers(users), handler.WithAccounts(fake, authn.Forget))
	h.SetLogger(log)
	return buildRouter(h, log, accounts, devEdge(t, 0))
}

// sendAccount sends one request from client (an IP), with token as a Bearer
// token unless it's empty.
func sendAccount(r http.Handler, client, method, path, token string) *httptest.ResponseRecorder {
	var body string
	if method == http.MethodPost {
		body = `{"vote":"yea"}`
	}
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	req.RemoteAddr = client + ":443"
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

// swipe has token vote on n bills from client and fails on any 429.
func swipe(t *testing.T, r http.Handler, client, token string, n int) {
	t.Helper()
	for i := range n {
		path := fmt.Sprintf("/api/v1/bills/119-hr-%d/vote", i+1)
		if rr := sendAccount(r, client, http.MethodPost, path, token); rr.Code != http.StatusOK {
			t.Fatalf("%s's vote %d = %d, want 200: %s", token, i+1, rr.Code, rr.Body)
		}
	}
}

// One account loads its votes and votes on 60 bills in a minute, and a second
// account behind the same IP does the same: nothing is refused, and none of
// it counts against the IP's public budget (#494).
func TestSignedInVotesAreLimitedPerAccount(t *testing.T) {
	r := accountLimitRouter(t, &bytes.Buffer{})

	// The most votes the web loads: 101 pages of 100 (web/src/lib/votes/account.ts).
	for offset := 0; offset <= 10_000; offset += 100 {
		path := fmt.Sprintf("/api/v1/me/votes?limit=100&offset=%d", offset)
		if rr := sendAccount(r, sharedIP, http.MethodGet, path, "alice"); rr.Code != http.StatusOK {
			t.Fatalf("GET %s = %d, want 200: %s", path, rr.Code, rr.Body)
		}
	}
	swipe(t, r, sharedIP, "alice", 60)
	swipe(t, r, sharedIP, "bob", 60)

	if rr := sendAccount(r, sharedIP, http.MethodGet, "/api/v1/not-a-route", ""); rr.Code != http.StatusNotFound {
		t.Errorf("public request after 221 signed-in ones = %d, want 404: they spent the IP's public budget", rr.Code)
	}
}

// Each account has its own ceiling, wherever its requests come from.
func TestAccountLimitFollowsTheAccount(t *testing.T) {
	var logs bytes.Buffer
	r := accountLimitRouter(t, &logs)
	for i := range accountUserRateLimit {
		if rr := sendAccount(r, sharedIP, http.MethodGet, "/api/v1/me", "alice"); rr.Code != http.StatusOK {
			t.Fatalf("alice's request %d = %d, want 200", i+1, rr.Code)
		}
	}
	rr := sendAccount(r, sharedIP, http.MethodGet, "/api/v1/me", "alice")
	if rr.Code != http.StatusTooManyRequests || rr.Header().Get("Retry-After") == "" {
		t.Fatalf("alice's request %d = %d with Retry-After %q, want 429 with Retry-After",
			accountUserRateLimit+1, rr.Code, rr.Header().Get("Retry-After"))
	}
	if rr = sendAccount(r, otherIP, http.MethodGet, "/api/v1/me", "alice"); rr.Code != http.StatusTooManyRequests {
		t.Errorf("alice from another IP = %d, want 429: her bucket is her account's", rr.Code)
	}
	if rr = sendAccount(r, sharedIP, http.MethodGet, "/api/v1/me", "bob"); rr.Code != http.StatusOK {
		t.Errorf("bob behind alice's IP = %d, want 200: he has his own budget", rr.Code)
	}
	if !strings.Contains(logs.String(), `"event":"rate_limited","class":"`+mw.ClassUser+`"`) {
		t.Errorf("no rate_limited log with class %q:\n%s", mw.ClassUser, logs.String())
	}
}

// Requests without a valid token keep the per-IP ceiling: anonymous ones and
// bad tokens alike.
func TestAccountRoutesLimitRequestsWithoutAValidTokenByIP(t *testing.T) {
	var logs bytes.Buffer
	r := accountLimitRouter(t, &logs)
	for i := range accountIPRateLimit {
		token := ""
		if i%2 == 0 {
			token = "forged"
		}
		if rr := sendAccount(r, sharedIP, http.MethodGet, "/api/v1/me", token); rr.Code != http.StatusUnauthorized {
			t.Fatalf("request %d (token %q) = %d, want 401", i+1, token, rr.Code)
		}
	}
	for _, token := range []string{"forged", ""} {
		if rr := sendAccount(r, sharedIP, http.MethodGet, "/api/v1/me", token); rr.Code != http.StatusTooManyRequests {
			t.Errorf("token %q over the per-IP limit = %d, want 429", token, rr.Code)
		}
	}
	if rr := sendAccount(r, otherIP, http.MethodGet, "/api/v1/me", "forged"); rr.Code != http.StatusUnauthorized {
		t.Errorf("a forged token from another IP = %d, want 401", rr.Code)
	}
	if !strings.Contains(logs.String(), `"event":"rate_limited","class":"`+mw.ClassAccount+`"`) {
		t.Errorf("no rate_limited log with class %q:\n%s", mw.ClassAccount, logs.String())
	}
}
