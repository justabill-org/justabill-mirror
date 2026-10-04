package handler_test

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/model"
)

const (
	craBill = "sjres-119-18"
	// craSearch is the Federal Register search for "Overdraft Lending" by the Bureau of Consumer
	// Financial Protection.
	craSearch = `"search_url":"https://www.federalregister.gov/documents/search?` +
		`conditions%5Bterm%5D=%22Overdraft+Lending%22+Bureau+of+Consumer+Financial+Protection"`
	craHTML = "https://www.federalregister.gov/documents/2024/12/30/2024-29699/overdraft-lending"
	craPDF  = "https://www.govinfo.gov/content/pkg/FR-2024-12-30/pdf/2024-29699.pdf"
)

// craDocument is a matched document as GetCRARule returns it, every field set, with the links
// given.
func craDocument(html, pdf string) *model.FRDocument {
	return &model.FRDocument{
		DocumentNumber: "2024-29699", Citation: "89 FR 106768", Type: "Rule",
		Action:   new("Final rule; official interpretation."),
		Title:    "Overdraft Lending: Very Large Financial Institutions",
		Agencies: []string{"Consumer Financial Protection Bureau"}, PublicationDate: "2024-12-30",
		EffectiveOn: new("2025-10-01"), Abstract: new("The CFPB amends Regulations E and Z."),
		HTMLURL: new(html), PDFURL: new(pdf), DocketID: new("CFPB-2024-0002"),
	}
}

// craRule is a rule as GetCRARule returns it, with no SearchURL.
func craRule(status string, doc *model.FRDocument) *model.CRARule {
	r := &model.CRARule{
		Status: status, RuleTitle: "Overdraft Lending", RuleAgency: "Bureau of Consumer Financial Protection",
		Document: doc, CheckedAt: time.Date(2026, 10, 3, 6, 0, 0, 0, time.UTC),
	}
	if doc != nil {
		r.Method, r.Cited = new("citation"), new("89 Fed. Reg. 106768 (December 30, 2024)")
	} else {
		r.Reason, r.GAOOpinion = new("no_candidates"), true
	}
	return r
}

func TestGetBill_DisapprovedRule(t *testing.T) {
	tests := []struct {
		name string
		repo *mockBillRepo
		want string
	}{
		{
			name: "matched",
			repo: &mockBillRepo{craRule: craRule("matched", craDocument(craHTML, craPDF))},
			want: `{"status":"matched","method":"citation","rule_title":"Overdraft Lending",` +
				`"rule_agency":"Bureau of Consumer Financial Protection",` +
				`"cited":"89 Fed. Reg. 106768 (December 30, 2024)","gao_opinion":false,` +
				`"document":{"document_number":"2024-29699","citation":"89 FR 106768","type":"Rule",` +
				`"action":"Final rule; official interpretation.",` +
				`"title":"Overdraft Lending: Very Large Financial Institutions",` +
				`"agencies":["Consumer Financial Protection Bureau"],"publication_date":"2024-12-30",` +
				`"effective_on":"2025-10-01","abstract":"The CFPB amends Regulations E and Z.",` +
				`"html_url":"` + craHTML + `","pdf_url":"` + craPDF + `","docket_id":"CFPB-2024-0002"},` +
				`"withdrawn_document":null,` + craSearch + `,"checked_at":"2026-10-03T06:00:00Z"}`,
		},
		{
			name: "unmatched",
			repo: &mockBillRepo{craRule: craRule("unmatched", nil)},
			want: `{"status":"unmatched","reason":"no_candidates","rule_title":"Overdraft Lending",` +
				`"rule_agency":"Bureau of Consumer Financial Protection","cited":null,"gao_opinion":true,` +
				`"document":null,"withdrawn_document":null,` + craSearch + `,"checked_at":"2026-10-03T06:00:00Z"}`,
		},
		{name: "not a CRA resolution, or not checked yet", repo: &mockBillRepo{}, want: "null"},
		{name: "read error", repo: &mockBillRepo{craRuleErr: errSpanner}, want: "null"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.repo.bill = &model.Bill{ID: craBill}
			code, body := getBill(t, newTestHandler(tt.repo), craBill)
			if code != http.StatusOK {
				t.Fatalf("status = %d, want 200", code)
			}
			if got := string(body["disapproved_rule"]); got != tt.want {
				t.Errorf("disapproved_rule =\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

func TestGetBill_DisapprovedRuleLinksOffHost(t *testing.T) {
	tests := []struct {
		name, html, pdf   string
		wantHTML, wantPDF string
	}{
		{
			name:     "both on their hosts",
			html:     craHTML,
			pdf:      craPDF,
			wantHTML: `"` + craHTML + `"`,
			wantPDF:  `"` + craPDF + `"`,
		},
		{name: "another host", html: "https://evil.example/2024-29699", pdf: "https://evil.example/x.pdf",
			wantHTML: "null", wantPDF: "null"},
		{
			name:     "each on the other's host",
			html:     "https://www.govinfo.gov/x",
			pdf:      "https://www.federalregister.gov/x.pdf",
			wantHTML: "null",
			wantPDF:  "null",
		},
		{name: "http", html: "http://www.federalregister.gov/x", pdf: "http://www.govinfo.gov/x.pdf",
			wantHTML: "null", wantPDF: "null"},
		{name: "a host that starts with ours", html: "https://www.federalregister.gov.evil.example/x",
			pdf: "https://www.govinfo.gov.evil.example/x.pdf", wantHTML: "null", wantPDF: "null"},
		{name: "a port or user", html: "https://www.federalregister.gov:8443/x", pdf: "https://u@www.govinfo.gov/x.pdf",
			wantHTML: "null", wantPDF: "null"},
		{name: "a script", html: "javascript:alert(1)", pdf: "javascript:alert(1)", wantHTML: "null", wantPDF: "null"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rule := craRule("matched", craDocument(tt.html, tt.pdf))
			rule.WithdrawnDocument = craDocument(tt.html, tt.pdf)
			repo := &mockBillRepo{bill: &model.Bill{ID: craBill}, craRule: rule}
			code, body := getBill(t, newTestHandler(repo), craBill)
			if code != http.StatusOK {
				t.Fatalf("status = %d, want 200", code)
			}
			type links struct {
				HTMLURL json.RawMessage `json:"html_url"`
				PDFURL  json.RawMessage `json:"pdf_url"`
			}
			var got struct {
				Document          links `json:"document"`
				WithdrawnDocument links `json:"withdrawn_document"`
			}
			if err := json.Unmarshal(body["disapproved_rule"], &got); err != nil {
				t.Fatal(err)
			}
			for _, doc := range []struct {
				name      string
				html, pdf json.RawMessage
			}{
				{"document", got.Document.HTMLURL, got.Document.PDFURL},
				{"withdrawn_document", got.WithdrawnDocument.HTMLURL, got.WithdrawnDocument.PDFURL},
			} {
				if string(doc.html) != tt.wantHTML || string(doc.pdf) != tt.wantPDF {
					t.Errorf("%s links = %s, %s; want %s, %s", doc.name, doc.html, doc.pdf, tt.wantHTML, tt.wantPDF)
				}
			}
		})
	}
}

func TestGetBill_SummaryWithRuleContext(t *testing.T) {
	for _, withRule := range []bool{true, false} {
		repo := &mockBillRepo{
			bill:    &model.Bill{ID: craBill},
			summary: &model.BillSummary{BillID: craBill, WithRuleContext: withRule},
		}
		_, body := getBill(t, newTestHandler(repo), craBill)
		var got struct {
			WithRuleContext *bool `json:"with_rule_context"`
		}
		if err := json.Unmarshal(body["summary"], &got); err != nil {
			t.Fatal(err)
		}
		if withRule != (got.WithRuleContext != nil && *got.WithRuleContext) {
			t.Errorf("summary written with the rule = %v: summary = %s", withRule, body["summary"])
		}
	}
}
