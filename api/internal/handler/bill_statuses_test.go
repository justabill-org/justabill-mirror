package handler_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/justabill-org/justabill/api/internal/cache"
	"github.com/justabill-org/justabill/api/internal/handler"
	"github.com/justabill-org/justabill/db/model"
)

func newBillStatusesRouter(bills *mockBillRepo) (*handler.Handler, *chi.Mux) {
	h := newTestHandler(bills)
	r := chi.NewRouter()
	r.Get("/api/v1/bill-statuses", h.GetBillStatuses)
	return h, r
}

func TestGetBillStatuses(t *testing.T) {
	bills := &mockBillRepo{statuses: []model.BillStatusIndexEntry{
		{ID: "hr-119-1", Status: "became_law"},
		{ID: "s-119-5", Status: "passed_senate"},
	}}
	_, r := newBillStatusesRouter(bills)

	w := serve(r, "/api/v1/bill-statuses?congress=119")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body)
	}
	want := `{"congress":119,"bills":[{"id":"hr-119-1","status":"became_law"},` +
		`{"id":"s-119-5","status":"passed_senate"}]}`
	if got := strings.TrimSpace(w.Body.String()); got != want {
		t.Errorf("body = %s\nwant %s", got, want)
	}
	if got := w.Header().Get("Cache-Control"); got != billIndexCacheControl {
		t.Errorf("Cache-Control = %q, want %q", got, billIndexCacheControl)
	}
	if bills.statusesOf != 119 {
		t.Errorf("repo congress = %d, want 119", bills.statusesOf)
	}
}

// TestGetBillStatuses_SameForEveryone checks the route reads no identity: a request with a
// bearer token and cookies gets the same body as one without, and no Vary on them.
func TestGetBillStatuses_SameForEveryone(t *testing.T) {
	bills := &mockBillRepo{statuses: []model.BillStatusIndexEntry{{ID: "hr-119-1", Status: "vetoed"}}}
	_, r := newBillStatusesRouter(bills)

	anonymous := serve(r, "/api/v1/bill-statuses?congress=119")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/bill-statuses?congress=119", nil)
	req.Header.Set("Authorization", "Bearer some-token")
	req.AddCookie(&http.Cookie{Name: "session", Value: "abc"})
	signed := httptest.NewRecorder()
	r.ServeHTTP(signed, req)
	if signed.Code != http.StatusOK || signed.Body.String() != anonymous.Body.String() {
		t.Errorf("signed-in answer %d %q differs from anonymous %q", signed.Code, signed.Body, anonymous.Body)
	}
	if got := signed.Header().Get("Cache-Control"); got != billIndexCacheControl {
		t.Errorf("Cache-Control = %q, want %q", got, billIndexCacheControl)
	}
	if got := signed.Header().Get("Vary"); got != "" {
		t.Errorf("Vary = %q, want none", got)
	}
}

func TestGetBillStatuses_EmptyCongress(t *testing.T) {
	_, r := newBillStatusesRouter(&mockBillRepo{})
	w := serve(r, "/api/v1/bill-statuses?congress=120")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body)
	}
	if got, want := strings.TrimSpace(w.Body.String()), `{"congress":120,"bills":[]}`; got != want {
		t.Errorf("body = %s, want %s", got, want)
	}
}

func TestGetBillStatuses_Errors(t *testing.T) {
	const badCongress = "congress must be a whole number from 1 to 200"
	tests := []struct {
		name    string
		path    string
		err     error
		status  int
		message string
	}{
		{"missing congress", "/api/v1/bill-statuses", nil, http.StatusBadRequest, badCongress},
		{"non-numeric congress", "/api/v1/bill-statuses?congress=abc", nil, http.StatusBadRequest, badCongress},
		{"zero congress", "/api/v1/bill-statuses?congress=0", nil, http.StatusBadRequest, badCongress},
		{"out-of-range congress", "/api/v1/bill-statuses?congress=201", nil, http.StatusBadRequest, badCongress},
		{"query fails", "/api/v1/bill-statuses?congress=119", errors.New("spanner down"),
			http.StatusInternalServerError, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bills := &mockBillRepo{statusesErr: tt.err}
			_, r := newBillStatusesRouter(bills)
			w := serve(r, tt.path)
			if w.Code != tt.status {
				t.Fatalf("status = %d, want %d: %s", w.Code, tt.status, w.Body)
			}
			if tt.message != "" && !strings.Contains(w.Body.String(), tt.message) {
				t.Errorf("body = %s, want the message %q", w.Body, tt.message)
			}
			if strings.Contains(w.Body.String(), "spanner down") {
				t.Errorf("body = %s leaks the error", w.Body)
			}
			if got := w.Header().Get("Cache-Control"); got != "" {
				t.Errorf("Cache-Control = %q on a %d, want none", got, w.Code)
			}
			if tt.status == http.StatusBadRequest && bills.statusCalls != 0 {
				t.Errorf("repo called %d times for a bad request", bills.statusCalls)
			}
		})
	}
}

func TestGetBillStatuses_Caching(t *testing.T) {
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
	const key = "bills:statuses:1"
	_ = c.Delete(t.Context(), key)
	t.Cleanup(func() { _ = c.Delete(context.Background(), key) })

	bills := &mockBillRepo{statuses: []model.BillStatusIndexEntry{{ID: "hr-1-1", Status: "signed"}}}
	h, r := newBillStatusesRouter(bills)
	h.SetCache(c)

	first := serve(r, "/api/v1/bill-statuses?congress=1")
	if stored, getErr := c.Get(t.Context(), key); getErr != nil || !strings.Contains(stored, `"hr-1-1"`) {
		t.Errorf("Redis %s = %q, %v; want the response", key, stored, getErr)
	}
	second := serve(r, "/api/v1/bill-statuses?congress=1")
	if bills.statusCalls != 1 {
		t.Errorf("repo calls = %d, want 1 (second request from cache)", bills.statusCalls)
	}
	if second.Code != http.StatusOK || strings.TrimSpace(first.Body.String()) != second.Body.String() {
		t.Errorf("cached response %d %q differs from %q", second.Code, second.Body, first.Body)
	}
	if got := second.Header().Get("Cache-Control"); got != billIndexCacheControl {
		t.Errorf("Cache-Control on a cache hit = %q, want %q", got, billIndexCacheControl)
	}
}
