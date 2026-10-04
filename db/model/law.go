package model

import (
	"strconv"
	"strings"
	"time"
)

// Kinds of reference a bill text makes to a section of law (bill_law_refs.ref_kind and
// bill_law_changes.change_kind). Amends, repeals and adds change the law; cites only mentions it.
const (
	LawRefAmends  = "amends"
	LawRefRepeals = "repeals"
	LawRefAdds    = "adds"
	LawRefCites   = "cites"
)

// NonUSCSectionPrefix starts the section ID of a law a bill names without a US Code citation,
// such as "nonusc:Section 5 of the Social Security Act". Such IDs have no USCSection.
const NonUSCSectionPrefix = "nonusc:"

// uscSectionIDPrefix starts every US Code section ID ("/us/usc/t42/s1395w-4").
const uscSectionIDPrefix = "/us/usc/t"

// USCNoteSuffix ends the ID of a statutory note, a provision the US Code prints under a section
// without making it part of the section's text: "/us/usc/t10/s4271/note" is "10 U.S.C. 4271 note".
// No usc_sections row has a note's ID, so GET /law never serves one.
const USCNoteSuffix = "/note"

// USCReleasePoint is a US Code release point from the Office of the Law Revision Counsel,
// named for the last public law it includes ("119-111").
type USCReleasePoint struct {
	ReleasePoint  string     `json:"release_point"`
	PublishedDate *time.Time `json:"published_date,omitempty"`
	SourceURL     string     `json:"source_url"`
	LoadedAt      *time.Time `json:"loaded_at,omitempty"`
	SectionCount  *int       `json:"section_count,omitempty"`
}

// USCSection is one section of the US Code at a release point. SectionID is the USLM
// identifier with a hyphen for its en dash ("/us/usc/t42/s1395w-4").
type USCSection struct {
	SectionID     string    `json:"section_id"`
	TitleNumber   int       `json:"title_number"`
	SectionNumber string    `json:"section_number"`
	Heading       *string   `json:"heading,omitempty"`
	Text          string    `json:"text"`
	Status        string    `json:"status"`
	PositiveLaw   bool      `json:"positive_law"`
	ReleasePoint  string    `json:"release_point"`
	UpdatedAt     time.Time `json:"updated_at"`
}

// BillLawChange is the plain-language explanation of how a bill's latest text changes one
// section of law, with its provenance. Heading is nil for a section that isn't loaded.
// Explanation is empty for a section the explainer listed but didn't explain (past its caps).
type BillLawChange struct {
	SectionID     string    `json:"section_id"`
	Heading       *string   `json:"heading,omitempty"`
	ChangeKind    string    `json:"change_kind"`
	Explanation   string    `json:"explanation"`
	ReleasePoint  string    `json:"release_point"`
	ModelUsed     string    `json:"model_used"`
	PromptVersion string    `json:"prompt_version"`
	GeneratedAt   time.Time `json:"generated_at"`
}

// LawRef is a CHANGES_LAW edge in civic_graph: a bill text version's reference to a loaded US
// Code section. References to sections that aren't loaded, and "nonusc:" ones, aren't edges.
type LawRef struct {
	VersionID      string  `json:"version_id"`
	SectionID      string  `json:"section_id"`
	RefKind        string  `json:"ref_kind"`
	SubsectionPath *string `json:"subsection_path,omitempty"`
	TitleNumber    int     `json:"title_number"`
	SectionNumber  string  `json:"section_number"`
	Heading        *string `json:"heading,omitempty"`
}

// SectionBill is a bill whose text amends, repeals or adds to a section of law. RefKinds lists
// the kinds across its text versions, sorted.
type SectionBill struct {
	BillID        string   `json:"bill_id"`
	Congress      int      `json:"congress"`
	BillType      string   `json:"bill_type"`
	Number        int      `json:"number"`
	Title         string   `json:"title"`
	CurrentStatus *string  `json:"current_status,omitempty"`
	RefKinds      []string `json:"ref_kinds"`
}

// BillLawChanges is what a bill's latest stored text changes in current law: one entry per
// section it amends, repeals or adds, with the explanation of that text when there is one
// (docs/design/149-law-aware-assistant.md). VersionID and VersionCode are nil, and Changes
// empty, for a bill with no stored text.
type BillLawChanges struct {
	BillID      string  `json:"bill_id"`
	VersionID   *string `json:"version_id"`
	VersionCode *string `json:"version_code"`
	// Explained is the provenance of the explanations, or nil when none were written for this
	// text. All of a bill's explanations come from one model call.
	Explained *LawChangeProvenance `json:"explained"`
	Changes   []LawChangeEntry     `json:"changes"`
}

// LawChangeProvenance says which model call wrote a bill's law-change explanations. They are AI
// text, so the web labels them with it. ReleasePoint is the US Code release point the model read.
type LawChangeProvenance struct {
	ModelUsed     string    `json:"model_used"`
	PromptVersion string    `json:"prompt_version"`
	GeneratedAt   time.Time `json:"generated_at"`
	ReleasePoint  string    `json:"release_point"`
}

// LawChangeEntry is one section of law a bill's text changes. A section several references
// name gets their subsection paths and instructions together and the strongest kind: repeals,
// then adds, then amends, as the explainer reads them.
//
// InUSCode is false for a "nonusc:" law, which has no title or section number. IsNote is true for
// a statutory note ("10 U.S.C. 4271 note"), which is in the US Code under the title and section
// number given but isn't the section's text, so it's never loaded. Loaded is true
// when the section's current text is stored, so GET /law/{title}/{section} serves it; Heading
// is nil otherwise. Explanation is nil when the section wasn't explained for this text (not yet,
// or past the explainer's caps). AlsoChangedBy lists other bills of the same congress whose text
// amends, repeals or adds the same section, newest first.
type LawChangeEntry struct {
	SectionID      string        `json:"section_id"`
	InUSCode       bool          `json:"in_us_code"`
	IsNote         bool          `json:"is_note"`
	Loaded         bool          `json:"loaded"`
	TitleNumber    *int          `json:"title_number"`
	SectionNumber  *string       `json:"section_number"`
	Heading        *string       `json:"heading"`
	ChangeKind     string        `json:"change_kind"`
	CiteText       *string       `json:"cite_text"`
	SubsectionPath *string       `json:"subsection_path"`
	Instruction    *string       `json:"instruction"`
	Explanation    *string       `json:"explanation"`
	AlsoChangedBy  []SectionBill `json:"also_changed_by"`
}

// ParseUSCSectionID splits a US Code section ID ("/us/usc/t42/s1395w-4") into its title number
// and section number. It reports false for any other ID, a "nonusc:" one included.
func ParseUSCSectionID(id string) (int, string, bool) {
	rest, found := strings.CutPrefix(id, uscSectionIDPrefix)
	if !found {
		return 0, "", false
	}
	t, s, found := strings.Cut(rest, "/s")
	if !found || s == "" || strings.Contains(s, "/") {
		return 0, "", false
	}
	n, err := strconv.Atoi(t)
	if err != nil || n <= 0 {
		return 0, "", false
	}
	return n, s, true
}

// ParseUSCNoteID splits the ID of a statutory note ("/us/usc/t10/s4271/note") into the title and
// section number of the section it's printed under. It reports false for any other ID, a section's
// own included.
func ParseUSCNoteID(id string) (int, string, bool) {
	section, found := strings.CutSuffix(id, USCNoteSuffix)
	if !found {
		return 0, "", false
	}
	return ParseUSCSectionID(section)
}

// USCSectionID is the section ID of US Code title and section ("/us/usc/t42/s1395w-4").
func USCSectionID(title int, section string) string {
	return uscSectionIDPrefix + strconv.Itoa(title) + "/s" + section
}
