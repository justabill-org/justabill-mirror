package spannerdb_test

import (
	"slices"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

// seedPolicyAreaBills syncs four bills through the pipeline's UpsertBill, which also writes their
// policy_areas rows: two on Health (one of them in the 118th), one on Taxation and one with none.
func seedPolicyAreaBills(t *testing.T, client *spannerdb.Client) {
	t.Helper()
	ctx := t.Context()
	testdb.SeedCongress(ctx, t, client.Spanner, 118)
	testdb.SeedCongress(ctx, t, client.Spanner, 119)
	store := spannerdb.NewPipelineStore(client)
	str := func(s string) *string { return &s }
	day := time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)
	rows := []repository.BillRow{
		{ID: "hr-119-11", Congress: 119, BillType: "hr", Number: 11, Title: "A", PolicyArea: str("Health")},
		{ID: "hr-119-12", Congress: 119, BillType: "hr", Number: 12, Title: "B", PolicyArea: str("Taxation")},
		{ID: "hr-119-13", Congress: 119, BillType: "hr", Number: 13, Title: "C"},
		{ID: "hr-118-14", Congress: 118, BillType: "hr", Number: 14, Title: "D", PolicyArea: str("Health")},
		{ID: "s-119-15", Congress: 119, BillType: "s", Number: 15, Title: "E",
			PolicyArea: str("Armed Forces and National Security")},
	}
	for i, b := range rows {
		introduced := day.AddDate(0, 0, -i)
		b.IntroducedDate = &introduced
		if err := store.UpsertBill(ctx, b); err != nil {
			t.Fatalf("UpsertBill(%s): %v", b.ID, err)
		}
	}
}

// TestBillList_PolicyArea checks GET /bills's policy_area filter (#708): it matches the name in any
// case, combines with the other filters, and a name no bill has matches nothing.
func TestBillList_PolicyArea(t *testing.T) {
	c := &spannerdb.Client{Spanner: testdb.New(t)}
	seedPolicyAreaBills(t, c)
	repo := spannerdb.NewBillRepo(c)
	str := func(s string) *string { return &s }
	congress := 119

	tests := []struct {
		name   string
		params model.ListParams
		want   []string
	}{
		{"Health", model.ListParams{PolicyArea: str("Health")}, []string{"hr-119-11", "hr-118-14"}},
		{"any case", model.ListParams{PolicyArea: str("health")}, []string{"hr-119-11", "hr-118-14"}},
		{"with congress", model.ListParams{PolicyArea: str("Health"), Congress: &congress}, []string{"hr-119-11"}},
		{"with chamber", model.ListParams{PolicyArea: str("Health"), Chamber: str("senate")}, []string{}},
		{"several words", model.ListParams{PolicyArea: str("Armed Forces and National Security")},
			[]string{"s-119-15"}},
		{"unknown", model.ListParams{PolicyArea: str("Not a policy area")}, []string{}},
		{"punctuation only", model.ListParams{PolicyArea: str("!!")}, []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := listIDs(t, repo, tt.params); !slices.Equal(got, tt.want) {
				t.Errorf("ids = %v, want %v", got, tt.want)
			}
		})
	}

	res, err := repo.List(t.Context(), model.ListParams{PolicyArea: str("Health"), Limit: 1})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if res.Total != 2 || len(res.Items) != 1 {
		t.Errorf("total = %d, items = %d; want 2 and 1", res.Total, len(res.Items))
	}
}

// TestPolicyAreas checks GET /policy-areas's read (#708): every name in policy_areas, once, A to Z.
func TestPolicyAreas(t *testing.T) {
	c := &spannerdb.Client{Spanner: testdb.New(t)}
	repo := spannerdb.NewBillRepo(c)

	empty, err := repo.PolicyAreas(t.Context())
	if err != nil {
		t.Fatalf("PolicyAreas (empty): %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Errorf("empty database: got %#v, want an empty, non-nil list", empty)
	}

	seedPolicyAreaBills(t, c)
	got, err := repo.PolicyAreas(t.Context())
	if err != nil {
		t.Fatalf("PolicyAreas: %v", err)
	}
	want := []string{"Armed Forces and National Security", "Health", "Taxation"}
	if !slices.Equal(got, want) {
		t.Errorf("PolicyAreas = %v, want %v", got, want)
	}
}
