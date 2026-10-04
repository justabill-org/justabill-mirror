package sync

import (
	"context"
	"fmt"
	"time"

	"github.com/justabill-org/justabill/db/repository"
)

// billsResumeSince returns the time a bills run continues from: it skips the bills marked
// synced at or after it, and records it as the watermark when it succeeds (#459). That is the
// SetResumeSince time if set, else the start of the interrupted run this one continues, which
// sync_state.last_offset holds for the bills step. Zero means the run continues nothing.
//
// A run that continues no saved run saves its own start (or the SetResumeSince time) there
// before it syncs anything, so if it times out or the process dies, the next run picks up
// where it stopped instead of walking the same list again; a success clears it. Limited runs
// never save one (their success records nothing), and a forced run without SetResumeSince
// neither reads nor saves one: it re-syncs every listed bill.
//
// Skipping is safe for any resume time R: a skipped bill was fully synced at or after R, so
// any change Congress.gov makes to it after that is after R too, and the next run lists it
// again because the watermark is R.
func (s *Service) billsResumeSince(ctx context.Context, congressNum int, limited bool) (time.Time, error) {
	if s.forceSync && s.resumeSince.IsZero() {
		return time.Time{}, nil
	}
	st, err := s.store.GetSyncState(ctx, stepBills, congressNum)
	if err != nil {
		return time.Time{}, fmt.Errorf("get bills sync state: %w", err)
	}
	saved := s.savedBillsResume(ctx, st)
	resume := s.resumeSince
	if resume.IsZero() {
		resume = saved
	}
	if limited || !saved.IsZero() {
		return resume, nil
	}
	s.saveBillsResume(ctx, congressNum, st, resume)
	return resume, nil
}

// savedBillsResume reads the interrupted run's start from the bills step's last_offset. A
// value that isn't a time is ignored (logged), so the run lists everything as before.
func (s *Service) savedBillsResume(ctx context.Context, st *repository.SyncStateRow) time.Time {
	if st == nil || st.LastOffset == nil || *st.LastOffset == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339Nano, *st.LastOffset)
	if err != nil {
		s.logger.WarnContext(ctx, "ignoring unreadable bills resume point", "congress", st.Congress, "error", err)
		return time.Time{}
	}
	return t
}

// saveBillsResume saves where the next run continues from if this one doesn't finish: resume,
// or the current time when resume is zero. It keeps items_synced. A failed write is logged
// and doesn't fail the run: the next run then can't continue this one and walks the whole
// list, as it did before #459.
func (s *Service) saveBillsResume(
	ctx context.Context, congressNum int, st *repository.SyncStateRow, resume time.Time,
) {
	if resume.IsZero() {
		resume = s.now()
	}
	items := 0
	if st != nil {
		items = st.ItemsSynced
	}
	offset := resume.UTC().Format(time.RFC3339Nano)
	if err := s.store.SaveSyncCheckpoint(ctx, stepBills, congressNum, &offset, items); err != nil {
		s.logger.WarnContext(ctx, "bills resume point not saved", "congress", congressNum, "error", err)
	}
}
