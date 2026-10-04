package spannerdb_test

import (
	"errors"
	"slices"
	"strconv"
	"testing"
	"time"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

const (
	craMatcher = "cra-v1"
	craTitle   = "Providing for congressional disapproval under chapter 8 of title 5, United States Code, " +
		"of the rule submitted by the Bureau of Consumer Financial Protection relating to \"Overdraft Lending\"."
)

// seedCRABill stores a bill of the given type with the title, introduced daysAgo, and, for each
// code in texts, a text version (in that order, so the last is the latest) whose stored text is
// "<text>code</text>" with hash "hash-<code>".
func seedCRABill(
	t *testing.T, store *spannerdb.PipelineStoreImpl, client *spanner.Client,
	billType string, congress, number, daysAgo int, title string, texts ...string,
) string {
	t.Helper()
	ctx := t.Context()
	id := billType + "-" + strconv.Itoa(congress) + "-" + strconv.Itoa(number)
	introduced := time.Now().AddDate(0, 0, -daysAgo)
	if err := store.UpsertBill(ctx, repository.BillRow{
		ID: id, Congress: congress, BillType: billType, Number: number, Title: title, IntroducedDate: &introduced,
	}); err != nil {
		t.Fatalf("upsert bill: %v", err)
	}
	if len(texts) == 0 {
		return id
	}
	rows := make([]repository.TextVersionRow, 0, len(texts))
	for i, code := range texts {
		rows = append(rows, textVersion(id, "Version "+code, code, i+1))
	}
	upsertVersions(t, store, id, rows...)
	ids := versionIDs(t, client, id)
	for _, code := range texts {
		if err := store.InsertBillText(ctx, repository.BillTextRow{
			TextVersionID: ids[code], Format: "Formatted XML", Content: "<text>" + code + "</text>",
			ContentHash: "hash-" + code, FetchedAt: time.Now(),
		}); err != nil {
			t.Fatalf("insert text: %v", err)
		}
	}
	return id
}

// craRow is a matched row for bill parsed from the text with hash textHash (nil: the title).
func craRow(bill string, textHash *string) repository.CRARuleRow {
	return repository.CRARuleRow{
		BillID: bill, RuleTitle: "Overdraft Lending", RuleAgency: "Bureau of Consumer Financial Protection",
		Cited: new("89 Fed. Reg. 106768 (December 30, 2024)"), Status: repository.CRAStatusMatched,
		Method: new(repository.CRAMethodCitation), DocumentNumber: new("2024-29699"),
		SourceTextHash: textHash, MatcherVersion: craMatcher, ContextHash: "ctx-1",
	}
}

func upsertCRA(t *testing.T, store *spannerdb.PipelineStoreImpl, r repository.CRARuleRow) bool {
	t.Helper()
	changed, err := store.UpsertCRARule(t.Context(), r)
	if err != nil {
		t.Fatalf("upsert cra rule %s: %v", r.BillID, err)
	}
	return changed
}

func setCRAChecked(t *testing.T, client *spanner.Client, bill string, at time.Time) {
	t.Helper()
	if _, err := client.Apply(t.Context(), []*spanner.Mutation{
		spanner.Update("bill_cra_rules", []string{"bill_id", "checked_at"}, []any{bill, at}),
	}); err != nil {
		t.Fatal(err)
	}
}

func craCheckIDs(checks []repository.CRARuleCheck) []string {
	ids := make([]string, 0, len(checks))
	for _, c := range checks {
		ids = append(ids, c.BillID)
	}
	return ids
}

func TestListCRARuleChecks(t *testing.T) {
	store, client := newLinkStore(t)
	ctx := t.Context()
	c := testdb.FixtureCongress
	day := 24 * time.Hour

	// Never checked: S.J.Res. 18 (text ih, then is), introduced 5 days ago, and H.J.Res. 17 (no
	// text yet), 1 day ago.
	sj18 := seedCRABill(t, store, client, "sjres", c, 18, 5, craTitle, "ih", "is")
	hj17 := seedCRABill(t, store, client, "hjres", c, 17, 1, craTitle)
	// Never listed: a disapproval that isn't a CRA resolution, a bill of another type, another
	// congress's resolution.
	seedCRABill(t, store, client, "sjres", c, 20, 2,
		"A joint resolution providing for congressional disapproval of the proposed foreign military sale.", "is")
	seedCRABill(t, store, client, "hr", c, 50, 2, craTitle)
	testdb.SeedCongress(ctx, t, client, testdb.FixturePrevCongress)
	seedCRABill(t, store, client, "sjres", testdb.FixturePrevCongress, 1, 2, craTitle)

	// Checked and fresh: matched from its latest text; matched from the title, still no text;
	// unmatched 29 days ago.
	fresh := seedCRABill(t, store, client, "hjres", c, 11, 30, craTitle, "ih")
	upsertCRA(t, store, craRow(fresh, new("hash-ih")))
	titleOnly := seedCRABill(t, store, client, "hjres", c, 16, 30, craTitle)
	upsertCRA(t, store, craRow(titleOnly, nil))
	recent := seedCRABill(t, store, client, "hjres", c, 15, 30, craTitle)
	r := craRow(recent, nil)
	r.Status, r.Method, r.Reason, r.DocumentNumber = repository.CRAStatusUnmatched, nil,
		new(repository.CRAReasonAmbiguous), nil
	upsertCRA(t, store, r)
	setCRAChecked(t, client, recent, time.Now().Add(-29*day))

	// Due: a newer text than the row's (20 days ago), another matcher version (40 days ago), and
	// unmatched 31 days ago (10 days ago).
	newText := seedCRABill(t, store, client, "hjres", c, 12, 20, craTitle, "ih", "rh")
	upsertCRA(t, store, craRow(newText, new("hash-ih")))
	oldMatcher := seedCRABill(t, store, client, "hjres", c, 13, 40, craTitle, "ih")
	old := craRow(oldMatcher, new("hash-ih"))
	old.MatcherVersion = "cra-v0"
	upsertCRA(t, store, old)
	stale := seedCRABill(t, store, client, "hjres", c, 14, 10, craTitle)
	r.BillID = stale
	upsertCRA(t, store, r)
	setCRAChecked(t, client, stale, time.Now().Add(-31*day))

	got, err := store.ListCRARuleChecks(ctx, c, craMatcher, 0)
	if err != nil {
		t.Fatal(err)
	}
	// The fixture's H.J.Res. 63 is unmatched and was checked in May 2025, so it's due too.
	want := []string{hj17, sj18, stale, newText, oldMatcher, testdb.FixtureCRAUnmatched}
	if ids := craCheckIDs(got); !slices.Equal(ids, want) {
		t.Fatalf("ListCRARuleChecks = %v, want %v", ids, want)
	}
	first, second := got[0], got[1]
	if first.TextHash != nil || first.Text != "" || first.BillType != "hjres" || first.Number != 17 {
		t.Errorf("H.J.Res. 17 = %+v, want hjres 17 with no text", first)
	}
	if second.TextHash == nil || *second.TextHash != "hash-is" || second.Text != "<text>is</text>" ||
		second.Title != craTitle || second.IntroducedDate == nil {
		t.Errorf("S.J.Res. 18 = %+v, want its latest text (is), title and introduced date", second)
	}

	got, err = store.ListCRARuleChecks(ctx, c, craMatcher, 3)
	if err != nil {
		t.Fatal(err)
	}
	if ids := craCheckIDs(got); !slices.Equal(ids, want[:3]) {
		t.Errorf("ListCRARuleChecks(limit 3) = %v, want %v", ids, want[:3])
	}
}

func TestUpsertFRDocument(t *testing.T) {
	store, client := newLinkStore(t)
	ctx := t.Context()
	effective := time.Date(2025, 10, 1, 0, 0, 0, 0, time.UTC)
	doc := repository.FRDocumentRow{
		DocumentNumber: "2024-29699",
		Citation:       "89 FR 106768",
		Volume:         89,
		StartPage:      106768,
		EndPage:        106830,
		DocType:        "Rule",
		Action:         new("Final rule; official interpretation."),
		Title:          "Overdraft Lending: Very Large Financial Institutions",
		Agencies: []repository.FRAgency{
			{Name: "Consumer Financial Protection Bureau", Slug: "consumer-financial-protection-bureau"},
		},
		PublicationDate:     time.Date(2024, 12, 30, 0, 0, 0, 0, time.UTC),
		EffectiveOn:         &effective,
		Abstract:            new("The CFPB amends Regulations E and Z."),
		HTMLURL:             "https://www.federalregister.gov/documents/2024/12/30/2024-29699/overdraft-lending",
		PDFURL:              new("https://www.govinfo.gov/content/pkg/FR-2024-12-30/pdf/2024-29699.pdf"),
		DocketID:            new("CFPB-2024-0002"),
		RegulationIDNumbers: []string{"3170-AA42"},
		ContentHash:         "h1",
	}
	read := func() string {
		t.Helper()
		rows := queryStrings(t, client, `SELECT CONCAT(citation, '|', doc_type, '|', title, '|',
			TO_JSON_STRING(agencies), '|', CAST(publication_date AS STRING), '|', CAST(effective_on AS STRING), '|',
			IFNULL(docket_id, ''), '|', IFNULL(TO_JSON_STRING(regulation_id_numbers), ''), '|', content_hash)
			FROM federal_register_documents WHERE document_number = @n`, map[string]any{"n": doc.DocumentNumber})
		if len(rows) != 1 {
			t.Fatalf("rows = %q, want one", rows)
		}
		return rows[0]
	}
	upsert := func(d repository.FRDocumentRow, want bool) {
		t.Helper()
		wrote, err := store.UpsertFRDocument(ctx, d)
		if err != nil {
			t.Fatal(err)
		}
		if wrote != want {
			t.Errorf("UpsertFRDocument(hash %s) wrote = %v, want %v", d.ContentHash, wrote, want)
		}
	}

	upsert(doc, true)
	want := `89 FR 106768|Rule|Overdraft Lending: Very Large Financial Institutions|` +
		`[{"name":"Consumer Financial Protection Bureau","slug":"consumer-financial-protection-bureau"}]|` +
		`2024-12-30|2025-10-01|CFPB-2024-0002|["3170-AA42"]|h1`
	if got := read(); got != want {
		t.Errorf("stored = %q, want %q", got, want)
	}

	// The same hash is no write, even if a field differs; a new hash is.
	same := doc
	same.Title = "Changed"
	upsert(same, false)
	if got := read(); got != want {
		t.Errorf("after an unchanged upsert = %q, want %q", got, want)
	}
	changed := doc
	changed.ContentHash, changed.Agencies, changed.RegulationIDNumbers, changed.DocketID = "h2", nil, nil, nil
	changed.EffectiveOn = nil
	upsert(changed, true)
	want = "[]||true||h2"
	if got := queryStrings(t, client, `SELECT CONCAT(TO_JSON_STRING(agencies), '|',
		IFNULL(CAST(effective_on AS STRING), ''), '|', CAST(regulation_id_numbers IS NULL AS STRING), '|',
		IFNULL(docket_id, ''), '|', content_hash) FROM federal_register_documents WHERE document_number = @n`,
		map[string]any{"n": doc.DocumentNumber}); !slices.Equal(got, []string{want}) {
		t.Errorf("after a changed upsert = %q, want %q", got, want)
	}
}

func TestUpsertCRARule(t *testing.T) {
	store, client := newLinkStore(t)
	bill := seedCRABill(t, store, client, "sjres", testdb.FixtureCongress, 18, 5, craTitle, "is")
	read := func() string {
		t.Helper()
		rows := queryStrings(t, client, `SELECT CONCAT(status, '|', IFNULL(method, ''), '|', IFNULL(reason, ''), '|',
			IFNULL(document_number, ''), '|', IFNULL(source_text_hash, ''), '|', matcher_version, '|', context_hash)
			FROM bill_cra_rules WHERE bill_id = @b AND checked_at IS NOT NULL`, map[string]any{"b": bill})
		if len(rows) != 1 {
			t.Fatalf("rows = %q, want one", rows)
		}
		return rows[0]
	}

	row := craRow(bill, new("hash-is"))
	if !upsertCRA(t, store, row) {
		t.Error("first upsert changed = false, want true")
	}
	if got, want := read(), "matched|citation||2024-29699|hash-is|cra-v1|ctx-1"; got != want {
		t.Errorf("stored = %q, want %q", got, want)
	}

	// A new text or matcher version that finds the same thing changes nothing a reader sees, but
	// is stored.
	again := row
	again.SourceTextHash, again.MatcherVersion = new("hash-enr"), "cra-v2"
	if upsertCRA(t, store, again) {
		t.Error("same match changed = true, want false")
	}
	if got, want := read(), "matched|citation||2024-29699|hash-enr|cra-v2|ctx-1"; got != want {
		t.Errorf("stored = %q, want %q", got, want)
	}

	for name, mod := range map[string]func(*repository.CRARuleRow){
		"context hash": func(r *repository.CRARuleRow) { r.ContextHash = "ctx-2" },
		"unmatched": func(r *repository.CRARuleRow) {
			r.Status, r.Method, r.DocumentNumber = repository.CRAStatusUnmatched, nil, nil
			r.Reason = new(repository.CRAReasonCiteMismatch)
		},
		"withdrawn document": func(r *repository.CRARuleRow) { r.WithdrawnDocumentNumber = new("2023-11111") },
		"gao opinion":        func(r *repository.CRARuleRow) { r.GAOOpinion = true },
	} {
		next := again
		mod(&next)
		if !upsertCRA(t, store, next) {
			t.Errorf("%s: changed = false, want true", name)
		}
		upsertCRA(t, store, again)
	}

	_, err := store.UpsertCRARule(t.Context(), craRow("sjres-119-999", nil))
	if err == nil {
		t.Error("upsert for a missing bill: err = nil, want an error")
	}
	if errors.Is(err, spanner.ErrRowNotFound) {
		t.Errorf("upsert for a missing bill: err = %v, want the write's error", err)
	}
}
