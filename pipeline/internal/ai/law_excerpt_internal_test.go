package ai

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

// longSection is a section of US Code text with the given subsections, each about bodyChars
// long, with paragraphs (1) to (3), a subparagraph and a headed clause (i), as 42 U.S.C. 1395w-4
// has. Subsection (h) gets no clause (i): a headed clause (i) inside (h) is the case
// topLevelSubsections can take for subsection (i).
func longSection(labels []string, bodyChars int) string {
	filler := strings.Repeat("Body text of the paragraph. ", bodyChars/(3*len("Body text of the paragraph. ")))
	var b strings.Builder
	for _, l := range labels {
		fmt.Fprintf(&b, "(%s) Heading %s\n", l, l)
		for p := 1; p <= 3; p++ {
			fmt.Fprintf(&b, "(%d) Paragraph %d of %s\n(A) A subparagraph.\n", p, p, l)
			if l != "h" {
				b.WriteString("(i) In general.— a clause.\n")
			}
			b.WriteString(filler + "\n")
		}
	}
	return b.String()
}

func letters(from, to byte) []string {
	var out []string
	for c := from; c <= to; c++ {
		out = append(out, string(c))
	}
	return out
}

// currentTextOf returns the current text part of a section's block in a prompt.
func currentTextOf(t *testing.T, prompt string) string {
	t.Helper()
	_, block, _ := strings.Cut(prompt, "Section: "+secPhysicians+"\n")
	_, rest, found := strings.Cut(block, "\nCurrent text")
	if !found {
		t.Fatalf("prompt has no current text:\n%.600s", prompt)
	}
	text, _, _ := strings.Cut(rest, "\n\nSection: ")
	return text
}

func TestLawPrompt_ExcerptsEachCitedSubsection(t *testing.T) {
	lc := testLawBill()
	text := longSection(letters('a', 't'), 6000) // ~120,000 characters
	lc.Sections[0].CurrentText = text
	lc.Sections[0].Subsections = []string{"(t)", "(c)(2)(B)(iv)(V)", "(e)"}

	prompt, asked, truncated := lawPrompt(lc, Config{}.withDefaults())
	if !truncated || len(asked) != 2 {
		t.Errorf("truncated %v, asked %v; want truncated and both sections asked", truncated, asked)
	}
	current := currentTextOf(t, prompt)
	if !strings.HasPrefix(current, ", excerpts of the cited subsections (c), (e), (t) only (") ||
		!strings.Contains(current, fmt.Sprintf("characters shown; the rest of the section is left out, and %s "+
			"marks each gap):\n%s\n(c) Heading c\n", excerptGap, excerptGap)) {
		t.Errorf("current text doesn't open with the excerpt note and subsection (c):\n%.400s", current)
	}
	positions := []int{
		strings.Index(current, "(c) Heading c"), strings.Index(current, "(e) Heading e"),
		strings.Index(current, "(t) Heading t\n(1) Paragraph 1 of t"),
	}
	if slices.Contains(positions, -1) || !slices.IsSorted(positions) {
		t.Errorf("subsections (c), (e), (t) at %v; want each, in text order", positions)
	}
	for _, absent := range []string{"(a) Heading a", "(d) Heading d", "(s) Heading s"} {
		if strings.Contains(current, absent) {
			t.Errorf("the excerpt has %q, which the bill doesn't cite", absent)
		}
	}
	// (c), (e) and (t) are ~6,000 characters each: all three fit whole, with a gap between each.
	if strings.Count(current, excerptGap+"\n(") != 3 || strings.Contains(current, "[Subsection ") {
		t.Errorf("want three gaps before three whole subsections:\n%s", current)
	}
	// (t) is the section's last subsection: nothing after it is left out.
	if !strings.Contains(current, "(3) Paragraph 3 of t\n") || strings.HasSuffix(current, excerptGap) {
		t.Errorf("the excerpt doesn't end with the whole of (t):\n%s", current[max(len(current)-300, 0):])
	}
}

func TestLawPrompt_ExcerptsShareTheSectionBudget(t *testing.T) {
	lc := testLawBill()
	// (c) is ~60,000 characters and (t) ~1,500: (t) is shown whole and (c) gets the rest.
	text := longSection(letters('a', 'e'), 60_000) + longSection([]string{"t"}, 1500)
	lc.Sections[0].CurrentText = text
	lc.Sections[0].Subsections = []string{"(c)(2)(B)(iv)(V)", "(t)", "(z)(1)"}

	prompt, _, _ := lawPrompt(lc, Config{}.withDefaults())
	current := currentTextOf(t, prompt)
	excerpt, notes, _ := strings.Cut(current, "\n[Subsection (c): ")
	if _, body, ok := strings.Cut(excerpt, "marks each gap):\n"); !ok || len(body) > lawSectionMaxTokens*charsPerToken {
		t.Errorf("excerpt is %d characters, over the section's %d", len(body), lawSectionMaxTokens*charsPerToken)
	}
	if !strings.Contains(excerpt, "(c) Heading c\n"+excerptGap+"\n(2) Paragraph 2 of c\n") {
		t.Errorf("subsection (c) isn't cut to start at the cited paragraph (2):\n%.600s", excerpt)
	}
	if strings.Contains(excerpt, "Paragraph 1 of c") {
		t.Error("the excerpt has paragraph (1) of (c), before the cited paragraph")
	}
	if !strings.Contains(excerpt, "(t) Heading t") || !strings.Contains(excerpt, "Paragraph 3 of t") {
		t.Error("subsection (t) isn't shown whole")
	}
	if !strings.Contains(notes, "characters shown, from paragraph (2).]\n") ||
		!strings.Contains(notes, "[Cited subsections not found in the current text: (z).]") {
		t.Errorf("notes don't say what was left out:\n%s", notes)
	}
}

func TestLawPrompt_ExcerptFallsBackToTheTop(t *testing.T) {
	lc := testLawBill()
	lc.Sections[0].CurrentText = longSection(letters('a', 'j'), 6000)
	lc.Sections[0].Subsections = []string{"(z)(2)", "paragraph (4)"}

	prompt, _, truncated := lawPrompt(lc, Config{}.withDefaults())
	current := currentTextOf(t, prompt)
	if !truncated || !strings.HasPrefix(current, ":\n(a) Heading a\n") ||
		!strings.Contains(current, "[Current text truncated: ") || strings.Contains(current, excerptGap) {
		t.Errorf("want the text cut from the top when no cited subsection is found:\n%.300s", current)
	}
}

func TestTopLevelSubsections(t *testing.T) {
	text := "(a) Alpha\n(1) In general\n(i) In general\n(i) the clause\n(b) Beta\n(iv) In general\n" +
		"(d) Delta, after a gap\n(e) Echo\n(z) Zulu\n(aa) Double"
	var got []string
	for _, sp := range topLevelSubsections(text) {
		line, _, _ := strings.Cut(text[sp.start:sp.end], "\n")
		got = append(got, sp.label+" "+line)
	}
	want := []string{"(a) (a) Alpha", "(b) (b) Beta", "(d) (d) Delta, after a gap", "(e) (e) Echo",
		"(z) (z) Zulu", "(aa) (aa) Double"}
	if !slices.Equal(got, want) {
		t.Errorf("topLevelSubsections = %q, want %q", got, want)
	}
	if spans := topLevelSubsections("no subsections here\n(1) A paragraph"); spans != nil {
		t.Errorf("topLevelSubsections without subsections = %v", spans)
	}
}

func TestSubsectionOrdinal(t *testing.T) {
	for label, want := range map[string]int{
		"(a)": 1, "(z)": 26, "(aa)": 27, "(bb)": 28, "(zz)": 52, "(aaa)": 53,
		"(iv)": 0, "(ab)": 0, "()": 0, "": 0,
	} {
		if got := subsectionOrdinal(label); got != want {
			t.Errorf("subsectionOrdinal(%q) = %d, want %d", label, got, want)
		}
	}
}

func TestShareBudget(t *testing.T) {
	tests := []struct {
		lengths []int
		budget  int
		want    []int
	}{
		{[]int{100, 200}, 1000, []int{100, 200}},
		{[]int{50_000, 1500, 8000}, 24_000, []int{14_500, 1500, 8000}},
		{[]int{50_000, 40_000}, 24_000, []int{12_000, 12_000}},
		{[]int{10}, -5, []int{0}},
	}
	for _, tt := range tests {
		if got := shareBudget(tt.lengths, tt.budget); !slices.Equal(got, tt.want) {
			t.Errorf("shareBudget(%v, %d) = %v, want %v", tt.lengths, tt.budget, got, tt.want)
		}
	}
}

func TestCitedParagraphs(t *testing.T) {
	paths := []string{"(c)(2)(B)", " (c)(5)", "(cc)(1)", "(t)"}
	if got := citedParagraphs(paths, "(c)"); !slices.Equal(got, []string{"(2)", "(5)"}) {
		t.Errorf("citedParagraphs (c) = %q", got)
	}
	if got := citedParagraphs(append(paths, "(c)"), "(c)"); got != nil {
		t.Errorf("citedParagraphs with the whole of (c) cited = %q, want none", got)
	}
	if got := citedParagraphs(paths, "(t)"); got != nil {
		t.Errorf("citedParagraphs (t) = %q, want none", got)
	}
}

func TestParagraphStart(t *testing.T) {
	body := "(c) Gamma\n(1) First\n(2) Second\n(3) Third"
	if off, label := paragraphStart(body, []string{"(3)", "(2)"}); label != "(2)" ||
		off != strings.Index(body, "(2) Second") {
		t.Errorf("paragraphStart = %d, %q; want the earliest cited, (2)", off, label)
	}
	if off, label := paragraphStart(body, []string{"(1)"}); off != 0 || label != "" {
		t.Errorf("paragraphStart for (1) right after the heading = %d, %q; want nothing to skip", off, label)
	}
	if off, _ := paragraphStart(body, []string{"(9)"}); off != 0 {
		t.Errorf("paragraphStart for a missing paragraph = %d", off)
	}
}
