package sync

import (
	"context"
)

// maxPassedSummaryPasses bounds SyncPassedSummaries' passes. A bill's failed attempt holds it back
// for an hour, so a pass never returns the same bills twice; this only guards against a queue
// that, against that rule, keeps returning bills it can't summarize. At the default batch of 200
// it's 20,000 bills, far more than any congress passes.
const maxPassedSummaryPasses = 100

// SyncPassedSummaries summarizes the congress's laws and the bills that passed at least one
// chamber, skipping any whose summary is current (#660). It's the launch load's summaries phase.
// Unlike [Service.SyncSummaries], it runs pass after pass of the job's batch until none of those
// bills is due, limit attempts were made (0 is no limit), or the day's cap is spent. It writes no
// diff summaries: serve does those.
func (s *Service) SyncPassedSummaries(ctx context.Context, congressNum, limit int) error {
	if s.summarizer == nil {
		s.logger.InfoContext(ctx, "summarizer not configured, skipping passed bill summaries")
		return nil
	}
	return s.runStep(ctx, stepPassedSummaries, congressNum, limit > 0, func(ctx context.Context) (int, error) {
		return s.syncPassedSummaries(ctx, congressNum, limit)
	})
}

func (s *Service) syncPassedSummaries(ctx context.Context, congressNum, limit int) (int, error) {
	summarized, attempted, passes := 0, 0, 0
	for passes < maxPassedSummaryPasses {
		if err := ctx.Err(); err != nil {
			return summarized, err
		}
		now := s.now().UTC()
		remaining, err := s.summaryBudget(ctx, congressNum, now)
		if err != nil {
			return summarized, err
		}
		q := s.summaryQuery(congressNum, now)
		q.PassedChamber = true
		if passes == 0 {
			s.logBacklog(ctx, q)
		}
		q.Limit = min(s.summaryJob.Batch, remaining)
		if limit > 0 {
			q.Limit = min(q.Limit, limit-attempted)
		}
		if q.Limit <= 0 {
			break
		}
		passes++
		ok, tried, err := s.syncBillSummaries(ctx, q)
		summarized += ok
		attempted += tried
		if err != nil {
			return summarized, err
		}
		if tried == 0 {
			break
		}
	}
	s.logger.InfoContext(ctx, "passed bill summaries synced", "congress", congressNum,
		"summarized", summarized, "attempted", attempted, "passes", passes)
	return summarized, nil
}
