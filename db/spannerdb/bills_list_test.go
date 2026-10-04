package spannerdb_test

import (
	"slices"
	"testing"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

// listIDs lists the fixture's bills with params and returns their IDs in order.
func listIDs(t *testing.T, repo *spannerdb.BillRepository, params model.ListParams) []string {
	t.Helper()
	params.Limit = 20
	res, err := repo.List(t.Context(), params)
	if err != nil {
		t.Fatalf("List(%+v): %v", params, err)
	}
	ids := make([]string, 0, len(res.Items))
	for _, b := range res.Items {
		ids = append(ids, b.ID)
	}
	return ids
}

// TestBillList_ChamberAndOrder checks the /bills filter and sort against the fixture (#453): the
// chamber matches in any case, no sort is newest introduced first, and sort=number breaks ties.
func TestBillList_ChamberAndOrder(t *testing.T) {
	client := testdb.New(t)
	testdb.SeedFixture(t.Context(), t, client)
	repo := spannerdb.NewBillRepo(&spannerdb.Client{Spanner: client})
	str := func(s string) *string { return &s }

	tests := []struct {
		name   string
		params model.ListParams
		want   []string
	}{
		// hjres-119-63 (April 2025) and sjres-119-41 (March) came after hr-119-1, hr-119-808 and
		// s-119-1, introduced the same day, after hr-118-1.
		{"no sort is newest first", model.ListParams{},
			[]string{testdb.FixtureCRAUnmatched, testdb.FixtureCRABill, testdb.FixtureHouseBill,
				testdb.FixtureLawBill, testdb.FixtureSenateBill, testdb.FixturePrevHouseBill}},
		{"number breaks ties on type", model.ListParams{Sort: str("number")},
			[]string{testdb.FixtureLawBill, testdb.FixtureCRAUnmatched, testdb.FixtureCRABill,
				testdb.FixtureHouseBill, testdb.FixtureSenateBill, testdb.FixturePrevHouseBill}},
		{"chamber house", model.ListParams{Chamber: str("house")},
			[]string{testdb.FixtureCRAUnmatched, testdb.FixtureHouseBill, testdb.FixtureLawBill,
				testdb.FixturePrevHouseBill}},
		{"chamber HOUSE", model.ListParams{Chamber: str("HOUSE")},
			[]string{testdb.FixtureCRAUnmatched, testdb.FixtureHouseBill, testdb.FixtureLawBill,
				testdb.FixturePrevHouseBill}},
		{"chamber Senate", model.ListParams{Chamber: str("Senate")},
			[]string{testdb.FixtureCRABill, testdb.FixtureSenateBill}},
		{"chamber senate", model.ListParams{Chamber: str("senate")},
			[]string{testdb.FixtureCRABill, testdb.FixtureSenateBill}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := listIDs(t, repo, tt.params); !slices.Equal(got, tt.want) {
				t.Errorf("ids = %v, want %v", got, tt.want)
			}
		})
	}

	// One bill per page: every bill shows up exactly once.
	var paged []string
	for off := range 6 {
		res, err := repo.List(t.Context(), model.ListParams{Sort: str("number"), Limit: 1, Offset: off})
		if err != nil {
			t.Fatalf("List page %d: %v", off, err)
		}
		for _, b := range res.Items {
			paged = append(paged, b.ID)
		}
	}
	want := []string{testdb.FixtureLawBill, testdb.FixtureCRAUnmatched, testdb.FixtureCRABill,
		testdb.FixtureHouseBill, testdb.FixtureSenateBill, testdb.FixturePrevHouseBill}
	if !slices.Equal(paged, want) {
		t.Errorf("paged ids = %v, want %v", paged, want)
	}
}
