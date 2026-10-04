package main

import (
	"regexp"
	"slices"
)

// term is a word the summary shouldn't use unless the bill does. In the source, case never
// matters: bill text is often in capitals.
type term struct {
	label    string
	summary  *regexp.Regexp
	inSource *regexp.Regexp
}

func newTerm(label, pattern string, caseSensitive bool) term {
	summary := `(?i)` + pattern
	if caseSensitive {
		summary = pattern
	}
	return term{label, regexp.MustCompile(summary), regexp.MustCompile(`(?i)` + pattern)}
}

// loadedTerms are words that judge a bill rather than describe it (design 68: "landmark",
// "common-sense", "radical", "job-killing", "controversial", and their kin). Case doesn't matter.
func loadedTerms() []term {
	labels := map[string]string{
		"landmark":      `landmark`,
		"historic":      `historic`,
		"sweeping":      `sweeping`,
		"common-sense":  `common[- ]?sense`,
		"radical":       `radical`,
		"extreme":       `extreme|extremist`,
		"job-killing":   `job[- ]killing`,
		"controversial": `controversial`,
		"draconian":     `draconian`,
		"reckless":      `reckless`,
		"disastrous":    `disastrous|devastating`,
		"much-needed":   `much[- ]needed`,
		"long-overdue":  `long[- ]overdue`,
		"so-called":     `so[- ]called`,
		"giveaway":      `giveaway|handout`,
		"woke":          `woke`,
	}
	terms := make([]term, 0, len(labels))
	for label, pattern := range labels {
		terms = append(terms, newTerm(label, `\b(?:`+pattern+`)\b`, false))
	}
	return terms
}

// partyNames name a party. They're case-sensitive, so "democratic process" doesn't count.
func partyNames() []term {
	return []term{
		newTerm("Republican", `\bRepublicans?\b`, true),
		newTerm("Democrat", `\bDemocrats?\b|\bDemocratic (?:Party|lawmakers|members|leaders)\b`, true),
		newTerm("GOP", `\bGOP\b`, true),
	}
}

// checker flags terms in a summary that the bill's own text (and title) doesn't use. A term the
// bill uses, like "Common-Sense" in a short title, is quoting, not the model's judgment.
type checker struct {
	loaded []term
	party  []term
}

func newChecker() checker {
	return checker{loaded: loadedTerms(), party: partyNames()}
}

// check returns the loaded terms and the party names found in summary and not in source.
func (c checker) check(summary, source string) ([]string, []string) {
	return notInSource(c.loaded, summary, source), notInSource(c.party, summary, source)
}

func notInSource(terms []term, summary, source string) []string {
	var found []string
	for _, t := range terms {
		if t.summary.MatchString(summary) && !t.inSource.MatchString(source) {
			found = append(found, t.label)
		}
	}
	slices.Sort(found)
	return found
}
