package billtext

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"regexp"
	"slices"
	"strings"

	"github.com/justabill-org/justabill/db/model"
)

// Law references (docs/design/149-law-aware-assistant.md, "Reference parser"): which sections of
// law a bill text version amends, repeals, adds to or only cites, read from GPO's bill DTD XML.

const (
	// MaxInstruction caps LawRef.Instruction, in bytes.
	MaxInstruction = 4096
	// maxSectionID is bill_law_refs.section_id's length (STRING(200)).
	maxSectionID = 200
	// maxSubsectionPath is bill_law_refs.subsection_path's length (STRING(200)).
	maxSubsectionPath = 200
	// maxCiteText caps the joined citations of one merged reference.
	maxCiteText = 1024
	// maxClause caps the text kept of one <text> element for classifying its citations.
	maxClause = 64 << 10

	// markerXref stands in a clause for a tagged US Code citation, and markerQuote for quoted
	// text, so that neither is read as the clause's own words.
	markerXref  = ""
	markerQuote = ""

	elemSection      = "section"
	elemEnum         = "enum"
	elemText         = "text"
	elemContinuation = "continuation-text"
	elemQuote        = "quote"
	elemQuotedBlock  = "quoted-block"
	elemXref         = "external-xref"

	uscPrefix = "/us/usc/t"
	// USCNoteSuffix ends the section ID of a statutory note, an uncodified provision the US Code
	// prints under a section ("10 U.S.C. 4271 note"). It joins no loaded section.
	USCNoteSuffix = model.USCNoteSuffix
	ircTitle      = "26" // the Internal Revenue Code of 1986 is title 26
)

var (
	errNotBillXML = errors.New("not bill XML")

	// verbRe finds the amending verb of a sentence: "is amended", "are each amended",
	// "is further amended", "is hereby repealed".
	verbRe = regexp.MustCompile(`\b(?:is|are)(?:\s+(?:each|further|hereby|also))*\s+(amended|repealed)\b`)
	// newSectionRe tells an amendment that adds a whole section from one that changes one.
	newSectionRe = regexp.MustCompile(`\bby\s+(?:adding|inserting)\b.*?\bthe\s+following\s+new\s+sections?\b`)
	// listVerbRe matches a text that ends by introducing a list of amendments or repeals:
	// "is amended—", "are repealed:", "is amended as follows:".
	listVerbRe = regexp.MustCompile(
		`\b(?:is|are)(?:\s+(?:each|further|hereby|also))*\s+(amended|repealed)(?:\s+as\s+follows)?\s*[—–-]*\s*:?\s*$`)
	// sentenceEndRe ends a sentence. Citations, whose periods ("U.S.C.") would end it early, are
	// markers by then.
	sentenceEndRe = regexp.MustCompile(`\.(?:\s+[A-Z“"(]|\s*$)`)

	// titleCiteRe is a citation of a positive-law title by its own section number, which bills
	// rarely tag: "section 8103(a) of title 5, United States Code".
	titleCiteRe = regexp.MustCompile(`\b[Ss]ections?\s+(\d+[A-Za-z0-9]*(?:[-–]\d+[A-Za-z0-9]*)*)` +
		`((?:\([A-Za-z0-9]+\))*)\s+of\s+title\s+(\d+),?\s+United\s+States\s+Code\b`)
	// titleCtxRe names a positive-law title: "title 5, United States Code".
	titleCtxRe = regexp.MustCompile(`\btitle\s+(\d+),?\s+United\s+States\s+Code\b`)
	// ircCiteRe is a citation of the Internal Revenue Code, whose section numbers are title 26's.
	ircCiteRe = regexp.MustCompile(`\b[Ss]ections?\s+(\d+[A-Za-z0-9]*(?:[-–]\d+[A-Za-z0-9]*)*)` +
		`((?:\([A-Za-z0-9]+\))*)\s+of\s+the\s+Internal\s+Revenue\s+Code\s+of\s+1986\b`)
	// actCiteRe is a citation of an Act by its own section number: "Section 4(d) of the
	// Statutory Pay-As-You-Go Act of 2010". Unless a tagged US Code citation follows it, it's
	// recorded as a "nonusc:" reference.
	actCiteRe = regexp.MustCompile(`\b[Ss]ections?\s+(\d+[A-Za-z0-9]*(?:[-–]\d+[A-Za-z0-9]*)*)` +
		`((?:\([A-Za-z0-9]+\))*)\s+of\s+(the\s+(?:[A-Z0-9][\w'’.&-]*,?\s+(?:(?:of|and|for|the|to|on|in|a|an)\s+)*)+` +
		`Act(?:\s+of\s+\d{4}|\s+for\s+[Ff]iscal\s+[Yy]ears?\s+\d{4}(?:\s+(?:and|through)\s+\d{4})?|,\s+\d{4})?)\b`)
	// pairedRe is the start of the text after an Act's citation that carries its US Code
	// parenthetical: "… Social Security Act (42 U.S.C. 1395w–4(t))".
	pairedRe = regexp.MustCompile(`^\s*,?\s*\(\s*` + markerXref)
	// noteRe follows a section number cited as a note: "10 U.S.C. 4271 note".
	noteRe = regexp.MustCompile(`^\s+note\b`)
	// strikeRe ends the text before a citation that an amendment strikes, redesignates or
	// repeals: "by striking section 3 (50 U.S.C. 3802)".
	strikeRe = regexp.MustCompile(
		`\bby\s+(?:striking|amending|redesignating|repealing)\s+(?:sections?\s+[\w.-]+\s*\(\s*)?$`)
	// subsectionRe is a subsection path: "(c)(2)(B)(iv)(V)".
	subsectionRe = regexp.MustCompile(`^(?:\([A-Za-z0-9]+\))+`)
	// newSectionEnumRe is the number of a section a bill inserts into a title: "226." or "§ 5545c.".
	newSectionEnumRe = regexp.MustCompile(`^(?:§\s*)?(\d+[A-Za-z0-9]*(?:[-–]\d+[A-Za-z0-9]*)*)\.?$`)
	// uscTitleRe is a US Code title number as the law tables key it; appendix titles ("5a")
	// aren't loaded.
	uscTitleRe = regexp.MustCompile(`^\d+$`)
	// uscSectionRe is a US Code section number: "1395w-4", "5545b", "300gg-11".
	uscSectionRe = regexp.MustCompile(`^[0-9A-Za-z]+(?:-[0-9A-Za-z]+)*$`)
)

// LawRef is one reference from a bill text version to a section of law, a row of bill_law_refs.
// SectionID is a US Code section ID ("/us/usc/t42/s1395w-4") or [model.NonUSCSectionPrefix]
// plus the citation; Kind is one of the model.LawRef* kinds. Instruction, the amendatory text,
// is set only for a reference that changes the law.
type LawRef struct {
	SectionID      string
	Kind           string
	CiteText       string
	SubsectionPath string
	Instruction    string
	BillSection    string
}

// USCSectionID returns the ID of US Code section in title, as the law tables key it, or "" when
// either isn't a US Code number. An en dash in the section becomes a hyphen.
func USCSectionID(title, section string) string {
	title = strings.TrimSpace(title)
	section = normalizeDash(strings.TrimSpace(section))
	if !uscTitleRe.MatchString(title) || !uscSectionRe.MatchString(section) {
		return ""
	}
	id := uscPrefix + strings.TrimLeft(title, "0") + "/s" + section
	if len(id) > maxSectionID {
		return ""
	}
	return id
}

// ParseLawRefs reads a bill or resolution's DTD XML in one streaming pass and returns its
// references to law, merged so there's one per section and kind (see [MergeLawRefs]).
//   - A tagged citation (external-xref, legal-doc "usc") gives the section; its display text
//     gives the subsection path.
//   - Untagged "section N of title T, United States Code" and "section N of the Internal Revenue
//     Code of 1986" citations are read as US Code sections.
//   - Untagged "Section N of the X Act" citations with no US Code parenthetical become "nonusc:"
//     references.
//
// A reference's kind comes from the rest of its sentence: "is amended" amends, "is repealed"
// repeals, "is amended by adding/inserting … the following new section" adds. A reference in an
// item of a list that a text introduces with "is amended—" or "are repealed:" takes that verb.
// Anything else, and any citation inside quoted text, only cites.
func ParseLawRefs(data []byte) ([]LawRef, error) {
	if len(bytes.TrimSpace(data)) == 0 {
		return nil, errors.New("empty input")
	}
	p := &refParser{}
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	dec.Entity = xml.HTMLEntity
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parsing bill XML: %w", err)
		}
		p.token(tok)
	}
	if !p.sawRoot || p.notBill {
		return nil, errNotBillXML
	}
	return MergeLawRefs(p.refs), nil
}

// refParser is ParseLawRefs's state.
type refParser struct {
	frames     []*refFrame
	clauses    []*clause
	quoteDepth int
	skipDepth  int
	sawRoot    bool
	notBill    bool

	billSection string
	xref        *xrefHit
	enum        *textBuf // the enum of a section a bill inserts, while it's read
	refs        []LawRef
}

// refFrame is an open element. A frame whose child <text> has started collects the text from
// there to its end as the instruction for the references in that text.
type refFrame struct {
	name       string
	collecting bool
	instr      textBuf
	pending    []LawRef
	// listKind is set when the frame's text introduces a list of amendments ("is amended—").
	listKind string
	// newSection waits for the number of the section that the frame's text inserts into a
	// positive-law title, from the next quoted-block.
	newSection *newSection
}

// newSection is a section a bill adds to a positive-law title, whose number is in the
// quoted-block that follows the instruction.
type newSection struct {
	title    string
	number   string
	inBlock  bool
	awaiting bool
}

// clause is an open <text> or <continuation-text>. Its text keeps a marker for each tagged
// citation and each quote, so that only the drafters' own words are matched.
type clause struct {
	text      textBuf
	quoted    bool
	owner     *refFrame
	hits      []hit
	titleCtx  string // the title of a chapter-level US Code citation ("chapter 1 of the Code")
	quoteBase int
}

// hit is a citation found in a clause, at text[start:end].
type hit struct {
	ref        LawRef
	start, end int
	quoted     bool
	paired     bool
}

// xrefHit is an open tagged US Code citation.
type xrefHit struct {
	sectionID string
	section   string
	display   textBuf
	depth     int
}

func (p *refParser) token(tok xml.Token) {
	switch t := tok.(type) {
	case xml.StartElement:
		p.start(t)
	case xml.EndElement:
		p.end(t)
	case xml.CharData:
		p.chars(string(t))
	}
}

func (p *refParser) start(t xml.StartElement) {
	name := t.Name.Local
	if !p.sawRoot {
		p.sawRoot = true
		if name != "bill" && name != "resolution" && name != "amendment-doc" {
			p.notBill = true
			p.skipDepth = 1 // the whole document
			return
		}
	}
	if p.skipDepth > 0 || name == "form" || name == "metadata" {
		p.skipDepth++
		return
	}
	if isBlock(name) {
		p.softSpace()
	}
	f := &refFrame{name: name, instr: textBuf{limit: MaxInstruction}}
	p.frames = append(p.frames, f)
	switch name {
	case elemSection:
		p.startSection(t)
	case elemQuote, elemQuotedBlock:
		p.startQuote(name)
	case elemText, elemContinuation:
		p.startClause()
	case elemXref:
		p.startXref(t)
	case elemEnum:
		if ns := p.awaitingSection(); ns != nil && ns.awaiting && p.parentName() == elemSection {
			p.enum = &textBuf{limit: maxSectionID}
		}
	}
}

func (p *refParser) startSection(t xml.StartElement) {
	if p.quoteDepth == 0 {
		p.billSection = attr(t, "id")
		return
	}
	if ns := p.awaitingSection(); ns != nil && ns.inBlock && ns.number == "" {
		ns.awaiting = true
	}
}

func (p *refParser) startQuote(name string) {
	if c := p.clause(); c != nil && p.quoteDepth == c.quoteBase {
		c.text.write(markerQuote)
	}
	p.writeInstr("“")
	p.quoteDepth++
	if name == elemQuotedBlock {
		if owner := p.parent(); owner != nil && owner.newSection != nil && owner.newSection.number == "" {
			owner.newSection.inBlock = true
		}
	}
}

func (p *refParser) startClause() {
	owner := p.parent()
	if owner == nil {
		return
	}
	owner.collecting = true
	p.clauses = append(p.clauses, &clause{
		text:      textBuf{limit: maxClause},
		quoted:    p.quoteDepth > 0,
		owner:     owner,
		quoteBase: p.quoteDepth,
	})
}

func (p *refParser) startXref(t xml.StartElement) {
	cite := attr(t, "parsable-cite")
	switch attr(t, "legal-doc") {
	case "usc":
		parts := strings.Split(cite, "/")
		if len(parts) < 3 || parts[0] != "usc" {
			return
		}
		id := USCSectionID(parts[1], parts[2])
		if id == "" {
			return
		}
		p.xref = &xrefHit{
			sectionID: id, section: normalizeDash(parts[2]),
			display: textBuf{limit: maxCiteText}, depth: len(p.frames),
		}
	case "usc-chapter":
		if parts := strings.Split(cite, "/"); len(parts) >= 2 && uscTitleRe.MatchString(parts[1]) {
			if c := p.clause(); c != nil {
				c.titleCtx = parts[1]
			}
		}
	}
}

func (p *refParser) chars(s string) {
	if p.skipDepth > 0 || len(p.frames) == 0 {
		return
	}
	p.writeInstr(s)
	if p.enum != nil {
		p.enum.write(s)
	}
	if p.xref != nil {
		p.xref.display.write(s)
		return
	}
	if c := p.clause(); c != nil && p.quoteDepth == c.quoteBase {
		c.text.write(s)
	}
}

func (p *refParser) end(t xml.EndElement) {
	if p.skipDepth > 0 {
		p.skipDepth--
		return
	}
	if len(p.frames) == 0 {
		return
	}
	switch t.Name.Local {
	case elemXref:
		p.endXref()
	case elemQuote, elemQuotedBlock:
		p.quoteDepth--
		p.writeInstr("”")
		if t.Name.Local == elemQuotedBlock {
			p.endQuotedBlock()
		}
	case elemText, elemContinuation:
		p.endClause()
	case elemEnum:
		p.endEnum()
	}
	if isBlock(t.Name.Local) {
		p.softSpace()
	}
	f := p.frames[len(p.frames)-1]
	p.frames = p.frames[:len(p.frames)-1]
	p.flush(f)
}

func (p *refParser) endXref() {
	x := p.xref
	if x == nil || x.depth != len(p.frames) {
		return
	}
	p.xref = nil
	display := x.display.String()
	sub, note := subsectionAfter(display, x.section)
	ref := LawRef{
		SectionID: x.sectionID, Kind: model.LawRefCites, CiteText: display,
		SubsectionPath: sub, BillSection: p.billSection,
	}
	if note {
		ref.SectionID += USCNoteSuffix
		ref.SubsectionPath = ""
	}
	c := p.clause()
	if c == nil {
		p.refs = append(p.refs, ref)
		return
	}
	start := c.text.Len()
	c.text.write(markerXref)
	c.hits = append(c.hits, hit{ref: ref, start: start, end: c.text.Len(), quoted: p.quoteDepth > c.quoteBase})
}

func (p *refParser) endClause() {
	if len(p.clauses) == 0 {
		return
	}
	c := p.clauses[len(p.clauses)-1]
	p.clauses = p.clauses[:len(p.clauses)-1]
	text := c.text.String()
	hits := c.hits
	if !c.quoted {
		hits = append(hits, untaggedHits(text, p.billSection)...)
	}
	adds := false
	for _, h := range hits {
		if h.paired {
			continue
		}
		h.ref.Kind = p.kindOf(c, text, h)
		adds = adds || h.ref.Kind == model.LawRefAdds
		c.owner.pending = append(c.owner.pending, h.ref)
	}
	if c.quoted {
		return
	}
	if m := listVerbRe.FindStringSubmatch(text); m != nil && c.owner.listKind == "" {
		c.owner.listKind = verbKind(m[1])
	}
	if title := newSectionTitle(c, text, adds); title != "" && c.owner.newSection == nil {
		c.owner.newSection = &newSection{title: title}
	}
}

// newSectionTitle returns the positive-law title a clause adds a new section to, taken from a
// chapter-level citation, "title T, United States Code" or the Internal Revenue Code, or "".
func newSectionTitle(c *clause, text string, adds bool) string {
	if !adds && !newSectionRe.MatchString(text) {
		return ""
	}
	if c.titleCtx != "" {
		return c.titleCtx
	}
	if m := titleCtxRe.FindStringSubmatch(text); m != nil {
		return m[1]
	}
	if strings.Contains(text, "Internal Revenue Code of 1986") {
		return ircTitle
	}
	return ""
}

func (p *refParser) endQuotedBlock() {
	owner := p.parent()
	if owner == nil || owner.newSection == nil {
		return
	}
	ns := owner.newSection
	ns.inBlock = false
	if ns.number == "" {
		owner.newSection = nil // the block wasn't a section; wait for no other
	}
}

func (p *refParser) endEnum() {
	if p.enum == nil {
		return
	}
	enum := strings.TrimSpace(p.enum.String())
	p.enum = nil
	ns := p.awaitingSection()
	if ns == nil {
		return
	}
	ns.awaiting = false
	if m := newSectionEnumRe.FindStringSubmatch(enum); m != nil {
		ns.number = normalizeDash(m[1])
	}
}

// flush hands a closing frame's references their instruction.
func (p *refParser) flush(f *refFrame) {
	instr := f.instr.String()
	for _, r := range f.pending {
		if r.Kind != model.LawRefCites {
			r.Instruction = instr
		}
		p.refs = append(p.refs, r)
	}
	if ns := f.newSection; ns != nil && ns.number != "" {
		if id := USCSectionID(ns.title, ns.number); id != "" {
			p.refs = append(p.refs, LawRef{
				SectionID: id, Kind: model.LawRefAdds, CiteText: fmt.Sprintf("%s U.S.C. %s", ns.title, ns.number),
				Instruction: instr, BillSection: p.billSection,
			})
		}
	}
}

// kindOf classifies one citation in a clause by the rest of its sentence, or by the list its
// clause is an item of.
func (p *refParser) kindOf(c *clause, text string, h hit) string {
	if c.quoted || h.quoted {
		return model.LawRefCites
	}
	tail := text[h.end:]
	if loc := sentenceEndRe.FindStringIndex(tail); loc != nil {
		tail = tail[:loc[0]]
	}
	if m := verbRe.FindStringSubmatchIndex(tail); m != nil {
		kind := verbKind(tail[m[2]:m[3]])
		if kind == model.LawRefAmends && newSectionRe.MatchString(tail[m[1]:]) {
			return model.LawRefAdds
		}
		return kind
	}
	if m := strikeRe.FindStringSubmatch(text[:h.start]); m != nil {
		if strings.Contains(m[0], "repealing") {
			return model.LawRefRepeals
		}
		return model.LawRefAmends
	}
	for _, f := range slices.Backward(p.frames) {
		if f.listKind != "" {
			return f.listKind
		}
	}
	return model.LawRefCites
}

func verbKind(verb string) string {
	if verb == "repealed" {
		return model.LawRefRepeals
	}
	return model.LawRefAmends
}

// untaggedHits finds the citations in a clause's text that carry no external-xref.
func untaggedHits(text, billSection string) []hit {
	var hits []hit
	for _, m := range titleCiteRe.FindAllStringSubmatchIndex(text, -1) {
		if id := USCSectionID(text[m[6]:m[7]], text[m[2]:m[3]]); id != "" {
			hits = append(hits, untaggedHit(text, m, id, billSection))
		}
	}
	for _, m := range ircCiteRe.FindAllStringSubmatchIndex(text, -1) {
		if id := USCSectionID(ircTitle, text[m[2]:m[3]]); id != "" {
			hits = append(hits, untaggedHit(text, m, id, billSection))
		}
	}
	for _, m := range actCiteRe.FindAllStringSubmatchIndex(text, -1) {
		id := model.NonUSCSectionPrefix + "Section " + normalizeDash(text[m[2]:m[3]]) + " of " + text[m[6]:m[7]]
		if len(id) > maxSectionID {
			continue
		}
		h := untaggedHit(text, m, id, billSection)
		h.paired = pairedRe.MatchString(text[m[1]:])
		hits = append(hits, h)
	}
	return hits
}

func untaggedHit(text string, m []int, id, billSection string) hit {
	return hit{
		ref: LawRef{
			SectionID: id, Kind: model.LawRefCites, CiteText: text[m[0]:m[1]],
			SubsectionPath: text[m[4]:m[5]], BillSection: billSection,
		},
		start: m[0],
		end:   m[1],
	}
}

// subsectionAfter returns the subsection path that follows section in a citation's display
// text, "(t)" in "42 U.S.C. 1395w–4(t)", and whether the citation is of the section's note.
func subsectionAfter(display, section string) (string, bool) {
	d := normalizeDash(display)
	for i := 0; ; {
		j := strings.Index(d[i:], section)
		if j < 0 {
			return "", false
		}
		i += j + len(section)
		if i < len(d) && isSectionChar(d[i]) {
			continue // a longer section number that starts the same way
		}
		sub := subsectionRe.FindString(d[i:])
		return sub, noteRe.MatchString(d[i+len(sub):])
	}
}

func isSectionChar(b byte) bool {
	return b == '-' || b >= '0' && b <= '9' || b >= 'a' && b <= 'z' || b >= 'A' && b <= 'Z'
}

func normalizeDash(s string) string {
	return strings.NewReplacer("–", "-", "—", "-", "‑", "-").Replace(s)
}

func (p *refParser) clause() *clause {
	if len(p.clauses) == 0 {
		return nil
	}
	return p.clauses[len(p.clauses)-1]
}

// parent returns the parent of the innermost open element, or nil.
func (p *refParser) parent() *refFrame {
	if len(p.frames) < 2 { //nolint:mnd // the innermost frame and its parent
		return nil
	}
	return p.frames[len(p.frames)-2]
}

func (p *refParser) parentName() string {
	if f := p.parent(); f != nil {
		return f.name
	}
	return ""
}

// awaitingSection returns the new section whose quoted-block is open, from the nearest frame
// that waits for one.
func (p *refParser) awaitingSection() *newSection {
	for _, f := range slices.Backward(p.frames) {
		if ns := f.newSection; ns != nil && ns.inBlock {
			return ns
		}
	}
	return nil
}

// softSpace separates the text of two block elements in the instructions: "(1) in the header".
func (p *refParser) softSpace() {
	for _, f := range p.frames {
		if f.collecting {
			f.instr.softSpace()
		}
	}
}

func (p *refParser) writeInstr(s string) {
	for _, f := range p.frames {
		if f.collecting {
			f.instr.write(s)
		}
	}
}

// isBlock reports whether a bill DTD element's text is set apart from its neighbors'.
func isBlock(name string) bool {
	switch name {
	case "division", "title", "subtitle", "part", "subpart", "chapter", "subchapter", "section", "subsection",
		"paragraph", "subparagraph", "clause", "subclause", "item", "subitem", "enum", "header", "text",
		"continuation-text", "quoted-block", "toc-entry":
		return true
	}
	return false
}

func attr(t xml.StartElement, name string) string {
	for _, a := range t.Attr {
		if a.Name.Local == name {
			return a.Value
		}
	}
	return ""
}

// textBuf collects text with runs of whitespace collapsed to one space, up to limit bytes.
type textBuf struct {
	b     strings.Builder
	limit int
	space bool
	full  bool
}

func (t *textBuf) write(s string) {
	for _, r := range s {
		if t.full {
			return
		}
		if r == ' ' || r == '\n' || r == '\t' || r == '\r' {
			t.space = t.b.Len() > 0
			continue
		}
		n := len(string(r))
		if t.space {
			n++
		}
		if t.b.Len()+n > t.limit {
			t.full = true
			return
		}
		if t.space {
			t.b.WriteByte(' ')
			t.space = false
		}
		t.b.WriteRune(r)
	}
}

// softSpace makes the next write start with a space, unless the buffer is empty.
func (t *textBuf) softSpace() { t.space = t.b.Len() > 0 }

// Len is the number of bytes written.
func (t *textBuf) Len() int { return t.b.Len() }

func (t *textBuf) String() string { return t.b.String() }
