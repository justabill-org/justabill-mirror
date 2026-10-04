package middleware_test

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	mw "github.com/justabill-org/justabill/api/internal/middleware"
)

func TestParseAccessMode(t *testing.T) {
	tests := []struct {
		in      string
		want    mw.AccessMode
		wantErr bool
	}{
		{"", mw.AccessPublic, false},
		{"public", mw.AccessPublic, false},
		{" private ", mw.AccessPrivate, false},
		{"Private", "", true},
		{"closed", "", true},
	}
	for _, tt := range tests {
		got, err := mw.ParseAccessMode(tt.in)
		if got != tt.want || (err != nil) != tt.wantErr {
			t.Errorf("ParseAccessMode(%q) = %q, %v; want %q, error %v", tt.in, got, err, tt.want, tt.wantErr)
		}
	}
}

func TestParseAllowCIDRs(t *testing.T) {
	tests := []struct {
		name    string
		in      string
		want    []string
		wantErr string
	}{
		{name: "empty", in: "", want: nil},
		{name: "blank entries", in: " , ,", want: nil},
		{
			name: "v4, v6 and a bare address",
			in:   "203.0.113.0/24, 2001:db8::/48,198.51.100.7",
			want: []string{"203.0.113.0/24", "2001:db8::/48", "198.51.100.7/32"},
		},
		{name: "host bits are masked", in: "203.0.113.9/24", want: []string{"203.0.113.0/24"}},
		{name: "a v4-mapped address", in: "::ffff:198.51.100.7", want: []string{"198.51.100.7/32"}},
		{name: "garbage", in: "10.0.0.0/8,not-an-ip", wantErr: `entry 2 "not-an-ip"`},
		{name: "bad prefix length", in: "10.0.0.0/33", wantErr: `entry 1 "10.0.0.0/33"`},
		{name: "everyone v4", in: "0.0.0.0/0", wantErr: "use ACCESS_MODE=public"},
		{name: "everyone v6", in: "::/0", wantErr: "use ACCESS_MODE=public"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := mw.ParseAllowCIDRs(tt.in)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("error = %v, want it to contain %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var gotStrings []string
			for _, p := range got {
				gotStrings = append(gotStrings, p.String())
			}
			if strings.Join(gotStrings, " ") != strings.Join(tt.want, " ") {
				t.Errorf("prefixes = %v, want %v", gotStrings, tt.want)
			}
		})
	}
}

func TestPrivateAccess(t *testing.T) {
	allow := []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24"), netip.MustParsePrefix("2001:db8::/48")}
	gate := mw.PrivateAccess(mw.ServerKeys([]string{currentKey}), allow)
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("served"))
	})
	h := mw.ClientIP(0, slog.New(slog.DiscardHandler))(gate(ok))

	tests := []struct {
		name       string
		remoteAddr string
		key        string
		served     bool
	}{
		{name: "allowed v4", remoteAddr: "203.0.113.7:1234", served: true},
		{name: "allowed v6", remoteAddr: "[2001:db8::1]:1234", served: true},
		{name: "allowed v4-mapped", remoteAddr: "[::ffff:203.0.113.7]:1234", served: true},
		{name: "server key from anywhere", remoteAddr: "192.0.2.1:1234", key: currentKey, served: true},
		{name: "other address", remoteAddr: "192.0.2.1:1234"},
		{name: "other address, wrong key", remoteAddr: "192.0.2.1:1234", key: "not-the-key"},
		{name: "no client IP", remoteAddr: "garbage"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/bills", nil)
			req.RemoteAddr = tt.remoteAddr
			if tt.key != "" {
				req.Header.Set(mw.ServerKeyHeader, tt.key)
			}
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if tt.served {
				if rr.Code != http.StatusOK || rr.Body.String() != "served" {
					t.Fatalf("status %d body %q, want it served", rr.Code, rr.Body.String())
				}
				return
			}
			notFound := httptest.NewRecorder()
			http.NotFound(notFound, req)
			if rr.Code != http.StatusNotFound || rr.Body.String() != notFound.Body.String() {
				t.Errorf("status %d body %q, want a bare 404 like an unknown route's", rr.Code, rr.Body.String())
			}
			if got := rr.Header().Get("Cache-Control"); got != "no-store" {
				t.Errorf("Cache-Control = %q, want no-store", got)
			}
		})
	}
}

func TestPrivateAccessWithoutKeys(t *testing.T) {
	gate := mw.PrivateAccess(nil, nil)
	h := mw.ClientIP(0, slog.New(slog.DiscardHandler))(gate(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Error("served a request with no keys and no allowlist")
	})))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/bills", nil)
	req.Header.Set(mw.ServerKeyHeader, currentKey)
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Errorf("status %d, want 404", rr.Code)
	}
}

// Behind two trusted proxies, the gate reads the client IP ClientIP resolved from the
// hop count: an allowed address the client put in X-Forwarded-For or X-Real-IP
// itself doesn't get it in.
func TestPrivateAccessUsesTrustedHops(t *testing.T) {
	gate := mw.PrivateAccess(nil, []netip.Prefix{netip.MustParsePrefix("203.0.113.0/24")})
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	h := mw.ClientIP(2, slog.New(slog.DiscardHandler))(gate(ok))

	tests := []struct {
		name   string
		xff    string
		realIP string
		want   int
	}{
		// The load balancer appends "<client>, <lb>".
		{name: "a tester", xff: "203.0.113.7, 10.0.0.1", want: http.StatusNoContent},
		{name: "a spoofed leading entry", xff: "203.0.113.7, 192.0.2.1, 10.0.0.1", want: http.StatusNotFound},
		{name: "a spoofed X-Real-IP", xff: "192.0.2.1, 10.0.0.1", realIP: "203.0.113.7", want: http.StatusNotFound},
		{name: "a chain shorter than the hops", xff: "203.0.113.7", want: http.StatusNotFound},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/bills", nil)
			req.RemoteAddr = "10.0.0.2:443"
			req.Header.Set("X-Forwarded-For", tt.xff)
			if tt.realIP != "" {
				req.Header.Set("X-Real-IP", tt.realIP)
			}
			rr := httptest.NewRecorder()
			h.ServeHTTP(rr, req)
			if rr.Code != tt.want {
				t.Errorf("status %d, want %d", rr.Code, tt.want)
			}
		})
	}
}
