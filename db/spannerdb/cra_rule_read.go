package spannerdb

import (
	"context"
	"errors"
	"fmt"
	"time"

	"cloud.google.com/go/civil"
	"cloud.google.com/go/spanner"
	"google.golang.org/api/iterator"

	"github.com/justabill-org/justabill/db/model"
)

// frDocumentFields selects federal_register_documents rows aliased d as STRUCTs for
// frDocumentRead, their agencies as their names in order. Spanner returns a STRUCT only inside an
// ARRAY, so craRuleSQL reads each document as an ARRAY of at most one.
const frDocumentFields = `SELECT AS STRUCT d.document_number, d.citation, d.doc_type, d.action, d.title,
		ARRAY(SELECT JSON_VALUE(a, '$.name') FROM UNNEST(JSON_QUERY_ARRAY(d.agencies)) AS a WITH OFFSET o
		      ORDER BY o) AS agencies,
		d.publication_date, d.effective_on, d.abstract, d.html_url, d.pdf_url, d.docket_id
	FROM federal_register_documents d`

// craRuleSQL reads a bill's bill_cra_rules row with the documents it names, in one query.
const craRuleSQL = `SELECT r.status, r.method, r.reason, r.rule_title, r.rule_agency, r.cited, r.gao_opinion,
	ARRAY(` + frDocumentFields + ` WHERE d.document_number = r.document_number) AS document,
	ARRAY(` + frDocumentFields + ` WHERE d.document_number = r.withdrawn_document_number) AS withdrawn,
	r.checked_at
FROM bill_cra_rules r
WHERE r.bill_id = @billID`

// frDocumentRead is one STRUCT of frDocumentFields.
type frDocumentRead struct {
	DocumentNumber  string               `spanner:"document_number"`
	Citation        string               `spanner:"citation"`
	DocType         string               `spanner:"doc_type"`
	Action          spanner.NullString   `spanner:"action"`
	Title           string               `spanner:"title"`
	Agencies        []spanner.NullString `spanner:"agencies"`
	PublicationDate civil.Date           `spanner:"publication_date"`
	EffectiveOn     spanner.NullDate     `spanner:"effective_on"`
	Abstract        spanner.NullString   `spanner:"abstract"`
	HTMLURL         string               `spanner:"html_url"`
	PDFURL          spanner.NullString   `spanner:"pdf_url"`
	DocketID        spanner.NullString   `spanner:"docket_id"`
}

// craRuleRead is one row of craRuleSQL.
type craRuleRead struct {
	Status     string             `spanner:"status"`
	Method     spanner.NullString `spanner:"method"`
	Reason     spanner.NullString `spanner:"reason"`
	RuleTitle  string             `spanner:"rule_title"`
	RuleAgency string             `spanner:"rule_agency"`
	Cited      spanner.NullString `spanner:"cited"`
	GAOOpinion bool               `spanner:"gao_opinion"`
	Document   []*frDocumentRead  `spanner:"document"`
	Withdrawn  []*frDocumentRead  `spanner:"withdrawn"`
	CheckedAt  time.Time          `spanner:"checked_at"`
}

// GetCRARule implements [repository.BillRepo]. A document the row names but
// federal_register_documents lacks reads as nil. SearchURL is left for the API to set.
func (r *BillRepository) GetCRARule(ctx context.Context, billID string) (*model.CRARule, error) {
	stmt := spanner.Statement{SQL: craRuleSQL, Params: map[string]any{paramBillID: billID}}
	iter := r.client.Single().Query(ctx, stmt)
	defer iter.Stop()

	row, err := iter.Next()
	if errors.Is(err, iterator.Done) {
		return nil, nil //nolint:nilnil // a bill that isn't a checked CRA resolution has no rule
	}
	if err != nil {
		return nil, fmt.Errorf("get cra rule %s: %w", billID, err)
	}
	var rr craRuleRead
	if err = row.ToStruct(&rr); err != nil {
		return nil, fmt.Errorf("read cra rule %s: %w", billID, err)
	}
	return &model.CRARule{
		Status:            rr.Status,
		Method:            nullStringPtr(rr.Method),
		Reason:            nullStringPtr(rr.Reason),
		RuleTitle:         rr.RuleTitle,
		RuleAgency:        rr.RuleAgency,
		Cited:             nullStringPtr(rr.Cited),
		GAOOpinion:        rr.GAOOpinion,
		Document:          frDocumentModel(rr.Document),
		WithdrawnDocument: frDocumentModel(rr.Withdrawn),
		CheckedAt:         rr.CheckedAt,
	}, nil
}

// frDocumentModel maps the document read as an ARRAY of at most one to its API shape, with its
// dates as YYYY-MM-DD; no document is nil.
func frDocumentModel(docs []*frDocumentRead) *model.FRDocument {
	if len(docs) == 0 || docs[0] == nil {
		return nil
	}
	d := docs[0]
	agencies := make([]string, 0, len(d.Agencies))
	for _, a := range d.Agencies {
		if a.Valid {
			agencies = append(agencies, a.StringVal)
		}
	}
	var effective *string
	if d.EffectiveOn.Valid {
		s := d.EffectiveOn.Date.String()
		effective = &s
	}
	html := d.HTMLURL
	return &model.FRDocument{
		DocumentNumber:  d.DocumentNumber,
		Citation:        d.Citation,
		Type:            d.DocType,
		Action:          nullStringPtr(d.Action),
		Title:           d.Title,
		Agencies:        agencies,
		PublicationDate: d.PublicationDate.String(),
		EffectiveOn:     effective,
		Abstract:        nullStringPtr(d.Abstract),
		HTMLURL:         &html,
		PDFURL:          nullStringPtr(d.PDFURL),
		DocketID:        nullStringPtr(d.DocketID),
	}
}
