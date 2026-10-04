package spannerdb

import (
	"testing"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/testdb"
)

func slugCases() map[string]string {
	return map[string]string{
		"Taxation":                                   "taxation",
		"Armed Forces and National Security":         "armed-forces-and-national-security",
		"Crime and Law Enforcement":                  "crime-and-law-enforcement",
		"Science, Technology, Communications":        "science-technology-communications",
		"  Income tax credits  ":                     "income-tax-credits",
		"Families -- Child care (daycare) & custody": "families-child-care-daycare-custody",
		"Section 8 housing":                          "section-8-housing",
		"Économie":                                   "conomie",
		" -- ":                                       "",
	}
}

func TestSlug(t *testing.T) {
	for in, want := range slugCases() {
		if got := slug(in); got != want {
			t.Errorf("slug(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestPolicyAreaIDMatchesSlug checks that the bills.policy_area_id generated
// column computes the same key as slug, which UpsertBill uses for the
// policy_areas node, and that UpsertBill writes that node.
func TestPolicyAreaIDMatchesSlug(t *testing.T) {
	client := testdb.New(t)
	ctx := t.Context()
	testdb.SeedFixture(ctx, t, client)
	store := &PipelineStoreImpl{client: client}

	for name, want := range slugCases() {
		if want == "" {
			continue
		}
		if err := store.UpsertBill(ctx, repository.BillRow{
			ID: testdb.FixtureHouseBill, Congress: testdb.FixtureCongress, BillType: "hr", Number: 1,
			Title: "Companion Act", PolicyArea: &name,
		}); err != nil {
			t.Fatalf("upsert bill with policy area %q: %v", name, err)
		}
		var got, node spanner.NullString
		stmt := spanner.Statement{
			SQL: `SELECT b.policy_area_id, pa.name FROM bills b
				LEFT JOIN policy_areas pa ON pa.policy_area_id = b.policy_area_id
				WHERE b.bill_id = @id`,
			Params: map[string]any{"id": testdb.FixtureHouseBill},
		}
		if err := client.Single().Query(ctx, stmt).Do(func(r *spanner.Row) error {
			return r.Columns(&got, &node)
		}); err != nil {
			t.Fatalf("read policy_area_id: %v", err)
		}
		if got.StringVal != want {
			t.Errorf("policy_area_id for %q = %q, want %q", name, got.StringVal, want)
		}
		if !node.Valid {
			t.Errorf("no policy_areas row for %q", name)
		}
	}
}
