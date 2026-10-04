package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/justabill-org/justabill/pipeline/internal/ai"
	"github.com/justabill-org/justabill/pipeline/internal/cra"
)

// The rule eval (docs/design/590-cra-disapproved-rules.md, "Prompt context" > Eval): with --rule
// each model runs twice, without the <disapproved_rule> block and with it, reported as <model> and
// <model>+rule. The list (testdata/bills-cra.json) carries each resolution's rule as the pipeline's
// matcher found it, since the eval has no Spanner.
const (
	ruleSuffix = "+rule"
	// ruleWordMin is the shortest word counted as a rule word: shorter ones are mostly function
	// words.
	ruleWordMin = 5
)

// How the matcher found a resolution's rule: the values of bill_cra_rules.method, or unmatched.
const (
	matchCitation  = "citation"
	matchTitle     = "title"
	matchUnmatched = "unmatched"
)

// civilDate is a date written as 2006-01-02 in the bill list.
type civilDate struct{ time.Time }

// UnmarshalJSON reads a "2006-01-02" string, or "" for none.
func (d *civilDate) UnmarshalJSON(data []byte) error {
	var s string
	if err := json.Unmarshal(data, &s); err != nil {
		return fmt.Errorf("date: %w", err)
	}
	if s == "" {
		d.Time = time.Time{}
		return nil
	}
	t, err := time.Parse(time.DateOnly, s)
	if err != nil {
		return fmt.Errorf("date %q: %w", s, err)
	}
	d.Time = t
	return nil
}

// evalRuleDoc is a Federal Register document in the bill list, with the fields of
// [ai.RuleDocument].
type evalRuleDoc struct {
	Title       string    `json:"title"`
	Agencies    []string  `json:"agencies"`
	DocType     string    `json:"doc_type"`
	Action      string    `json:"action"`
	Citation    string    `json:"citation"`
	Published   civilDate `json:"published"`
	EffectiveOn civilDate `json:"effective_on"`
	Abstract    string    `json:"abstract"`
}

// evalRule is the rule a CRA resolution disapproves, as the matcher found it: Match is how
// (matchCitation, matchTitle or matchUnmatched), Reason why not when unmatched, and Cited the
// citation the text gives. Title and Agency are as the resolution names them.
type evalRule struct {
	Match     string       `json:"match"`
	Reason    string       `json:"reason"`
	Cited     string       `json:"cited"`
	Title     string       `json:"title"`
	Agency    string       `json:"agency"`
	Document  *evalRuleDoc `json:"document"`
	Withdrawn *evalRuleDoc `json:"withdrawn"`
}

// errBadRule is a bill list entry whose rule doesn't hold together.
var errBadRule = errors.New("bad rule")

// validate checks that the rule is what the matcher can produce: a title and agency, a document
// exactly when it matched, and a withdrawn document only with a document.
func (r *evalRule) validate() error {
	switch {
	case strings.TrimSpace(r.Title) == "" || strings.TrimSpace(r.Agency) == "":
		return fmt.Errorf("%w: no title or agency", errBadRule)
	case r.Match != matchCitation && r.Match != matchTitle && r.Match != matchUnmatched:
		return fmt.Errorf("%w: match %q", errBadRule, r.Match)
	case r.Match == matchUnmatched && (r.Document != nil || r.Withdrawn != nil):
		return fmt.Errorf("%w: unmatched with a document", errBadRule)
	case r.Match != matchUnmatched && r.Document == nil:
		return fmt.Errorf("%w: matched by %s without a document", errBadRule, r.Match)
	}
	for _, d := range []*evalRuleDoc{r.Document, r.Withdrawn} {
		if d != nil && (d.Title == "" || d.Published.IsZero()) {
			return fmt.Errorf("%w: a document without a title or publication date", errBadRule)
		}
	}
	return nil
}

// context is the rule as the pipeline gives it to the model; nil for none.
func (r *evalRule) context() *ai.RuleContext {
	if r == nil {
		return nil
	}
	return &ai.RuleContext{
		Title: r.Title, Agency: r.Agency, Document: r.Document.document(), Withdrawn: r.Withdrawn.document(),
	}
}

func (d *evalRuleDoc) document() *ai.RuleDocument {
	if d == nil {
		return nil
	}
	return &ai.RuleDocument{
		Title: d.Title, Agencies: d.Agencies, DocType: d.DocType, Action: d.Action, Citation: d.Citation,
		Published: d.Published.Time, EffectiveOn: d.EffectiveOn.Time, Abstract: d.Abstract,
	}
}

// documentText is the text of the rule's Federal Register documents that the prompt carries:
// their titles, actions and abstracts.
func (r *evalRule) documentText() string {
	var b strings.Builder
	for _, d := range []*evalRuleDoc{r.Document, r.Withdrawn} {
		if d != nil {
			b.WriteString(strings.Join([]string{d.Title, d.Action, d.Abstract, ""}, "\n"))
		}
	}
	return b.String()
}

// sourceText is the rule as the prompt gives it, for the neutrality checks: a term the
// agency's own words use, like "Historic" in the National Historic Preservation Act, is quoting.
// It's "" for no rule.
func (r *evalRule) sourceText() string {
	if r == nil {
		return ""
	}
	return r.Title + "\n" + r.Agency + "\n" + r.documentText()
}

// ruleSignals are the automatic checks of a CRA resolution's summary. They're signals for the
// reviewer, not the verdict: NoForce is set when the summary says the rule would lose its force or
// effect or stop applying; Attributed when it attributes something to the agency ("the agency
// said"); RuleWords counts the distinct words of 5 letters or more that the summary shares with
// the Federal Register document but not with the resolution, nil when there's no document.
type ruleSignals struct {
	NoForce    bool `json:"no_force_or_effect"`
	Attributed bool `json:"attributed_to_agency"`
	RuleWords  *int `json:"rule_words,omitempty"`
}

var (
	// noForcePattern is the CRA's effect in the words summaries use for it.
	noForcePattern = regexp.MustCompile(`(?i)no (?:legal )?force or effect|` +
		`(?:would|will) (?:no longer|not|cease to) (?:apply|take effect|be in effect|have (?:any )?(?:legal )?effect)|` +
		`\b(?:nullif\w*|invalidat\w*|void)\b`)
	// attributedPattern is a statement attributed to the agency or a named part of it.
	attributedPattern = regexp.MustCompile(`(?i)\b(?:agency|bureau|department|commission|administration|` +
		`service|board|office|corporation|EPA|CFPB|FTC|FDIC|OCC|NLRB)(?:'s|’s)?\s+(?:said|says|stated|states|` +
		`described|describes|explained|explains|wrote|noted|notes|indicated|indicates|reported)\b|` +
		`\baccording to (?:the|its)\b`)
)

// checkRule computes the signals for a summary of a resolution whose rule is r, with the
// resolution's title and stored text.
func checkRule(summary string, r *evalRule, title, text string) *ruleSignals {
	s := &ruleSignals{NoForce: noForcePattern.MatchString(summary), Attributed: attributedPattern.MatchString(summary)}
	if r.Document == nil {
		return s
	}
	bill := wordSet(title + " " + cra.PlainText(text))
	doc := wordSet(r.documentText())
	n := 0
	for w := range wordSet(summary) {
		if doc[w] && !bill[w] {
			n++
		}
	}
	s.RuleWords = &n
	return s
}

// wordSet is the distinct words of s of at least ruleWordMin characters, lowercased.
func wordSet(s string) map[string]bool {
	out := make(map[string]bool)
	for _, w := range words(s) {
		if len([]rune(w)) >= ruleWordMin {
			out[w] = true
		}
	}
	return out
}
