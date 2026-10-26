package sync

import (
	"context"
	"fmt"
)

const (
	// stepRecordedVotes is the sync_state step of BackfillRecordedVotes, which holds its
	// checkpoint.
	stepRecordedVotes = "recorded_votes"

	// DefaultRecordedVotesPageSize is how many bills BackfillRecordedVotes lists per page, and
	// so how often it checkpoints. Each bill is one Congress.gov request or a few.
	DefaultRecordedVotesPageSize = 50
)

// recordedVotesCounts totals one BackfillRecordedVotes run.
type recordedVotesCounts struct {
	bills  int
	stored int
	failed int
}

// BackfillRecordedVotes fetches the actions of each stored bill a congress's recorded roll calls
// name again, and replaces them as the bills sync does (syncBillActions: the actions through
// [BillActionRows], their unrecorded passages and the status history), so actions stored before
// the sync kept bill_actions.recorded_vote get their roll-call links (#1046). It requests only
// /bill/{congress}/{type}/{number}/actions and leaves updated_at alone: the bill isn't fully
// synced. Each stored bill is marked, so the API's cached copy is dropped and the web app told.
//
// A bill whose fetch or write fails is logged and the others still run; the run then fails,
// naming how many failed. It pages by bill_id and checkpoints in sync_state (step
// "recorded_votes"), so an interrupted run resumes after the last page finished before any bill
// failed. A run that reached the end with failures clears the checkpoint, so a rerun does every
// bill again, the failed ones included: it is idempotent, since each bill's actions are replaced.
func (s *Service) BackfillRecordedVotes(ctx context.Context, congressNum, pageSize int) error {
	if pageSize <= 0 {
		pageSize = DefaultRecordedVotesPageSize
	}
	return s.runStep(ctx, stepRecordedVotes, congressNum, false, func(ctx context.Context) (int, error) {
		return s.backfillRecordedVotes(ctx, congressNum, pageSize)
	})
}

func (s *Service) backfillRecordedVotes(ctx context.Context, congressNum, pageSize int) (int, error) {
	state, err := s.store.GetSyncState(ctx, stepRecordedVotes, congressNum)
	if err != nil {
		return 0, fmt.Errorf("read recorded votes checkpoint: %w", err)
	}
	var c recordedVotesCounts
	after := ""
	if state != nil && state.LastOffset != nil {
		after = *state.LastOffset
		c.bills = state.ItemsSynced
	}
	s.logger.InfoContext(ctx, "fetching voted bills' actions again", "congress", congressNum,
		"resume_after", after)

	for {
		ids, listErr := s.store.ListVotedBillIDs(ctx, congressNum, after, pageSize)
		if listErr != nil {
			return c.bills, fmt.Errorf("list voted bills after %q: %w", after, listErr)
		}
		for _, id := range ids {
			if ctx.Err() != nil {
				return c.bills, ctx.Err()
			}
			s.refetchBillActions(ctx, id, &c)
		}
		if len(ids) < pageSize {
			break
		}
		after = ids[len(ids)-1]
		if c.failed > 0 {
			// Past a failed bill, the checkpoint stays put: a resumed run must retry it.
			continue
		}
		if err = s.store.SaveSyncCheckpoint(ctx, stepRecordedVotes, congressNum, &after, c.bills); err != nil {
			return c.bills, fmt.Errorf("save recorded votes checkpoint: %w", err)
		}
	}

	s.logger.InfoContext(ctx, "voted bills' actions fetched again", "congress", congressNum,
		"bills", c.bills, "stored", c.stored, "failed", c.failed)
	if c.failed == 0 {
		return c.bills, nil
	}
	// The run reached the end, so a rerun starts over rather than after the failed bills' pages.
	if err = s.store.SaveSyncCheckpoint(ctx, stepRecordedVotes, congressNum, nil, c.bills); err != nil {
		return c.bills, fmt.Errorf("clear recorded votes checkpoint: %w", err)
	}
	return c.bills, fmt.Errorf("%d of %d voted bills failed; rerun to retry them", c.failed, c.bills)
}

// refetchBillActions fetches and stores one bill's actions, counting the outcome in c.
func (s *Service) refetchBillActions(ctx context.Context, id string, c *recordedVotesCounts) {
	c.bills++
	congressNum, billType, number, err := parseBillID(id)
	if err == nil {
		err = s.syncBillActions(ctx, id, congressNum, billType, number)
	}
	if err != nil {
		c.failed++
		s.logger.WarnContext(ctx, "refetch bill actions failed", "bill_id", id, "error", err)
		return
	}
	c.stored++
	markBill(ctx, id)
}
