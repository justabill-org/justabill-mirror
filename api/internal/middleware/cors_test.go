package middleware_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mw "github.com/justabill-org/justabill/api/internal/middleware"
)

const previewPattern = "https://justabill-*-team.vercel.app"

func TestNewCORSRejects(t *testing.T) {
	tests := []struct {
		name, spec string
		production bool
		wantErr    string
	}{
		{name: "empty in production", spec: " , ", production: true, wantErr: "required in production"},
		{name: "star", spec: "*", wantErr: "list each origin"},
		{name: "star among others", spec: "http://localhost:3000, *", wantErr: "list each origin"},
		{name: "null", spec: "null", wantErr: "list each origin"},
		{name: "http in production", spec: "http://justabill.io", production: true, wantErr: "only https"},
		{name: "no scheme", spec: "justabill.io", wantErr: "scheme"},
		{name: "other scheme", spec: "ftp://justabill.io", wantErr: "scheme"},
		{name: "path", spec: "https://justabill.io/app", wantErr: "no path"},
		{name: "query", spec: "https://justabill.io?x=1", wantErr: "no path"},
		{name: "credentials", spec: "https://u:p@justabill.io", wantErr: "no path"},
		{name: "bare wildcard label", spec: "https://*.vercel.app", wantErr: "fixed text"},
		{name: "wildcard past the first label", spec: "https://app.*.vercel.app", wantErr: "first label"},
		{name: "two wildcards", spec: "https://a-*-b-*.vercel.app", wantErr: "at most one"},
		{name: "wildcard without a domain", spec: "https://app-*", wantErr: "fixed domain"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := mw.NewCORS(tt.spec, tt.production)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("NewCORS(%q, %v) error = %v, want one containing %q", tt.spec, tt.production, err, tt.wantErr)
			}
		})
	}
}

func TestCORSAllowed(t *testing.T) {
	spec := " https://justabill.io/ ,https://WWW.justabill.io," + previewPattern + ",http://localhost:3000"
	c, err := mw.NewCORS(spec, false)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		origin string
		want   bool
	}{
		{"https://justabill.io", true},
		{"https://www.justabill.io", true},
		{"http://localhost:3000", true},
		{"https://justabill-git-feat-x-team.vercel.app", true},
		{"https://justabill-abc123-team.vercel.app", true},
		{"", false},
		{"null", false},
		{"http://justabill.io", false},
		{"https://justabill.io:8443", false},
		{"https://evil.justabill.io", false},
		{"https://justabill.io.evil.com", false},
		{"http://localhost:3001", false},
		{"https://justabill--team.vercel.app", false},
		{"https://justabill-x.evil-team.vercel.app", false},
		{"https://justabill-x-team.vercel.app.evil.com", false},
		{"https://justabill-x_y-team.vercel.app", false},
		{"https://other-x-team.vercel.app", false},
	}
	for _, tt := range tests {
		if got := c.Allowed(tt.origin); got != tt.want {
			t.Errorf("Allowed(%q) = %v, want %v", tt.origin, got, tt.want)
		}
	}
}

func TestNewCORSDevDefault(t *testing.T) {
	c, err := mw.NewCORS("", false)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Allowed(mw.DevCORSOrigin) || c.Allowed("https://example.com") {
		t.Errorf("empty CORS_ORIGIN outside production should allow exactly %s", mw.DevCORSOrigin)
	}
}

func TestNewCORSProduction(t *testing.T) {
	c, err := mw.NewCORS("https://justabill.io,"+previewPattern, true)
	if err != nil {
		t.Fatal(err)
	}
	if !c.Allowed("https://justabill.io") || !c.Allowed("https://justabill-pr-1-team.vercel.app") {
		t.Error("production allowlist rejected one of its own origins")
	}
	if c.Allowed(mw.DevCORSOrigin) {
		t.Error("production allowlist allowed the dev origin")
	}
}

func TestCORSHandler(t *testing.T) {
	c, err := mw.NewCORS("https://justabill.io", true)
	if err != nil {
		t.Fatal(err)
	}
	var reached bool
	h := c.Handler(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusOK)
	}))

	tests := []struct {
		name, method, origin string
		wantStatus           int
		wantReached          bool
		wantAllowOrigin      string
		wantPreflight        bool
	}{
		{"allowed GET", http.MethodGet, "https://justabill.io", http.StatusOK, true, "https://justabill.io", false},
		{"other origin GET", http.MethodGet, "https://evil.example", http.StatusOK, true, "", false},
		{"no origin GET", http.MethodGet, "", http.StatusOK, true, "", false},
		{"allowed preflight", http.MethodOptions, "https://justabill.io", http.StatusNoContent, false,
			"https://justabill.io", true},
		{"other origin preflight", http.MethodOptions, "https://evil.example", http.StatusNoContent, false, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reached = false
			req := httptest.NewRequest(tt.method, "/api/v1/bills", nil)
			if tt.origin != "" {
				req.Header.Set("Origin", tt.origin)
			}
			if tt.method == http.MethodOptions {
				req.Header.Set("Access-Control-Request-Method", http.MethodPost)
			}
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)

			if rr.Code != tt.wantStatus || reached != tt.wantReached {
				t.Errorf("status %d, reached %v; want %d, %v", rr.Code, reached, tt.wantStatus, tt.wantReached)
			}
			checkCORSHeaders(t, rr.Header(), tt.wantAllowOrigin, tt.wantPreflight)
		})
	}
}

// checkCORSHeaders checks the CORS headers of one response.
func checkCORSHeaders(t *testing.T, hdr http.Header, wantAllowOrigin string, wantPreflight bool) {
	t.Helper()
	if got := hdr.Get("Access-Control-Allow-Origin"); got != wantAllowOrigin {
		t.Errorf("Access-Control-Allow-Origin = %q, want %q", got, wantAllowOrigin)
	}
	if got := hdr.Values("Vary"); len(got) != 1 || got[0] != "Origin" {
		t.Errorf("Vary = %q, want [Origin]", got)
	}
	if got := hdr.Get("Access-Control-Allow-Credentials"); got != "" {
		t.Errorf("Access-Control-Allow-Credentials = %q, want none", got)
	}
	allowHeaders := hdr.Get("Access-Control-Allow-Headers")
	if (allowHeaders != "") != wantPreflight || (hdr.Get("Access-Control-Max-Age") != "") != wantPreflight {
		t.Errorf("preflight headers present = %v, want %v", allowHeaders != "", wantPreflight)
	}
	if wantPreflight && (!strings.Contains(allowHeaders, "Authorization") ||
		!strings.Contains(allowHeaders, "traceparent") || !strings.Contains(allowHeaders, "tracestate") ||
		strings.Contains(allowHeaders, "X-Dev-User-Id")) {
		t.Errorf("Access-Control-Allow-Headers = %q, want Authorization, traceparent and tracestate "+
			"and no X-Dev-User-Id", allowHeaders)
	}
}
