// Package diff compares two versions of a bill's text section by section and counts what
// was added, removed and changed.
//
// GovInfo generates new XML ids for every version of a bill, so sections are matched by where
// they sit instead (#449): the enums and headers of the divisions, titles and other groupings
// above them, plus their own enum and header. A section keeps its match when it's renumbered,
// renamed or moved, as long as one of those still pins it down (see [passes]).
package diff

import (
	"strings"

	"github.com/justabill-org/justabill/pipeline/internal/billtext"
)

// The SectionDiff types. Unchanged sections aren't reported.
const (
	TypeAdded    = "added"
	TypeRemoved  = "removed"
	TypeModified = "modified"
)

// kindText is the Kind of loose text between units (billtext's "text" sections).
const kindText = "text"

// Stats summarizes changes between two bill text versions.
type Stats struct {
	SectionsAdded    int `json:"sections_added"`
	SectionsRemoved  int `json:"sections_removed"`
	SectionsModified int `json:"sections_modified"`
	WordsAdded       int `json:"words_added"`
	WordsRemoved     int `json:"words_removed"`
}

// Empty reports whether no section was added, removed or modified.
func (s Stats) Empty() bool {
	return s.SectionsAdded == 0 && s.SectionsRemoved == 0 && s.SectionsModified == 0
}

// SectionDiff represents changes within a single section.
type SectionDiff struct {
	// SectionID is the section's id in the newer version, or in the older one when it was removed.
	SectionID string `json:"section_id"`
	// Header names the section with the groupings above it: "Title I, Sec. 101. Short title".
	Header  string `json:"header"`
	Type    string `json:"type"` // added, removed or modified
	OldText string `json:"old_text,omitempty"`
	NewText string `json:"new_text,omitempty"`
}

// unit is a section the diff compares: a section, headed paragraph, preamble or run of loose
// text, with the text of everything inside it.
type unit struct {
	id     string
	kind   string
	path   string // the groupings above it, normalized: "title i/subtitle a"
	enum   string // normalized
	header string // normalized
	label  string
	text   string
	norm   string // text with whitespace collapsed
}

// ComputeDiff generates a structured diff between two bill text versions. It lists the added,
// removed and modified sections in the newer version's order, each removed section after the
// sections that preceded it. A section whose header and text are the same in both versions is
// unchanged even if it was renumbered or moved.
func ComputeDiff(oldSections, newSections []billtext.Section) ([]SectionDiff, Stats, error) {
	olds := flatten(oldSections)
	news := flatten(newSections)
	match := matchUnits(olds, news) // new index -> old index

	var diffs []SectionDiff
	var stats Stats
	matchedOld := make([]bool, len(olds))
	for _, oi := range match {
		if oi >= 0 {
			matchedOld[oi] = true
		}
	}
	nextOld := 0
	removedBefore := func(end int) {
		for ; nextOld < end; nextOld++ {
			if !matchedOld[nextOld] {
				diffs = append(diffs, removed(olds[nextOld], &stats))
			}
		}
	}
	for ni, oi := range match {
		n := news[ni]
		if oi < 0 {
			stats.SectionsAdded++
			stats.WordsAdded += wordCount(n.text)
			diffs = append(diffs, SectionDiff{SectionID: n.id, Header: n.label, Type: TypeAdded, NewText: n.text})
			continue
		}
		removedBefore(oi)
		o := olds[oi]
		if o.header == n.header && o.norm == n.norm {
			continue
		}
		diffs = append(diffs, modified(o, n, &stats))
	}
	removedBefore(len(olds))
	return diffs, stats, nil
}

func removed(o unit, st *Stats) SectionDiff {
	st.SectionsRemoved++
	st.WordsRemoved += wordCount(o.text)
	return SectionDiff{SectionID: o.id, Header: o.label, Type: TypeRemoved, OldText: o.text}
}

func modified(o, n unit, st *Stats) SectionDiff {
	st.SectionsModified++
	oldWords := wordCount(o.text)
	newWords := wordCount(n.text)
	switch {
	case newWords > oldWords:
		st.WordsAdded += newWords - oldWords
	case oldWords > newWords:
		st.WordsRemoved += oldWords - newWords
	default:
		if oldWords > 0 {
			st.WordsAdded++
			st.WordsRemoved++
		}
	}
	return SectionDiff{SectionID: n.id, Header: n.label, Type: TypeModified, OldText: o.text, NewText: n.text}
}

// pass is one round of matching: units still unmatched on both sides pair up when their keys
// are equal, in document order. A unit whose key is empty sits the round out. A unique pass pairs
// a key only when exactly one unmatched unit on each side has it.
type pass struct {
	key    func(u unit) string
	unique bool
}

// passes go from the strictest match to the loosest, so a renumbered section (same place, same
// header) isn't paired with whatever section took its number.
func passes() []pass {
	return []pass{
		// Same place, enum and header.
		{key: func(u unit) string { return join(u.path, u.kind, u.enum, u.header) }},
		// Same place and header: renumbered.
		{key: func(u unit) string { return ifSet(u.header, join(u.path, u.kind, u.header)) }},
		// Same place and enum: renamed.
		{key: func(u unit) string { return ifSet(u.enum, join(u.path, u.kind, u.enum)) }},
		// Same enum and header under renumbered or renamed groupings.
		{key: func(u unit) string { return ifSet(u.enum+u.header, join(u.kind, u.enum, u.header)) }},
		// The only section anywhere with this header: moved.
		{key: func(u unit) string { return ifSet(u.header, join(u.kind, u.header)) }, unique: true},
	}
}

// matchUnits pairs old and new units, returning for each new unit the index of its old unit,
// or -1 when it was added.
func matchUnits(olds, news []unit) []int {
	m := matcher{match: make([]int, len(news)), oldTaken: make([]bool, len(olds))}
	for i := range m.match {
		m.match[i] = -1
	}
	for _, p := range passes() {
		m.run(p, olds, news)
	}
	return m.match
}

// matcher holds the pairs made so far.
type matcher struct {
	match    []int // new index -> old index, or -1
	oldTaken []bool
}

// run pairs the units still unmatched whose keys are equal under p.
func (m *matcher) run(p pass, olds, news []unit) {
	queues := map[string][]int{}
	for oi, o := range olds {
		if k := p.key(o); k != "" && !m.oldTaken[oi] {
			queues[k] = append(queues[k], oi)
		}
	}
	newCount := map[string]int{}
	for ni, n := range news {
		if m.match[ni] < 0 {
			newCount[p.key(n)]++
		}
	}
	for ni, n := range news {
		k := p.key(n)
		if m.match[ni] >= 0 || k == "" || len(queues[k]) == 0 {
			continue
		}
		if p.unique && (len(queues[k]) != 1 || newCount[k] != 1) {
			continue
		}
		oi := queues[k][0]
		queues[k] = queues[k][1:]
		m.match[ni] = oi
		m.oldTaken[oi] = true
	}
}

// flatten lists the units of a parsed text in document order. Divisions, titles and other
// groupings aren't units themselves: they become the path of the units inside them, and any loose
// text directly inside one becomes a "text" unit.
func flatten(sections []billtext.Section) []unit {
	var out []unit
	var walk func(ss []billtext.Section, path, label []string)
	walk = func(ss []billtext.Section, path, label []string) {
		for _, s := range ss {
			if !billtext.IsContainer(s.Kind) {
				out = append(out, newUnit(s, path, label, s.Kind, sectionText(s)))
				continue
			}
			name := s.Enum
			if name == "" {
				name = s.Header
			}
			p := append(append([]string(nil), path...), normalize(name))
			l := append(append([]string(nil), label...), strings.TrimSpace(joinWords(s.Enum, s.Header)))
			if strings.TrimSpace(s.Content) != "" {
				out = append(out, newUnit(billtext.Section{ID: s.ID}, p, l, kindText, s.Content))
			}
			walk(s.Children, p, l)
		}
	}
	walk(sections, nil, nil)
	return out
}

func newUnit(s billtext.Section, path, label []string, kind, text string) unit {
	own := joinWords(s.Enum, s.Header)
	if own == "" && kind == kindText {
		own = "Text"
		if len(label) > 0 {
			own = "text"
		}
	}
	full := strings.Join(nonEmpty(append(append([]string(nil), label...), own)), ", ")
	return unit{
		id:     s.ID,
		kind:   kind,
		path:   strings.Join(path, "/"),
		enum:   normalize(s.Enum),
		header: normalize(s.Header),
		label:  full,
		text:   text,
		norm:   strings.Join(strings.Fields(text), " "),
	}
}

// sectionText is a section's own text followed by the units inside it (a section's
// subsections), each introduced by its enum and header.
func sectionText(s billtext.Section) string {
	parts := []string{strings.TrimSpace(s.Content)}
	for _, c := range s.Children {
		parts = append(parts, strings.TrimSpace(joinWords(c.Enum, c.Header)), sectionText(c))
	}
	return strings.Join(nonEmpty(parts), "\n")
}

// normalize makes an enum or header comparable across versions: lower case, whitespace
// collapsed, without trailing punctuation ("Sec. 101." and "SEC. 101" are the same).
func normalize(s string) string {
	s = strings.ToLower(strings.Join(strings.Fields(s), " "))
	return strings.TrimRight(s, ".:;,—- ")
}

func joinWords(a, b string) string {
	return strings.TrimSpace(strings.TrimSpace(a) + " " + strings.TrimSpace(b))
}

// join builds a match key; \x00 can't occur in parsed text.
func join(parts ...string) string {
	return strings.Join(parts, "\x00")
}

func ifSet(cond, key string) string {
	if cond == "" {
		return ""
	}
	return key
}

func nonEmpty(ss []string) []string {
	out := ss[:0:0]
	for _, s := range ss {
		if s != "" {
			out = append(out, s)
		}
	}
	return out
}

func wordCount(s string) int {
	return len(strings.Fields(s))
}
