package cra

import (
	"html"
	"strings"
	"unicode"
)

// Words the title and agency comparisons ignore. Agency names put the same words in different
// orders ("Bureau of Land Management", "Land Management Bureau"), so the generic ones go.
func titleStopWords() map[string]bool {
	return wordSet("a an and as at by for from in into of on or the to under with")
}

func agencyStopWords() map[string]bool {
	return wordSet("a an and for of on the u s us united states department bureau office agency " +
		"administration commission service federal national")
}

func wordSet(words string) map[string]bool {
	set := map[string]bool{}
	for w := range strings.FieldsSeq(words) {
		set[w] = true
	}
	return set
}

// NormalizeTitle folds a title for exact comparison: entities unescaped, lowercase, quotes,
// dashes and other punctuation turned into spaces, and whitespace collapsed. "Circular
// 2023–02: Reopening" and "circular 2023-02 reopening" normalize the same.
func NormalizeTitle(s string) string {
	return strings.Join(words(s), " ")
}

// words returns a text's lowercase words: runs of letters and digits.
func words(s string) []string {
	s = strings.ToLower(html.UnescapeString(s))
	return strings.FieldsFunc(s, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) })
}

// significant returns the distinct words of s that aren't in stop, in order.
func significant(s string, stop map[string]bool) []string {
	seen := map[string]bool{}
	var out []string
	for _, w := range words(s) {
		if stop[w] || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
	}
	return out
}

// TitleOverlap reports whether at least half of the resolution title's significant words appear
// in the document's title: the guard that sets aside a citation pointing at another document.
func TitleOverlap(resolutionTitle, documentTitle string) bool {
	want := significant(resolutionTitle, titleStopWords())
	if len(want) == 0 {
		return false
	}
	have := map[string]bool{}
	for _, w := range words(documentTitle) {
		have[w] = true
	}
	shared := 0
	for _, w := range want {
		if have[w] {
			shared++
		}
	}
	return 2*shared >= len(want)
}

// AgencyMatches reports whether an agency the resolution names shares a significant word with
// one of the document's agencies, so "Bureau of Consumer Financial Protection" matches "Consumer
// Financial Protection Bureau" and "Department of Energy" matches "Energy Department".
func AgencyMatches(resolutionAgency string, documentAgencies []string) bool {
	stop := agencyStopWords()
	want := map[string]bool{}
	for _, w := range significant(resolutionAgency, stop) {
		want[w] = true
	}
	for _, a := range documentAgencies {
		for _, w := range significant(a, stop) {
			if want[w] {
				return true
			}
		}
	}
	return false
}

// IsWithdrawal reports whether a document withdraws something, by its title or action
// ("Interpretive Rules, Policy Statements, and Advisory Opinions; Withdrawal").
func IsWithdrawal(title, action string) bool {
	return strings.Contains(strings.ToLower(title+" "+action), "withdraw")
}

// IsAmendment reports whether a document's action says it only corrects, delays or extends
// another, so the title fallback never takes it for the rule itself.
func IsAmendment(action string) bool {
	a := strings.ToLower(action)
	for _, s := range []string{
		"correction", "correcting", "delay of effective date", "technical amendment", "extension of comment period",
	} {
		if strings.Contains(a, s) {
			return true
		}
	}
	return false
}
