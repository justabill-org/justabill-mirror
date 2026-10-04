package handler_test

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/justabill-org/justabill/api/internal/cache"
	"github.com/justabill-org/justabill/api/internal/handler"
	"github.com/justabill-org/justabill/db/model"
)

const billIndexCacheControl = "public, max-age=3600, s-maxage=3600, stale-while-revalidate=86400"

func newBillIndexRouter(bills *mockBillRepo) (*handler.Handler, *chi.Mux) {
	h := newTestHandler(bills)
	r := chi.NewRouter()
	r.Get("/api/v1/bill-index", h.GetBillIndex)
	return h, r
}

func TestGetBillIndex(t *testing.T) {
	updated := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	bills := &mockBillRepo{index: []model.BillIndexEntry{
		{ID: "hr-119-1", UpdatedAt: &updated},
		{ID: "s-119-1"},
	}}
	_, r := newBillIndexRouter(bills)

	w := serve(r, "/api/v1/bill-index?congress=119")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body)
	}
	want := `{"congress":119,"bills":[{"id":"hr-119-1","updated_at":"2026-09-01T12:00:00Z"},{"id":"s-119-1"}]}`
	if got := strings.TrimSpace(w.Body.String()); got != want {
		t.Errorf("body = %s\nwant %s", got, want)
	}
	if got := w.Header().Get("Cache-Control"); got != billIndexCacheControl {
		t.Errorf("Cache-Control = %q, want %q", got, billIndexCacheControl)
	}
	if bills.indexOf != 119 {
		t.Errorf("repo congress = %d, want 119", bills.indexOf)
	}
}

func TestGetBillIndex_EmptyCongress(t *testing.T) {
	_, r := newBillIndexRouter(&mockBillRepo{})
	w := serve(r, "/api/v1/bill-index?congress=120")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body)
	}
	if !strings.Contains(w.Body.String(), `"bills":[]`) {
		t.Errorf("body = %s, want an empty bills array", w.Body)
	}
}

func TestGetBillIndex_Errors(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		err    error
		status int
	}{
		{"missing congress", "/api/v1/bill-index", nil, http.StatusBadRequest},
		{"non-numeric congress", "/api/v1/bill-index?congress=abc", nil, http.StatusBadRequest},
		{"zero congress", "/api/v1/bill-index?congress=0", nil, http.StatusBadRequest},
		{"out-of-range congress", "/api/v1/bill-index?congress=201", nil, http.StatusBadRequest},
		{"query fails", "/api/v1/bill-index?congress=119", errors.New("spanner down"),
			http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bills := &mockBillRepo{indexErr: tt.err}
			_, r := newBillIndexRouter(bills)
			w := serve(r, tt.path)
			if w.Code != tt.status {
				t.Fatalf("status = %d, want %d: %s", w.Code, tt.status, w.Body)
			}
			if got := w.Header().Get("Cache-Control"); got != "" {
				t.Errorf("Cache-Control = %q on a %d, want none", got, w.Code)
			}
			if tt.status == http.StatusBadRequest && bills.indexCalls != 0 {
				t.Errorf("repo called %d times for a bad request", bills.indexCalls)
			}
		})
	}
}

func TestGetBillIndex_Caching(t *testing.T) {
	redisURL := os.Getenv("REDIS_URL")
	if redisURL == "" {
		t.Skip("REDIS_URL not set; skipping Redis integration test")
	}
	c, err := cache.New(redisURL)
	if err != nil {
		t.Fatalf("cache.New: %v", err)
	}
	t.Cleanup(func() { _ = c.Close() })

	// Congress 1 has no bills anywhere real, so this key is the test's own.
	const key = "bills:index:1"
	_ = c.Delete(t.Context(), key)
	t.Cleanup(func() { _ = c.Delete(context.Background(), key) })

	bills := &mockBillRepo{index: []model.BillIndexEntry{{ID: "hr-1-1"}}}
	h, r := newBillIndexRouter(bills)
	h.SetCache(c)

	first := serve(r, "/api/v1/bill-index?congress=1")
	second := serve(r, "/api/v1/bill-index?congress=1")
	if bills.indexCalls != 1 {
		t.Errorf("repo calls = %d, want 1 (second request from cache)", bills.indexCalls)
	}
	if second.Code != http.StatusOK || strings.TrimSpace(first.Body.String()) != second.Body.String() {
		t.Errorf("cached response %d %q differs from %q", second.Code, second.Body, first.Body)
	}
	if got := second.Header().Get("Cache-Control"); got != billIndexCacheControl {
		t.Errorf("Cache-Control on a cache hit = %q, want %q", got, billIndexCacheControl)
	}
}
