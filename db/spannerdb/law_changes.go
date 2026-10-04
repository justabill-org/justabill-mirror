package spannerdb

import (
	"context"
	"errors"
	"fmt"
	"time"

	"cloud.google.com/go/spanner"
	"google.golang.org/api/iterator"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
)

// The law-change explanation queue (docs/design/149-law-aware-assistant.md, "Explanations"): one
// Gemini call per bill whose latest text changes the law, on the summary queue's contract.

// lawChangeDueSQL finds each bill's latest version with stored text, by latestVersionOrder, and
// keeps the bills whose version amends, repeals or adds a section of the US Code (a statutory
// note included). A bill whose only changes are to "nonusc:" laws isn't due: the site shows no
// explanation for those (#538), so it makes no call and records no attempt. It drops the bills
// whose explanations are of that text and prompt and older than every loaded section they cover,
// and those an attempt for the same text, prompt and model holds back. A new US Code release
// point makes a bill due only when it rewrote one of the bill's sections. Each bill carries its
// queue tier (#748): 0 for a law (its status is became_law, or Congress.gov lists the laws it
// became, which catches a law whose status lags), 1 for a bill that passed both chambers and isn't
// law yet, 2 for the rest. Like the summary queue (fetchedVersionsSQL), it reads a text's
// content_hash from idx_bill_texts_version alone.
const lawChangeDueSQL = `WITH latest AS (
	SELECT b.bill_id, b.status_date,
		CASE WHEN b.current_status = @becameLaw OR b.laws IS NOT NULL THEN 0
			WHEN b.current_status IN UNNEST(@passedBoth) THEN 1
			ELSE 2 END AS tier,
		EXISTS(SELECT 1 FROM congressional_votes cv WHERE cv.bill_id = b.bill_id) AS voted,
		(SELECT AS STRUCT btv.version_id, bt.content_hash
		 FROM bill_text_versions btv
		 JOIN bill_texts@{FORCE_INDEX=idx_bill_texts_version} bt ON bt.version_id = btv.version_id
		 WHERE btv.bill_id = b.bill_id
		 ORDER BY ` + latestVersionOrder + ` LIMIT 1) AS v
	FROM bills b
	WHERE b.congress = @congress
), due AS (
	SELECT l.bill_id, l.status_date, l.tier, l.voted, l.v.version_id AS version_id, l.v.content_hash AS content_hash
	FROM latest l
	LEFT JOIN law_change_attempts a ON a.bill_id = l.bill_id
	WHERE l.v.version_id IS NOT NULL
		AND EXISTS(SELECT 1 FROM bill_law_refs r
			WHERE r.bill_id = l.bill_id AND r.version_id = l.v.version_id AND r.ref_kind != @citesKind
				AND NOT STARTS_WITH(r.section_id, @nonUSCPrefix))
		AND NOT (
			EXISTS(SELECT 1 FROM bill_law_changes c
				WHERE c.bill_id = l.bill_id AND c.source_content_hash = l.v.content_hash
					AND c.prompt_version = @lawPrompt)
			AND NOT EXISTS(SELECT 1 FROM bill_law_changes c
				JOIN usc_sections s ON s.section_id = c.section_id
				WHERE c.bill_id = l.bill_id AND s.updated_at > c.generated_at))
		AND NOT (a.bill_id IS NOT NULL AND a.outcome != @okOutcome AND a.content_hash = l.v.content_hash
			AND a.prompt_version = @lawPrompt AND a.model = @lawModel
			AND (a.next_attempt_at IS NULL OR a.next_attempt_at > @now))
)
`

// lawChangeQueueSQL returns the due bills in queue order: laws first, then the bills that passed
// both chambers, each newest status_date first, so the day's calls explain what is (or is about to
// be) law. The rest keep the order from before tiers: roll-call-voted bills first, then newest.
// A NULL status_date sorts last within its tier; bill_id makes ties stable.
const lawChangeQueueSQL = lawChangeDueSQL + `SELECT bill_id, version_id, content_hash FROM due
ORDER BY tier, IF(tier = 2, voted, FALSE) DESC, status_date DESC, bill_id
LIMIT @lim`

// lawChangeBacklogSQL counts the due bills.
const lawChangeBacklogSQL = lawChangeDueSQL + `SELECT COUNT(*) FROM due`

// lawChangeBillSQL reads the bill, the version's text hash and the bill's AI short summary.
const lawChangeBillSQL = `SELECT b.bill_id, b.congress, b.bill_type, b.number, b.title, bs.short_summary,
	btv.version_id, btv.version_code, bt.content_hash
FROM bills b
JOIN bill_text_versions btv ON btv.bill_id = b.bill_id
JOIN bill_texts bt ON bt.version_id = btv.version_id
LEFT JOIN bill_summaries bs ON bs.bill_id = b.bill_id
WHERE b.bill_id = @billID AND btv.version_id = @lawVersion`

// lawChangeRefsSQL reads the version's references that change the law, with each section's
// current text when it's loaded. It keeps "nonusc:" references, which the job stores without an
// explanation.
const lawChangeRefsSQL = `SELECT r.section_id, r.ref_kind, r.subsection_path, r.instruction, r.bill_section_ref,
	s.section_id IS NOT NULL AS loaded, s.heading, s.text
FROM bill_law_refs r
LEFT JOIN usc_sections s ON s.section_id = r.section_id
WHERE r.bill_id = @billID AND r.version_id = @lawVersion AND r.ref_kind != @citesKind
ORDER BY r.bill_section_ref, r.section_id, r.ref_kind`

type lawChangeBillRow struct {
	BillID       string             `spanner:"bill_id"`
	Congress     int64              `spanner:"congress"`
	BillType     string             `spanner:"bill_type"`
	Number       int64              `spanner:"number"`
	Title        string             `spanner:"title"`
	ShortSummary spanner.NullString `spanner:"short_summary"`
	VersionID    string             `spanner:"version_id"`
	VersionCode  string             `spanner:"version_code"`
	ContentHash  string             `spanner:"content_hash"`
}

type lawChangeRefRow struct {
	SectionID      string             `spanner:"section_id"`
	RefKind        string             `spanner:"ref_kind"`
	SubsectionPath spanner.NullString `spanner:"subsection_path"`
	Instruction    spanner.NullString `spanner:"instruction"`
	BillSectionRef spanner.NullString `spanner:"bill_section_ref"`
	Loaded         bool               `spanner:"loaded"`
	Heading        spanner.NullString `spanner:"heading"`
	Text           spanner.NullString `spanner:"text"`
}

// lawChangeQueueParams are lawChangeDueSQL's parameters for q.
func lawChangeQueueParams(q repository.LawChangeQueueQuery) map[string]any {
	return map[string]any{
		paramCongress:  int64(q.Congress),
		paramBecameLaw: statusBecameLaw,
		"passedBoth":   []string{statusToPresident, statusSigned, statusVetoed},
		paramCitesKind: model.LawRefCites,
		"nonUSCPrefix": model.NonUSCSectionPrefix,
		"lawPrompt":    q.PromptVersion,
		"lawModel":     q.Model,
		"okOutcome":    repository.SummaryOutcomeOK,
		paramNow:       q.Now,
	}
}

// QueryBillsToExplainLaw returns the bills due for a law-change explanation; see
// [repository.PipelineStore].
func (s *PipelineStoreImpl) QueryBillsToExplainLaw(
	ctx context.Context, q repository.LawChangeQueueQuery,
) ([]repository.LawChangeQueueItem, error) {
	stmt := spanner.Statement{SQL: lawChangeQueueSQL, Params: lawChangeQueueParams(q)}
	stmt.Params[paramLimit] = int64(q.Limit)

	iter := s.client.Single().Query(ctx, stmt)
	defer iter.Stop()

	var items []repository.LawChangeQueueItem
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return items, nil
		}
		if err != nil {
			return nil, fmt.Errorf("query bills to explain law: %w", err)
		}
		var item repository.LawChangeQueueItem
		if err = row.Columns(&item.BillID, &item.VersionID, &item.ContentHash); err != nil {
			return nil, fmt.Errorf("read law change queue row: %w", err)
		}
		items = append(items, item)
	}
}

// CountBillsToExplainLaw counts the bills due for a law-change explanation.
func (s *PipelineStoreImpl) CountBillsToExplainLaw(
	ctx context.Context, q repository.LawChangeQueueQuery,
) (int, error) {
	iter := s.client.Single().Query(ctx, spanner.Statement{SQL: lawChangeBacklogSQL, Params: lawChangeQueueParams(q)})
	defer iter.Stop()
	row, err := iter.Next()
	if err != nil {
		return 0, fmt.Errorf("count bills to explain law: %w", err)
	}
	var n int64
	if err = row.Columns(&n); err != nil {
		return 0, fmt.Errorf("count bills to explain law: %w", err)
	}
	return int(n), nil
}

// LoadLawChangeContext loads the bill, the version's text hash and its references that change
// the law, in one read-only transaction. It returns an error wrapping [spanner.ErrRowNotFound]
// when the bill, the version or its text doesn't exist.
func (s *PipelineStoreImpl) LoadLawChangeContext(
	ctx context.Context, billID, versionID string,
) (*repository.LawChangeContext, error) {
	txn := s.client.ReadOnlyTransaction()
	defer txn.Close()
	params := map[string]any{paramBillID: billID, "lawVersion": versionID, "citesKind": model.LawRefCites}

	bills, err := queryRows(ctx, txn, spanner.Statement{SQL: lawChangeBillSQL, Params: params}, "law change bill",
		func(r lawChangeBillRow) repository.LawChangeContext {
			return repository.LawChangeContext{
				BillID: r.BillID, Congress: int(r.Congress), BillType: r.BillType, Number: int(r.Number),
				Title: r.Title, ShortSummary: r.ShortSummary.StringVal,
				VersionID: r.VersionID, VersionCode: r.VersionCode, ContentHash: r.ContentHash,
			}
		})
	if err != nil {
		return nil, err
	}
	if len(bills) == 0 {
		return nil, fmt.Errorf("bill %s version %s with text: %w", billID, versionID, spanner.ErrRowNotFound)
	}
	out := &bills[0]
	out.Refs, err = queryRows(ctx, txn, spanner.Statement{SQL: lawChangeRefsSQL, Params: params}, "law change refs",
		func(r lawChangeRefRow) repository.LawChangeRef {
			return repository.LawChangeRef{
				SectionID: r.SectionID, RefKind: r.RefKind, SubsectionPath: r.SubsectionPath.StringVal,
				Instruction: r.Instruction.StringVal, BillSectionRef: r.BillSectionRef.StringVal,
				Loaded: r.Loaded, Heading: r.Heading.StringVal, CurrentText: r.Text.StringVal,
			}
		})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// lawChangeAttemptRecord is a law_change_attempts row: summary_attempts' columns before it gained
// the batch path's request_type and batch_id.
type lawChangeAttemptRecord struct {
	BillID        string             `spanner:"bill_id"`
	ContentHash   string             `spanner:"content_hash"`
	PromptVersion string             `spanner:"prompt_version"`
	Model         string             `spanner:"model"`
	Outcome       string             `spanner:"outcome"`
	Reason        spanner.NullString `spanner:"reason"`
	Attempts      int64              `spanner:"attempts"`
	AttemptedAt   time.Time          `spanner:"attempted_at"`
	NextAttemptAt spanner.NullTime   `spanner:"next_attempt_at"`
}

// RecordLawChangeAttempt replaces the bill's law_change_attempts row and, when a.Changes is set,
// the bill's explanations, in one transaction; see [repository.PipelineStore].
func (s *PipelineStoreImpl) RecordLawChangeAttempt(ctx context.Context, a repository.LawChangeAttemptRow) error {
	_, err := s.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		failures, err := priorFailures(ctx, txn, spanner.Statement{
			SQL: `SELECT attempts FROM law_change_attempts
			 WHERE bill_id = @billID AND content_hash = @hash AND prompt_version = @prompt AND model = @model`,
			Params: map[string]any{
				paramBillID: a.BillID, paramHash: a.ContentHash, paramPrompt: a.PromptVersion, paramModel: a.Model,
			},
		})
		if err != nil {
			return err
		}
		attempts, next := nextSummaryAttempt(a.Outcome, failures, a.AttemptedAt)
		attempt, err := spanner.InsertOrUpdateStruct("law_change_attempts", lawChangeAttemptRecord{
			BillID: a.BillID, ContentHash: a.ContentHash, PromptVersion: a.PromptVersion, Model: a.Model,
			Outcome: a.Outcome, Reason: emptyToNull(a.Reason), Attempts: attempts,
			AttemptedAt: a.AttemptedAt, NextAttemptAt: next,
		})
		if err != nil {
			return fmt.Errorf("law change attempt mutation: %w", err)
		}
		muts := []*spanner.Mutation{attempt}
		if a.Changes != nil {
			changes, chErr := lawChangeMutations(a.BillID, a.Changes)
			if chErr != nil {
				return chErr
			}
			muts = append(muts, changes...)
		}
		return txn.BufferWrite(muts)
	})
	if err != nil {
		return fmt.Errorf("record law change attempt for %s: %w", a.BillID, err)
	}
	return nil
}

// CountLawChangeAttemptsSince counts the bills whose latest law-change attempt is at or after
// since, using idx_law_change_attempts_attempted_at.
func (s *PipelineStoreImpl) CountLawChangeAttemptsSince(ctx context.Context, since time.Time) (int, error) {
	iter := s.client.Single().Query(ctx, spanner.Statement{
		SQL: `SELECT COUNT(*) FROM law_change_attempts@{FORCE_INDEX=idx_law_change_attempts_attempted_at}
		 WHERE attempted_at >= @since`,
		Params: map[string]any{paramSince: since},
	})
	defer iter.Stop()
	row, err := iter.Next()
	if err != nil {
		return 0, fmt.Errorf("count law change attempts: %w", err)
	}
	var n int64
	if err = row.Columns(&n); err != nil {
		return 0, fmt.Errorf("count law change attempts: %w", err)
	}
	return int(n), nil
}
