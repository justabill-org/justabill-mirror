package handler_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/justabill-org/justabill/db/model"
)

// billStore is a seeded store for a bill's list routes: the bills it holds, and each one's lists
// by bill ID. Like Spanner, a bill it doesn't hold reads as no bill and empty lists. listErr fails
// every list read and byIDErr fails GetByID; byIDReads counts GetByID calls.
type billStore struct {
	mockBillRepo

	bills      map[string]*model.Bill
	actions    map[string][]model.BillAction
	versions   map[string][]model.BillTextVersion
	diffs      map[string][]model.BillTextDiff
	amendments map[string][]model.Amendment
	gao        map[string][]model.GAOReport
	listErr    error
	byIDErr    error
	byIDReads  int
}

func (s *billStore) GetByID(_ context.Context, id string) (*model.Bill, error) {
	s.byIDReads++
	return s.bills[id], s.byIDErr
}
func (s *billStore) GetActions(_ context.Context, id string) ([]model.BillAction, error) {
	return s.actions[id], s.listErr
}
func (s *billStore) GetTextVersions(_ context.Context, id string) ([]model.BillTextVersion, error) {
	return s.versions[id], s.listErr
}
func (s *billStore) GetDiffs(_ context.Context, id string) ([]model.BillTextDiff, error) {
	return s.diffs[id], s.listErr
}
func (s *billStore) GetAmendments(_ context.Context, id string) ([]model.Amendment, error) {
	return s.amendments[id], s.listErr
}
func (s *billStore) ListGAOReports(_ context.Context, id string) ([]model.GAOReport, error) {
	return s.gao[id], s.listErr
}

// voteStore holds each bill's roll calls by bill ID; err fails every read.
type voteStore struct {
	mockVoteRepo

	votes map[string][]model.CongressionalVote
	err   error
}

func (s *voteStore) GetCongressionalVotes(_ context.Context, id string) ([]model.CongressionalVote, error) {
	return s.votes[id], s.err
}

const (
	seededBill = "hr-119-1"
	// quietBill exists but has nothing in any list.
	quietBill   = "s-119-2"
	unknownBill = "hr-119-999"
)

// seededBillStores holds seededBill, with one row in every list, and quietBill, with none.
func seededBillStores() (*billStore, *voteStore) {
	day := time.Date(2026, time.March, 4, 0, 0, 0, 0, time.UTC)
	bid := seededBill
	bills := &billStore{
		bills: map[string]*model.Bill{
			seededBill: {ID: seededBill, Title: "Lower Costs Act"},
			quietBill:  {ID: quietBill, Title: "Quiet Act"},
		},
		actions: map[string][]model.BillAction{seededBill: {{
			ID: "a1", BillID: seededBill, ActionDate: day, ActionText: "Introduced in House", SortOrder: 1,
		}}},
		versions: map[string][]model.BillTextVersion{seededBill: {{
			ID: "v1", BillID: seededBill, VersionType: "Introduced in House", VersionCode: "ih",
			Formats: json.RawMessage(`[{"type":"Formatted XML"}]`), SortOrder: 1,
		}}},
		diffs: map[string][]model.BillTextDiff{seededBill: {{
			ID: "d1", BillID: seededBill, FromVersionID: "v1", ToVersionID: "v2",
			DiffStats: json.RawMessage(`{"added":3}`),
		}}},
		amendments: map[string][]model.Amendment{seededBill: {{
			ID: "hamdt-119-7", BillID: seededBill, Congress: 119, AmendmentType: "hamdt", AmendmentNumber: 7,
			Chamber: "House",
		}}},
		gao: map[string][]model.GAOReport{seededBill: {{ReportID: "GAO-26-1", Title: "Drug Prices"}}},
	}
	votes := &voteStore{votes: map[string][]model.CongressionalVote{seededBill: {{
		ID: "h-119-1-42", BillID: &bid, Congress: 119, Chamber: "House", VoteDate: day,
	}}}}
	return bills, votes
}

// billListRouter serves a bill's list routes from bills and votes.
func billListRouter(bills *billStore, votes *voteStore) http.Handler {
	h := newTestHandler(bills)
	h.Votes = votes
	r := chi.NewRouter()
	r.Get("/api/v1/bills/{id}/actions", h.GetBillActions)
	r.Get("/api/v1/bills/{id}/votes", h.GetBillVotes)
	r.Get("/api/v1/bills/{id}/text", h.ListTextVersions)
	r.Get("/api/v1/bills/{id}/diffs", h.ListDiffs)
	r.Get("/api/v1/bills/{id}/amendments", h.ListAmendments)
	r.Get("/api/v1/bills/{id}/gao-reports", h.ListGAOReports)
	return r
}

// billList is one of a bill's list routes and seededBill's answer on it, written out by hand.
type billList struct {
	route, want string
}

// billLists are the routes under test.
func billLists() []billList {
	return []billList{
		{"actions", `[{"id":"a1","bill_id":"hr-119-1","action_date":"2026-03-04T00:00:00Z",
		"action_text":"Introduced in House","sort_order":1}]`},
		{"votes", `[{"id":"h-119-1-42","bill_id":"hr-119-1","congress":119,"chamber":"House",
		"vote_date":"2026-03-04T00:00:00Z"}]`},
		{"text", `[{"id":"v1","bill_id":"hr-119-1","version_type":"Introduced in House","version_code":"ih",
		"formats":[{"type":"Formatted XML"}],"sort_order":1}]`},
		{"diffs", `[{"id":"d1","bill_id":"hr-119-1","from_version_id":"v1","to_version_id":"v2",
		"diff_stats":{"added":3}}]`},
		{"amendments", `[{"id":"hamdt-119-7","bill_id":"hr-119-1","congress":119,"amendment_type":"hamdt",
		"amendment_number":7,"chamber":"House"}]`},
		{"gao-reports", `[{"report_id":"GAO-26-1","title":"Drug Prices"}]`},
	}
}

// assertJSON fails unless body is the same JSON value as want.
func assertJSON(t *testing.T, body []byte, want string) {
	t.Helper()
	var got, wantV any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode body %s: %v", body, err)
	}
	if err := json.Unmarshal([]byte(want), &wantV); err != nil {
		t.Fatalf("decode want: %v", err)
	}
	if !reflect.DeepEqual(got, wantV) {
		t.Errorf("body = %s, want %s", body, want)
	}
}

// assertHiddenServerError fails unless w is a 500 whose body doesn't carry errSpanner's text.
func assertHiddenServerError(t *testing.T, w *httptest.ResponseRecorder) {
	t.Helper()
	if w.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500 (%s)", w.Code, w.Body.String())
	}
	if strings.Contains(w.Body.String(), errSpanner.Error()) {
		t.Errorf("500 body leaks the repository error: %s", w.Body.String())
	}
	if msg := errorBody(t, w); msg == "" {
		t.Error("500 body has no error message")
	}
}

func TestBillLists_Seeded(t *testing.T) {
	for _, tt := range billLists() {
		t.Run(tt.route, func(t *testing.T) {
			bills, votes := seededBillStores()
			w := get(t, billListRouter(bills, votes), "/api/v1/bills/"+seededBill+"/"+tt.route)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
			}
			assertJSON(t, w.Body.Bytes(), tt.want)
			// A bill with rows needs no second read to tell it from an unknown bill.
			if bills.byIDReads != 0 {
				t.Errorf("GetByID reads = %d, want 0", bills.byIDReads)
			}
		})
	}
}

func TestBillLists_EmptyAnswersEmptyArray(t *testing.T) {
	for _, tt := range billLists() {
		t.Run(tt.route, func(t *testing.T) {
			bills, votes := seededBillStores()
			w := get(t, billListRouter(bills, votes), "/api/v1/bills/"+quietBill+"/"+tt.route)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200 (%s)", w.Code, w.Body.String())
			}
			assertJSON(t, w.Body.Bytes(), `[]`)
		})
	}
}

func TestBillLists_Errors(t *testing.T) {
	tests := []struct {
		name, id   string
		listErr    error
		byIDErr    error
		wantStatus int
		wantError  string
	}{
		{name: "unknown bill", id: unknownBill, wantStatus: http.StatusNotFound, wantError: "bill not found"},
		{name: "malformed id", id: "hr-119", wantStatus: http.StatusBadRequest, wantError: "invalid bill id"},
		{name: "unknown type", id: "xx-119-1", wantStatus: http.StatusBadRequest, wantError: "invalid bill id"},
		{name: "list read fails", id: seededBill, listErr: errSpanner, wantStatus: http.StatusInternalServerError},
		{name: "bill read fails", id: quietBill, byIDErr: errSpanner, wantStatus: http.StatusInternalServerError},
	}
	for _, route := range billLists() {
		for _, tt := range tests {
			t.Run(route.route+"/"+tt.name, func(t *testing.T) {
				bills, votes := seededBillStores()
				bills.listErr, bills.byIDErr, votes.err = tt.listErr, tt.byIDErr, tt.listErr
				w := get(t, billListRouter(bills, votes), "/api/v1/bills/"+tt.id+"/"+route.route)

				if tt.wantStatus == http.StatusInternalServerError {
					assertHiddenServerError(t, w)
					return
				}
				if w.Code != tt.wantStatus {
					t.Fatalf("status = %d, want %d (%s)", w.Code, tt.wantStatus, w.Body.String())
				}
				if got := errorBody(t, w); got != tt.wantError {
					t.Errorf("error = %q, want %q", got, tt.wantError)
				}
			})
		}
	}
}
