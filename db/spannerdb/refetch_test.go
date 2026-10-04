package spannerdb_test

import (
	"encoding/json"
	"testing"
	"time"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

// corrected is textVersion with a corrected print's URL.
func corrected(bill, versionType, code string, sortOrder int) repository.TextVersionRow {
	r := textVersion(bill, versionType, code, sortOrder)
	r.Formats = json.RawMessage(`[{"type":"Formatted XML","url":"https://example.test/` + code + `-corrected.xml"}]`)
	return r
}

// unfetched returns the queue as "code|refetch" rows for the fixture House bill, in queue order.
func unfetched(t *testing.T, store *spannerdb.PipelineStoreImpl) []string {
	t.Helper()
	refs, err := store.QueryUnfetchedTextVersions(t.Context(), 0)
	if err != nil {
		t.Fatalf("QueryUnfetchedTextVersions: %v", err)
	}
	var out []string
	for _, r := range refs {
		if r.BillID != testdb.FixtureHouseBill {
			continue
		}
		refetch := "new"
		if r.Refetch {
			refetch = "refetch"
		}
		out = append(out, r.VersionCode+"|"+refetch)
	}
	return out
}

func refetchText(
	t *testing.T, store *spannerdb.PipelineStoreImpl, versionID, hash string,
) repository.TextRefetch {
	t.Helper()
	got, err := store.RefetchBillText(t.Context(), repository.BillTextRow{
		TextVersionID: versionID, Format: "xml", Content: "text " + hash, ContentHash: hash,
		Sections: json.RawMessage(`[]`), FetchedAt: time.Now(),
	})
	if err != nil {
		t.Fatalf("RefetchBillText: %v", err)
	}
	return got
}

// queuedForRefetch stores ih < rh < eh with texts, diffs and summaries, then re-syncs them with
// rh's formats corrected, so rh's text is queued for a refetch.
func queuedForRefetch(
	t *testing.T, store *spannerdb.PipelineStoreImpl, client *spanner.Client, bill string,
) map[string]string {
	t.Helper()
	ids := sweepBill(t, store, client, bill)
	addTextAndDiff(t, store, client, bill, ids["ih"], ids["rh"], ids["eh"])
	res := upsertVersions(t, store, bill, textVersion(bill, typeEngrossed, "eh", 3),
		corrected(bill, typeReported, "rh", 2), textVersion(bill, typeIntroduced, "ih", 1))
	if len(res.RefetchCodes) != 1 || res.RefetchCodes[0] != "rh" || res.Updated != 3 {
		t.Fatalf("re-sync = %+v, want 3 updated and rh queued", res)
	}
	return ids
}

func TestUpsertBillTextVersions_QueuesRefetchOnChangedFormats(t *testing.T) {
	store, client := newLinkStore(t)
	bill := testdb.FixtureHouseBill
	queuedForRefetch(t, store, client, bill)

	assertRows(t, unfetched(t, store), []string{"rh|refetch"})
	// The old text, diffs and summaries stay readable until the refetch.
	assertRows(t, textCounts(t, client, bill), []string{"3|2|2"})

	// Queuing it again, or an unchanged list, changes nothing more.
	res := upsertVersions(t, store, bill, textVersion(bill, typeEngrossed, "eh", 3),
		corrected(bill, typeReported, "rh", 2), textVersion(bill, typeIntroduced, "ih", 1))
	if len(res.RefetchCodes) != 0 {
		t.Errorf("unchanged re-sync = %+v, want nothing queued", res)
	}
	assertRows(t, unfetched(t, store), []string{"rh|refetch"})
}

func TestUpsertBillTextVersions_UnchangedFormatsQueueNothing(t *testing.T) {
	store, client := newLinkStore(t)
	bill := testdb.FixtureHouseBill
	ids := sweepBill(t, store, client, bill)
	addTextAndDiff(t, store, client, bill, ids["ih"], ids["rh"])

	// eh was never fetched, so its new URL only changes what the first fetch reads.
	res := upsertVersions(t, store, bill, corrected(bill, typeEngrossed, "eh", 3),
		textVersion(bill, typeReported, "rh", 2), textVersion(bill, typeIntroduced, "ih", 1))
	if len(res.RefetchCodes) != 0 {
		t.Errorf("re-sync = %+v, want nothing queued", res)
	}
	assertRows(t, unfetched(t, store), []string{"eh|new"})
}

func TestRefetchBillText_SameHashKeepsDiffs(t *testing.T) {
	store, client := newLinkStore(t)
	bill := testdb.FixtureHouseBill
	ids := queuedForRefetch(t, store, client, bill)

	got := refetchText(t, store, ids["rh"], "h-"+ids["rh"]) // addTextAndDiff's hash
	if got.Changed || got.Deleted != (repository.DeletedDiffs{}) {
		t.Errorf("refetch = %+v, want unchanged", got)
	}
	assertRows(t, unfetched(t, store), nil)
	assertRows(t, textCounts(t, client, bill), []string{"3|2|2"})
	assertRows(t, queryStrings(t, client, "SELECT content FROM bill_texts WHERE version_id = @id",
		map[string]any{"id": ids["rh"]}), []string{"text " + ids["rh"]})
}

func TestRefetchBillText_NewHashReplacesTextAndDropsItsDiffs(t *testing.T) {
	store, client := newLinkStore(t)
	bill, other := testdb.FixtureHouseBill, testdb.FixtureSenateBill
	ids := queuedForRefetch(t, store, client, bill)
	upsertVersions(t, store, other, textVersion(other, "Reported in Senate", "rs", 2),
		textVersion(other, "Introduced in Senate", "is", 1))
	otherIDs := versionIDs(t, client, other)
	addTextAndDiff(t, store, client, other, otherIDs["is"], otherIDs["rs"])

	got := refetchText(t, store, ids["rh"], "h-corrected")
	if !got.Changed || got.Deleted != (repository.DeletedDiffs{Diffs: 2, Summaries: 2, Attempts: 2}) {
		t.Errorf("refetch = %+v, want changed with 2 diffs, summaries and attempts deleted", got)
	}
	assertRows(t, unfetched(t, store), nil)
	assertRows(t, textCounts(t, client, bill), []string{"3|0|0"})
	assertRows(t, diffAttemptCounts(t, client), []string{"1|0"}) // other's diff; none orphaned
	assertRows(t, queryStrings(t, client,
		"SELECT content, content_hash FROM bill_texts WHERE version_id = @id AND fetched_at IS NOT NULL",
		map[string]any{"id": ids["rh"]}), []string{"text h-corrected|h-corrected"})
	assertRows(t, textCounts(t, client, other), []string{"2|1|1"})

	// The diff sweep recomputes both ends.
	pairs := missingPairs(t, store, 0)
	want := []repository.DiffPair{
		{BillID: bill, FromVersionID: ids["ih"], ToVersionID: ids["rh"]},
		{BillID: bill, FromVersionID: ids["rh"], ToVersionID: ids["eh"]},
	}
	if len(pairs) != len(want) || pairs[0] != want[0] || pairs[1] != want[1] {
		t.Errorf("missing pairs = %+v, want %+v", pairs, want)
	}
}

func TestRefetchBillText_NotQueuedDoesNothing(t *testing.T) {
	store, client := newLinkStore(t)
	bill := testdb.FixtureHouseBill
	ids := sweepBill(t, store, client, bill)
	addTextAndDiff(t, store, client, bill, ids["ih"], ids["rh"])

	// A fetched text that isn't queued, and a version with no text at all.
	for _, id := range []string{ids["rh"], ids["eh"]} {
		if got := refetchText(t, store, id, "h-other"); got.Changed {
			t.Errorf("refetch %s = %+v, want nothing", id, got)
		}
	}
	assertRows(t, textCounts(t, client, bill), []string{"2|1|1"})
	assertRows(t, unfetched(t, store), []string{"eh|new"})
}
