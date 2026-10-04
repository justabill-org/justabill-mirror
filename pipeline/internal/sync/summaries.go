package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/obs"
	"github.com/justabill-org/justabill/obs/semconv"
	"github.com/justabill-org/justabill/pipeline/internal/ai"
	"github.com/justabill-org/justabill/pipeline/internal/ai/batchjsonl"
)

// Kinds of summary in summary_result logs.
const (
	summaryKindBill = "bill"
	summaryKindDiff = "diff"
)

// summarizer is the part of [ai.Summarizer] the summary job uses.
type summarizer interface {
	Config() ai.Config
	SummarizeBill(ctx context.Context, bc ai.BillContext) (*ai.BillSummary, error)
	SummarizeDiff(ctx context.Context, dc ai.DiffContext) (*ai.DiffSummary, error)
}

// SetSummarizer sets the AI summarizer on the service, for summaries and law-change
// explanations. A nil summarizer turns both off.
func (s *Service) SetSummarizer(summarizer *ai.Summarizer) {
	if summarizer == nil {
		s.summarizer, s.lawExplainer = nil, nil
		return
	}
	s.summarizer, s.lawExplainer = summarizer, summarizer
}

// now is the summary job's clock.
func (s *Service) now() time.Time {
	if s.clock == nil {
		return time.Now()
	}
	return s.clock()
}

// SetSummaryJob sets the summary job's batch size, workers, daily cap and queue settings.
func (s *Service) SetSummaryJob(cfg SummaryJobConfig) { s.summaryJob = cfg }

// SyncSummaries runs one pass of the summary job for a congress
// (docs/design/68-ai-summaries-gemini-3.md, "Job"). It logs the day's budget and the backlog, then
// summarizes up to min(batch, cap − attempts today) due bills from the queue, in priority order,
// recording every attempt. limit, when positive, lowers the batch for this run. Diffs follow
// only when diff summaries are enabled, from what's left of the same budget. With the batch
// path on, it first polls the congress's open batches ([Service.PollSummaryBatches]).
func (s *Service) SyncSummaries(ctx context.Context, congressNum, limit int) error {
	if s.summarizer == nil {
		s.logger.InfoContext(ctx, "summarizer not configured, skipping summary sync")
		return nil
	}
	return s.runStep(ctx, stepSummaries, congressNum, false, func(ctx context.Context) (int, error) {
		return s.syncSummariesStep(ctx, congressNum, limit)
	})
}

// syncSummariesStep polls the congress's batches, then runs the synchronous pass. Batches go
// first: an import or release changes what the pass finds due. A failed poll doesn't hold up the
// pass, but it fails the step.
func (s *Service) syncSummariesStep(ctx context.Context, congressNum, limit int) (int, error) {
	pollErr := s.pollSummaryBatches(ctx, congressNum)
	n, err := s.syncSummaries(ctx, congressNum, limit)
	return n, errors.Join(pollErr, err)
}

func (s *Service) syncSummaries(ctx context.Context, congressNum, limit int) (int, error) {
	cfg := s.summaryJob
	batch := cfg.Batch
	if limit > 0 {
		batch = min(batch, limit)
	}
	now := s.now().UTC()

	remaining, err := s.summaryBudget(ctx, congressNum, now)
	if err != nil {
		return 0, err
	}
	q := s.summaryQuery(congressNum, now)
	s.logBacklog(ctx, q)
	if remaining == 0 {
		return 0, nil
	}

	q.Limit = min(batch, remaining)
	billCount, attempted, err := s.syncBillSummaries(ctx, q)
	if !cfg.DiffsEnabled {
		return billCount, err
	}
	diffCount, diffErr := s.syncDiffSummaries(ctx, congressNum, now, min(batch, remaining-attempted))
	s.logger.InfoContext(ctx, "diff summaries synced", "congress", congressNum, "diffs", diffCount)
	return billCount + diffCount, errors.Join(err, diffErr)
}

// summaryBudget logs summary_budget and returns how many attempts are left today (UTC).
func (s *Service) summaryBudget(ctx context.Context, congressNum int, now time.Time) (int, error) {
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	used, err := s.store.CountSummaryAttemptsSince(ctx, dayStart)
	if err != nil {
		return 0, fmt.Errorf("count today's summary attempts: %w", err)
	}
	remaining := max(s.summaryJob.DailyCap-used, 0)
	s.logger.InfoContext(ctx, "summary_budget", "congress", congressNum, "used", used,
		"cap", s.summaryJob.DailyCap, "remaining", remaining)
	return remaining, nil
}

// summaryQuery is the queue query for the summarizer's model and the bill prompt, without a limit.
func (s *Service) summaryQuery(congressNum int, now time.Time) repository.SummaryQueueQuery {
	return repository.SummaryQueueQuery{
		Congress:                  congressNum,
		PromptVersion:             ai.PromptVersionBill,
		Model:                     s.summarizer.Config().Model,
		Now:                       now,
		RecentDays:                s.summaryJob.RecentDays,
		ResummarizeOnPromptChange: s.summaryJob.ResummarizeOnPromptChange,
		CRSContext:                s.summarizer.Config().CRSContext(),
		RuleContext:               s.summarizer.Config().RuleContext(),
	}
}

// logBacklog logs summary_backlog, the due bills per tier. A failed count is only a warning.
func (s *Service) logBacklog(ctx context.Context, q repository.SummaryQueueQuery) {
	counts, err := s.store.CountBillsToSummarize(ctx, q)
	if err != nil {
		s.logger.WarnContext(ctx, "summary backlog not counted", "congress", q.Congress, "error", err)
		return
	}
	total := 0
	for _, n := range counts {
		total += n
	}
	s.logger.InfoContext(ctx, "summary_backlog", "congress", q.Congress, "total", total,
		"voted", counts[repository.SummaryTierVoted], "recent", counts[repository.SummaryTierRecent],
		"other", counts[repository.SummaryTierOther], "prompt_change", counts[repository.SummaryTierPromptChange])
}

// summaryTally counts a run's bills: summarized, attempted (recorded), and not loaded or not
// recorded because the store failed.
type summaryTally struct {
	ok, attempted, storeErrors syncCounter
}

// syncBillSummaries summarizes the bills q returns, and returns how many were summarized and how
// many attempts were recorded. A model outcome other than ok is recorded, not an error; a bill
// the store couldn't load or record fails the step.
func (s *Service) syncBillSummaries(ctx context.Context, q repository.SummaryQueueQuery) (int, int, error) {
	items, err := s.store.QueryBillsToSummarize(ctx, q)
	if err != nil {
		return 0, 0, fmt.Errorf("query bills to summarize: %w", err)
	}

	tally := &summaryTally{}
	workerPool(ctx, items, s.summaryJob.Workers, func(ctx context.Context, item repository.SummaryQueueItem) {
		s.summarizeOneBill(ctx, item, tally)
	})

	ok, attempted := tally.ok.get(), tally.attempted.get()
	s.logger.InfoContext(ctx, "bill summaries synced", "congress", q.Congress, "queued", len(items),
		"summarized", ok, "attempted", attempted)
	if n := tally.storeErrors.get(); n > 0 {
		return ok, attempted, fmt.Errorf("%d of %d bills not loaded or not recorded", n, len(items))
	}
	return ok, attempted, nil
}

func (s *Service) summarizeOneBill(ctx context.Context, item repository.SummaryQueueItem, tally *summaryTally) {
	bc, err := s.store.LoadBillContext(ctx, item.BillID, item.VersionID)
	if err != nil {
		s.logger.WarnContext(ctx, "load bill context failed", "bill_id", item.BillID, "error", err)
		tally.storeErrors.inc()
		countItem(ctx, err)
		return
	}

	summary, genErr := s.summarizer.SummarizeBill(ctx, aiBillContext(bc, s.summarizer.Config()))
	if genErr != nil && ctx.Err() != nil {
		// The run was stopped (timeout or shutdown). That's not the bill's failure, so it keeps
		// its place in the queue and isn't counted: the job's outcome says it was stopped.
		s.logger.WarnContext(ctx, "summary interrupted", "bill_id", item.BillID, "error", genErr)
		return
	}

	attempt := s.billAttempt(bc, summary, genErr)
	if err = s.store.RecordSummaryAttempt(ctx, attempt); err != nil {
		s.logger.WarnContext(ctx, "record summary attempt failed", "bill_id", item.BillID, "error", err)
		tally.storeErrors.inc()
		countItem(ctx, err)
		return
	}
	tally.attempted.inc()
	if attempt.Summary != nil {
		tally.ok.inc()
		markBill(ctx, item.BillID)
	}
	obs.Items(ctx, attemptItemOutcome(attempt.Summary != nil, attempt.Outcome), 1)

	var result *ai.Result
	if summary != nil {
		result = &summary.Result
	}
	s.logSummaryResult(ctx, summaryKindBill, result, attempt.Outcome, attempt.Reason,
		slog.String("bill_id", item.BillID), slog.String("version_code", bc.VersionCode),
		slog.Int("tier", item.Tier))
}

// billAttempt is the attempt row for one SummarizeBill call, with the summary and its provenance
// when the outcome is ok. A call that returned no result (no text to summarize) is an error.
func (s *Service) billAttempt(
	bc *repository.SummaryBillContext, summary *ai.BillSummary, genErr error,
) repository.SummaryAttemptRow {
	row := repository.SummaryAttemptRow{
		BillID:        bc.BillID,
		ContentHash:   bc.ContentHash,
		PromptVersion: ai.PromptVersionBill,
		Model:         s.summarizer.Config().Model,
		Outcome:       string(ai.OutcomeError),
		AttemptedAt:   s.now().UTC(),
		RequestType:   s.summarizer.Config().RequestType,
	}
	if summary == nil {
		if genErr != nil {
			row.Reason = genErr.Error()
		}
		return row
	}
	row.Outcome, row.Reason = string(summary.Outcome), summary.Reason
	if genErr != nil || summary.Outcome != ai.OutcomeOK {
		return row
	}
	row.Summary = billSummaryRow(summarySource(bc, s.summarizer.Config()), summary)
	return row
}

// billSummaryRow is the stored summary of an ok outcome for one bill version, with its
// provenance: the text, and the hashes of the CRS summary and disapproved rule its prompt carried.
func billSummaryRow(src batchjsonl.Bill, summary *ai.BillSummary) *repository.BillSummaryRow {
	return &repository.BillSummaryRow{
		BillID:       src.BillID,
		ShortSummary: summary.ShortSummary,
		LongSummary:  summary.LongSummary,
		// The column keeps its name; the prompt now asks who the bill affects (#68, #199).
		WhoItAffects:      summary.WhoItAffects,
		ModelUsed:         summary.Model,
		SourceVersionID:   src.VersionID,
		SourceVersionCode: src.VersionCode,
		SourceContentHash: src.ContentHash,
		SourceCRSHash:     src.CRSHash,
		SourceRuleHash:    src.RuleHash,
		PromptVersion:     summary.PromptVersion,
		ModelVersion:      summary.ModelVersion,
		InputTruncated:    summary.InputTruncated,
		InputTokens:       int(summary.Usage.InputTokens),
		OutputTokens:      int(summary.Usage.OutputTokens),
		ThinkingTokens:    int(summary.Usage.ThinkingTokens),
	}
}

// attemptItemOutcome is how a recorded bill or diff attempt counts on justabill.pipeline.items: ok
// when it stored a summary, skipped when the model blocked the item (never retried), and failed
// otherwise (error, invalid or truncated output, retried after its backoff).
func attemptItemOutcome(stored bool, outcome string) string {
	switch {
	case stored:
		return semconv.ItemOutcomeOK
	case outcome == string(ai.OutcomeBlocked):
		return semconv.ItemOutcomeSkipped
	default:
		return semconv.ItemOutcomeFailed
	}
}

// summarySource is the bill version a summary of bc is written from, with the hashes of the
// context its prompt carries under cfg: the latest CRS summary's content hash while the CRS
// context is on, and the bill_cra_rules context hash while the rule context is on, else empty.
func summarySource(bc *repository.SummaryBillContext, cfg ai.Config) batchjsonl.Bill {
	src := batchjsonl.Bill{
		BillID: bc.BillID, VersionID: bc.VersionID, VersionCode: bc.VersionCode, ContentHash: bc.ContentHash,
	}
	if bc.CRS != nil && cfg.CRSContext() {
		src.CRSHash = bc.CRS.ContentHash
	}
	if bc.Rule != nil && cfg.RuleContext() {
		src.RuleHash = bc.Rule.ContextHash
	}
	return src
}

// aiBillContext is the summarizer's input for a bill loaded from the store, with its latest CRS
// summary and its disapproved rule while cfg sends them.
func aiBillContext(bc *repository.SummaryBillContext, cfg ai.Config) ai.BillContext {
	out := ai.BillContext{
		BillID: bc.BillID, Congress: bc.Congress, BillType: bc.BillType, Number: bc.Number,
		Title: bc.Title, PolicyArea: bc.PolicyArea, Status: bc.Status, LatestAction: bc.LatestAction,
		Committees: bc.Committees, Subjects: bc.Subjects,
		VersionCode: bc.VersionCode, VersionName: bc.VersionName, Text: bc.Text,
	}
	if bc.StatusDate != nil {
		out.StatusDate = *bc.StatusDate
	}
	if bc.CRS != nil && cfg.CRSContext() {
		out.CRSSummary = &ai.CRSContext{
			VersionDesc: bc.CRS.ActionDesc, ActionDate: bc.CRS.ActionDate, Text: bc.CRS.Text,
		}
	}
	if bc.Rule != nil && cfg.RuleContext() {
		out.Rule = &ai.RuleContext{
			Title: bc.Rule.Title, Agency: bc.Rule.Agency,
			Document: aiRuleDocument(bc.Rule.Document), Withdrawn: aiRuleDocument(bc.Rule.Withdrawn),
		}
	}
	return out
}

// aiRuleDocument is a Federal Register document as the prompt describes it, or nil for none.
func aiRuleDocument(d *repository.SummaryRuleDocument) *ai.RuleDocument {
	if d == nil {
		return nil
	}
	out := &ai.RuleDocument{
		Title: d.Title, Agencies: d.Agencies, DocType: d.DocType, Action: d.Action, Citation: d.Citation,
		Published: d.Published, Abstract: d.Abstract,
	}
	if d.EffectiveOn != nil {
		out.EffectiveOn = *d.EffectiveOn
	}
	return out
}

// logSummaryResult logs one summary_result line for an attempt (design, "Testing and
// monitoring"): info when it's ok, a warning otherwise. result is nil when no call was made.
func (s *Service) logSummaryResult(
	ctx context.Context, kind string, result *ai.Result, outcome, reason string, ids ...slog.Attr,
) {
	s.logAIResult(ctx, "summary_result", s.summarizer.Config(), result, outcome, reason,
		append(ids, slog.String("kind", kind))...)
}

// logAIResult logs one AI call's result line, msg, with its outcome, provenance, tokens and
// latency after ids: info when it's ok, a warning otherwise. result is nil when no call was made,
// and the line then names cfg's model.
func (s *Service) logAIResult(
	ctx context.Context, msg string, cfg ai.Config, result *ai.Result, outcome, reason string, ids ...slog.Attr,
) {
	if result == nil {
		result = &ai.Result{Model: cfg.Model, RequestType: cfg.RequestType}
	}
	attrs := make([]slog.Attr, 0, len(ids)+11) //nolint:mnd // the 11 fields below
	attrs = append(attrs, ids...)
	attrs = append(attrs,
		slog.String("outcome", outcome),
		slog.String("reason", reason),
		slog.String("model", result.Model),
		slog.String("model_version", result.ModelVersion),
		slog.String("prompt_version", result.PromptVersion),
		slog.Int64("input_tokens", result.Usage.InputTokens),
		slog.Int64("output_tokens", result.Usage.OutputTokens),
		slog.Int64("thinking_tokens", result.Usage.ThinkingTokens),
		slog.Bool("input_truncated", result.InputTruncated),
		slog.Int64("latency_ms", result.Latency.Milliseconds()),
		slog.String("request_type", result.RequestType),
	)
	level := slog.LevelInfo
	if outcome != string(ai.OutcomeOK) {
		level = slog.LevelWarn
	}
	s.logger.LogAttrs(ctx, level, msg, attrs...)
}

// syncDiffSummaries summarizes up to limit due diffs, and returns how many were summarized. Like
// bills, every attempt is recorded (a model outcome other than ok isn't an error), and a diff whose
// attempt the store couldn't record fails the step.
func (s *Service) syncDiffSummaries(ctx context.Context, congressNum int, now time.Time, limit int) (int, error) {
	if limit <= 0 {
		return 0, nil
	}
	diffs, err := s.store.QueryDiffsToSummarize(ctx, repository.DiffSummaryQueueQuery{
		Congress:      congressNum,
		Limit:         limit,
		PromptVersion: ai.PromptVersionDiff,
		Model:         s.summarizer.Config().Model,
		Now:           now,
	})
	if err != nil {
		return 0, fmt.Errorf("query diffs to summarize: %w", err)
	}

	tally := &summaryTally{}
	workerPool(ctx, diffs, s.summaryJob.Workers, func(ctx context.Context, d repository.DiffRef) {
		s.summarizeOneDiff(ctx, d, tally)
	})

	ok := tally.ok.get()
	if n := tally.storeErrors.get(); n > 0 {
		return ok, fmt.Errorf("%d of %d diffs not recorded", n, len(diffs))
	}
	return ok, nil
}

func (s *Service) summarizeOneDiff(ctx context.Context, d repository.DiffRef, tally *summaryTally) {
	summary, genErr := s.summarizer.SummarizeDiff(ctx, ai.DiffContext{
		DiffID: d.DiffID, BillID: d.BillID, Diff: string(d.DiffContent),
	})
	if genErr != nil && ctx.Err() != nil {
		// The run was stopped, which isn't the diff's failure: it keeps its place in the queue and
		// isn't counted.
		s.logger.WarnContext(ctx, "diff summary interrupted", "diff_id", d.DiffID, "error", genErr)
		return
	}

	attempt := s.diffAttempt(d.DiffID, summary, genErr)
	if err := s.store.RecordDiffSummaryAttempt(ctx, attempt); err != nil {
		s.logger.WarnContext(ctx, "record diff summary attempt failed", "diff_id", d.DiffID, "error", err)
		tally.storeErrors.inc()
		countItem(ctx, err)
		return
	}
	tally.attempted.inc()
	if attempt.Summary != nil {
		tally.ok.inc()
	}
	obs.Items(ctx, attemptItemOutcome(attempt.Summary != nil, attempt.Outcome), 1)

	var result *ai.Result
	if summary != nil {
		result = &summary.Result
	}
	s.logSummaryResult(ctx, summaryKindDiff, result, attempt.Outcome, attempt.Reason,
		slog.String("bill_id", d.BillID), slog.String("diff_id", d.DiffID))
}

// diffAttempt is the attempt row for one SummarizeDiff call, with the summary and its provenance
// when the outcome is ok. A call that returned no result (an empty diff) is an error.
func (s *Service) diffAttempt(diffID string, summary *ai.DiffSummary, genErr error) repository.DiffSummaryAttemptRow {
	row := repository.DiffSummaryAttemptRow{
		DiffID:        diffID,
		PromptVersion: ai.PromptVersionDiff,
		Model:         s.summarizer.Config().Model,
		Outcome:       string(ai.OutcomeError),
		AttemptedAt:   s.now().UTC(),
	}
	if summary == nil {
		if genErr != nil {
			row.Reason = genErr.Error()
		}
		return row
	}
	row.Outcome, row.Reason = string(summary.Outcome), summary.Reason
	if genErr != nil || summary.Outcome != ai.OutcomeOK {
		return row
	}
	row.Summary = &repository.DiffSummaryRow{
		DiffID:        diffID,
		Summary:       summary.Summary,
		ModelUsed:     summary.Model,
		PromptVersion: summary.PromptVersion,
		ModelVersion:  summary.ModelVersion,
		InputTokens:   int(summary.Usage.InputTokens),
		OutputTokens:  int(summary.Usage.OutputTokens),
	}
	return row
}
