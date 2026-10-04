package uscode

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

// sectionIDPattern matches the identifier of a title's own section, "/us/usc/t42/s1395w–4", and
// not an appendix's ("/us/usc/t5a/s…") or a subsection's ("/us/usc/t42/s1395w–4/a").
var sectionIDPattern = regexp.MustCompile(`^/us/usc/t(\d+)/s([^/]+)$`)

const (
	positiveLawRole = "is-positive-law"
	elemSection     = "section"
	elemHeading     = "heading"
	elemNum         = "num"
	elemMeta        = "meta"
	elemDocNumber   = "docNumber"
	elemProperty    = "property"
)

// Title is what a title file's metadata says about the title, and how many sections it had.
type Title struct {
	// Number is the title number, or 0 when docNumber isn't a number (an appendix, "5a").
	Number int
	// PositiveLaw is the title's is-positive-law property.
	PositiveLaw bool
	// Sections counts the sections passed to the callback.
	Sections int
}

// ParseTitle reads one USLM title file from r with [xml.Decoder.Token], so only the section in
// progress is ever in memory, and calls fn with each of the title's sections in document order.
// Sections quoted in notes, and a section nested in another's text, aren't the title's own and
// aren't passed to fn. ParseTitle stops at the first error fn returns and returns it.
func ParseTitle(r io.Reader, fn func(Section) error) (Title, error) {
	p := titleParser{fn: fn}
	d := xml.NewDecoder(r)
	for {
		tok, err := d.Token()
		if errors.Is(err, io.EOF) {
			return p.title, nil
		}
		if err != nil {
			return p.title, fmt.Errorf("uslm: %w", err)
		}
		if err = p.token(tok); err != nil {
			return p.title, err
		}
	}
}

// skipped are the elements whose text never belongs to a section: notes (editorial and
// statutory notes, amendment history), source credits, tables of contents and footnotes.
func skipped(local string) bool {
	switch local {
	case "notes", "note", "sourceCredit", "toc", "footnote":
		return true
	}
	return false
}

// lineBreaks are the elements that start and end a line of a section's text: its subdivisions,
// paragraphs and table rows.
func lineBreak(local string) bool {
	switch local {
	case "subsection", "paragraph", "subparagraph", "clause", "subclause", "item", "subitem",
		"subsubitem", "level", "p", "continuation", "tr":
		return true
	}
	return false
}

type titleParser struct {
	fn    func(Section) error
	title Title

	depth  int // depth of the element being read; the root is 1
	skipAt int // depth of the element whose content is ignored, or 0

	inMeta    bool
	metaField string // docNumber or property while reading one inside meta
	metaText  strings.Builder

	sec *sectionBuilder
}

// sectionBuilder collects one section while it is read.
type sectionBuilder struct {
	Section

	depth      int    // depth of the <section> element
	field      string // num or heading while reading the section's own, else ""
	fieldDepth int
	heading    textBuilder
	text       textBuilder
}

func (p *titleParser) token(tok xml.Token) error {
	switch t := tok.(type) {
	case xml.StartElement:
		p.depth++
		if p.skipAt == 0 {
			p.start(t)
		}
	case xml.EndElement:
		defer func() { p.depth-- }()
		if p.skipAt != 0 {
			if p.depth == p.skipAt {
				p.skipAt = 0
			}
			return nil
		}
		return p.end(t)
	case xml.CharData:
		if p.skipAt == 0 {
			p.charData(t)
		}
	}
	return nil
}

func (p *titleParser) start(t xml.StartElement) {
	local := t.Name.Local
	if p.sec != nil {
		p.sectionStart(local)
		return
	}
	switch {
	case p.inMeta:
		if local == elemDocNumber || (local == elemProperty && attr(t, "role") == positiveLawRole) {
			p.metaField = local
			p.metaText.Reset()
		}
	case local == elemMeta:
		p.inMeta = true
	case skipped(local) || local == "quotedContent":
		p.skipAt = p.depth
	case local == elemSection:
		p.sectionOpen(t)
	}
}

// sectionOpen starts a section if t is one of this title's own, and skips it otherwise.
func (p *titleParser) sectionOpen(t xml.StartElement) {
	m := sectionIDPattern.FindStringSubmatch(attr(t, "identifier"))
	if m == nil || m[1] != strconv.Itoa(p.title.Number) || p.title.Number == 0 {
		p.skipAt = p.depth
		return
	}
	status := attr(t, "status")
	if status == "" {
		status = StatusCurrent
	}
	p.sec = &sectionBuilder{
		ID:          NormalizeID(m[0]),
		Title:       p.title.Number,
		Number:      NormalizeID(m[2]),
		Status:      status,
		PositiveLaw: p.title.PositiveLaw,
		depth:       p.depth,
	}
}

// wordBreak reports whether an element's text is a separate word from the text before it, as
// a subdivision's "(a)" is from its heading, even where the XML has no space between them.
func wordBreak(local string) bool {
	switch local {
	case elemNum, elemHeading, "content", "chapeau", "quotedContent", "td", "th":
		return true
	}
	return false
}

func (p *titleParser) sectionStart(local string) {
	s := p.sec
	switch {
	case skipped(local):
		p.skipAt = p.depth
	case p.depth == s.depth+1 && (local == elemNum || local == elemHeading):
		s.field, s.fieldDepth = local, p.depth
	case lineBreak(local):
		s.text.newline()
	case wordBreak(local):
		s.text.space = true
	}
}

func (p *titleParser) end(t xml.EndElement) error {
	local := t.Name.Local
	if s := p.sec; s != nil {
		switch {
		case p.depth == s.depth:
			return p.sectionClose()
		case s.field != "" && p.depth == s.fieldDepth:
			s.field = ""
		case local == elemHeading || lineBreak(local):
			// A subdivision's heading ends its line: "(a) In general" / "Effective for…".
			s.text.newline()
		case wordBreak(local):
			s.text.space = true
		}
		return nil
	}
	if p.inMeta {
		p.metaEnd(local)
	}
	return nil
}

func (p *titleParser) metaEnd(local string) {
	switch local {
	case elemMeta:
		p.inMeta = false
	case p.metaField:
		value := strings.TrimSpace(p.metaText.String())
		if local == elemDocNumber {
			p.title.Number, _ = strconv.Atoi(value) // an appendix ("5a") stays 0: no sections
		} else {
			p.title.PositiveLaw = value == "yes"
		}
		p.metaField = ""
	}
}

func (p *titleParser) sectionClose() error {
	s := p.sec
	p.sec = nil
	s.Heading = s.heading.String()
	s.Text = s.text.String()
	p.title.Sections++
	return p.fn(s.Section)
}

func (p *titleParser) charData(data xml.CharData) {
	s := p.sec
	switch {
	case s == nil:
		if p.metaField != "" {
			p.metaText.Write(data)
		}
	case s.field == elemHeading:
		s.heading.write(data)
	case s.field == elemNum:
		// The section's own "§ 1395w–4." is its Number already.
	default:
		s.text.write(data)
	}
}

func attr(t xml.StartElement, name string) string {
	for _, a := range t.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

// textBuilder turns character data into lines of text, collapsing each run of white space to
// one space and dropping empty lines.
type textBuilder struct {
	lines []string
	cur   strings.Builder
	space bool // a space is due before the next character
}

func (b *textBuilder) write(data []byte) {
	for _, r := range string(data) {
		if unicode.IsSpace(r) {
			b.space = true
			continue
		}
		if b.space && b.cur.Len() > 0 {
			b.cur.WriteByte(' ')
		}
		b.space = false
		b.cur.WriteRune(r)
	}
}

func (b *textBuilder) newline() {
	if b.cur.Len() > 0 {
		b.lines = append(b.lines, b.cur.String())
		b.cur.Reset()
	}
	b.space = false
}

func (b *textBuilder) String() string {
	b.newline()
	return strings.Join(b.lines, "\n")
}
