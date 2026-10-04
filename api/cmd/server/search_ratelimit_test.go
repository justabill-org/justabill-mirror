package main

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/justabill-org/justabill/api/internal/handler"
	mw "github.com/justabill-org/justabill/api/internal/middleware"
)

// searchRouter is the API's router with the edge settings read from env and a
// bill list that answers every request, logging to logs.
func searchRouter(t *testing.T, env map[string]string, logs *bytes.Buffer) http.Handler {
	t.Helper()
	setEdgeEnv(t, env)
	log := newLogger(slog.NewJSONHandler(logs, nil))
	e, err := edgeSettings(false, log)
	if err != nil {
		t.Fatal(err)
	}
	h := handler.New(nil, handler.WithBills(&listRecorder{}))
	h.SetLogger(log)
	return buildRouter(h, log, nil, e)
}

// listFrom sends GET path from one client IP, with key as X-Server-Key unless it's empty.
func listFrom(r http.Handler, path, key string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = "203.0.113.7:443"
	if key != "" {
		req.Header.Set(mw.ServerKeyHeader, key)
	}
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	return rr
}

// A search has its own per-IP limit, under the public one, whichever
// parameter carries it; browsing the list doesn't spend it (#619).
func TestSearchRateLimit(t *testing.T) {
	var logs bytes.Buffer
	r := searchRouter(t, nil, &logs)

	for i := range searchRateLimit {
		path := "/api/v1/bills?q=tax"
		if i%2 == 1 {
			path = "/api/v1/bills?search=tax&congress=119"
		}
		if rr := listFrom(r, path, ""); rr.Code != http.StatusOK {
			t.Fatalf("search %d = %d, want 200: %s", i+1, rr.Code, rr.Body)
		}
	}
	for _, path := range []string{"/api/v1/bills?q=farm", "/api/v1/bills?search=farm"} {
		rr := listFrom(r, path, "")
		if rr.Code != http.StatusTooManyRequests || rr.Header().Get("Retry-After") == "" {
			t.Errorf("%s past the search limit = %d (Retry-After %q), want 429 with Retry-After",
				path, rr.Code, rr.Header().Get("Retry-After"))
		}
	}
	for _, path := range []string{"/api/v1/bills", "/api/v1/bills?q=", "/api/v1/bills?congress=119"} {
		if rr := listFrom(r, path, ""); rr.Code != http.StatusOK {
			t.Errorf("%s = %d, want 200: the search limit spilled over to the list", path, rr.Code)
		}
	}
	if !strings.Contains(logs.String(), `"event":"rate_limited","class":"`+mw.ClassSearch+`"`) {
		t.Errorf("no rate_limited log with class %q:\n%s", mw.ClassSearch, logs.String())
	}
}

// GET /bills/counts with a search spends the same per-IP search allowance as the list's search,
// one search per call, and counts without one don't spend it (#713).
func TestCountsSearchRateLimit(t *testing.T) {
	var logs bytes.Buffer
	r := searchRouter(t, nil, &logs)

	for i := range searchRateLimit {
		path := "/api/v1/bills/counts?q=tax"
		if i%2 == 1 {
			path = "/api/v1/bills?q=tax"
		}
		if rr := listFrom(r, path, ""); rr.Code != http.StatusOK {
			t.Fatalf("search %d (%s) = %d, want 200: %s", i+1, path, rr.Code, rr.Body)
		}
	}
	for _, path := range []string{"/api/v1/bills/counts?q=farm", "/api/v1/bills/counts?search=farm"} {
		if rr := listFrom(r, path, ""); rr.Code != http.StatusTooManyRequests {
			t.Errorf("%s past the search limit = %d, want 429", path, rr.Code)
		}
	}
	if rr := listFrom(r, "/api/v1/bills/counts?congress=119", ""); rr.Code != http.StatusOK {
		t.Errorf("counts without a search = %d, want 200: the search limit spilled over", rr.Code)
	}
}

// The web app's server searches in one bucket of WEB_SERVER_SEARCH_RATE_LIMIT,
// apart from the per-IP search buckets of the address it shares (#619).
func TestWebServerSearchRateLimit(t *testing.T) {
	const limit = 25
	var logs bytes.Buffer
	r := searchRouter(t, map[string]string{
		"API_SERVER_KEYS":              testServerKey,
		"WEB_SERVER_SEARCH_RATE_LIMIT": "25",
	}, &logs)

	for i := range limit {
		if rr := listFrom(r, "/api/v1/bills?q=tax", testServerKey); rr.Code != http.StatusOK {
			t.Fatalf("keyed search %d = %d, want 200", i+1, rr.Code)
		}
	}
	if rr := listFrom(r, "/api/v1/bills?q=tax", testServerKey); rr.Code != http.StatusTooManyRequests {
		t.Fatalf("keyed search %d = %d, want 429 at WEB_SERVER_SEARCH_RATE_LIMIT", limit+1, rr.Code)
	}
	if rr := listFrom(r, "/api/v1/bills", testServerKey); rr.Code != http.StatusOK {
		t.Errorf("keyed list = %d, want 200: only searches count in the search bucket", rr.Code)
	}
	if rr := listFrom(r, "/api/v1/bills?q=tax", ""); rr.Code != http.StatusOK {
		t.Errorf("unkeyed search from the same IP = %d, want 200 from its own bucket", rr.Code)
	}
	if !strings.Contains(logs.String(), `"event":"rate_limited","class":"`+mw.ClassWebServerSearch+`"`) {
		t.Errorf("no rate_limited log with class %q:\n%s", mw.ClassWebServerSearch, logs.String())
	}
}

func TestWebServerSearchRateLimitSetting(t *testing.T) {
	tests := []struct {
		raw     string
		want    int
		wantErr bool
	}{
		{raw: "", want: defaultWebServerSearchRateLimit},
		{raw: "600", want: 600},
		{raw: "0", wantErr: true},
		{raw: "-5", wantErr: true},
		{raw: "lots", wantErr: true},
	}
	for _, tt := range tests {
		t.Run("WEB_SERVER_SEARCH_RATE_LIMIT="+tt.raw, func(t *testing.T) {
			setEdgeEnv(t, map[string]string{"WEB_SERVER_SEARCH_RATE_LIMIT": tt.raw})
			e, err := edgeSettings(false, slog.New(slog.DiscardHandler))
			if tt.wantErr {
				if err == nil || !strings.Contains(err.Error(), "WEB_SERVER_SEARCH_RATE_LIMIT") {
					t.Fatalf("edgeSettings() error = %v, want a WEB_SERVER_SEARCH_RATE_LIMIT error", err)
				}
				return
			}
			if err != nil || e.webServerSearchLimit != tt.want {
				t.Fatalf("edgeSettings() search limit = %d, %v; want %d", e.webServerSearchLimit, err, tt.want)
			}
		})
	}
}
