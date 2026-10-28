package sync

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
)

// RefreshPassageRanks records the congress and answers with rankWritten, or fails with rankErr.
func (f *voteStore) RefreshPassageRanks(_ context.Context, congress int) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rankCalls = append(f.rankCalls, congress)
	if f.rankErr != nil {
		return 0, f.rankErr
	}
	return f.rankWritten, nil
}

// Every run ends by recomputing the congress's passage ranks (#1090), whether or not it stored new
// roll calls, and logs how many changed.
func TestSyncVotes_RefreshesPassageRanks(t *testing.T) {
	store := newVoteStore(session1IDs()...)
	store.rankWritten = 4
	s, logs := lisSyncService(t, store, the119th())

	if err := s.SyncVotes(t.Context(), 119, []int{1}); err != nil {
		t.Fatal(err)
	}

	if !slices.Equal(store.rankCalls, []int{119}) {
		t.Errorf("refreshed passage ranks of congresses %v, want [119]", store.rankCalls)
	}
	wantFields(t, onlyLogLine(t, logs, "vote sync finished"), "passage_ranks=4")
}

// A failed refresh fails the step with the cause; the attendance step before it still ran.
func TestSyncVotes_PassageRanksFail(t *testing.T) {
	store := newVoteStore()
	store.roster = storedSenators()
	store.rankErr = errors.New("spanner unavailable")
	s, logs := lisSyncService(t, store, the119th())

	err := s.SyncVotes(t.Context(), 119, []int{1})
	if !errors.Is(err, store.rankErr) || !strings.Contains(err.Error(), "refresh passage ranks") {
		t.Fatalf("SyncVotes error %v, want the refresh's error in refresh passage ranks", err)
	}
	if store.success != nil || len(store.failures) != 1 {
		t.Errorf("sync success %+v, failures %+v, want one failure", store.success, store.failures)
	}
	if _, ok := store.attendance[119]; !ok {
		t.Error("attendance not written; want it computed before the ranks")
	}
	wantFields(t, onlyLogLine(t, logs, "vote sync finished"), "passage_ranks=0")
}

// A failed attendance write doesn't keep the ranks from being refreshed.
func TestSyncVotes_PassageRanksAfterAttendanceFails(t *testing.T) {
	store := newVoteStore()
	store.replaceErr = errors.New("spanner unavailable")
	s, _ := lisSyncService(t, store, the119th())

	if err := s.SyncVotes(t.Context(), 119, []int{1}); !errors.Is(err, store.replaceErr) {
		t.Fatalf("SyncVotes error %v, want the attendance write's", err)
	}
	if !slices.Equal(store.rankCalls, []int{119}) {
		t.Errorf("refreshed passage ranks of congresses %v, want [119]", store.rankCalls)
	}
}

// A cancelled run fails anyway: it doesn't refresh the ranks.
func TestSyncVotes_CancelledRunSkipsPassageRanks(t *testing.T) {
	store := newVoteStore()
	s, _ := lisSyncService(t, store, the119th())
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := s.SyncVotes(ctx, 119, []int{1}); err == nil {
		t.Fatal("SyncVotes succeeded on a cancelled context")
	}
	if len(store.rankCalls) != 0 {
		t.Errorf("refreshed passage ranks %v on a cancelled run, want none", store.rankCalls)
	}
}
