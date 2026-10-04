package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"google.golang.org/genai"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/obs"
	"github.com/justabill-org/justabill/obs/semconv"
	"github.com/justabill-org/justabill/pipeline/internal/ai"
	"github.com/justabill-org/justabill/pipeline/internal/ai/batchjsonl"
)

// PollSummaryBatches polls every open batch of a congress (0 for every congress), stores its
// job's state, imports the results of a finished job, and releases the bills of a job that
// failed, was cancelled or expired, or has been open longer than AI_BATCH_HOLD
// (docs/design/198-corpus-resummarization.md, "Flow", steps 2 to 4). sync-summaries runs it
// first on every run. Without AI_BATCH_BUCKET it does nothing. One batch's failure doesn't stop
// the others; the errors are joined.
func (s *Service) PollSummaryBatches(ctx context.Context, congressNum int) error {
	if !s.batchesOn() {
		return nil
	}
	ctx, changes := withChangeSet(ctx)
	defer s.flushChanges(ctx, changes)
	return s.pollSummaryBatches(ctx, congressNum)
}

func (s *Service) pollSummaryBatches(ctx context.Context, congressNum int) error {
	if !s.batchesOn() {
		return nil
	}
	open, err := s.store.OpenSummaryBatches(ctx)
	if err != nil {
		return fmt.Errorf("list open summary batches: %w", err)
	}
	var errs []error
	for _, b := range open {
		if congressNum != 0 && b.Congress != congressNum {
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if err = s.pollBatch(ctx, b); err != nil {
			errs = append(errs, fmt.Errorf("summary batch %s: %w", b.BatchID, err))
		}
	}
	return errors.Join(errs...)
}

// pollBatch moves one open batch on: it stores a changed job state, then imports, releases, or
// cancels and releases, as the state and the batch's age call for.
func (s *Service) pollBatch(ctx context.Context, b repository.SummaryBatch) error {
	now := s.now().UTC()
	if b.JobName == "" {
		// A submit that stopped before creating its job: whatever it held goes back.
		if now.Sub(b.CreatedAt) < batchExportGrace {
			return nil
		}
		return s.releaseBatch(ctx, b, now)
	}
	job, err := s.getBatchJob(ctx, b.JobName)
	if err != nil {
		return err
	}
	if b, err = s.storeJobState(ctx, b, job, now); err != nil {
		return err
	}
	switch job.State {
	case genai.JobStateSucceeded, genai.JobStatePartiallySucceeded:
		err = s.importBatch(ctx, b, job, now)
		if err != nil && now.Sub(b.CreatedAt) >= s.batches.cfg.Hold {
			// An import that still fails once the hold is over (its files gone, say) gives up.
			return errors.Join(err, s.releaseBatch(ctx, b, now))
		}
		return err
	case genai.JobStateFailed, genai.JobStateCancelled, genai.JobStateExpired:
		return s.releaseBatch(ctx, b, now)
	case genai.JobStateUnspecified, genai.JobStateQueued, genai.JobStatePending, genai.JobStateRunning,
		genai.JobStateCancelling, genai.JobStatePaused, genai.JobStateUpdating:
		return s.checkBatchHold(ctx, b, job.State, now)
	default:
		// A state newer than this SDK: treat it as still open.
		return s.checkBatchHold(ctx, b, job.State, now)
	}
}

// checkBatchHold cancels and releases an open batch that has outlived its hold.
func (s *Service) checkBatchHold(
	ctx context.Context,
	b repository.SummaryBatch,
	state genai.JobState,
	now time.Time,
) error {
	if now.Sub(b.CreatedAt) < s.batches.cfg.Hold {
		return nil
	}
	// Past its hold: cancel it first, so results that arrive late aren't paid for twice.
	if state != genai.JobStateCancelling {
		if err := s.cancelBatchJob(ctx, b.JobName); err != nil {
			return err
		}
	}
	return s.releaseBatch(ctx, b, now)
}

func (s *Service) getBatchJob(ctx context.Context, name string) (*genai.BatchJob, error) {
	callCtx, cancel := context.WithTimeout(ctx, batchCallTimeout)
	defer cancel()
	job, err := s.batches.jobs.Get(callCtx, name, nil)
	if err != nil {
		return nil, fmt.Errorf("get batch job: %w", err)
	}
	return job, nil
}

func (s *Service) cancelBatchJob(ctx context.Context, name string) error {
	callCtx, cancel := context.WithTimeout(ctx, batchCallTimeout)
	defer cancel()
	if err := s.batches.jobs.Cancel(callCtx, name, nil); err != nil {
		return fmt.Errorf("cancel batch job past its hold: %w", err)
	}
	return nil
}

// storeJobState stores the job's state when it changed, with its output directory and, once the
// job is done, when it finished, and logs summary_batch. It returns b as stored.
func (s *Service) storeJobState(
	ctx context.Context, b repository.SummaryBatch, job *genai.BatchJob, now time.Time,
) (repository.SummaryBatch, error) {
	state := string(job.State)
	if state == b.State {
		return b, nil
	}
	u := repository.SummaryBatchUpdate{BatchID: b.BatchID, State: state}
	if job.OutputInfo != nil && job.OutputInfo.GCSOutputDirectory != "" {
		u.OutputURI = job.OutputInfo.GCSOutputDirectory
	}
	if jobDone(job.State) {
		finished := now
		if !job.EndTime.IsZero() {
			finished = job.EndTime.UTC()
		}
		u.FinishedAt = &finished
	}
	if err := s.store.UpdateSummaryBatch(ctx, u); err != nil {
		return b, fmt.Errorf("store job state %s: %w", state, err)
	}
	b.State = state
	if u.OutputURI != "" {
		b.OutputURI = u.OutputURI
	}
	if u.FinishedAt != nil {
		b.FinishedAt = u.FinishedAt
	}
	s.logSummaryBatch(ctx, b, now)
	return b, nil
}

// jobDone reports whether a job state is final.
func jobDone(state genai.JobState) bool {
	return slices.Contains([]genai.JobState{
		genai.JobStateSucceeded, genai.JobStatePartiallySucceeded, genai.JobStateFailed,
		genai.JobStateCancelled, genai.JobStateExpired,
	}, state)
}

// releaseBatch makes the batch's held bills due now, marks it released and logs summary_batch.
func (s *Service) releaseBatch(ctx context.Context, b repository.SummaryBatch, now time.Time) error {
	released, err := s.store.ReleaseSummaryBatch(ctx, b.BatchID, now)
	if err != nil {
		return fmt.Errorf("release batch bills: %w", err)
	}
	u := repository.SummaryBatchUpdate{BatchID: b.BatchID, State: repository.SummaryBatchReleased}
	if b.FinishedAt == nil {
		u.FinishedAt = &now
	}
	if err = s.store.UpdateSummaryBatch(ctx, u); err != nil {
		return fmt.Errorf("mark batch released: %w", err)
	}
	b.State = repository.SummaryBatchReleased
	s.logSummaryBatch(ctx, b, now, slog.Int("released", released))
	return nil
}

// batchImport counts an import's lines. stale counts the lines for bills the batch no longer
// holds for that text: they're skipped, and count as neither ok nor failed.
type batchImport struct {
	ok, failed, stale, storeErrors syncCounter
}

// importBatch records every output line of a finished job through the synchronous path's
// classification, releases the bills with no output line, and marks the batch imported. It's
// idempotent: a failed import leaves the batch open, and the next poll reads it all again.
func (s *Service) importBatch(
	ctx context.Context,
	b repository.SummaryBatch,
	job *genai.BatchJob,
	now time.Time,
) error {
	im, err := s.readBatchManifest(ctx, b)
	if err != nil {
		return err
	}
	outputs, err := s.batchOutputFiles(ctx, b, job)
	if err != nil {
		return err
	}

	tally := &batchImport{}
	for _, uri := range outputs {
		if err = s.importOutputFile(ctx, b, im, uri, now, tally); err != nil {
			return err
		}
	}
	if n := tally.storeErrors.get(); n > 0 {
		return fmt.Errorf("%d batch lines not recorded; the next poll imports the batch again", n)
	}

	released, err := s.store.ReleaseSummaryBatch(ctx, b.BatchID, now)
	if err != nil {
		return fmt.Errorf("release bills with no output: %w", err)
	}
	ok, failed := tally.ok.get(), tally.failed.get()+len(im.Missing())
	if err = s.store.UpdateSummaryBatch(ctx, repository.SummaryBatchUpdate{
		BatchID: b.BatchID, State: repository.SummaryBatchImported, OKCount: &ok, FailedCount: &failed,
		ImportedAt: &now,
	}); err != nil {
		return fmt.Errorf("mark batch imported: %w", err)
	}
	b.State, b.OKCount, b.FailedCount = repository.SummaryBatchImported, &ok, &failed
	s.logSummaryBatch(ctx, b, now, slog.Int("released", released), slog.Int("unmatched", im.Unmatched()),
		slog.Int("stale", tally.stale.get()))
	return nil
}

// readBatchManifest reads the manifest written next to the batch's input.
func (s *Service) readBatchManifest(ctx context.Context, b repository.SummaryBatch) (*batchjsonl.Import, error) {
	uri, ok := strings.CutSuffix(b.InputURI, batchInputFile)
	if !ok {
		return nil, fmt.Errorf("input %q isn't an %s", b.InputURI, batchInputFile)
	}
	r, err := s.batches.files.Open(ctx, uri+batchManifestFile)
	if err != nil {
		return nil, fmt.Errorf("open batch manifest: %w", err)
	}
	defer r.Close()
	return batchjsonl.ReadManifest(r)
}

// batchOutputFiles lists the JSONL files in the job's output directory.
func (s *Service) batchOutputFiles(
	ctx context.Context,
	b repository.SummaryBatch,
	job *genai.BatchJob,
) ([]string, error) {
	dir := b.OutputURI
	if job.OutputInfo != nil && job.OutputInfo.GCSOutputDirectory != "" {
		dir = job.OutputInfo.GCSOutputDirectory
	}
	if dir == "" {
		return nil, errors.New("no output directory")
	}
	uris, err := s.batches.files.List(ctx, strings.TrimSuffix(dir, "/")+"/")
	if err != nil {
		return nil, fmt.Errorf("list batch output: %w", err)
	}
	outputs := uris[:0]
	for _, uri := range uris {
		if strings.HasSuffix(uri, ".jsonl") {
			outputs = append(outputs, uri)
		}
	}
	return outputs, nil
}

// importOutputFile records each line of one output file, eight at a time.
func (s *Service) importOutputFile(
	ctx context.Context, b repository.SummaryBatch, im *batchjsonl.Import, uri string, now time.Time,
	tally *batchImport,
) error {
	r, err := s.batches.files.Open(ctx, uri)
	if err != nil {
		return fmt.Errorf("open batch output: %w", err)
	}
	defer r.Close()

	lines := make(chan batchjsonl.Line)
	done := make(chan struct{})
	go func() {
		defer close(done)
		workerPoolChan(ctx, lines, DefaultSummaryWorkers, func(ctx context.Context, l batchjsonl.Line) {
			s.recordBatchLine(ctx, b, l, now, tally)
		})
	}()
	err = im.Read(r, func(l batchjsonl.Line) error {
		select {
		case lines <- l:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	})
	close(lines)
	<-done
	if err != nil {
		return fmt.Errorf("read batch output %s: %w", uri, err)
	}
	return ctx.Err()
}

// recordBatchLine records one output line's attempt, with its summary when it's ok, and logs
// summary_result as the synchronous path does. A line Vertex AI failed is due again at once. A
// line for a bill the batch no longer holds for that text (the synchronous job summarized newer
// text during the hold) is skipped as stale, so it never replaces a newer summary (#650).
func (s *Service) recordBatchLine(
	ctx context.Context, b repository.SummaryBatch, l batchjsonl.Line, now time.Time, tally *batchImport,
) {
	summary := l.Summary
	row := repository.SummaryAttemptRow{
		BillID: l.BillID, ContentHash: l.ContentHash, PromptVersion: l.PromptVersion, Model: l.Model,
		Outcome: string(summary.Outcome), Reason: summary.Reason, AttemptedAt: now,
		RequestType: ai.RequestTypeBatch, BatchID: b.BatchID,
	}
	if l.Failed {
		row.RetryAt = &now
	}
	if l.Err == nil && summary.Outcome == ai.OutcomeOK {
		row.Summary = billSummaryRow(l.Bill, summary)
	}
	err := s.store.RecordSummaryAttempt(ctx, row)
	stale := errors.Is(err, repository.ErrStaleBatchAttempt)
	if err != nil && !stale {
		s.logger.WarnContext(ctx, "record batch summary attempt failed", "bill_id", l.BillID, "error", err)
		tally.storeErrors.inc()
		countItem(ctx, err)
		return
	}
	itemOutcome := attemptItemOutcome(row.Summary != nil, row.Outcome)
	switch {
	case stale:
		tally.stale.inc()
		itemOutcome = semconv.ItemOutcomeSkipped
	case row.Summary != nil:
		tally.ok.inc()
		markBill(ctx, l.BillID)
	default:
		tally.failed.inc()
	}
	obs.Items(ctx, itemOutcome, 1)
	s.logAIResult(ctx, "summary_result", s.batches.ai, &summary.Result, row.Outcome, row.Reason,
		slog.String("bill_id", l.BillID), slog.String("version_code", l.VersionCode),
		slog.String("batch_id", b.BatchID), slog.String("kind", summaryKindBill), slog.Bool("stale", stale))
}
