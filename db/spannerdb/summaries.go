package spannerdb

import (
	"context"
	"errors"
	"fmt"
	"time"

	"cloud.google.com/go/civil"
	"cloud.google.com/go/spanner"
	"google.golang.org/api/iterator"

	"github.com/justabill-org/justabill/db/repository"
)

// latestVersionOrder orders a bill's text versions latest first, as latestFetchedVersions does
// for the summary queue. Since #79 (PR #304) sort_order is chronological (1 is the oldest), so the
// highest sort_order wins.
const latestVersionOrder = "btv.sort_order DESC, btv.version_id"

// Query parameters of the summary and diff summary queues and attempts.
const (
	paramPrompt = "prompt"
	paramModel  = "model"
	paramHash   = "hash"
)

// Summary retry delays after consecutive failures of the same text, prompt and model
// (docs/design/68-ai-summaries-gemini-3.md, "Queue").
const (
	summaryRetryFirst  = time.Hour
	summaryRetrySecond = 4 * time.Hour
	summaryRetryParked = 24 * time.Hour
)

// passedChamberSQL is true for a bill b that passed at least one chamber or became law
// (repository.SummaryQueueQuery.PassedChamber): a Library of Congress action code for passage in
// the House (8000) or Senate (17000), presentment (28000), signing (E30000, 41000) or a public law
// (36000, E40000), an action text that says it passed, or a status of passed_house or later. The
// codes and text don't depend on how the status history classified the actions (#659).
const passedChamberSQL = `(EXISTS(SELECT 1 FROM bill_actions ba WHERE ba.bill_id = b.bill_id
		AND (ba.action_code IN UNNEST(@passedCodes)
			OR STARTS_WITH(ba.action_text, 'Passed/agreed to in ')
			OR STARTS_WITH(ba.action_text, 'Became Public Law')))
	OR EXISTS(SELECT 1 FROM bill_status_history h WHERE h.bill_id = b.bill_id AND h.status_rank >= @passedRank))`

// passedHouseRank is the status_rank of passed_house, the first stage after a chamber's passage
// (pipeline/internal/sync's lifecycle ranks).
const passedHouseRank = 4

// passedChamberCodes are the Library of Congress action codes passedChamberSQL accepts.
func passedChamberCodes() []string {
	return []string{"8000", "17000", "28000", "E30000", "41000", "36000", "E40000"}
}

// summaryContextRow is what LoadBillContext reads in one query.
type summaryContextRow struct {
	BillID       string             `spanner:"bill_id"`
	Congress     int64              `spanner:"congress"`
	BillType     string             `spanner:"bill_type"`
	Number       int64              `spanner:"number"`
	Title        string             `spanner:"title"`
	PolicyArea   spanner.NullString `spanner:"policy_area"`
	Status       spanner.NullString `spanner:"current_status"`
	StatusDate   spanner.NullDate   `spanner:"status_date"`
	LatestAction spanner.NullString `spanner:"latest_action_text"`
	Committees   []string           `spanner:"committees"`
	Subjects     []string           `spanner:"subjects"`
	VersionID    string             `spanner:"version_id"`
	VersionCode  string             `spanner:"version_code"`
	VersionName  string             `spanner:"version_type"`
	ContentHash  string             `spanner:"content_hash"`
	Content      string             `spanner:"content"`
	ContentGz    []byte             `spanner:"content_gz"`
	CRS          []*summaryCRSRow   `spanner:"crs"`
	Rule         []*summaryRuleRow  `spanner:"cra_rule"`
	RuleDocs     []*summaryRuleDoc  `spanner:"cra_documents"`
}

// summaryCRSRow is the latest CRS summary in a summaryContextRow: an array of at most one, since
// ToStruct can't decode a NULL STRUCT.
type summaryCRSRow struct {
	ActionDesc  string     `spanner:"action_desc"`
	ActionDate  civil.Date `spanner:"action_date"`
	Text        string     `spanner:"text"`
	ContentHash string     `spanner:"content_hash"`
}

// LoadBillContext loads a bill's metadata, the stored text of one of its versions, its latest
// CRS summary and, for a CRA resolution, the rule it disapproves. Committee and subject names come
// from the ontology link tables, sorted by name. It returns an error wrapping
// [spanner.ErrRowNotFound] when the bill, the version or its text doesn't exist.
func (s *PipelineStoreImpl) LoadBillContext(
	ctx context.Context, billID, versionID string,
) (*repository.SummaryBillContext, error) {
	stmt := spanner.Statement{
		SQL: `SELECT b.bill_id, b.congress, b.bill_type, b.number, b.title, b.policy_area, b.current_status,
			b.status_date, JSON_VALUE(b.latest_action, '$.text') AS latest_action_text,
			ARRAY(SELECT DISTINCT c.name FROM bill_committees bc
				JOIN committees c ON c.committee_id = bc.committee_id
				WHERE bc.bill_id = b.bill_id ORDER BY c.name) AS committees,
			ARRAY(SELECT sj.name FROM bill_subjects bsj
				JOIN subjects sj ON sj.subject_id = bsj.subject_id
				WHERE bsj.bill_id = b.bill_id ORDER BY sj.name) AS subjects,
			btv.version_id, btv.version_code, btv.version_type, bt.content_hash, bt.content, bt.content_gz,
			ARRAY(SELECT AS STRUCT c.action_desc, c.action_date, c.text, c.content_hash
				FROM bill_crs_summaries c WHERE c.bill_id = b.bill_id
				ORDER BY c.action_date DESC, c.crs_updated_at DESC LIMIT 1) AS crs,
			` + summaryRuleColumns + `
		 FROM bills b
		 JOIN bill_text_versions btv ON btv.bill_id = b.bill_id
		 JOIN bill_texts bt ON bt.version_id = btv.version_id
		 WHERE b.bill_id = @billID AND btv.version_id = @version`,
		Params: map[string]any{paramBillID: billID, "version": versionID},
	}

	iter := s.client.Single().Query(ctx, stmt)
	defer iter.Stop()
	row, err := iter.Next()
	if errors.Is(err, iterator.Done) {
		return nil, fmt.Errorf("bill %s version %s with text: %w", billID, versionID, spanner.ErrRowNotFound)
	}
	if err != nil {
		return nil, fmt.Errorf("load bill context: %w", err)
	}
	var r summaryContextRow
	if err = row.ToStruct(&r); err != nil {
		return nil, fmt.Errorf("read bill context: %w", err)
	}
	text, err := storedText(r.Content, r.ContentHash, r.ContentGz)
	if err != nil {
		return nil, fmt.Errorf("bill %s version %s text: %w", billID, versionID, err)
	}
	var crs *repository.SummaryCRSContext
	if len(r.CRS) > 0 && r.CRS[0] != nil {
		c := r.CRS[0]
		crs = &repository.SummaryCRSContext{
			ActionDesc: c.ActionDesc, ActionDate: c.ActionDate.In(time.UTC), Text: c.Text, ContentHash: c.ContentHash,
		}
	}
	rule, err := summaryRule(r.Rule, r.RuleDocs)
	if err != nil {
		return nil, fmt.Errorf("bill %s disapproved rule: %w", billID, err)
	}
	return &repository.SummaryBillContext{
		BillID: r.BillID, Congress: int(r.Congress), BillType: r.BillType, Number: int(r.Number),
		Title: r.Title, PolicyArea: r.PolicyArea.StringVal, Status: r.Status.StringVal,
		StatusDate: nullDatePtr(r.StatusDate), LatestAction: r.LatestAction.StringVal,
		Committees: r.Committees, Subjects: r.Subjects,
		VersionID: r.VersionID, VersionCode: r.VersionCode, VersionName: r.VersionName,
		ContentHash: r.ContentHash, Text: text, CRS: crs, Rule: rule,
	}, nil
}

// billSummaryRecord is a bill_summaries row as written; summary_tokens is generated.
//
// WhoItAffects keeps the column name why_it_matters: Spanner can't rename a column, so the Go,
// API and web names changed and the column didn't (docs/design/199-who-it-affects-rename.md).
// The summary SELECTs (billSummaryColumns) read the same column.
type billSummaryRecord struct {
	BillID            string             `spanner:"bill_id"`
	ShortSummary      string             `spanner:"short_summary"`
	LongSummary       string             `spanner:"long_summary"`
	WhoItAffects      string             `spanner:"why_it_matters"`
	ModelUsed         string             `spanner:"model_used"`
	GeneratedAt       time.Time          `spanner:"generated_at"`
	SourceVersionID   spanner.NullString `spanner:"source_version_id"`
	SourceVersionCode spanner.NullString `spanner:"source_version_code"`
	SourceContentHash spanner.NullString `spanner:"source_content_hash"`
	SourceCRSHash     spanner.NullString `spanner:"source_crs_hash"`
	SourceRuleHash    spanner.NullString `spanner:"source_rule_hash"`
	PromptVersion     spanner.NullString `spanner:"prompt_version"`
	ModelVersion      spanner.NullString `spanner:"model_version"`
	InputTruncated    bool               `spanner:"input_truncated"`
	InputTokens       int64              `spanner:"input_tokens"`
	OutputTokens      int64              `spanner:"output_tokens"`
	ThinkingTokens    int64              `spanner:"thinking_tokens"`
}

// summaryAttemptRecord is a summary_attempts row.
type summaryAttemptRecord struct {
	BillID        string             `spanner:"bill_id"`
	ContentHash   string             `spanner:"content_hash"`
	PromptVersion string             `spanner:"prompt_version"`
	Model         string             `spanner:"model"`
	Outcome       string             `spanner:"outcome"`
	Reason        spanner.NullString `spanner:"reason"`
	Attempts      int64              `spanner:"attempts"`
	AttemptedAt   time.Time          `spanner:"attempted_at"`
	NextAttemptAt spanner.NullTime   `spanner:"next_attempt_at"`
	RequestType   spanner.NullString `spanner:"request_type"`
	BatchID       spanner.NullString `spanner:"batch_id"`
}

// billSummaryMutation writes a summary with its provenance. Empty provenance strings are
// stored as NULL.
func billSummaryMutation(bs repository.BillSummaryRow, generatedAt time.Time) (*spanner.Mutation, error) {
	return spanner.InsertOrUpdateStruct("bill_summaries", billSummaryRecord{
		BillID: bs.BillID, ShortSummary: bs.ShortSummary, LongSummary: bs.LongSummary,
		WhoItAffects: bs.WhoItAffects, ModelUsed: bs.ModelUsed, GeneratedAt: generatedAt,
		SourceVersionID: emptyToNull(bs.SourceVersionID), SourceVersionCode: emptyToNull(bs.SourceVersionCode),
		SourceContentHash: emptyToNull(bs.SourceContentHash), SourceCRSHash: emptyToNull(bs.SourceCRSHash),
		SourceRuleHash: emptyToNull(bs.SourceRuleHash), PromptVersion: emptyToNull(bs.PromptVersion),
		ModelVersion:   emptyToNull(bs.ModelVersion),
		InputTruncated: bs.InputTruncated, InputTokens: int64(bs.InputTokens),
		OutputTokens: int64(bs.OutputTokens), ThinkingTokens: int64(bs.ThinkingTokens),
	})
}

// UpsertBillSummary writes a bill's summary and its provenance.
func (s *PipelineStoreImpl) UpsertBillSummary(ctx context.Context, bs repository.BillSummaryRow) error {
	m, err := billSummaryMutation(bs, time.Now())
	if err != nil {
		return err
	}
	_, err = s.client.Apply(ctx, []*spanner.Mutation{m})
	return err
}

// RecordSummaryAttempt replaces the bill's attempt row and, when a.Summary is set, writes the
// summary in the same transaction; see [repository.PipelineStore]. A batch result (a.BatchID set)
// is written only over its batch's hold for the same text (#650).
func (s *PipelineStoreImpl) RecordSummaryAttempt(ctx context.Context, a repository.SummaryAttemptRow) error {
	if a.Outcome == repository.SummaryOutcomeBatchPending {
		return fmt.Errorf("record summary attempt for %s: batch holds are written by HoldSummaryBatch", a.BillID)
	}
	_, err := s.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		if a.BatchID != "" {
			held, err := batchStillHolds(ctx, txn, a)
			if err != nil || !held {
				return err
			}
		}
		failures, err := priorFailures(ctx, txn, spanner.Statement{
			SQL: `SELECT attempts FROM summary_attempts
			 WHERE bill_id = @billID AND content_hash = @hash AND prompt_version = @prompt AND model = @model
				AND outcome != @pending`,
			Params: map[string]any{
				paramBillID: a.BillID, paramHash: a.ContentHash, paramPrompt: a.PromptVersion, paramModel: a.Model,
				paramPending: repository.SummaryOutcomeBatchPending,
			},
		})
		if err != nil {
			return err
		}
		attempts, next := nextSummaryAttempt(a.Outcome, failures, a.AttemptedAt)
		if a.RetryAt != nil && next.Valid {
			next.Time = *a.RetryAt
		}
		attempt, err := spanner.InsertOrUpdateStruct("summary_attempts", summaryAttemptRecord{
			BillID: a.BillID, ContentHash: a.ContentHash, PromptVersion: a.PromptVersion, Model: a.Model,
			Outcome: a.Outcome, Reason: emptyToNull(a.Reason), Attempts: attempts,
			AttemptedAt: a.AttemptedAt, NextAttemptAt: next,
			RequestType: emptyToNull(a.RequestType), BatchID: emptyToNull(a.BatchID),
		})
		if err != nil {
			return err
		}
		muts := []*spanner.Mutation{attempt}
		if a.Summary != nil {
			summary, sumErr := billSummaryMutation(*a.Summary, a.AttemptedAt)
			if sumErr != nil {
				return sumErr
			}
			muts = append(muts, summary)
		}
		return txn.BufferWrite(muts)
	})
	return err
}

// batchStillHolds reports whether the bill's attempt row is still a's batch's batch_pending hold
// for a's content hash, so a's result may replace it. It returns false with no error when that
// batch has already recorded its result for the same hash (a re-import writes nothing, so its
// failures aren't counted twice), and [repository.ErrStaleBatchAttempt] when anything else
// replaced the hold: a synchronous attempt, likely for newer text, or another batch.
func batchStillHolds(
	ctx context.Context, txn *spanner.ReadWriteTransaction, a repository.SummaryAttemptRow,
) (bool, error) {
	row, err := txn.ReadRow(ctx, "summary_attempts", spanner.Key{a.BillID},
		[]string{colContentHash, "outcome", colBatchID})
	if errors.Is(err, spanner.ErrRowNotFound) {
		return false, fmt.Errorf("bill %s has no hold: %w", a.BillID, repository.ErrStaleBatchAttempt)
	}
	if err != nil {
		return false, fmt.Errorf("read summary attempt: %w", err)
	}
	var (
		hash, outcome string
		batchID       spanner.NullString
	)
	if err = row.Columns(&hash, &outcome, &batchID); err != nil {
		return false, fmt.Errorf("read summary attempt: %w", err)
	}
	if batchID.StringVal != a.BatchID || hash != a.ContentHash {
		return false, fmt.Errorf("bill %s is no longer held by batch %s for %s: %w",
			a.BillID, a.BatchID, a.ContentHash, repository.ErrStaleBatchAttempt)
	}
	return outcome == repository.SummaryOutcomeBatchPending, nil
}

// priorFailures runs stmt, which selects the attempts column of the attempt row for the same
// input, prompt version and model as the new attempt, and returns its consecutive failures, or 0
// when there's no such row (the last attempt was for something else, or there was none).
func priorFailures(ctx context.Context, txn *spanner.ReadWriteTransaction, stmt spanner.Statement) (int64, error) {
	iter := txn.Query(ctx, stmt)
	defer iter.Stop()
	row, err := iter.Next()
	if errors.Is(err, iterator.Done) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("read summary attempt: %w", err)
	}
	var failures int64
	if err = row.Columns(&failures); err != nil {
		return 0, fmt.Errorf("read summary attempt: %w", err)
	}
	return failures, nil
}

// nextSummaryAttempt returns an attempt's consecutive-failure count and when the same text,
// prompt and model may be tried again. Success resets the count; blocked is never retried
// (NULL); other failures wait 1 h, 4 h, then 24 h each time.
func nextSummaryAttempt(outcome string, prevFailures int64, at time.Time) (int64, spanner.NullTime) {
	switch outcome {
	case repository.SummaryOutcomeOK:
		return 0, spanner.NullTime{}
	case repository.SummaryOutcomeBlocked:
		return prevFailures + 1, spanner.NullTime{}
	}
	failures := prevFailures + 1
	return failures, spanner.NullTime{Time: at.Add(summaryRetryDelay(failures)), Valid: true}
}

// summaryRetryDelay is the wait after the nth consecutive failure.
func summaryRetryDelay(failures int64) time.Duration {
	switch failures {
	case 1:
		return summaryRetryFirst
	case 2: //nolint:mnd // the second failure
		return summaryRetrySecond
	default:
		return summaryRetryParked
	}
}

// CountSummaryAttemptsSince counts the bills and diffs whose latest attempt is at or after since,
// using idx_summary_attempts_attempted_at and idx_diff_summary_attempts_attempted_at. Batch attempts
// (holds and imported results) don't count: the daily cap is the synchronous job's, and a batch's
// size is bounded when it's submitted.
func (s *PipelineStoreImpl) CountSummaryAttemptsSince(ctx context.Context, since time.Time) (int, error) {
	iter := s.client.Single().Query(ctx, spanner.Statement{
		SQL: `SELECT
			(SELECT COUNT(*) FROM summary_attempts@{FORCE_INDEX=idx_summary_attempts_attempted_at}
			 WHERE attempted_at >= @since AND IFNULL(request_type, '') != @batch)
			+ (SELECT COUNT(*) FROM diff_summary_attempts@{FORCE_INDEX=idx_diff_summary_attempts_attempted_at}
			 WHERE attempted_at >= @since)`,
		Params: map[string]any{paramSince: since, "batch": repository.SummaryRequestBatch},
	})
	defer iter.Stop()
	row, err := iter.Next()
	if err != nil {
		return 0, fmt.Errorf("count summary attempts: %w", err)
	}
	var n int64
	if err = row.Columns(&n); err != nil {
		return 0, fmt.Errorf("count summary attempts: %w", err)
	}
	return int(n), nil
}

// emptyToNull stores an empty string as NULL.
func emptyToNull(s string) spanner.NullString {
	return spanner.NullString{StringVal: s, Valid: s != ""}
}
