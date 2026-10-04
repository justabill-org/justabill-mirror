package repository

import (
	"time"
)

// CRATitlePattern finds a Congressional Review Act resolution by its title
// (docs/design/590-cra-disapproved-rules.md): "under chapter 8 of title 5", or a disapproval of a
// rule "submitted by" or "issued by" an agency. It's RE2, so the pipeline's detection and
// [PipelineStore.ListCRARuleChecks]' REGEXP_CONTAINS use the same pattern. Only joint resolutions
// (sjres, hjres) are CRA resolutions; arms-sale and D.C. Council disapprovals don't match it.
const CRATitlePattern = `(?i)chapter\s+8\s+of\s+title\s+5\b|` +
	`disapprov\w*\s+(?:of\s+)?the\s+(?:\w+\s+){0,2}rule\s+(?:submitted|issued)\s+by`

// CRARecheckAfter is how long an unmatched CRA resolution stays checked before
// [PipelineStore.ListCRARuleChecks] returns it again, in case the Federal Register now has it.
const CRARecheckAfter = 30 * 24 * time.Hour

// Values of bill_cra_rules.status.
const (
	CRAStatusMatched   = "matched"
	CRAStatusUnmatched = "unmatched"
)

// Values of bill_cra_rules.method, set when the status is matched.
const (
	CRAMethodCitation = "citation"
	CRAMethodTitle    = "title"
)

// Values of bill_cra_rules.reason, set when the status is unmatched.
const (
	CRAReasonNoCandidates = "no_candidates"
	CRAReasonAmbiguous    = "ambiguous"
	CRAReasonCiteMismatch = "cite_mismatch"
	CRAReasonUnparsed     = "unparsed"
)

// CRARuleCheck is a CRA resolution that [PipelineStore.ListCRARuleChecks] says is due for a
// Federal Register lookup, with what the lookup reads: its title, introduction date and latest
// stored text.
type CRARuleCheck struct {
	BillID         string
	BillType       string
	Number         int
	Title          string
	IntroducedDate *time.Time
	// TextHash is the latest stored text's bill_texts.content_hash, nil when the bill has no
	// stored text yet; it goes back as [CRARuleRow].SourceTextHash.
	TextHash *string
	// Text is that text as stored (bill XML or HTML, decompressed), empty when TextHash is nil.
	Text string
}

// FRAgency is one agency of a Federal Register document, in the document's order.
type FRAgency struct {
	Name string `json:"name"`
	Slug string `json:"slug,omitempty"`
}

// FRDocumentRow holds a Federal Register document for federal_register_documents. Text fields
// are plain text; the URLs are already checked to be on www.federalregister.gov (HTMLURL) and
// www.govinfo.gov (PDFURL). ContentHash is the sha256 of the other fields, computed by the
// caller, so [PipelineStore.UpsertFRDocument] can skip an unchanged document.
type FRDocumentRow struct {
	DocumentNumber      string
	Citation            string
	Volume              int
	StartPage           int
	EndPage             int
	DocType             string
	Action              *string
	Title               string
	Agencies            []FRAgency
	PublicationDate     time.Time
	EffectiveOn         *time.Time
	Abstract            *string
	HTMLURL             string
	PDFURL              *string
	DocketID            *string
	RegulationIDNumbers []string
	ContentHash         string
}

// CRARuleRow holds one bill_cra_rules row: what a CRA resolution names and what was matched.
// Status is a CRAStatus value; Method is set when matched and Reason when unmatched.
// DocumentNumber and WithdrawnDocumentNumber name federal_register_documents rows written first.
// SourceTextHash is the [CRARuleCheck].TextHash the row was parsed from (nil: from the title).
// ContextHash is the sha256 of the summary prompt block's inputs.
type CRARuleRow struct {
	BillID                  string
	RuleTitle               string
	RuleAgency              string
	Cited                   *string
	GAOOpinion              bool
	Status                  string
	Method                  *string
	Reason                  *string
	DocumentNumber          *string
	WithdrawnDocumentNumber *string
	SourceTextHash          *string
	MatcherVersion          string
	ContextHash             string
}
