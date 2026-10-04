package spannerdb

import (
	"context"
	"errors"
	"fmt"
	"time"

	"cloud.google.com/go/spanner"
	"google.golang.org/api/iterator"

	"github.com/justabill-org/justabill/db/repository"
)

// diffSummaryQueueSQL returns a congress's diffs with no summary, leaving out the ones an attempt
// for the same prompt and model holds back: blocked for good, or failed and not yet due again.
// Empty diffs (is_empty) have nothing to summarize and are never queued.
const diffSummaryQueueSQL = `SELECT btd.diff_id, btd.bill_id, btd.diff_content
FROM bill_text_diffs btd
JOIN bills b ON btd.bill_id = b.bill_id
LEFT JOIN bill_text_diff_summaries btds ON btds.diff_id = btd.diff_id
LEFT JOIN diff_summary_attempts dsa ON dsa.diff_id = btd.diff_id
WHERE btds.diff_id IS NULL AND NOT btd.is_empty AND b.congress = @congress
	AND NOT (dsa.diff_id IS NOT NULL AND dsa.outcome != @ok
		AND dsa.prompt_version = @prompt AND dsa.model = @model
		AND (dsa.next_attempt_at IS NULL OR dsa.next_attempt_at > @now))
ORDER BY btd.bill_id, btd.diff_id
LIMIT @lim`

// QueryDiffsToSummarize returns the diffs due for a summary; see [repository.PipelineStore].
func (s *PipelineStoreImpl) QueryDiffsToSummarize(
	ctx context.Context, q repository.DiffSummaryQueueQuery,
) ([]repository.DiffRef, error) {
	stmt := spanner.Statement{SQL: diffSummaryQueueSQL, Params: map[string]any{
		paramCongress: int64(q.Congress), paramLimit: int64(q.Limit), paramPrompt: q.PromptVersion,
		paramModel: q.Model, paramNow: q.Now, "ok": repository.SummaryOutcomeOK,
	}}

	iter := s.client.Single().Query(ctx, stmt)
	defer iter.Stop()

	var refs []repository.DiffRef
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return refs, nil
		}
		if err != nil {
			return nil, fmt.Errorf("query diffs to summarize: %w", err)
		}
		var (
			ref     repository.DiffRef
			content spanner.NullJSON
		)
		if err = row.Columns(&ref.DiffID, &ref.BillID, &content); err != nil {
			return nil, fmt.Errorf("read diff queue row: %w", err)
		}
		ref.DiffContent = nullJSONToRaw(content)
		refs = append(refs, ref)
	}
}

// diffSummaryRecord is a bill_text_diff_summaries row as written.
type diffSummaryRecord struct {
	DiffID        string             `spanner:"diff_id"`
	Summary       string             `spanner:"summary"`
	ModelUsed     spanner.NullString `spanner:"model_used"`
	GeneratedAt   time.Time          `spanner:"generated_at"`
	PromptVersion spanner.NullString `spanner:"prompt_version"`
	ModelVersion  spanner.NullString `spanner:"model_version"`
	InputTokens   int64              `spanner:"input_tokens"`
	OutputTokens  int64              `spanner:"output_tokens"`
}

// diffSummaryAttemptRecord is a diff_summary_attempts row.
type diffSummaryAttemptRecord struct {
	DiffID        string             `spanner:"diff_id"`
	PromptVersion string             `spanner:"prompt_version"`
	Model         string             `spanner:"model"`
	Outcome       string             `spanner:"outcome"`
	Reason        spanner.NullString `spanner:"reason"`
	Attempts      int64              `spanner:"attempts"`
	AttemptedAt   time.Time          `spanner:"attempted_at"`
	NextAttemptAt spanner.NullTime   `spanner:"next_attempt_at"`
}

// RecordDiffSummaryAttempt replaces the diff's attempt row and, when a.Summary is set, writes the
// summary with its provenance in the same transaction; see [repository.PipelineStore].
func (s *PipelineStoreImpl) RecordDiffSummaryAttempt(
	ctx context.Context, a repository.DiffSummaryAttemptRow,
) error {
	_, err := s.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		failures, err := priorFailures(ctx, txn, spanner.Statement{
			SQL: `SELECT attempts FROM diff_summary_attempts
			 WHERE diff_id = @diff AND prompt_version = @prompt AND model = @model`,
			Params: map[string]any{"diff": a.DiffID, paramPrompt: a.PromptVersion, paramModel: a.Model},
		})
		if err != nil {
			return err
		}
		attempts, next := nextSummaryAttempt(a.Outcome, failures, a.AttemptedAt)
		attempt, err := spanner.InsertOrUpdateStruct("diff_summary_attempts", diffSummaryAttemptRecord{
			DiffID: a.DiffID, PromptVersion: a.PromptVersion, Model: a.Model, Outcome: a.Outcome,
			Reason: emptyToNull(a.Reason), Attempts: attempts, AttemptedAt: a.AttemptedAt, NextAttemptAt: next,
		})
		if err != nil {
			return err
		}
		muts := []*spanner.Mutation{attempt}
		if ds := a.Summary; ds != nil {
			summary, sumErr := spanner.InsertOrUpdateStruct("bill_text_diff_summaries", diffSummaryRecord{
				DiffID: ds.DiffID, Summary: ds.Summary, ModelUsed: emptyToNull(ds.ModelUsed),
				GeneratedAt: a.AttemptedAt, PromptVersion: emptyToNull(ds.PromptVersion),
				ModelVersion: emptyToNull(ds.ModelVersion), InputTokens: int64(ds.InputTokens),
				OutputTokens: int64(ds.OutputTokens),
			})
			if sumErr != nil {
				return sumErr
			}
			muts = append(muts, summary)
		}
		return txn.BufferWrite(muts)
	})
	return err
}
