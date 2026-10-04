package main

import (
	"bytes"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/justabill-org/justabill/api/internal/handler"
	mw "github.com/justabill-org/justabill/api/internal/middleware"
)

const (
	testServerKey = "k3y-0123456789abcdefghijklmnopqrstuvwx" // gitleaks:allow (a made-up key)
	nextServerKey = "n3xt-0123456789abcdefghijklmnopqrstuvw" // gitleaks:allow (a made-up key)
)

// setEdgeEnv sets the edge settings' variables to env, unsetting the rest, in
// development with TRUSTED_PROXY_HOPS=0.
func setEdgeEnv(t *testing.T, env map[string]string) {
	t.Helper()
	setAuthEnv(t, nil)
	t.Setenv("TRUSTED_PROXY_HOPS", "0")
	t.Setenv("CORS_ORIGIN", "")
	for _, k := range []string{
		"API_SERVER_KEYS", "API_SERVER_KEYS_FILE", "WEB_SERVER_RATE_LIMIT", "WEB_SERVER_SEARCH_RATE_LIMIT",
		"ACCESS_MODE", "ACCESS_ALLOW_CIDRS",
		"REDIS_URL", "REDIS_URL_FILE",
	} {
		t.Setenv(k, env[k])
		if _, ok := env[k]; !ok {
			_ = os.Unsetenv(k)
		}
	}
}

func TestServerKeysSetting(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		want    int
		wantErr string
	}{
		{name: "unset: off", want: 0},
		{name: "blank: off", env: map[string]string{"API_SERVER_KEYS": " , "}, want: 0},
		{name: "one key", env: map[string]string{"API_SERVER_KEYS": testServerKey}, want: 1},
		{
			name: "two keys during a rotation",
			env:  map[string]string{"API_SERVER_KEYS": " " + testServerKey + " , " + nextServerKey + ","},
			want: 2,
		},
		{
			name:    "three keys",
			env:     map[string]string{"API_SERVER_KEYS": testServerKey + "," + nextServerKey + ",x" + testServerKey},
			wantErr: "at most 2",
		},
		{
			name:    "a short key",
			env:     map[string]string{"API_SERVER_KEYS": testServerKey + ",changeme"},
			wantErr: "key 2 is 8 characters",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setEdgeEnv(t, tt.env)
			got, err := serverKeys()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("serverKeys() error = %v, want one containing %q", err, tt.wantErr)
				}
				if strings.Contains(err.Error(), "changeme") || strings.Contains(err.Error(), testServerKey) {
					t.Errorf("serverKeys() error quotes a key: %v", err)
				}
				return
			}
			if err != nil || len(got) != tt.want {
				t.Fatalf("serverKeys() = %d keys, %v; want %d", len(got), err, tt.want)
			}
		})
	}
}

func TestWebServerRateLimitSetting(t *testing.T) {
	tests := []struct {
		raw     string
		want    int
		wantErr bool
	}{
		{raw: "", want: defaultWebServerRateLimit},
		{raw: "3000", want: 3000},
		{raw: "0", wantErr: true},
		{raw: "-5", wantErr: true},
		{raw: "lots", wantErr: true},
	}
	for _, tt := range tests {
		t.Run("WEB_SERVER_RATE_LIMIT="+tt.raw, func(t *testing.T) {
			setEdgeEnv(t, map[string]string{"WEB_SERVER_RATE_LIMIT": tt.raw})
			e, err := edgeSettings(false, slog.New(slog.DiscardHandler))
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), "WEB_SERVER_RATE_LIMIT") {
					t.Fatalf("edgeSettings() error = %v, want a WEB_SERVER_RATE_LIMIT error", err)
				}
				return
			}
			if err != nil || e.webServerLimit != tt.want {
				t.Fatalf("edgeSettings() limit = %d, %v; want %d", e.webServerLimit, err, tt.want)
			}
		})
	}
}

// keyedRouter is the API's router with the edge settings read from env,
// logging to logs through the server's own logger.
func keyedRouter(t *testing.T, env map[string]string, logs *bytes.Buffer) http.Handler {
	t.Helper()
	setEdgeEnv(t, env)
	log := newLogger(slog.NewJSONHandler(logs, nil))
	e, err := edgeSettings(false, log)
	if err != nil {
		t.Fatal(err)
	}
	h := handler.New(nil)
	h.SetLogger(log)
	return buildRouter(h, log, nil, e)
}

// unroutedPath is a path no route matches: the limiter still counts it.
const unroutedPath = "/api/v1/not-a-route"

// sendAs sends GET unroutedPath from one client IP, with key as X-Server-Key unless it's empty.
func sendAs(r http.Handler, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, unroutedPath, nil)
	req.RemoteAddr = "76.76.21.21:443"
	if key != "" {
		req.Header.Set(mw.ServerKeyHeader, key)
	}
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

// With API_SERVER_KEYS set, the web app's server gets past the public per-IP
// limit on the IP it shares with other callers, a wrong key doesn't, and no
// key ever reaches the logs.
func TestRouterServerKey(t *testing.T) {
	var logs bytes.Buffer
	r := keyedRouter(t, map[string]string{
		"API_SERVER_KEYS":       nextServerKey + "," + testServerKey,
		"WEB_SERVER_RATE_LIMIT": "100",
	}, &logs)

	for i := range publicRateLimit {
		if rr := sendAs(r, ""); rr.Code == http.StatusTooManyRequests {
			t.Fatalf("unkeyed request %d was limited early", i+1)
		}
	}
	if rr := sendAs(r, ""); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("unkeyed request %d = %d, want 429", publicRateLimit+1, rr.Code)
	}
	if rr := sendAs(r, "wrong-"+testServerKey); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("wrong key = %d, want 429 from the per-IP bucket", rr.Code)
	}
	for i := range 100 {
		if rr := sendAs(r, testServerKey); rr.Code == http.StatusTooManyRequests {
			t.Fatalf("keyed request %d = 429, want it counted in the web-server bucket", i+1)
		}
	}
	if rr := sendAs(r, testServerKey); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("keyed request 101 = %d, want 429 at WEB_SERVER_RATE_LIMIT", rr.Code)
	}

	out := logs.String()
	if strings.Contains(out, testServerKey) || strings.Contains(out, nextServerKey) {
		t.Fatalf("a server key reached the logs:\n%s", out)
	}
	for _, class := range []string{mw.ClassIP, mw.ClassWebServer} {
		if !strings.Contains(out, `"event":"rate_limited","class":"`+class+`"`) {
			t.Errorf("no rate_limited log with class %q:\n%s", class, out)
		}
	}
}

// Without API_SERVER_KEYS, a request carrying a key is limited by IP.
func TestRouterServerKeyOff(t *testing.T) {
	r := keyedRouter(t, nil, &bytes.Buffer{})
	for range publicRateLimit {
		sendAs(r, testServerKey)
	}
	if rr := sendAs(r, testServerKey); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("request %d = %d, want 429", publicRateLimit+1, rr.Code)
	}
}

// POST /reps logs its 429s under the reps class.
func TestRepsRateLimitLogsClass(t *testing.T) {
	var logs bytes.Buffer
	r := repsRouter(t, noDistricts{}, &logs)
	for range repsRateLimit + 1 {
		do(t, r, http.MethodPost, "/api/v1/reps", "", capitolBody)
	}
	if !strings.Contains(logs.String(), `"event":"rate_limited","class":"reps"`) {
		t.Errorf("no rate_limited log with class reps:\n%s", logs.String())
	}
}

// sendForVisitor sends GET unroutedPath from the web server's address with
// key as X-Server-Key (unless empty) and visitor as X-Visitor-IP.
func sendForVisitor(r http.Handler, key, visitor string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, unroutedPath, nil)
	req.RemoteAddr = "76.76.21.21:443"
	if key != "" {
		req.Header.Set(mw.ServerKeyHeader, key)
	}
	req.Header.Set(mw.VisitorIPHeader, visitor)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

// The router runs mw.Visitor after mw.ClientIP and before the public
// limiter: a visitor named through the key counts in their own per-IP
// bucket, so one visitor over the limit doesn't stop another, and the header
// means nothing without the key (docs/design/607-per-visitor-web-limits.md).
func TestRouterVisitorThroughServerKey(t *testing.T) {
	var logs bytes.Buffer
	r := keyedRouter(t, map[string]string{"API_SERVER_KEYS": testServerKey}, &logs)

	const a, b = "203.0.113.7", "198.51.100.9"
	for i := range publicRateLimit {
		if rr := sendForVisitor(r, testServerKey, a); rr.Code == http.StatusTooManyRequests {
			t.Fatalf("visitor A request %d was limited early", i+1)
		}
	}
	if rr := sendForVisitor(r, testServerKey, a); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("visitor A request %d = %d, want 429", publicRateLimit+1, rr.Code)
	}
	if rr := sendForVisitor(r, testServerKey, b); rr.Code == http.StatusTooManyRequests {
		t.Fatal("visitor B = 429, want their own bucket")
	}
	if rr := sendAs(r, testServerKey); rr.Code == http.StatusTooManyRequests {
		t.Fatal("keyed request without a visitor = 429, want the web-server bucket")
	}

	// Without the key, rotating the header doesn't give the caller fresh buckets.
	for i := range publicRateLimit {
		sendForVisitor(r, "", fmt.Sprintf("203.0.113.%d", i+10))
	}
	if rr := sendForVisitor(r, "", "203.0.113.200"); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("unkeyed request %d = %d, want 429 from its own IP's bucket", publicRateLimit+1, rr.Code)
	}
	if strings.Contains(logs.String(), a) || strings.Contains(logs.String(), b) {
		t.Errorf("a visitor IP reached the logs:\n%s", logs.String())
	}
}
