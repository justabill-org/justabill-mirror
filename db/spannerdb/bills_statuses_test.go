package spannerdb_test

import (
	"slices"
	"testing"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

// statusBill is a bill row for the status and latest-action tests: its status, and its
// latest_action JSON (an invalid NullJSON stores SQL NULL).
type statusBill struct {
	id, status string
	action     spanner.NullJSON
}

// actionOn is a latest_action dated date, as the pipeline stores Congress.gov's.
func actionOn(date string) spanner.NullJSON {
	return spanner.NullJSON{Value: map[string]string{"actionDate": date, "text": "An action."}, Valid: true}
}

// seedStatusBills seeds the 119th congress and bills, numbered from their IDs' order.
func seedStatusBills(t *testing.T, client *spanner.Client, bills []statusBill) {
	t.Helper()
	testdb.SeedCongress(t.Context(), t, client, 119)
	muts := make([]*spanner.Mutation, 0, len(bills))
	for i, b := range bills {
		muts = append(muts, spanner.Insert("bills",
			[]string{"bill_id", "congress", "bill_type", "number", "title", "current_status", "latest_action"},
			[]any{b.id, int64(119), "hr", int64(i + 1), "Bill " + b.id, b.status, b.action}))
	}
	if _, err := client.Apply(t.Context(), muts); err != nil {
		t.Fatalf("seed bills: %v", err)
	}
}

// TestBillListStatusesAndLatestAction checks the Laws view's query (#712): several statuses make
// one list with one total, sorted by the latest action's date, newest first, with ties broken by
// bill_id and bills without a date last, so one bill per page neither repeats nor skips a bill.
func TestBillListStatusesAndLatestAction(t *testing.T) {
	client := testdb.New(t)
	seedStatusBills(t, client, []statusBill{
		{"hr-119-1", "became_law", actionOn("2026-03-01")},
		{"hr-119-2", "signed", actionOn("2026-05-01")},
		{"hr-119-3", "signed", actionOn("2026-05-01")},
		{"hr-119-4", "in_committee", actionOn("2026-06-01")},
		{"hr-119-5", "became_law", spanner.NullJSON{}},
		{"hr-119-6", "became_law", spanner.NullJSON{Value: map[string]string{"text": "No date."}, Valid: true}},
		{"hr-119-7", "to_president", spanner.NullJSON{Value: nil, Valid: true}},
		{"hr-119-8", "became_law", actionOn("2025-12-31")},
	})
	repo := spannerdb.NewBillRepo(&spannerdb.Client{Spanner: client})
	latest := "latest_action"

	tests := []struct {
		name     string
		statuses []string
		want     []string
	}{
		{"laws", []string{"became_law", "signed"},
			[]string{"hr-119-2", "hr-119-3", "hr-119-1", "hr-119-8", "hr-119-5", "hr-119-6"}},
		{"one status", []string{"signed"}, []string{"hr-119-2", "hr-119-3"}},
		{"every bill", nil,
			[]string{"hr-119-4", "hr-119-2", "hr-119-3", "hr-119-1", "hr-119-8", "hr-119-5", "hr-119-6", "hr-119-7"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, limit := range []int{1, 20} {
				ids := pageAll(t, limit, func(p model.ListParams) (*model.ListResult[model.Bill], error) {
					p.Statuses, p.Sort = tt.statuses, &latest
					return repo.List(t.Context(), p)
				}, func(b model.Bill) string { return b.ID })
				assertOnce(t, ids)
				if !slices.Equal(ids, tt.want) {
					t.Errorf("limit %d: ids = %v, want %v", limit, ids, tt.want)
				}
			}
		})
	}
}
