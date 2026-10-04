package htmltext_test

import (
	"strings"
	"testing"

	"github.com/justabill-org/justabill/pipeline/internal/htmltext"
)

func TestRender(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"paragraphs", "<p>One.</p><p>Two.</p>", "One.\n\nTwo."},
		{"list", "<p>Levels for</p><ul><li>revenues,</li><li>outlays.</li></ul><p>After.</p>",
			"Levels for\n\n• revenues,\n• outlays.\n\nAfter."},
		{"ordered list", "<ol><li>first</li><li>second</li></ol>", "• first\n• second"},
		{"nested list", "<ul><li>a<ul><li>b</li></ul></li><li><p>c</p></li></ul>", "• a\n  • b\n• c"},
		{"br", "<p>line one<br>line two<br/>line three</p>", "line one\nline two\nline three"},
		{"nbsp", "<p>budget&nbsp;levels.&nbsp; &nbsp; &nbsp;</p><p>Next&nbsp;</p>", "budget levels.\n\nNext"},
		{"entities", "<p>A &amp; B &lt;C&gt; &quot;D&quot; &#8212; &sect; 2</p>", "A & B <C> \"D\" — § 2"},
		{"inline tags", "<p><strong>Tax Relief</strong> This <em>bill</em> amends <a href=\"x\">law</a>.</p>",
			"Tax Relief This bill amends law."},
		{"unclosed p", "<p>one<p>two", "one\n\ntwo"},
		{"unclosed at end", "<p>one</p><p>two <b>bold", "one\n\ntwo bold"},
		{"stray end tag", "<p>x</b> y</p><p>z</p>", "x y\n\nz"},
		{"less-than in text", "<p>a < b</p><p>c</p>", "a < b\n\nc"},
		{"comment", "<p>a</p><!-- <p>hidden</p> --><p>b</p>", "a\n\nb"},
		{"script", "<script>alert(1)</script><p>text</p>", "text"},
		{"divs", "<div>a<div>b</div></div>", "a\n\nb"},
		{"plain", "  just   text ", "just text"},
		{"empty", "", ""},
		{"only tags", "<p></p><ul><li> </li></ul>", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := htmltext.Render(tt.in); got != tt.want {
				t.Errorf("Render(%q) =\n%q\nwant\n%q", tt.in, got, tt.want)
			}
		})
	}
}

// TestRender_CRSSummary renders the start of S.Con.Res. 38's CRS summary (119th) as Congress.gov
// published it on 2026-09-21.
func TestRender_CRSSummary(t *testing.T) {
	in := "<p>This concurrent resolution establishes the congressional budget for the federal government " +
		"for FY2027.&nbsp;</p><p>The resolution recommends levels and amounts for FY2027-FY2036 for</p><ul>" +
		"<li>federal revenues,</li><li>new budget authority,</li><li>the major functional categories of " +
		"spending.</li></ul><p>In addition, the resolution establishes a reserve fund.&nbsp; &nbsp; &nbsp;" +
		"&nbsp;&nbsp;</p>"
	want := strings.Join([]string{
		"This concurrent resolution establishes the congressional budget for the federal government for FY2027.",
		"",
		"The resolution recommends levels and amounts for FY2027-FY2036 for",
		"",
		"• federal revenues,",
		"• new budget authority,",
		"• the major functional categories of spending.",
		"",
		"In addition, the resolution establishes a reserve fund.",
	}, "\n")
	got := htmltext.Render(in)
	if got != want {
		t.Errorf("Render =\n%s\nwant\n%s", got, want)
	}
	if strings.ContainsAny(got, "<>& ") {
		t.Errorf("Render left a tag, entity or non-breaking space: %q", got)
	}
}
