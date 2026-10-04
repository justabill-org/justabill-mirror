package main

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/spf13/viper"

	"github.com/justabill-org/justabill/api/internal/auth"
	"github.com/justabill-org/justabill/api/internal/auth/authtest"
	"github.com/justabill-org/justabill/api/internal/handler"
	mw "github.com/justabill-org/justabill/api/internal/middleware"
	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
)

// memUsers implements the UserRepo methods the account routes use. Any
// other method panics on the nil embedded interface.
type memUsers struct {
	repository.UserRepo

	byUID map[string]*model.User
}

func (m *memUsers) CreateForAuthUID(_ context.Context, id, uid, provider string) (*model.User, error) {
	if u, ok := m.byUID[uid]; ok {
		return u, nil
	}
	m.byUID[uid] = &model.User{ID: id, AuthUID: uid, SignInProvider: &provider}
	return m.byUID[uid], nil
}

func (m *memUsers) GetByAuthUID(_ context.Context, uid string) (*model.User, error) {
	return m.byUID[uid], nil
}

func (m *memUsers) GetByID(_ context.Context, id string) (*model.User, error) {
	for _, u := range m.byUID {
		if u.ID == id {
			return u, nil
		}
	}
	return nil, nil //nolint:nilnil // matches the repository contract
}

func (m *memUsers) Delete(_ context.Context, id string) error {
	for uid, u := range m.byUID {
		if u.ID == id {
			delete(m.byUID, uid)
		}
	}
	return nil
}

func (m *memUsers) CastVote(_ context.Context, _, _, _ string, _ model.VoteChecks) error { return nil }

func (m *memUsers) GetVotes(
	_ context.Context, _ string, _ model.ListParams,
) (*model.ListResult[model.UserVote], error) {
	return &model.ListResult[model.UserVote]{}, nil
}

func (m *memUsers) ImportVotes(
	_ context.Context, _ string, votes []model.UserVote, _ model.VoteChecks,
) (model.ImportResult, error) {
	return model.ImportResult{Imported: len(votes)}, nil
}

func (m *memUsers) DeleteVote(_ context.Context, _, _ string) error { return nil }

func newTestRouter(t *testing.T, accountsOn bool) http.Handler {
	t.Helper()
	return newTestRouterWithAppCheck(t, accountsOn, auth.AppCheckOff)
}

// goodAppCheck accepts only the App Check token "good".
type goodAppCheck struct{}

func (goodAppCheck) VerifyAppCheck(_ context.Context, token string) error {
	if token == "good" {
		return nil
	}
	return auth.ErrInvalidAppCheckToken
}

func newTestRouterWithAppCheck(t *testing.T, accountsOn bool, mode auth.AppCheckMode) http.Handler {
	t.Helper()
	log := slog.New(slog.DiscardHandler)
	users := &memUsers{byUID: map[string]*model.User{}}
	fake := authtest.New()
	fake.Add("alice", auth.Principal{UID: "uid-alice", Provider: "google.com", AuthTime: time.Now()})

	opts := []handler.Option{handler.WithUsers(users)}
	var accounts *accountMiddleware
	if accountsOn {
		authn := mw.NewAuth(fake, users, log)
		accounts = &accountMiddleware{authn: authn, appCheck: mw.AppCheck(goodAppCheck{}, mode, log)}
		opts = append(opts, handler.WithAccounts(fake, authn.Forget))
	}
	h := handler.New(nil, opts...)
	h.SetLogger(log)
	return buildRouter(h, log, accounts, devEdge(t, 0))
}

// devCORS is the CORS allowlist with CORS_ORIGIN unset in development.
func devCORS(t *testing.T) *mw.CORS {
	t.Helper()
	c, err := mw.NewCORS("", false)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// devEdge is local dev's edge settings with hops trusted proxies: the dev
// CORS allowlist and no server keys.
func devEdge(t *testing.T, hops int) edge {
	t.Helper()
	return edge{
		hops: hops, cors: devCORS(t),
		webServerLimit: defaultWebServerRateLimit, webServerSearchLimit: defaultWebServerSearchRateLimit,
	}
}

// listRecorder is a BillRepo that records List's params. Any other method
// panics on the nil embedded interface.
type listRecorder struct {
	repository.BillRepo

	calls []model.ListParams
}

func (l *listRecorder) List(_ context.Context, p model.ListParams) (*model.ListResult[model.Bill], error) {
	l.calls = append(l.calls, p)
	return &model.ListResult[model.Bill]{}, nil
}

func (l *listRecorder) CountByStatus(_ context.Context, p model.ListParams) (*model.BillCounts, error) {
	l.calls = append(l.calls, p)
	return &model.BillCounts{ByStatus: map[string]int{}}, nil
}

// newBillsRouter is the router with a recording bill repo and, when accounts
// are on, a user "alice" with the account u-alice.
func newBillsRouter(t *testing.T, accountsOn bool) (http.Handler, *listRecorder) {
	t.Helper()
	log := slog.New(slog.DiscardHandler)
	users := &memUsers{byUID: map[string]*model.User{"uid-alice": {ID: "u-alice", AuthUID: "uid-alice"}}}
	bills := &listRecorder{}
	opts := []handler.Option{handler.WithUsers(users), handler.WithBills(bills)}
	var accounts *accountMiddleware
	if accountsOn {
		fake := authtest.New()
		fake.Add("alice", auth.Principal{UID: "uid-alice", Provider: "google.com", AuthTime: time.Now()})
		authn := mw.NewAuth(fake, users, log)
		accounts = &accountMiddleware{authn: authn, appCheck: mw.AppCheck(nil, auth.AppCheckOff, log)}
		opts = append(opts, handler.WithAccounts(fake, authn.Forget))
	}
	h := handler.New(nil, opts...)
	h.SetLogger(log)
	return buildRouter(h, log, accounts, devEdge(t, 0)), bills
}

func do(t *testing.T, r http.Handler, method, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

func TestAccountRoutesLifecycle(t *testing.T) {
	r := newTestRouter(t, true)
	steps := []struct {
		method, path, token, body string
		status                    int
	}{
		{http.MethodGet, "/api/v1/me", "", "", http.StatusUnauthorized},
		{http.MethodGet, "/api/v1/me", "forged", "", http.StatusUnauthorized},
		{http.MethodGet, "/api/v1/me", "alice", "", http.StatusForbidden}, // no account yet
		{http.MethodPost, "/api/v1/me", "", "", http.StatusUnauthorized},
		{http.MethodPost, "/api/v1/me", "alice", "", http.StatusCreated},
		{http.MethodPost, "/api/v1/me", "alice", "", http.StatusOK},
		{http.MethodGet, "/api/v1/me", "alice", "", http.StatusOK},
		{http.MethodPost, "/api/v1/me/votes:import", "alice", `{"votes":[{"bill_id":"119-hr-1","vote":"yea"}]}`,
			http.StatusOK},
		{http.MethodDelete, "/api/v1/bills/119-hr-1/vote", "alice", "", http.StatusNoContent},
		{http.MethodPost, "/api/v1/auth/register", "", `{"email":"a@example.com"}`, http.StatusNotFound},
		{http.MethodDelete, "/api/v1/me", "alice", "", http.StatusNoContent},
		{http.MethodGet, "/api/v1/me", "alice", "", http.StatusForbidden},
		// The deleted account's ID token is still unexpired, but it can't make a new row (#458).
		{http.MethodPost, "/api/v1/me", "alice", "", http.StatusUnauthorized},
	}
	for _, s := range steps {
		rr := do(t, r, s.method, s.path, s.token, s.body)
		if rr.Code != s.status {
			t.Fatalf("%s %s (token %q) = %d, want %d: %s", s.method, s.path, s.token, rr.Code, s.status, rr.Body)
		}
	}
}

func TestAppCheckGuardsVoteAndImportOnly(t *testing.T) {
	r := newTestRouterWithAppCheck(t, true, auth.AppCheckEnforce)
	if rr := do(t, r, http.MethodPost, "/api/v1/me", "alice", ""); rr.Code != http.StatusCreated {
		t.Fatalf("POST /me without App Check = %d, want 201: %s", rr.Code, rr.Body)
	}
	for _, route := range []struct{ method, path, body string }{
		{http.MethodPost, "/api/v1/bills/119-hr-1/vote", `{"vote":"yea"}`},
		{http.MethodPost, "/api/v1/me/votes:import", `{"votes":[{"bill_id":"119-hr-1","vote":"yea"}]}`},
	} {
		rr := do(t, r, route.method, route.path, "alice", route.body)
		if rr.Code != http.StatusUnauthorized || !strings.Contains(rr.Body.String(), "app_check_required") {
			t.Errorf("%s without App Check = %d %s, want 401 app_check_required", route.path, rr.Code, rr.Body)
		}
		req := httptest.NewRequest(route.method, route.path, strings.NewReader(route.body))
		req.Header.Set("Authorization", "Bearer alice")
		req.Header.Set(mw.AppCheckHeader, "good")
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Errorf("%s with App Check = %d, want 200: %s", route.path, rec.Code, rec.Body)
		}
	}
	if rr := do(t, r, http.MethodGet, "/api/v1/me", "alice", ""); rr.Code != http.StatusOK {
		t.Errorf("GET /me without App Check = %d, want 200", rr.Code)
	}
}

func TestAccountRoutesOff(t *testing.T) {
	r := newTestRouter(t, false)
	for _, route := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/me"},
		{http.MethodPost, "/api/v1/me"},
		{http.MethodDelete, "/api/v1/me"},
		{http.MethodPost, "/api/v1/bills/119-hr-1/vote"},
		{http.MethodDelete, "/api/v1/bills/119-hr-1/vote"},
		{http.MethodGet, "/api/v1/me/export"},
	} {
		if rr := do(t, r, route.method, route.path, "alice", ""); rr.Code != http.StatusNotFound {
			t.Errorf("%s %s with accounts off = %d, want 404", route.method, route.path, rr.Code)
		}
	}
}

// TestPositionsRouteIsPublic checks the positions route answers with accounts on or off. A
// malformed congress gets a 400 from the handler before any repository is used; an unmounted
// route would be a 404.
func TestPositionsRouteIsPublic(t *testing.T) {
	for _, accountsOn := range []bool{false, true} {
		r := newTestRouter(t, accountsOn)
		rr := do(t, r, http.MethodGet, "/api/v1/members/A000001/positions?congress=abc", "", "")
		if rr.Code != http.StatusBadRequest {
			t.Errorf("accounts on = %v: GET positions = %d, want 400: %s", accountsOn, rr.Code, rr.Body)
		}
	}
}

// getBills sends GET /api/v1/bills+query with an optional Bearer token and
// the old X-Dev-User-Id header naming u-alice.
func getBills(r http.Handler, query, token string, devHeader bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/bills"+query, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if devHeader {
		req.Header.Set("X-Dev-User-Id", "u-alice")
	}
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

// unvotedBy returns p.UnvotedBy, or "" when it's nil.
func unvotedBy(p model.ListParams) string {
	if p.UnvotedBy == nil {
		return ""
	}
	return *p.UnvotedBy
}

// TestListBillsUnvoted checks that GET /bills?unvoted=true takes its user
// from a verified ID token only: the X-Dev-User-Id header is gone, and
// without accounts nobody is signed in.
func TestListBillsUnvoted(t *testing.T) {
	tests := []struct {
		name       string
		accountsOn bool
		query      string
		token      string
		devHeader  bool
		wantStatus int
		wantUser   string
	}{
		{name: "signed in", accountsOn: true, query: "?unvoted=true", token: "alice",
			wantStatus: http.StatusOK, wantUser: "u-alice"},
		{name: "anonymous", accountsOn: true, query: "?unvoted=true", wantStatus: http.StatusOK},
		{name: "dev header", accountsOn: true, query: "?unvoted=true", devHeader: true, wantStatus: http.StatusOK},
		{name: "bad token", accountsOn: true, query: "?unvoted=true", token: "forged",
			wantStatus: http.StatusUnauthorized},
		{name: "bad token on the public list", accountsOn: true, token: "forged", wantStatus: http.StatusOK},
		{name: "accounts off", query: "?unvoted=true", token: "alice", wantStatus: http.StatusOK},
		{name: "accounts off with dev header", query: "?unvoted=true", devHeader: true, wantStatus: http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, bills := newBillsRouter(t, tt.accountsOn)
			rr := getBills(r, tt.query, tt.token, tt.devHeader)
			if rr.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d: %s", rr.Code, tt.wantStatus, rr.Body)
			}
			if tt.wantStatus != http.StatusOK {
				return
			}
			if len(bills.calls) != 1 {
				t.Fatalf("List calls = %d, want 1", len(bills.calls))
			}
			if got := unvotedBy(bills.calls[0]); got != tt.wantUser {
				t.Errorf("UnvotedBy = %q, want %q", got, tt.wantUser)
			}
			private := rr.Header().Get("Cache-Control") == "private, no-store"
			if private != (tt.wantUser != "") {
				t.Errorf("Cache-Control = %q; want private, no-store exactly when signed in",
					rr.Header().Get("Cache-Control"))
			}
		})
	}
}

// TestRouterCORS checks the router answers only allowlisted origins.
func TestRouterCORS(t *testing.T) {
	r, _ := newBillsRouter(t, false)
	for origin, want := range map[string]string{
		"http://localhost:3000": "http://localhost:3000",
		"https://evil.example":  "",
	} {
		req := httptest.NewRequest(http.MethodOptions, "/api/v1/bills", nil)
		req.Header.Set("Origin", origin)
		req.Header.Set("Access-Control-Request-Method", http.MethodGet)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		if got := rr.Header().Get("Access-Control-Allow-Origin"); got != want {
			t.Errorf("preflight from %s: Access-Control-Allow-Origin = %q, want %q", origin, got, want)
		}
	}
}

// setAuthEnv sets exactly the auth variables in env, unsets the others, and
// points a fresh global viper at the environment.
func setAuthEnv(t *testing.T, env map[string]string) {
	t.Helper()
	for _, k := range []string{
		"APP_ENV", "ACCOUNTS_ENABLED", "AUTH_PROJECT_ID", "FIREBASE_AUTH_EMULATOR_HOST", "APP_CHECK_MODE",
		"AGG_DAILY_VOTE_CAP", "AGG_DISTRICT_CHANGE_DAYS", "AGGREGATES_PUBLIC",
	} {
		t.Setenv(k, env[k])
		if _, ok := env[k]; !ok {
			_ = os.Unsetenv(k)
		}
	}
	viper.Reset()
	viper.AutomaticEnv()
	t.Cleanup(viper.Reset)
}

func TestAuthSettings(t *testing.T) {
	tests := []struct {
		name        string
		env         map[string]string
		wantErr     string
		wantEnabled bool
	}{
		{"development defaults to accounts on", map[string]string{"AUTH_PROJECT_ID": "demo-justabill"}, "", true},
		{"production defaults to accounts off", map[string]string{"APP_ENV": "production"}, "", false},
		{"production opts in", map[string]string{
			"APP_ENV": "production", "ACCOUNTS_ENABLED": "true", "AUTH_PROJECT_ID": "example-prod",
		}, "", true},
		{"development opts out", map[string]string{"ACCOUNTS_ENABLED": "false"}, "", false},
		{"production refuses the emulator", map[string]string{
			"APP_ENV": "production", "FIREBASE_AUTH_EMULATOR_HOST": "localhost:9099",
		}, "FIREBASE_AUTH_EMULATOR_HOST", false},
		{"unknown APP_ENV", map[string]string{"APP_ENV": "prod"}, "APP_ENV", false},
		{"accounts on without a project", map[string]string{"AUTH_PROJECT_ID": ""}, "AUTH_PROJECT_ID", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setAuthEnv(t, tt.env)

			s, err := authSettings()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("authSettings() error = %v, want one mentioning %s", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("authSettings() = %v", err)
			}
			if s.Enabled != tt.wantEnabled {
				t.Errorf("Enabled = %v, want %v", s.Enabled, tt.wantEnabled)
			}
		})
	}
}

func TestAppCheckModeSetting(t *testing.T) {
	setAuthEnv(t, map[string]string{"AUTH_PROJECT_ID": "demo-justabill"})
	if s, err := authSettings(); err != nil || s.AppCheck != auth.AppCheckOff {
		t.Errorf("unset APP_CHECK_MODE = %q, %v; want off", s.AppCheck, err)
	}
	setAuthEnv(t, map[string]string{"AUTH_PROJECT_ID": "demo-justabill", "APP_CHECK_MODE": "audit"})
	if s, err := authSettings(); err != nil || s.AppCheck != auth.AppCheckAudit {
		t.Errorf("APP_CHECK_MODE=audit = %q, %v; want audit", s.AppCheck, err)
	}
	setAuthEnv(t, map[string]string{"AUTH_PROJECT_ID": "demo-justabill", "APP_CHECK_MODE": "strict"})
	if _, err := authSettings(); err == nil || !strings.Contains(err.Error(), "APP_CHECK_MODE") {
		t.Errorf("APP_CHECK_MODE=strict: error = %v, want one naming APP_CHECK_MODE", err)
	}
}

func TestWriteRules(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    handler.WriteRules
		wantErr string
	}{
		{"defaults", nil, handler.WriteRules{DailyVoteCap: 500, DistrictChangeInterval: 30 * day}, ""},
		{"overrides", map[string]string{"AGG_DAILY_VOTE_CAP": "200", "AGG_DISTRICT_CHANGE_DAYS": "7"},
			handler.WriteRules{DailyVoteCap: 200, DistrictChangeInterval: 7 * day}, ""},
		{"zero turns them off", map[string]string{"AGG_DAILY_VOTE_CAP": "0", "AGG_DISTRICT_CHANGE_DAYS": "0"},
			handler.WriteRules{}, ""},
		{"negative cap", map[string]string{"AGG_DAILY_VOTE_CAP": "-1"}, handler.WriteRules{}, "AGG_DAILY_VOTE_CAP"},
		{"days not a number", map[string]string{"AGG_DISTRICT_CHANGE_DAYS": "30d"}, handler.WriteRules{},
			"AGG_DISTRICT_CHANGE_DAYS"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setAuthEnv(t, tt.env)
			got, err := writeRules()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("writeRules() error = %v, want one naming %s", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Errorf("writeRules() = %+v, %v; want %+v", got, err, tt.want)
			}
		})
	}
}

func TestAggregateSettings(t *testing.T) {
	tests := []struct {
		name, value string
		want        bool
		wantErr     bool
	}{
		{"off by default", "", false, false},
		{"on", "true", true, false},
		{"off", "false", false, false},
		{"not a bool", "yes please", false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			env := map[string]string{}
			if tt.value != "" {
				env["AGGREGATES_PUBLIC"] = tt.value
			}
			setAuthEnv(t, env)
			got, err := aggregateSettings()
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), "AGGREGATES_PUBLIC") {
					t.Fatalf("aggregateSettings() error = %v, want one naming AGGREGATES_PUBLIC", err)
				}
				return
			}
			if err != nil || got.public != tt.want || got.rules.DailyVoteCap != defaultDailyVoteCap {
				t.Fatalf("aggregateSettings() = %+v, %v; want public %v", got, err, tt.want)
			}
			h := &handler.Handler{}
			got.reader(&spannerdb.Client{})(h)
			if (h.Aggregates != nil) != tt.want {
				t.Errorf("Aggregates set = %v, want %v", h.Aggregates != nil, tt.want)
			}
		})
	}
	setAuthEnv(t, map[string]string{"AGG_DAILY_VOTE_CAP": "-1"})
	if _, err := aggregateSettings(); err == nil {
		t.Error("a bad AGG_DAILY_VOTE_CAP: no error")
	}
}

func TestAuthSettingsExportsEmulatorHostFromDotEnv(t *testing.T) {
	t.Setenv("FIREBASE_AUTH_EMULATOR_HOST", "")
	_ = os.Unsetenv("FIREBASE_AUTH_EMULATOR_HOST")
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.Set("auth_project_id", "demo-justabill")
	viper.Set("firebase_auth_emulator_host", "localhost:9099") // as if read from .env

	if _, err := authSettings(); err != nil {
		t.Fatalf("authSettings() = %v", err)
	}
	if got := os.Getenv("FIREBASE_AUTH_EMULATOR_HOST"); got != "localhost:9099" {
		t.Errorf("FIREBASE_AUTH_EMULATOR_HOST = %q, want it exported for the Admin SDK", got)
	}
}

func TestTrustedProxyHops(t *testing.T) {
	tests := []struct {
		name       string
		env        map[string]string
		production bool
		want       int
		wantErr    string
	}{
		{name: "unset is an error", wantErr: "TRUSTED_PROXY_HOPS is required"},
		{name: "blank is an error", env: map[string]string{"TRUSTED_PROXY_HOPS": " "}, wantErr: "is required"},
		{
			name:    "the old name points at the new one",
			env:     map[string]string{"TRUSTED_PROXIES": "2"},
			wantErr: "TRUSTED_PROXIES was renamed to TRUSTED_PROXY_HOPS",
		},
		{name: "not a number", env: map[string]string{"TRUSTED_PROXY_HOPS": "two"}, wantErr: `"two"`},
		{name: "negative", env: map[string]string{"TRUSTED_PROXY_HOPS": "-1"}, wantErr: `"-1"`},
		{name: "zero in development", env: map[string]string{"TRUSTED_PROXY_HOPS": "0"}, want: 0},
		{
			name: "zero in production", env: map[string]string{"TRUSTED_PROXY_HOPS": "0"}, production: true,
			wantErr: "APP_ENV=production",
		},
		{name: "two in production", env: map[string]string{"TRUSTED_PROXY_HOPS": "2"}, production: true, want: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, k := range []string{"TRUSTED_PROXY_HOPS", "TRUSTED_PROXIES"} {
				t.Setenv(k, tt.env[k])
				if _, ok := tt.env[k]; !ok {
					_ = os.Unsetenv(k)
				}
			}
			viper.Reset()
			viper.AutomaticEnv()
			t.Cleanup(viper.Reset)

			got, err := trustedProxyHops(tt.production)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("trustedProxyHops() error = %v, want one containing %s", err, tt.wantErr)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("trustedProxyHops() = %d, %v; want %d", got, err, tt.want)
			}
		})
	}
}

// TestRouterRateLimitResistsSpoofing is the launch-day check from design #71 in
// miniature: one client behind the load balancer rotates X-Forwarded-For and
// X-Real-IP, and request publicRateLimit+1 still gets a 429 with Retry-After.
func TestRouterRateLimitResistsSpoofing(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	h := handler.New(nil)
	h.SetLogger(log)
	r := buildRouter(h, log, nil, devEdge(t, 2))

	send := func(i int, client string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/v1/not-a-route", nil)
		req.RemoteAddr = "10.0.0.7:443"
		spoofed := fmt.Sprintf("203.0.%d.%d", i/256, i%256)
		req.Header.Set("X-Forwarded-For", spoofed+", "+client+", 35.1.2.3")
		req.Header.Set("X-Real-IP", spoofed)
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		return rr
	}

	for i := range publicRateLimit {
		if rr := send(i, "198.51.100.1"); rr.Code == http.StatusTooManyRequests {
			t.Fatalf("request %d was limited early", i+1)
		}
	}
	rr := send(publicRateLimit, "198.51.100.1")
	if rr.Code != http.StatusTooManyRequests || rr.Header().Get("Retry-After") == "" {
		t.Fatalf("request %d = %d with Retry-After %q, want 429 with Retry-After",
			publicRateLimit+1, rr.Code, rr.Header().Get("Retry-After"))
	}
	if rr = send(0, "198.51.100.2"); rr.Code == http.StatusTooManyRequests {
		t.Fatal("a second client behind the load balancer shared the first one's bucket")
	}
}

// TestBodyCapCoversEveryRoute sends every /api/v1 route a 70 KiB body and
// expects the MaxBytes 413 before any handler or auth check runs, except
// votes:import, whose cap is MaxImportBody.
func TestBodyCapCoversEveryRoute(t *testing.T) {
	r := newTestRouter(t, true)
	body := strings.Repeat("x", 70<<10)
	routes := 0
	walk := func(method, route string, _ http.Handler, _ ...func(http.Handler) http.Handler) error {
		if !strings.HasPrefix(route, "/api/v1/") || route == "/api/v1/me/votes:import" {
			return nil
		}
		routes++
		path := strings.NewReplacer("{id}", "x", "{vid}", "x", "{did}", "x", "{billID}", "x", "{memberID}", "x").
			Replace(route)
		rr := do(t, r, method, path, "alice", body)
		if rr.Code != http.StatusRequestEntityTooLarge || !strings.Contains(rr.Body.String(), `"error"`) {
			t.Errorf("%s %s with a 70 KiB body = %d %s, want 413 with a JSON error", method, path, rr.Code, rr.Body)
		}
		return nil
	}
	if err := chi.Walk(r.(chi.Routes), walk); err != nil {
		t.Fatal(err)
	}
	if routes < 20 {
		t.Fatalf("walked %d routes, want every /api/v1 route", routes)
	}
}

func TestImportRouteKeepsItsLargerCap(t *testing.T) {
	r := newTestRouter(t, true)
	if rr := do(t, r, http.MethodPost, "/api/v1/me", "alice", ""); rr.Code != http.StatusCreated {
		t.Fatalf("POST /me = %d", rr.Code)
	}
	// Over the router-wide cap but under MaxImportBody: the handler decodes it (and rejects the bill ID).
	long := `{"votes":[{"bill_id":"` + strings.Repeat("x", 70<<10) + `","vote":"yea"}]}`
	if rr := do(t, r, http.MethodPost, "/api/v1/me/votes:import", "alice", long); rr.Code != http.StatusBadRequest {
		t.Errorf("70 KiB import = %d %s, want the handler's 400", rr.Code, rr.Body)
	}
	huge := `{"votes":[{"bill_id":"` + strings.Repeat("x", handler.MaxImportBody) + `"}]}`
	rr := do(t, r, http.MethodPost, "/api/v1/me/votes:import", "alice", huge)
	if rr.Code != http.StatusRequestEntityTooLarge {
		t.Errorf("import over MaxImportBody = %d, want 413", rr.Code)
	}
}

func TestHTTPIdleTimeout(t *testing.T) {
	tests := []struct {
		value   string
		set     bool
		want    time.Duration
		wantErr bool
	}{
		{set: false, want: defaultIdleTimeout},
		{value: "", set: true, want: defaultIdleTimeout},
		{value: "90s", set: true, want: 90 * time.Second},
		{value: "11m", set: true, want: 11 * time.Minute},
		{value: "620", set: true, wantErr: true},
		{value: "0s", set: true, wantErr: true},
		{value: "-5s", set: true, wantErr: true},
		{value: "soon", set: true, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%q set=%v", tt.value, tt.set), func(t *testing.T) {
			t.Setenv("HTTP_IDLE_TIMEOUT", tt.value)
			if !tt.set {
				_ = os.Unsetenv("HTTP_IDLE_TIMEOUT")
			}
			viper.Reset()
			viper.AutomaticEnv()
			t.Cleanup(viper.Reset)

			got, err := httpIdleTimeout()
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), "HTTP_IDLE_TIMEOUT") {
					t.Fatalf("httpIdleTimeout() = %v, %v; want an error naming HTTP_IDLE_TIMEOUT", got, err)
				}
				return
			}
			if err != nil || got != tt.want {
				t.Fatalf("httpIdleTimeout() = %v, %v; want %v", got, err, tt.want)
			}
		})
	}
}

// startServer serves newServer on a loopback port and returns its address.
func startServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := newServer("", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}), defaultIdleTimeout)
	go func() { _ = srv.Serve(ln) }()
	t.Cleanup(func() { _ = srv.Close() })
	return ln.Addr().String()
}

// rawRequest writes req on a new connection and returns the status line of
// the response.
func rawRequest(t *testing.T, addr, req string) string {
	t.Helper()
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	if _, err = io.WriteString(conn, req); err != nil {
		t.Fatal(err)
	}
	line, err := bufio.NewReader(conn).ReadString('\n')
	if err != nil {
		t.Fatalf("reading the status line: %v", err)
	}
	return strings.TrimSpace(line)
}

func TestServerHeaderLimit(t *testing.T) {
	addr := startServer(t)
	request := func(headerBytes int) string {
		return "GET / HTTP/1.1\r\nHost: x\r\nX-Pad: " + strings.Repeat("a", headerBytes) +
			"\r\nConnection: close\r\n\r\n"
	}
	if got := rawRequest(t, addr, request(8<<10)); got != "HTTP/1.1 204 No Content" {
		t.Errorf("8 KiB of headers = %q, want 204", got)
	}
	// Go allows 4 KiB of slack over MaxHeaderBytes, so go well past it.
	if got := rawRequest(t, addr, request(24<<10)); !strings.HasPrefix(got, "HTTP/1.1 431") {
		t.Errorf("24 KiB of headers = %q, want 431", got)
	}
}

// TestServerReadHeaderTimeout is the slowloris check: a client that stops
// partway through its headers is disconnected after readHeaderTimeout.
func TestServerReadHeaderTimeout(t *testing.T) {
	addr := startServer(t)
	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	start := time.Now()
	if _, err = io.WriteString(conn, "GET / HTTP/1.1\r\nHost: x\r\nX-Slow: "); err != nil {
		t.Fatal(err)
	}
	_ = conn.SetReadDeadline(start.Add(readHeaderTimeout + 5*time.Second))
	_, err = io.ReadAll(conn)
	elapsed := time.Since(start)
	if ne, ok := err.(net.Error); ok && ne.Timeout() { //nolint:errorlint // a direct net.Conn read error
		t.Fatalf("connection still open after %v, want it closed after about %v", elapsed, readHeaderTimeout)
	}
	if elapsed < readHeaderTimeout-time.Second {
		t.Errorf("closed after %v, want about %v", elapsed, readHeaderTimeout)
	}
}

// TestRunServerNeedsCORSOriginInProduction checks production refuses to start
// without an explicit allowlist, before it connects to anything.
func TestRunServerNeedsCORSOriginInProduction(t *testing.T) {
	setAuthEnv(t, map[string]string{"APP_ENV": "production"})
	for k, v := range map[string]string{
		"SPANNER_PROJECT": "p", "SPANNER_INSTANCE": "i", "SPANNER_DATABASE": "d", "TRUSTED_PROXY_HOPS": "2",
		"CORS_ORIGIN": "",
	} {
		t.Setenv(k, v)
	}
	err := runServer(nil, nil)
	if err == nil || !strings.Contains(err.Error(), "CORS_ORIGIN is required in production") {
		t.Fatalf("runServer() = %v, want the CORS_ORIGIN error", err)
	}

	t.Setenv("CORS_ORIGIN", "*")
	if err = runServer(nil, nil); err == nil || !strings.Contains(err.Error(), "list each origin") {
		t.Fatalf("runServer() with CORS_ORIGIN=* = %v, want an error", err)
	}
}
