package spannerdb_test

import (
	"strings"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
)

func recordFailure(t *testing.T, store *spannerdb.PipelineStoreImpl, f repository.ItemFailure) {
	t.Helper()
	if err := store.RecordItemFailure(t.Context(), f); err != nil {
		t.Fatal(err)
	}
}

// dueItem reads one item's row back through DueRetries at now; the item must be due by then.
func dueItem(t *testing.T, store *spannerdb.PipelineStoreImpl, step, id string, now time.Time) repository.RetryItem {
	t.Helper()
	items, err := store.DueRetries(t.Context(), step, 119, now, 100)
	if err != nil {
		t.Fatal(err)
	}
	for _, it := range items {
		if it.ItemID == id {
			return it
		}
	}
	t.Fatalf("%s not due at %v (due: %+v)", id, now, items)
	return repository.RetryItem{}
}

func mustStats(t *testing.T, store *spannerdb.PipelineStoreImpl, step string, now time.Time) repository.RetryStats {
	t.Helper()
	st, err := store.RetryStats(t.Context(), step, 119, now)
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// A new row starts at one attempt due in 30 minutes; each failure adds one and doubles the wait,
// and the first failure time survives.
func TestRecordItemFailure_CountsAndBacksOff(t *testing.T) {
	store := newSyncStateStore(t)
	first := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)
	second := first.Add(45 * time.Minute)

	recordFailure(t, store, repository.ItemFailure{
		Step: repository.RetryStepBills, Congress: 119, ItemID: "hr-119-43", Error: "actions: 500", FailedAt: first,
	})
	it := dueItem(t, store, repository.RetryStepBills, "hr-119-43", first.Add(30*time.Minute))
	if it.Attempts != 1 || !it.NextAttemptAt.Equal(first.Add(30*time.Minute)) {
		t.Errorf("after 1 failure: attempts %d, next %v; want 1, +30m", it.Attempts, it.NextAttemptAt)
	}

	recordFailure(t, store, repository.ItemFailure{
		Step: repository.RetryStepBills, Congress: 119, ItemID: "hr-119-43", Error: "actions: 502", FailedAt: second,
	})
	it = dueItem(t, store, repository.RetryStepBills, "hr-119-43", second.Add(time.Hour))
	if it.Attempts != 2 || !it.NextAttemptAt.Equal(second.Add(time.Hour)) {
		t.Errorf(
			"after 2 failures: attempts %d, next %v; want 2, %v",
			it.Attempts,
			it.NextAttemptAt,
			second.Add(time.Hour),
		)
	}
	if !it.FirstFailedAt.Equal(first) || !it.LastFailedAt.Equal(second) {
		t.Errorf("first, last failed = %v, %v; want %v, %v", it.FirstFailedAt, it.LastFailedAt, first, second)
	}
	if it.LastError != "actions: 502" {
		t.Errorf("LastError = %q, want the latest", it.LastError)
	}
}

// The tenth failure gives the item up, as does a permanent error on the first; given-up items
// are never due but are counted.
func TestRecordItemFailure_GivesUp(t *testing.T) {
	store := newSyncStateStore(t)
	base := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)

	for i := range repository.MaxRetryAttempts {
		recordFailure(t, store, repository.ItemFailure{
			Step: repository.RetryStepBills, Congress: 119, ItemID: "s-119-1", Error: "timeout",
			FailedAt: base.Add(time.Duration(i) * time.Hour),
		})
	}
	recordFailure(t, store, repository.ItemFailure{
		Step: repository.RetryStepBills, Congress: 119, ItemID: "s-119-2", Error: "404 Not Found",
		Permanent: true, FailedAt: base,
	})

	farFuture := base.AddDate(1, 0, 0)
	items, err := store.DueRetries(t.Context(), repository.RetryStepBills, 119, farFuture, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 0 {
		t.Errorf("DueRetries = %+v, want none (both given up)", items)
	}
	if st := mustStats(t, store, repository.RetryStepBills, farFuture); st != (repository.RetryStats{GivenUp: 2}) {
		t.Errorf("RetryStats = %+v, want 2 given up", st)
	}
}

// DueRetries returns only the step and congress asked for, only due rows, longest overdue first,
// up to the limit.
func TestDueRetries_FiltersOrdersAndLimits(t *testing.T) {
	store := newSyncStateStore(t)
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	fail := func(step string, congress int, id string, at time.Time) {
		recordFailure(
			t,
			store,
			repository.ItemFailure{Step: step, Congress: congress, ItemID: id, Error: "x", FailedAt: at},
		)
	}
	fail(repository.RetryStepBills, 119, "hr-119-3", now.Add(-40*time.Minute)) // due 10 min ago
	fail(repository.RetryStepBills, 119, "hr-119-1", now.Add(-2*time.Hour))    // due 90 min ago
	fail(repository.RetryStepBills, 119, "hr-119-2", now.Add(-time.Hour))      // due 30 min ago
	fail(repository.RetryStepBills, 119, "hr-119-9", now.Add(-10*time.Minute)) // due in 20 min
	fail(repository.RetryStepGovInfo, 119, "BILLS-119hr1ih", now.Add(-2*time.Hour))
	fail(repository.RetryStepBills, 118, "hr-118-1", now.Add(-2*time.Hour))

	items, err := store.DueRetries(t.Context(), repository.RetryStepBills, 119, now, 100)
	if err != nil {
		t.Fatal(err)
	}
	if got := itemIDs(items); strings.Join(got, ",") != "hr-119-1,hr-119-2,hr-119-3" {
		t.Errorf("DueRetries = %v, want hr-119-1,hr-119-2,hr-119-3", got)
	}

	items, err = store.DueRetries(t.Context(), repository.RetryStepBills, 119, now, 2)
	if err != nil {
		t.Fatal(err)
	}
	if got := itemIDs(items); strings.Join(got, ",") != "hr-119-1,hr-119-2" {
		t.Errorf("DueRetries(limit 2) = %v, want hr-119-1,hr-119-2", got)
	}

	if st := mustStats(t, store, repository.RetryStepBills, now); st != (repository.RetryStats{Due: 3, Waiting: 1}) {
		t.Errorf("RetryStats = %+v, want 3 due, 1 waiting", st)
	}
	if st := mustStats(t, store, repository.RetryStepGovInfo, now); st != (repository.RetryStats{Due: 1}) {
		t.Errorf("govinfo RetryStats = %+v, want 1 due", st)
	}
}

func TestClearItemRetries(t *testing.T) {
	store := newSyncStateStore(t)
	ctx := t.Context()
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	for _, id := range []string{"hr-119-1", "hr-119-2", "hr-119-3"} {
		recordFailure(t, store, repository.ItemFailure{
			Step: repository.RetryStepBills, Congress: 119, ItemID: id, Error: "x", FailedAt: now.Add(-time.Hour),
		})
	}

	if err := store.ClearItemRetries(ctx, repository.RetryStepBills, 119, nil); err != nil {
		t.Fatalf("clearing nothing: %v", err)
	}
	// hr-119-7 has no row and is ignored.
	if err := store.ClearItemRetries(ctx, repository.RetryStepBills, 119,
		[]string{"hr-119-1", "hr-119-3", "hr-119-7"}); err != nil {
		t.Fatal(err)
	}

	items, err := store.DueRetries(ctx, repository.RetryStepBills, 119, now, 100)
	if err != nil {
		t.Fatal(err)
	}
	if got := itemIDs(items); strings.Join(got, ",") != "hr-119-2" {
		t.Errorf("after clear: %v, want hr-119-2", got)
	}

	// A cleared item that fails again starts over at one attempt.
	recordFailure(t, store, repository.ItemFailure{
		Step: repository.RetryStepBills, Congress: 119, ItemID: "hr-119-1", Error: "x", FailedAt: now,
	})
	if it := dueItem(t, store, repository.RetryStepBills, "hr-119-1", now.Add(time.Hour)); it.Attempts != 1 {
		t.Errorf("attempts after clear and fail = %d, want 1", it.Attempts)
	}
}

// last_error keeps at most 1 KiB, cut on a rune boundary.
func TestRecordItemFailure_TruncatesError(t *testing.T) {
	store := newSyncStateStore(t)
	now := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)
	long := "a" + strings.Repeat("é", repository.MaxRetryErrorBytes) // 1 + 2 KiB bytes

	recordFailure(t, store, repository.ItemFailure{
		Step: repository.RetryStepGovInfo, Congress: 119, ItemID: "BILLS-119hr1ih", Error: long, FailedAt: now,
	})
	it := dueItem(t, store, repository.RetryStepGovInfo, "BILLS-119hr1ih", now.Add(time.Hour))
	if len(it.LastError) > repository.MaxRetryErrorBytes {
		t.Errorf("len(LastError) = %d, want at most %d", len(it.LastError), repository.MaxRetryErrorBytes)
	}
	if want := long[:repository.MaxRetryErrorBytes-1]; it.LastError != want {
		t.Errorf("LastError is %d bytes, want the first %d (a whole number of runes)", len(it.LastError), len(want))
	}
}

// With no rows, RetryStats is all zeros rather than an error.
func TestRetryStats_Empty(t *testing.T) {
	store := newSyncStateStore(t)
	if st := mustStats(t, store, repository.RetryStepBills, time.Now()); st != (repository.RetryStats{}) {
		t.Errorf("RetryStats = %+v, want zeros", st)
	}
}

func itemIDs(items []repository.RetryItem) []string {
	ids := make([]string, 0, len(items))
	for _, it := range items {
		ids = append(ids, it.ItemID)
	}
	return ids
}
