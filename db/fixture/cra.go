package fixture

import (
	"time"

	"cloud.google.com/go/civil"
	"cloud.google.com/go/spanner"
)

// Congressional Review Act resolutions (docs/design/590-cra-disapproved-rules.md), so the
// browser tests can reach the "The rule this resolution disapproves" card both ways. CRABill's
// rule is matched by its citation to the Federal Register document CRADocument; CRAUnmatchedBill
// names its rule by a GAO opinion and isn't matched. Neither is law, so /vote's default deck
// stays LawBill. All of it is invented, like the rest of the fixture.
const (
	CRABill          = "sjres-119-41"
	CRAUnmatchedBill = "hjres-119-63"

	// CRATitle and CRAUnmatchedTitle are the resolutions' titles.
	CRATitle = `Providing for congressional disapproval under chapter 8 of title 5, United States ` +
		`Code, of the rule submitted by the Office of Lamplight Standards relating to "Lantern Wick ` +
		`Efficiency Standards".`
	CRAUnmatchedTitle = `Providing for congressional disapproval under chapter 8 of title 5, United ` +
		`States Code, of the rule submitted by the Office of Lamplight Standards relating to "Guidance ` +
		`on Reading Room Hours".`

	// CRADocument is the Federal Register document CRABill's rule is matched to, and
	// CRADocumentTitle its title.
	CRADocument      = "2025-90141"
	CRADocumentTitle = "Lantern Wick Efficiency Standards"
)

const (
	craAgency           = "Office of Lamplight Standards"
	craUnmatchedRule    = "Guidance on Reading Room Hours"
	craCitation         = "90 FR 91234"
	craVolume           = 90
	craStartPage        = 91234
	craEndPage          = 91240
	craPDFURL           = "https://www.govinfo.gov/content/pkg/FR-2025-01-15/pdf/" + CRADocument + ".pdf"
	craMatcherVersion   = "cra-v1"
	craStatusMatched    = "matched"
	craStatusUnmatched  = "unmatched"
	craDocumentAbstract = "The Office of Lamplight Standards sets minimum efficiency standards for " +
		"lantern wicks sold for use in public reading rooms, and the test procedure manufacturers " +
		"use to show that a wick meets them."
	craAgencies = `[{"name":"Office of Lamplight Standards","slug":"office-of-lamplight-standards"}]`
	// craContextHash stands in for the hash of the rule context a summary would use: no fixture
	// summary reads it.
	craContextHash = "e2e0000000000000000000000000000000000000000000000000000000000590"
)

type frDocumentRow struct {
	DocumentNumber  string             `spanner:"document_number"`
	Citation        string             `spanner:"citation"`
	Volume          int64              `spanner:"volume"`
	StartPage       int64              `spanner:"start_page"`
	EndPage         int64              `spanner:"end_page"`
	DocType         string             `spanner:"doc_type"`
	Action          spanner.NullString `spanner:"action"`
	Title           string             `spanner:"title"`
	Agencies        spanner.NullJSON   `spanner:"agencies"`
	PublicationDate civil.Date         `spanner:"publication_date"`
	EffectiveOn     spanner.NullDate   `spanner:"effective_on"`
	Abstract        spanner.NullString `spanner:"abstract"`
	HTMLURL         string             `spanner:"html_url"`
	PDFURL          spanner.NullString `spanner:"pdf_url"`
	DocketID        spanner.NullString `spanner:"docket_id"`
	ContentHash     string             `spanner:"content_hash"`
	FetchedAt       time.Time          `spanner:"fetched_at"`
}

type craRuleRow struct {
	BillID         string             `spanner:"bill_id"`
	RuleTitle      string             `spanner:"rule_title"`
	RuleAgency     string             `spanner:"rule_agency"`
	Cited          spanner.NullString `spanner:"cited"`
	GAOOpinion     bool               `spanner:"gao_opinion"`
	Status         string             `spanner:"status"`
	Method         spanner.NullString `spanner:"method"`
	Reason         spanner.NullString `spanner:"reason"`
	DocumentNumber spanner.NullString `spanner:"document_number"`
	MatcherVersion string             `spanner:"matcher_version"`
	ContextHash    string             `spanner:"context_hash"`
	CheckedAt      time.Time          `spanner:"checked_at"`
}

// craFixture is the CRA resolutions, the Federal Register document and the bill_cra_rules rows.
type craFixture struct {
	bills    []any
	document frDocumentRow
	rules    []any
}

// craRows returns CRABill and CRAUnmatchedBill, introduced in the spring, with their checked rules.
func craRows() craFixture {
	date := civil.DateOf
	published := date(time.Date(2025, time.January, 15, 0, 0, 0, 0, time.UTC))
	effective := date(time.Date(2025, time.March, 17, 0, 0, 0, 0, time.UTC))
	introduced := date(time.Date(2025, time.March, 10, 0, 0, 0, 0, time.UTC))
	introducedUnmatched := date(time.Date(2025, time.April, 2, 0, 0, 0, 0, time.UTC))
	checked := time.Date(2025, time.May, 1, 12, 0, 0, 0, time.UTC)
	noJSON, noStatus, noDate := spanner.NullJSON{}, spanner.NullString{}, spanner.NullDate{}

	return craFixture{
		bills: []any{
			billRow{
				CRABill, Congress, "sjres", 41, CRATitle, introduced, chamberSenate,
				noJSON, noJSON, noJSON, noJSON, noJSON, noStatus, noDate,
			},
			billRow{
				CRAUnmatchedBill, Congress, "hjres", 63, CRAUnmatchedTitle, introducedUnmatched, chamberHouse,
				noJSON, noJSON, noJSON, noJSON, noJSON, noStatus, noDate,
			},
		},
		document: frDocumentRow{
			DocumentNumber:  CRADocument,
			Citation:        craCitation,
			Volume:          craVolume,
			StartPage:       craStartPage,
			EndPage:         craEndPage,
			DocType:         "Rule",
			Action:          nullString("Final rule."),
			Title:           CRADocumentTitle,
			Agencies:        jsonCol(craAgencies),
			PublicationDate: published,
			EffectiveOn:     spanner.NullDate{Date: effective, Valid: true},
			Abstract:        nullString(craDocumentAbstract),
			HTMLURL:         "https://www.federalregister.gov/documents/2025/01/15/" + CRADocument + "/lantern-wick-efficiency-standards",
			PDFURL:          nullString(craPDFURL),
			DocketID:        nullString("OLS-2024-0007"),
			ContentHash:     craContextHash,
			FetchedAt:       checked,
		},
		rules: []any{
			craRuleRow{
				BillID: CRABill, RuleTitle: CRADocumentTitle, RuleAgency: craAgency, Cited: nullString(craCitation),
				Status: craStatusMatched, Method: nullString("citation"), DocumentNumber: nullString(CRADocument),
				MatcherVersion: craMatcherVersion, ContextHash: craContextHash, CheckedAt: checked,
			},
			craRuleRow{
				BillID: CRAUnmatchedBill, RuleTitle: craUnmatchedRule, RuleAgency: craAgency, GAOOpinion: true,
				Status: craStatusUnmatched, Reason: nullString("no_candidates"),
				MatcherVersion: craMatcherVersion, ContextHash: craContextHash, CheckedAt: checked,
			},
		},
	}
}

func nullString(s string) spanner.NullString { return spanner.NullString{StringVal: s, Valid: true} }
