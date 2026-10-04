package spannerdb_test

import (
	"slices"
	"testing"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

// TestListSearch_PlainWords runs searches people type, including rquery syntax they don't mean
// (#452): each returns results or an empty page, never an error.
func TestListSearch_PlainWords(t *testing.T) {
	client := testdb.New(t)
	testdb.SeedFixture(t.Context(), t, client)
	repo := spannerdb.NewBillRepo(&spannerdb.Client{Spanner: client})

	tests := []struct {
		search  string
		matches bool
	}{
		{"H.R. 1", false},
		{"tax -", false},
		{`"health care`, false},
		{"(", false},
		{"OR", false},
		{"S. 5 (2025)", false},
		{"-", false},
		{`"`, false},
		{"companion", true},
		{"Companion Act", true},
		{`"Companion act`, true},
		{"companion OR nothing", false},
	}
	for _, tt := range tests {
		t.Run(tt.search, func(t *testing.T) {
			search := tt.search
			res, err := repo.List(t.Context(), model.ListParams{Limit: 20, Search: &search})
			if err != nil {
				t.Fatalf("List(%q): %v", tt.search, err)
			}
			if got := res.Total > 0; got != tt.matches {
				t.Errorf("List(%q) total = %d, want matches=%v", tt.search, res.Total, tt.matches)
			}
		})
	}
}

// TestListSearch_TitleOrSummary checks the two indexed searches joined by UNION (#619) find a bill
// by its title, by its summary, or by both once, with the other filters, the count and paging
// applying to the union.
func TestListSearch_TitleOrSummary(t *testing.T) {
	ctx := t.Context()
	client := testdb.New(t)
	testdb.SeedCongress(ctx, t, client, 119)
	testdb.SeedCongress(ctx, t, client, 118)
	testdb.SeedBill(ctx, t, client, "hr-119-1", 119, "hr", 1, "Watershed Restoration Act")
	testdb.SeedBill(ctx, t, client, "hr-119-2", 119, "hr", 2, "Farm Credit Act")
	testdb.SeedBill(ctx, t, client, "hr-119-3", 119, "hr", 3, "Watershed Grants Act")
	testdb.SeedBill(ctx, t, client, "hr-119-4", 119, "hr", 4, "Postal Naming Act")
	testdb.SeedBill(ctx, t, client, "hr-118-5", 118, "hr", 5, "Watershed Study Act")
	cols := []string{"bill_id", "short_summary", "long_summary"}
	if _, err := client.Apply(ctx, []*spanner.Mutation{
		spanner.Insert("bill_summaries", cols, []any{"hr-119-2", "Loans for watershed projects on farms.", nil}),
		spanner.Insert("bill_summaries", cols, []any{"hr-119-3", nil, "Grants for watershed districts."}),
		spanner.Insert("bill_summaries", cols, []any{"hr-119-4", "Names a post office.", nil}),
	}); err != nil {
		t.Fatalf("seed summaries: %v", err)
	}
	repo := spannerdb.NewBillRepo(&spannerdb.Client{Spanner: client})
	congress := 119

	tests := []struct {
		name   string
		search string
		params model.ListParams
		total  int
		want   []string
	}{
		{"title and summary, each once", "watershed", model.ListParams{Limit: 20}, 4,
			[]string{"hr-118-5", "hr-119-1", "hr-119-2", "hr-119-3"}},
		{"summary only", "loans", model.ListParams{Limit: 20}, 1, []string{"hr-119-2"}},
		{"title only", "postal", model.ListParams{Limit: 20}, 1, []string{"hr-119-4"}},
		{"with a filter", "watershed", model.ListParams{Limit: 20, Congress: &congress}, 3,
			[]string{"hr-119-1", "hr-119-2", "hr-119-3"}},
		{"paged", "watershed", model.ListParams{Limit: 2, Offset: 2, Sort: new("number")}, 4,
			[]string{"hr-119-1", "hr-118-5"}},
		{"no match", "spectrum", model.ListParams{Limit: 20}, 0, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := tt.params
			params.Search = &tt.search
			res, err := repo.List(ctx, params)
			if err != nil {
				t.Fatalf("List(%q): %v", tt.search, err)
			}
			var got []string
			for _, b := range res.Items {
				got = append(got, b.ID)
			}
			if tt.params.Sort == nil {
				slices.Sort(got)
			}
			if res.Total != tt.total || !slices.Equal(got, tt.want) {
				t.Errorf("List(%q) = %v (total %d), want %v (total %d)", tt.search, got, res.Total, tt.want, tt.total)
			}
		})
	}
}
