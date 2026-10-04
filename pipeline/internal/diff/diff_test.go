package diff_test

import (
	"os"
	"regexp"
	"strconv"
	"testing"

	"github.com/justabill-org/justabill/pipeline/internal/billtext"
	"github.com/justabill-org/justabill/pipeline/internal/diff"
)

func sec(id, enum, header, content string, kids ...billtext.Section) billtext.Section {
	return billtext.Section{ID: id, Kind: "section", Enum: enum, Header: header, Content: content, Children: kids}
}

func title(id, enum, header string, kids ...billtext.Section) billtext.Section {
	return billtext.Section{ID: id, Kind: "title", Enum: enum, Header: header, Children: kids}
}

func compute(t *testing.T, old, newer []billtext.Section) ([]diff.SectionDiff, diff.Stats) {
	t.Helper()
	diffs, stats, err := diff.ComputeDiff(old, newer)
	if err != nil {
		t.Fatalf("ComputeDiff: %v", err)
	}
	return diffs, stats
}

// kinds lists each entry's type and header, for comparing in one line.
func kinds(diffs []diff.SectionDiff) []string {
	out := make([]string, 0, len(diffs))
	for _, d := range diffs {
		out = append(out, d.Type+" "+d.Header)
	}
	return out
}

func assertKinds(t *testing.T, diffs []diff.SectionDiff, want ...string) {
	t.Helper()
	got := kinds(diffs)
	if len(got) != len(want) {
		t.Fatalf("diff entries = %q, want %q", got, want)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("diff entries = %q, want %q", got, want)
		}
	}
}

func TestComputeDiff_NewIDsSameTextIsEmpty(t *testing.T) {
	old := []billtext.Section{
		sec("H1", "Sec. 1.", "Short title", "This Act may be cited as the Test Act."),
		title("H2", "Title I", "General", sec("H3", "Sec. 101.", "Rule", "The rule.",
			billtext.Section{ID: "H4", Kind: "subsection", Enum: "(a)", Header: "In general", Content: "Text."})),
	}
	newer := []billtext.Section{
		sec("X1", "SEC. 1", "Short title.", "This Act may be  cited as the Test Act.\n"),
		title("X2", "Title I", "General", sec("X3", "Sec. 101.", "Rule", "The rule.",
			billtext.Section{ID: "X4", Kind: "subsection", Enum: "(a)", Header: "In general", Content: "Text."})),
	}
	diffs, stats := compute(t, old, newer)
	if !stats.Empty() || len(diffs) != 0 {
		t.Fatalf("got %+v %q, want an empty diff", stats, kinds(diffs))
	}
}

func TestComputeDiff_RewordedSectionIsModifiedAlone(t *testing.T) {
	old := []billtext.Section{
		sec("a1", "Sec. 1.", "Short title", "The Test Act."),
		sec("a2", "Sec. 2.", "Funding", "There are authorized $5."),
		sec("a3", "Sec. 3.", "Report", "The Secretary shall report."),
	}
	newer := []billtext.Section{
		sec("b1", "Sec. 1.", "Short title", "The Test Act."),
		sec("b2", "Sec. 2.", "Funding", "There are authorized $10 each year."),
		sec("b3", "Sec. 3.", "Report", "The Secretary shall report."),
	}
	diffs, stats := compute(t, old, newer)
	assertKinds(t, diffs, "modified Sec. 2. Funding")
	d := diffs[0]
	if d.SectionID != "b2" || d.OldText != "There are authorized $5." ||
		d.NewText != "There are authorized $10 each year." {
		t.Errorf("modified entry = %+v", d)
	}
	want := diff.Stats{SectionsModified: 1, WordsAdded: 2}
	if stats != want {
		t.Errorf("stats = %+v, want %+v", stats, want)
	}
}

func TestComputeDiff_SubsectionChangeModifiesItsSection(t *testing.T) {
	sub := func(id, content string) billtext.Section {
		return billtext.Section{ID: id, Kind: "subsection", Enum: "(a)", Header: "In general", Content: content}
	}
	old := []billtext.Section{sec("a1", "Sec. 1.", "Rule", "", sub("a2", "Old words."))}
	newer := []billtext.Section{sec("b1", "Sec. 1.", "Rule", "", sub("b2", "New words."))}
	diffs, _ := compute(t, old, newer)
	assertKinds(t, diffs, "modified Sec. 1. Rule")
	if diffs[0].OldText != "(a) In general\nOld words." || diffs[0].NewText != "(a) In general\nNew words." {
		t.Errorf("texts = %q -> %q", diffs[0].OldText, diffs[0].NewText)
	}
}

func TestComputeDiff_InsertedSectionRenumbersTheRest(t *testing.T) {
	old := []billtext.Section{
		sec("a1", "Sec. 1.", "Short title", "The Test Act."),
		sec("a2", "Sec. 2.", "Funding", "Money."),
		sec("a3", "Sec. 3.", "Report", "A report."),
	}
	newer := []billtext.Section{
		sec("b1", "Sec. 1.", "Short title", "The Test Act."),
		sec("b2", "Sec. 2.", "Findings", "Congress finds."),
		sec("b3", "Sec. 3.", "Funding", "Money."),
		sec("b4", "Sec. 4.", "Report", "A report."),
	}
	diffs, stats := compute(t, old, newer)
	assertKinds(t, diffs, "added Sec. 2. Findings")
	if stats.SectionsAdded != 1 || stats.SectionsModified != 0 || stats.SectionsRemoved != 0 {
		t.Errorf("stats = %+v", stats)
	}
}

func TestComputeDiff_RenamedSectionKeepsItsNumber(t *testing.T) {
	old := []billtext.Section{sec("a1", "Sec. 2.", "Funding", "Money.")}
	newer := []billtext.Section{sec("b1", "Sec. 2.", "Authorization of appropriations", "Money.")}
	diffs, _ := compute(t, old, newer)
	assertKinds(t, diffs, "modified Sec. 2. Authorization of appropriations")
}

func TestComputeDiff_MovedSectionIsMatched(t *testing.T) {
	old := []billtext.Section{
		title("a1", "Title I", "Health", sec("a2", "Sec. 101.", "Telehealth", "Telehealth text.")),
		title("a3", "Title II", "Tax"),
	}
	newer := []billtext.Section{
		title("b1", "Title I", "Health"),
		title("b3", "Title II", "Tax", sec("b2", "Sec. 201.", "Telehealth", "Telehealth text, amended.")),
	}
	diffs, _ := compute(t, old, newer)
	assertKinds(t, diffs, "modified Title II Tax, Sec. 201. Telehealth")
}

func TestComputeDiff_RenumberedTitleKeepsItsSections(t *testing.T) {
	old := []billtext.Section{title("a1", "Title II", "Tax", sec("a2", "Sec. 1.", "Credit", "A credit."))}
	newer := []billtext.Section{
		title("b0", "Title I", "Findings", sec("b9", "Sec. 1.", "Findings", "Congress finds.")),
		title("b1", "Title II", "Taxes", sec("b2", "Sec. 1.", "Credit", "A credit.")),
	}
	diffs, _ := compute(t, old, newer)
	assertKinds(t, diffs, "added Title I Findings, Sec. 1. Findings")
}

func TestComputeDiff_AddedAndRemovedInDocumentOrder(t *testing.T) {
	old := []billtext.Section{
		sec("a1", "Sec. 1.", "Short title", "The Test Act."),
		sec("a2", "Sec. 2.", "Sunset", "It ends."),
		sec("a3", "Sec. 3.", "Report", "A report."),
	}
	newer := []billtext.Section{
		sec("b1", "Sec. 1.", "Short title", "The Test Act."),
		sec("b3", "Sec. 2.", "Report", "A report."),
		sec("b4", "Sec. 3.", "Effective date", "Now."),
	}
	diffs, stats := compute(t, old, newer)
	assertKinds(t, diffs, "removed Sec. 2. Sunset", "added Sec. 3. Effective date")
	if diffs[0].OldText != "It ends." || diffs[0].SectionID != "a2" || diffs[1].NewText != "Now." {
		t.Errorf("entries = %+v", diffs)
	}
	want := diff.Stats{SectionsAdded: 1, SectionsRemoved: 1, WordsAdded: 1, WordsRemoved: 2}
	if stats != want {
		t.Errorf("stats = %+v, want %+v", stats, want)
	}
}

func TestComputeDiff_LooseTextAndHeaderlessUnits(t *testing.T) {
	old := []billtext.Section{
		{ID: "t1", Kind: "text", Content: "Strike all after the enacting clause."},
		{ID: "t2", Kind: "title", Enum: "Title I", Header: "Programs", Content: "Intro words.",
			Children: []billtext.Section{{ID: "p1", Kind: "appropriations-major", Header: "Army", Content: "$1."}}},
	}
	newer := []billtext.Section{
		{ID: "u1", Kind: "text", Content: "Strike all after the enacting clause."},
		{ID: "u2", Kind: "title", Enum: "Title I", Header: "Programs", Content: "Intro words, amended.",
			Children: []billtext.Section{{ID: "q1", Kind: "appropriations-major", Header: "Army", Content: "$2."}}},
	}
	diffs, _ := compute(t, old, newer)
	assertKinds(t, diffs, "modified Title I Programs, text", "modified Title I Programs, Army")
}

func TestComputeDiff_PlainTextSections(t *testing.T) {
	// Sections parsed from plain text have no kind or enum.
	old := []billtext.Section{{ID: "sec-1", Header: "Short title.", Content: "A."}}
	newer := []billtext.Section{{ID: "sec-9", Header: "Short title.", Content: "B."}}
	diffs, _ := compute(t, old, newer)
	assertKinds(t, diffs, "modified Short title.")
}

func TestComputeDiff_EmptyInputs(t *testing.T) {
	diffs, stats := compute(t, nil, nil)
	if len(diffs) != 0 || !stats.Empty() {
		t.Errorf("got %v %+v", diffs, stats)
	}
	diffs, stats = compute(t, nil, []billtext.Section{sec("b1", "Sec. 1.", "Short title", "The Act.")})
	assertKinds(t, diffs, "added Sec. 1. Short title")
	if stats.WordsAdded != 2 {
		t.Errorf("stats = %+v", stats)
	}
}

func parseFixture(t testing.TB, name string) []billtext.Section {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return parse(t, data)
}

func parse(t testing.TB, data []byte) []billtext.Section {
	t.Helper()
	sections, err := billtext.ParseXML(data)
	if err != nil {
		t.Fatal(err)
	}
	return sections
}

// GovInfo gives every version new ids: H.R. 815 (118th) as introduced and as it passed the
// House share none, though its first two sections are word for word the same.
func TestComputeDiff_RealVersionsWithNewIDs(t *testing.T) {
	ih := parseFixture(t, "BILLS-118hr815ih.xml")
	eh := parseFixture(t, "BILLS-118hr815eh.xml")
	if ih[0].ID == eh[0].ID {
		t.Fatalf("fixture ids match (%s); the test needs versions with new ids", ih[0].ID)
	}
	diffs, stats := compute(t, ih, eh)
	assertKinds(t, diffs, "added Sec. 3. Modification of certain housing loan fees")
	if stats.SectionsRemoved != 0 || stats.SectionsModified != 0 {
		t.Errorf("stats = %+v", stats)
	}
}

func TestComputeDiff_RealVersionWithEveryIDRegenerated(t *testing.T) {
	data, err := os.ReadFile("testdata/BILLS-118hr815ih.xml")
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	regenerated := regexp.MustCompile(` id="[^"]*"`).ReplaceAllFunc(data, func([]byte) []byte {
		n++
		return []byte(` id="NEW` + strconv.Itoa(n) + `"`)
	})
	diffs, stats := compute(t, parse(t, data), parse(t, regenerated))
	if !stats.Empty() || len(diffs) != 0 {
		t.Fatalf("got %+v %q, want an empty diff", stats, kinds(diffs))
	}
}
