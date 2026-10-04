package ai

import (
	"encoding/xml"
	"errors"
	"io"
	"regexp"
	"strings"
	"unicode/utf8"
)

// charsPerToken is the rough size of a token in English legal text, used to estimate prompt size.
const charsPerToken = 4

// maxBoundaryLabel caps the section heading quoted in a truncation note.
const maxBoundaryLabel = 120

// renderedText is bill text as plain text, with the offsets where a section (or a larger unit such
// as a title or division) starts.
type renderedText struct {
	text       string
	boundaries []int
}

// sectionElement reports whether a GovInfo bill XML element starts a unit the text can be cut
// before.
func sectionElement(name string) bool {
	switch name {
	case "section", "title", "subtitle", "division", "subdivision", "part", "subpart", "chapter", "subchapter":
		return true
	}
	return false
}

// inlineElement reports whether an element's text continues the current line. Everything else
// (paragraphs, subsections, text blocks, HTML block tags) starts a new line.
func inlineElement(name string) bool {
	switch name {
	case "enum", "header", "quote", "term", "external-xref", "internal-xref", "short-title", "act-name",
		"italic", "bold", "bold-italic", "sup", "sub", "fraction", "footnote-ref", "pagebreak",
		"a", "b", "i", "em", "strong", "span", "u", "small", "font", "abbr", "cite", "code":
		return true
	}
	return false
}

// skippedElement reports whether an element's content is left out of the prompt: metadata that
// repeats the form, and HTML that isn't text.
func skippedElement(name string) bool {
	switch name {
	case "metadata", "head", "script", "style":
		return true
	}
	return false
}

// sectionLinePattern finds section and title headings in plain text (Congress.gov "Formatted Text").
func sectionLinePattern() *regexp.Regexp {
	return regexp.MustCompile(`(?m)^[ \t]*(SEC(TION)?\.?[ \t]+\d|TITLE[ \t]+[IVXLCDM]+\b|DIVISION[ \t]+[A-Z]\b)`)
}

// renderText turns stored bill text (GovInfo XML, Congress.gov HTML or plain text) into plain text
// for the prompt: no tags, one line per element, and section headers on their own lines.
func renderText(content string) renderedText {
	trimmed := strings.TrimSpace(strings.TrimPrefix(content, "\uFEFF"))
	if !strings.HasPrefix(trimmed, "<") {
		return withLineBoundaries(normalizePlain(trimmed))
	}
	r, err := renderMarkup(trimmed)
	if err != nil {
		// Unparseable markup: drop anything that looks like a tag and keep the rest.
		return withLineBoundaries(normalizePlain(tagPattern().ReplaceAllString(trimmed, "\n")))
	}
	if len(r.boundaries) == 0 {
		return withLineBoundaries(r.text)
	}
	return r
}

func tagPattern() *regexp.Regexp { return regexp.MustCompile(`<[^>]*>`) }

// withLineBoundaries finds section boundaries in plain text by its headings.
func withLineBoundaries(text string) renderedText {
	var boundaries []int
	for _, m := range sectionLinePattern().FindAllStringIndex(text, -1) {
		if m[0] > 0 {
			boundaries = append(boundaries, m[0])
		}
	}
	return renderedText{text: text, boundaries: boundaries}
}

// normalizePlain trims each line, collapses runs of spaces and keeps at most one blank line.
func normalizePlain(s string) string {
	lines := strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	blank := false
	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			if !blank && len(out) > 0 {
				out = append(out, "")
			}
			blank = true
			continue
		}
		blank = false
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// textWriter builds the rendered text, collapsing whitespace as it goes so that recorded offsets
// stay valid: a space is written only before the next word, and at most one blank line is kept.
type textWriter struct {
	b            strings.Builder
	pendingSpace bool
	// newlines is the number of line breaks at the end of b.
	newlines int
}

// lineBreak ends the current line. Called twice in a row it leaves one blank line; more calls do
// nothing, and nothing is written before the first word.
func (w *textWriter) lineBreak(maxNewlines int) {
	w.pendingSpace = false
	if w.b.Len() > 0 && w.newlines < maxNewlines {
		w.b.WriteByte('\n')
		w.newlines++
	}
}

func (w *textWriter) newline() { w.lineBreak(1) }

func (w *textWriter) space() {
	if w.b.Len() > 0 && w.newlines == 0 {
		w.pendingSpace = true
	}
}

// words writes a text chunk with its whitespace collapsed to single spaces.
func (w *textWriter) words(s string) {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		if s != "" {
			w.space()
		}
		return
	}
	if first, _ := utf8.DecodeRuneInString(s); isSpace(first) {
		w.space()
	}
	if w.pendingSpace {
		w.b.WriteByte(' ')
	}
	w.b.WriteString(strings.Join(fields, " "))
	w.pendingSpace, w.newlines = false, 0
	if last, _ := utf8.DecodeLastRuneInString(s); isSpace(last) {
		w.space()
	}
}

// preformatted writes text from an HTML <pre> element, keeping its line breaks.
func (w *textWriter) preformatted(s string) {
	const blankLine = 2
	for i, line := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if i > 0 {
			w.lineBreak(blankLine)
		}
		w.words(line)
	}
}

func isSpace(r rune) bool { return r == ' ' || r == '\t' || r == '\n' || r == '\r' }

// markupRenderer holds the state of one renderMarkup walk.
type markupRenderer struct {
	w          textWriter
	boundaries []int
	skip, pre  int
}

// renderMarkup walks XML or HTML tokens leniently and writes their text.
func renderMarkup(content string) (renderedText, error) {
	dec := xml.NewDecoder(strings.NewReader(content))
	dec.Strict = false
	dec.AutoClose = xml.HTMLAutoClose
	dec.Entity = xml.HTMLEntity

	var r markupRenderer
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return renderedText{}, err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			r.start(strings.ToLower(t.Name.Local))
		case xml.EndElement:
			r.end(strings.ToLower(t.Name.Local))
		case xml.CharData:
			r.chars(string(t))
		}
	}
	return renderedText{text: strings.TrimRight(r.w.b.String(), " \n"), boundaries: r.boundaries}, nil
}

func (r *markupRenderer) start(name string) {
	switch {
	case r.skip > 0 || skippedElement(name):
		r.skip++
	case name == "pre":
		r.pre++
		r.w.newline()
	case inlineElement(name):
		if name == "enum" || name == "header" {
			r.w.space()
		}
	default:
		r.w.newline()
		if sectionElement(name) && r.w.b.Len() > 0 {
			r.boundaries = append(r.boundaries, r.w.b.Len())
		}
	}
}

func (r *markupRenderer) end(name string) {
	switch {
	case r.skip > 0:
		r.skip--
	case name == "pre":
		r.pre = max(r.pre-1, 0)
		r.w.newline()
	case !inlineElement(name):
		r.w.newline()
	}
}

func (r *markupRenderer) chars(s string) {
	switch {
	case r.skip > 0:
	case r.pre > 0:
		r.w.preformatted(s)
	default:
		r.w.words(s)
	}
}

// truncation describes where long text was cut.
type truncation struct {
	keptChars, totalChars int
	// nextHeading is the first line of the first section left out.
	nextHeading string
}

// truncateAt cuts r to at most maxChars bytes, at the last section boundary that fits. With no
// boundary in range it cuts at the last line break, and failing that at a rune boundary.
func truncateAt(r renderedText, maxChars int) (string, *truncation) {
	if len(r.text) <= maxChars {
		return r.text, nil
	}
	cut := 0
	for _, b := range r.boundaries {
		if b <= maxChars && b > cut {
			cut = b
		}
	}
	if cut == 0 {
		cut = strings.LastIndexByte(r.text[:maxChars], '\n')
	}
	if cut <= 0 {
		cut = maxChars
		for cut > 0 && !utf8.RuneStart(r.text[cut]) {
			cut--
		}
	}
	next := strings.TrimSpace(r.text[cut:])
	if i := strings.IndexByte(next, '\n'); i >= 0 {
		next = next[:i]
	}
	if len(next) > maxBoundaryLabel {
		next = strings.ToValidUTF8(next[:maxBoundaryLabel], "") + "…"
	}
	kept := strings.TrimRight(r.text[:cut], " \n")
	return kept, &truncation{keptChars: len(kept), totalChars: len(r.text), nextHeading: next}
}
