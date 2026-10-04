package sync

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/ai"
)

// Law-change explanations (docs/design/149-law-aware-assistant.md, "Explanations"): one Gemini
// call per bill whose latest text amends, repeals or adds sections of the US Code, on the summary
// job's contract (queue, attempts, retries, daily cap), with its own attempts table and cap.
// "nonusc:" laws (popular-name citations with no US Code cite) are stored but never explained.

// stepLawChanges is the law-change job's sync_state step.
const stepLawChanges = "law_changes"

// lawExplainer is the part of [ai.Summarizer] the law-change job uses.
type lawExplainer interface {
	Config() ai.Config
	ExplainLawChanges(ctx context.Context, lc ai.LawChangeContext) (*ai.LawChanges, error)
}

// SyncLawChanges runs one pass of the law-change job for a congress. It logs the day's budget and
// the backlog, then explains up to min(batch, cap − attempts today) due bills, recording every
// attempt. limit, when positive, lowers the batch for this run. It does nothing without a
// summarizer or a loaded US Code release point.
func (s *Service) SyncLawChanges(ctx context.Context, congressNum, limit int) error {
	if s.lawExplainer == nil {
		s.logger.InfoContext(ctx, "summarizer not configured, skipping law change sync")
		return nil
	}
	return s.runStep(ctx, stepLawChanges, congressNum, false, func(ctx context.Context) (int, error) {
		return s.syncLawChanges(ctx, congressNum, limit)
	})
}

func (s *Service) syncLawChanges(ctx context.Context, congressNum, limit int) (int, error) {
	rp, err := s.store.CurrentUSCReleasePoint(ctx)
	if err != nil {
		return 0, fmt.Errorf("read the US Code release point: %w", err)
	}
	if rp == nil {
		s.logger.InfoContext(ctx, "no US Code release point loaded, skipping law change sync", "congress", congressNum)
		return 0, nil
	}
	now := s.now().UTC()
	remaining, err := s.lawChangeBudget(ctx, congressNum, now)
	if err != nil {
		return 0, err
	}
	q := repository.LawChangeQueueQuery{
		Congress: congressNum, PromptVersion: ai.PromptVersionLaw, Model: s.lawExplainer.Config().Model, Now: now,
	}
	if backlog, countErr := s.store.CountBillsToExplainLaw(ctx, q); countErr != nil {
		s.logger.WarnContext(ctx, "law change backlog not counted", "congress", congressNum, "error", countErr)
	} else {
		s.logger.InfoContext(ctx, "law_change_backlog", "congress", congressNum, "total", backlog)
	}
	if remaining == 0 {
		return 0, nil
	}

	batch := s.summaryJob.LawBatch
	if limit > 0 {
		batch = min(batch, limit)
	}
	q.Limit = min(batch, remaining)
	items, err := s.store.QueryBillsToExplainLaw(ctx, q)
	if err != nil {
		return 0, fmt.Errorf("query bills to explain law: %w", err)
	}

	tally := &summaryTally{}
	workerPool(ctx, items, s.summaryJob.Workers, func(ctx context.Context, item repository.LawChangeQueueItem) {
		s.explainOneBill(ctx, item, rp.ReleasePoint, tally)
	})

	ok, attempted := tally.ok.get(), tally.attempted.get()
	s.logger.InfoContext(ctx, "law changes synced", "congress", congressNum, "queued", len(items),
		"explained", ok, "attempted", attempted, "release_point", rp.ReleasePoint)
	if n := tally.storeErrors.get(); n > 0 {
		return ok, fmt.Errorf("%d of %d bills not loaded or not recorded", n, len(items))
	}
	return ok, ctx.Err()
}

// lawChangeBudget logs law_change_budget and returns how many attempts are left today (UTC).
func (s *Service) lawChangeBudget(ctx context.Context, congressNum int, now time.Time) (int, error) {
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	used, err := s.store.CountLawChangeAttemptsSince(ctx, dayStart)
	if err != nil {
		return 0, fmt.Errorf("count today's law change attempts: %w", err)
	}
	remaining := max(s.summaryJob.LawDailyCap-used, 0)
	s.logger.InfoContext(ctx, "law_change_budget", "congress", congressNum, "used", used,
		"cap", s.summaryJob.LawDailyCap, "remaining", remaining)
	return remaining, nil
}

func (s *Service) explainOneBill(
	ctx context.Context, item repository.LawChangeQueueItem, releasePoint string, tally *summaryTally,
) {
	lc, err := s.store.LoadLawChangeContext(ctx, item.BillID, item.VersionID)
	if err != nil {
		s.logger.WarnContext(ctx, "load law change context failed", "bill_id", item.BillID, "error", err)
		tally.storeErrors.inc()
		return
	}
	in := aiLawChangeContext(lc, releasePoint)
	ask := explainable(in)
	if len(ask.Sections) == 0 {
		// The queue leaves out bills whose only changes are to "nonusc:" laws; one whose references
		// changed since it was queued gets no call and no attempt.
		s.logger.InfoContext(ctx, "no US Code section to explain", "bill_id", item.BillID)
		return
	}

	changes, genErr := s.lawExplainer.ExplainLawChanges(ctx, ask)
	if genErr != nil && ctx.Err() != nil {
		// The run was stopped: not the bill's failure, so it keeps its place in the queue.
		s.logger.WarnContext(ctx, "law change explanation interrupted", "bill_id", item.BillID, "error", genErr)
		return
	}

	attempt := s.lawChangeAttempt(lc, in, releasePoint, changes, genErr)
	if err = s.store.RecordLawChangeAttempt(ctx, attempt); err != nil {
		s.logger.WarnContext(ctx, "record law change attempt failed", "bill_id", item.BillID, "error", err)
		tally.storeErrors.inc()
		return
	}
	tally.attempted.inc()
	if attempt.Changes != nil {
		tally.ok.inc()
		markBill(ctx, item.BillID)
	}
	s.logLawChangeResult(ctx, lc, len(in.Sections), changes, attempt)
}

// lawChangeAttempt is the attempt row for one ExplainLawChanges call and, when the outcome is ok,
// one bill_law_changes row per section of in: explained, or with an empty explanation when it
// wasn't (past the explainer's caps, or a "nonusc:" law, which isn't sent).
func (s *Service) lawChangeAttempt(
	lc *repository.LawChangeContext, in ai.LawChangeContext, releasePoint string, changes *ai.LawChanges,
	genErr error,
) repository.LawChangeAttemptRow {
	row := repository.LawChangeAttemptRow{
		BillID:        lc.BillID,
		ContentHash:   lc.ContentHash,
		PromptVersion: ai.PromptVersionLaw,
		Model:         s.lawExplainer.Config().Model,
		Outcome:       string(ai.OutcomeError),
		AttemptedAt:   s.now().UTC(),
	}
	if changes == nil {
		if genErr != nil {
			row.Reason = genErr.Error()
		}
		return row
	}
	row.Outcome, row.Reason = string(changes.Outcome), changes.Reason
	if genErr != nil || changes.Outcome != ai.OutcomeOK {
		return row
	}
	row.Changes = make([]repository.BillLawChangeRow, 0, len(in.Sections))
	for _, sec := range in.Sections {
		row.Changes = append(row.Changes, repository.BillLawChangeRow{
			SectionID:         sec.SectionID,
			ChangeKind:        sec.Kind,
			Explanation:       changes.Explanations[sec.SectionID],
			SourceContentHash: lc.ContentHash,
			ReleasePoint:      releasePoint,
			ModelUsed:         changes.Model,
			PromptVersion:     changes.PromptVersion,
		})
	}
	return row
}

// logLawChangeResult logs one law_change_result line for an attempt: info when it's ok, a
// warning otherwise. invalid_items counts answer items dropped for naming a section that wasn't
// asked for, or for an empty or overlong explanation.
func (s *Service) logLawChangeResult(
	ctx context.Context, lc *repository.LawChangeContext, sections int, changes *ai.LawChanges,
	attempt repository.LawChangeAttemptRow,
) {
	var result *ai.Result
	asked, explained, dropped := 0, 0, 0
	if changes != nil {
		result = &changes.Result
		asked, explained, dropped = changes.Asked, len(changes.Explanations), changes.Dropped
	}
	s.logAIResult(ctx, "law_change_result", s.lawExplainer.Config(), result, attempt.Outcome, attempt.Reason,
		slog.String("bill_id", lc.BillID), slog.String("version_code", lc.VersionCode),
		slog.Int("sections", sections), slog.Int("asked", asked), slog.Int("explained", explained),
		slog.Int("invalid_items", dropped))
}

// aiLawChangeContext is the explainer's input: the bill, and one section per section of law its
// references change, in the order the store returned them. A section named by several references
// gets their instructions and subsection paths together, and the strongest kind: repeals, then
// adds, then amends.
func aiLawChangeContext(lc *repository.LawChangeContext, releasePoint string) ai.LawChangeContext {
	out := ai.LawChangeContext{
		BillID: lc.BillID, Congress: lc.Congress, BillType: lc.BillType, Number: lc.Number, Title: lc.Title,
		ShortSummary: lc.ShortSummary, ReleasePoint: releasePoint,
	}
	index := map[string]int{}
	for _, r := range lc.Refs {
		i, seen := index[r.SectionID]
		if !seen {
			i = len(out.Sections)
			index[r.SectionID] = i
			out.Sections = append(out.Sections, ai.LawSection{
				SectionID: r.SectionID, Kind: r.RefKind, Heading: r.Heading, CurrentText: r.CurrentText,
			})
		}
		sec := &out.Sections[i]
		if kindRank(r.RefKind) > kindRank(sec.Kind) {
			sec.Kind = r.RefKind
		}
		// The parser joins the paths one reference cites with ", " ("(t), (c)(2)(B)").
		for p := range strings.SplitSeq(r.SubsectionPath, ",") {
			if p = strings.TrimSpace(p); p != "" && !slices.Contains(sec.Subsections, p) {
				sec.Subsections = append(sec.Subsections, p)
			}
		}
		if instr := strings.TrimSpace(r.Instruction); instr != "" && !strings.Contains(sec.Instruction, instr) {
			if sec.Instruction != "" {
				sec.Instruction += "\n\n"
			}
			sec.Instruction += instr
		}
	}
	return out
}

// explainable is in with only its US Code sections, statutory notes included. It leaves out the
// "nonusc:" laws: the site shows no explanation for them, so explaining them would spend tokens
// and a share of the per-call section limit on text no one reads (#538).
func explainable(in ai.LawChangeContext) ai.LawChangeContext {
	in.Sections = slices.DeleteFunc(slices.Clone(in.Sections), func(sec ai.LawSection) bool {
		return strings.HasPrefix(sec.SectionID, model.NonUSCSectionPrefix)
	})
	return in
}

// kindRank orders the kinds that change law, weakest first; any other kind ranks below them.
func kindRank(kind string) int {
	return slices.Index([]string{model.LawRefAmends, model.LawRefAdds, model.LawRefRepeals}, kind)
}
