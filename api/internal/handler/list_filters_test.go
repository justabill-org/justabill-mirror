package handler_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/justabill-org/justabill/api/internal/handler"
	mw "github.com/justabill-org/justabill/api/internal/middleware"
	"github.com/justabill-org/justabill/db/model"
)

const (
	badCongressErr = "congress must be a whole number from 1 to 200"
	badDistrictErr = "district must be a whole number from 0 to 99"
)

// listReadsUserRepo records the params of every GetVotes and GetFavorites call.
type listReadsUserRepo struct {
	mockUserRepo

	calls []model.ListParams
}

func (m *listReadsUserRepo) GetVotes(
	ctx context.Context, userID string, params model.ListParams,
) (*model.ListResult[model.UserVote], error) {
	m.calls = append(m.calls, params)
	return m.mockUserRepo.GetVotes(ctx, userID, params)
}

func (m *listReadsUserRepo) GetFavorites(
	ctx context.Context, userID string, params model.ListParams,
) (*model.ListResult[model.UserFavorite], error) {
	m.calls = append(m.calls, params)
	return m.mockUserRepo.GetFavorites(ctx, userID, params)
}

// listRoute is one route that reads the list filters, served by its handler with a signed-in
// user in the context, and how to find the params its repository was read with.
type listRoute struct {
	path  string
	serve func(h *handler.Handler) http.HandlerFunc
	reads func(bills *mockBillRepo, members *memberStore, users *listReadsUserRepo) []model.ListParams
}

func billListReads(bills *mockBillRepo, _ *memberStore, _ *listReadsUserRepo) []model.ListParams {
	return bills.listCalls
}

func billCountReads(bills *mockBillRepo, _ *memberStore, _ *listReadsUserRepo) []model.ListParams {
	return bills.countCalls
}

func memberListReads(_ *mockBillRepo, members *memberStore, _ *listReadsUserRepo) []model.ListParams {
	return members.listCalls
}

func userListReads(_ *mockBillRepo, _ *memberStore, users *listReadsUserRepo) []model.ListParams {
	return users.calls
}

func listRoutes() []listRoute {
	return []listRoute{
		{"/api/v1/bills", func(h *handler.Handler) http.HandlerFunc { return h.ListBills }, billListReads},
		{"/api/v1/bills/counts", func(h *handler.Handler) http.HandlerFunc { return h.CountBills }, billCountReads},
		{"/api/v1/members", func(h *handler.Handler) http.HandlerFunc { return h.ListMembers }, memberListReads},
		{"/api/v1/me/votes", func(h *handler.Handler) http.HandlerFunc { return h.GetMyVotes }, userListReads},
		{"/api/v1/me/favorites", func(h *handler.Handler) http.HandlerFunc { return h.GetMyFavorites }, userListReads},
	}
}

// serveList sends GET path?query to the route's handler, signed in as u1, and returns the response
// and the params each repository read was made with.
func serveList(rt listRoute, query string) (*httptest.ResponseRecorder, []model.ListParams) {
	bills := &mockBillRepo{listResult: &model.ListResult[model.Bill]{}, counts: &model.BillCounts{}}
	members := seededMembers()
	users := &listReadsUserRepo{}
	h := newTestHandler(bills)
	h.Members = members
	h.Users = users

	req := httptest.NewRequest(http.MethodGet, rt.path+"?"+query, nil)
	req = req.WithContext(mw.ContextWithUserID(req.Context(), "u1"))
	w := httptest.NewRecorder()
	rt.serve(h)(w, req)
	return w, rt.reads(bills, members, users)
}

// TestListFilters_BadCongress: on every list route, a congress that isn't a whole number from 1 to
// 200 is a 400 before any read, not a filter silently dropped that lists every congress (#881).
func TestListFilters_BadCongress(t *testing.T) {
	for _, rt := range listRoutes() {
		for _, congress := range []string{"abc", "0", "-119", "+119", "201", "119.0", "1e2"} {
			t.Run(rt.path+"?congress="+congress, func(t *testing.T) {
				w, reads := serveList(rt, "congress="+congress)

				if w.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400 (%s)", w.Code, w.Body.String())
				}
				if got := errorBody(t, w); got != badCongressErr {
					t.Errorf("error = %q, want %q", got, badCongressErr)
				}
				if len(reads) != 0 {
					t.Errorf("repository read %d times, want 0", len(reads))
				}
			})
		}
	}
}

// TestListFilters_BadDistrict: district gets the same check, from 0 (at-large) to 99 (#881).
func TestListFilters_BadDistrict(t *testing.T) {
	for _, rt := range listRoutes() {
		if rt.path == "/api/v1/bills/counts" {
			continue // the counts don't read district
		}
		for _, district := range []string{"abc", "-1", "100", "1.5"} {
			t.Run(rt.path+"?district="+district, func(t *testing.T) {
				w, reads := serveList(rt, "state=CA&district="+district)

				if w.Code != http.StatusBadRequest {
					t.Fatalf("status = %d, want 400 (%s)", w.Code, w.Body.String())
				}
				if got := errorBody(t, w); got != badDistrictErr {
					t.Errorf("error = %q, want %q", got, badDistrictErr)
				}
				if len(reads) != 0 {
					t.Errorf("repository read %d times, want 0", len(reads))
				}
			})
		}
	}
}

// TestListFilters_GoodValues: a valid congress and district reach the repository as numbers, and
// leading zeros read as the same number (one cache key for 119 and 0119).
func TestListFilters_GoodValues(t *testing.T) {
	tests := []struct {
		query                      string
		wantCongress, wantDistrict *int
	}{
		{"", nil, nil},
		{"congress=1", new(1), nil},
		{"congress=200", new(200), nil},
		{"congress=0119", new(119), nil},
		{"district=0", nil, new(0)},
		{"district=99", nil, new(99)},
		{"congress=119&district=07", new(119), new(7)},
	}
	for _, rt := range listRoutes() {
		for _, tt := range tests {
			t.Run(rt.path+"?"+tt.query, func(t *testing.T) {
				w, reads := serveList(rt, tt.query)

				if w.Code != http.StatusOK {
					t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
				}
				checkFilters(t, rt, reads, tt.wantCongress, tt.wantDistrict)
			})
		}
	}
}

// checkFilters checks the route read its repository once, with the congress and district wanted
// (the counts don't read district).
func checkFilters(t *testing.T, rt listRoute, reads []model.ListParams, wantCongress, wantDistrict *int) {
	t.Helper()
	if len(reads) != 1 {
		t.Fatalf("repository read %d times, want 1", len(reads))
	}
	if got := reads[0].Congress; !equalIntPtr(got, wantCongress) {
		t.Errorf("congress = %s, want %s", showIntPtr(got), showIntPtr(wantCongress))
	}
	if rt.path == "/api/v1/bills/counts" {
		return
	}
	if got := reads[0].District; !equalIntPtr(got, wantDistrict) {
		t.Errorf("district = %s, want %s", showIntPtr(got), showIntPtr(wantDistrict))
	}
}

func equalIntPtr(a, b *int) bool {
	return (a == nil && b == nil) || (a != nil && b != nil && *a == *b)
}

func showIntPtr(p *int) string {
	if p == nil {
		return "none"
	}
	return strconv.Itoa(*p)
}
