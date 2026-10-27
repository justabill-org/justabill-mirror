package upstream_test

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"slices"
	"strings"
	"testing"

	"github.com/justabill-org/justabill/pipeline/internal/upstream"
)

// hangUp closes every connection without answering.
func hangUp(w http.ResponseWriter, _ *http.Request, _ int) {
	conn, _, err := w.(http.Hijacker).Hijack()
	if err == nil {
		_ = conn.Close()
	}
}

// A request the client gave up on is an outage only when the host was down: a 5xx or no answer
// after the retries. Its error comes back through [http.Client], wrapped in a [*url.Error].
func TestOutagesFromTheClient(t *testing.T) {
	for _, tc := range []struct {
		name       string
		handle     func(http.ResponseWriter, *http.Request, int)
		maxBody    int64
		wantOutage bool
		wantStatus int
		wantInErr  string
	}{
		{
			name:       "5xx after the retries",
			handle:     statusThen(500, 500, 500, 500, 500),
			wantOutage: true, wantStatus: http.StatusInternalServerError,
			wantInErr: "HTTP 500 after 5 attempt(s)",
		},
		{
			name: "no answer after the retries", handle: hangUp, wantOutage: true,
			wantInErr: "after 5 attempt(s): ",
		},
		{name: "4xx", handle: statusThen(http.StatusUnauthorized), wantInErr: "HTTP 401 after 1 attempt(s)"},
		{
			name: "rate limited after the retries",
			handle: func(w http.ResponseWriter, _ *http.Request, _ int) {
				w.Header().Set("Retry-After", "0")
				w.WriteHeader(http.StatusTooManyRequests)
			},
			wantInErr: "HTTP 429 after 5 attempt(s)",
		},
		{
			name: "body over the cap",
			handle: func(w http.ResponseWriter, _ *http.Request, _ int) {
				_, _ = io.WriteString(w, strings.Repeat("x", 64))
			},
			maxBody: 16, wantInErr: "after 1 attempt(s): upstream: response body exceeds the size cap",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := newServer(t, tc.handle)
			h := host(fastBudget(t))
			if tc.maxBody > 0 {
				h.MaxBodyBytes = tc.maxBody
			}
			c := newClient(t, slog.New(slog.DiscardHandler), map[string]upstream.Host{srv.host(): h},
				upstream.Hooks{Sleep: (&sleeps{}).sleep})

			_, err := get(t.Context(), c, srv.URL+"/collections/BILLS", nil)
			if err == nil || !strings.Contains(err.Error(), tc.wantInErr) {
				t.Fatalf("got %v, want an error containing %q", err, tc.wantInErr)
			}
			outages, ok := upstream.Outages(fmt.Errorf("collection page 1: %w", err))
			if ok != tc.wantOutage {
				t.Fatalf("Outages(%v) = %v, want %v", err, ok, tc.wantOutage)
			}
			want := []upstream.Outage{{Host: srv.host(), Status: tc.wantStatus}}
			if ok && !slices.Equal(outages, want) {
				t.Errorf("outages = %v, want %v", outages, want)
			}
		})
	}
}

// Every cause in a joined error has to be an outage: [errors.As] on the join would find the 5xx
// beside a 4xx or a store error and call the whole run an outage.
func TestOutagesJoins(t *testing.T) {
	down := &upstream.StatusError{Host: "api.govinfo.gov", Path: "/collections", Status: 500, Attempts: 5}
	gateway := &upstream.StatusError{Host: "api.congress.gov", Path: "/v3/bill", Status: 502, Attempts: 5}
	silent := &upstream.TransportError{Host: "api.congress.gov", Path: "/v3/bill", Attempts: 5,
		Err: errors.New("connection reset by peer")}
	revoked := &upstream.StatusError{Host: "api.congress.gov", Path: "/v3/bill", Status: 403, Attempts: 1}
	store := errors.New("spanner: deadline exceeded")
	for _, tc := range []struct {
		name string
		err  error
		want []upstream.Outage
	}{
		{name: "nil"},
		{name: "plain error", err: store},
		{name: "a 5xx", err: fmt.Errorf("poll: %w", down), want: []upstream.Outage{{"api.govinfo.gov", 500}}},
		{
			name: "outages joined",
			err:  errors.Join(down, fmt.Errorf("bill: %w", silent), gateway),
			want: []upstream.Outage{{"api.govinfo.gov", 500}, {"api.congress.gov", 0}, {"api.congress.gov", 502}},
		},
		{
			name: "a join inside a chain",
			err:  fmt.Errorf("congress 119: %w", errors.Join(down)),
			want: []upstream.Outage{{"api.govinfo.gov", 500}},
		},
		{name: "a 5xx joined with a 4xx", err: errors.Join(down, revoked)},
		{name: "a 5xx joined with a store error", err: errors.Join(store, down)},
		{name: "a nested join with a store error", err: fmt.Errorf("x: %w", errors.Join(down, errors.Join(store)))},
		{name: "a sentinel beside a 5xx", err: fmt.Errorf("%w: %w", store, down)},
		{name: "an empty join", err: fmt.Errorf("x: %w", emptyJoinError{})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := upstream.Outages(tc.err)
			if ok != (tc.want != nil) {
				t.Fatalf("Outages(%v) = %v, want %v", tc.err, ok, tc.want != nil)
			}
			if ok && !slices.Equal(got, tc.want) {
				t.Errorf("outages = %v, want %v", got, tc.want)
			}
		})
	}
}

// emptyJoinError unwraps to no causes, which proves nothing about an outage.
type emptyJoinError struct{}

func (emptyJoinError) Error() string   { return "empty" }
func (emptyJoinError) Unwrap() []error { return nil }
