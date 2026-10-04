package model

import "time"

// CRARule is the rule a Congressional Review Act resolution disapproves, from its bill_cra_rules
// row and the Federal Register documents it names (docs/design/590-cra-disapproved-rules.md).
// RuleTitle, RuleAgency and Cited are as the resolution writes them. Status is "matched" or
// "unmatched"; Method ("citation" or "title") is set when matched, and Reason ("no_candidates",
// "ambiguous", "cite_mismatch" or "unparsed") when unmatched. Document is nil when unmatched;
// WithdrawnDocument is set only when the disapproved rule withdraws another document. SearchURL is
// a Federal Register search for the rule, set by the API rather than stored.
type CRARule struct {
	Status            string      `json:"status"`
	Method            *string     `json:"method,omitempty"`
	Reason            *string     `json:"reason,omitempty"`
	RuleTitle         string      `json:"rule_title"`
	RuleAgency        string      `json:"rule_agency"`
	Cited             *string     `json:"cited"`
	GAOOpinion        bool        `json:"gao_opinion"`
	Document          *FRDocument `json:"document"`
	WithdrawnDocument *FRDocument `json:"withdrawn_document"`
	SearchURL         string      `json:"search_url"`
	CheckedAt         time.Time   `json:"checked_at"`
}

// FRDocument is a Federal Register document as it was published: plain text, never HTML.
// Citation is the Federal Register's own ("89 FR 106768"), Type its document type ("Rule"),
// Agencies its agency names in order, and PublicationDate and EffectiveOn dates as YYYY-MM-DD.
// HTMLURL is on www.federalregister.gov and PDFURL (the official edition) on www.govinfo.gov: the
// API serves nil in place of a stored URL on any other host.
type FRDocument struct {
	DocumentNumber  string   `json:"document_number"`
	Citation        string   `json:"citation"`
	Type            string   `json:"type"`
	Action          *string  `json:"action"`
	Title           string   `json:"title"`
	Agencies        []string `json:"agencies"`
	PublicationDate string   `json:"publication_date"`
	EffectiveOn     *string  `json:"effective_on"`
	Abstract        *string  `json:"abstract"`
	HTMLURL         *string  `json:"html_url"`
	PDFURL          *string  `json:"pdf_url"`
	DocketID        *string  `json:"docket_id"`
}
