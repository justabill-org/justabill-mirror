package spannerdb_test

import (
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

const diffPrompt = "diff-v2"

// recordDiffSummary stores an ok attempt with a summary for the diff, as sync-summaries does.
func recordDiffSummary(t *testing.T, store *spannerdb.PipelineStoreImpl, diffID string) {
	t.Helper()
	a := diffAttempt(diffID, repository.SummaryOutcomeOK, time.Now())
	a.Summary = &repository.DiffSummaryRow{DiffID: diffID, Summary: "summary", ModelUsed: summaryModel}
	if err := store.RecordDiffSummaryAttempt(t.Context(), a); err != nil {
		t.Fatalf("record diff summary: %v", err)
	}
}

// diffAttemptCounts returns the diff_summary_attempts rows and those whose diff is gone, as
// "rows|orphans".
func diffAttemptCounts(t *testing.T, client *spanner.Client) []string {
	t.Helper()
	return queryStrings(t, client, `SELECT CAST(COUNT(*) AS STRING),
		CAST(COUNTIF(NOT EXISTS(SELECT 1 FROM bill_text_diffs d WHERE d.diff_id = a.diff_id)) AS STRING)
		FROM diff_summary_attempts a`, nil)
}

func diffAttempt(diffID, outcome string, at time.Time) repository.DiffSummaryAttemptRow {
	return repository.DiffSummaryAttemptRow{
		DiffID: diffID, PromptVersion: diffPrompt, Model: summaryModel, Outcome: outcome, Reason: "r",
		AttemptedAt: at,
	}
}

// seedDiffBill stores an H.R. bill of congress with fetched ih and rh texts and the ih → rh diff,
// unsummarized, and returns the diff's ID.
func seedDiffBill(
	t *testing.T,
	store *spannerdb.PipelineStoreImpl,
	client *spanner.Client,
	congress, number int,
) string {
	t.Helper()
	ids := seedSummaryBill(t, store, client, congress, number, 10, []string{"rh", "ih"}, "rh", "ih")
	if err := store.InsertBillTextDiff(t.Context(), repository.BillTextDiffRow{
		BillID: "hr-" + strconv.Itoa(congress) + "-" + strconv.Itoa(number), FromVersionID: ids["ih"],
		ToVersionID: ids["rh"], DiffContent: json.RawMessage(`{"added":["SEC. 2."]}`), GeneratedAt: summaryNow(),
	}); err != nil {
		t.Fatalf("insert diff: %v", err)
	}
	diffIDs := queryStrings(t, client, "SELECT diff_id FROM bill_text_diffs WHERE to_version_id = @to",
		map[string]any{"to": ids["rh"]})
	if len(diffIDs) != 1 {
		t.Fatalf("diff ids = %q, want one", diffIDs)
	}
	return diffIDs[0]
}

// diffQueue returns the due diffs as "bill|diff" rows.
func diffQueue(t *testing.T, store *spannerdb.PipelineStoreImpl, mod func(*repository.DiffSummaryQueueQuery)) []string {
	t.Helper()
	q := repository.DiffSummaryQueueQuery{
		Congress: testdb.FixtureCongress, Limit: 100, PromptVersion: diffPrompt, Model: summaryModel,
		Now: summaryNow(),
	}
	if mod != nil {
		mod(&q)
	}
	refs, err := store.QueryDiffsToSummarize(t.Context(), q)
	if err != nil {
		t.Fatalf("query diffs to summarize: %v", err)
	}
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.BillID+"|"+r.DiffID)
	}
	return out
}

func TestQueryDiffsToSummarize(t *testing.T) {
	store, client := newSummaryStore(t)
	ctx := t.Context()
	diffs := make(map[int]string)
	for n := 1; n <= 6; n++ {
		diffs[n] = seedDiffBill(t, store, client, testdb.FixtureCongress, n)
	}
	seedDiffBill(t, store, client, testdb.FixturePrevCongress, 7) // another congress
	anHourAgo := summaryNow().Add(-90 * time.Minute)
	blockedOtherModel := diffAttempt(diffs[5], repository.SummaryOutcomeBlocked, anHourAgo)
	blockedOtherModel.Model = "gemini-3.5-flash"
	recordDiffSummary(t, store, diffs[1]) // summarized
	for _, a := range []repository.DiffSummaryAttemptRow{
		diffAttempt(diffs[2], repository.SummaryOutcomeBlocked, anHourAgo),
		diffAttempt(diffs[3], repository.SummaryOutcomeError, summaryNow().Add(-time.Minute)),
		diffAttempt(diffs[4], repository.SummaryOutcomeInvalid, anHourAgo), // 1 h wait is over
		blockedOtherModel,
	} {
		if err := store.RecordDiffSummaryAttempt(ctx, a); err != nil {
			t.Fatalf("record diff attempt: %v", err)
		}
	}
	row := func(n int) string { return billID(n) + "|" + diffs[n] }

	assertRows(t, diffQueue(t, store, nil), []string{row(4), row(5), row(6)})
	// Once the error's hour has passed it's due again; the block never is.
	later := func(q *repository.DiffSummaryQueueQuery) { q.Now = summaryNow().Add(2 * time.Hour) }
	assertRows(t, diffQueue(t, store, later), []string{row(3), row(4), row(5), row(6)})
	// Attempts with another prompt version hold nothing back, the block included.
	newPrompt := func(q *repository.DiffSummaryQueueQuery) { q.PromptVersion = "diff-v3" }
	assertRows(t, diffQueue(t, store, newPrompt), []string{row(2), row(3), row(4), row(5), row(6)})
	limited := func(q *repository.DiffSummaryQueueQuery) { q.Limit = 2 }
	assertRows(t, diffQueue(t, store, limited), []string{row(4), row(5)})

	refs, err := store.QueryDiffsToSummarize(ctx, repository.DiffSummaryQueueQuery{
		Congress: testdb.FixtureCongress, Limit: 1, PromptVersion: diffPrompt, Model: summaryModel,
		Now: summaryNow(),
	})
	if err != nil || len(refs) != 1 || string(refs[0].DiffContent) != `{"added":["SEC. 2."]}` {
		t.Errorf("refs = %+v, %v; want the diff content", refs, err)
	}
}

// diffAttemptRow reads a diff's attempt as outcome|attempts|next_attempt_at (RFC 3339, or "").
func diffAttemptRow(t *testing.T, client *spanner.Client, diffID string) string {
	t.Helper()
	rows := queryStrings(t, client, `SELECT outcome, CAST(attempts AS STRING),
		IFNULL(FORMAT_TIMESTAMP('%FT%TZ', next_attempt_at, 'UTC'), '')
		FROM diff_summary_attempts WHERE diff_id = @diff`, map[string]any{"diff": diffID})
	if len(rows) != 1 {
		t.Fatalf("diff attempt rows = %q, want one", rows)
	}
	return rows[0]
}

func TestRecordDiffSummaryAttempt_BackoffAndReset(t *testing.T) {
	store, client := newSummaryStore(t)
	diff := seedDiffBill(t, store, client, testdb.FixtureCongress, 1)
	at := time.Date(2026, time.September, 27, 0, 0, 0, 0, time.UTC)
	withPrompt := func(a repository.DiffSummaryAttemptRow, prompt string) repository.DiffSummaryAttemptRow {
		a.PromptVersion = prompt
		return a
	}

	steps := []struct {
		name string
		row  repository.DiffSummaryAttemptRow
		want string
	}{
		{"first error waits 1 h", diffAttempt(diff, repository.SummaryOutcomeError, at),
			"error|1|2026-09-27T01:00:00Z"},
		{"second failure waits 4 h", diffAttempt(diff, repository.SummaryOutcomeInvalid, at),
			"invalid|2|2026-09-27T04:00:00Z"},
		{"third waits 24 h", diffAttempt(diff, repository.SummaryOutcomeTruncatedOutput, at),
			"truncated_output|3|2026-09-28T00:00:00Z"},
		{"then every 24 h", diffAttempt(diff, repository.SummaryOutcomeError, at),
			"error|4|2026-09-28T00:00:00Z"},
		{
			"a new prompt restarts the count",
			withPrompt(diffAttempt(diff, repository.SummaryOutcomeError, at), "diff-v3"),
			"error|1|2026-09-27T01:00:00Z",
		},
		{"blocked is never retried", withPrompt(diffAttempt(diff, repository.SummaryOutcomeBlocked, at), "diff-v3"),
			"blocked|2|"},
		{"success resets", diffAttempt(diff, repository.SummaryOutcomeOK, at), "ok|0|"},
	}
	for _, step := range steps {
		if err := store.RecordDiffSummaryAttempt(t.Context(), step.row); err != nil {
			t.Fatalf("%s: record diff attempt: %v", step.name, err)
		}
		if got := diffAttemptRow(t, client, diff); got != step.want {
			t.Errorf("%s: attempt = %q, want %q", step.name, got, step.want)
		}
	}
}

func TestRecordDiffSummaryAttempt_WritesSummaryWithProvenance(t *testing.T) {
	store, client := newSummaryStore(t)
	diff := seedDiffBill(t, store, client, testdb.FixtureCongress, 1)
	a := diffAttempt(diff, repository.SummaryOutcomeOK, summaryNow())
	a.Summary = &repository.DiffSummaryRow{
		DiffID: diff, Summary: "Adds section 2.", ModelUsed: summaryModel, PromptVersion: diffPrompt,
		ModelVersion: "gemini-3.8-flash-001", InputTokens: 900, OutputTokens: 120,
	}
	if err := store.RecordDiffSummaryAttempt(t.Context(), a); err != nil {
		t.Fatalf("record diff attempt: %v", err)
	}

	assertRows(t, queryStrings(t, client, `SELECT summary, model_used, prompt_version, model_version,
		CAST(input_tokens AS STRING), CAST(output_tokens AS STRING),
		FORMAT_TIMESTAMP('%FT%TZ', generated_at, 'UTC') FROM bill_text_diff_summaries WHERE diff_id = @diff`,
		map[string]any{"diff": diff}),
		[]string{"Adds section 2.|" + summaryModel + "|diff-v2|gemini-3.8-flash-001|900|120|2026-09-27T12:00:00Z"})
	if got := diffAttemptRow(t, client, diff); got != "ok|0|" {
		t.Errorf("attempt = %q, want ok", got)
	}
	assertRows(t, diffQueue(t, store, nil), []string{})

	// A failed attempt writes no summary.
	other := seedDiffBill(t, store, client, testdb.FixtureCongress, 2)
	failed := diffAttempt(other, repository.SummaryOutcomeError, summaryNow())
	if err := store.RecordDiffSummaryAttempt(t.Context(), failed); err != nil {
		t.Fatalf("record diff attempt: %v", err)
	}
	assertRows(t, queryStrings(t, client, "SELECT summary FROM bill_text_diff_summaries WHERE diff_id = @diff",
		map[string]any{"diff": other}), nil)
}

// Diff attempts count toward the same daily cap as bill attempts.
func TestCountSummaryAttemptsSince_CountsDiffs(t *testing.T) {
	store, client := newSummaryStore(t)
	ctx := t.Context()
	midnight := time.Date(2026, time.September, 27, 0, 0, 0, 0, time.UTC)
	for n, at := range []time.Time{midnight.Add(-time.Second), midnight, midnight.Add(3 * time.Hour)} {
		diff := seedDiffBill(t, store, client, testdb.FixtureCongress, n+1)
		if err := store.RecordDiffSummaryAttempt(
			ctx,
			diffAttempt(diff, repository.SummaryOutcomeError, at),
		); err != nil {
			t.Fatalf("record diff attempt: %v", err)
		}
	}
	// One bill attempt today, on a bill without diffs.
	seedSummaryBill(t, store, client, testdb.FixtureCongress, 9, 10, nil)
	if err := store.RecordSummaryAttempt(
		ctx,
		attempt(billID(9), "h", repository.SummaryOutcomeOK, midnight),
	); err != nil {
		t.Fatalf("record attempt: %v", err)
	}

	got, err := store.CountSummaryAttemptsSince(ctx, midnight)
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if got != 3 {
		t.Errorf("attempts since midnight = %d, want 2 diffs and 1 bill", got)
	}
}
