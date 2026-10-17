package handler_test

import (
	"bytes"
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	mw "github.com/justabill-org/justabill/api/internal/middleware"
	"github.com/justabill-org/justabill/db/cachekey"
	"github.com/justabill-org/justabill/db/repository"
)

const (
	invalidBillIDErr    = "invalid bill id"
	invalidMemberIDErr  = "invalid member id"
	invalidVersionIDErr = "invalid text version id"
	invalidDiffIDErr    = "invalid diff id"
)

// Repositories that panic on any call: a route that reads one before checking its path answers
// 500 through the recoverer instead of the 400 the tests want.
type (
	untouchedBills     struct{ repository.BillRepo }
	untouchedUsers     struct{ repository.UserRepo }
	untouchedVotes     struct{ repository.VoteRepo }
	untouchedScorecard struct{ repository.Scorecard }
)

// idRouter mounts the routes that take a bill, member, text version or diff ID in their path, as
// cmd/server does, with repositories that must not be called, signed in as u1, and logs into buf.
func idRouter(buf *bytes.Buffer) http.Handler {
	h := newTestHandler(untouchedBills{})
	h.Users, h.Votes, h.Scorecard = untouchedUsers{}, untouchedVotes{}, untouchedScorecard{}
	h.SetLogger(slog.New(slog.NewJSONHandler(buf, nil)))

	r := chi.NewRouter()
	r.Use(middleware.Recoverer, func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			next.ServeHTTP(w, req.WithContext(mw.ContextWithUserID(req.Context(), "u1")))
		})
	})
	r.Get("/api/v1/bills/{id}", h.GetBill)
	r.Get("/api/v1/bills/{id}/text/{vid}", h.GetBillText)
	r.Get("/api/v1/bills/{id}/diffs/{did}", h.GetBillDiff)
	r.Post("/api/v1/bills/{id}/vote", h.CastVote)
	r.Delete("/api/v1/bills/{id}/vote", h.DeleteVote)
	r.Post("/api/v1/me/favorites/{billID}", h.AddFavorite)
	r.Get("/api/v1/me/compare/{memberID}", h.CompareMember)
	return r
}

// TestMalformedPathIDs: a bill, member, text version or diff ID that can't be one is a 400 with a
// static message before any repository read, and nothing is logged. "%FF" reaches the handler as
// the byte 0xFF, which Spanner fails as Internal (a 500 and an ERROR log), and "%ff" as the text
// "%ff" (#915).
func TestMalformedPathIDs(t *testing.T) {
	const version = "/text/0b7a3c52-6a1e-4c0e-9d0e-3f1b2a4c5d01"
	const diff = "/diffs/5d2e8f10-1c3b-4a7d-8e9f-0a1b2c3d4e01"
	tests := []struct {
		method, path, wantErr string
	}{
		{http.MethodGet, "/api/v1/bills/%FF", invalidBillIDErr},
		{http.MethodGet, "/api/v1/bills/%ff", invalidBillIDErr},
		{http.MethodGet, "/api/v1/bills/hr-119-1x", invalidBillIDErr},
		{http.MethodGet, "/api/v1/bills/%FF" + version, invalidBillIDErr},
		{http.MethodGet, "/api/v1/bills/%ff" + version, invalidBillIDErr},
		{http.MethodGet, "/api/v1/bills/%FF" + diff, invalidBillIDErr},
		{http.MethodGet, "/api/v1/bills/%ff" + diff, invalidBillIDErr},
		{http.MethodPost, "/api/v1/bills/%FF/vote", invalidBillIDErr},
		{http.MethodPost, "/api/v1/bills/%ff/vote", invalidBillIDErr},
		{http.MethodDelete, "/api/v1/bills/%FF/vote", invalidBillIDErr},
		{http.MethodDelete, "/api/v1/bills/%ff/vote", invalidBillIDErr},
		{http.MethodPost, "/api/v1/me/favorites/%FF", invalidBillIDErr},
		{http.MethodPost, "/api/v1/me/favorites/%ff", invalidBillIDErr},
		{http.MethodGet, "/api/v1/me/compare/%FF", invalidMemberIDErr},
		{http.MethodGet, "/api/v1/me/compare/%ff", invalidMemberIDErr},
		{http.MethodGet, "/api/v1/me/compare/a000001", invalidMemberIDErr},
		{http.MethodGet, "/api/v1/bills/hr-119-1/text/%FF", invalidVersionIDErr},
		{http.MethodGet, "/api/v1/bills/hr-119-1/text/%fF", invalidVersionIDErr},
		{http.MethodGet, "/api/v1/bills/hr-119-1/text/v1", invalidVersionIDErr},
		{http.MethodGet, "/api/v1/bills/hr-119-1/diffs/%FF", invalidDiffIDErr},
		{http.MethodGet, "/api/v1/bills/hr-119-1/diffs/%fF", invalidDiffIDErr},
		{http.MethodGet, "/api/v1/bills/hr-119-1/diffs/d1", invalidDiffIDErr},
	}
	for _, tt := range tests {
		t.Run(tt.method+" "+tt.path, func(t *testing.T) {
			var buf bytes.Buffer
			w := httptest.NewRecorder()
			req := httptest.NewRequest(tt.method, tt.path, strings.NewReader(`{"vote":"yea"}`))
			idRouter(&buf).ServeHTTP(w, req)

			if w.Code != http.StatusBadRequest {
				t.Fatalf("status = %d, want 400 (%s)", w.Code, w.Body.String())
			}
			if got := errorBody(t, w); got != tt.wantErr {
				t.Errorf("error = %q, want %q", got, tt.wantErr)
			}
			if buf.Len() != 0 {
				t.Errorf("a 400 must not be logged, got %s", buf.String())
			}
		})
	}
}

// TestTextAndDiffIDs: a version or diff ID is a UUID in either case, as GENERATE_UUID() writes
// it; one well formed but unknown is still a 404, and anything else a 400 before any read (#915).
func TestTextAndDiffIDs(t *testing.T) {
	tests := []struct {
		id         string
		wantStatus int
	}{
		{"0b7a3c52-6a1e-4c0e-9d0e-3f1b2a4c5d99", http.StatusNotFound},
		{"0B7A3C52-6A1E-4C0E-9D0E-3F1B2A4C5D99", http.StatusNotFound},
		{"0b7a3c52-6a1e-4c0e-9d0e-3f1b2a4c5d9", http.StatusBadRequest},      // 35 characters
		{"0b7a3c52-6a1e-4c0e-9d0e-3f1b2a4c5d999", http.StatusBadRequest},    // 37 characters
		{"0b7a3c526a1e-4c0e-9d0e-3f1b2a4c5d99-", http.StatusBadRequest},     // a hyphen moved
		{"0b7a3c52-6a1e-4c0e-9d0e-3f1b2a4c5d9g", http.StatusBadRequest},     // not hex
		{"0b7a3c52-6a1e-4c0e-9d0e-3f1b2a4c5d%C3%A9", http.StatusBadRequest}, // ends in é: 36 bytes
	}
	for _, kind := range []struct{ path, wantErr string }{
		{"/text/", invalidVersionIDErr},
		{"/diffs/", invalidDiffIDErr},
	} {
		for _, tt := range tests {
			t.Run(kind.path+tt.id, func(t *testing.T) {
				w := get(t, textRouter(&mockBillRepo{}), "/api/v1/bills/"+billA+kind.path+tt.id)

				if w.Code != tt.wantStatus {
					t.Fatalf("status = %d, want %d (%s)", w.Code, tt.wantStatus, w.Body.String())
				}
				if tt.wantStatus == http.StatusBadRequest {
					if got := errorBody(t, w); got != kind.wantErr {
						t.Errorf("error = %q, want %q", got, kind.wantErr)
					}
				}
			})
		}
	}
}

// TestGetBill_MalformedIDSkipsCache: a malformed bill ID is refused before the cache lookup, so it
// never reads a key built from raw bytes, even one that holds a body (#915).
func TestGetBill_MalformedIDSkipsCache(t *testing.T) {
	c := testCache(t)
	h := newTestHandler(untouchedBills{})
	h.SetCache(c)
	ctx := context.Background()
	for _, id := range []string{"\xff", "%ff"} {
		key := cachekey.BillDetail(id)
		if err := c.Set(ctx, key, `{"bill":{"id":"cached"}}`, time.Minute); err != nil {
			t.Fatalf("seed cache: %v", err)
		}
		t.Cleanup(func() { _ = c.Delete(context.Background(), key) })
	}
	r := chi.NewRouter()
	r.Use(middleware.Recoverer)
	r.Get("/api/v1/bills/{id}", h.GetBill)

	for _, path := range []string{"/api/v1/bills/%FF", "/api/v1/bills/%ff"} {
		w := get(t, r, path)
		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400 (%s)", path, w.Code, w.Body.String())
		}
	}
}
