package ai

import (
	"cmp"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Excerpts of long sections (#481): a section's current text over lawSectionMaxTokens is cut to
// the subsections the bill cites, each in full or capped, in text order, with excerptGap where
// text is left out. 42 U.S.C. 1395w-4 is 255,000 characters, and a bill amending its (c) and (t)
// needs both in the prompt, not one window from (c).

// excerptGap marks text left out of an excerpt.
const excerptGap = "[…]"

// subsectionLetters is how many single-letter subsection labels there are, (a) to (z), before
// (aa).
const subsectionLetters = 26

// subsectionSpan is one top-level subsection of a section's text: its label, "(t)", and its
// bytes, from its first line up to the next subsection's.
type subsectionSpan struct {
	label      string
	start, end int
}

// subsectionHeading is a line that starts like a subsection: its label, the label's place in the
// sequence (a)…(z), (aa)… (subsectionOrdinal), and the line's offset.
type subsectionHeading struct {
	label   string
	ordinal int
	offset  int
}

// sectionExcerpt is the excerpt of a long section's current text.
type sectionExcerpt struct {
	// text is the excerpt, a line per line of the section, with excerptGap lines for what's
	// left out.
	text string
	// labels are the cited subsections shown, in text order; missing are those not found.
	labels, missing []string
	// notes say how each subsection cut to fit was cut.
	notes []string
	// shown counts the section's characters in text.
	shown int
}

// citedExcerpt cuts a section's text to the subsections the bill cites, within maxChars. It
// returns nil when no cited subsection is found in the text, so the caller falls back to a cut
// from the top.
func citedExcerpt(text string, paths []string, maxChars int) *sectionExcerpt {
	cited := citedSubsections(paths)
	var spans []subsectionSpan
	for _, sp := range topLevelSubsections(text) {
		if _, ok := cited[sp.label]; ok {
			spans = append(spans, sp)
			delete(cited, sp.label)
		}
	}
	if len(spans) == 0 {
		return nil
	}
	ex := &sectionExcerpt{missing: sortedLabels(cited)}

	// Room for a gap line before each subsection, one inside it (paragraphStart) and one at the
	// end, and each subsection's closing line break.
	gapLine := len(excerptGap) + 1
	budget := maxChars - (2*len(spans)+1)*gapLine - len(spans)
	lengths := make([]int, len(spans))
	for i, sp := range spans {
		lengths[i] = len(strings.TrimRight(text[sp.start:sp.end], "\n"))
	}
	caps := shareBudget(lengths, budget)

	var b strings.Builder
	end, gapDue := 0, false
	for i, sp := range spans {
		body := strings.TrimRight(text[sp.start:sp.end], "\n")
		cut := len(body) > caps[i]
		if cut {
			var note string
			body, note = cutSubsection(body, sp.label, citedParagraphs(paths, sp.label), caps[i])
			ex.notes = append(ex.notes, note)
		}
		if gapDue || sp.start > end {
			b.WriteString(excerptGap + "\n")
		}
		b.WriteString(body + "\n")
		ex.labels = append(ex.labels, sp.label)
		ex.shown += len(body)
		end, gapDue = sp.end, cut
	}
	if gapDue || end < len(text) {
		b.WriteString(excerptGap + "\n")
	}
	ex.text = b.String()
	return ex
}

// cutSubsection cuts one subsection's text to maxChars, starting at the earliest paragraph the
// bill cites when that's past its first line, which is kept. It returns the text and a note
// saying what it shows.
func cutSubsection(body, label string, paragraphs []string, maxChars int) (string, string) {
	total, from := len(body), ""
	if off, para := paragraphStart(body, paragraphs); off > 0 {
		first, _, _ := strings.Cut(body, "\n")
		body = first + "\n" + excerptGap + "\n" + body[off:]
		from = ", from paragraph " + para
	}
	kept, _ := truncateAt(renderedText{text: body}, maxChars)
	return kept, fmt.Sprintf("[Subsection %s: %d of its %d characters shown%s.]", label, len(kept), total, from)
}

// paragraphStart finds the earliest of the cited paragraphs, "(2)", in a subsection's text past
// its first line, and returns its offset and label, or 0 and "".
func paragraphStart(body string, paragraphs []string) (int, string) {
	if len(paragraphs) == 0 {
		return 0, ""
	}
	first, _, _ := strings.Cut(body, "\n")
	for offset := len(first) + 1; offset < len(body); {
		line, _, _ := strings.Cut(body[offset:], "\n")
		label := leadingLabel(line, isDigit)
		if slices.Contains(paragraphs, label) && startsLikeHeading(line[len(label):]) {
			if offset == len(first)+1 {
				return 0, "" // right after the subsection's first line: nothing to skip
			}
			return offset, label
		}
		offset += len(line) + 1
	}
	return 0, ""
}

// shareBudget splits budget among items of the given lengths, shortest first: each gets its
// length or an equal share of what's left, so short items are shown whole and long ones share
// the rest.
func shareBudget(lengths []int, budget int) []int {
	order := make([]int, len(lengths))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int { return cmp.Compare(lengths[a], lengths[b]) })
	caps := make([]int, len(lengths))
	for k, i := range order {
		caps[i] = min(lengths[i], max(budget, 0)/(len(order)-k))
		budget -= caps[i]
	}
	return caps
}

// topLevelSubsections splits a section's text into its subsections. The US Code text has each
// subsection on its own line ("(t) Temporary adjustment"), but a clause's label can be a letter
// too ("(i) In general"), so the subsections are taken in sequence: after (c), the first (d), or
// when a label is skipped, the first line with the lowest label after (c). A clause can still be
// taken for a subsection when its label is the next one, such as a headed clause (i) in (h).
func topLevelSubsections(text string) []subsectionSpan {
	headings := subsectionHeadings(text)
	var spans []subsectionSpan
	for prev, from := 0, 0; from < len(headings); {
		i := nextSubsection(headings[from:], prev)
		if i < 0 {
			break
		}
		h := headings[from+i]
		if n := len(spans); n > 0 {
			spans[n-1].end = h.offset
		}
		spans = append(spans, subsectionSpan{label: h.label, start: h.offset, end: len(text)})
		prev, from = h.ordinal, from+i+1
	}
	return spans
}

// nextSubsection returns the index of the heading of the subsection after the one numbered
// prev: the first labeled prev+1, or else the first of the lowest label above prev, or -1.
func nextSubsection(headings []subsectionHeading, prev int) int {
	best := -1
	for i, h := range headings {
		if h.ordinal == prev+1 {
			return i
		}
		if h.ordinal > prev && (best < 0 || h.ordinal < headings[best].ordinal) {
			best = i
		}
	}
	return best
}

// subsectionHeadings lists the lines that start like a subsection: a label of letters with a
// place in the sequence, then a space and a capital letter, which tells "(i) Heading" from a
// clause such as "(i) in paragraph (2)".
func subsectionHeadings(text string) []subsectionHeading {
	var headings []subsectionHeading
	for offset := 0; offset < len(text); {
		line, _, _ := strings.Cut(text[offset:], "\n")
		label := leadingLabel(line, isLower)
		if n := subsectionOrdinal(label); n > 0 && startsLikeHeading(line[len(label):]) {
			headings = append(headings, subsectionHeading{label: label, ordinal: n, offset: offset})
		}
		offset += len(line) + 1
	}
	return headings
}

// subsectionOrdinal is a subsection label's place in the US Code's sequence: (a) is 1, (z) 26,
// (aa) 27, (zz) 52, (aaa) 53. A label of mixed letters, such as the clause (iv), has none: 0.
func subsectionOrdinal(label string) int {
	if len(label) < len("(a)") {
		return 0
	}
	letters := label[1 : len(label)-1]
	if strings.Trim(letters, letters[:1]) != "" {
		return 0
	}
	return (len(letters)-1)*subsectionLetters + int(letters[0]-'a') + 1
}

// leadingLabel returns the label a line starts with, "(" then one or more bytes for which in is
// true then ")", or "".
func leadingLabel(line string, in func(byte) bool) string {
	if line == "" || line[0] != '(' {
		return ""
	}
	i := 1
	for i < len(line) && in(line[i]) {
		i++
	}
	if i == 1 || i == len(line) || line[i] != ')' {
		return ""
	}
	return line[:i+1]
}

func isLower(c byte) bool { return c >= 'a' && c <= 'z' }

func isDigit(c byte) bool { return c >= '0' && c <= '9' }

// startsLikeHeading reports whether the rest of a line after its label starts like a
// subsection's or paragraph's: a space, then a capital letter.
func startsLikeHeading(rest string) bool {
	return len(rest) >= 2 && rest[0] == ' ' && rest[1] >= 'A' && rest[1] <= 'Z'
}

// citedSubsections returns the subsection labels the bill's paths cite, "(t)" from "(t)(2)", as
// a set.
func citedSubsections(paths []string) map[string]bool {
	cited := map[string]bool{}
	for _, path := range paths {
		if label := leadingLabel(strings.TrimSpace(path), isLower); label != "" {
			cited[label] = true
		}
	}
	return cited
}

// citedParagraphs returns the paragraphs the bill's paths cite in one subsection, "(2)" from
// "(c)(2)(B)", or none when a path cites the whole subsection.
func citedParagraphs(paths []string, label string) []string {
	pattern := regexp.MustCompile(`^\(\d+\)`)
	var paragraphs []string
	for _, path := range paths {
		rest, ok := strings.CutPrefix(strings.TrimSpace(path), label)
		if !ok {
			continue
		}
		para := pattern.FindString(rest)
		if para == "" {
			return nil
		}
		paragraphs = append(paragraphs, para)
	}
	return paragraphs
}

// sortedLabels returns a set of subsection labels in the US Code's order.
func sortedLabels(labels map[string]bool) []string {
	out := make([]string, 0, len(labels))
	for label := range labels {
		out = append(out, label)
	}
	slices.SortFunc(out, func(a, b string) int {
		return cmp.Or(cmp.Compare(subsectionOrdinal(a), subsectionOrdinal(b)), cmp.Compare(a, b))
	})
	return out
}
