package model

import (
	"regexp"
	"strings"
	"time"
)

// Ways a chamber passed a bill ([PassageEntry].Method): a recorded roll call, or an unrecorded
// passage by voice vote or unanimous consent, as the pipeline stores them (vote IDs
// "{chamber}-{congress}-voice-…" and "…-uc-…").
const (
	PassageRoll  = "roll"
	PassageVoice = "voice"
	PassageUC    = "uc"
)

// Kinds of law a bill becomes ([Enactment].LawType).
const (
	LawTypePublic  = "public"
	LawTypePrivate = "private"
)

// leadMinWords is the word count a CRS paragraph must exceed to read as a sentence rather than
// the bill's short title.
const leadMinWords = 6

// BillCardFacts is what a /vote card shows about a bill beyond its list row (#704, for the card
// of #662): what the bill does, how each chamber passed it, when it became law and how many
// sections of law it changes. The API serves it as is.
type BillCardFacts struct {
	// CRS is the latest CRS summary's lead, nil without one.
	CRS *CardCRS `json:"crs"`
	// Passage holds each chamber's latest final vote, oldest first; empty when none.
	Passage []PassageEntry `json:"passage"`
	// Enacted says when the bill became law, nil when it hasn't.
	Enacted *Enactment `json:"enacted"`
	// LawChangeCount is how many sections of law the bill's latest stored text changes: the
	// entries GET /bills/{id}/law-changes serves.
	LawChangeCount int `json:"law_change_count"`
}

// CardCRS is the part of the latest CRS summary a card shows.
type CardCRS struct {
	VersionCode string    `json:"version_code"`
	ActionDate  time.Time `json:"action_date"`
	ActionDesc  string    `json:"action_desc"`
	// Lead is the summary's paragraph the card leads with (see [CRSLead]).
	Lead string `json:"lead"`
}

// PassageEntry is how one chamber passed the bill. A roll call carries its number and tallies;
// a voice vote or unanimous consent has none.
type PassageEntry struct {
	Chamber    string    `json:"chamber"`
	Method     string    `json:"method"`
	Date       time.Time `json:"date"`
	Question   *string   `json:"question,omitempty"`
	Result     *string   `json:"result,omitempty"`
	RollNumber *int      `json:"roll_number,omitempty"`
	Yeas       *int      `json:"yeas,omitempty"`
	Nays       *int      `json:"nays,omitempty"`
	Present    *int      `json:"present,omitempty"`
	NotVoting  *int      `json:"not_voting,omitempty"`
}

// Enactment is when a bill became law and its law type and number ("public", "119-95"), from
// the bill's laws (Congress.gov's own record, #736) or, for a row synced before those were
// stored, its "Became Public Law No: 119-95." action. Without either both are nil.
type Enactment struct {
	Date      time.Time `json:"date"`
	LawType   *string   `json:"law_type,omitempty"`
	LawNumber *string   `json:"law_number,omitempty"`
}

// Empty reports whether f holds no fact at all.
func (f BillCardFacts) Empty() bool {
	return f.CRS == nil && len(f.Passage) == 0 && f.Enacted == nil && f.LawChangeCount == 0
}

// blankLine separates the paragraphs of a CRS summary's plain text.
var blankLine = regexp.MustCompile(`\n\s*\n`)

// lawActionPattern matches the action that names a bill's law: "Became Public Law No: 119-95.".
var lawActionPattern = regexp.MustCompile(`Became (Public|Private) Law No:\s*(\d+-\d+)`)

// LawActionPattern is the RE2 pattern of the action [ParseLawAction] reads, for queries that
// find it.
func LawActionPattern() string { return lawActionPattern.String() }

// ParseLawAction reads the law type ("public" or "private") and number ("119-95") from an
// action's text, such as "Became Public Law No: 119-95.". ok is false when the text names none.
func ParseLawAction(text string) (string, string, bool) {
	m := lawActionPattern.FindStringSubmatch(text)
	if m == nil {
		return "", "", false
	}
	return strings.ToLower(m[1]), m[2], true
}

// CRSParagraphs splits a CRS summary's plain text into its paragraphs (separated by blank
// lines), trimmed, without empty ones. It is web/src/lib/crs.ts's crsParagraphs.
func CRSParagraphs(text string) []string {
	var out []string
	for _, p := range blankLine.Split(text, -1) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// CRSLead is the paragraph of a CRS summary a card leads with: the first that ends in ".", ":"
// or ")" and has more than six words, since CRS often opens with the bill's short title, else
// the first paragraph. It is "" for an empty text.
func CRSLead(text string) string {
	paragraphs := CRSParagraphs(text)
	if len(paragraphs) == 0 {
		return ""
	}
	for _, p := range paragraphs {
		if strings.ContainsAny(p[len(p)-1:], ".:)") && len(strings.Split(p, " ")) > leadMinWords {
			return p
		}
	}
	return paragraphs[0]
}
