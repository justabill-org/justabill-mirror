package sync

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/cra"
	"github.com/justabill-org/justabill/pipeline/internal/fedreg"
)

// Real resolutions of the 119th Congress (titles and resolving clauses as stored).
const (
	sjres18Title = `A joint resolution disapproving the rule submitted by the Bureau of Consumer Financial ` +
		`Protection relating to "Overdraft Lending: Very Large Financial Institutions".`
	sjres18Text = `<section><enum/><text>That Congress disapproves the final rule submitted by the Bureau of ` +
		`Consumer Financial Protection relating to <quote>Overdraft Lending: Very Large Financial Institutions` +
		`</quote> (89 Fed. Reg. 106768 (December 30, 2024)), and such rule shall have no force or effect. ` +
		`</text></section>`
	sjres67Title = `A joint resolution providing for congressional disapproval under chapter 8 of title 5, United ` +
		`States Code, of the rule submitted by the Environmental Protection Agency relating to "National ` +
		`Emission Standards for Hazardous Air Pollutants: Integrated Iron and Steel Manufacturing Facilities ` +
		`Technology Review: Interim Final Rule".`
	sjres67Text = `<section><text>That Congress disapproves the rule submitted by the Environmental Protection ` +
		`Agency relating to <quote>National Emission Standards for Hazardous Air Pollutants: Integrated Iron ` +
		`and Steel Manufacturing Facilities Technology Review: Interim Final Rule</quote> (90 Fed. Reg. 29485 ` +
		`(July 3, 2025)), and such rule shall have no force or effect.</text></section>`
	sjres143Title = `A joint resolution providing for congressional disapproval under chapter 8 of title 5, ` +
		`United States Code, of the rule submitted by the Bureau of Consumer Financial Protection relating ` +
		`to the withdrawal of the rule relating to "Consumer Financial Protection Circular 2023-02: Reopening ` +
		`Deposit Accounts That Consumers Previously Closed".`
	sjres143Text = `<section><text>That Congress disapproves the rule submitted by the Bureau of Consumer ` +
		`Financial Protection relating to the withdrawal of the rule relating to <quote>Consumer Financial ` +
		`Protection Circular 2023–02: Reopening Deposit Accounts That Consumers Previously Closed (88 Fed. Reg. ` +
		`33545 (May 24, 2023))</quote> (90 Fed. Reg. 20084 (May 12, 2025)), and such rule shall have no force ` +
		`or effect.</text></section>`
	hjres41Title = `Providing for congressional disapproval under chapter 8 of title 5, United States Code, of ` +
		`the rule submitted by the Department of Education relating to "Postsecondary Student Success Grant".`
	hjres41Text = `<section><enum/><text>That Congress disapproves the rule submitted by the Department of ` +
		`Education relating to <quote>Postsecondary Student Success Grant</quote> (89 Fed. Reg. 48517 (June 7, ` +
		`2024)), and such rule shall have no force or effect. </text></section>`
	hjres39Title = `Providing for congressional disapproval under chapter 8 of title 5, United States Code, of ` +
		`the rule submitted by the Federal Trade Commission relating to "Premerger Notification; Reporting and ` +
		`Waiting Period Requirements".`
	// hjres39Uncited is H.J.Res. 39's clause without its citation, as an uncited resolution reads.
	hjres39Uncited = `<section><text>That Congress disapproves the rule submitted by the Federal Trade ` +
		`Commission relating to <quote>Premerger Notification; Reporting and Waiting Period Requirements` +
		`</quote>, and such rule shall have no force or effect.</text></section>`
)

const (
	overdraftTitle = "Overdraft Lending: Very Large Financial Institutions"
	overdraftDoc   = "2024-29699"
)

// frServer serves the recorded Federal Register responses in testdata/fedreg: a day's documents
// by conditions[publication_date][is], a phrase search by conditions[term]. Anything else matches
// nothing. down makes every request a 503; failDays fails those days only.
type frServer struct {
	t        *testing.T
	searches map[string]string
	down     bool
	failDays map[string]bool

	mu       gosync.Mutex
	requests []string
}

func newFRServer(t *testing.T, searches map[string]string) (*frServer, *httptest.Server) {
	t.Helper()
	f := &frServer{t: t, searches: searches, failDays: map[string]bool{}}
	srv := httptest.NewServer(f)
	t.Cleanup(srv.Close)
	return f, srv
}

func (f *frServer) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	day, term := q.Get("conditions[publication_date][is]"), strings.Trim(q.Get("conditions[term]"), `"`)
	f.mu.Lock()
	f.requests = append(f.requests, day+term)
	f.mu.Unlock()
	if f.down || f.failDays[day] {
		w.WriteHeader(http.StatusServiceUnavailable)
		return
	}
	file := ""
	switch {
	case day != "":
		file = "day-" + day + ".json"
	case term != "":
		file = f.searches[term]
	}
	body, err := os.ReadFile(filepath.Join("testdata", "fedreg", file))
	if file == "" || err != nil {
		_, _ = w.Write([]byte(`{"count":0,"description":"nothing"}`))
		return
	}
	_, _ = w.Write(body)
}

func (f *frServer) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

// craStore serves the due resolutions and records what the step writes. UpsertCRARule reports a
// change unless unchanged lists the bill; UpsertFRDocument writes unless stored lists the document.
type craStore struct {
	repository.PipelineStore

	checks    []repository.CRARuleCheck
	unchanged map[string]bool
	stored    map[string]bool
	docErr    error
	ruleErr   error

	matcher   string
	limit     int
	docs      []repository.FRDocumentRow
	rows      []repository.CRARuleRow
	successes []repository.SyncRun
	failures  []repository.SyncRun
}

func (f *craStore) ListCRARuleChecks(
	_ context.Context, _ int, matcherVersion string, limit int,
) ([]repository.CRARuleCheck, error) {
	f.matcher, f.limit = matcherVersion, limit
	return f.checks, nil
}

func (f *craStore) UpsertFRDocument(_ context.Context, d repository.FRDocumentRow) (bool, error) {
	if f.docErr != nil {
		return false, f.docErr
	}
	f.docs = append(f.docs, d)
	return !f.stored[d.DocumentNumber], nil
}

func (f *craStore) UpsertCRARule(_ context.Context, r repository.CRARuleRow) (bool, error) {
	if f.ruleErr != nil {
		return false, f.ruleErr
	}
	f.rows = append(f.rows, r)
	return !f.unchanged[r.BillID], nil
}

func (f *craStore) RecordSyncSuccess(_ context.Context, run repository.SyncRun) error {
	f.successes = append(f.successes, run)
	return nil
}

func (f *craStore) RecordSyncFailure(_ context.Context, run repository.SyncRun) error {
	f.failures = append(f.failures, run)
	return nil
}

func (f *craStore) row(t *testing.T, billID string) repository.CRARuleRow {
	t.Helper()
	i := slices.IndexFunc(f.rows, func(r repository.CRARuleRow) bool { return r.BillID == billID })
	if i < 0 {
		t.Fatalf("no bill_cra_rules row for %s; rows %+v", billID, f.rows)
	}
	return f.rows[i]
}

func (f *craStore) doc(t *testing.T, number string) repository.FRDocumentRow {
	t.Helper()
	i := slices.IndexFunc(f.docs, func(d repository.FRDocumentRow) bool { return d.DocumentNumber == number })
	if i < 0 {
		t.Fatalf("document %s not stored; stored %d", number, len(f.docs))
	}
	return f.docs[i]
}

// craCheck builds a due resolution with a stored text, introduced on introduced (YYYY-MM-DD).
func craCheck(t *testing.T, billID, title, text, introduced string) repository.CRARuleCheck {
	t.Helper()
	hash := "hash-" + billID
	return repository.CRARuleCheck{
		BillID: billID, Title: title, Text: text, TextHash: &hash, IntroducedDate: date(t, time.DateOnly, introduced),
	}
}

// runCRARules runs the step over checks against the recorded responses, and returns the store,
// the server and the bills the run flushed for revalidation.
func runCRARules(
	t *testing.T, searches map[string]string, prepare func(*frServer, *craStore), checks ...repository.CRARuleCheck,
) (*craStore, *frServer, []string, error) {
	t.Helper()
	fr, srv := newFRServer(t, searches)
	store := &craStore{checks: checks}
	if prepare != nil {
		prepare(fr, store)
	}
	rv := &recordingRevalidator{}
	s := newBackfillService(store)
	s.revalidator = rv
	s.SetFederalRegister(fedreg.NewClientWithBaseURL(srv.Client(), srv.URL))
	err := s.SyncCRARules(t.Context(), 119, 0)
	var flushed []string
	for _, f := range rv.calls() {
		flushed = append(flushed, f...)
	}
	return store, fr, flushed, err
}

func checkMatch(t *testing.T, r repository.CRARuleRow, method, document string) {
	t.Helper()
	if r.Status != repository.CRAStatusMatched || deref(r.Method) != method || deref(r.DocumentNumber) != document {
		t.Errorf("%s = %s by %s to %s (reason %s), want matched by %s to %s", r.BillID, r.Status, deref(r.Method),
			deref(r.DocumentNumber), deref(r.Reason), method, document)
	}
}

func checkUnmatched(t *testing.T, r repository.CRARuleRow, reason string) {
	t.Helper()
	if r.Status != repository.CRAStatusUnmatched || deref(r.Reason) != reason || r.DocumentNumber != nil {
		t.Errorf("%s = %s, reason %s, document %s; want unmatched as %s", r.BillID, r.Status, deref(r.Reason),
			deref(r.DocumentNumber), reason)
	}
}

// S.J.Res. 18 cites 89 FR 106768 (December 30, 2024): it's matched by citation to 2024-29699, the
// document's fields are stored, and the bill is marked for revalidation and cache deletion.
func TestSyncCRARules_ByCitation(t *testing.T) {
	store, fr, flushed, err := runCRARules(t, nil, nil,
		craCheck(t, "sjres-119-18", sjres18Title, sjres18Text, "2025-02-04"))
	if err != nil {
		t.Fatalf("SyncCRARules: %v", err)
	}
	r := store.row(t, "sjres-119-18")
	checkMatch(t, r, repository.CRAMethodCitation, overdraftDoc)
	if deref(r.Cited) != "89 Fed. Reg. 106768 (December 30, 2024)" || r.RuleTitle != overdraftTitle ||
		r.RuleAgency != "Bureau of Consumer Financial Protection" || r.GAOOpinion {
		t.Errorf("row names %q by %q, cited %q, gao %v", r.RuleTitle, r.RuleAgency, deref(r.Cited), r.GAOOpinion)
	}
	if deref(r.SourceTextHash) != "hash-sjres-119-18" || r.MatcherVersion != cra.MatcherVersion ||
		len(r.ContextHash) != 64 || r.WithdrawnDocumentNumber != nil {
		t.Errorf("row text hash %s, matcher %s, context hash %q, withdrawn %s", deref(r.SourceTextHash),
			r.MatcherVersion, r.ContextHash, deref(r.WithdrawnDocumentNumber))
	}

	checkOverdraftDocument(t, store.doc(t, overdraftDoc))

	if !slices.Equal(flushed, []string{"sjres-119-18"}) {
		t.Errorf("flushed %v, want the changed bill", flushed)
	}
	if fr.count() != 1 {
		t.Errorf("%d requests, want 1: the day's listing", fr.count())
	}
	if store.matcher != cra.MatcherVersion || store.limit != 0 {
		t.Errorf("listed with matcher %q, limit %d", store.matcher, store.limit)
	}
	if len(store.successes) != 1 || store.successes[0].Step != stepCRARules || store.successes[0].ItemsSynced != 1 {
		t.Errorf("successes = %+v, want one cra_rules run of 1", store.successes)
	}
}

// checkOverdraftDocument checks the stored fields of 2024-29699, as the recorded response has them.
func checkOverdraftDocument(t *testing.T, d repository.FRDocumentRow) {
	t.Helper()
	if d.Citation != "89 FR 106768" || d.Volume != 89 || d.StartPage != 106768 || d.EndPage != 106845 ||
		d.DocType != fedreg.DocRule || deref(d.Action) != "Final rule; official interpretation." ||
		d.Title != overdraftTitle {
		t.Errorf("document = %+v", d)
	}
	if d.PublicationDate.Format(time.DateOnly) != "2024-12-30" || d.EffectiveOn == nil ||
		d.EffectiveOn.Format(time.DateOnly) != "2025-10-01" {
		t.Errorf("document dates = %v, %v; want 2024-12-30 and 2025-10-01", d.PublicationDate, d.EffectiveOn)
	}
	if len(d.Agencies) != 1 || d.Agencies[0] != (repository.FRAgency{
		Name: "Consumer Financial Protection Bureau", Slug: "consumer-financial-protection-bureau",
	}) {
		t.Errorf("agencies = %+v", d.Agencies)
	}
	if d.HTMLURL != "https://www.federalregister.gov/documents/2024/12/30/2024-29699/overdraft-lending-very-"+
		"large-financial-institutions" || deref(d.PDFURL) != "https://www.govinfo.gov/content/pkg/FR-2024-12-30/"+
		"pdf/2024-29699.pdf" {
		t.Errorf("links = %s, %s", d.HTMLURL, deref(d.PDFURL))
	}
	if deref(d.DocketID) != "CFPB-2024-0002" || !slices.Equal(d.RegulationIDNumbers, []string{"3170-AA42"}) ||
		!strings.HasPrefix(deref(d.Abstract), "The Consumer Financial Protection Bureau (CFPB) amends") ||
		len(d.ContentHash) != 64 {
		t.Errorf("docket %s, RINs %v, abstract %.40q, hash %q", deref(d.DocketID), d.RegulationIDNumbers,
			deref(d.Abstract), d.ContentHash)
	}
}

// S.J.Res. 67's page holds a Postal Service correction and the EPA rule: the title guard picks
// the rule.
func TestSyncCRARules_ByCitationPicksTheTitleOnTheCitedPage(t *testing.T) {
	store, _, _, err := runCRARules(t, nil, nil, craCheck(t, "sjres-119-67", sjres67Title, sjres67Text, "2025-07-15"))
	if err != nil {
		t.Fatalf("SyncCRARules: %v", err)
	}
	checkMatch(t, store.row(t, "sjres-119-67"), repository.CRAMethodCitation, "2025-12407")
}

// A citation to a page whose document shares less than half the title's words is set aside, and
// the title fallback decides: here it finds the rule, there nothing.
func TestSyncCRARules_CiteMismatch(t *testing.T) {
	wrongPage := strings.Replace(sjres18Text, "106768", "106848", 1)
	searches := map[string]string{overdraftTitle: "search-overdraft.json"}

	store, _, _, err := runCRARules(t, searches, nil,
		craCheck(t, "sjres-119-18", sjres18Title, wrongPage, "2025-02-04"))
	if err != nil {
		t.Fatalf("SyncCRARules: %v", err)
	}
	r := store.row(t, "sjres-119-18")
	checkMatch(t, r, repository.CRAMethodTitle, overdraftDoc)
	if deref(r.Cited) != "89 Fed. Reg. 106848 (December 30, 2024)" {
		t.Errorf("cited = %s, want the citation as written", deref(r.Cited))
	}

	store, _, _, err = runCRARules(t, nil, nil, craCheck(t, "sjres-119-18", sjres18Title, wrongPage, "2025-02-04"))
	if err != nil {
		t.Fatalf("SyncCRARules: %v", err)
	}
	checkUnmatched(t, store.row(t, "sjres-119-18"), repository.CRAReasonCiteMismatch)
	if len(store.docs) != 0 {
		t.Errorf("stored %d documents, want none", len(store.docs))
	}
}

// An uncited resolution with one exact title, agency and date match is matched by title. FTC's
// "Premerger Notification" rules of November and February 2024 are less than 365 days apart, so
// H.J.Res. 39 without its citation is ambiguous.
func TestSyncCRARules_ByTitle(t *testing.T) {
	uncited := strings.Replace(sjres18Text, " (89 Fed. Reg. 106768 (December 30, 2024))", "", 1)
	searches := map[string]string{
		overdraftTitle: "search-overdraft.json",
		"Premerger Notification; Reporting and Waiting Period Requirements": "search-premerger.json",
	}
	store, _, flushed, err := runCRARules(t, searches, nil,
		craCheck(t, "sjres-119-18", sjres18Title, uncited, "2025-02-04"),
		craCheck(t, "hjres-119-39", hjres39Title, hjres39Uncited, "2025-01-31"))
	if err != nil {
		t.Fatalf("SyncCRARules: %v", err)
	}
	r := store.row(t, "sjres-119-18")
	checkMatch(t, r, repository.CRAMethodTitle, overdraftDoc)
	if r.Cited != nil {
		t.Errorf("cited = %s, want none", deref(r.Cited))
	}
	checkUnmatched(t, store.row(t, "hjres-119-39"), repository.CRAReasonAmbiguous)
	if !slices.Equal(flushed, []string{"hjres-119-39", "sjres-119-18"}) {
		t.Errorf("flushed %v, want both new rows", flushed)
	}
}

// H.J.Res. 41 cites the proposed rule: the title fallback finds the final rule, which is the
// match. With no final rule, the cited proposed rule is.
func TestSyncCRARules_CitedProposedRule(t *testing.T) {
	check := craCheck(t, "hjres-119-41", hjres41Title, hjres41Text, "2025-02-20")
	store, _, _, err := runCRARules(t, map[string]string{"Postsecondary Student Success Grant": "search-pssg.json"},
		nil, check)
	if err != nil {
		t.Fatalf("SyncCRARules: %v", err)
	}
	r := store.row(t, "hjres-119-41")
	checkMatch(t, r, repository.CRAMethodTitle, "2024-17709")
	if deref(r.Cited) != "89 Fed. Reg. 48517 (June 7, 2024)" {
		t.Errorf("cited = %s, want the proposed rule's citation", deref(r.Cited))
	}

	store, _, _, err = runCRARules(t, nil, nil, check)
	if err != nil {
		t.Fatalf("SyncCRARules: %v", err)
	}
	checkMatch(t, store.row(t, "hjres-119-41"), repository.CRAMethodCitation, "2024-12502")
	if d := store.doc(t, "2024-12502"); d.DocType != fedreg.DocProposedRule {
		t.Errorf("document type = %q, want %q", d.DocType, fedreg.DocProposedRule)
	}
}

// S.J.Res. 143 disapproves the CFPB's withdrawal of its guidance: the withdrawal is the match,
// and the withdrawn circular is stored as withdrawn_document_number.
func TestSyncCRARules_Withdrawal(t *testing.T) {
	store, _, _, err := runCRARules(t, nil, nil,
		craCheck(t, "sjres-119-143", sjres143Title, sjres143Text, "2025-06-10"))
	if err != nil {
		t.Fatalf("SyncCRARules: %v", err)
	}
	r := store.row(t, "sjres-119-143")
	checkMatch(t, r, repository.CRAMethodCitation, "2025-08286")
	if deref(r.WithdrawnDocumentNumber) != "2023-10982" {
		t.Errorf("withdrawn = %s, want 2023-10982", deref(r.WithdrawnDocumentNumber))
	}
	if w := store.doc(t, "2023-10982"); !strings.HasPrefix(w.Title, "Consumer Financial Protection Circular 2023-02") {
		t.Errorf("withdrawn title = %q", w.Title)
	}
	store.doc(t, "2025-08286")
}

// When the Federal Register returns 503s, nothing is written, the bills stay due, the run stops
// after craMaxConsecutiveErrors failures in a row, and it fails since every lookup failed.
func TestSyncCRARules_Outage(t *testing.T) {
	var checks []repository.CRARuleCheck
	for _, id := range []string{"sjres-119-1", "sjres-119-2", "sjres-119-3", "sjres-119-4", "sjres-119-5",
		"sjres-119-6", "sjres-119-7"} {
		checks = append(checks, craCheck(t, id, sjres18Title, sjres18Text, "2025-02-04"))
	}
	store, fr, flushed, err := runCRARules(t, nil, func(f *frServer, _ *craStore) { f.down = true }, checks...)
	if err == nil || !strings.Contains(err.Error(), "every CRA rule lookup failed") {
		t.Fatalf("SyncCRARules = %v, want every lookup failed", err)
	}
	if len(store.rows) != 0 || len(store.docs) != 0 || len(flushed) != 0 {
		t.Errorf("wrote %d rows and %d documents, flushed %v; want nothing", len(store.rows), len(store.docs), flushed)
	}
	if fr.count() != craMaxConsecutiveErrors {
		t.Errorf("%d requests, want %d: the run stops after that many failures", fr.count(), craMaxConsecutiveErrors)
	}
	if len(store.failures) != 1 || store.failures[0].Step != stepCRARules || len(store.successes) != 0 {
		t.Errorf("failures %+v, successes %+v; want one cra_rules failure", store.failures, store.successes)
	}
}

// One failed lookup leaves that bill without a row and the run succeeds with a warning; the day
// another bill cites again is read once.
func TestSyncCRARules_PartialFailure(t *testing.T) {
	store, fr, _, err := runCRARules(t, nil, func(f *frServer, _ *craStore) { f.failDays["2025-07-03"] = true },
		craCheck(t, "sjres-119-67", sjres67Title, sjres67Text, "2025-07-15"),
		craCheck(t, "sjres-119-18", sjres18Title, sjres18Text, "2025-02-04"),
		craCheck(t, "hjres-119-999", sjres18Title, sjres18Text, "2025-02-04"))
	if err != nil {
		t.Fatalf("SyncCRARules: %v", err)
	}
	if len(store.rows) != 2 || slices.ContainsFunc(store.rows, func(r repository.CRARuleRow) bool {
		return r.BillID == "sjres-119-67"
	}) {
		t.Errorf("rows = %+v, want the two December 30 bills only", store.rows)
	}
	if len(store.docs) != 1 {
		t.Errorf("upserted %d documents, want 1: once a run", len(store.docs))
	}
	if fr.count() != 2 {
		t.Errorf("%d requests, want 2: July 3 once, December 30 once", fr.count())
	}
	if len(store.successes) != 1 || store.successes[0].Warning != "1 of 3 CRA rule lookups failed" {
		t.Errorf("successes = %+v, want one with the warning", store.successes)
	}
}

// A resolution that names no agency and rule is stored unmatched as unparsed, with no request; an
// unchanged row with an unchanged document marks nothing.
func TestSyncCRARules_UnparsedAndUnchanged(t *testing.T) {
	store, fr, flushed, err := runCRARules(t, nil, func(_ *frServer, s *craStore) {
		s.unchanged = map[string]bool{"sjres-119-18": true}
		s.stored = map[string]bool{overdraftDoc: true}
	},
		craCheck(t, "sjres-119-9", "A joint resolution disapproving the rule.", "", "2025-02-04"),
		craCheck(t, "sjres-119-18", sjres18Title, sjres18Text, "2025-02-04"))
	if err != nil {
		t.Fatalf("SyncCRARules: %v", err)
	}
	checkUnmatched(t, store.row(t, "sjres-119-9"), repository.CRAReasonUnparsed)
	if !slices.Equal(flushed, []string{"sjres-119-9"}) {
		t.Errorf("flushed %v, want only the new unparsed row", flushed)
	}
	if fr.count() != 1 {
		t.Errorf("%d requests, want 1 (none for the unparsed resolution)", fr.count())
	}
}

// A citation without a date is looked up by searching its year for the title. A resolution with no
// introduction date searches up to today.
func TestSyncCRARules_UndatedCitationAndNoIntroducedDate(t *testing.T) {
	undated := strings.Replace(sjres18Text, " (December 30, 2024)", "", 1)
	uncited := strings.Replace(sjres18Text, " (89 Fed. Reg. 106768 (December 30, 2024))", "", 1)
	noDate := craCheck(t, "sjres-119-19", sjres18Title, uncited, "2025-02-04")
	noDate.IntroducedDate = nil
	store, _, _, err := runCRARules(t, map[string]string{overdraftTitle: "search-overdraft.json"}, nil,
		craCheck(t, "sjres-119-18", sjres18Title, undated, "2025-02-04"), noDate)
	if err != nil {
		t.Fatalf("SyncCRARules: %v", err)
	}
	r := store.row(t, "sjres-119-18")
	checkMatch(t, r, repository.CRAMethodCitation, overdraftDoc)
	if deref(r.Cited) != "89 Fed. Reg. 106768" {
		t.Errorf("cited = %s", deref(r.Cited))
	}
	checkMatch(t, store.row(t, "sjres-119-19"), repository.CRAMethodTitle, overdraftDoc)
}

// A failed write leaves the bill due like a failed lookup: when every bill's write fails, the run
// fails.
func TestSyncCRARules_StoreErrors(t *testing.T) {
	for name, prepare := range map[string]func(*frServer, *craStore){
		"document": func(_ *frServer, s *craStore) { s.docErr = errors.New("spanner down") },
		"rule":     func(_ *frServer, s *craStore) { s.ruleErr = errors.New("spanner down") },
	} {
		store, _, flushed, err := runCRARules(t, nil, prepare,
			craCheck(t, "sjres-119-18", sjres18Title, sjres18Text, "2025-02-04"))
		if err == nil || len(store.rows) != 0 || len(flushed) != 0 {
			t.Errorf("%s write fails: err %v, rows %d, flushed %v; want an error and nothing", name, err,
				len(store.rows), flushed)
		}
	}
}

// After a success, craMaxConsecutiveErrors failures in a row stop the run, which succeeds with a
// warning; the bills after them aren't tried.
func TestSyncCRARules_StopsAfterFailuresInARow(t *testing.T) {
	checks := []repository.CRARuleCheck{craCheck(t, "sjres-119-18", sjres18Title, sjres18Text, "2025-02-04")}
	for i := range craMaxConsecutiveErrors + 2 {
		checks = append(checks, craCheck(t, "sjres-119-"+strconv.Itoa(100+i), sjres67Title, sjres67Text, "2025-07-15"))
	}
	store, fr, _, err := runCRARules(t, nil, func(f *frServer, _ *craStore) { f.failDays["2025-07-03"] = true },
		checks...)
	if err != nil {
		t.Fatalf("SyncCRARules: %v", err)
	}
	if fr.count() != 1+craMaxConsecutiveErrors {
		t.Errorf("%d requests, want %d", fr.count(), 1+craMaxConsecutiveErrors)
	}
	want := "stopped after 5 failed CRA rule lookups in a row; 1 of 8 due bills checked"
	if len(store.successes) != 1 || store.successes[0].Warning != want {
		t.Errorf("successes = %+v, want one warning %q", store.successes, want)
	}
}

// Without a Federal Register client the step skips itself and records nothing.
func TestSyncCRARules_NotConfigured(t *testing.T) {
	store := &craStore{}
	if err := newBackfillService(store).SyncCRARules(t.Context(), 119, 0); err != nil {
		t.Fatalf("SyncCRARules: %v", err)
	}
	if len(store.successes)+len(store.failures) != 0 {
		t.Error("the skipped step recorded a run")
	}
}

func TestAcceptByTitle(t *testing.T) {
	introduced := *date(t, time.DateOnly, "2026-01-15")
	doc := func(number, published string) repository.FRDocumentRow {
		return repository.FRDocumentRow{DocumentNumber: number, PublicationDate: *date(t, time.DateOnly, published)}
	}
	for name, tc := range map[string]struct {
		docs      []repository.FRDocumentRow
		truncated bool
		want      string // the document accepted, or the reason none was
	}{
		"none":           {nil, false, repository.CRAReasonNoCandidates},
		"one, any age":   {[]repository.FRDocumentRow{doc("old", "2009-03-01")}, false, "old"},
		"one, truncated": {[]repository.FRDocumentRow{doc("old", "2009-03-01")}, true, repository.CRAReasonAmbiguous},
		"newest recent, next a year older": {
			[]repository.FRDocumentRow{doc("prev", "2024-09-01"), doc("new", "2025-12-31")}, false, "new",
		},
		"newest recent, next a year older, truncated": {
			[]repository.FRDocumentRow{doc("prev", "2024-09-01"), doc("new", "2025-12-31")}, true, "new",
		},
		"next less than a year older": {
			[]repository.FRDocumentRow{doc("new", "2025-12-31"), doc("prev", "2025-03-01")}, false,
			repository.CRAReasonAmbiguous,
		},
		"newest over two years before": {
			[]repository.FRDocumentRow{doc("new", "2023-12-01"), doc("prev", "2020-01-01")}, false,
			repository.CRAReasonAmbiguous,
		},
	} {
		got, reason, err := acceptByTitle(tc.docs, introduced, tc.truncated)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if got != nil {
			reason = got.DocumentNumber
		}
		if reason != tc.want {
			t.Errorf("%s: got %q, want %q", name, reason, tc.want)
		}
	}
}

// Links off www.federalregister.gov and www.govinfo.gov are dropped at ingest: the page link
// becomes the Federal Register's short link, the PDF none. Entities are unescaped once, and a
// document missing its number or date is never stored.
func TestDocumentRowChecksLinksAndFields(t *testing.T) {
	m := newCRAMatcher(nil, newBackfillService(nil).logger, time.Now())
	row, ok := m.documentRow(t.Context(), fedreg.Document{
		DocumentNumber: "2025-00001", Volume: 90, StartPage: 10, EndPage: 1, Type: "Rule",
		Title: "Fish &amp; Wildlife:  Rules", PublicationDate: "2025-01-02", EffectiveOn: "soon",
		HTMLURL: "https://evil.example/documents/2025-00001", PDFURL: "http://www.govinfo.gov/x.pdf",
		Agencies: []fedreg.Agency{{RawName: "Unlisted &amp; Agency"}, {}},
	})
	if !ok {
		t.Fatal("documentRow rejected a complete document")
	}
	if row.HTMLURL != "https://www.federalregister.gov/d/2025-00001" || row.PDFURL != nil {
		t.Errorf("links = %s, %s; want the short link and no PDF", row.HTMLURL, deref(row.PDFURL))
	}
	if row.Title != "Fish & Wildlife: Rules" || row.Citation != "90 FR 10" || row.EndPage != 10 ||
		row.EffectiveOn != nil || row.Action != nil || row.Abstract != nil {
		t.Errorf("row = %+v", row)
	}
	if len(row.Agencies) != 1 || row.Agencies[0].Name != "Unlisted & Agency" {
		t.Errorf("agencies = %+v", row.Agencies)
	}
	for _, d := range []fedreg.Document{
		{DocumentNumber: "../2025", Volume: 90, StartPage: 10, Title: "T", PublicationDate: "2025-01-02"},
		{DocumentNumber: "2025-00001", Volume: 90, StartPage: 10, Title: "T", PublicationDate: "January 2"},
		{DocumentNumber: "2025-00001", Volume: 90, StartPage: 10, Title: " ", PublicationDate: "2025-01-02"},
	} {
		if _, ok = m.documentRow(t.Context(), d); ok {
			t.Errorf("documentRow accepted %+v", d)
		}
	}
	changed := row
	changed.Title = "Other"
	if frContentHash(changed) == row.ContentHash {
		t.Error("content hash didn't change with the title")
	}
}
