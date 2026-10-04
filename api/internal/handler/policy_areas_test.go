package handler_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/justabill-org/justabill/api/internal/handler"
	"github.com/justabill-org/justabill/db/model"
)

const policyAreasCacheControl = "public, max-age=3600, s-maxage=3600, stale-while-revalidate=86400"

func newPolicyAreasRouter(bills *mockBillRepo) (*handler.Handler, *chi.Mux) {
	h := newTestHandler(bills)
	r := chi.NewRouter()
	r.Get("/api/v1/policy-areas", h.ListPolicyAreas)
	return h, r
}

func TestListPolicyAreas(t *testing.T) {
	bills := &mockBillRepo{policyAreas: []string{"Agriculture and Food", "Health", "Taxation"}}
	_, r := newPolicyAreasRouter(bills)

	w := serve(r, "/api/v1/policy-areas")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body)
	}
	want := `{"policy_areas":["Agriculture and Food","Health","Taxation"]}`
	if got := strings.TrimSpace(w.Body.String()); got != want {
		t.Errorf("body = %s\nwant %s", got, want)
	}
	if got := w.Header().Get("Cache-Control"); got != policyAreasCacheControl {
		t.Errorf("Cache-Control = %q, want %q", got, policyAreasCacheControl)
	}
}

func TestListPolicyAreas_Empty(t *testing.T) {
	_, r := newPolicyAreasRouter(&mockBillRepo{})
	w := serve(r, "/api/v1/policy-areas")
	if got := strings.TrimSpace(w.Body.String()); w.Code != http.StatusOK || got != `{"policy_areas":[]}` {
		t.Errorf("empty database: %d %s, want 200 with an empty list", w.Code, got)
	}
}

func TestListPolicyAreas_Error(t *testing.T) {
	_, r := newPolicyAreasRouter(&mockBillRepo{policyAreasErr: errors.New("spanner down")})
	w := serve(r, "/api/v1/policy-areas")
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500: %s", w.Code, w.Body)
	}
	if got := w.Header().Get("Cache-Control"); got != "" {
		t.Errorf("Cache-Control = %q on a 500, want none", got)
	}
}

func TestListPolicyAreas_Caching(t *testing.T) {
	c := testCache(t)
	const key = "policy-areas"
	_ = c.Delete(t.Context(), key)
	t.Cleanup(func() { _ = c.Delete(context.Background(), key) })

	bills := &mockBillRepo{policyAreas: []string{"Health"}}
	h, r := newPolicyAreasRouter(bills)
	h.SetCache(c)

	first := serve(r, "/api/v1/policy-areas")
	second := serve(r, "/api/v1/policy-areas")
	if bills.policyAreaCalls != 1 {
		t.Errorf("repo calls = %d, want 1 (second request from cache)", bills.policyAreaCalls)
	}
	if second.Code != http.StatusOK || strings.TrimSpace(first.Body.String()) != second.Body.String() {
		t.Errorf("cached response %d %q differs from %q", second.Code, second.Body, first.Body)
	}
	if got := second.Header().Get("Cache-Control"); got != policyAreasCacheControl {
		t.Errorf("Cache-Control on a cache hit = %q, want %q", got, policyAreasCacheControl)
	}
}

// TestListBills_PolicyArea checks that GET /bills passes policy_area to the repository with the
// other filters (#708).
func TestListBills_PolicyArea(t *testing.T) {
	bills := &mockBillRepo{listResult: &model.ListResult[model.Bill]{Items: []model.Bill{}}}
	h := newTestHandler(bills)

	req := httptest.NewRequest(http.MethodGet,
		"/api/v1/bills?policy_area=Armed+Forces+and+National+Security&status=became_law&status_mode=past", nil)
	w := httptest.NewRecorder()
	h.ListBills(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200: %s", w.Code, w.Body)
	}
	if len(bills.listCalls) != 1 {
		t.Fatalf("List calls = %d, want 1", len(bills.listCalls))
	}
	p := bills.listCalls[0]
	if p.PolicyArea == nil || *p.PolicyArea != "Armed Forces and National Security" {
		t.Errorf("PolicyArea = %v, want Armed Forces and National Security", p.PolicyArea)
	}
	if len(p.Statuses) != 1 || p.Statuses[0] != "became_law" || p.StatusMode != "past" {
		t.Errorf("status = %v %q, want became_law past", p.Statuses, p.StatusMode)
	}
}
