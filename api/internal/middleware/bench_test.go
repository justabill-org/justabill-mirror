package middleware_test

// Benchmarks for the middleware every request passes (#873): the public rate limit and the auth
// middleware with a cached account. Run them with `task bench`.

import (
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/justabill-org/justabill/api/internal/auth"
	"github.com/justabill-org/justabill/api/internal/auth/authtest"
	mw "github.com/justabill-org/justabill/api/internal/middleware"
)

// benchClients is how many client IPs the rate-limit benchmark spreads its requests over, so
// it times the map lookups of a busy limiter rather than one hot bucket.
const benchClients = 1000

// benchUnlimited is a limit no benchmark reaches.
const benchUnlimited = 1 << 30

// discardWriter is a ResponseWriter that keeps nothing but its headers, so a benchmark times the
// middleware rather than a recorder.
type discardWriter struct{ h http.Header }

func (d *discardWriter) Header() http.Header         { return d.h }
func (d *discardWriter) Write(p []byte) (int, error) { return len(p), nil }
func (d *discardWriter) WriteHeader(int)             {}

// serveEach serves reqs in turn to h, b.N requests in all, and fails if next never ran.
func serveEach(b *testing.B, h http.Handler, reqs []*http.Request, ran *int) {
	b.Helper()
	w := &discardWriter{h: http.Header{}}
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		h.ServeHTTP(w, reqs[i%len(reqs)])
		i++
	}
	if *ran != i {
		b.Fatalf("next ran %d times for %d requests: the middleware refused some", *ran, i)
	}
}

func counting(ran *int) http.Handler {
	return http.HandlerFunc(func(http.ResponseWriter, *http.Request) { *ran++ })
}

func BenchmarkRateLimit(b *testing.B) {
	for _, clients := range []int{1, benchClients} {
		b.Run(fmt.Sprintf("clients=%d", clients), func(b *testing.B) {
			var ran int
			h := mw.ClientIP(0, slog.New(slog.DiscardHandler))(mw.RateLimit(benchUnlimited)(counting(&ran)))
			reqs := make([]*http.Request, clients)
			for i := range reqs {
				reqs[i] = httptest.NewRequest(http.MethodGet, "/api/v1/bills", nil)
				reqs[i].RemoteAddr = fmt.Sprintf("10.0.%d.%d:4000", i/256, i%256)
			}
			serveEach(b, h, reqs, &ran)
		})
	}
}

// benchAuth is the auth middleware with one valid token, whose account the users fake knows.
func benchAuth() *mw.Auth {
	fake := authtest.New()
	fake.Add("alice-token", auth.Principal{UID: "uid-alice", Provider: "google.com"})
	users := &countingUsers{byUID: map[string]string{"uid-alice": "user-alice"}}
	return mw.NewAuth(fake, users, slog.New(slog.DiscardHandler))
}

func BenchmarkAuth(b *testing.B) {
	cases := map[string]string{"anonymous": "", "signed-in": "Bearer alice-token"}
	for name, header := range cases {
		b.Run(name, func(b *testing.B) {
			var ran int
			h := benchAuth().Handler(counting(&ran))
			req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
			if header != "" {
				req.Header.Set("Authorization", header)
			}
			serveEach(b, h, []*http.Request{req}, &ran)
		})
	}
}

// BenchmarkSignedInRateLimit is the account routes' limiter, which runs the auth middleware
// itself, for a signed-in request whose account is cached after the first.
func BenchmarkSignedInRateLimit(b *testing.B) {
	var ran int
	limit := mw.SignedInRateLimit(benchAuth().Handler, benchUnlimited, benchUnlimited, slog.New(slog.DiscardHandler))
	h := mw.ClientIP(0, slog.New(slog.DiscardHandler))(limit(counting(&ran)))
	req := httptest.NewRequest(http.MethodGet, "/api/v1/me", nil)
	req.Header.Set("Authorization", "Bearer alice-token")
	serveEach(b, h, []*http.Request{req}, &ran)
}
