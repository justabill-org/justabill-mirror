package spannerdb_test

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

// batchHold is the hold AI_BATCH_HOLD's default gives a batch submitted at summaryNow.
const batchHold = 100 * time.Hour

// seedTextBills stores H.R. 1..n of the fixture congress, each with an "ih" version whose text
// hash is "hash-ih", and returns them as queue items.
func seedTextBills(
	t *testing.T, store *spannerdb.PipelineStoreImpl, client *spanner.Client, n int,
) []repository.SummaryQueueItem {
	t.Helper()
	items := make([]repository.SummaryQueueItem, 0, n)
	for i := 1; i <= n; i++ {
		ids := seedSummaryBill(t, store, client, testdb.FixtureCongress, i, 10, []string{"ih"}, "ih")
		items = append(items, repository.SummaryQueueItem{
			BillID: billID(i), VersionID: ids["ih"], VersionCode: "ih", ContentHash: "hash-ih",
		})
	}
	return items
}

func holds(batch string, bills ...repository.SummaryQueueItem) repository.SummaryBatchHolds {
	return repository.SummaryBatchHolds{
		BatchID: batch, PromptVersion: summaryPrompt, Model: summaryModel,
		At: summaryNow(), Until: summaryNow().Add(batchHold), Bills: bills,
	}
}

// batchAttempt reads a bill's attempt as outcome|attempts|next_attempt_at|request_type|batch_id.
func batchAttempt(t *testing.T, client *spanner.Client, bill string) string {
	t.Helper()
	rows := queryStrings(t, client, `SELECT CONCAT(outcome, '|', CAST(attempts AS STRING), '|',
		IFNULL(FORMAT_TIMESTAMP('%FT%TZ', next_attempt_at, 'UTC'), ''), '|',
		IFNULL(request_type, ''), '|', IFNULL(batch_id, ''))
		FROM summary_attempts WHERE bill_id = @bill`, map[string]any{"bill": bill})
	if len(rows) != 1 {
		t.Fatalf("attempt rows = %q, want one", rows)
	}
	return rows[0]
}

func assertAttempt(t *testing.T, client *spanner.Client, bill, want string) {
	t.Helper()
	if got := batchAttempt(t, client, bill); got != want {
		t.Errorf("%s attempt = %q, want %q", bill, got, want)
	}
}

func TestSummaryBatch_CreateUpdateAndOpen(t *testing.T) {
	store, _ := newSummaryStore(t)
	ctx := t.Context()
	created := summaryNow()
	first := repository.SummaryBatch{
		BatchID: "b-1", Congress: testdb.FixtureCongress, Model: summaryModel, PromptVersion: summaryPrompt,
		State: repository.SummaryBatchExporting, BillCount: 1200,
		InputURI: "gs://bucket/summaries/b-1/input.jsonl", CreatedAt: created,
	}
	second := first
	second.BatchID, second.CreatedAt = "b-2", created.Add(time.Hour)
	second.InputURI = "gs://bucket/summaries/b-2/input.jsonl"
	for _, b := range []repository.SummaryBatch{second, first} {
		if err := store.CreateSummaryBatch(ctx, b); err != nil {
			t.Fatalf("create %s: %v", b.BatchID, err)
		}
	}
	if err := store.CreateSummaryBatch(ctx, first); err == nil {
		t.Error("creating b-1 twice succeeded, want an error")
	}

	// The job is created, then finishes: each update sets only what it names.
	ok, failed, finished := 1180, 20, created.Add(30*time.Hour)
	updates := []repository.SummaryBatchUpdate{
		{BatchID: "b-1", State: "JOB_STATE_PENDING", JobName: "projects/p/locations/global/batchPredictionJobs/7"},
		{BatchID: "b-1", State: "JOB_STATE_SUCCEEDED", OutputURI: "gs://bucket/summaries/b-1/output/",
			OKCount: &ok, FailedCount: &failed, FinishedAt: &finished},
	}
	for _, u := range updates {
		if err := store.UpdateSummaryBatch(ctx, u); err != nil {
			t.Fatalf("update to %s: %v", u.State, err)
		}
	}

	got, err := store.SummaryBatch(ctx, "b-1")
	if err != nil {
		t.Fatalf("read b-1: %v", err)
	}
	want := first
	want.State, want.JobName = "JOB_STATE_SUCCEEDED", "projects/p/locations/global/batchPredictionJobs/7"
	want.OutputURI, want.OKCount, want.FailedCount, want.FinishedAt = "gs://bucket/summaries/b-1/output/", &ok, &failed, &finished
	if !reflect.DeepEqual(*got, want) {
		t.Errorf("b-1 = %+v, want %+v", *got, want)
	}

	assertOpen(t, store, "b-1", "b-2")

	imported := created.Add(31 * time.Hour)
	if err = store.UpdateSummaryBatch(ctx, repository.SummaryBatchUpdate{
		BatchID: "b-1", State: repository.SummaryBatchImported, ImportedAt: &imported,
	}); err != nil {
		t.Fatalf("mark imported: %v", err)
	}
	if err = store.UpdateSummaryBatch(ctx, repository.SummaryBatchUpdate{
		BatchID: "b-2", State: repository.SummaryBatchReleased,
	}); err != nil {
		t.Fatalf("mark released: %v", err)
	}
	assertOpen(t, store)
	got, err = store.SummaryBatch(ctx, "b-1")
	if err != nil || got.ImportedAt == nil || !got.ImportedAt.Equal(imported) ||
		got.OKCount == nil || *got.OKCount != ok {
		t.Errorf("b-1 after import = %+v, %v; want imported_at %s and the counts kept", got, err, imported)
	}
}

// assertOpen checks the IDs OpenSummaryBatches lists, in order.
func assertOpen(t *testing.T, store *spannerdb.PipelineStoreImpl, want ...string) {
	t.Helper()
	open, err := store.OpenSummaryBatches(t.Context())
	if err != nil {
		t.Fatalf("open batches: %v", err)
	}
	ids := make([]string, 0, len(open))
	for _, b := range open {
		ids = append(ids, b.BatchID)
	}
	if len(ids) != len(want) || (len(want) > 0 && !reflect.DeepEqual(ids, want)) {
		t.Errorf("open batches = %q, want %q", ids, want)
	}
}

func TestSummaryBatch_NotFound(t *testing.T) {
	store, _ := newSummaryStore(t)
	if _, err := store.SummaryBatch(t.Context(), "missing"); !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("read missing batch: err = %v, want ErrNotFound", err)
	}
	err := store.UpdateSummaryBatch(t.Context(), repository.SummaryBatchUpdate{BatchID: "missing", State: "x"})
	if !errors.Is(err, repository.ErrNotFound) {
		t.Errorf("update missing batch: err = %v, want ErrNotFound", err)
	}
}

func TestHoldSummaryBatch_HoldsBillsOutOfTheQueue(t *testing.T) {
	store, client := newSummaryStore(t)
	ctx := t.Context()
	bills := seedTextBills(t, store, client, 3)

	// A hold for text the bill no longer has doesn't hold it back.
	stale := bills[2]
	stale.ContentHash = "hash-old"
	if err := store.HoldSummaryBatch(ctx, holds("b-1", bills[0], bills[1], stale)); err != nil {
		t.Fatalf("hold: %v", err)
	}

	assertAttempt(t, client, billID(1), "batch_pending|0|2026-10-01T16:00:00Z|batch|b-1")
	assertRows(t, queue(t, store, nil), []string{"hr-119-3|ih|hash-ih|1"})
	// Another model or prompt isn't held.
	assertRows(t, queue(t, store, func(q *repository.SummaryQueueQuery) { q.Model = "other" }),
		[]string{"hr-119-1|ih|hash-ih|1", "hr-119-2|ih|hash-ih|1", "hr-119-3|ih|hash-ih|1"})
	// Once the hold has passed, the bills are due again.
	assertRows(t, queue(t, store, func(q *repository.SummaryQueueQuery) { q.Now = q.Now.Add(batchHold) }),
		[]string{"hr-119-1|ih|hash-ih|1", "hr-119-2|ih|hash-ih|1", "hr-119-3|ih|hash-ih|1"})
}

func TestHoldSummaryBatch_CommitsInChunks(t *testing.T) {
	store, client := newSummaryStore(t)
	bills := seedTextBills(t, store, client, 4)
	// H.R. 5 has no bills row, so the chunk holding it fails; the chunks before it stay committed.
	bills = append(bills, repository.SummaryQueueItem{BillID: billID(5), ContentHash: "hash-ih"})

	err := store.HoldSummaryBatchInChunks(t.Context(), holds("b-1", bills...), 2)
	if err == nil {
		t.Fatal("hold with a missing bill succeeded, want an error")
	}
	assertRows(t, queryStrings(t, client, `SELECT bill_id FROM summary_attempts
		WHERE batch_id = 'b-1' AND outcome = 'batch_pending' ORDER BY bill_id`, nil),
		[]string{"hr-119-1", "hr-119-2", "hr-119-3", "hr-119-4"})

	if err = store.HoldSummaryBatch(t.Context(), holds("", bills[0])); err == nil {
		t.Error("hold without a batch ID succeeded, want an error")
	}
}

func TestReleaseSummaryBatch(t *testing.T) {
	store, client := newSummaryStore(t)
	ctx := t.Context()
	bills := seedTextBills(t, store, client, 4)
	if err := store.HoldSummaryBatch(ctx, holds("b-1", bills[0], bills[1], bills[2])); err != nil {
		t.Fatalf("hold b-1: %v", err)
	}
	if err := store.HoldSummaryBatch(ctx, holds("b-2", bills[3])); err != nil {
		t.Fatalf("hold b-2: %v", err)
	}
	// H.R. 3 was summarized after its batch was submitted: release leaves it alone.
	s := summary(billID(3), "hash-ih", summaryPrompt)
	done := attempt(billID(3), "hash-ih", repository.SummaryOutcomeOK, summaryNow())
	done.Summary = &s
	if err := store.RecordSummaryAttempt(ctx, done); err != nil {
		t.Fatalf("record: %v", err)
	}

	releasedAt := summaryNow().Add(2 * time.Hour)
	n, err := store.ReleaseSummaryBatch(ctx, "b-1", releasedAt)
	if err != nil {
		t.Fatalf("release: %v", err)
	}
	if n != 2 {
		t.Errorf("released %d bills, want 2", n)
	}
	assertAttempt(t, client, billID(1), "batch_pending|0|2026-09-27T14:00:00Z|batch|b-1")
	assertAttempt(t, client, billID(3), "ok|0|||")
	assertRows(t, queue(t, store, func(q *repository.SummaryQueueQuery) { q.Now = releasedAt }),
		[]string{"hr-119-1|ih|hash-ih|1", "hr-119-2|ih|hash-ih|1"})

	if n, err = store.ReleaseSummaryBatch(ctx, "b-1", releasedAt); err != nil || n != 0 {
		t.Errorf("release again = %d, %v; want 0, nil", n, err)
	}
}

func TestRecordSummaryAttempt_AfterBatchHold(t *testing.T) {
	store, client := newSummaryStore(t)
	ctx := t.Context()
	bills := seedTextBills(t, store, client, 1)
	bill := billID(1)
	at := summaryNow()
	for _, outcome := range []string{repository.SummaryOutcomeError, repository.SummaryOutcomeInvalid} {
		if err := store.RecordSummaryAttempt(ctx, attempt(bill, "hash-ih", outcome, at)); err != nil {
			t.Fatalf("record %s: %v", outcome, err)
		}
	}
	if err := store.HoldSummaryBatch(ctx, holds("b-1", bills...)); err != nil {
		t.Fatalf("hold: %v", err)
	}

	// The batch line failed: the two failures before the hold don't count, as after an ok.
	failed := attempt(bill, "hash-ih", repository.SummaryOutcomeError, at.Add(time.Hour))
	failed.RequestType, failed.BatchID = repository.SummaryRequestBatch, "b-1"
	if err := store.RecordSummaryAttempt(ctx, failed); err != nil {
		t.Fatalf("record batch failure: %v", err)
	}
	assertAttempt(t, client, bill, "error|1|2026-09-27T14:00:00Z|batch|b-1")

	// A re-import (after another line failed to store) writes nothing, so the failure counts once.
	failed.AttemptedAt = at.Add(2 * time.Hour)
	if err := store.RecordSummaryAttempt(ctx, failed); err != nil {
		t.Fatalf("record batch failure again: %v", err)
	}
	assertAttempt(t, client, bill, "error|1|2026-09-27T14:00:00Z|batch|b-1")

	// A line Vertex AI failed is due when the import says.
	if err := store.HoldSummaryBatch(ctx, holds("b-2", bills...)); err != nil {
		t.Fatalf("hold b-2: %v", err)
	}
	retryAt := at.Add(3 * time.Hour)
	failed.BatchID, failed.RetryAt = "b-2", &retryAt
	if err := store.RecordSummaryAttempt(ctx, failed); err != nil {
		t.Fatalf("record batch line error: %v", err)
	}
	assertAttempt(t, client, bill, "error|1|2026-09-27T15:00:00Z|batch|b-2")

	pending := attempt(bill, "hash-ih", repository.SummaryOutcomeBatchPending, at)
	if err := store.RecordSummaryAttempt(ctx, pending); err == nil {
		t.Error("recording a batch_pending attempt succeeded, want an error")
	}
}

// A batch result for a bill the batch no longer holds for that text writes nothing (#650): the
// synchronous job summarized the bill's newer text during the hold, or another batch holds it.
func TestRecordSummaryAttempt_StaleBatchResult(t *testing.T) {
	store, client := newSummaryStore(t)
	ctx := t.Context()
	// H.R. 1 was held for its introduced text, then the reported version arrived.
	ids := seedSummaryBill(t, store, client, testdb.FixtureCongress, 1, 10, []string{"rh", "ih"}, "rh", "ih")
	held := repository.SummaryQueueItem{BillID: billID(1), VersionID: ids["ih"], ContentHash: "hash-ih"}
	// H.R. 2 is held by b-2, and H.R. 3 was never held.
	for n := 2; n <= 3; n++ {
		seedSummaryBill(t, store, client, testdb.FixtureCongress, n, 10, []string{"ih"}, "ih")
	}
	if err := store.HoldSummaryBatch(ctx, holds("b-1", held)); err != nil {
		t.Fatalf("hold b-1: %v", err)
	}
	other := repository.SummaryQueueItem{BillID: billID(2), ContentHash: "hash-ih"}
	if err := store.HoldSummaryBatch(ctx, holds("b-2", other)); err != nil {
		t.Fatalf("hold b-2: %v", err)
	}
	newer := attempt(billID(1), "hash-rh", repository.SummaryOutcomeOK, summaryNow().Add(time.Hour))
	newer.RequestType = repository.SummaryRequestStandard
	newerSummary := summary(billID(1), "hash-rh", summaryPrompt)
	newer.Summary = &newerSummary
	if err := store.RecordSummaryAttempt(ctx, newer); err != nil {
		t.Fatalf("record the newer summary: %v", err)
	}

	for _, bill := range []string{billID(1), billID(2), billID(3)} {
		line := attempt(bill, "hash-ih", repository.SummaryOutcomeOK, summaryNow().Add(2*time.Hour))
		line.RequestType, line.BatchID = repository.SummaryRequestBatch, "b-1"
		lineSummary := summary(bill, "hash-ih", summaryPrompt)
		line.Summary = &lineSummary
		if err := store.RecordSummaryAttempt(ctx, line); !errors.Is(err, repository.ErrStaleBatchAttempt) {
			t.Errorf("import %s's line: err = %v, want ErrStaleBatchAttempt", bill, err)
		}
	}

	assertAttempt(t, client, billID(1), "ok|0||standard|")
	assertAttempt(t, client, billID(2), "batch_pending|0|2026-10-01T16:00:00Z|batch|b-2")
	assertRows(t, queryStrings(t, client, `SELECT CONCAT(bill_id, '|', source_content_hash) FROM bill_summaries
		ORDER BY bill_id`, nil), []string{billID(1) + "|hash-rh"})
	assertRows(t, queue(t, store, nil), []string{billID(3) + "|ih|hash-ih|1"})
}

func TestCountSummaryAttemptsSince_SkipsBatchAttempts(t *testing.T) {
	store, client := newSummaryStore(t)
	ctx := t.Context()
	bills := seedTextBills(t, store, client, 4)
	at := summaryNow()
	for i, requestType := range []string{"", repository.SummaryRequestStandard, repository.SummaryRequestBatch} {
		a := attempt(bills[i].BillID, "hash-ih", repository.SummaryOutcomeOK, at)
		a.RequestType = requestType
		if err := store.RecordSummaryAttempt(ctx, a); err != nil {
			t.Fatalf("record: %v", err)
		}
	}
	if err := store.HoldSummaryBatch(ctx, holds("b-1", bills[3])); err != nil {
		t.Fatalf("hold: %v", err)
	}

	got, err := store.CountSummaryAttemptsSince(ctx, at.Add(-time.Hour))
	if err != nil {
		t.Fatalf("count: %v", err)
	}
	if got != 2 {
		t.Errorf("attempts counted = %d, want 2 (the batch attempt and hold don't count)", got)
	}
}
