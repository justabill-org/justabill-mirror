package spannerdb_test

import (
	"slices"
	"testing"
	"time"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

const (
	scopedVersionID = "00000000-0000-0000-0000-00000000000a"
	scopedToID      = "00000000-0000-0000-0000-00000000000b"
	scopedTextID    = "00000000-0000-0000-0000-00000000000c"
	scopedDiffID    = "00000000-0000-0000-0000-00000000000d"
)

// newScopedTextRepo seeds the fixture plus two text versions, a text and a diff, all on
// FixtureHouseBill. The fixture's own text rows are on FixtureLawBill.
func newScopedTextRepo(t *testing.T) *spannerdb.BillRepository {
	t.Helper()
	return spannerdb.NewBillRepo(&spannerdb.Client{Spanner: seedScopedTexts(t)})
}

// seedScopedTexts makes a database with the fixture and newScopedTextRepo's text rows: the "ih"
// version (sort order 1, with a text) and the "eh" version (sort order 2, no text yet).
func seedScopedTexts(t *testing.T) *spanner.Client {
	t.Helper()
	client := testdb.New(t)
	testdb.SeedFixture(t.Context(), t, client)

	versionCols := []string{"bill_id", "version_id", "version_type", "version_code", "sort_order"}
	_, err := client.Apply(t.Context(), []*spanner.Mutation{
		spanner.Insert("bill_text_versions", versionCols,
			[]any{testdb.FixtureHouseBill, scopedVersionID, "Introduced in House", "ih", int64(1)}),
		spanner.Insert("bill_text_versions", versionCols,
			[]any{testdb.FixtureHouseBill, scopedToID, "Engrossed in House", "eh", int64(2)}),
		spanner.Insert("bill_texts",
			[]string{"text_id", "version_id", "format", "content", "content_hash"},
			[]any{scopedTextID, scopedVersionID, "xml", "the text", "hash"}),
		spanner.Insert("bill_text_diffs",
			[]string{"bill_id", "diff_id", "from_version_id", "to_version_id", "diff_content"},
			[]any{testdb.FixtureHouseBill, scopedDiffID, scopedVersionID, scopedToID,
				spanner.NullJSON{Value: map[string]any{"changes": []any{}}, Valid: true}}),
	})
	if err != nil {
		t.Fatalf("seed text rows: %v", err)
	}
	return client
}

func TestGetTextContent_ScopedToBill(t *testing.T) {
	repo := newScopedTextRepo(t)

	text, err := repo.GetTextContent(t.Context(), testdb.FixtureHouseBill, scopedVersionID)
	if err != nil {
		t.Fatalf("own bill: %v", err)
	}
	if text == nil || text.ID != scopedTextID || text.TextVersionID != scopedVersionID || text.Content != "the text" {
		t.Fatalf("own bill: text = %+v", text)
	}

	for _, tc := range []struct{ name, bill, version string }{
		{"other bill", testdb.FixtureSenateBill, scopedVersionID},
		{"version without text", testdb.FixtureHouseBill, scopedToID},
		{"unknown version", testdb.FixtureHouseBill, "missing"},
	} {
		got, gotErr := repo.GetTextContent(t.Context(), tc.bill, tc.version)
		if gotErr != nil || got != nil {
			t.Errorf("%s: got %+v, %v; want nil, nil", tc.name, got, gotErr)
		}
	}
}

func TestGetDiffByID_ScopedToBill(t *testing.T) {
	repo := newScopedTextRepo(t)

	diff, err := repo.GetDiffByID(t.Context(), testdb.FixtureHouseBill, scopedDiffID)
	if err != nil {
		t.Fatalf("own bill: %v", err)
	}
	if diff == nil || diff.ID != scopedDiffID || diff.BillID != testdb.FixtureHouseBill ||
		diff.FromVersionID != scopedVersionID || diff.ToVersionID != scopedToID {
		t.Fatalf("own bill: diff = %+v", diff)
	}

	for _, tc := range []struct{ name, bill, diff string }{
		{"other bill", testdb.FixtureSenateBill, scopedDiffID},
		{"unknown diff", testdb.FixtureHouseBill, "missing"},
	} {
		got, gotErr := repo.GetDiffByID(t.Context(), tc.bill, tc.diff)
		if gotErr != nil || got != nil {
			t.Errorf("%s: got %+v, %v; want nil, nil", tc.name, got, gotErr)
		}
	}
}

func TestGetDiffs_MetadataOnly(t *testing.T) {
	repo := newScopedTextRepo(t)

	diffs, err := repo.GetDiffs(t.Context(), testdb.FixtureHouseBill)
	if err != nil {
		t.Fatalf("GetDiffs: %v", err)
	}
	if len(diffs) != 1 || diffs[0].ID != scopedDiffID ||
		diffs[0].FromVersionID != scopedVersionID || diffs[0].ToVersionID != scopedToID {
		t.Fatalf("diffs = %+v", diffs)
	}
	if diffs[0].DiffContent != nil {
		t.Errorf("DiffContent = %s, want nil: the list carries metadata only", diffs[0].DiffContent)
	}

	full, err := repo.GetDiffByID(t.Context(), testdb.FixtureHouseBill, scopedDiffID)
	if err != nil || full == nil || len(full.DiffContent) == 0 {
		t.Fatalf("GetDiffByID = %+v, %v; want the diff with its content", full, err)
	}
}

func TestBillGetSummaries(t *testing.T) {
	client := testdb.New(t)
	ctx := t.Context()
	testdb.SeedFixture(ctx, t, client)
	c := &spannerdb.Client{Spanner: client}
	repo := spannerdb.NewBillRepo(c)
	// The House summary's source version is on file; the Senate one's "is" isn't.
	upsertVersions(t, spannerdb.NewPipelineStore(c), testdb.FixtureHouseBill,
		textVersion(testdb.FixtureHouseBill, typeReported, "rh", 1))

	cols := []string{
		"bill_id", "short_summary", "long_summary", "why_it_matters", "model_used", "source_version_code",
		"source_crs_hash", "source_rule_hash",
	}
	if _, err := client.Apply(ctx, []*spanner.Mutation{
		spanner.Insert("bill_summaries", cols, []any{
			testdb.FixtureHouseBill, "House short", "House long", "House why", "gemini", "rh", "crs-hash", "rule-hash",
		}),
		spanner.Insert("bill_summaries", cols,
			[]any{testdb.FixtureSenateBill, "Senate short", nil, nil, "gemini", "is", nil, nil}),
		spanner.Insert("bill_summaries", []string{"bill_id", "short_summary"},
			[]any{testdb.FixturePrevHouseBill, "Older row, no provenance"}),
	}); err != nil {
		t.Fatalf("seed summaries: %v", err)
	}

	got, err := repo.GetSummaries(ctx,
		[]string{testdb.FixtureHouseBill, testdb.FixtureSenateBill, testdb.FixturePrevHouseBill, "hr-119-99999"})
	if err != nil {
		t.Fatalf("summaries: %v", err)
	}
	// Bills without a summary (or unknown IDs) are left out.
	if len(got) != 3 {
		t.Fatalf("got %d summaries, want 3: %+v", len(got), got)
	}
	house := got[testdb.FixtureHouseBill]
	if house.BillID != testdb.FixtureHouseBill || deref(house.ShortSummary) != "House short" ||
		deref(house.LongSummary) != "House long" || deref(house.WhoItAffects) != "House why" ||
		house.GeneratedAt == nil ||
		deref(house.SourceVersionCode) != "rh" || deref(house.SourceVersionName) != typeReported ||
		!house.WithCRSSummary || !house.WithRuleContext {
		t.Errorf("house summary = %+v", house)
	}
	senate := got[testdb.FixtureSenateBill]
	if deref(senate.ShortSummary) != "Senate short" || senate.LongSummary != nil || senate.WhoItAffects != nil ||
		deref(senate.SourceVersionCode) != "is" || senate.SourceVersionName != nil || senate.WithCRSSummary ||
		senate.WithRuleContext {
		t.Errorf("senate summary = %+v", senate)
	}
	if prev := got[testdb.FixturePrevHouseBill]; prev.SourceVersionCode != nil || prev.SourceVersionName != nil {
		t.Errorf("summary without provenance = %+v", prev)
	}

	// The single read shares the scan and agrees with the batch.
	one, err := repo.GetSummary(ctx, testdb.FixtureHouseBill)
	if err != nil || one == nil || deref(one.ShortSummary) != "House short" ||
		deref(one.SourceVersionCode) != "rh" || deref(one.SourceVersionName) != typeReported {
		t.Errorf("GetSummary = %+v, %v", one, err)
	}

	empty, err := repo.GetSummaries(ctx, nil)
	if err != nil || empty == nil || len(empty) != 0 {
		t.Errorf("no IDs: got %v, %v; want an empty, non-nil map", empty, err)
	}
}

func deref(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func TestIndex_OneCongress(t *testing.T) {
	client := testdb.New(t)
	testdb.SeedFixture(t.Context(), t, client)
	repo := spannerdb.NewBillRepo(&spannerdb.Client{Spanner: client})

	entries, err := repo.Index(t.Context(), testdb.FixtureCongress)
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	var ids []string
	for _, e := range entries {
		ids = append(ids, e.ID)
	}
	// Ordered by ID, and hr-118-1 (the previous congress) is left out.
	want := []string{
		testdb.FixtureCRAUnmatched, testdb.FixtureHouseBill, testdb.FixtureLawBill, testdb.FixtureSenateBill,
		testdb.FixtureCRABill,
	}
	if !slices.Equal(ids, want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}

	empty, err := repo.Index(t.Context(), 1)
	if err != nil {
		t.Fatalf("Index(1): %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Fatalf("Index(1) = %#v, want an empty, non-nil slice", empty)
	}
}

func TestIndex_UpdatedAt(t *testing.T) {
	client := testdb.New(t)
	testdb.SeedCongress(t.Context(), t, client, testdb.FixtureCongress)
	updated := time.Date(2026, time.September, 1, 12, 0, 0, 0, time.UTC)
	_, err := client.Apply(t.Context(), []*spanner.Mutation{
		spanner.Insert("bills", []string{"bill_id", "congress", "bill_type", "number", "title", "updated_at"},
			[]any{"hr-119-7", int64(119), "hr", int64(7), "Updated Act", updated}),
		spanner.Insert("bills", []string{"bill_id", "congress", "bill_type", "number", "title"},
			[]any{"hr-119-8", int64(119), "hr", int64(8), "Never Updated Act"}),
	})
	if err != nil {
		t.Fatalf("seed bills: %v", err)
	}
	repo := spannerdb.NewBillRepo(&spannerdb.Client{Spanner: client})

	entries, err := repo.Index(t.Context(), testdb.FixtureCongress)
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("entries = %+v, want 2", entries)
	}
	if got := entries[0].UpdatedAt; got == nil || !got.Equal(updated) {
		t.Errorf("hr-119-7 updated_at = %v, want %v", got, updated)
	}
	if got := entries[1].UpdatedAt; got != nil {
		t.Errorf("hr-119-8 updated_at = %v, want nil", got)
	}
}

func TestStatusIndex(t *testing.T) {
	client := testdb.New(t)
	testdb.SeedCongress(t.Context(), t, client, 118)
	testdb.SeedCongress(t.Context(), t, client, testdb.FixtureCongress)
	cols := []string{"bill_id", "congress", "bill_type", "number", "title", "current_status"}
	bill := func(id string, congress int64, number int64, status any) *spanner.Mutation {
		return spanner.Insert("bills", cols, []any{id, congress, "hr", number, "An Act", status})
	}
	// One 119th bill per status, inserted out of ID order, a bill with no status, and a 118th
	// bill past committee.
	_, err := client.Apply(t.Context(), []*spanner.Mutation{
		bill("hr-119-10", 119, 10, "became_law"),
		bill("hr-119-1", 119, 1, "introduced"),
		bill("hr-119-2", 119, 2, "in_committee"),
		bill("hr-119-3", 119, 3, "reported"),
		bill("hr-119-4", 119, 4, "passed_house"),
		bill("hr-119-5", 119, 5, "passed_senate"),
		bill("hr-119-6", 119, 6, "resolving_differences"),
		bill("hr-119-7", 119, 7, "to_president"),
		bill("hr-119-8", 119, 8, "signed"),
		bill("hr-119-9", 119, 9, "vetoed"),
		bill("hr-119-11", 119, 11, nil),
		bill("hr-118-4", 118, 4, "passed_house"),
	})
	if err != nil {
		t.Fatalf("seed bills: %v", err)
	}
	repo := spannerdb.NewBillRepo(&spannerdb.Client{Spanner: client})

	got, err := repo.StatusIndex(t.Context(), testdb.FixtureCongress)
	if err != nil {
		t.Fatalf("StatusIndex: %v", err)
	}
	want := []model.BillStatusIndexEntry{
		{ID: "hr-119-10", Status: "became_law"},
		{ID: "hr-119-4", Status: "passed_house"},
		{ID: "hr-119-5", Status: "passed_senate"},
		{ID: "hr-119-6", Status: "resolving_differences"},
		{ID: "hr-119-7", Status: "to_president"},
		{ID: "hr-119-8", Status: "signed"},
		{ID: "hr-119-9", Status: "vetoed"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("StatusIndex(119) =\n%v\nwant\n%v", got, want)
	}

	old, err := repo.StatusIndex(t.Context(), 118)
	if err != nil {
		t.Fatalf("StatusIndex(118): %v", err)
	}
	wantOld := []model.BillStatusIndexEntry{{ID: "hr-118-4", Status: "passed_house"}}
	if !slices.Equal(old, wantOld) {
		t.Errorf("StatusIndex(118) = %v, want %v", old, wantOld)
	}

	empty, err := repo.StatusIndex(t.Context(), 1)
	if err != nil {
		t.Fatalf("StatusIndex(1): %v", err)
	}
	if empty == nil || len(empty) != 0 {
		t.Errorf("StatusIndex(1) = %#v, want an empty, non-nil slice", empty)
	}
}
