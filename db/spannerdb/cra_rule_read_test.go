package spannerdb_test

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

// frDocument is a stored federal_register_documents row with every field set.
func frDocument(number, citation, title string) repository.FRDocumentRow {
	effective := time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC)
	return repository.FRDocumentRow{
		DocumentNumber: number, Citation: citation, Volume: 89, StartPage: 106768, EndPage: 106830,
		DocType: "Rule", Action: new("Final rule; official interpretation."), Title: title,
		Agencies: []repository.FRAgency{
			{Name: "Treasury Department", Slug: "treasury-department"},
			{Name: "Consumer Financial Protection Bureau", Slug: "consumer-financial-protection-bureau"},
		},
		PublicationDate: time.Date(2024, 12, 30, 0, 0, 0, 0, time.UTC), EffectiveOn: &effective,
		Abstract: new("The CFPB amends Regulations E and Z."),
		HTMLURL:  "https://www.federalregister.gov/documents/2024/12/30/" + number + "/overdraft-lending",
		PDFURL:   new("https://www.govinfo.gov/content/pkg/FR-2024-12-30/pdf/" + number + ".pdf"),
		DocketID: new("CFPB-2024-0002"), ContentHash: "h-" + number,
	}
}

// zeroCheckedAt ends a rule's JSON once the test has zeroed its checked_at.
const zeroCheckedAt = `"checked_at":"0001-01-01T00:00:00Z"}`

func TestGetCRARule(t *testing.T) {
	store, client := newLinkStore(t)
	repo := spannerdb.NewBillRepo(&spannerdb.Client{Spanner: client})
	ctx := t.Context()
	c := testdb.FixtureCongress
	for _, d := range []repository.FRDocumentRow{
		frDocument("2024-29699", "89 FR 106768", "Overdraft Lending: Very Large Financial Institutions"),
		{
			DocumentNumber: "2024-00001", Citation: "89 FR 1", Volume: 89, StartPage: 1, EndPage: 2,
			DocType: "Notice", Title: "Withdrawn guidance", PublicationDate: time.Date(2024, 1, 2, 0, 0, 0, 0, time.UTC),
			HTMLURL: "https://www.federalregister.gov/d/2024-00001", ContentHash: "h-w",
		},
	} {
		if _, err := store.UpsertFRDocument(ctx, d); err != nil {
			t.Fatal(err)
		}
	}

	matched := seedCRABill(t, store, client, "sjres", c, 18, 5, craTitle)
	upsertCRA(t, store, craRow(matched, nil))
	withdrawal := seedCRABill(t, store, client, "sjres", c, 143, 5, craTitle)
	w := craRow(withdrawal, nil)
	w.WithdrawnDocumentNumber = new("2024-00001")
	upsertCRA(t, store, w)
	unmatched := seedCRABill(t, store, client, "hjres", c, 15, 5, craTitle)
	u := craRow(unmatched, nil)
	u.Status, u.Method, u.Reason, u.DocumentNumber, u.Cited, u.GAOOpinion = repository.CRAStatusUnmatched, nil,
		new(repository.CRAReasonAmbiguous), nil, nil, true
	upsertCRA(t, store, u)
	unchecked := seedCRABill(t, store, client, "hjres", c, 16, 5, craTitle)

	tests := []struct {
		name string
		bill string
		want string
	}{
		{
			name: "matched",
			bill: matched,
			want: `{"status":"matched","method":"citation","rule_title":"Overdraft Lending",` +
				`"rule_agency":"Bureau of Consumer Financial Protection",` +
				`"cited":"89 Fed. Reg. 106768 (December 30, 2024)","gao_opinion":false,` +
				`"document":{"document_number":"2024-29699","citation":"89 FR 106768","type":"Rule",` +
				`"action":"Final rule; official interpretation.",` +
				`"title":"Overdraft Lending: Very Large Financial Institutions",` +
				`"agencies":["Treasury Department","Consumer Financial Protection Bureau"],` +
				`"publication_date":"2024-12-30","effective_on":"2025-10-01",` +
				`"abstract":"The CFPB amends Regulations E and Z.",` +
				`"html_url":"https://www.federalregister.gov/documents/2024/12/30/2024-29699/overdraft-lending",` +
				`"pdf_url":"https://www.govinfo.gov/content/pkg/FR-2024-12-30/pdf/2024-29699.pdf",` +
				`"docket_id":"CFPB-2024-0002"},"withdrawn_document":null,"search_url":"",` + zeroCheckedAt,
		},
		{
			name: "withdrawal",
			bill: withdrawal,
			want: `{"document_number":"2024-00001","citation":"89 FR 1","type":"Notice","action":null,` +
				`"title":"Withdrawn guidance","agencies":[],"publication_date":"2024-01-02","effective_on":null,` +
				`"abstract":null,"html_url":"https://www.federalregister.gov/d/2024-00001","pdf_url":null,` +
				`"docket_id":null}`,
		},
		{
			name: "unmatched",
			bill: unmatched,
			want: `{"status":"unmatched","reason":"ambiguous","rule_title":"Overdraft Lending",` +
				`"rule_agency":"Bureau of Consumer Financial Protection","cited":null,"gao_opinion":true,` +
				`"document":null,"withdrawn_document":null,"search_url":"",` + zeroCheckedAt,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := craRuleJSON(t, repo, tt.bill, tt.name == "withdrawal"); got != tt.want {
				t.Errorf("rule =\n%s\nwant\n%s", got, tt.want)
			}
		})
	}

	for _, bill := range []string{unchecked, testdb.FixtureHouseBill, "sjres-119-99999"} {
		got, err := repo.GetCRARule(ctx, bill)
		if err != nil || got != nil {
			t.Errorf("GetCRARule(%s) = %+v, %v; want nil, nil", bill, got, err)
		}
	}
}

// craRuleJSON reads bill's rule, checks its checked_at is the upsert's commit time, and returns
// it as JSON with checked_at zeroed, or only its withdrawn document's.
func craRuleJSON(t *testing.T, repo *spannerdb.BillRepository, bill string, withdrawn bool) string {
	t.Helper()
	got, err := repo.GetCRARule(t.Context(), bill)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("rule = nil, want one")
	}
	if since := time.Since(got.CheckedAt); since < 0 || since > time.Hour {
		t.Errorf("checked_at = %v, want the upsert's commit time", got.CheckedAt)
	}
	got.CheckedAt = time.Time{}
	var v any = got
	if withdrawn {
		v = got.WithdrawnDocument
	}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}
