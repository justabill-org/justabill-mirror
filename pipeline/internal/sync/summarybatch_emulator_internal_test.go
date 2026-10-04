package sync

import (
	"encoding/json"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/spanner"
	"google.golang.org/genai"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
	"github.com/justabill-org/justabill/pipeline/internal/ai"
	"github.com/justabill-org/justabill/pipeline/internal/ai/batchjsonl"
)

// emulatorBatchService is a Service on a fresh emulator database holding H.R. 1..n of the 119th,
// each with one text version and its text, with the batch path on the fake API and files.
func emulatorBatchService(t *testing.T, n int) (*Service, *spanner.Client, *fakeBatchAPI, *memFiles) {
	t.Helper()
	client := testdb.New(t)
	ctx := t.Context()
	testdb.SeedCongress(ctx, t, client, 119)
	store := spannerdb.NewPipelineStore(&spannerdb.Client{Spanner: client})
	for i := 1; i <= n; i++ {
		id := "hr-119-" + strconv.Itoa(i)
		if err := store.UpsertBill(ctx, repository.BillRow{
			ID: id, Congress: 119, BillType: "hr", Number: i, Title: "Bill " + strconv.Itoa(i),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := store.UpsertBillTextVersions(ctx, id, []repository.TextVersionRow{{
			BillID: id, VersionType: "Introduced in House", VersionCode: "ih", SortOrder: 1,
			Formats: json.RawMessage(`[{"type":"Formatted XML","url":"https://example.test/ih.xml"}]`),
		}}); err != nil {
			t.Fatal(err)
		}
	}
	iter := client.Single().Query(ctx, spanner.Statement{SQL: "SELECT bill_id, version_id FROM bill_text_versions"})
	err := iter.Do(func(row *spanner.Row) error {
		var bill, version string
		if err := row.Columns(&bill, &version); err != nil {
			return err
		}
		return store.InsertBillText(ctx, repository.BillTextRow{
			TextVersionID: version, Format: "xml", Content: "SEC. 1. Text of " + bill + ".",
			ContentHash: "hash-" + bill, FetchedAt: jobNow(),
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	s, _ := jobService(&summaryStore{}, nil)
	s.store = store
	api := &fakeBatchAPI{}
	files := newMemFiles()
	s.SetSummaryBatches(SummaryBatchConfig{Bucket: testBucket, Hold: DefaultSummaryBatchHold, MinBills: 1},
		ai.Config{Model: ai.DefaultModel}, newBatchJobs(t, api), files)
	return s, client, api, files
}

// emulatorQueue is the bills the synchronous job would take now, by ID.
func emulatorQueue(t *testing.T, s *Service) []repository.SummaryQueueItem {
	t.Helper()
	items, err := s.store.QueryBillsToSummarize(t.Context(), repository.SummaryQueueQuery{
		Congress: 119, Limit: 100, PromptVersion: ai.PromptVersionBill, Model: ai.DefaultModel, Now: jobNow(),
		RecentDays: DefaultSummaryRecentDays,
	})
	if err != nil {
		t.Fatal(err)
	}
	return items
}

func queuedIDs(items []repository.SummaryQueueItem) []string {
	ids := make([]string, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.BillID)
	}
	slices.Sort(ids)
	return ids
}

// submitThree submits H.R. 1..3 and returns the batch's prefix and its manifest items by bill.
func submitThree(t *testing.T, s *Service, files *memFiles) (string, map[string]batchjsonl.Item) {
	t.Helper()
	plan, err := s.SubmitSummaryBatch(t.Context(), SummaryBatchRequest{
		Congress: 119, Tiers: []int{repository.SummaryTierOther}, Max: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if plan.Bills != 3 {
		t.Fatalf("plan = %+v, want 3 bills", plan)
	}
	if left := emulatorQueue(t, s); len(left) != 0 {
		t.Errorf("queue after submit = %v, want every bill held", queuedIDs(left))
	}
	prefix := "gs://" + testBucket + "/summaries/" + plan.BatchID + "/"
	items := map[string]batchjsonl.Item{}
	for _, raw := range files.lines(t, prefix+"manifest.jsonl") {
		var it batchjsonl.Item
		if err = json.Unmarshal([]byte(raw), &it); err != nil {
			t.Fatal(err)
		}
		items[it.BillID] = it
	}
	return prefix, items
}

// Submit holds the bills out of the synchronous queue; the import stores H.R. 1's summary, makes
// H.R. 2 (a line Vertex AI failed) due now, and releases H.R. 3 (no line). A second poll finds no
// open batch.
func TestEmulator_SummaryBatchImportAndRelease(t *testing.T) {
	s, client, api, files := emulatorBatchService(t, 3)
	store := s.store
	prefix, items := submitThree(t, s, files)

	ok := answer(`{"short_summary":"Short.","long_summary":"Long.","who_it_affects":"Farmers."}`, "STOP")
	files.objects[prefix+"output/prediction-1/predictions.jsonl"] = []byte(strings.Join([]string{
		outputLine(items["hr-119-1"].Line, ok, ""),
		outputLine(items["hr-119-2"].Line, "", `{"error":{"code":500,"status":"INTERNAL"}}`),
	}, "\n"))
	api.state = genai.JobStateSucceeded
	if err := s.PollSummaryBatches(t.Context(), 119); err != nil {
		t.Fatal(err)
	}

	if due := queuedIDs(emulatorQueue(t, s)); !slices.Equal(due, []string{"hr-119-2", "hr-119-3"}) {
		t.Errorf("due after import = %v, want H.R. 2 and 3", due)
	}
	row, err := client.Single().ReadRow(t.Context(), "bill_summaries", spanner.Key{"hr-119-1"},
		[]string{"why_it_matters", "source_content_hash"})
	if err != nil {
		t.Fatalf("H.R. 1's summary: %v", err)
	}
	var who, hash string
	if err = row.Columns(&who, &hash); err != nil || who != "Farmers." || hash != "hash-hr-119-1" {
		t.Errorf("H.R. 1's summary = %q for %q, %v", who, hash, err)
	}
	open, err := store.OpenSummaryBatches(t.Context())
	if err != nil || len(open) != 0 {
		t.Errorf("open batches = %+v, %v; want none", open, err)
	}
	if err = s.PollSummaryBatches(t.Context(), 119); err != nil || len(api.calls()) != 2 {
		t.Errorf("second poll = %v, calls %v; want no more job calls", err, api.calls())
	}
	used, err := store.CountSummaryAttemptsSince(t.Context(), jobNow().Add(-time.Hour))
	if err != nil || used != 0 {
		t.Errorf("attempts counted toward the daily cap = %d, %v; want none", used, err)
	}
}

// The synchronous job summarized H.R. 1 during the hold (its text changed): the batch's later
// line for H.R. 1 is skipped and the newer summary stays (#650).
func TestEmulator_SummaryBatchSkipsStaleLines(t *testing.T) {
	s, client, api, files := emulatorBatchService(t, 3)
	prefix, items := submitThree(t, s, files)
	newer := repository.SummaryAttemptRow{
		BillID: "hr-119-1", ContentHash: "hash-new", PromptVersion: ai.PromptVersionBill, Model: ai.DefaultModel,
		Outcome: repository.SummaryOutcomeOK, AttemptedAt: jobNow(), RequestType: repository.SummaryRequestStandard,
		Summary: &repository.BillSummaryRow{
			BillID: "hr-119-1", ShortSummary: "S.", LongSummary: "L.", WhoItAffects: "Newer.",
			ModelUsed: ai.DefaultModel, SourceContentHash: "hash-new", PromptVersion: ai.PromptVersionBill,
		},
	}
	if err := s.store.RecordSummaryAttempt(t.Context(), newer); err != nil {
		t.Fatal(err)
	}

	ok := answer(`{"short_summary":"Short.","long_summary":"Long.","who_it_affects":"Older."}`, "STOP")
	files.objects[prefix+"output/prediction-1/predictions.jsonl"] = []byte(strings.Join([]string{
		outputLine(items["hr-119-1"].Line, ok, ""), outputLine(items["hr-119-2"].Line, ok, ""),
	}, "\n"))
	api.state = genai.JobStateSucceeded
	if err := s.PollSummaryBatches(t.Context(), 119); err != nil {
		t.Fatal(err)
	}

	row, err := client.Single().ReadRow(t.Context(), "bill_summaries", spanner.Key{"hr-119-1"},
		[]string{"why_it_matters", "source_content_hash"})
	if err != nil {
		t.Fatalf("H.R. 1's summary: %v", err)
	}
	var who, hash string
	if err = row.Columns(&who, &hash); err != nil || who != "Newer." || hash != "hash-new" {
		t.Errorf("H.R. 1's summary = %q for %q, %v; want the newer one kept", who, hash, err)
	}
	iter := client.Single().Query(t.Context(), spanner.Statement{SQL: "SELECT ok_count FROM summary_batches"})
	defer iter.Stop()
	if row, err = iter.Next(); err != nil {
		t.Fatalf("batch: %v", err)
	}
	var okCount int64
	if err = row.Columns(&okCount); err != nil || okCount != 1 {
		t.Errorf("batch ok_count = %d, %v; want 1 (H.R. 2)", okCount, err)
	}
}

// A failed job releases every bill it held.
func TestEmulator_SummaryBatchReleaseOnFailure(t *testing.T) {
	s, _, api, files := emulatorBatchService(t, 3)
	store := s.store
	submitThree(t, s, files)

	api.state = genai.JobStateFailed
	if err := s.PollSummaryBatches(t.Context(), 119); err != nil {
		t.Fatal(err)
	}
	if due := queuedIDs(emulatorQueue(t, s)); len(due) != 3 {
		t.Errorf("due after release = %v, want all 3", due)
	}
	open, err := store.OpenSummaryBatches(t.Context())
	if err != nil || len(open) != 0 {
		t.Errorf("open batches = %+v, %v", open, err)
	}
}
