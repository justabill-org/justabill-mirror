package spannerdb_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

// sweepBill stores ih < rh < eh for bill and returns their IDs by code.
func sweepBill(
	t *testing.T,
	store *spannerdb.PipelineStoreImpl,
	client *spanner.Client,
	bill string,
) map[string]string {
	t.Helper()
	upsertVersions(t, store, bill, textVersion(bill, typeEngrossed, "eh", 3),
		textVersion(bill, typeReported, "rh", 2), textVersion(bill, typeIntroduced, "ih", 1))
	return versionIDs(t, client, bill)
}

func insertText(t *testing.T, store *spannerdb.PipelineStoreImpl, versionID, hash string) {
	t.Helper()
	if err := store.InsertBillText(t.Context(), repository.BillTextRow{
		TextVersionID: versionID, Format: "xml", Content: "text " + versionID, ContentHash: hash,
		FetchedAt: time.Now(),
	}); err != nil {
		t.Fatalf("insert text: %v", err)
	}
}

// insertDiff stores a diff from → to and a summary for it.
func insertDiff(t *testing.T, store *spannerdb.PipelineStoreImpl, client *spanner.Client, bill, from, to string) {
	t.Helper()
	if err := store.InsertBillTextDiff(t.Context(), repository.BillTextDiffRow{
		BillID: bill, FromVersionID: from, ToVersionID: to,
		DiffContent: json.RawMessage(`[]`), GeneratedAt: time.Now(),
	}); err != nil {
		t.Fatalf("insert diff: %v", err)
	}
	ids := queryStrings(t, client,
		"SELECT diff_id FROM bill_text_diffs WHERE from_version_id = @f AND to_version_id = @t",
		map[string]any{"f": from, "t": to})
	if len(ids) != 1 {
		t.Fatalf("diff ids = %q, want one", ids)
	}
	recordDiffSummary(t, store, ids[0])
}

// diffPairs returns the bill's diffs as "from|to" version codes, sorted.
func diffPairs(t *testing.T, client *spanner.Client, bill string) []string {
	t.Helper()
	pairs := queryStrings(t, client, `SELECT f.version_code, t.version_code FROM bill_text_diffs d
		JOIN bill_text_versions f ON f.bill_id = d.bill_id AND f.version_id = d.from_version_id
		JOIN bill_text_versions t ON t.bill_id = d.bill_id AND t.version_id = d.to_version_id
		WHERE d.bill_id = @bill`, map[string]any{"bill": bill})
	slices.Sort(pairs)
	return pairs
}

func deleteNonConsecutive(t *testing.T, store *spannerdb.PipelineStoreImpl) repository.DeletedDiffs {
	t.Helper()
	got, err := store.DeleteNonConsecutiveDiffs(t.Context())
	if err != nil {
		t.Fatalf("DeleteNonConsecutiveDiffs: %v", err)
	}
	return got
}

func missingPairs(t *testing.T, store *spannerdb.PipelineStoreImpl, limit int) []repository.DiffPair {
	t.Helper()
	got, err := store.QueryMissingDiffPairs(t.Context(), limit)
	if err != nil {
		t.Fatalf("QueryMissingDiffPairs: %v", err)
	}
	return got
}

func TestDeleteNonConsecutiveDiffs_DeletesBackwardsSkippingAndOrphanDiffs(t *testing.T) {
	store, client := newLinkStore(t)
	bill, other := testdb.FixtureHouseBill, testdb.FixtureSenateBill
	ids := sweepBill(t, store, client, bill)
	for code, id := range ids {
		insertText(t, store, id, "h-"+code)
	}
	insertDiff(t, store, client, bill, ids["ih"], ids["rh"]) // valid
	insertDiff(t, store, client, bill, ids["eh"], ids["rh"]) // backwards
	insertDiff(t, store, client, bill, ids["ih"], ids["eh"]) // skips the fetched rh
	insertDiff(t, store, client, bill, "gone", ids["eh"])    // from a version that no longer exists
	// Another bill's valid diff is untouched.
	otherIDs := sweepBill(t, store, client, other)
	insertText(t, store, otherIDs["ih"], "a")
	insertText(t, store, otherIDs["eh"], "b")
	insertDiff(t, store, client, other, otherIDs["ih"], otherIDs["eh"]) // rh isn't fetched: consecutive

	if got := deleteNonConsecutive(t, store); got != (repository.DeletedDiffs{Diffs: 3, Summaries: 3, Attempts: 3}) {
		t.Fatalf("deleted = %+v, want 3 diffs, summaries and attempts", got)
	}
	assertRows(t, diffPairs(t, client, bill), []string{"ih|rh"})
	assertRows(t, textCounts(t, client, bill), []string{"3|1|1"})
	assertRows(t, diffPairs(t, client, other), []string{"ih|eh"})
	assertRows(t, textCounts(t, client, other), []string{"2|1|1"})
	assertRows(t, diffAttemptCounts(t, client), []string{"2|0"})

	// A second run finds nothing.
	if got := deleteNonConsecutive(t, store); got != (repository.DeletedDiffs{}) {
		t.Errorf("second run deleted %+v, want nothing", got)
	}
}

func TestDeleteNonConsecutiveDiffs_DeletesDiffsToUnfetchedVersions(t *testing.T) {
	store, client := newLinkStore(t)
	bill := testdb.FixtureHouseBill
	ids := sweepBill(t, store, client, bill)
	insertText(t, store, ids["ih"], "a")
	insertDiff(t, store, client, bill, ids["ih"], ids["rh"]) // rh has no text

	if got := deleteNonConsecutive(t, store); got.Diffs != 1 {
		t.Fatalf("deleted = %+v, want the diff to the unfetched rh", got)
	}
	assertRows(t, diffPairs(t, client, bill), nil)
}

func TestQueryMissingDiffPairs(t *testing.T) {
	store, client := newLinkStore(t)
	bill, other := testdb.FixtureHouseBill, testdb.FixtureSenateBill
	ids := sweepBill(t, store, client, bill)
	insertText(t, store, ids["ih"], "a")
	insertText(t, store, ids["rh"], "b")
	insertText(t, store, ids["eh"], "c")
	insertDiff(t, store, client, bill, ids["ih"], ids["rh"])
	// The other bill has ih and eh fetched, rh not, and identical ih and eh texts.
	otherIDs := sweepBill(t, store, client, other)
	insertText(t, store, otherIDs["ih"], "same")
	insertText(t, store, otherIDs["eh"], "same")

	want := []repository.DiffPair{{BillID: bill, FromVersionID: ids["rh"], ToVersionID: ids["eh"]}}
	if got := missingPairs(t, store, 0); !slices.Equal(got, want) {
		t.Fatalf("missing = %+v, want only rh → eh", got)
	}

	// With different texts, the other bill's pair skips its unfetched rh.
	if _, err := client.Apply(t.Context(), []*spanner.Mutation{spanner.Update("bill_texts",
		[]string{"text_id", "content_hash"}, []any{textID(t, client, otherIDs["eh"]), "changed"})}); err != nil {
		t.Fatalf("update hash: %v", err)
	}
	want = append(want, repository.DiffPair{BillID: other, FromVersionID: otherIDs["ih"], ToVersionID: otherIDs["eh"]})
	slices.SortFunc(want, func(a, b repository.DiffPair) int { return strings.Compare(a.BillID, b.BillID) })
	if got := missingPairs(t, store, 0); !slices.Equal(got, want) {
		t.Fatalf("missing = %+v, want %+v", got, want)
	}
	if got := missingPairs(t, store, 1); !slices.Equal(got, want[:1]) {
		t.Errorf("limit 1 = %+v, want %+v", got, want[:1])
	}

	// Once the diffs exist, nothing is missing.
	for _, p := range want {
		insertDiff(t, store, client, p.BillID, p.FromVersionID, p.ToVersionID)
	}
	if got := missingPairs(t, store, 0); len(got) != 0 {
		t.Errorf("missing after filling = %+v, want none", got)
	}
	if got := deleteNonConsecutive(t, store); got != (repository.DeletedDiffs{}) {
		t.Errorf("deleted %+v from correct diffs, want nothing", got)
	}
}

func textID(t *testing.T, client *spanner.Client, versionID string) string {
	t.Helper()
	ids := queryStrings(
		t,
		client,
		"SELECT text_id FROM bill_texts WHERE version_id = @v",
		map[string]any{"v": versionID},
	)
	if len(ids) != 1 {
		t.Fatalf("text ids for %s = %q, want one", versionID, ids)
	}
	return ids[0]
}
