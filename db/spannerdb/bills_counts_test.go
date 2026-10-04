package spannerdb_test

import (
	"maps"
	"testing"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

// TestBillCountByStatusMatchesList checks GET /bills/counts' query (#713): under each of the list's
// filters, the count of a status is the total List gives for that status, a bill with no status
// counts in Total only, and Total is List's total. The status filter is ignored, since the counts
// are what the status views split.
func TestBillCountByStatusMatchesList(t *testing.T) {
	client := testdb.New(t)
	seedStatusBills(t, client, []statusBill{
		{"hr-119-1", "became_law", actionOn("2026-03-01")},
		{"hr-119-2", "signed", actionOn("2026-05-01")},
		{"hr-119-3", "in_committee", actionOn("2026-06-01")},
		{"hr-119-4", "in_committee", actionOn("2026-06-02")},
		{"hr-119-5", "passed_house", actionOn("2026-06-03")},
	})
	testdb.SeedBill(t.Context(), t, client, "s-119-9", 119, "s", 9, "Farm bill")
	testdb.SeedCongress(t.Context(), t, client, 118)
	testdb.SeedBill(t.Context(), t, client, "hr-118-7", 118, "hr", 7, "Farm aid")
	repo := spannerdb.NewBillRepo(&spannerdb.Client{Spanner: client})
	c119, c118, senate, farm := 119, 118, "s", "farm"

	tests := []struct {
		name         string
		params       model.ListParams
		wantByStatus map[string]int
		wantTotal    int
	}{
		{"every bill", model.ListParams{},
			map[string]int{"became_law": 1, "signed": 1, "in_committee": 2, "passed_house": 1}, 7},
		{"congress", model.ListParams{Congress: &c119},
			map[string]int{"became_law": 1, "signed": 1, "in_committee": 2, "passed_house": 1}, 6},
		{"no status only", model.ListParams{Congress: &c118}, map[string]int{}, 1},
		{"type", model.ListParams{BillType: &senate}, map[string]int{}, 1},
		{"search", model.ListParams{Search: &farm}, map[string]int{}, 2},
		{"status filter ignored", model.ListParams{Congress: &c119, Statuses: []string{"signed"}},
			map[string]int{"became_law": 1, "signed": 1, "in_committee": 2, "passed_house": 1}, 6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := repo.CountByStatus(t.Context(), tt.params)
			if err != nil {
				t.Fatalf("CountByStatus: %v", err)
			}
			if !maps.Equal(got.ByStatus, tt.wantByStatus) || got.Total != tt.wantTotal {
				t.Fatalf("CountByStatus = %v, total %d; want %v, total %d",
					got.ByStatus, got.Total, tt.wantByStatus, tt.wantTotal)
			}

			assertCountsMatchList(t, repo, tt.params, got)
		})
	}
}

// assertCountsMatchList checks counts against List under the same filters: Total is List's total,
// and each status's count is List's total for that status alone.
func assertCountsMatchList(
	t *testing.T,
	repo *spannerdb.BillRepository,
	params model.ListParams,
	counts *model.BillCounts,
) {
	t.Helper()
	params.Limit, params.Statuses = 1, nil
	all, err := repo.List(t.Context(), params)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if all.Total != counts.Total {
		t.Errorf("Total = %d, List's total = %d", counts.Total, all.Total)
	}
	for status, n := range counts.ByStatus {
		params.Statuses = []string{status}
		page, listErr := repo.List(t.Context(), params)
		if listErr != nil {
			t.Fatalf("List %s: %v", status, listErr)
		}
		if page.Total != n {
			t.Errorf("by_status[%s] = %d, List's total = %d", status, n, page.Total)
		}
	}
}

// TestBillCountByStatusEmpty checks a filter nothing matches: no statuses, total 0, and a map
// rather than nil, so the API answers {"by_status":{}} and not null.
func TestBillCountByStatusEmpty(t *testing.T) {
	client := testdb.New(t)
	repo := spannerdb.NewBillRepo(&spannerdb.Client{Spanner: client})
	got, err := repo.CountByStatus(t.Context(), model.ListParams{})
	if err != nil {
		t.Fatalf("CountByStatus: %v", err)
	}
	if got.ByStatus == nil || len(got.ByStatus) != 0 || got.Total != 0 {
		t.Errorf("CountByStatus = %#v, want an empty map and total 0", got)
	}
}
