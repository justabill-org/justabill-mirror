package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/justabill-org/justabill/api/internal/handler"
	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
)

// countBills sends GET /bills/counts?query to h and returns the response.
func countBills(h *handler.Handler, query string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, "/api/v1/bills/counts?"+query, nil)
	w := httptest.NewRecorder()
	h.CountBills(w, req)
	return w
}

// TestCountBills checks GET /bills/counts (#713): it passes the list's filters to the repository,
// ignores the list's paging, sort, status and personal parameters, and answers the counts.
func TestCountBills(t *testing.T) {
	bills := &mockBillRepo{counts: &model.BillCounts{
		ByStatus: map[string]int{"became_law": 3, "in_committee": 40}, Total: 45,
	}}
	h := newTestHandler(bills)

	w := countBills(h, "congress=119&type=hr&chamber=house&policy_area=Agriculture+and+Food&q=farm"+
		"&offset=40&limit=5&sort=latest_action&status=signed&status_mode=past&unvoted=true&include=summary")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body)
	}
	var got model.BillCounts
	if err := json.NewDecoder(w.Body).Decode(&got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !maps.Equal(got.ByStatus, bills.counts.ByStatus) || got.Total != 45 {
		t.Errorf("body = %+v, want %+v", got, *bills.counts)
	}

	if len(bills.countCalls) != 1 {
		t.Fatalf("CountByStatus calls = %d, want 1", len(bills.countCalls))
	}
	congress, billType, chamber, area, search := 119, "hr", "house", "Agriculture and Food", "farm"
	want := model.ListParams{
		Congress: &congress, BillType: &billType, Chamber: &chamber, PolicyArea: &area, Search: &search,
	}
	if key, wantKey := handler.ListCacheKey("", bills.countCalls[0]), handler.ListCacheKey("", want); key != wantKey {
		t.Errorf("CountByStatus params = %s, want %s", key, wantKey)
	}
	if p := bills.countCalls[0]; p.UnvotedBy != nil || p.Statuses != nil || p.Sort != nil {
		t.Errorf("CountByStatus params carry list-only fields: %+v", p)
	}
	if len(bills.listCalls) != 0 {
		t.Errorf("List calls = %d, want 0: the counts are one query", len(bills.listCalls))
	}
}

// TestCountBills_Errors checks the 400s and the 500: a search or policy area the list refuses, a
// search Spanner can't read, and a failed query.
func TestCountBills_Errors(t *testing.T) {
	tests := []struct {
		name     string
		query    string
		countErr error
		want     int
	}{
		{"search too long", "q=a+b+c+d+e+f+g+h+i", nil, http.StatusBadRequest},
		{"policy area too long", "policy_area=" + strings.Repeat("a", 101), nil, http.StatusBadRequest},
		{"search Spanner can't read", "q=farm", repository.ErrInvalidSearch, http.StatusBadRequest},
		{"query fails", "congress=119", errors.New("spanner down"), http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newTestHandler(&mockBillRepo{countErr: tt.countErr})
			if w := countBills(h, tt.query); w.Code != tt.want {
				t.Errorf("status = %d, want %d: %s", w.Code, tt.want, w.Body)
			}
		})
	}
}

// TestCountBills_Cache checks the counts are cached under the filters alone: a second request for
// another page or view of the same filters is served from Redis, and other filters aren't.
func TestCountBills_Cache(t *testing.T) {
	c := testCache(t)
	// A search unique to this run, so runs sharing a Redis don't see each other's keys.
	search := fmt.Sprintf("counts%d", time.Now().UnixNano())
	congress := 119
	t.Cleanup(func() {
		for _, p := range []model.ListParams{{Search: &search}, {Search: &search, Congress: &congress}} {
			_ = c.Delete(context.Background(), handler.ListCacheKey("bills:counts", p))
		}
	})

	bills := &mockBillRepo{counts: &model.BillCounts{ByStatus: map[string]int{"signed": 2}, Total: 2}}
	h := newTestHandler(bills)
	h.SetCache(c)

	for _, query := range []string{
		"q=" + search, // reads the repository, then caches
		"q=" + search + "&offset=20&status=signed&sort=number", // the same filters: from the cache
		"q=" + search + "&congress=119",                        // other filters: the repository
	} {
		if w := countBills(h, query); w.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200", query, w.Code)
		}
	}
	if len(bills.countCalls) != 2 {
		t.Errorf("CountByStatus calls = %d, want 2 (only the second request is a cache hit)", len(bills.countCalls))
	}
}
