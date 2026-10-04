package spannerdb_test

import (
	"context"
	"encoding/json"
	"slices"
	"testing"
	"time"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

// insertEmptyDiff stores an empty diff from → to, the way the diff sweep remembers a pair with no
// section changes (docs/design/401-remember-empty-diffs.md), and returns its ID.
func insertEmptyDiff(
	t *testing.T,
	store *spannerdb.PipelineStoreImpl,
	client *spanner.Client,
	bill, from, to string,
) string {
	t.Helper()
	if err := store.InsertBillTextDiff(t.Context(), repository.BillTextDiffRow{
		BillID: bill, FromVersionID: from, ToVersionID: to,
		DiffStats:   json.RawMessage(`{"sections_added":0,"sections_removed":0,"sections_modified":0}`),
		DiffContent: json.RawMessage(`[]`), GeneratedAt: time.Now(), IsEmpty: true,
	}); err != nil {
		t.Fatalf("insert empty diff: %v", err)
	}
	return diffID(t, client, from, to)
}

func diffID(t *testing.T, client *spanner.Client, from, to string) string {
	t.Helper()
	ids := queryStrings(t, client,
		"SELECT diff_id FROM bill_text_diffs WHERE from_version_id = @f AND to_version_id = @t",
		map[string]any{"f": from, "t": to})
	if len(ids) != 1 {
		t.Fatalf("diff ids = %q, want one", ids)
	}
	return ids[0]
}

// markEmpty flags every diff of the bill as empty and drops their summaries, as if the sweep had
// found no section changes in any of its pairs.
func markEmpty(t *testing.T, client *spanner.Client, bill string) {
	t.Helper()
	_, err := client.ReadWriteTransaction(
		t.Context(),
		func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
			params := map[string]any{"bill": bill}
			_, err := txn.BatchUpdate(ctx, []spanner.Statement{
				{SQL: `DELETE FROM bill_text_diff_summaries WHERE diff_id IN
				(SELECT diff_id FROM bill_text_diffs WHERE bill_id = @bill)`, Params: params},
				{SQL: "UPDATE bill_text_diffs SET is_empty = true WHERE bill_id = @bill", Params: params},
			})
			return err
		},
	)
	if err != nil {
		t.Fatalf("mark diffs empty: %v", err)
	}
}

func TestEmptyDiffs_LeftOutOfEveryReader(t *testing.T) {
	store, client := newLinkStore(t)
	repo := spannerdb.NewBillRepo(&spannerdb.Client{Spanner: client})
	bill := testdb.FixtureHouseBill
	ids := sweepBill(t, store, client, bill)
	for code, id := range ids {
		insertText(t, store, id, "h-"+code)
	}
	emptyID := insertEmptyDiff(t, store, client, bill, ids["ih"], ids["rh"])
	if err := store.InsertBillTextDiff(t.Context(), repository.BillTextDiffRow{
		BillID: bill, FromVersionID: ids["rh"], ToVersionID: ids["eh"],
		DiffContent: json.RawMessage(`[{"type":"modified"}]`), GeneratedAt: time.Now(),
	}); err != nil {
		t.Fatalf("insert diff: %v", err)
	}
	realID := diffID(t, client, ids["rh"], ids["eh"])
	assertRows(t, queryStrings(t, client, `SELECT CAST(is_empty AS STRING) FROM bill_text_diffs
		WHERE bill_id = @bill ORDER BY is_empty`, map[string]any{"bill": bill}), []string{"false", "true"})

	diffs, err := repo.GetDiffs(t.Context(), bill)
	if err != nil {
		t.Fatalf("GetDiffs: %v", err)
	}
	if len(diffs) != 1 || diffs[0].ID != realID {
		t.Errorf("GetDiffs = %+v, want only the non-empty diff %s", diffs, realID)
	}

	if got, gotErr := repo.GetDiffByID(t.Context(), bill, emptyID); gotErr != nil || got != nil {
		t.Errorf("GetDiffByID(empty) = %+v, %v; want nil, nil", got, gotErr)
	}
	if got, gotErr := repo.GetDiffByID(t.Context(), bill, realID); gotErr != nil || got == nil || got.ID != realID {
		t.Errorf("GetDiffByID(real) = %+v, %v; want the diff", got, gotErr)
	}

	refs, err := store.QueryDiffsToSummarize(t.Context(), repository.DiffSummaryQueueQuery{
		Congress: testdb.FixtureCongress, Limit: 100, PromptVersion: "p", Model: "m", Now: time.Now(),
	})
	if err != nil {
		t.Fatalf("QueryDiffsToSummarize: %v", err)
	}
	queued := make([]string, 0, len(refs))
	for _, r := range refs {
		queued = append(queued, r.DiffID)
	}
	if slices.Contains(queued, emptyID) || !slices.Contains(queued, realID) {
		t.Errorf("unsummarized = %q, want %s and not the empty %s", queued, realID, emptyID)
	}

	// The sweep counts the empty row as a diff: nothing is missing, and it's consecutive.
	if got := missingPairs(t, store, 0); len(got) != 0 {
		t.Errorf("missing = %+v, want none", got)
	}
	if got := deleteNonConsecutive(t, store); got != (repository.DeletedDiffs{}) {
		t.Errorf("deleted %+v, want nothing", got)
	}
}

func TestEmptyDiffs_DeletedByNonConsecutiveSweep(t *testing.T) {
	store, client := newLinkStore(t)
	bill := testdb.FixtureHouseBill
	ids := sweepBill(t, store, client, bill)
	for code, id := range ids {
		insertText(t, store, id, "h-"+code)
	}
	insertEmptyDiff(t, store, client, bill, ids["ih"], ids["eh"]) // skips the fetched rh

	if got := deleteNonConsecutive(t, store); got.Diffs != 1 {
		t.Fatalf("deleted = %+v, want the empty diff", got)
	}
	assertRows(t, diffPairs(t, client, bill), nil)
}

func TestEmptyDiffs_DeletedByRefetchSoThePairIsDiffedAgain(t *testing.T) {
	store, client := newLinkStore(t)
	bill := testdb.FixtureSenateBill
	ids := queuedForRefetch(t, store, client, bill)
	markEmpty(t, client, bill)
	if got := missingPairs(t, store, 0); len(got) != 0 {
		t.Fatalf("missing = %+v, want none while the empty diffs stand", got)
	}

	got := refetchText(t, store, ids["rh"], "h-corrected")
	if !got.Changed || got.Deleted.Diffs != 2 {
		t.Fatalf("refetch = %+v, want changed with both empty diffs deleted", got)
	}
	want := []repository.DiffPair{
		{BillID: bill, FromVersionID: ids["ih"], ToVersionID: ids["rh"]},
		{BillID: bill, FromVersionID: ids["rh"], ToVersionID: ids["eh"]},
	}
	if pairs := missingPairs(t, store, 0); !slices.Equal(pairs, want) {
		t.Errorf("missing = %+v, want %+v", pairs, want)
	}
}

func TestEmptyDiffs_DeletedByPrune(t *testing.T) {
	store, client := newLinkStore(t)
	bill := testdb.FixtureHouseBill
	ids := sweepBill(t, store, client, bill)
	addTextAndDiff(t, store, client, bill, ids["ih"], ids["rh"], ids["eh"])
	markEmpty(t, client, bill)

	res := upsertVersions(t, store, bill, textVersion(bill, typeEngrossed, "eh", 2),
		textVersion(bill, typeIntroduced, "ih", 1))
	if res.Pruned != 1 {
		t.Fatalf("prune = %+v, want rh pruned", res)
	}
	assertRows(t, textCounts(t, client, bill), []string{"2|0|0"})
	want := []repository.DiffPair{{BillID: bill, FromVersionID: ids["ih"], ToVersionID: ids["eh"]}}
	if pairs := missingPairs(t, store, 0); !slices.Equal(pairs, want) {
		t.Errorf("missing = %+v, want %+v", pairs, want)
	}
}
