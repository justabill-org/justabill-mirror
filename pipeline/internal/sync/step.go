package sync

import (
	"context"
	"time"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/redact"
)

// Step names: the sync_state.step of each sync step.
const (
	stepMembers = "members"
	// stepMemberTerms is a past congress's term load from congress-legislators.
	stepMemberTerms = "member_terms"
	stepBills       = "bills"
	stepVotes       = "votes"
	// stepVotedBills loads the bills stored roll calls name that aren't stored yet.
	stepVotedBills = "voted_bills"
	stepTexts      = "texts"
	stepSummaries  = "summaries"
	// stepPassedSummaries is the launch load's summaries of laws and bills that passed a chamber.
	stepPassedSummaries = "passed_summaries"
	stepGovInfo         = "govinfo"
	stepGAOReports      = "gao_reports"
	// stepCRSSummaries is the CRS summary sync from /summaries/{congress}.
	stepCRSSummaries = "crs_summaries"
	// stepCRARules matches CRA resolutions to Federal Register documents (design 590).
	stepCRARules = "cra_rules"
)

// stepRecordTimeout bounds the sync_state write that records a failed run. It runs on a
// context detached from the run's, so a timeout or shutdown is still recorded.
const stepRecordTimeout = 5 * time.Second

// stepFunc is one run of a sync step. It returns how many items it synced.
type stepFunc func(ctx context.Context) (int, error)

// stepOutcome is what one run of a step did: how many items it synced and, for a run that
// succeeded with a problem worth seeing without the logs, a warning for sync_state.last_error.
// A run that resumed an interrupted one sets watermark to that run's start, so its success
// covers the changes since then (repository.SyncRun.Watermark); zero means the run's own start.
type stepOutcome struct {
	items     int
	warning   string
	watermark time.Time
}

// outcomeFunc is one run of a sync step that can succeed with a warning.
type outcomeFunc func(ctx context.Context) (stepOutcome, error)

// runStep runs one sync step and records the outcome in sync_state, so every step behaves the
// same in serve and backfill (docs/design/80-pipeline-operability.md, "sync_state semantics"):
//   - Success is recorded only when fn returned no error and ctx is still live. A cancelled
//     run can return nil after skipping work (workerPool stops early), so ctx.Err() counts as
//     a failure.
//   - The watermark becomes the time the run started, not when it finished, so an item that
//     changed while the run was going is listed again next time. A run that resumed an
//     interrupted one (sync-bills) records that run's start instead: it skipped the items
//     synced since then, so a change made after one of them synced must be listed again.
//   - A limited run (backfill --limit) didn't see everything, so it records nothing on
//     success and never moves the watermark. Its failures are recorded.
//   - Every failure is recorded, redacted and truncated, even before the first success.
//   - The bills the run marked (markBill) are sent to the web app for revalidation once fn
//     returns, whether it succeeded or not (flushChanges).
//
// Steps that skip themselves (not configured, synced recently) return before calling runStep.
func (s *Service) runStep(ctx context.Context, step string, congressNum int, limited bool, fn stepFunc) error {
	return s.runStepOutcome(ctx, step, congressNum, limited, func(ctx context.Context) (stepOutcome, error) {
		items, err := fn(ctx)
		return stepOutcome{items: items}, err
	})
}

// runStepOutcome is runStep for a step that can succeed with a warning. The warning is logged
// and, for an unlimited run, stored with the success (repository.SyncRun.Warning).
func (s *Service) runStepOutcome(
	ctx context.Context, step string, congressNum int, limited bool, fn outcomeFunc,
) error {
	ctx, changes := withChangeSet(ctx)
	started := time.Now()
	out, err := fn(ctx)
	s.flushChanges(ctx, changes)
	if err == nil {
		err = ctx.Err()
	}
	if err != nil {
		s.recordFailure(ctx, repository.SyncRun{Step: step, Congress: congressNum, StartedAt: started}, err)
		return err
	}
	if out.warning != "" {
		s.logger.WarnContext(ctx, "step succeeded with a warning", "step", step, "congress", congressNum,
			"warning", out.warning)
	}
	if limited {
		s.logger.InfoContext(ctx, "limited run leaves sync_state unchanged",
			"step", step, "congress", congressNum, "items", out.items)
		return nil
	}
	return s.store.RecordSyncSuccess(ctx, repository.SyncRun{
		Step: step, Congress: congressNum, StartedAt: started, Watermark: out.watermark,
		ItemsSynced: out.items, Warning: redact.String(out.warning),
	})
}

// recordFailure writes a failed run to sync_state. A write error is logged, not returned:
// the caller returns the run's own error.
func (s *Service) recordFailure(ctx context.Context, run repository.SyncRun, err error) {
	run.Error = redact.Error(redact.URLError(err))
	recordCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), stepRecordTimeout)
	defer cancel()
	if recErr := s.store.RecordSyncFailure(recordCtx, run); recErr != nil {
		s.logger.WarnContext(ctx, "record sync failure failed", "step", run.Step, "congress", run.Congress,
			"error", recErr)
	}
}
