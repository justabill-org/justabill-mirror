package billtext

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// Bill text XML (GPO's bill DTD, as GovInfo and Congress.gov serve it): the body of a bill,
// resolution or engrossed amendment nests sections inside divisions, titles, subtitles, parts,
// subparts, chapters and subchapters to any depth. ParseXML keeps that nesting down to
// subsections and renders everything below a subsection (paragraphs, subparagraphs, clauses,
// quoted blocks, tables, the table of contents) as indented plain text, so no text is lost.

const (
	elemHeader        = "header"
	elemSubsection    = "subsection"
	elemAfterQuoted   = "after-quoted-block"
	elemDeletedPhrase = "deleted-phrase"
	elemLinebreak     = "linebreak"
	elemTerm          = "term"

	kindText     = "text"
	kindPreamble = "preamble"

	// headerDash follows a paragraph's header, as in print: "(1) In general.—The Secretary".
	headerDash = ".—"
	// cellSep separates a table row's cells.
	cellSep = " |"
	// indentUnit indents each level below a unit's own paragraphs.
	indentUnit = "  "
)

// node is an element of the parsed XML, or a run of character data when name is empty.
type node struct {
	name    string
	id      string
	deleted bool // changed="deleted": text a reported version proposes to strike
	text    string
	kids    []*node
}

// ParseXML extracts structured sections from bill, resolution or engrossed amendment XML.
func ParseXML(data []byte) ([]Section, error) {
	if len(data) == 0 {
		return nil, errors.New("empty input")
	}
	root, err := parseTree(data)
	if err != nil {
		return nil, err
	}
	switch root.name {
	case "bill", "resolution", "amendment-doc":
	default:
		return nil, fmt.Errorf(
			"parsing XML: unsupported root element <%s> (expected <bill>, <resolution> or <amendment-doc>)", root.name)
	}

	p := &xmlParser{ids: map[string]bool{}}
	top := &level{wrapLoose: true}
	for _, k := range root.kids {
		switch {
		case bodyElement(k.name):
			p.structure(k, top)
		case k.name == kindPreamble:
			top.add(p, p.unit(k, kindPreamble, ""))
		}
	}
	top.flush(p)
	if top.sections == nil {
		return []Section{}, nil
	}
	return top.sections, nil
}

// parseTree reads the whole document into a tree of nodes.
func parseTree(data []byte) (*node, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	dec.Entity = xml.HTMLEntity
	var b treeBuilder
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parsing XML: %w", err)
		}
		if err = b.add(tok); err != nil {
			return nil, err
		}
	}
	if b.root == nil {
		return nil, errors.New("parsing XML: no root element")
	}
	if len(b.stack) > 0 {
		return nil, fmt.Errorf("parsing XML: <%s> is never closed", b.stack[len(b.stack)-1].name)
	}
	return b.root, nil
}

// treeBuilder assembles nodes from XML tokens.
type treeBuilder struct {
	root  *node
	stack []*node
}

func (b *treeBuilder) add(tok xml.Token) error {
	switch t := tok.(type) {
	case xml.StartElement:
		n := &node{name: t.Name.Local}
		for _, a := range t.Attr {
			switch a.Name.Local {
			case "id":
				n.id = a.Value
			case "changed":
				n.deleted = a.Value == "deleted"
			}
		}
		if len(b.stack) == 0 {
			if b.root != nil {
				return errors.New("parsing XML: more than one root element")
			}
			b.root = n
		} else {
			b.append(n)
		}
		b.stack = append(b.stack, n)
	case xml.EndElement:
		if len(b.stack) > 0 {
			b.stack = b.stack[:len(b.stack)-1]
		}
	case xml.CharData:
		if len(b.stack) > 0 {
			b.append(&node{text: string(t)})
		}
	}
	return nil
}

func (b *treeBuilder) append(n *node) {
	parent := b.stack[len(b.stack)-1]
	parent.kids = append(parent.kids, n)
}

// bodyElement reports whether a child of the document root holds the text: a bill's
// legis-body, a resolution's resolution-body (resolving-body in older documents) or an
// amendment's engrossed-amendment-body.
func bodyElement(name string) bool {
	switch name {
	case "legis-body", "resolution-body", "resolving-body", "engrossed-amendment-body", "amendment-body":
		return true
	}
	return false
}

// IsContainer reports whether a [Section] of this Kind only groups others (a division, title,
// subtitle, part, chapter…): its Children are the units inside it, and its Content is only the
// loose text between them.
func IsContainer(kind string) bool {
	return containerElement(kind)
}

// containerElement reports whether an element groups sections: it becomes a [Section] whose
// Children are the units inside it.
func containerElement(name string) bool {
	switch name {
	case "division", "subdivision", "title", "subtitle", "part", "subpart", "chapter", "subchapter":
		return true
	}
	return false
}

// transparentElement reports whether an element only wraps units, which belong to its parent: an
// engrossed amendment's amendment and the amendment-block holding the inserted text.
func transparentElement(name string) bool {
	return name == "amendment" || name == "amendment-block"
}

// appropriationsElement reports whether an element is one of an appropriations Act's headed
// paragraphs ("Military Personnel, Army"), which sit among its sections.
func appropriationsElement(name string) bool {
	switch name {
	case "appropriations-major", "appropriations-intermediate", "appropriations-small":
		return true
	}
	return false
}

// skippedElement reports whether an element has no text a reader should see.
func skippedElement(name string) bool {
	switch name {
	case "metadata", "pagebreak", "graphic", "colspec", elemDeletedPhrase:
		return true
	}
	return false
}

// inlineElement reports whether an element's text continues the current line. Everything else
// that isn't a unit's enum or header starts a new line.
func inlineElement(name string) bool {
	switch name {
	case elemXref, "internal-xref", "short-title", "act-name", "italic", "bold", "bold-italic",
		"superscript", "subscript", "sup", "sub", "fraction", "footnote-ref", "proviso", "added-phrase",
		"header-in-text", "enum-in-header", "committee-name", "inline-comment", "target", "toc-enum",
		"editorial", "entity-ref":
		return true
	}
	return false
}

// textBlock reports whether an element is a run of a unit's text, which follows the unit's enum
// and header on their line.
func textBlock(name string) bool {
	switch name {
	case elemText, elemContinuation, "quoted-block-continuation-text":
		return true
	}
	return false
}

// blockElement reports whether an element starts its own line: not character data, a text
// run, an inline element or a table cell.
func blockElement(name string) bool {
	switch name {
	case "", elemQuote, elemTerm, elemAfterQuoted, elemLinebreak:
		return false
	}
	return !inlineElement(name) && !textBlock(name) && !skippedElement(name)
}

// hasEnum reports whether an element is a designated unit (paragraph, clause, quoted section…).
func hasEnum(n *node) bool {
	for _, k := range n.kids {
		if k.name == elemEnum {
			return true
		}
	}
	return false
}

// xmlParser builds the section tree, keeping section IDs unique.
type xmlParser struct {
	ids map[string]bool
	seq int
}

// uniqueID returns id, or an ID made from kind when the element has none, with a suffix if the
// document already used it.
func (p *xmlParser) uniqueID(id, kind string) string {
	if id == "" {
		p.seq++
		id = kind + "-" + strconv.Itoa(p.seq)
	}
	base := id
	for i := 2; p.ids[id]; i++ {
		id = base + "-" + strconv.Itoa(i)
	}
	p.ids[id] = true
	return id
}

// level collects the units of one body or container, and the text between them. At the top
// (wrapLoose), each run of loose text becomes its own "text" [Section] in document order; in a
// container it becomes the container's Content.
type level struct {
	sections  []Section
	w         textWriter
	wrapLoose bool
	looseID   string
}

func (lv *level) add(p *xmlParser, s Section) {
	lv.flush(p)
	lv.sections = append(lv.sections, s)
}

func (lv *level) flush(p *xmlParser) {
	if !lv.wrapLoose || lv.w.empty() {
		return
	}
	lv.sections = append(lv.sections, Section{
		ID: p.uniqueID(lv.looseID, kindText), Kind: kindText, Content: lv.w.String(),
	})
	lv.w = textWriter{}
	lv.looseID = ""
}

// structure walks the children of a body or container, adding the units it finds to lv.
func (p *xmlParser) structure(n *node, lv *level) {
	for _, k := range n.kids {
		switch {
		case k.deleted, k.name == elemEnum, k.name == elemHeader:
			// A container's enum and header are read by container.
		case k.name == "":
			lv.w.words(k.text)
		case transparentElement(k.name):
			p.structure(k, lv)
		case containerElement(k.name):
			lv.add(p, p.container(k))
		case k.name == elemSection:
			lv.add(p, p.unit(k, elemSection, elemSubsection))
		case appropriationsElement(k.name):
			lv.add(p, p.unit(k, k.name, ""))
		default:
			if lv.looseID == "" {
				lv.looseID = k.id
			}
			render(k, &lv.w, 0)
		}
	}
}

// container turns a division, title or other grouping into a [Section] whose Children are the
// units inside it.
func (p *xmlParser) container(n *node) Section {
	s := Section{ID: p.uniqueID(n.id, n.name), Kind: n.name}
	for _, k := range n.kids {
		switch k.name {
		case elemEnum:
			s.Enum = enumLabel(n.name, inlineText(k))
		case elemHeader:
			s.Header = inlineText(k)
		}
	}
	lv := &level{}
	p.structure(n, lv)
	s.Children = lv.sections
	s.Content = lv.w.String()
	return s
}

// unit turns a section, subsection or other headed unit into a [Section]. Children of kind
// childKind (a section's subsections) become its Children; everything else is rendered into
// Content.
func (p *xmlParser) unit(n *node, kind, childKind string) Section {
	s := Section{ID: p.uniqueID(n.id, kind), Kind: kind}
	var w textWriter
	for _, k := range n.kids {
		switch {
		case k.deleted:
		case k.name == elemEnum:
			s.Enum = enumLabel(kind, inlineText(k))
		case k.name == elemHeader:
			s.Header = inlineText(k)
		case childKind != "" && k.name == childKind:
			s.Children = append(s.Children, p.unit(k, childKind, ""))
		default:
			render(k, &w, 0)
		}
	}
	if kind == kindPreamble && s.Header == "" {
		s.Header = "Preamble"
	}
	s.Content = w.String()
	return s
}

// enumLabel is a unit's designation as printed: "Sec. 101." for a section enum of "101.",
// "Title I" for a title enum of "I", and a paragraph's "(1)" as it is.
func enumLabel(kind, enum string) string {
	if enum == "" {
		return ""
	}
	switch {
	case kind == elemSection:
		if strings.HasPrefix(strings.ToLower(enum), "sec") {
			return enum
		}
		return "Sec. " + enum
	case containerElement(kind):
		if strings.HasPrefix(strings.ToLower(enum), kind) {
			return enum
		}
		return strings.ToUpper(kind[:1]) + kind[1:] + " " + enum
	default:
		return enum
	}
}

// inlineText is an element's text on one line, with its whitespace collapsed.
func inlineText(n *node) string {
	var w textWriter
	for _, k := range n.kids {
		render(k, &w, 0)
	}
	return strings.Join(strings.Fields(w.String()), " ")
}

// render writes an element's text to w. A block element starts a new line at indent; a
// designated unit's enum, header and text share its first line, and the units inside it are
// indented one more level.
func render(n *node, w *textWriter, indent int) {
	switch {
	case n.name == "":
		w.words(n.text)
	case n.deleted || skippedElement(n.name):
	case n.name == elemQuote || n.name == elemTerm:
		// GPO prints a quote, and a defined term, between curly quotes.
		w.open("“")
		renderKids(n, w, indent)
		w.close("”")
	case n.name == elemAfterQuoted:
		// The closing quote isn't in the XML; GPO's stylesheet prints it: ”. or ”; and
		w.close("”" + inlineText(n))
	case n.name == elemLinebreak:
		w.newline(indent)
	case n.name == "row" || n.name == "tr":
		renderRow(n, w, indent)
	case inlineElement(n.name):
		renderKids(n, w, indent)
	case textBlock(n.name):
		if !w.afterLabel {
			w.newline(indent)
		}
		renderKids(n, w, indent)
	default:
		renderBlock(n, w, indent)
	}
}

func renderKids(n *node, w *textWriter, indent int) {
	for _, k := range n.kids {
		render(k, w, indent)
	}
}

// renderRow writes a table row on one line, its cells separated by " | ", empty ones included.
func renderRow(n *node, w *textWriter, indent int) {
	w.newline(indent)
	cells := 0
	for _, k := range n.kids {
		if k.name == "" {
			continue
		}
		if cells > 0 {
			w.close(cellSep)
			w.pendingSpace = true
		}
		cells++
		renderKids(k, w, indent)
	}
}

// renderBlock writes a block element on a new line: a unit's enum and header first, then its
// text, then what it contains.
func renderBlock(n *node, w *textWriter, indent int) {
	w.newline(indent)
	if n.name == elemQuotedBlock {
		w.quoted++
		defer func() { w.quoted-- }()
	}
	unit := hasEnum(n)
	for _, k := range n.kids {
		switch {
		case unit && k.name == elemEnum:
			w.words(inlineText(k))
			w.pendingSpace = true
			w.afterLabel = true
		case unit && k.name == elemHeader:
			w.words(inlineText(k))
			w.close(headerDash)
			w.afterLabel = true
		case unit && blockElement(k.name):
			render(k, w, indent+1)
		default:
			render(k, w, indent)
		}
	}
}

// textWriter builds plain text, collapsing whitespace as it goes: a space is written only
// between words on one line, and each line starts with its indent.
type textWriter struct {
	b            strings.Builder
	pendingSpace bool
	atLineStart  bool
	indent       int
	// afterLabel is set once a unit's enum or header is written, so its text follows on the
	// same line.
	afterLabel bool
	// quoted counts the quoted blocks being written. Each of their lines opens with a quote, as
	// in print.
	quoted int
}

func (w *textWriter) empty() bool { return w.b.Len() == 0 }

func (w *textWriter) String() string { return w.b.String() }

// newline ends the current line; the next word starts a line at indent.
func (w *textWriter) newline(indent int) {
	if w.b.Len() > 0 && !w.atLineStart {
		w.b.WriteByte('\n')
		w.atLineStart = true
	}
	w.indent = indent
	w.pendingSpace = false
	w.afterLabel = false
}

// startWord writes what goes before a word: the line's indent (and a quoted block's opening
// quote), or a pending space.
func (w *textWriter) startWord() {
	switch {
	case w.b.Len() == 0 || w.atLineStart:
		w.b.WriteString(strings.Repeat(indentUnit, w.indent))
		if w.quoted > 0 {
			w.b.WriteString("“")
		}
		w.atLineStart = false
	case w.pendingSpace:
		w.b.WriteByte(' ')
	}
	w.pendingSpace = false
}

// words writes a run of character data with its whitespace collapsed to single spaces.
func (w *textWriter) words(s string) {
	fields := strings.Fields(s)
	if len(fields) == 0 {
		if s != "" && w.b.Len() > 0 && !w.atLineStart {
			w.pendingSpace = true
		}
		return
	}
	lead := s[0] == ' ' || s[0] == '\n' || s[0] == '\t' || s[0] == '\r'
	if lead && w.b.Len() > 0 && !w.atLineStart {
		w.pendingSpace = true
	}
	for i, f := range fields {
		if i > 0 {
			w.pendingSpace = true
		}
		w.startWord()
		w.b.WriteString(f)
	}
	last := s[len(s)-1]
	w.pendingSpace = last == ' ' || last == '\n' || last == '\t' || last == '\r'
	w.afterLabel = false
}

// open writes punctuation that attaches to the next word, such as an opening quote.
func (w *textWriter) open(s string) {
	w.startWord()
	w.b.WriteString(s)
}

// close writes punctuation that attaches to the previous word, such as a closing quote.
func (w *textWriter) close(s string) {
	if s == "" {
		return
	}
	w.pendingSpace = false
	if w.b.Len() == 0 || w.atLineStart {
		w.startWord()
	}
	w.b.WriteString(s)
}
