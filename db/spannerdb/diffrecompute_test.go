package spannerdb_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/testdb"
)

func TestQueryStoredDiffPairs(t *testing.T) {
	store, client := newLinkStore(t)
	bill := testdb.FixtureHouseBill
	ids := sweepBill(t, store, client, bill)
	insertDiff(t, store, client, bill, ids["rh"], ids["eh"])
	insertDiff(t, store, client, bill, ids["ih"], ids["rh"])

	got, err := store.QueryStoredDiffPairs(t.Context())
	if err != nil {
		t.Fatalf("QueryStoredDiffPairs: %v", err)
	}
	want := []repository.DiffPair{
		{BillID: bill, FromVersionID: ids["ih"], ToVersionID: ids["rh"]},
		{BillID: bill, FromVersionID: ids["rh"], ToVersionID: ids["eh"]},
	}
	if !slices.Equal(got, want) {
		t.Errorf("pairs = %+v, want %+v", got, want)
	}
}

func TestReplaceBillTextDiff(t *testing.T) {
	store, client := newLinkStore(t)
	bill := testdb.FixtureHouseBill
	ids := sweepBill(t, store, client, bill)
	insertDiff(t, store, client, bill, ids["ih"], ids["rh"]) // content [] with a summary
	row := repository.BillTextDiffRow{
		BillID: bill, FromVersionID: ids["ih"], ToVersionID: ids["rh"],
		DiffStats: json.RawMessage(`{"sections_added":1}`), GeneratedAt: time.Now(),
	}
	summaries := func() []string {
		return queryStrings(t, client, "SELECT summary FROM bill_text_diff_summaries", nil)
	}

	// The same content writes nothing, so the summary stays.
	row.DiffContent = json.RawMessage(` [ ] `)
	got, err := store.ReplaceBillTextDiff(t.Context(), row)
	if err != nil || got != (repository.DiffReplacement{Found: true}) {
		t.Fatalf("same content: %+v, %v", got, err)
	}
	assertRows(t, summaries(), []string{"summary"})

	// New content replaces the diff and drops its summary and summary attempt.
	row.DiffContent = json.RawMessage(`[{"section_id":"s1","header":"Sec. 1.","type":"added","new_text":"x"}]`)
	got, err = store.ReplaceBillTextDiff(t.Context(), row)
	want := repository.DiffReplacement{Found: true, Changed: true, SummaryDeleted: true, AttemptDeleted: true}
	if err != nil || got != want {
		t.Fatalf("new content: %+v, %v", got, err)
	}
	assertRows(t, summaries(), nil)
	assertRows(t, diffAttemptCounts(t, client), []string{"0|0"})
	assertRows(t, queryStrings(t, client,
		"SELECT TO_JSON_STRING(diff_content) || '|' || TO_JSON_STRING(diff_stats) FROM bill_text_diffs", nil),
		[]string{`[{"header":"Sec. 1.","new_text":"x","section_id":"s1","type":"added"}]|{"sections_added":1}`})

	// A pair without a diff is left alone.
	row.FromVersionID, row.ToVersionID = ids["rh"], ids["eh"]
	got, err = store.ReplaceBillTextDiff(t.Context(), row)
	if err != nil || got != (repository.DiffReplacement{}) {
		t.Fatalf("missing pair: %+v, %v", got, err)
	}
	assertRows(t, diffPairs(t, client, bill), []string{"ih|rh"})
}

// TestReplaceBillTextDiffFlagsEmpty checks that recompute rewrites is_empty with the content (#503):
// a diff that's now empty keeps its row, flagged, and an empty one that changed is shown again.
func TestReplaceBillTextDiffFlagsEmpty(t *testing.T) {
	store, client := newLinkStore(t)
	bill := testdb.FixtureHouseBill
	ids := sweepBill(t, store, client, bill)
	insertDiff(t, store, client, bill, ids["ih"], ids["rh"]) // content [] with a summary, not flagged
	row := repository.BillTextDiffRow{
		BillID: bill, FromVersionID: ids["ih"], ToVersionID: ids["rh"],
		DiffStats: json.RawMessage(`{}`), DiffContent: json.RawMessage(`[]`), GeneratedAt: time.Now(),
		IsEmpty: true,
	}
	flags := func() []string {
		return queryStrings(t, client,
			"SELECT CAST(is_empty AS STRING) || '|' || TO_JSON_STRING(diff_content) FROM bill_text_diffs", nil)
	}

	// The same content, now flagged empty: the flag is written and the summary and attempt dropped.
	got, err := store.ReplaceBillTextDiff(t.Context(), row)
	want := repository.DiffReplacement{Found: true, Changed: true, SummaryDeleted: true, AttemptDeleted: true}
	if err != nil || got != want {
		t.Fatalf("now empty: %+v, %v", got, err)
	}
	assertRows(t, flags(), []string{"true|[]"})

	// Empty again: nothing to write.
	got, err = store.ReplaceBillTextDiff(t.Context(), row)
	if err != nil || got != (repository.DiffReplacement{Found: true}) {
		t.Fatalf("still empty: %+v, %v", got, err)
	}

	// A change clears the flag, so readers show the diff again.
	row.IsEmpty = false
	row.DiffContent = json.RawMessage(`[{"section_id":"s1","header":"Sec. 1.","type":"added","new_text":"x"}]`)
	got, err = store.ReplaceBillTextDiff(t.Context(), row)
	if err != nil || got != (repository.DiffReplacement{Found: true, Changed: true}) {
		t.Fatalf("changed: %+v, %v", got, err)
	}
	assertRows(t, flags(), []string{`false|[{"header":"Sec. 1.","new_text":"x","section_id":"s1","type":"added"}]`})
}

// TestReplaceBillTextDiffRequeuesBlockedDiff is #529's regression test: a blocked attempt on a
// diff's old content mustn't keep its new content from being summarized.
func TestReplaceBillTextDiffRequeuesBlockedDiff(t *testing.T) {
	store, client := newSummaryStore(t)
	diffID := seedDiffBill(t, store, client, testdb.FixtureCongress, 1)
	blocked := diffAttempt(diffID, repository.SummaryOutcomeBlocked, summaryNow().Add(-time.Hour))
	if err := store.RecordDiffSummaryAttempt(t.Context(), blocked); err != nil {
		t.Fatalf("record diff attempt: %v", err)
	}
	assertRows(t, diffQueue(t, store, nil), []string{})
	pair := queryStrings(t, client,
		"SELECT bill_id, from_version_id, to_version_id FROM bill_text_diffs WHERE diff_id = @id",
		map[string]any{"id": diffID})
	if len(pair) != 1 {
		t.Fatalf("diff rows = %q, want one", pair)
	}
	ids := strings.Split(pair[0], "|")
	row := repository.BillTextDiffRow{
		BillID: ids[0], FromVersionID: ids[1], ToVersionID: ids[2], GeneratedAt: summaryNow(),
		DiffContent: json.RawMessage(`{"added": ["SEC. 2."]}`),
	}

	// The same content keeps the block: nothing is re-queued.
	got, err := store.ReplaceBillTextDiff(t.Context(), row)
	if err != nil || got != (repository.DiffReplacement{Found: true}) {
		t.Fatalf("same content: %+v, %v", got, err)
	}
	assertRows(t, diffAttemptCounts(t, client), []string{"1|0"})
	assertRows(t, diffQueue(t, store, nil), []string{})

	// New content drops the attempt, so the diff is due again under the same prompt and model.
	row.DiffContent = json.RawMessage(`{"added": ["SEC. 2.", "SEC. 3."]}`)
	got, err = store.ReplaceBillTextDiff(t.Context(), row)
	if err != nil || got != (repository.DiffReplacement{Found: true, Changed: true, AttemptDeleted: true}) {
		t.Fatalf("new content: %+v, %v", got, err)
	}
	assertRows(t, diffAttemptCounts(t, client), []string{"0|0"})
	assertRows(t, diffQueue(t, store, nil), []string{billID(1) + "|" + diffID})
}
