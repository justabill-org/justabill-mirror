// Package htmltext renders short HTML documents, such as CRS bill summaries from Congress.gov,
// as plain text: paragraphs separated by blank lines, list items as lines starting with "• ",
// and no tags or entities (docs/design/197-crs-summaries.md, "Rendering the HTML").
//
// The input is untrusted and often invalid HTML (unclosed tags, stray end tags), so the walk is
// lenient, and markup it can't walk at all has its tags stripped instead.
package htmltext

import (
	"encoding/xml"
	"errors"
	"html"
	"io"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Bullet starts each rendered list item.
const Bullet = "• "

const (
	lineBreak      = 1
	paragraphBreak = 2
	// indent is the extra indentation of each nested list level.
	indent = "  "
)

// Render returns html as plain text. It never fails: text that isn't markup comes back with its
// whitespace tidied, and markup the lenient decoder rejects comes back with its tags removed.
func Render(html string) string {
	text, err := renderMarkup(html)
	if err != nil {
		return stripTags(html)
	}
	return text
}

// renderer holds the state of one walk.
type renderer struct {
	b strings.Builder
	// breakWant is how many line breaks the next word needs: 1 for a new line, 2 for a blank
	// line. Breaks are only written before a word, so none lead or trail and runs collapse.
	breakWant int
	space     bool
	bullet    bool
	skip      int
	lists     int
}

func renderMarkup(content string) (string, error) {
	dec := xml.NewDecoder(strings.NewReader(content))
	dec.Strict = false
	dec.AutoClose = xml.HTMLAutoClose
	dec.Entity = xml.HTMLEntity

	var r renderer
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return r.b.String(), nil
		}
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			r.start(strings.ToLower(t.Name.Local))
		case xml.EndElement:
			r.end(strings.ToLower(t.Name.Local))
		case xml.CharData:
			if r.skip == 0 {
				r.words(string(t))
			}
		}
	}
}

func (r *renderer) start(name string) {
	if r.skip > 0 || skipped(name) {
		r.skip++
		return
	}
	switch {
	case inline(name):
	case name == "br":
		r.want(lineBreak)
	case name == "li":
		r.want(lineBreak)
		r.bullet = true
	case name == "ul" || name == "ol":
		r.lists++
		if r.lists > 1 {
			r.want(lineBreak)
		} else {
			r.want(paragraphBreak)
		}
	default:
		r.want(r.blockBreak())
	}
}

// blockBreak is the break around a block element: a blank line, or inside a list (a paragraph in
// a list item) a new line.
func (r *renderer) blockBreak() int {
	if r.lists > 0 {
		return lineBreak
	}
	return paragraphBreak
}

func (r *renderer) end(name string) {
	if r.skip > 0 {
		r.skip--
		return
	}
	switch {
	case inline(name) || name == "br":
	case name == "li":
		r.want(lineBreak)
		r.bullet = false
	case name == "ul" || name == "ol":
		r.lists = max(r.lists-1, 0)
		if r.lists > 0 {
			r.want(lineBreak)
		} else {
			r.want(paragraphBreak)
		}
	default:
		r.want(r.blockBreak())
	}
}

func (r *renderer) want(n int) { r.breakWant = max(r.breakWant, n) }

// words writes a text chunk with its whitespace (non-breaking spaces included) collapsed.
func (r *renderer) words(s string) {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		if s != "" {
			r.space = true
		}
		return
	}
	if first, _ := utf8.DecodeRuneInString(s); unicode.IsSpace(first) {
		r.space = true
	}
	if r.b.Len() > 0 {
		switch {
		case r.breakWant > 0:
			r.b.WriteString(strings.Repeat("\n", r.breakWant))
		case r.space:
			r.b.WriteByte(' ')
		}
	}
	if r.breakWant > 0 && r.bullet {
		r.b.WriteString(strings.Repeat(indent, max(r.lists-1, 0)) + Bullet)
	}
	r.breakWant, r.space = 0, false
	r.b.WriteString(strings.Join(fields, " "))
	if last, _ := utf8.DecodeLastRuneInString(s); unicode.IsSpace(last) {
		r.space = true
	}
}

// inline reports whether an element's text continues the current line.
func inline(name string) bool {
	switch name {
	case "a", "abbr", "b", "cite", "code", "em", "font", "i", "q", "s", "small", "span", "strong", "sub", "sup",
		"u":
		return true
	}
	return false
}

// skipped reports whether an element's content isn't text.
func skipped(name string) bool {
	switch name {
	case "head", "script", "style", "template":
		return true
	}
	return false
}

// tagPattern matches a comment, or a tag and captures whether it ends an element and its name. A
// "<" not followed by a name ("a < b") is text.
func tagPattern() *regexp.Regexp {
	return regexp.MustCompile(`(?s)<!--.*?-->|<(/?)([A-Za-z][A-Za-z0-9]*)[^>]*>`)
}

// stripTags is the fallback for markup the decoder rejects: tags are removed but still break
// lines as [Render] would, entities are decoded, and whitespace is tidied.
func stripTags(content string) string {
	var r renderer
	rest := content
	for _, m := range tagPattern().FindAllStringSubmatchIndex(content, -1) {
		if r.skip == 0 {
			r.words(html.UnescapeString(content[len(content)-len(rest) : m[0]]))
		}
		rest = content[m[1]:]
		if m[4] < 0 {
			continue // a comment
		}
		name := strings.ToLower(content[m[4]:m[5]])
		if m[3] > m[2] {
			r.end(name)
		} else {
			r.start(name)
		}
	}
	if r.skip == 0 {
		r.words(html.UnescapeString(rest))
	}
	return r.b.String()
}
