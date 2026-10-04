package sync

import (
	"context"
	"fmt"
	"strings"

	"github.com/justabill-org/justabill/db/repository"
)

// storedTextFunc processes one stored bill text for eachStoredText.
type storedTextFunc func(ctx context.Context, src repository.LawRefSource) error

// eachStoredText calls fn for every bill text stored for one congress, in bill and version ID
// order, reading pageSize texts at a time. It starts after step's sync_state checkpoint, if
// there is one, and writes the checkpoint after every full page, so an interrupted run resumes
// where it stopped. It returns how many texts fn has handled, counting those of earlier runs.
func (s *Service) eachStoredText(
	ctx context.Context, step string, congressNum, pageSize int, fn storedTextFunc,
) (int, error) {
	state, err := s.store.GetSyncState(ctx, step, congressNum)
	if err != nil {
		return 0, fmt.Errorf("read %s checkpoint: %w", step, err)
	}
	done := 0
	afterBill, afterVersion := "", ""
	if state != nil && state.LastOffset != nil {
		afterBill, afterVersion, _ = strings.Cut(*state.LastOffset, lawRefsCursorSep)
		done = state.ItemsSynced
	}
	s.logger.InfoContext(ctx, "walking stored bill texts", "step", step, "congress", congressNum,
		"resume_after_bill", afterBill, "resume_after_version", afterVersion)

	for {
		page, listErr := s.store.ListLawRefSources(ctx, congressNum, afterBill, afterVersion, pageSize)
		if listErr != nil {
			return done, fmt.Errorf("list texts after %q %q: %w", afterBill, afterVersion, listErr)
		}
		for _, src := range page {
			if ctx.Err() != nil {
				return done, ctx.Err()
			}
			if err = fn(ctx, src); err != nil {
				return done, err
			}
			done++
		}
		if len(page) < pageSize {
			return done, nil
		}
		last := page[len(page)-1]
		afterBill, afterVersion = last.BillID, last.VersionID
		cursor := afterBill + lawRefsCursorSep + afterVersion
		if err = s.store.SaveSyncCheckpoint(ctx, step, congressNum, &cursor, done); err != nil {
			return done, fmt.Errorf("save %s checkpoint: %w", step, err)
		}
		s.logger.InfoContext(ctx, "stored bill texts progress", "step", step, "congress", congressNum,
			"texts", done, "after_bill", afterBill)
	}
}
