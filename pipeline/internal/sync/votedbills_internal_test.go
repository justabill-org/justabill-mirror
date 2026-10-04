package sync

import (
	"fmt"
	"maps"
	"net/http"
	"net/url"
	"slices"
	"testing"

	"github.com/justabill-org/justabill/pipeline/internal/congress"
	"github.com/justabill-org/justabill/pipeline/internal/upstream"
)

func TestSyncBillsByID_FetchesEachBillOnceAndMarksIt(t *testing.T) {
	store := &billSyncStore{}
	api := &billAPI{}
	s := newBillSyncService(t, store, api)

	got := s.SyncBillsByID(t.Context(), []string{"hr-119-2", "hr-119-3", "hr-119-2"})

	if got != 2 {
		t.Errorf("synced = %d, want 2", got)
	}
	if want := map[string]int{"2": 1, "3": 1}; !maps.Equal(api.details, want) {
		t.Errorf("detail requests = %v, want %v (once each, the duplicate ID too)", api.details, want)
	}
	slices.Sort(store.marked)
	if !slices.Equal(store.marked, []string{"hr-119-2", "hr-119-3"}) {
		t.Errorf("marked = %v, want [hr-119-2 hr-119-3]", store.marked)
	}
	// With no list entry, the latest action comes from the detail.
	if la := string(store.rows["hr-119-2"].LatestAction); la != `{"actionDate":"2025-01-03","text":"Introduced"}` {
		t.Errorf("latest_action = %s, want the detail's", la)
	}
}

func TestSyncBillsByID_SkipsUnresolvedAndFailedBills(t *testing.T) {
	store := &billSyncStore{}
	api := &billAPI{
		missingPaths: map[string]bool{"/bill/119/hr/404": true},
		failPaths:    map[string]bool{"/bill/119/hr/5/subjects": true},
	}
	s := newBillSyncService(t, store, api)

	ids := []string{
		"hr-119-404", // unknown to Congress.gov
		"hr-119-5",   // a sub-resource fails: logged, retried by the next run
		"hr-119-6",
	}
	// Malformed IDs are never requested.
	ids = append(ids, "hr-119-007", "pn-119-5", "hr-119", "hr-x-1", "hr-119-0")
	if got := s.SyncBillsByID(t.Context(), ids); got != 1 {
		t.Errorf("synced = %d, want 1", got)
	}
	// billAPI answers the 404 before counting it; the malformed IDs are never requested.
	if want := map[string]int{"5": 1, "6": 1}; !maps.Equal(api.details, want) {
		t.Errorf("served details = %v, want %v", api.details, want)
	}
	if !slices.Equal(store.marked, []string{"hr-119-6"}) {
		t.Errorf("marked = %v, want [hr-119-6]", store.marked)
	}
	if _, stored := store.rows["hr-119-404"]; stored {
		t.Error("an unknown bill was upserted")
	}
}

func TestSyncVotedBills_LoadsMissingBillsAndRecordsTheStep(t *testing.T) {
	store := &billSyncStore{missing: []string{"hr-119-1", "hr-119-404", "s-118-9x"}}
	api := &billAPI{missingPaths: map[string]bool{"/bill/119/hr/404": true}}
	s := newBillSyncService(t, store, api)

	if err := s.SyncVotedBills(t.Context(), 119, 0); err != nil {
		t.Fatalf("SyncVotedBills: %v (unresolved bills must not fail the step)", err)
	}
	if !slices.Equal(store.marked, []string{"hr-119-1"}) {
		t.Errorf("marked = %v, want [hr-119-1]", store.marked)
	}
	if run := store.success; run == nil || run.Step != stepVotedBills || run.Congress != 119 || run.ItemsSynced != 1 {
		t.Errorf("sync success = %+v, want step %s, congress 119, 1 item", run, stepVotedBills)
	}
}

func TestSyncVotedBills_LimitSyncsTheFirstBillsAndLeavesSyncState(t *testing.T) {
	store := &billSyncStore{missing: []string{"hr-119-1", "hr-119-2", "hr-119-3"}}
	api := &billAPI{}
	s := newBillSyncService(t, store, api)

	if err := s.SyncVotedBills(t.Context(), 119, 2); err != nil {
		t.Fatalf("SyncVotedBills: %v", err)
	}
	if want := map[string]int{"1": 1, "2": 1}; !maps.Equal(api.details, want) {
		t.Errorf("detail requests = %v, want %v", api.details, want)
	}
	if store.success != nil {
		t.Errorf("a limited run recorded success %+v, want sync_state left alone", store.success)
	}
}

func TestBillSummaryFromID(t *testing.T) {
	bs, err := billSummaryFromID("hjres-118-7")
	if want := (congress.BillSummary{Congress: 118, Type: "hjres", Number: "7"}); err != nil || bs != want {
		t.Errorf("billSummaryFromID(hjres-118-7) = %+v, %v; want %+v", bs, err, want)
	}
	if bs, err = billSummaryFromID("sconres-118-12"); err != nil || billID(bs, bs.Congress) != "sconres-118-12" {
		t.Errorf("billSummaryFromID(sconres-118-12) = %+v, %v", bs, err)
	}
}

func TestNotFound(t *testing.T) {
	status := func(code int) error {
		return &url.Error{Op: "Get", URL: "https://api.congress.gov/v3/bill/119/hr/1",
			Err: &upstream.StatusError{Host: "api.congress.gov", Path: "/v3/bill/119/hr/1", Status: code, Attempts: 1}}
	}
	tests := []struct {
		err  error
		want bool
	}{
		{fmt.Errorf("get: %w", congress.ErrNotFound), true},
		{status(http.StatusNotFound), true},
		{status(http.StatusInternalServerError), false},
		{errFakeStore, false},
		{nil, false},
	}
	for _, tt := range tests {
		if got := notFound(tt.err); got != tt.want {
			t.Errorf("notFound(%v) = %v, want %v", tt.err, got, tt.want)
		}
	}
}
