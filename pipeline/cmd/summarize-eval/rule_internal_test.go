package main

import (
	"context"
	"log/slog"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/justabill-org/justabill/pipeline/internal/ai"
)

const (
	overdraftTitle = `A joint resolution disapproving the rule submitted by the Bureau of Consumer Financial ` +
		`Protection relating to "Overdraft Lending: Very Large Financial Institutions".`
	overdraftText = `<section><text>That Congress disapproves the rule submitted by the Bureau of Consumer ` +
		`Financial Protection relating to <quote>Overdraft Lending: Very Large Financial Institutions</quote>, ` +
		`and such rule shall have no force or effect.</text></section>`
	overdraftAbstract = `The Bureau is amending Regulation Z to update overdraft credit provisions for very ` +
		`large financial institutions with assets above $10 billion.`
)

// overdraftRule is S.J.Res. 18's rule as the list writes it: matched by citation.
func overdraftRule() *evalRule {
	return &evalRule{
		Match:  matchCitation,
		Cited:  "89 Fed. Reg. 106768 (December 30, 2024)",
		Title:  "Overdraft Lending: Very Large Financial Institutions",
		Agency: "Bureau of Consumer Financial Protection",
		Document: &evalRuleDoc{
			Title:       "Overdraft Lending: Very Large Financial Institutions",
			Agencies:    []string{"Consumer Financial Protection Bureau"},
			DocType:     "Rule",
			Action:      "Final rule; official interpretation.",
			Citation:    "89 FR 106768",
			Published:   civilDate{time.Date(2024, time.December, 30, 0, 0, 0, 0, time.UTC)},
			EffectiveOn: civilDate{time.Date(2025, time.October, 1, 0, 0, 0, 0, time.UTC)},
			Abstract:    overdraftAbstract,
		},
	}
}

func TestLoadBillsReadsTheRule(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bills.json")
	list := `[{"bill_id":"sjres-119-18","category":"cra","package_id":"BILLS-119sjres18enr",
	  "rule":{"match":"citation","cited":"89 Fed. Reg. 106768 (December 30, 2024)",
	    "title":"Overdraft Lending","agency":"Bureau of Consumer Financial Protection",
	    "document":{"title":"Overdraft Lending","agencies":["Consumer Financial Protection Bureau","Treasury"],
	      "doc_type":"Rule","action":"Final rule.","citation":"89 FR 106768","published":"2024-12-30",
	      "effective_on":"2025-10-01","abstract":"The Bureau amends Regulation Z."},
	    "withdrawn":{"title":"Old guidance","agencies":["Consumer Financial Protection Bureau"],
	      "doc_type":"Notice","citation":"81 FR 1","published":"2016-01-04"}}}]`
	if err := os.WriteFile(path, []byte(list), 0o600); err != nil {
		t.Fatal(err)
	}
	bills, err := loadBills(path)
	if err != nil {
		t.Fatal(err)
	}
	got := bills[0].Rule.context()
	want := &ai.RuleContext{
		Title: "Overdraft Lending", Agency: "Bureau of Consumer Financial Protection",
		Document: &ai.RuleDocument{
			Title: "Overdraft Lending", Agencies: []string{"Consumer Financial Protection Bureau", "Treasury"},
			DocType: "Rule", Action: "Final rule.", Citation: "89 FR 106768",
			Published:   time.Date(2024, time.December, 30, 0, 0, 0, 0, time.UTC),
			EffectiveOn: time.Date(2025, time.October, 1, 0, 0, 0, 0, time.UTC),
			Abstract:    "The Bureau amends Regulation Z.",
		},
		Withdrawn: &ai.RuleDocument{
			Title: "Old guidance", Agencies: []string{"Consumer Financial Protection Bureau"}, DocType: "Notice",
			Citation: "81 FR 1", Published: time.Date(2016, time.January, 4, 0, 0, 0, 0, time.UTC),
		},
	}
	if got.Title != want.Title || got.Agency != want.Agency || !sameDoc(got.Document, want.Document) ||
		!sameDoc(got.Withdrawn, want.Withdrawn) {
		t.Errorf("context = %+v %+v %+v,\nwant %+v %+v %+v", got, got.Document, got.Withdrawn, want, want.Document,
			want.Withdrawn)
	}
	if (*evalRule)(nil).context() != nil {
		t.Error("a bill without a rule gets a rule context")
	}
}

func sameDoc(a, b *ai.RuleDocument) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Title == b.Title && slices.Equal(a.Agencies, b.Agencies) && a.DocType == b.DocType &&
		a.Action == b.Action && a.Citation == b.Citation && a.Published.Equal(b.Published) &&
		a.EffectiveOn.Equal(b.EffectiveOn) && a.Abstract == b.Abstract
}

func TestLoadBillsRejectsBadRules(t *testing.T) {
	const doc = `{"title":"T","agencies":["A"],"doc_type":"Rule","citation":"89 FR 1","published":"2024-12-30"}`
	entry := func(category, rule string) string {
		e := `{"bill_id":"sjres-119-18","category":"` + category + `","package_id":"BILLS-119sjres18enr"`
		if rule != "" {
			e += `,"rule":` + rule
		}
		return "[" + e + "]"
	}
	tests := map[string]string{
		"cra without a rule":       entry("cra", ""),
		"a rule on another bill":   entry("resolution", `{"match":"unmatched","title":"T","agency":"A"}`),
		"no agency":                entry("cra", `{"match":"unmatched","title":"T"}`),
		"unknown match":            entry("cra", `{"match":"guess","title":"T","agency":"A","document":`+doc+`}`),
		"matched without document": entry("cra", `{"match":"title","title":"T","agency":"A"}`),
		"unmatched with document":  entry("cra", `{"match":"unmatched","title":"T","agency":"A","document":`+doc+`}`),
		"unmatched with withdrawn": entry("cra", `{"match":"unmatched","title":"T","agency":"A","withdrawn":`+doc+`}`),
		"document without a date": entry("cra",
			`{"match":"citation","title":"T","agency":"A","document":{"title":"T","published":""}}`),
		"bad date": entry("cra",
			`{"match":"citation","title":"T","agency":"A","document":{"title":"T","published":"Dec 30, 2024"}}`),
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "bills.json")
			if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := loadBills(path); err == nil {
				t.Error("want an error")
			}
		})
	}
}

func TestCheckRule(t *testing.T) {
	unmatched := &evalRule{Match: matchUnmatched, Title: "Overdraft Lending", Agency: "Bureau"}
	tests := map[string]struct {
		summary    string
		rule       *evalRule
		noForce    bool
		attributed bool
		ruleWords  int // -1: nil
	}{
		// regulation, assets, above and billion come from the abstract; "institutions" and "bureau"
		// are in the resolution too, and "amends" isn't the abstract's "amending".
		"uses the abstract": {
			summary: "The resolution would give the rule no force or effect. The Bureau said the rule amends " +
				"Regulation Z for institutions with assets above $10 billion.",
			rule: overdraftRule(), noForce: true, attributed: true, ruleWords: 4,
		},
		"restates the title": {
			summary: "This joint resolution disapproves the Overdraft Lending rule.",
			rule:    overdraftRule(), ruleWords: 0,
		},
		"would no longer apply": {
			summary: "Its requirements would no longer apply.", rule: unmatched, noForce: true, ruleWords: -1,
		},
		"nullifies": {
			summary:   "The resolution nullifies the rule.",
			rule:      unmatched,
			noForce:   true,
			ruleWords: -1,
		},
		"avoid is not void": {summary: "Banks would avoid fees.", rule: unmatched, ruleWords: -1},
		"according to the": {
			summary:    "According to the agency, it caps fees.",
			rule:       unmatched,
			attributed: true,
			ruleWords:  -1,
		},
		"the resolution states": {
			summary:   "The resolution states that Congress disapproves.",
			rule:      unmatched,
			ruleWords: -1,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			got := checkRule(tt.summary, tt.rule, overdraftTitle, overdraftText)
			if got.NoForce != tt.noForce || got.Attributed != tt.attributed {
				t.Errorf("no force %v, attributed %v; want %v, %v", got.NoForce, got.Attributed, tt.noForce,
					tt.attributed)
			}
			switch {
			case tt.ruleWords < 0 && got.RuleWords != nil:
				t.Errorf("rule words = %d, want none without a document", *got.RuleWords)
			case tt.ruleWords >= 0 && (got.RuleWords == nil || *got.RuleWords != tt.ruleWords):
				t.Errorf("rule words = %v, want %d", got.RuleWords, tt.ruleWords)
			}
		})
	}
}

// ruleSummarizer records the rule each call carried and, given a matched one, says what it does.
type ruleSummarizer struct {
	fakeSummarizer

	rules map[string]*ai.RuleContext
}

func (f *ruleSummarizer) SummarizeBill(ctx context.Context, bc ai.BillContext) (*ai.BillSummary, error) {
	out, err := f.fakeSummarizer.SummarizeBill(ctx, bc)
	if bc.Rule != nil {
		f.mu.Lock()
		f.rules[bc.BillID] = bc.Rule
		f.mu.Unlock()
		if bc.Rule.Document != nil {
			out.ShortSummary = "The rule would have no force or effect."
			out.LongSummary = "The agency said: " + bc.Rule.Document.Abstract
		}
	}
	return out, err
}

func TestRunWithRule(t *testing.T) {
	r, _ := newTestRunner(t, 1)
	r.bills = []evalBill{
		{BillID: "sjres-119-18", Category: categoryCRA, Title: overdraftTitle, Rule: overdraftRule()},
		{BillID: "hjres-119-104", Category: categoryCRA, Title: "Miles City",
			Rule: &evalRule{Match: matchUnmatched, Reason: "no_candidates", Title: "Miles City Plan", Agency: "BLM"}},
		{BillID: "s-119-3", Category: categoryRandom, Title: "Parks"},
	}
	r.texts = map[string]string{"sjres-119-18": overdraftText, "hjres-119-104": "<bill/>", "s-119-3": "<bill/>"}
	s := &ruleSummarizer{rules: map[string]*ai.RuleContext{}}
	s.model = "m"
	for _, v := range (options{rule: true}).variants() {
		r.variant = v
		if err := r.run(t.Context(), s); err != nil {
			t.Fatal(err)
		}
	}
	if len(s.calls) != 6 {
		t.Errorf("%d calls, want 6: each bill without and with its rule", len(s.calls))
	}
	if got := s.rules["sjres-119-18"]; got == nil || got.Document == nil || got.Document.Citation != "89 FR 106768" {
		t.Errorf("sjres-119-18's rule = %+v, want its matched document", got)
	}
	if got := s.rules["hjres-119-104"]; got == nil || got.Document != nil || got.Title != "Miles City Plan" {
		t.Errorf("hjres-119-104's rule = %+v, want its title and no document", got)
	}
	if _, ok := s.rules["s-119-3"]; ok || len(s.rules) != 2 {
		t.Errorf("calls with a rule: %v, want only the two CRA resolutions", s.rules)
	}

	base, withRule := r.results.latest("m"), r.results.latest("m"+ruleSuffix)
	checkRuleRecords(t, base, withRule)

	var b strings.Builder
	if err := writeModelReport(&b, newModelReport("m"+ruleSuffix, r.bills, withRule), time.Now()); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"# Summary eval: m+rule", "No force or effect / median rule words: 1/2 · ",
		"| sjres-119-18 | citation | yes | yes | ", "| hjres-119-104 | unmatched | no | no | n/a |",
		"## Rubric (CRA resolutions)", "| sjres-119-18 | The rule would have no force or effect. | | | | |"} {
		if !strings.Contains(b.String(), want) {
			t.Errorf("report lacks %q:\n%s", want, b.String())
		}
	}
	if strings.Contains(b.String(), "| s-119-3 | random") || strings.Contains(b.String(), "floor votes") {
		t.Errorf("report lists a bill that isn't a CRA resolution:\n%s", b.String())
	}
}

// textSummarizer answers every bill with the same long summary.
type textSummarizer struct {
	fakeSummarizer

	text string
}

func (f *textSummarizer) SummarizeBill(ctx context.Context, bc ai.BillContext) (*ai.BillSummary, error) {
	out, err := f.fakeSummarizer.SummarizeBill(ctx, bc)
	out.ShortSummary, out.LongSummary, out.WhoItAffects = "It disapproves the rule.", f.text, ""
	return out, err
}

// A word the model read in the rule's abstract is quoting in the +rule run, and flagged in the run
// that never saw it.
func TestRunChecksLoadedTermsAgainstTheRuleGiven(t *testing.T) {
	r, _ := newTestRunner(t, 1)
	rule := overdraftRule()
	rule.Document.Abstract = "The rule meets the National Historic Preservation Act."
	r.bills = []evalBill{{BillID: "sjres-119-11", Category: categoryCRA, Title: "Marine resources", Rule: rule}}
	r.texts = map[string]string{"sjres-119-11": "<bill/>"}
	s := &textSummarizer{text: "The agency said the rule meets the National Historic Preservation Act."}
	s.model = "m"
	for _, v := range (options{rule: true}).variants() {
		r.variant = v
		if err := r.run(t.Context(), s); err != nil {
			t.Fatal(err)
		}
	}
	if got := r.results.latest("m")["sjres-119-11"].LoadedTerms; !slices.Equal(got, []string{"historic"}) {
		t.Errorf("base run loaded terms = %v, want [historic]: the prompt had no abstract", got)
	}
	if got := r.results.latest("m" + ruleSuffix)["sjres-119-11"].LoadedTerms; len(got) != 0 {
		t.Errorf("+rule run loaded terms = %v, want none: the abstract uses the word", got)
	}
}

// checkRuleRecords checks TestRunWithRule's records: the rule flag only on the +rule run, and
// signals for the CRA resolutions in both runs.
func checkRuleRecords(t *testing.T, base, withRule map[string]record) {
	t.Helper()
	if len(base) != 3 || len(withRule) != 3 {
		t.Fatalf("runs have %d and %d results, want 3 each", len(base), len(withRule))
	}
	if rec := withRule["sjres-119-18"]; !rec.Rule || rec.RuleSignals == nil || !rec.RuleSignals.NoForce ||
		!rec.RuleSignals.Attributed || rec.RuleSignals.RuleWords == nil || *rec.RuleSignals.RuleWords == 0 {
		t.Errorf("+rule sjres-119-18 = %+v %+v, want the rule flag and its signals", rec, rec.RuleSignals)
	}
	if rec := base["sjres-119-18"]; rec.Rule || rec.RuleSignals == nil || rec.RuleSignals.NoForce ||
		rec.RuleSignals.RuleWords == nil || *rec.RuleSignals.RuleWords != 0 {
		t.Errorf("base sjres-119-18 = %+v %+v, want signals without the rule", rec, rec.RuleSignals)
	}
	if rec := withRule["s-119-3"]; rec.RuleSignals != nil {
		t.Errorf("s-119-3 has rule signals %+v; it isn't a CRA resolution", rec.RuleSignals)
	}
}

func TestVariantLabels(t *testing.T) {
	o := options{models: []string{"a", "b"}, rule: true}
	if got := o.labels(); !slices.Equal(got, []string{"a", "a+rule", "b", "b+rule"}) {
		t.Errorf("labels with --rule = %v", got)
	}
	for _, label := range []string{"gemini-3.8-flash", "gemini-3.8-flash+crs", "gemini-3.8-flash+rule"} {
		if got := labelModel(label); got != "gemini-3.8-flash" {
			t.Errorf("labelModel(%q) = %q", label, got)
		}
	}
}

func TestNewCommandRejectsCRSWithRule(t *testing.T) {
	cmd := newCommand(slog.New(slog.DiscardHandler))
	cmd.SetArgs([]string{"--models", "m", "--crs", "--rule", "--bills", filepath.Join(t.TempDir(), "none.json")})
	if err := cmd.ExecuteContext(t.Context()); err == nil || !strings.Contains(err.Error(), "--crs and --rule") {
		t.Fatalf("err = %v, want --crs and --rule rejected together", err)
	}
}

// The rule eval's list is 20 CRA resolutions of the 119th: 10 matched by citation, 3 disapproved
// withdrawals, 3 matched by title and 4 unmatched (docs/design/590-cra-disapproved-rules.md).
func TestCommittedCRABillList(t *testing.T) {
	bills, err := loadBills(filepath.Join("testdata", "bills-cra.json"))
	if err != nil {
		t.Fatal(err)
	}
	counts := make(map[string]int)
	for _, b := range bills {
		kind := b.Rule.Match
		if b.Rule.Withdrawn != nil {
			kind = "withdrawal"
		}
		counts[kind]++
		if b.Title == "" || b.Version == "" || b.VersionName == "" {
			t.Errorf("%s lacks a title or version", b.BillID)
		}
	}
	want := map[string]int{matchCitation: 10, "withdrawal": 3, matchTitle: 3, matchUnmatched: 4}
	if len(bills) != 20 || !maps.Equal(counts, want) {
		t.Errorf("list has %d bills, %v; want 20, %v", len(bills), counts, want)
	}
}
