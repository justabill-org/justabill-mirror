package spannerdb

import (
	"encoding/json"
	"fmt"
	"time"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/repository"
)

// summaryRuleColumns are LoadBillContext's columns for the rule a CRA resolution disapproves
// (docs/design/590-cra-disapproved-rules.md, "Prompt context"): the bill's bill_cra_rules row, an
// array of at most one, and the Federal Register documents it names, matched to the row by
// document number in Go.
const summaryRuleColumns = `ARRAY(SELECT AS STRUCT r.rule_title, r.rule_agency, r.document_number,
				r.withdrawn_document_number, r.context_hash
				FROM bill_cra_rules r WHERE r.bill_id = b.bill_id) AS cra_rule,
			ARRAY(SELECT AS STRUCT d.document_number, d.title, TO_JSON_STRING(d.agencies) AS agencies,
				d.doc_type, d.action, d.citation, d.publication_date, d.effective_on, d.abstract
				FROM bill_cra_rules r JOIN federal_register_documents d
					ON d.document_number IN (r.document_number, r.withdrawn_document_number)
				WHERE r.bill_id = b.bill_id) AS cra_documents`

// summaryRuleRow is a bill_cra_rules row in a summaryContextRow.
type summaryRuleRow struct {
	Title          string             `spanner:"rule_title"`
	Agency         string             `spanner:"rule_agency"`
	DocumentNumber spanner.NullString `spanner:"document_number"`
	WithdrawnNum   spanner.NullString `spanner:"withdrawn_document_number"`
	ContextHash    string             `spanner:"context_hash"`
}

// summaryRuleDoc is a federal_register_documents row in a summaryContextRow. Agencies is the JSON
// column as a string.
type summaryRuleDoc struct {
	DocumentNumber string             `spanner:"document_number"`
	Title          string             `spanner:"title"`
	Agencies       string             `spanner:"agencies"`
	DocType        string             `spanner:"doc_type"`
	Action         spanner.NullString `spanner:"action"`
	Citation       string             `spanner:"citation"`
	Published      spanner.NullDate   `spanner:"publication_date"`
	EffectiveOn    spanner.NullDate   `spanner:"effective_on"`
	Abstract       spanner.NullString `spanner:"abstract"`
}

// summaryRule is the rule context of a bill's bill_cra_rules row and its documents, or nil when
// the bill has no row. A document number whose document is missing leaves that document nil, so a
// matched row without its document is described as unmatched.
func summaryRule(rows []*summaryRuleRow, docs []*summaryRuleDoc) (*repository.SummaryRuleContext, error) {
	if len(rows) == 0 || rows[0] == nil {
		return nil, nil //nolint:nilnil // no row: the bill isn't a checked CRA resolution
	}
	r := rows[0]
	byNumber := make(map[string]*summaryRuleDoc, len(docs))
	for _, d := range docs {
		if d != nil {
			byNumber[d.DocumentNumber] = d
		}
	}
	doc, err := summaryRuleDocument(r.DocumentNumber, byNumber)
	if err != nil {
		return nil, err
	}
	withdrawn, err := summaryRuleDocument(r.WithdrawnNum, byNumber)
	if err != nil {
		return nil, err
	}
	return &repository.SummaryRuleContext{
		Title: r.Title, Agency: r.Agency, Document: doc, Withdrawn: withdrawn, ContextHash: r.ContextHash,
	}, nil
}

// summaryRuleDocument is the document numbered num, or nil when num is NULL or not loaded.
func summaryRuleDocument(
	num spanner.NullString, byNumber map[string]*summaryRuleDoc,
) (*repository.SummaryRuleDocument, error) {
	d := byNumber[num.StringVal]
	if !num.Valid || d == nil {
		return nil, nil //nolint:nilnil // no document is a valid outcome
	}
	var agencies []repository.FRAgency
	if err := json.Unmarshal([]byte(d.Agencies), &agencies); err != nil {
		return nil, fmt.Errorf("document %s agencies: %w", d.DocumentNumber, err)
	}
	names := make([]string, 0, len(agencies))
	for _, a := range agencies {
		names = append(names, a.Name)
	}
	out := &repository.SummaryRuleDocument{
		Title: d.Title, Agencies: names, DocType: d.DocType, Action: d.Action.StringVal,
		Citation: d.Citation, Abstract: d.Abstract.StringVal, EffectiveOn: nullDatePtr(d.EffectiveOn),
	}
	if d.Published.Valid {
		out.Published = d.Published.Date.In(time.UTC)
	}
	return out, nil
}
