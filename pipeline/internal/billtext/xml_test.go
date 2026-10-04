package billtext_test

import (
	"strings"
	"testing"

	"github.com/justabill-org/justabill/pipeline/internal/billtext"
)

func parse(t *testing.T, doc string) []billtext.Section {
	t.Helper()
	sections, err := billtext.ParseXML([]byte(doc))
	if err != nil {
		t.Fatalf("ParseXML: %v", err)
	}
	return sections
}

func TestParseXML_ContainersNestToAnyDepth(t *testing.T) {
	sections := parse(t, `<bill><legis-body>
<division id="d"><enum>A</enum><header>Defense</header>
 <title id="t"><enum>I</enum><header>Procurement</header>
  <subtitle id="st"><enum>A</enum><header>Army</header>
   <section id="s"><enum>101.</enum><header>Authorization</header><text>Funds are authorized.</text></section>
  </subtitle>
 </title>
</division></legis-body></bill>`)
	if len(sections) != 1 {
		t.Fatalf("top-level units = %d, want 1", len(sections))
	}
	div := sections[0]
	title := div.Children[0]
	sub := title.Children[0]
	sec := sub.Children[0]
	got := []string{div.Enum, div.Header, title.Enum, sub.Kind, sub.Enum, sec.Kind, sec.Enum, sec.Header, sec.Content}
	want := []string{"Division A", "Defense", "Title I", "subtitle", "Subtitle A", "section", "Sec. 101.",
		"Authorization", "Funds are authorized."}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("tree = %q, want %q", got, want)
	}
}

func TestParseXML_RendersUnitsBelowSubsections(t *testing.T) {
	sections := parse(t, `<bill><legis-body><section id="s"><enum>2.</enum><header>Rules</header>
<subsection id="a"><enum>(a)</enum><header>In general</header><text>The Secretary shall—</text>
 <paragraph><enum>(1)</enum><header>Report</header><text>report to the <term>covered committees</term>
 under <external-xref legal-doc="usc">5 U.S.C. 101</external-xref>; and</text>
  <subparagraph><enum>(A)</enum><text>publish it.</text></subparagraph></paragraph>
 <paragraph changed="deleted"><enum>(2)</enum><text>struck text</text></paragraph>
 <continuation-text>Nothing else.</continuation-text>
</subsection></section></legis-body></bill>`)
	sub := sections[0].Children[0]
	want := "The Secretary shall—\n" +
		"(1) Report.—report to the “covered committees” under 5 U.S.C. 101; and\n" +
		"  (A) publish it.\n" +
		"Nothing else."
	if sub.Enum != "(a)" || sub.Header != "In general" || sub.Content != want {
		t.Errorf("subsection = %q %q\n%s\nwant\n%s", sub.Enum, sub.Header, sub.Content, want)
	}
}

func TestParseXML_QuotedBlocksAndTables(t *testing.T) {
	sections := parse(t, `<bill><legis-body><section id="s"><enum>3.</enum><header>Amendment</header>
<text>Section 5 is amended to read as follows:</text>
<quoted-block><section><enum>5.</enum><header>Rule</header><text>A rule.</text>
 <subsection><enum>(a)</enum><text>Detail.</text></subsection></section></quoted-block>
<after-quoted-block>.</after-quoted-block>
<table><tgroup><thead><row><entry>State</entry><entry>Amount</entry></row></thead>
<tbody><row><entry></entry><entry>$5</entry></row></tbody></tgroup></table>
</section></legis-body></bill>`)
	if len(sections) != 1 || len(sections[0].Children) != 0 {
		t.Fatalf("a quoted section became a unit: %+v", sections)
	}
	want := "Section 5 is amended to read as follows:\n" +
		"“5. Rule.—A rule.\n" +
		"  “(a) Detail.”.\n" +
		"State | Amount\n" +
		" | $5"
	if sections[0].Content != want {
		t.Errorf("content =\n%s\nwant\n%s", sections[0].Content, want)
	}
}

func TestParseXML_ResolutionPreambleAndUndesignatedSection(t *testing.T) {
	sections := parse(t, `<resolution><form><official-title>Honoring X.</official-title></form>
<preamble><whereas><text>Whereas X served;</text></whereas><whereas><text>Whereas X retired:</text></whereas></preamble>
<resolution-body><section id="r"><text>That the House honors X.</text></section></resolution-body></resolution>`)
	if len(sections) != 2 {
		t.Fatalf("units = %d, want 2", len(sections))
	}
	if p := sections[0]; p.Kind != "preamble" || p.Header != "Preamble" ||
		p.Content != "Whereas X served;\nWhereas X retired:" {
		t.Errorf("preamble = %+v", p)
	}
	if r := sections[1]; r.Enum != "" || r.Header != "" || r.Content != "That the House honors X." {
		t.Errorf("resolving section = %+v", r)
	}
}

func TestParseXML_AppropriationsParagraphs(t *testing.T) {
	sections := parse(t, `<bill><legis-body><title><enum>I</enum><header>Military Personnel</header>
<appropriations-intermediate><header>Military Personnel, Army</header>
<text>For pay, $5.</text></appropriations-intermediate>
<section><enum>101.</enum><header>Limit</header><text>None.</text></section>
</title></legis-body></bill>`)
	kids := sections[0].Children
	if len(kids) != 2 || kids[0].Kind != "appropriations-intermediate" ||
		kids[0].Header != "Military Personnel, Army" || kids[0].Content != "For pay, $5." {
		t.Errorf("title children = %+v", kids)
	}
}

func TestParseXML_SectionIDsAreUniqueAndNeverEmpty(t *testing.T) {
	sections := parse(t, `<bill><legis-body>
<section id="dup"><text>One.</text></section><section id="dup"><text>Two.</text></section>
<section><text>Three.</text></section></legis-body></bill>`)
	seen := map[string]bool{}
	for _, s := range sections {
		if s.ID == "" || seen[s.ID] {
			t.Errorf("ID %q empty or repeated", s.ID)
		}
		seen[s.ID] = true
	}
}

func TestParseXML_Errors(t *testing.T) {
	for name, doc := range map[string]string{
		"unsupported root": `<html><body>not a bill</body></html>`,
		"unclosed":         `<bill><legis-body><section>`,
		"no root":          `   `,
	} {
		if _, err := billtext.ParseXML([]byte(doc)); err == nil {
			t.Errorf("%s: want an error", name)
		}
	}
}

func TestParseXML_NoBodyGivesNoSections(t *testing.T) {
	sections := parse(t, `<bill><form><official-title>A bill.</official-title></form></bill>`)
	if sections == nil || len(sections) != 0 {
		t.Errorf("sections = %#v, want an empty slice", sections)
	}
}
