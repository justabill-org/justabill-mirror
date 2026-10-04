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

// Tables of the CRA rule lookup (docs/design/590-cra-disapproved-rules.md).
const (
	tableFRDocuments = "federal_register_documents"
	tableBillCRARule = "bill_cra_rules"
	colStatus        = "status"
)

// craRuleChecksSQL lists a congress's due CRA resolutions with their latest stored text. The
// latest version is found by latestVersionOrder; its text is joined only for the due rows.
const craRuleChecksSQL = `WITH cra AS (
	SELECT b.bill_id, b.bill_type, b.number, b.title, b.introduced_date,
		(SELECT AS STRUCT btv.version_id, bt.content_hash
		 FROM bill_text_versions btv JOIN bill_texts bt ON bt.version_id = btv.version_id
		 WHERE btv.bill_id = b.bill_id
		 ORDER BY ` + latestVersionOrder + ` LIMIT 1) AS v
	FROM bills b
	WHERE b.congress = @congress AND b.bill_type IN ('sjres', 'hjres') AND REGEXP_CONTAINS(b.title, @pattern)
)
SELECT c.bill_id, c.bill_type, c.number, c.title, c.introduced_date, c.v.content_hash, bt.content, bt.content_gz
FROM cra c
LEFT JOIN bill_cra_rules r ON r.bill_id = c.bill_id
LEFT JOIN bill_texts bt ON bt.version_id = c.v.version_id
WHERE r.bill_id IS NULL
	OR IFNULL(r.source_text_hash, '') != IFNULL(c.v.content_hash, '')
	OR r.matcher_version != @matcher
	OR (r.status = @unmatched AND r.checked_at < @recheck_before)
ORDER BY r.bill_id IS NULL DESC, c.introduced_date DESC, c.bill_id`

// ListCRARuleChecks implements [repository.PipelineStore]. Bills with no introduced_date come
// last in each group.
func (s *PipelineStoreImpl) ListCRARuleChecks(
	ctx context.Context, congressNum int, matcherVersion string, limit int,
) ([]repository.CRARuleCheck, error) {
	stmt := spanner.Statement{SQL: craRuleChecksSQL, Params: map[string]any{
		paramCongress:    int64(congressNum),
		"pattern":        repository.CRATitlePattern,
		"matcher":        matcherVersion,
		"unmatched":      repository.CRAStatusUnmatched,
		"recheck_before": time.Now().Add(-repository.CRARecheckAfter),
	}}
	if limit > 0 {
		stmt.SQL += limitClause
		stmt.Params[paramLimit] = int64(limit)
	}
	iter := s.client.Single().Query(ctx, stmt)
	defer iter.Stop()

	var out []repository.CRARuleCheck
	for {
		row, err := iter.Next()
		if errors.Is(err, iterator.Done) {
			return out, nil
		}
		if err != nil {
			return nil, fmt.Errorf("list cra rule checks: %w", err)
		}
		c, err := readCRARuleCheck(row)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
}

// readCRARuleCheck reads one row of craRuleChecksSQL.
func readCRARuleCheck(row *spanner.Row) (repository.CRARuleCheck, error) {
	var (
		c          repository.CRARuleCheck
		number     int64
		introduced spanner.NullDate
		hash       spanner.NullString
		content    spanner.NullString
		gz         []byte
	)
	if err := row.Columns(&c.BillID, &c.BillType, &number, &c.Title, &introduced, &hash, &content, &gz); err != nil {
		return c, fmt.Errorf("read cra rule check: %w", err)
	}
	c.Number = int(number)
	c.IntroducedDate = nullDatePtr(introduced)
	c.TextHash = nullStringPtr(hash)
	if c.TextHash == nil {
		return c, nil
	}
	text, err := storedText(content.StringVal, hash.StringVal, gz)
	if err != nil {
		return c, fmt.Errorf("cra rule check %s: %w", c.BillID, err)
	}
	c.Text = text
	return c, nil
}

// frDocumentMut is a federal_register_documents row.
type frDocumentMut struct {
	DocumentNumber      string             `spanner:"document_number"`
	Citation            string             `spanner:"citation"`
	Volume              int64              `spanner:"volume"`
	StartPage           int64              `spanner:"start_page"`
	EndPage             int64              `spanner:"end_page"`
	DocType             string             `spanner:"doc_type"`
	Action              spanner.NullString `spanner:"action"`
	Title               string             `spanner:"title"`
	Agencies            spanner.NullJSON   `spanner:"agencies"`
	PublicationDate     spanner.NullDate   `spanner:"publication_date"`
	EffectiveOn         spanner.NullDate   `spanner:"effective_on"`
	Abstract            spanner.NullString `spanner:"abstract"`
	HTMLURL             string             `spanner:"html_url"`
	PDFURL              spanner.NullString `spanner:"pdf_url"`
	DocketID            spanner.NullString `spanner:"docket_id"`
	RegulationIDNumbers spanner.NullJSON   `spanner:"regulation_id_numbers"`
	ContentHash         string             `spanner:"content_hash"`
	FetchedAt           time.Time          `spanner:"fetched_at"`
}

// newFRDocumentMut maps d to its row, with a commit-timestamp fetched_at.
func newFRDocumentMut(d repository.FRDocumentRow) frDocumentMut {
	agencies := d.Agencies
	if agencies == nil {
		agencies = []repository.FRAgency{}
	}
	m := frDocumentMut{
		DocumentNumber:  d.DocumentNumber,
		Citation:        d.Citation,
		Volume:          int64(d.Volume),
		StartPage:       int64(d.StartPage),
		EndPage:         int64(d.EndPage),
		DocType:         d.DocType,
		Action:          ptrToNullString(d.Action),
		Title:           d.Title,
		Agencies:        spanner.NullJSON{Value: agencies, Valid: true},
		PublicationDate: ptrTimeToCivilDate(&d.PublicationDate),
		EffectiveOn:     ptrTimeToCivilDate(d.EffectiveOn),
		Abstract:        ptrToNullString(d.Abstract),
		HTMLURL:         d.HTMLURL,
		PDFURL:          ptrToNullString(d.PDFURL),
		DocketID:        ptrToNullString(d.DocketID),
		ContentHash:     d.ContentHash,
		FetchedAt:       spanner.CommitTimestamp,
	}
	if d.RegulationIDNumbers != nil {
		m.RegulationIDNumbers = spanner.NullJSON{Value: d.RegulationIDNumbers, Valid: true}
	}
	return m
}

// UpsertFRDocument implements [repository.PipelineStore].
func (s *PipelineStoreImpl) UpsertFRDocument(ctx context.Context, d repository.FRDocumentRow) (bool, error) {
	m, err := spanner.InsertOrUpdateStruct(tableFRDocuments, newFRDocumentMut(d))
	if err != nil {
		return false, fmt.Errorf("federal register document %s: %w", d.DocumentNumber, err)
	}
	var wrote bool
	_, err = s.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		wrote = false
		row, readErr := txn.ReadRow(ctx, tableFRDocuments, spanner.Key{d.DocumentNumber}, []string{"content_hash"})
		switch {
		case errors.Is(readErr, spanner.ErrRowNotFound):
		case readErr != nil:
			return readErr
		default:
			var hash string
			if readErr = row.Columns(&hash); readErr != nil {
				return readErr
			}
			if hash == d.ContentHash {
				return nil
			}
		}
		wrote = true
		return txn.BufferWrite([]*spanner.Mutation{m})
	})
	if err != nil {
		return false, fmt.Errorf("upsert federal register document %s: %w", d.DocumentNumber, err)
	}
	return wrote, nil
}

// craRuleMut is a bill_cra_rules row.
type craRuleMut struct {
	BillID                  string             `spanner:"bill_id"`
	RuleTitle               string             `spanner:"rule_title"`
	RuleAgency              string             `spanner:"rule_agency"`
	Cited                   spanner.NullString `spanner:"cited"`
	GAOOpinion              bool               `spanner:"gao_opinion"`
	Status                  string             `spanner:"status"`
	Method                  spanner.NullString `spanner:"method"`
	Reason                  spanner.NullString `spanner:"reason"`
	DocumentNumber          spanner.NullString `spanner:"document_number"`
	WithdrawnDocumentNumber spanner.NullString `spanner:"withdrawn_document_number"`
	SourceTextHash          spanner.NullString `spanner:"source_text_hash"`
	MatcherVersion          string             `spanner:"matcher_version"`
	ContextHash             string             `spanner:"context_hash"`
	CheckedAt               time.Time          `spanner:"checked_at"`
}

// craRuleShownColumns returns the bill_cra_rules columns a reader sees, directly or through the
// summary's context hash: a change in any of them is a change to the bill.
func craRuleShownColumns() []string {
	return []string{"rule_title", "rule_agency", "cited", "gao_opinion", colStatus, "method", "reason",
		"document_number", "withdrawn_document_number", "context_hash"}
}

// UpsertCRARule implements [repository.PipelineStore].
func (s *PipelineStoreImpl) UpsertCRARule(ctx context.Context, r repository.CRARuleRow) (bool, error) {
	next := craRuleMut{
		BillID:                  r.BillID,
		RuleTitle:               r.RuleTitle,
		RuleAgency:              r.RuleAgency,
		Cited:                   ptrToNullString(r.Cited),
		GAOOpinion:              r.GAOOpinion,
		Status:                  r.Status,
		Method:                  ptrToNullString(r.Method),
		Reason:                  ptrToNullString(r.Reason),
		DocumentNumber:          ptrToNullString(r.DocumentNumber),
		WithdrawnDocumentNumber: ptrToNullString(r.WithdrawnDocumentNumber),
		SourceTextHash:          ptrToNullString(r.SourceTextHash),
		MatcherVersion:          r.MatcherVersion,
		ContextHash:             r.ContextHash,
		CheckedAt:               spanner.CommitTimestamp,
	}
	m, err := spanner.InsertOrUpdateStruct(tableBillCRARule, next)
	if err != nil {
		return false, fmt.Errorf("cra rule %s: %w", r.BillID, err)
	}
	var changed bool
	_, err = s.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		changed = true
		row, readErr := txn.ReadRow(ctx, tableBillCRARule, spanner.Key{r.BillID}, craRuleShownColumns())
		switch {
		case errors.Is(readErr, spanner.ErrRowNotFound):
		case readErr != nil:
			return readErr
		default:
			var prev craRuleMut
			if readErr = row.ToStruct(&prev); readErr != nil {
				return readErr
			}
			prev.BillID, prev.SourceTextHash, prev.MatcherVersion, prev.CheckedAt =
				next.BillID, next.SourceTextHash, next.MatcherVersion, next.CheckedAt
			changed = prev != next
		}
		return txn.BufferWrite([]*spanner.Mutation{m})
	})
	if err != nil {
		return false, fmt.Errorf("upsert cra rule %s: %w", r.BillID, err)
	}
	return changed, nil
}
