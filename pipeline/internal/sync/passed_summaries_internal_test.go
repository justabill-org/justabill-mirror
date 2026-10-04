package sync

import (
	"context"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/repository"
)

// drainingStore is a summaryStore whose queue drops the bills already attempted, as the real
// queue does once a bill has a current summary or a failed attempt holding it back.
type drainingStore struct {
	*summaryStore

	successes []repository.SyncRun
}

func (f *drainingStore) RecordSyncSuccess(_ context.Context, run repository.SyncRun) error {
	f.successes = append(f.successes, run)
	return nil
}

func (f *drainingStore) QueryBillsToSummarize(
	_ context.Context, q repository.SummaryQueueQuery,
) ([]repository.SummaryQueueItem, error) {
	f.queries = append(f.queries, q)
	done := f.attemptsByBill()
	var due []repository.SummaryQueueItem
	for _, it := range f.items {
		if _, ok := done[it.BillID]; !ok && len(due) < q.Limit {
			due = append(due, it)
		}
	}
	return due, nil
}

// CountSummaryAttemptsSince counts this run's attempts on top of the day's earlier ones.
func (f *drainingStore) CountSummaryAttemptsSince(ctx context.Context, since time.Time) (int, error) {
	used, err := f.summaryStore.CountSummaryAttemptsSince(ctx, since)
	return used + len(f.attempts), err
}

func TestSyncPassedSummaries_RunsUntilTheQueueIsEmpty(t *testing.T) {
	tests := []struct {
		name        string
		items       int
		used        int
		limit       int
		wantDone    int
		wantQueries int
	}{
		{"every passed bill, in batches", 450, 0, 0, 450, 4},
		{"the limit stops it", 450, 0, 250, 250, 2},
		{"the day's cap stops it", 450, 2800, 0, 200, 1},
		{"nothing due", 0, 0, 0, 0, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inner := &summaryStore{used: tt.used, items: queueItems(tt.items)}
			store := &drainingStore{summaryStore: inner}
			s, logs := jobService(inner, &fakeSummarizer{})
			s.store = store
			n, err := s.syncPassedSummaries(t.Context(), 119, tt.limit)
			if err != nil {
				t.Fatal(err)
			}
			if n != tt.wantDone || len(inner.attempts) != tt.wantDone {
				t.Errorf("summarized %d, recorded %d; want %d", n, len(inner.attempts), tt.wantDone)
			}
			if len(inner.queries) != tt.wantQueries {
				t.Errorf("%d queue queries, want %d", len(inner.queries), tt.wantQueries)
			}
			assertPassedQueries(t, inner.queries)
			if lines := logLines(t, logs, "passed bill summaries synced"); len(lines) != 1 ||
				lines[0]["summarized"] != float64(tt.wantDone) {
				t.Errorf("passed bill summaries synced = %v", lines)
			}
		})
	}
}

// assertPassedQueries checks every queue query was scoped to passed bills and at most a batch.
func assertPassedQueries(t *testing.T, queries []repository.SummaryQueueQuery) {
	t.Helper()
	for _, q := range queries {
		if !q.PassedChamber || q.Limit > DefaultSummaryBatch {
			t.Errorf("query %+v: want PassedChamber and a limit of at most a batch", q)
		}
	}
}

func TestSyncPassedSummaries_StopsAfterMaxPasses(t *testing.T) {
	// summaryStore's queue never drains: every pass returns the same bills.
	store := &summaryStore{items: queueItems(1)}
	s, _ := jobService(store, &fakeSummarizer{})
	if _, err := s.syncPassedSummaries(t.Context(), 119, 0); err != nil {
		t.Fatal(err)
	}
	if len(store.queries) != maxPassedSummaryPasses {
		t.Errorf("%d passes, want the cap %d", len(store.queries), maxPassedSummaryPasses)
	}
}

func TestSyncPassedSummaries_NoSummarizer(t *testing.T) {
	s, _ := jobService(&summaryStore{}, nil)
	if err := s.SyncPassedSummaries(t.Context(), 119, 0); err != nil {
		t.Errorf("SyncPassedSummaries without a summarizer = %v", err)
	}
}

func TestSyncPassedSummaries_LimitedRunLeavesSyncState(t *testing.T) {
	tests := []struct {
		name          string
		limit         int
		wantSuccesses int
	}{
		{"unlimited records success", 0, 1},
		{"limited records nothing", 2, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			inner := &summaryStore{items: queueItems(3)}
			store := &drainingStore{summaryStore: inner}
			s, _ := jobService(inner, &fakeSummarizer{})
			s.store = store
			if err := s.SyncPassedSummaries(t.Context(), 119, tt.limit); err != nil {
				t.Fatal(err)
			}
			if len(store.successes) != tt.wantSuccesses {
				t.Errorf("recorded %d successes %+v, want %d", len(store.successes), store.successes, tt.wantSuccesses)
			}
		})
	}
}
