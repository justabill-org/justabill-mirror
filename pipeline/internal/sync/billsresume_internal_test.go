package sync

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strconv"
	gosync "sync"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/repository"
)

// resumeStore is billSyncStore with what Spanner keeps across runs: each bill's synced time
// (MarkBillSynced at the fake clock now) and the bills step's sync_state row, where a
// checkpoint sets last_offset and a success clears it and moves the watermark. cancelAfter
// cancels the run once that many bills are marked, as a timeout would.
type resumeStore struct {
	*billSyncStore

	mu          gosync.Mutex
	now         time.Time
	syncedAt    map[string]time.Time
	row         repository.SyncStateRow
	successes   []repository.SyncRun
	marks       int
	cancelAfter int
	cancel      context.CancelFunc
}

func newResumeStore() *resumeStore {
	return &resumeStore{
		billSyncStore: &billSyncStore{},
		syncedAt:      map[string]time.Time{},
		row:           repository.SyncStateRow{Step: stepBills, Congress: 119},
	}
}

func (f *resumeStore) clock() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *resumeStore) MarkBillSynced(_ context.Context, billID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.syncedAt[billID] = f.now
	f.marks++
	if f.cancel != nil && f.marks == f.cancelAfter {
		f.cancel()
	}
	return nil
}

func (f *resumeStore) ListBillIDsSyncedSince(_ context.Context, _ int, since time.Time) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var ids []string
	for id, at := range f.syncedAt {
		if !at.Before(since) {
			ids = append(ids, id)
		}
	}
	return ids, nil
}

func (f *resumeStore) GetSyncState(context.Context, string, int) (*repository.SyncStateRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	row := f.row
	return &row, nil
}

func (f *resumeStore) SaveSyncCheckpoint(_ context.Context, _ string, _ int, offset *string, items int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.row.LastOffset = offset
	f.row.ItemsSynced = items
	f.checkpoints = append(f.checkpoints, *offset)
	return nil
}

func (f *resumeStore) RecordSyncSuccess(_ context.Context, run repository.SyncRun) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.successes = append(f.successes, run)
	f.row.LastSyncedAt = run.StartedAt
	if !run.Watermark.IsZero() {
		f.row.LastSyncedAt = run.Watermark
	}
	f.row.LastOffset = nil
	f.row.ConsecutiveFailures = 0
	return nil
}

func (f *resumeStore) RecordSyncFailure(context.Context, repository.SyncRun) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.row.ConsecutiveFailures++
	return nil
}

// runBills runs SyncBills once at the store's time now against a fresh copy of a 20-bill
// list, and returns its error and the bills it fetched details for.
func (f *resumeStore) runBills(ctx context.Context, t *testing.T, now time.Time) (map[string]int, error) {
	t.Helper()
	f.mu.Lock()
	f.now = now
	f.mu.Unlock()
	api := &billAPI{n: 20}
	s := newBillSyncService(t, f, api)
	s.clock = f.clock
	err := s.SyncBills(ctx, 119, 0)
	return api.details, err
}

// The acceptance test for #459: a run that times out after N bills, then the next run skips
// the bills synced since the stuck run started, syncs the rest, and records the stuck run's
// start as the watermark. The run after that continues nothing.
func TestSyncBills_ContinuesATimedOutRun(t *testing.T) {
	store := newResumeStore()
	stuck := time.Date(2026, 10, 1, 8, 0, 0, 0, time.UTC)

	ctx, cancel := context.WithCancel(t.Context())
	store.cancel, store.cancelAfter = cancel, 5
	if _, err := store.runBills(ctx, t, stuck); !errors.Is(err, context.Canceled) {
		t.Fatalf("first run = %v, want it cut off", err)
	}
	cancel()
	firstRun := slices.Sorted(maps.Keys(store.syncedAt))
	if len(firstRun) < 5 || len(firstRun) >= 20 {
		t.Fatalf("first run synced %v, want at least 5 and not all", firstRun)
	}
	if want := stuck.Format(time.RFC3339Nano); !slices.Equal(store.checkpoints, []string{want}) {
		t.Fatalf("checkpoints = %v, want [%s]: the first run saves its start before syncing", store.checkpoints, want)
	}

	store.cancel = nil
	details, err := store.runBills(t.Context(), t, stuck.Add(4*time.Hour))
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	for _, id := range firstRun {
		if n := details[billNumber(id)]; n != 0 {
			t.Errorf("second run fetched %s again (%d times), want it skipped", id, n)
		}
	}
	if len(details) != 20-len(firstRun) || len(store.syncedAt) != 20 {
		t.Errorf("second run fetched %d bills (%v), want the other %d; synced overall %d, want 20",
			len(details), details, 20-len(firstRun), len(store.syncedAt))
	}
	if len(store.successes) != 1 || !store.successes[0].Watermark.Equal(stuck) ||
		store.successes[0].ItemsSynced != 20-len(firstRun) {
		t.Fatalf("successes = %+v, want one with watermark %v and %d items", store.successes, stuck, 20-len(firstRun))
	}
	if len(store.checkpoints) != 1 || store.row.LastOffset != nil || !store.row.LastSyncedAt.Equal(stuck) {
		t.Errorf("checkpoints %v, last_offset %v, watermark %v; want no new checkpoint, cleared, %v",
			store.checkpoints, store.row.LastOffset, store.row.LastSyncedAt, stuck)
	}

	next := stuck.Add(8 * time.Hour)
	details, err = store.runBills(t.Context(), t, next)
	if err != nil {
		t.Fatalf("third run: %v", err)
	}
	if len(details) != 20 || !store.successes[1].Watermark.IsZero() {
		t.Errorf("third run fetched %d bills with watermark %v, want all 20 and its own start", len(details),
			store.successes[1].Watermark)
	}
	if got := store.checkpoints[len(store.checkpoints)-1]; got != next.Format(time.RFC3339Nano) {
		t.Errorf("third run saved %q, want its own start %v", got, next)
	}
}

// billNumber is the number in an hr-119-<n> ID, the key billAPI counts details by.
func billNumber(id string) string {
	n, _ := strconv.Atoi(id[len("hr-119-"):])
	return strconv.Itoa(n)
}

var errSyncState = errors.New("spanner: unavailable")

// syncStateErrStore fails GetSyncState.
type syncStateErrStore struct{ *billSyncStore }

func (syncStateErrStore) GetSyncState(context.Context, string, int) (*repository.SyncStateRow, error) {
	return nil, errSyncState
}

func TestBillsResumeSince(t *testing.T) {
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	saved := now.Add(-6 * time.Hour)
	explicit := now.Add(-24 * time.Hour)
	savedRow := &repository.SyncStateRow{LastOffset: new(saved.Format(time.RFC3339Nano)), ItemsSynced: 7}
	tests := []struct {
		name     string
		state    *repository.SyncStateRow
		force    bool
		limited  bool
		explicit time.Time
		want     time.Time
		wantSave []string
	}{
		{name: "no row saves the run's start", want: time.Time{}, wantSave: []string{now.Format(time.RFC3339Nano)}},
		{name: "a saved start is continued", state: savedRow, want: saved},
		{name: "an unreadable offset is ignored", state: &repository.SyncStateRow{LastOffset: new("hr-119-3")},
			wantSave: []string{now.Format(time.RFC3339Nano)}},
		{name: "a limited run continues but saves nothing", state: savedRow, limited: true, want: saved},
		{name: "a limited run with nothing saved saves nothing", limited: true},
		{name: "a forced run re-syncs everything", state: savedRow, force: true},
		{name: "an explicit time wins and is saved", explicit: explicit, force: true, want: explicit,
			wantSave: []string{explicit.Format(time.RFC3339Nano)}},
		{name: "an explicit time keeps a saved one", state: savedRow, explicit: explicit, want: explicit},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &billSyncStore{state: tt.state}
			s := newBillSyncService(t, store, &billAPI{})
			s.clock = func() time.Time { return now }
			s.SetForceSync(tt.force)
			s.SetResumeSince(tt.explicit)

			got, err := s.billsResumeSince(t.Context(), 119, tt.limited)
			if err != nil {
				t.Fatal(err)
			}
			if !got.Equal(tt.want) {
				t.Errorf("resume = %v, want %v", got, tt.want)
			}
			if !slices.Equal(store.checkpoints, tt.wantSave) {
				t.Errorf("checkpoints = %v, want %v", store.checkpoints, tt.wantSave)
			}
		})
	}
}

func TestSyncBills_SyncStateReadErrorFailsTheRun(t *testing.T) {
	inner := &billSyncStore{}
	api := &billAPI{n: 2}
	s := newBillSyncService(t, syncStateErrStore{inner}, api)

	if err := s.SyncBills(t.Context(), 119, 0); !errors.Is(err, errSyncState) {
		t.Fatalf("SyncBills = %v, want the sync_state error", err)
	}
	if len(api.details) != 0 {
		t.Errorf("detail requests = %v, want none", api.details)
	}
}
