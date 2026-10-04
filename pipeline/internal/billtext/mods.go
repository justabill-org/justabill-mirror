package billtext

import (
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/justabill-org/justabill/db/model"
)

// ParseMODSCitations returns the US Code sections a GovInfo bill package's MODS lists
// (<USCode title="42"><section number="1395w-4" detail="(t)"/></USCode>), as cites. MODS list
// citations the bill XML doesn't tag, but not what the bill does to them. Chapter-level entries
// and appendix titles are left out.
func ParseMODSCitations(data []byte) ([]LawRef, error) {
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	var m modsReader
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("parsing MODS: %w", err)
		}
		if err = m.token(tok); err != nil {
			return nil, err
		}
	}
	if !m.sawRoot {
		return nil, errors.New("parsing MODS: empty document")
	}
	return MergeLawRefs(m.refs), nil
}

// modsReader is ParseMODSCitations's state: the open <USCode>'s title and the citations so far.
type modsReader struct {
	title   string
	sawRoot bool
	refs    []LawRef
}

func (m *modsReader) token(tok xml.Token) error {
	switch t := tok.(type) {
	case xml.StartElement:
		if !m.sawRoot && t.Name.Local != "mods" {
			return fmt.Errorf("parsing MODS: root element %q", t.Name.Local)
		}
		m.sawRoot = true
		switch t.Name.Local {
		case "USCode":
			m.title = attr(t, "title")
		case elemSection:
			if ref, ok := modsRef(m.title, t); ok {
				m.refs = append(m.refs, ref)
			}
		}
	case xml.EndElement:
		if t.Name.Local == "USCode" {
			m.title = ""
		}
	}
	return nil
}

func modsRef(title string, t xml.StartElement) (LawRef, bool) {
	if title == "" {
		return LawRef{}, false
	}
	number := strings.TrimSpace(attr(t, "number"))
	id := USCSectionID(title, number)
	if id == "" {
		return LawRef{}, false
	}
	detail := strings.TrimSpace(attr(t, "detail"))
	cite := strings.TrimSpace(title + " U.S.C. " + normalizeDash(number) + detail)
	if noteRe.MatchString(" " + detail) {
		id += USCNoteSuffix
		cite = title + " U.S.C. " + normalizeDash(number) + " note"
		detail = ""
	}
	return LawRef{
		SectionID:      id,
		Kind:           model.LawRefCites,
		CiteText:       cite,
		SubsectionPath: subsectionRe.FindString(detail),
	}, true
}

// SupplementLawRefs adds to refs, parsed from a bill's XML, the MODS citations of sections
// refs doesn't mention at all, as cites.
func SupplementLawRefs(refs, mods []LawRef) []LawRef {
	seen := make(map[string]bool, len(refs))
	for _, r := range refs {
		seen[r.SectionID] = true
	}
	out := slices.Clone(refs)
	for _, m := range mods {
		if !seen[m.SectionID] {
			out = append(out, m)
		}
	}
	return MergeLawRefs(out)
}

// MergeLawRefs merges references to the same section with the same kind, which share one
// bill_law_refs row. The merged row keeps the first bill section, and joins the distinct
// citations, subsection paths and instructions, each within its column's cap. Its subsection
// path is empty when any of them references the whole section. A section a version changes
// isn't also listed as cited. The result is sorted by section ID, then kind.
func MergeLawRefs(refs []LawRef) []LawRef {
	type key struct{ section, kind string }
	changed := map[string]bool{}
	for _, r := range refs {
		if r.Kind != model.LawRefCites {
			changed[r.SectionID] = true
		}
	}
	index := map[key]int{}
	wholeSection := map[key]bool{}
	var out []LawRef
	for _, r := range refs {
		if r.Kind == model.LawRefCites && changed[r.SectionID] {
			continue
		}
		k := key{r.SectionID, r.Kind}
		wholeSection[k] = wholeSection[k] || r.SubsectionPath == ""
		i, ok := index[k]
		if !ok {
			index[k] = len(out)
			out = append(out, r)
			continue
		}
		m := &out[i]
		m.CiteText = joinDistinct(m.CiteText, r.CiteText, "; ", maxCiteText)
		m.SubsectionPath = joinDistinct(m.SubsectionPath, r.SubsectionPath, ", ", maxSubsectionPath)
		m.Instruction = joinDistinct(m.Instruction, r.Instruction, "\n\n", MaxInstruction)
		if m.BillSection == "" {
			m.BillSection = r.BillSection
		}
	}
	for k, i := range index {
		if wholeSection[k] {
			out[i].SubsectionPath = ""
		}
	}
	slices.SortFunc(out, func(a, b LawRef) int {
		if c := strings.Compare(a.SectionID, b.SectionID); c != 0 {
			return c
		}
		return strings.Compare(a.Kind, b.Kind)
	})
	return out
}

// joinDistinct appends add to joined, a list separated by sep, unless it's empty, already there
// or would take joined past limit bytes.
func joinDistinct(joined, add, sep string, limit int) string {
	switch {
	case add == "":
		return joined
	case joined == "":
		return add
	case slices.Contains(strings.Split(joined, sep), add), len(joined)+len(sep)+len(add) > limit:
		return joined
	}
	return joined + sep + add
}
