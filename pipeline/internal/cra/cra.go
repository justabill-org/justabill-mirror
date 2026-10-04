// Package cra recognizes Congressional Review Act (CRA) resolutions and reads, from a
// resolution's title and stored text, the rule it disapproves: the agency, the rule's title, the
// Federal Register citations, and whether it identifies the rule by a GAO opinion instead
// (docs/design/590-cra-disapproved-rules.md). It's pure: it makes no requests.
package cra

import (
	"errors"
	"html"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/justabill-org/justabill/db/repository"
)

// MatcherVersion names the detection and matching rules. It's stored on every bill_cra_rules
// row; changing the rules bumps it, which makes every resolution due for a lookup again.
const MatcherVersion = "cra-v1"

// ErrUnparsed means neither the text nor the title names the rule's agency and title.
var ErrUnparsed = errors.New("cra: no agency and rule title found")

// Patterns. Each reads plain text, as [PlainText] makes it.
var (
	titlePattern = regexp.MustCompile(repository.CRATitlePattern)
	tagPattern   = regexp.MustCompile(`<[^>]*>`)
	// inlineTagPattern is the tags inside a word or phrase, which go without a space: NO<sub>X</sub>.
	inlineTagPattern = regexp.MustCompile(`(?i)</?(?:quote|sub|subscript|sup|superscript|i|b|em|strong|span|` +
		`italic|bold|term|external-xref|short-title)\b[^>]*>`)
	spacePattern = regexp.MustCompile(`\s+`)
	// clausePattern finds the resolving clause, up to its "force or effect" when it has one.
	clausePattern = regexp.MustCompile(`(?i)\bCongress\s+disapproves\b.*?(?:force\s+or\s+effect|$)`)
	// namePattern reads "submitted by (the) <agency> relating to (the) <rest>".
	namePattern = regexp.MustCompile(`(?i)\b(?:submitted|issued)\s+by\s+(?:the\s+)?(.+?),?\s+relating\s+(?:to\s+)?` +
		`(?:the\s+)?(.+)$`)
	withdrawalPattern = regexp.MustCompile(`(?i)^withdrawal\s+of\s+the\s+rule\s+relating\s+to\s+(?:the\s+)?(.+)$`)
	// titleEndPattern ends a rule's title where the clause goes on to something else.
	titleEndPattern = regexp.MustCompile(`(?i)\s*(?:[,;]\s*published\b|[,;]\s*issued\b|,?\s+and\s+such\s+rule\b|` +
		`,\s*determined\s+to\s+be\b|,?\s+\d+\s*(?:Fed\.?\s*Reg\b|FR\b))`)
	citationPattern = regexp.MustCompile(`(\d+)\s*(?:Fed\.?\s*Reg\.?|F\.\s*R\.|FR)\s*(\d+(?:,\d{3})*)`)
	// dateAfterPattern is a citation's date right after it: "(December 30, 2024)",
	// "; published July 30, 2024" or ", Oct. 2, 2025".
	dateAfterPattern = regexp.MustCompile(`^\s*(?:\(|[,;]\s*(?:published\s+(?:on\s+)?)?)(` + datePart + `)`)
	// dateBeforePattern is "published May 12, 2026 (" right before a citation.
	dateBeforePattern = regexp.MustCompile(`(?i)published\s+(?:on\s+)?(` + datePart + `)\s*\(\s*$`)
	issuedPattern     = regexp.MustCompile(`(?i)\b(?:issued|published)\s+(?:on\s+)?(` + datePart + `)`)
	gaoPattern        = regexp.MustCompile(`(?i)letter\s+of\s+opinion\s+(?:from|by)\s+the\s+Government\s+` +
		`Accountability\s+Office`)
	spaceBeforePunct = regexp.MustCompile(`\s+([,.;:)\]])`)
	spaceAfterParen  = regexp.MustCompile(`([(\[])\s+`)
)

// datePart matches a written date: "December 30, 2024", "Oct. 2, 2025", "Sept. 9, 2024".
const datePart = `[A-Z][a-z]{2,8}\.?\s+\d{1,2},\s+\d{4}`

// Citation is one Federal Register citation in a resolution, such as
// "89 Fed. Reg. 106768 (December 30, 2024)".
type Citation struct {
	// Text is the citation as written, with its date when one follows it.
	Text   string
	Volume int
	Page   int
	// Date is the publication date the text gives with the citation; zero when it gives none.
	Date time.Time
}

// Resolution is what a CRA resolution says about the rule it disapproves.
type Resolution struct {
	// Agency is the agency the rule was submitted or issued by, as the resolution names it.
	Agency string
	// RuleTitle is the rule's title as the resolution names it, without quotes. For a
	// disapproved withdrawal it's the whole "withdrawal of the rule relating to …".
	RuleTitle string
	// Withdrawal is set when the disapproved rule withdraws another; WithdrawnTitle is the
	// withdrawn document's title.
	Withdrawal     bool
	WithdrawnTitle string
	// Citations are the Federal Register citations in the resolving clause, in order. The last
	// is the disapproved document; with Withdrawal, the one before it is the withdrawn one.
	Citations []Citation
	// GAOOpinion is set when the resolution identifies the action by a GAO letter of opinion
	// that it is a rule. IssuedOn is the "issued" or "published" date it gives, if any.
	GAOOpinion bool
	IssuedOn   time.Time
	// FromText is set when the agency and title come from the text's resolving clause rather
	// than from the bill's title.
	FromText bool
}

// Disapproved returns the citation of the disapproved document, or false when there's none.
func (r Resolution) Disapproved() (Citation, bool) {
	if len(r.Citations) == 0 {
		return Citation{}, false
	}
	return r.Citations[len(r.Citations)-1], true
}

// Withdrawn returns the citation of the document a disapproved withdrawal withdrew, or false.
func (r Resolution) Withdrawn() (Citation, bool) {
	if !r.Withdrawal || len(r.Citations) < 2 {
		return Citation{}, false
	}
	return r.Citations[len(r.Citations)-2], true
}

// IsCRA reports whether a bill is a CRA resolution: a joint resolution (sjres or hjres) whose
// title matches [repository.CRATitlePattern].
func IsCRA(billType, title string) bool {
	switch strings.ToLower(billType) {
	case "sjres", "hjres":
		return titlePattern.MatchString(title)
	default:
		return false
	}
}

// Parse reads a CRA resolution's title and stored text (bill XML or HTML, or empty when there's
// none yet). The agency and rule title come from the text's resolving clause, or else from the
// title; citations, the GAO opinion and the issue date only from the clause. It returns
// [ErrUnparsed] when neither names the agency and the rule.
func Parse(title, text string) (Resolution, error) {
	var r Resolution
	clause := clausePattern.FindString(PlainText(text))
	if clause != "" {
		r.Agency, r.RuleTitle = names(clause)
		r.FromText = r.Agency != ""
		r.Citations = citations(clause)
		r.GAOOpinion = gaoPattern.MatchString(clause)
		if m := issuedPattern.FindStringSubmatch(clause); m != nil {
			r.IssuedOn = parseDate(m[1])
		}
	}
	if !r.FromText {
		r.Agency, r.RuleTitle = names(PlainText(title))
	}
	if r.Agency == "" || r.RuleTitle == "" {
		return r, ErrUnparsed
	}
	if m := withdrawalPattern.FindStringSubmatch(r.RuleTitle); m != nil {
		r.Withdrawal, r.WithdrawnTitle = true, m[1]
	}
	return r, nil
}

// PlainText turns stored bill XML or HTML into one line of text: tags removed (inline ones
// without a space), entities
// unescaped, whitespace collapsed, and no space left inside parentheses or before punctuation.
func PlainText(s string) string {
	s = inlineTagPattern.ReplaceAllString(s, "")
	s = html.UnescapeString(tagPattern.ReplaceAllString(s, " "))
	s = strings.TrimSpace(spacePattern.ReplaceAllString(s, " "))
	s = spaceBeforePunct.ReplaceAllString(s, "$1")
	return spaceAfterParen.ReplaceAllString(s, "$1")
}

// names reads the agency and the rule's title from a clause or a title.
func names(s string) (string, string) {
	m := namePattern.FindStringSubmatch(s)
	if m == nil {
		return "", ""
	}
	agency := strings.TrimSpace(m[1])
	rest := m[2]
	if loc := titleEndPattern.FindStringIndex(rest); loc != nil {
		rest = rest[:loc[0]]
	}
	rest = cutAtParenthetical(rest)
	return agency, cleanTitle(rest)
}

// cutAtParenthetical ends a title at the first parenthetical that holds a number: a citation,
// a date or a notice number, as in "(89 Fed. Reg. 106768 …)" or "(issued November 20, 2024 …)".
// A parenthetical without one, such as "(CCDF)" or "(WISeR)", is part of the title.
func cutAtParenthetical(s string) string {
	for i := range len(s) {
		if s[i] != '(' {
			continue
		}
		end := strings.IndexByte(s[i:], ')')
		inner := s[i:]
		if end >= 0 {
			inner = s[i : i+end]
		}
		if strings.ContainsAny(inner, "0123456789") {
			return s[:i]
		}
	}
	return s
}

// cleanTitle drops quotes and the trailing punctuation and space around a title.
func cleanTitle(s string) string {
	s = strings.NewReplacer("“", "", "”", "", `"`, "", "``", "", "''", "").Replace(s)
	s = strings.TrimSpace(s)
	s = strings.TrimRight(s, " ,;.")
	return strings.TrimSpace(s)
}

// citations returns the clause's Federal Register citations in order, each with its date.
func citations(clause string) []Citation {
	var out []Citation
	for _, loc := range citationPattern.FindAllStringSubmatchIndex(clause, -1) {
		volume, vErr := strconv.Atoi(clause[loc[2]:loc[3]])
		page, pErr := strconv.Atoi(strings.ReplaceAll(clause[loc[4]:loc[5]], ",", ""))
		if vErr != nil || pErr != nil {
			continue
		}
		c := Citation{Text: clause[loc[0]:loc[1]], Volume: volume, Page: page}
		if m := dateAfterPattern.FindStringSubmatchIndex(clause[loc[1]:]); m != nil {
			c.Date = parseDate(clause[loc[1]+m[2] : loc[1]+m[3]])
			if !c.Date.IsZero() {
				c.Text = clause[loc[0] : loc[1]+m[1]]
				if strings.HasPrefix(strings.TrimSpace(clause[loc[1]:]), "(") {
					c.Text += ")"
				}
			}
		} else if m := dateBeforePattern.FindStringSubmatch(clause[:loc[0]]); m != nil {
			c.Date = parseDate(m[1])
		}
		out = append(out, c)
	}
	return out
}

// parseDate reads a written date, full or abbreviated ("Oct.", "Sept."); zero when it can't.
func parseDate(s string) time.Time {
	s = spacePattern.ReplaceAllString(strings.TrimSpace(s), " ")
	month, rest, ok := strings.Cut(s, " ")
	if !ok {
		return time.Time{}
	}
	month = strings.TrimSuffix(month, ".")
	if strings.EqualFold(month, "Sept") {
		month = "Sep"
	}
	for _, layout := range []string{"January 2, 2006", "Jan 2, 2006"} {
		if t, err := time.Parse(layout, month+" "+rest); err == nil {
			return t
		}
	}
	return time.Time{}
}
