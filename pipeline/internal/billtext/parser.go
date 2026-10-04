// Package billtext parses bill and resolution text, in GovInfo's XML or as plain text, into a
// tree of [Section]s that the diff and summary steps work from.
package billtext

import (
	"fmt"
	"regexp"
	"strings"
)

// Section represents a hierarchical section of a bill's text: a division, title or other
// grouping whose Children are the units inside it, a section whose Children are its subsections,
// or a subsection. Content is the unit's own text, rendered as plain lines (one per paragraph,
// subparagraph, clause and so on, indented by depth), without its Children's text.
type Section struct {
	ID string `json:"id"`
	// Kind is the XML element the unit came from ("division", "title", "section", "subsection",
	// …), or "text" for loose text between units and "preamble" for a resolution's whereas
	// clauses. It's empty in sections parsed from plain text and in rows stored before it existed.
	Kind string `json:"kind,omitempty"`
	// Enum is the unit's designation as printed: "Division A", "Title I", "Sec. 101.", "(a)".
	Enum     string    `json:"enum,omitempty"`
	Header   string    `json:"header"`
	Content  string    `json:"content"`
	Children []Section `json:"children,omitempty"`
}

var sectionRe = regexp.MustCompile(`(?m)^(?:SECTION|SEC\.)\s+\d+\.\s+(.+)`)

// ParsePlainText extracts sections from plain text bill format.
func ParsePlainText(text string) ([]Section, error) {
	if strings.TrimSpace(text) == "" {
		return []Section{}, nil
	}

	matches := sectionRe.FindAllStringIndex(text, -1)
	if len(matches) == 0 {
		return []Section{}, nil
	}

	var sections []Section
	for i, loc := range matches {
		headerLine := text[loc[0]:loc[1]]
		header := strings.TrimSpace(sectionRe.FindStringSubmatch(headerLine)[1])

		var body string
		contentStart := loc[1]
		if i+1 < len(matches) {
			body = text[contentStart:matches[i+1][0]]
		} else {
			body = text[contentStart:]
		}
		body = strings.TrimSpace(body)

		sections = append(sections, Section{
			ID:      fmt.Sprintf("sec%d", i+1),
			Header:  header,
			Content: body,
		})
	}

	return sections, nil
}
