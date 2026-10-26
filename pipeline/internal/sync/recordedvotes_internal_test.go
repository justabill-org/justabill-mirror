package sync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
)

// recordedVotesStore is a PipelineStore fake for BackfillRecordedVotes. ListVotedBillIDs pages
// through voted (sorted) as the Spanner query does; the writes keep each bill's last actions.
// Any other method panics through the nil embedded interface.
type recordedVotesStore struct {
	repository.PipelineStore

	voted   []string
	listErr error
	state   *repository.SyncStateRow // what GetSyncState returns (nil: no row)

	actions     map[string][]repository.BillActionRow
	afters      []string // every ListVotedBillIDs afterBillID, in order
	checkpoints []string // every SaveSyncCheckpoint offset, "<nil>" for a cleared one
	success     *repository.SyncRun
	failure     *repository.SyncRun
}

func (f *recordedVotesStore) ListVotedBillIDs(_ context.Context, _ int, after string, limit int) ([]string, error) {
	f.afters = append(f.afters, after)
	if f.listErr != nil {
		return nil, f.listErr
	}
	var page []string
	for _, id := range f.voted {
		if id > after && len(page) < limit {
			page = append(page, id)
		}
	}
	return page, nil
}

func (f *recordedVotesStore) ReplaceBillActions(
	_ context.Context,
	billID string,
	rows []repository.BillActionRow,
) error {
	if f.actions == nil {
		f.actions = map[string][]repository.BillActionRow{}
	}
	f.actions[billID] = rows
	return nil
}

func (f *recordedVotesStore) ReplaceBillStatus(
	context.Context, string, string, *time.Time, []repository.BillStatusRow,
) error {
	return nil
}

func (f *recordedVotesStore) UpsertCongressionalVote(context.Context, repository.CongressionalVoteRow) error {
	return nil
}

func (f *recordedVotesStore) GetSyncState(context.Context, string, int) (*repository.SyncStateRow, error) {
	return f.state, nil
}

func (f *recordedVotesStore) SaveSyncCheckpoint(_ context.Context, step string, _ int, offset *string, _ int) error {
	if step != stepRecordedVotes {
		return fmt.Errorf("checkpoint for step %q", step)
	}
	if offset == nil {
		f.checkpoints = append(f.checkpoints, "<nil>")
		return nil
	}
	f.checkpoints = append(f.checkpoints, *offset)
	return nil
}

func (f *recordedVotesStore) RecordSyncSuccess(_ context.Context, run repository.SyncRun) error {
	f.success = &run
	return nil
}

func (f *recordedVotesStore) RecordSyncFailure(_ context.Context, run repository.SyncRun) error {
	f.failure = &run
	return nil
}

// actionsAPI is a fake Congress.gov that records every path requested. A bill's actions are a
// referral and a passage on roll call <number>; paths in failPaths answer 500.
type actionsAPI struct {
	failPaths map[string]bool

	mu    gosync.Mutex
	paths []string
}

func (a *actionsAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	a.mu.Lock()
	a.paths = append(a.paths, r.URL.Path)
	a.mu.Unlock()
	if a.failPaths[r.URL.Path] {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	// /bill/119/hr/2/actions
	parts := strings.Split(r.URL.Path, "/")
	if len(parts) != 6 || parts[5] != "actions" {
		http.NotFound(w, r)
		return
	}
	fmt.Fprintf(w, `{"actions":[
	 {"actionDate":"2025-05-22","text":"Passed House","type":"Floor","recordedVotes":[
	  {"chamber":"House","congress":119,"date":"2025-05-22T10:00:00Z","rollNumber":%[1]s,
	   "sessionNumber":1,"url":"https://clerk.house.gov/evs/2025/roll%[1]s.xml"}]},
	 {"actionDate":"2025-01-03","text":"Referred to committee","type":"IntroReferral"}]}`, parts[4])
}

// clearedCache records the bills the step run asked to drop from the API's cache.
type clearedCache struct{ ids []string }

func (c *clearedCache) Clear(_ context.Context, billIDs []string) { c.ids = append(c.ids, billIDs...) }

func newRecordedVotesService(t *testing.T, store *recordedVotesStore, api *actionsAPI) (*Service, *clearedCache) {
	t.Helper()
	s := serviceWithAPI(t, store, api.ServeHTTP)
	cache := &clearedCache{}
	s.apiCache = cache
	return s, cache
}

// passageVote is the recorded vote actionsAPI's passage of a bill numbered n stores.
func passageVote(n int) *model.RecordedVote {
	return &model.RecordedVote{
		RollNumber: n, URL: fmt.Sprintf("https://clerk.house.gov/evs/2025/roll%d.xml", n), Chamber: "House",
	}
}

// checkStoredActions fails unless each bill in ids has actionsAPI's two actions, the passage
// linking its roll call and the referral linking none.
func checkStoredActions(t *testing.T, store *recordedVotesStore, ids map[string]int) {
	t.Helper()
	for id, n := range ids {
		rows := store.actions[id]
		if len(rows) != 2 {
			t.Errorf("%s: stored %d actions, want 2", id, len(rows))
			continue
		}
		if got, want := rows[0].RecordedVote, passageVote(n); got == nil || *got != *want {
			t.Errorf("%s: passage RecordedVote = %v, want %+v", id, got, *want)
		}
		if rows[1].RecordedVote != nil {
			t.Errorf("%s: referral RecordedVote = %+v, want nil", id, *rows[1].RecordedVote)
		}
	}
}

func TestBackfillRecordedVotes_FetchesOnlyEachVotedBillsActions(t *testing.T) {
	store := &recordedVotesStore{voted: []string{"hr-119-1", "hr-119-2", "s-119-3"}}
	api := &actionsAPI{}
	s, cache := newRecordedVotesService(t, store, api)

	if err := s.BackfillRecordedVotes(t.Context(), 119, 2); err != nil {
		t.Fatalf("BackfillRecordedVotes: %v", err)
	}

	wantPaths := []string{"/bill/119/hr/1/actions", "/bill/119/hr/2/actions", "/bill/119/s/3/actions"}
	if !slices.Equal(api.paths, wantPaths) {
		t.Errorf("requested %v, want only %v", api.paths, wantPaths)
	}
	checkStoredActions(t, store, map[string]int{"hr-119-1": 1, "hr-119-2": 2, "s-119-3": 3})
	if want := []string{"", "hr-119-2"}; !slices.Equal(store.afters, want) {
		t.Errorf("listed after %q, want %q (pages of 2)", store.afters, want)
	}
	if want := []string{"hr-119-2"}; !slices.Equal(store.checkpoints, want) {
		t.Errorf("checkpoints = %q, want %q (after each full page)", store.checkpoints, want)
	}
	if store.success == nil || store.success.Step != "recorded_votes" || store.success.ItemsSynced != 3 {
		t.Errorf("success = %+v, want step recorded_votes with 3 items", store.success)
	}
	if want := []string{"hr-119-1", "hr-119-2", "s-119-3"}; !slices.Equal(cache.ids, want) {
		t.Errorf("API cache cleared for %v, want %v", cache.ids, want)
	}
}

func TestBackfillRecordedVotes_RerunDoesTheSameWork(t *testing.T) {
	store := &recordedVotesStore{voted: []string{"hr-119-1", "hr-119-2"}}
	api := &actionsAPI{}
	s, _ := newRecordedVotesService(t, store, api)

	for run := 1; run <= 2; run++ {
		store.actions = nil
		if err := s.BackfillRecordedVotes(t.Context(), 119, 0); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		checkStoredActions(t, store, map[string]int{"hr-119-1": 1, "hr-119-2": 2})
	}
	want := []string{"/bill/119/hr/1/actions", "/bill/119/hr/2/actions"}
	if got := api.paths; !slices.Equal(got, append(slices.Clone(want), want...)) {
		t.Errorf("requested %v, want %v twice", got, want)
	}
}

func TestBackfillRecordedVotes_ResumesAfterTheCheckpoint(t *testing.T) {
	store := &recordedVotesStore{
		voted: []string{"hr-119-1", "hr-119-2", "s-119-3"},
		state: &repository.SyncStateRow{
			Step:        "recorded_votes",
			Congress:    119,
			LastOffset:  new("hr-119-2"),
			ItemsSynced: 2,
		},
	}
	api := &actionsAPI{}
	s, _ := newRecordedVotesService(t, store, api)

	if err := s.BackfillRecordedVotes(t.Context(), 119, 2); err != nil {
		t.Fatalf("BackfillRecordedVotes: %v", err)
	}

	if want := []string{"/bill/119/s/3/actions"}; !slices.Equal(api.paths, want) {
		t.Errorf("requested %v, want %v (bills after the checkpoint)", api.paths, want)
	}
	if store.success == nil || store.success.ItemsSynced != 3 {
		t.Errorf("success = %+v, want 3 items (2 before the checkpoint, 1 now)", store.success)
	}
}

func TestBackfillRecordedVotes_AFailedBillDoesNotStopTheOthers(t *testing.T) {
	store := &recordedVotesStore{voted: []string{"hr-119-1", "hr-119-2", "s-119-3"}}
	api := &actionsAPI{failPaths: map[string]bool{"/bill/119/hr/1/actions": true}}
	s, cache := newRecordedVotesService(t, store, api)

	err := s.BackfillRecordedVotes(t.Context(), 119, 1)

	if err == nil || !strings.Contains(err.Error(), "1 of 3 voted bills failed") {
		t.Fatalf("BackfillRecordedVotes = %v, want an error naming 1 of 3 failed", err)
	}
	if _, stored := store.actions["hr-119-1"]; stored {
		t.Error("stored actions for hr-119-1, whose fetch failed")
	}
	checkStoredActions(t, store, map[string]int{"hr-119-2": 2, "s-119-3": 3})
	if store.success != nil || store.failure == nil || store.failure.Step != "recorded_votes" {
		t.Errorf("success = %+v, failure = %+v; want a recorded failure only", store.success, store.failure)
	}
	// Pages of one: no checkpoint moves past the failed bill, and the finished run clears it,
	// so a rerun starts over and retries hr-119-1.
	if want := []string{"<nil>"}; !slices.Equal(store.checkpoints, want) {
		t.Errorf("checkpoints = %q, want %q", store.checkpoints, want)
	}
	if want := []string{"hr-119-2", "s-119-3"}; !slices.Equal(cache.ids, want) {
		t.Errorf("API cache cleared for %v, want %v (stored bills only)", cache.ids, want)
	}
}

func TestBackfillRecordedVotes_ListFailureKeepsTheCheckpoint(t *testing.T) {
	errList := errors.New("spanner down")
	store := &recordedVotesStore{listErr: errList}
	s, _ := newRecordedVotesService(t, store, &actionsAPI{})

	if err := s.BackfillRecordedVotes(t.Context(), 119, 2); !errors.Is(err, errList) {
		t.Fatalf("BackfillRecordedVotes = %v, want %v", err, errList)
	}
	if store.checkpoints != nil || store.failure == nil {
		t.Errorf("checkpoints = %q, failure = %+v; want no checkpoint write and a recorded failure",
			store.checkpoints, store.failure)
	}
}
