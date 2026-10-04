package spannerdb_test

import (
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

func newSyncStateStore(t *testing.T) *spannerdb.PipelineStoreImpl {
	t.Helper()
	return spannerdb.NewPipelineStore(&spannerdb.Client{Spanner: testdb.New(t)})
}

func mustSyncState(t *testing.T, store *spannerdb.PipelineStoreImpl, step string) repository.SyncStateRow {
	t.Helper()
	st, err := store.GetSyncState(t.Context(), step, 119)
	if err != nil {
		t.Fatal(err)
	}
	if st == nil {
		t.Fatalf("no sync_state row for %s", step)
	}
	return *st
}

func TestGetSyncState_MissingRow(t *testing.T) {
	store := newSyncStateStore(t)
	st, err := store.GetSyncState(t.Context(), "bills", 119)
	if err != nil || st != nil {
		t.Fatalf("GetSyncState = %+v, %v; want nil, nil", st, err)
	}
}

// A failure before the first success creates a row with a NULL watermark, which reads back
// as the zero time.
func TestRecordSyncFailure_NoRow(t *testing.T) {
	store := newSyncStateStore(t)
	started := time.Date(2026, 9, 27, 10, 0, 0, 0, time.UTC)

	if err := store.RecordSyncFailure(t.Context(), repository.SyncRun{
		Step: "bills", Congress: 119, StartedAt: started, Error: "list bills: EOF",
	}); err != nil {
		t.Fatal(err)
	}

	st := mustSyncState(t, store, "bills")
	if !st.LastSyncedAt.IsZero() {
		t.Errorf("LastSyncedAt = %v, want zero (never succeeded)", st.LastSyncedAt)
	}
	if st.ErrorCount != 1 || st.ConsecutiveFailures != 1 {
		t.Errorf("ErrorCount, ConsecutiveFailures = %d, %d; want 1, 1", st.ErrorCount, st.ConsecutiveFailures)
	}
	if st.LastError == nil || *st.LastError != "list bills: EOF" {
		t.Errorf("LastError = %v, want %q", st.LastError, "list bills: EOF")
	}
	if st.LastErrorAt.IsZero() {
		t.Error("LastErrorAt not set")
	}
	if !st.LastAttemptAt.Equal(started) {
		t.Errorf("LastAttemptAt = %v, want %v", st.LastAttemptAt, started)
	}
}

// Success keeps the error history and resets only the streak; a later failure keeps the
// watermark.
func TestRecordSync_SuccessAndFailureKeepEachOthersColumns(t *testing.T) {
	store := newSyncStateStore(t)
	ctx := t.Context()
	base := time.Date(2026, 9, 27, 8, 0, 0, 0, time.UTC)

	for i := range 3 {
		if err := store.RecordSyncFailure(ctx, repository.SyncRun{
			Step: "votes", Congress: 119, StartedAt: base.Add(time.Duration(i) * time.Minute), Error: "house: 503",
		}); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.SaveSyncCheckpoint(ctx, "votes", 119, new("hr-119-10"), 10); err != nil {
		t.Fatal(err)
	}

	started := base.Add(time.Hour)
	if err := store.RecordSyncSuccess(ctx, repository.SyncRun{
		Step: "votes", Congress: 119, StartedAt: started, ItemsSynced: 42,
	}); err != nil {
		t.Fatal(err)
	}
	st := mustSyncState(t, store, "votes")
	if !st.LastSyncedAt.Equal(started) || !st.LastAttemptAt.Equal(started) {
		t.Errorf("LastSyncedAt, LastAttemptAt = %v, %v; want the run's start %v",
			st.LastSyncedAt, st.LastAttemptAt, started)
	}
	if st.ItemsSynced != 42 || st.ConsecutiveFailures != 0 || st.LastOffset != nil {
		t.Errorf("after success: ItemsSynced %d, ConsecutiveFailures %d, LastOffset %v; want 42, 0, nil",
			st.ItemsSynced, st.ConsecutiveFailures, st.LastOffset)
	}
	if st.ErrorCount != 3 || st.LastError == nil || *st.LastError != "house: 503" || st.LastErrorAt.IsZero() {
		t.Errorf("success erased the error history: %+v", st)
	}

	failedAt := started.Add(time.Hour)
	if err := store.RecordSyncFailure(ctx, repository.SyncRun{
		Step: "votes", Congress: 119, StartedAt: failedAt, Error: "senate: timeout",
	}); err != nil {
		t.Fatal(err)
	}
	st = mustSyncState(t, store, "votes")
	if !st.LastSyncedAt.Equal(started) {
		t.Errorf("failure moved the watermark to %v, want %v", st.LastSyncedAt, started)
	}
	if st.ErrorCount != 4 || st.ConsecutiveFailures != 1 || st.ItemsSynced != 42 {
		t.Errorf("after failure: ErrorCount %d, ConsecutiveFailures %d, ItemsSynced %d; want 4, 1, 42",
			st.ErrorCount, st.ConsecutiveFailures, st.ItemsSynced)
	}
	if !st.LastAttemptAt.Equal(failedAt) {
		t.Errorf("LastAttemptAt = %v, want %v", st.LastAttemptAt, failedAt)
	}
}

// A checkpoint on a new row leaves the watermark NULL and touches only its own columns.
func TestSaveSyncCheckpoint(t *testing.T) {
	store := newSyncStateStore(t)
	ctx := t.Context()

	if err := store.SaveSyncCheckpoint(ctx, "links", 119, new("hr-119-250"), 250); err != nil {
		t.Fatal(err)
	}
	st := mustSyncState(t, store, "links")
	if st.LastOffset == nil || *st.LastOffset != "hr-119-250" || st.ItemsSynced != 250 {
		t.Errorf("checkpoint = %v, %d; want hr-119-250, 250", st.LastOffset, st.ItemsSynced)
	}
	if !st.LastSyncedAt.IsZero() || !st.LastAttemptAt.IsZero() || st.ErrorCount != 0 {
		t.Errorf("checkpoint wrote other columns: %+v", st)
	}

	if err := store.SaveSyncCheckpoint(ctx, "links", 119, nil, 300); err != nil {
		t.Fatal(err)
	}
	if st = mustSyncState(t, store, "links"); st.LastOffset != nil || st.ItemsSynced != 300 {
		t.Errorf("cleared checkpoint = %v, %d; want nil, 300", st.LastOffset, st.ItemsSynced)
	}
}

// A success with a warning sets last_error and last_error_at, and it is still a success: the
// failure counts stay where they were and the watermark moves.
func TestRecordSyncSuccess_Warning(t *testing.T) {
	store := newSyncStateStore(t)
	ctx := t.Context()
	started := time.Date(2026, 9, 27, 12, 0, 0, 0, time.UTC)

	if err := store.RecordSyncFailure(ctx, repository.SyncRun{
		Step: "votes", Congress: 119, StartedAt: started.Add(-time.Hour), Error: "senate: 503",
	}); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordSyncSuccess(ctx, repository.SyncRun{
		Step: "votes", Congress: 119, StartedAt: started, ItemsSynced: 4, Warning: "unmatched senators: S409",
	}); err != nil {
		t.Fatal(err)
	}

	st := mustSyncState(t, store, "votes")
	if !st.LastSyncedAt.Equal(started) || st.ItemsSynced != 4 {
		t.Errorf("LastSyncedAt, ItemsSynced = %v, %d; want %v, 4", st.LastSyncedAt, st.ItemsSynced, started)
	}
	if st.LastError == nil || *st.LastError != "unmatched senators: S409" {
		t.Errorf("LastError = %v, want the warning", st.LastError)
	}
	if st.ErrorCount != 1 || st.ConsecutiveFailures != 0 {
		t.Errorf("ErrorCount, ConsecutiveFailures = %d, %d; want 1, 0", st.ErrorCount, st.ConsecutiveFailures)
	}
	if !st.LastErrorAt.After(started) {
		t.Errorf("LastErrorAt = %v, want the time of the warning", st.LastErrorAt)
	}
}

// A run that resumed an interrupted one records that run's start as the watermark; its own
// start stays the last attempt. A later Watermark than the start is ignored.
func TestRecordSyncSuccess_EarlierWatermark(t *testing.T) {
	store := newSyncStateStore(t)
	ctx := t.Context()
	interrupted := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)
	started := interrupted.Add(4 * time.Hour)

	if err := store.SaveSyncCheckpoint(ctx, "bills", 119, new(interrupted.Format(time.RFC3339Nano)), 0); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordSyncSuccess(ctx, repository.SyncRun{
		Step: "bills", Congress: 119, StartedAt: started, Watermark: interrupted, ItemsSynced: 900,
	}); err != nil {
		t.Fatal(err)
	}
	st := mustSyncState(t, store, "bills")
	if !st.LastSyncedAt.Equal(interrupted) || !st.LastAttemptAt.Equal(started) {
		t.Errorf("LastSyncedAt, LastAttemptAt = %v, %v; want %v, %v",
			st.LastSyncedAt, st.LastAttemptAt, interrupted, started)
	}
	if st.LastOffset != nil {
		t.Errorf("LastOffset = %q after success, want nil", *st.LastOffset)
	}

	later := started.Add(4 * time.Hour)
	if err := store.RecordSyncSuccess(ctx, repository.SyncRun{
		Step: "bills", Congress: 119, StartedAt: later, Watermark: later.Add(time.Hour), Warning: "w",
	}); err != nil {
		t.Fatal(err)
	}
	if st = mustSyncState(t, store, "bills"); !st.LastSyncedAt.Equal(later) {
		t.Errorf("LastSyncedAt = %v, want the run's start %v (a later Watermark is ignored)", st.LastSyncedAt, later)
	}
}
