package middleware_test

import (
	"bytes"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	chimw "github.com/go-chi/chi/v5/middleware"

	mw "github.com/justabill-org/justabill/api/internal/middleware"
)

const lbIP = "35.1.2.3"

func TestClientIP(t *testing.T) {
	tests := []struct {
		name       string
		hops       int
		remoteAddr string
		xff        []string
		want       string
	}{
		{name: "no proxy ignores headers", xff: []string{"203.0.113.9"}, want: "10.0.0.1"},
		{name: "no proxy unmaps v4-mapped IPv6", remoteAddr: "[::ffff:192.0.2.4]:443", want: "192.0.2.4"},
		{name: "no proxy with IPv6 peer", remoteAddr: "[2001:db8::1]:443", want: "2001:db8::1"},
		{
			name: "one proxy takes the rightmost entry",
			hops: 1,
			xff:  []string{"6.6.6.6, 198.51.100.7"},
			want: "198.51.100.7",
		},
		{
			name: "two hops skip the load balancer and a spoofed leftmost entry",
			hops: 2, xff: []string{"6.6.6.6, 198.51.100.7, " + lbIP}, want: "198.51.100.7",
		},
		{
			name: "two hops across duplicate headers",
			hops: 2, xff: []string{"6.6.6.6", "198.51.100.7, " + lbIP}, want: "198.51.100.7",
		},
		{
			name: "garbage left of the trusted entry doesn't matter",
			hops: 2, xff: []string{"not-an-ip, 198.51.100.7, " + lbIP}, want: "198.51.100.7",
		},
		{name: "garbage at the trusted entry fails closed", hops: 2, xff: []string{"not-an-ip, " + lbIP}, want: ""},
		{
			name: "v4-mapped IPv6 in the chain",
			hops: 2,
			xff:  []string{"::ffff:198.51.100.7, " + lbIP},
			want: "198.51.100.7",
		},
		{name: "IPv6 client in the chain", hops: 2, xff: []string{"2001:db8:1:2::9, " + lbIP}, want: "2001:db8:1:2::9"},
		{name: "short chain fails closed", hops: 2, xff: []string{"198.51.100.7"}, want: ""},
		{name: "no header fails closed", hops: 2, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var got string
			handler := mw.ClientIP(tt.hops, slog.New(slog.DiscardHandler))(
				http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
					got = chimw.GetClientIP(r.Context())
				}))

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.RemoteAddr = "10.0.0.1:1234"
			if tt.remoteAddr != "" {
				req.RemoteAddr = tt.remoteAddr
			}
			req.Header.Set("X-Real-IP", "203.0.113.1")
			req.Header.Set("True-Client-Ip", "203.0.113.2")
			req.Header["X-Forwarded-For"] = tt.xff
			handler.ServeHTTP(httptest.NewRecorder(), req)

			if got != tt.want {
				t.Fatalf("expected client IP %q, got %q", tt.want, got)
			}
		})
	}
}

func TestClientIP_LogsMissingClientIP(t *testing.T) {
	var buf bytes.Buffer
	handler := mw.ClientIP(2, slog.New(slog.NewJSONHandler(&buf, nil)))(okHandler())

	send := func(xff string) {
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.Header.Set("X-Forwarded-For", xff)
		handler.ServeHTTP(httptest.NewRecorder(), req)
	}

	send("198.51.100.7, " + lbIP)
	if buf.Len() != 0 {
		t.Fatalf("a valid chain logged %q", buf.String())
	}

	send("198.51.100.8")
	out := buf.String()
	for _, want := range []string{`"event":"client_ip_missing"`, `"trusted_proxy_hops":2`, `"xff_entries":1`} {
		if !strings.Contains(out, want) {
			t.Errorf("log %q is missing %s", out, want)
		}
	}
	if strings.Contains(out, "198.51.100.8") {
		t.Errorf("log %q contains the client's address", out)
	}
}
