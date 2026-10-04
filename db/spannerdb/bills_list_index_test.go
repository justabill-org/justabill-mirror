package spannerdb_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"cloud.google.com/go/civil"
	"cloud.google.com/go/spanner"
	"google.golang.org/api/iterator"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

// indexBill is a bills row for the list-index tests (#760). A zero introduced is a NULL
// introduced_date; action is the latest_action JSON (an invalid NullJSON stores SQL NULL).
type indexBill struct {
	id, billType   string
	congress, num  int
	introduced     string
	action         spanner.NullJSON
	chamber        string
	status         string
	policyArea     spanner.NullString
	wantLatestDate spanner.NullString
}

// indexBills cover Review Focus 1 and 3: three bills tie on introduced_date and two on the
// latest action's date; s-119-1 has no introduced_date and a latest action without a date,
// hr-119-3 has no latest action and s-119-2 a JSON null one. hr-118-1 is in another congress and
// would sort first in either order, so it shows whether the congress filter holds.
func indexBills() []indexBill {
	health := spanner.NullString{StringVal: "Health", Valid: true}
	dated := func(d string) spanner.NullString { return spanner.NullString{StringVal: d, Valid: true} }
	return []indexBill{
		{"hr-119-1", "hr", 119, 1, "2025-03-01", actionOn("2026-05-01"), "House", "in_committee", health,
			dated("2026-05-01")},
		{"hr-119-2", "hr", 119, 2, "2025-03-01", actionOn("2026-05-01"), "House", "passed_house", health,
			dated("2026-05-01")},
		{"hr-119-3", "hr", 119, 3, "2025-06-01", spanner.NullJSON{}, "House", "in_committee",
			spanner.NullString{StringVal: "Taxation", Valid: true}, spanner.NullString{}},
		{"hr-119-4", "hr", 119, 4, "2025-03-01", actionOn("2026-07-01"), "House", "in_committee", health,
			dated("2026-07-01")},
		{"s-119-1", "s", 119, 1, "",
			spanner.NullJSON{Value: map[string]string{"text": "No date."}, Valid: true},
			"Senate", "in_committee", health, spanner.NullString{}},
		{"s-119-2", "s", 119, 2, "2025-01-15", spanner.NullJSON{Value: nil, Valid: true}, "Senate",
			"passed_senate", spanner.NullString{}, spanner.NullString{}},
		{"hr-118-1", "hr", 118, 1, "2025-12-01", actionOn("2026-08-01"), "House", "in_committee", health,
			dated("2026-08-01")},
	}
}

// seedIndexBills writes indexBills (and their congresses) through ordinary mutations, as the
// pipeline does, and fails the test if any write fails.
func seedIndexBills(t *testing.T, client *spanner.Client) {
	t.Helper()
	testdb.SeedCongress(t.Context(), t, client, 118)
	testdb.SeedCongress(t.Context(), t, client, 119)
	var muts []*spanner.Mutation
	for _, b := range indexBills() {
		var introduced spanner.NullDate
		if b.introduced != "" {
			d, err := civil.ParseDate(b.introduced)
			if err != nil {
				t.Fatal(err)
			}
			introduced = spanner.NullDate{Date: d, Valid: true}
		}
		muts = append(muts, spanner.Insert("bills",
			[]string{"bill_id", "congress", "bill_type", "number", "title", "introduced_date", "latest_action",
				"origin_chamber", "current_status", "policy_area"},
			[]any{b.id, int64(b.congress), b.billType, int64(b.num), "Bill " + b.id, introduced, b.action,
				b.chamber, b.status, b.policyArea}))
	}
	if _, err := client.Apply(t.Context(), muts); err != nil {
		t.Fatalf("seed bills: %v", err)
	}
}

// TestBillsLatestActionDate checks migration 27's generated column on the rows indexBills writes:
// the latest action's date, and NULL (not a failed write) for no latest action, a JSON null one or
// one without a date (Review Focus 1).
func TestBillsLatestActionDate(t *testing.T) {
	client := testdb.New(t)
	seedIndexBills(t, client)
	iter := client.Single().Query(t.Context(), spanner.Statement{
		SQL: "SELECT bill_id, latest_action_date FROM bills",
	})
	defer iter.Stop()
	got := map[string]spanner.NullString{}
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		var id string
		var date spanner.NullString
		if err = row.Columns(&id, &date); err != nil {
			t.Fatal(err)
		}
		got[id] = date
	}
	for _, b := range indexBills() {
		if got[b.id] != b.wantLatestDate {
			t.Errorf("%s: latest_action_date = %v, want %v", b.id, got[b.id], b.wantLatestDate)
		}
	}
}

// TestBillListIndexedOrders pages the list through the ordered indexes (a congress, no search)
// and through bills (no congress), one, two and twenty bills a page: every bill shows up once, in
// the order the list always had, NULL dates last and ties by bill_id, with filters applied in
// the index and the same total on every page (Review Focus 1 to 3).
func TestBillListIndexedOrders(t *testing.T) {
	client := testdb.New(t)
	seedIndexBills(t, client)
	testdb.SeedUser(t.Context(), t, client, "u1")
	if _, err := client.Apply(t.Context(), []*spanner.Mutation{spanner.Insert("user_votes",
		[]string{"user_id", "bill_id", "vote", "voted_at"},
		[]any{"u1", "hr-119-1", "yea", time.Date(2026, time.October, 3, 12, 0, 0, 0, time.UTC)})}); err != nil {
		t.Fatalf("seed vote: %v", err)
	}
	// updated_at for sort=updated_at, in an order neither date order has: hr-119-3 keeps NULL
	// (last), and hr-118-1, the newest, is in another congress.
	var updates []*spanner.Mutation
	for id, day := range map[string]int{"hr-119-4": 3, "s-119-1": 2, "hr-119-1": 1, "hr-118-1": 30} {
		updates = append(updates, spanner.Update("bills", []string{"bill_id", "updated_at"},
			[]any{id, time.Date(2026, time.September, day, 0, 0, 0, 0, time.UTC)}))
	}
	if _, err := client.Apply(t.Context(), updates); err != nil {
		t.Fatalf("seed updated_at: %v", err)
	}
	repo := spannerdb.NewBillRepo(&spannerdb.Client{Spanner: client})
	str := func(s string) *string { return &s }
	congress := 119

	tests := []struct {
		name   string
		params model.ListParams
		want   []string
	}{
		{"congress, newest introduced", model.ListParams{Congress: &congress},
			[]string{"hr-119-3", "hr-119-1", "hr-119-2", "hr-119-4", "s-119-2", "s-119-1"}},
		{"congress, latest action", model.ListParams{Congress: &congress, Sort: str("latest_action")},
			[]string{"hr-119-4", "hr-119-1", "hr-119-2", "hr-119-3", "s-119-1", "s-119-2"}},
		{"all congresses, newest introduced", model.ListParams{},
			[]string{"hr-118-1", "hr-119-3", "hr-119-1", "hr-119-2", "hr-119-4", "s-119-2", "s-119-1"}},
		{"all congresses, latest action", model.ListParams{Sort: str("latest_action")},
			[]string{"hr-118-1", "hr-119-4", "hr-119-1", "hr-119-2", "hr-119-3", "s-119-1", "s-119-2"}},
		{"congress, number", model.ListParams{Congress: &congress, Sort: str("number")},
			[]string{"hr-119-4", "hr-119-3", "hr-119-2", "s-119-2", "hr-119-1", "s-119-1"}},
		{"status", model.ListParams{Congress: &congress, Statuses: []string{"in_committee"}},
			[]string{"hr-119-3", "hr-119-1", "hr-119-4", "s-119-1"}},
		{"statuses, latest action", model.ListParams{Congress: &congress, Sort: str("latest_action"),
			Statuses: []string{"passed_house", "passed_senate"}},
			[]string{"hr-119-2", "s-119-2"}},
		{"status, latest action", model.ListParams{Congress: &congress, Sort: str("latest_action"),
			Statuses: []string{"in_committee"}},
			[]string{"hr-119-4", "hr-119-1", "hr-119-3", "s-119-1"}},
		{"status and every index-stored filter, latest action", model.ListParams{Congress: &congress,
			Sort: str("latest_action"), Statuses: []string{"in_committee"}, BillType: str("hr"),
			Chamber: str("house"), PolicyArea: str("health"), UnvotedBy: str("u1")},
			[]string{"hr-119-4"}},
		{"status, updated_at", model.ListParams{Congress: &congress, Sort: str("updated_at"),
			Statuses: []string{"in_committee"}},
			[]string{"hr-119-4", "s-119-1", "hr-119-1", "hr-119-3"}},
		{"chamber, latest action", model.ListParams{Congress: &congress, Sort: str("latest_action"),
			Chamber: str("senate")},
			[]string{"s-119-1", "s-119-2"}},
		{"bill type", model.ListParams{Congress: &congress, BillType: str("s")},
			[]string{"s-119-2", "s-119-1"}},
		{"policy area", model.ListParams{Congress: &congress, PolicyArea: str("health")},
			[]string{"hr-119-1", "hr-119-2", "hr-119-4", "s-119-1"}},
		{"unvoted", model.ListParams{Congress: &congress, UnvotedBy: str("u1")},
			[]string{"hr-119-3", "hr-119-2", "hr-119-4", "s-119-2", "s-119-1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for _, limit := range []int{1, 2, 20} {
				ids := pageAll(t, limit, func(p model.ListParams) (*model.ListResult[model.Bill], error) {
					params := tt.params
					params.Offset, params.Limit = p.Offset, p.Limit
					return repo.List(t.Context(), params)
				}, func(b model.Bill) string { return b.ID })
				if !slices.Equal(ids, tt.want) {
					t.Errorf("limit %d: ids = %v, want %v", limit, ids, tt.want)
				}
			}
		})
	}
}

// TestBillListPreviousSQL runs the list queries the API ran before #760, verbatim, on the
// migrated schema: during a release the migrations apply before the new image rolls out, so the
// old image's queries must still work and return what the new ones do (Review Focus 4).
func TestBillListPreviousSQL(t *testing.T) {
	client := testdb.New(t)
	seedIndexBills(t, client)
	params := map[string]any{"congress": int64(119), "lim": int64(20), "off": int64(0)}

	var total int64
	err := client.Single().Query(t.Context(), spanner.Statement{
		SQL: "SELECT COUNT(*) FROM bills WHERE TRUE AND congress = @congress", Params: params,
	}).Do(func(r *spanner.Row) error { return r.Columns(&total) })
	if err != nil || total != 6 {
		t.Errorf("old count = %d, %v; want 6", total, err)
	}

	tests := []struct {
		name, orderBy string
		want          []string
	}{
		{"newest introduced", "ORDER BY introduced_date DESC, bill_id",
			[]string{"hr-119-3", "hr-119-1", "hr-119-2", "hr-119-4", "s-119-2", "s-119-1"}},
		{"latest action", "ORDER BY JSON_VALUE(latest_action, '$.actionDate') DESC, bill_id",
			[]string{"hr-119-4", "hr-119-1", "hr-119-2", "hr-119-3", "s-119-1", "s-119-2"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sql := `SELECT bill_id, congress, bill_type, number, title, introduced_date, origin_chamber,
		        latest_action, current_status, status_date, policy_area,
		        sponsors, cosponsors, committees, subjects, related_bills, laws, updated_at, synced_at
		 FROM bills WHERE TRUE AND congress = @congress
		 ` + tt.orderBy + `
		 LIMIT @lim OFFSET @off`
			var ids []string
			selectErr := client.Single().Query(t.Context(), spanner.Statement{SQL: sql, Params: params}).
				Do(func(r *spanner.Row) error {
					var id string
					if colErr := r.ColumnByName("bill_id", &id); colErr != nil {
						return colErr
					}
					ids = append(ids, id)
					return nil
				})
			if selectErr != nil {
				t.Fatalf("old select: %v", selectErr)
			}
			if !slices.Equal(ids, tt.want) {
				t.Errorf("ids = %v, want %v", ids, tt.want)
			}
		})
	}
}
