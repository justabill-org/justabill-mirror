package middleware_test

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/justabill-org/justabill/api/internal/auth"
	"github.com/justabill-org/justabill/api/internal/auth/authtest"
	mw "github.com/justabill-org/justabill/api/internal/middleware"
	"github.com/justabill-org/justabill/db/model"
)

// countingUsers is a UserLookup over a map that counts lookups.
type countingUsers struct {
	byUID   map[string]string
	err     error
	lookups int
}

func (c *countingUsers) GetByAuthUID(_ context.Context, uid string) (*model.User, error) {
	c.lookups++
	if c.err != nil {
		return nil, c.err
	}
	id, ok := c.byUID[uid]
	if !ok {
		return nil, nil //nolint:nilnil // matches the repository contract
	}
	return &model.User{ID: id, AuthUID: uid}, nil
}

// seen records what the auth middleware put in the context.
type seen struct {
	called    bool
	userID    string
	principal auth.Principal
	hasPrinc  bool
}

func newAuth(t *testing.T) (*mw.Auth, *authtest.Fake, *countingUsers) {
	t.Helper()
	fake := authtest.New()
	fake.Add("alice-token", auth.Principal{UID: "uid-alice", Provider: "google.com", AuthTime: time.Now()})
	fake.Add("bob-token", auth.Principal{UID: "uid-bob", Provider: "microsoft.com"})
	users := &countingUsers{byUID: map[string]string{"uid-alice": "user-alice"}}
	return mw.NewAuth(fake, users, slog.New(slog.DiscardHandler)), fake, users
}

func serve(a *mw.Auth, header string) (*httptest.ResponseRecorder, *seen) {
	s := &seen{}
	h := a.Handler(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.called = true
		s.userID = mw.UserIDFromContext(r.Context())
		s.principal, s.hasPrinc = mw.PrincipalFromContext(r.Context())
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	if header != "" {
		req.Header.Set("Authorization", header)
	}
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	return rr, s
}

func TestAuthRejects(t *testing.T) {
	tests := []struct {
		name     string
		header   string
		wantCode string
	}{
		{"not bearer", "Basic dXNlcjpwYXNz", "invalid_token"},
		{"bearer without token", "Bearer ", "invalid_token"},
		{"no scheme", "alice-token", "invalid_token"},
		{"unknown token", "Bearer forged", "invalid_token"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a, _, _ := newAuth(t)
			rr, s := serve(a, tt.header)
			if rr.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", rr.Code)
			}
			if s.called {
				t.Error("next handler ran for a rejected token")
			}
			if !strings.Contains(rr.Body.String(), `"code":"`+tt.wantCode+`"`) {
				t.Errorf("body = %s, want code %s", rr.Body.String(), tt.wantCode)
			}
		})
	}
}

func TestAuthNoHeaderIsAnonymous(t *testing.T) {
	a, _, users := newAuth(t)
	rr, s := serve(a, "")
	if rr.Code != http.StatusOK || !s.called {
		t.Fatalf("status = %d, called = %v; want the request to continue", rr.Code, s.called)
	}
	if s.userID != "" || s.hasPrinc {
		t.Errorf("anonymous request got user %q, principal %v", s.userID, s.hasPrinc)
	}
	if users.lookups != 0 {
		t.Errorf("lookups = %d, want 0", users.lookups)
	}
	if got := rr.Header().Get("Cache-Control"); got != "" {
		t.Errorf("Cache-Control = %q on an anonymous request, want none", got)
	}
}

func TestAuthVerifierOutageFailsClosed(t *testing.T) {
	a, fake, _ := newAuth(t)
	fake.Err = errors.New("fetch keys: connection refused")
	rr, s := serve(a, "Bearer alice-token")
	if rr.Code != http.StatusServiceUnavailable || s.called {
		t.Errorf("status = %d, called = %v; want 503 and no call", rr.Code, s.called)
	}
}

func TestAuthKnownUser(t *testing.T) {
	a, _, users := newAuth(t)
	rr, s := serve(a, "Bearer alice-token")
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rr.Code)
	}
	if s.userID != "user-alice" || s.principal.UID != "uid-alice" || s.principal.Provider != "google.com" {
		t.Errorf("context user %q, principal %+v", s.userID, s.principal)
	}
	if got := rr.Header().Get("Cache-Control"); got != "private, no-store" {
		t.Errorf("Cache-Control = %q, want private, no-store", got)
	}

	serve(a, "Bearer alice-token")
	if users.lookups != 1 {
		t.Errorf("lookups after two requests = %d, want 1 (cached)", users.lookups)
	}
	a.Forget("uid-alice")
	serve(a, "bearer alice-token")
	if users.lookups != 2 {
		t.Errorf("lookups after Forget = %d, want 2", users.lookups)
	}
}

func TestAuthUnknownUserIsNotCached(t *testing.T) {
	a, _, users := newAuth(t)
	_, s := serve(a, "Bearer bob-token")
	if !s.called || !s.hasPrinc || s.userID != "" {
		t.Fatalf("called %v, principal %v, user %q; want a principal without a user", s.called, s.hasPrinc, s.userID)
	}
	users.byUID["uid-bob"] = "user-bob" // POST /me created the account
	_, s = serve(a, "Bearer bob-token")
	if s.userID != "user-bob" {
		t.Errorf("user after account creation = %q, want user-bob", s.userID)
	}
}

func TestAuthLookupError(t *testing.T) {
	a, _, users := newAuth(t)
	users.err = errors.New("spanner unavailable")
	rr, s := serve(a, "Bearer alice-token")
	if rr.Code != http.StatusInternalServerError || s.called {
		t.Errorf("status = %d, called = %v; want 500 and no call", rr.Code, s.called)
	}
}

func TestRequireAuth(t *testing.T) {
	tests := []struct {
		name   string
		ctx    func(context.Context) context.Context
		status int
	}{
		{"anonymous", func(ctx context.Context) context.Context { return ctx }, http.StatusUnauthorized},
		{"signed in without account", func(ctx context.Context) context.Context {
			return mw.ContextWithPrincipal(ctx, auth.Principal{UID: "uid-bob"})
		}, http.StatusForbidden},
		{"user", func(ctx context.Context) context.Context {
			return mw.ContextWithUserID(mw.ContextWithPrincipal(ctx, auth.Principal{UID: "u"}), "user-1")
		}, http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := mw.RequireAuth(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req.WithContext(tt.ctx(req.Context())))
			if rr.Code != tt.status {
				t.Errorf("status = %d, want %d", rr.Code, tt.status)
			}
		})
	}
}

func TestRequirePrincipal(t *testing.T) {
	h := mw.RequirePrincipal(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusUnauthorized {
		t.Errorf("anonymous status = %d, want 401", rr.Code)
	}

	rr = httptest.NewRecorder()
	h.ServeHTTP(rr, req.WithContext(mw.ContextWithPrincipal(req.Context(), auth.Principal{UID: "u"})))
	if rr.Code != http.StatusOK {
		t.Errorf("signed-in status = %d, want 200", rr.Code)
	}
}
