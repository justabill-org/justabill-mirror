package main

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/justabill-org/justabill/api/internal/handler"
	"github.com/justabill-org/justabill/api/internal/health"
	mw "github.com/justabill-org/justabill/api/internal/middleware"
)

// testerIP is a tester's address inside the allowlist privateRouter sets.
const testerIP = "203.0.113.7:5000"

// privateRouter is the API behind its probes with ACCESS_MODE=private, one
// server key and 203.0.113.0/24 allowed, and a bill repo that answers.
func privateRouter(t *testing.T, logs *bytes.Buffer) http.Handler {
	t.Helper()
	setEdgeEnv(t, map[string]string{
		"ACCESS_MODE": "private", "ACCESS_ALLOW_CIDRS": "203.0.113.0/24", "API_SERVER_KEYS": testServerKey,
	})
	log := newLogger(slog.NewJSONHandler(logs, nil))
	e, err := edgeSettings(false, log)
	if err != nil {
		t.Fatal(err)
	}
	h := handler.New(nil, handler.WithBills(&listRecorder{}))
	h.SetLogger(log)
	return health.New(slog.New(slog.DiscardHandler), okPinger{}).Wrap(buildRouter(h, log, nil, e))
}

// sendFrom sends GET path from remoteAddr, as a browser on localhost:3000,
// with key as X-Server-Key unless it's empty.
func sendFrom(r http.Handler, path, remoteAddr, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = remoteAddr
	req.Header.Set("Origin", "http://localhost:3000")
	if key != "" {
		req.Header.Set(mw.ServerKeyHeader, key)
	}
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

// With ACCESS_MODE=private the API serves the web app's server and the
// allowlist, answers everyone else with a bare 404 that no CORS header, rate
// limit or key gives away, and still answers the probes.
func TestRouterPrivateMode(t *testing.T) {
	var logs bytes.Buffer
	r := privateRouter(t, &logs)

	if rr := sendFrom(r, "/api/v1/bills", testerIP, ""); rr.Code != http.StatusOK ||
		rr.Header().Get("Access-Control-Allow-Origin") == "" {
		t.Errorf("tester = %d, CORS %q; want 200 with CORS", rr.Code, rr.Header().Get("Access-Control-Allow-Origin"))
	}
	if rr := sendFrom(r, "/api/v1/bills", "76.76.21.21:443", testServerKey); rr.Code != http.StatusOK {
		t.Errorf("web server with its key = %d, want 200", rr.Code)
	}

	for _, key := range []string{"", "wrong-" + testServerKey} {
		// More than the public limit: a 429 would give the API away too.
		for i := range publicRateLimit + 5 {
			rr := sendFrom(r, "/api/v1/bills", "192.0.2.1:5000", key)
			if rr.Code != http.StatusNotFound || strings.TrimSpace(rr.Body.String()) != "404 page not found" {
				t.Fatalf("outsider (key %q) request %d = %d %q, want a bare 404", key, i+1, rr.Code, rr.Body.String())
			}
			if h := rr.Header(); h.Get("Access-Control-Allow-Origin") != "" || h.Get("Retry-After") != "" {
				t.Fatalf("outsider's 404 has CORS or rate-limit headers: %v", h)
			}
		}
	}

	for _, path := range []string{"/healthz", "/readyz"} {
		if rr := sendFrom(r, path, "192.0.2.1:5000", ""); rr.Code != http.StatusOK {
			t.Errorf("%s from an outsider = %d, want 200", path, rr.Code)
		}
	}
	if strings.Contains(logs.String(), testServerKey) {
		t.Errorf("the server key reached the logs:\n%s", logs.String())
	}
}

// Unset, ACCESS_MODE is public: nothing changes.
func TestRouterPublicByDefault(t *testing.T) {
	var logs bytes.Buffer
	r := keyedRouter(t, nil, &logs)
	if rr := sendFrom(r, "/api/v1/not-a-route", "192.0.2.1:5000", ""); rr.Code != http.StatusNotFound ||
		rr.Header().Get("Access-Control-Allow-Origin") == "" {
		t.Errorf("public 404 = %d, CORS %q; want the router's 404 with CORS",
			rr.Code, rr.Header().Get("Access-Control-Allow-Origin"))
	}
}

func TestAccessSettings(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
		wantLog string
	}{
		{name: "unset: public"},
		{name: "public", env: map[string]string{"ACCESS_MODE": "public"}},
		{
			name:    "private",
			env:     map[string]string{"ACCESS_MODE": "private", "ACCESS_ALLOW_CIDRS": "203.0.113.0/24"},
			wantLog: "ACCESS_MODE=private",
		},
		{
			name:    "private with nobody allowed",
			env:     map[string]string{"ACCESS_MODE": "private"},
			wantLog: "every request but the probes gets 404",
		},
		{
			name:    "an allowlist while public",
			env:     map[string]string{"ACCESS_ALLOW_CIDRS": "203.0.113.0/24"},
			wantLog: "ACCESS_ALLOW_CIDRS is ignored",
		},
		{name: "a typo", env: map[string]string{"ACCESS_MODE": "privat"}, wantErr: `ACCESS_MODE="privat"`},
		{
			name:    "a bad CIDR",
			env:     map[string]string{"ACCESS_MODE": "private", "ACCESS_ALLOW_CIDRS": "203.0.113.0/40"},
			wantErr: "ACCESS_ALLOW_CIDRS entry 1",
		},
		{
			name:    "everyone",
			env:     map[string]string{"ACCESS_MODE": "private", "ACCESS_ALLOW_CIDRS": "0.0.0.0/0"},
			wantErr: "use ACCESS_MODE=public",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setEdgeEnv(t, tt.env)
			var logs bytes.Buffer
			_, err := edgeSettings(false, slog.New(slog.NewTextHandler(&logs, nil)))
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("edgeSettings() error = %v, want one containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(logs.String(), tt.wantLog) {
				t.Errorf("logs = %s, want %q", logs.String(), tt.wantLog)
			}
		})
	}
}

// TestRunServerRefusesBadAccessModeInProduction checks production won't start
// with a mistyped mode or allowlist, before it connects to anything.
func TestRunServerRefusesBadAccessModeInProduction(t *testing.T) {
	for _, env := range []map[string]string{
		{"ACCESS_MODE": "on"},
		{"ACCESS_MODE": "private", "ACCESS_ALLOW_CIDRS": "203.0.113.0/24,tester-laptop"},
	} {
		setEdgeEnv(t, env)
		setAuthEnv(t, map[string]string{"APP_ENV": "production"})
		for k, v := range map[string]string{
			"SPANNER_PROJECT": "p", "SPANNER_INSTANCE": "i", "SPANNER_DATABASE": "d", "TRUSTED_PROXY_HOPS": "2",
			"CORS_ORIGIN": "https://justabill.io",
		} {
			t.Setenv(k, v)
		}
		if err := runServer(nil, nil); err == nil || !strings.Contains(err.Error(), "ACCESS_") {
			t.Fatalf("runServer() with %v = %v, want an ACCESS_ error", env, err)
		}
	}
}

// writeSecret writes value to a file in a fresh directory and returns its path.
func writeSecret(t *testing.T, value string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(path, []byte(value), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// API_SERVER_KEYS and REDIS_URL can come from mounted files, not both forms.
func TestSecretFiles(t *testing.T) {
	keys := writeSecret(t, testServerKey+","+nextServerKey+"\n")
	redis := writeSecret(t, "redis://:pw@redis:6379\n")

	setEdgeEnv(t, map[string]string{"API_SERVER_KEYS_FILE": keys, "REDIS_URL_FILE": redis})
	e, err := edgeSettings(false, slog.New(slog.DiscardHandler))
	if err != nil {
		t.Fatal(err)
	}
	if e.redisURL != "redis://:pw@redis:6379" {
		t.Errorf("redisURL = %q, want the file's URL", e.redisURL)
	}
	got, err := serverKeys()
	if err != nil || len(got) != 2 {
		t.Fatalf("serverKeys() = %d keys, %v; want 2 from the file", len(got), err)
	}

	for _, env := range []map[string]string{
		{"API_SERVER_KEYS": testServerKey, "API_SERVER_KEYS_FILE": keys},
		{"REDIS_URL": "redis://localhost:6379", "REDIS_URL_FILE": redis},
	} {
		setEdgeEnv(t, env)
		_, err = edgeSettings(false, slog.New(slog.DiscardHandler))
		if err == nil || !strings.Contains(err.Error(), "not both") {
			t.Errorf("edgeSettings() with %v = %v, want a not-both error", env, err)
		}
		if err != nil && (strings.Contains(err.Error(), testServerKey) || strings.Contains(err.Error(), "localhost")) {
			t.Errorf("error quotes a secret: %v", err)
		}
	}

	setEdgeEnv(t, map[string]string{"REDIS_URL_FILE": filepath.Join(t.TempDir(), "missing")})
	if _, err = edgeSettings(false, slog.New(slog.DiscardHandler)); err == nil ||
		!strings.Contains(err.Error(), "REDIS_URL_FILE") {
		t.Errorf("edgeSettings() with a missing file = %v, want a REDIS_URL_FILE error", err)
	}
}

// An empty REDIS_URL turns the cache off (the e2e API sets it so, #834); only an unset one
// falls back to the --redis-url default, local Redis.
func TestRedisURLSetting(t *testing.T) {
	file := writeSecret(t, "redis://:pw@redis:6379\n")
	tests := []struct {
		name string
		env  map[string]string
		want string
	}{
		{name: "unset: local Redis", want: "redis://localhost:6379"},
		{name: "empty: no cache", env: map[string]string{"REDIS_URL": ""}, want: ""},
		{name: "blank: no cache", env: map[string]string{"REDIS_URL": "  "}, want: ""},
		{name: "set", env: map[string]string{"REDIS_URL": "redis://cache:6380"}, want: "redis://cache:6380"},
		{
			name: "empty beside a file: the file",
			env:  map[string]string{"REDIS_URL": "", "REDIS_URL_FILE": file},
			want: "redis://:pw@redis:6379",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			setEdgeEnv(t, tt.env)
			flags := (&cobra.Command{}).Flags()
			flags.String("redis-url", defaultRedisURL, "")
			if err := viper.BindPFlag("redis_url", flags.Lookup("redis-url")); err != nil {
				t.Fatal(err)
			}
			got, err := redisURL()
			if err != nil || got != tt.want {
				t.Errorf("redisURL() = %q, %v; want %q", got, err, tt.want)
			}
		})
	}
}
