package spannerdb_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

// seedCRS stores CRS summaries and returns the content hash of the last one.
func seedCRS(t *testing.T, store *spannerdb.PipelineStoreImpl, rows ...repository.CRSSummaryRow) string {
	t.Helper()
	if err := store.UpsertCRSSummaries(t.Context(), rows); err != nil {
		t.Fatalf("upsert crs summaries: %v", err)
	}
	return rows[len(rows)-1].ContentHash
}

// crsSummary is a summary of the current text ("hash-ih") written with the CRS summary crsHash
// ("" for none).
func crsSummary(bill, crsHash string) repository.BillSummaryRow {
	s := summary(bill, "hash-ih", summaryPrompt)
	s.SourceCRSHash = crsHash
	return s
}

func TestQueryBillsToSummarize_CRSChange(t *testing.T) {
	store, client := newSummaryStore(t)
	ctx := t.Context()
	for n := 1; n <= 6; n++ {
		seedSummaryBill(t, store, client, testdb.FixtureCongress, n, 100, []string{"ih"}, "ih")
	}
	seedSummaryBill(t, store, client, testdb.FixtureCongress, 7, 5, []string{"ih"}, "ih")

	crs := make(map[int]string)
	for _, n := range []int{1, 2, 3, 5, 6, 7} {
		crs[n] = seedCRS(t, store,
			crsRow(t, billID(n), "00", "2025-05-20", "Introduced in House", "House", "2025-05-22T17:15:29Z"))
	}
	blockedOtherPrompt := attempt(billID(5), "hash-ih", repository.SummaryOutcomeBlocked, summaryNow().Add(-time.Hour))
	blockedOtherPrompt.PromptVersion = "bill-v1"
	for _, a := range []repository.SummaryAttemptRow{
		blockedOtherPrompt, // blocked for this text: a new CRS summary doesn't retry it
		attempt(billID(6), "hash-old", repository.SummaryOutcomeBlocked, summaryNow().Add(-time.Hour)), // other text
	} {
		if err := store.RecordSummaryAttempt(ctx, a); err != nil {
			t.Fatalf("record attempt: %v", err)
		}
	}
	for _, s := range []repository.BillSummaryRow{
		crsSummary(billID(1), ""),                 // written before the bill had a CRS summary: due
		crsSummary(billID(2), crs[2]),             // written with the latest: not due
		crsSummary(billID(3), "an-older-summary"), // written with an older one: due
		crsSummary(billID(4), ""),                 // no CRS summary: not due
		crsSummary(billID(5), ""),                 // blocked for its text: not due
		crsSummary(billID(6), ""),                 // blocked for other text: due
		crsSummary(billID(7), ""),                 // recent: due, in the recent tier
	} {
		if err := store.UpsertBillSummary(ctx, s); err != nil {
			t.Fatalf("upsert summary: %v", err)
		}
	}

	withCRS := func(q *repository.SummaryQueueQuery) { q.CRSContext = true }
	assertRows(t, queue(t, store, withCRS), []string{
		billID(7) + "|ih|hash-ih|1",
		billID(1) + "|ih|hash-ih|2", billID(3) + "|ih|hash-ih|2", billID(6) + "|ih|hash-ih|2",
	})
	counts, err := store.CountBillsToSummarize(ctx, repository.SummaryQueueQuery{
		Congress: testdb.FixtureCongress, PromptVersion: summaryPrompt, Model: summaryModel,
		Now: summaryNow(), RecentDays: 30, CRSContext: true,
	})
	if err != nil {
		t.Fatalf("count bills to summarize: %v", err)
	}
	if want := map[int]int{repository.SummaryTierRecent: 1, repository.SummaryTierOther: 3}; !reflect.DeepEqual(
		counts, want) {
		t.Errorf("backlog = %v, want %v", counts, want)
	}

	// With the CRS context off, a CRS change makes nothing due.
	assertRows(t, queue(t, store, nil), []string{})

	// Once a bill is summarized with its latest CRS summary, it isn't due again.
	if err = store.UpsertBillSummary(ctx, crsSummary(billID(1), crs[1])); err != nil {
		t.Fatalf("upsert summary: %v", err)
	}
	assertRows(t, queue(t, store, withCRS), []string{
		billID(7) + "|ih|hash-ih|1", billID(3) + "|ih|hash-ih|2", billID(6) + "|ih|hash-ih|2",
	})

	// A newer CRS summary makes it due once more.
	seedCRS(t, store, crsRow(t, billID(1), "07", "2025-06-01", "Reported to House", "House", "2025-06-03T10:00:00Z"))
	assertRows(t, queue(t, store, withCRS), []string{
		billID(7) + "|ih|hash-ih|1",
		billID(1) + "|ih|hash-ih|2", billID(3) + "|ih|hash-ih|2", billID(6) + "|ih|hash-ih|2",
	})
}

func TestLoadBillContext_LatestCRSSummary(t *testing.T) {
	store, client := newSummaryStore(t)
	ids := seedSummaryBill(t, store, client, testdb.FixtureCongress, 1, 10, []string{"ih"}, "ih")
	bill := billID(1)
	latest := crsRow(t, bill, "07", "2025-06-01", "Reported to House", "House", "2025-06-03T10:00:00Z")
	seedCRS(t, store,
		latest,
		crsRow(t, bill, "00", "2025-05-20", "Introduced in House", "House", "2025-09-22T12:34:08Z"))

	got, err := store.LoadBillContext(t.Context(), bill, ids["ih"])
	if err != nil {
		t.Fatalf("load bill context: %v", err)
	}
	want := &repository.SummaryCRSContext{
		ActionDesc: "Reported to House", ActionDate: time.Date(2025, time.June, 1, 0, 0, 0, 0, time.UTC),
		Text: latest.Text, ContentHash: latest.ContentHash,
	}
	if !reflect.DeepEqual(got.CRS, want) {
		t.Errorf("crs = %+v, want %+v", got.CRS, want)
	}
}
