package billtext_test

import (
	"bytes"
	"compress/gzip"
	"encoding/xml"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode"

	"github.com/justabill-org/justabill/pipeline/internal/billtext"
)

// maxTextLoss is how far the parsed text's size may stray from the XML body's: the parser adds
// designations ("Sec.", "Title"), quote marks and header dashes, and must not drop text.
const maxTextLoss = 0.05

// readBillFixture reads a GovInfo XML fixture from testdata, gunzipping *.gz.
func readBillFixture(t testing.TB, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	if !strings.HasSuffix(name, ".gz") {
		return data
	}
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		t.Fatalf("gunzip %s: %v", name, err)
	}
	out, err := io.ReadAll(zr)
	if err != nil {
		t.Fatalf("gunzip %s: %v", name, err)
	}
	return out
}

// bodyChars counts the non-space characters of the XML's body text: everything inside
// legis-body, resolution-body, engrossed-amendment-body or preamble.
func bodyChars(t *testing.T, data []byte) int {
	t.Helper()
	dec := xml.NewDecoder(bytes.NewReader(data))
	dec.Strict = false
	dec.Entity = xml.HTMLEntity
	depth, n := 0, 0
	for {
		tok, err := dec.Token()
		if errors.Is(err, io.EOF) {
			return n
		}
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		switch tk := tok.(type) {
		case xml.StartElement:
			switch tk.Name.Local {
			case "legis-body", "resolution-body", "engrossed-amendment-body", "preamble":
				depth++
			}
		case xml.EndElement:
			switch tk.Name.Local {
			case "legis-body", "resolution-body", "engrossed-amendment-body", "preamble":
				depth--
			}
		case xml.CharData:
			if depth > 0 {
				n += nonSpace(string(tk))
			}
		}
	}
}

func nonSpace(s string) int {
	n := 0
	for _, r := range s {
		if !unicode.IsSpace(r) {
			n++
		}
	}
	return n
}

// treeChars counts the non-space characters of every section's enum, header and content.
func treeChars(sections []billtext.Section) int {
	n := 0
	for _, s := range sections {
		n += nonSpace(s.Enum) + nonSpace(s.Header) + nonSpace(s.Content) + treeChars(s.Children)
	}
	return n
}

// countKind counts the sections of one kind at any depth.
func countKind(sections []billtext.Section, kind string) int {
	n := 0
	for _, s := range sections {
		if s.Kind == kind {
			n++
		}
		n += countKind(s.Children, kind)
	}
	return n
}

// findSection returns the first section at any depth whose enum is enum.
func findSection(sections []billtext.Section, enum string) *billtext.Section {
	for i := range sections {
		if sections[i].Enum == enum {
			return &sections[i]
		}
		if s := findSection(sections[i].Children, enum); s != nil {
			return s
		}
	}
	return nil
}

// checkTextKept fails when the parsed text's size is more than maxTextLoss off the XML body's.
func checkTextKept(t *testing.T, data []byte, sections []billtext.Section) {
	t.Helper()
	want, got := bodyChars(t, data), treeChars(sections)
	if want == 0 {
		t.Fatal("fixture has no body text")
	}
	ratio := float64(got) / float64(want)
	t.Logf("body text: %d characters in the XML, %d parsed (%.3f)", want, got, ratio)
	if ratio < 1-maxTextLoss || ratio > 1+maxTextLoss {
		t.Errorf("parsed %d of the body's %d non-space characters (%.3f), want within %.0f%%",
			got, want, ratio, maxTextLoss*100)
	}
}

func TestParseXML_RealFixtures(t *testing.T) {
	cases := []struct {
		file                           string
		top, sections, subsections     int
		titles, divisions, looseBlocks int
	}{
		// The enrolled One Big Beautiful Bill Act: titles, subtitles, parts, chapters, subchapters.
		{file: "BILLS-119hr1enr.xml.gz", top: 11, sections: 310, subsections: 719, titles: 10},
		// A Senate amendment that substitutes a whole supplemental appropriations Act: divisions,
		// titles, appropriations paragraphs, and the "Strike all after the enacting clause" line.
		{
			file: "BILLS-118hr815eas.xml.gz", top: 7, sections: 54, subsections: 48, titles: 11,
			divisions: 2, looseBlocks: 1,
		},
		{file: "BILLS-119hr4eas.xml", top: 4, sections: 3, subsections: 2, looseBlocks: 1},
		// A concurrent resolution (the budget) with titles in its resolution-body.
		{file: "BILLS-119hconres14eh.xml.gz", top: 6, sections: 15, subsections: 24, titles: 5},
		{file: "BILLS-119hr22ih.xml.gz", top: 8, sections: 8, subsections: 11},
		{file: "BILLS-119sres1ats.xml", top: 1, sections: 1},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			data := readBillFixture(t, tc.file)
			sections, err := billtext.ParseXML(data)
			if err != nil {
				t.Fatalf("ParseXML: %v", err)
			}
			if len(sections) != tc.top {
				t.Errorf("top-level units = %d, want %d", len(sections), tc.top)
			}
			for kind, want := range map[string]int{
				"section": tc.sections, "subsection": tc.subsections, "title": tc.titles,
				"division": tc.divisions, "text": tc.looseBlocks,
			} {
				if got := countKind(sections, kind); got != want {
					t.Errorf("%s units = %d, want %d", kind, got, want)
				}
			}
			checkTextKept(t, data, sections)
		})
	}
}

func TestParseXML_HR1EnrolledKeepsNestedSectionsAndInlineText(t *testing.T) {
	sections, err := billtext.ParseXML(readBillFixture(t, "BILLS-119hr1enr.xml.gz"))
	if err != nil {
		t.Fatal(err)
	}
	title := sections[1]
	if title.Kind != "title" || title.Enum != "Title I" ||
		title.Header != "Committee on Agriculture, Nutrition, and Forestry" {
		t.Errorf("second unit = %q %q %q, want title I", title.Kind, title.Enum, title.Header)
	}
	sec := findSection(sections, "Sec. 10101.")
	if sec == nil {
		t.Fatal("Sec. 10101. (inside Title I, Subtitle A) not found")
	}
	if sec.Header != "Re-evaluation of thrifty food plan" || len(sec.Children) != 2 {
		t.Errorf("Sec. 10101. = %q with %d subsections", sec.Header, len(sec.Children))
	}
	// The quoted block that replaces section 3(u), with its paragraphs and defined term.
	for _, want := range []string{
		"inserting the following:\n“(u) Thrifty food plan.—\n",
		"\n  “(1) In general.—The term “thrifty food plan” means the diet",
		"\n    “(A) the relevant market baskets",
		"\n    “(C) the cost of the thrifty food plan may only be adjusted in accordance with this subsection.\n",
	} {
		if !strings.Contains(sec.Children[0].Content, want) {
			t.Errorf("Sec. 10101.(a) content lacks %q", want)
		}
	}
	fehb := findSection(sections, "Sec. 90101.")
	if fehb == nil {
		t.Fatal("Sec. 90101. (inside Title IX, Subtitle B) not found")
	}
	if !strings.Contains(fehb.Children[0].Content, "may be cited as the “FEHB Protection Act of 2025”.") {
		t.Errorf("short title lost its inline text: %q", fehb.Children[0].Content)
	}
}

func TestParseXML_EngrossedAmendment(t *testing.T) {
	sections, err := billtext.ParseXML(readBillFixture(t, "BILLS-119hr4eas.xml"))
	if err != nil {
		t.Fatalf("ParseXML: %v", err)
	}
	if !strings.HasPrefix(sections[0].Content, "That the bill from the House of Representatives (H.R. 4) entitled") {
		t.Errorf("resolving section = %q", sections[0].Content)
	}
	if sections[1].Kind != "text" ||
		sections[1].Content != "Strike all after the enacting clause and insert the following:" {
		t.Errorf("amendment instruction = %+v", sections[1])
	}
	if sections[2].Enum != "Sec. 1." ||
		sections[2].Content != "This Act may be cited as the “Rescissions Act of 2025”." {
		t.Errorf("section 1 = %q %q", sections[2].Enum, sections[2].Content)
	}
	rescissions := sections[3].Children[1]
	for _, want := range []string{
		"The rescissions described in this subsection are as follows:\n(1) Of the unobligated balances",
		"\n(20)\n  (A) Amounts made available for “Corporation for Public Broadcasting”",
	} {
		if !strings.Contains(rescissions.Content, want) {
			t.Errorf("subsection (b) lacks %q", want)
		}
	}
}

// TestParseXML_LargeBills checks the parser against every BILLS-*.xml file in the directory
// BILLTEXT_XML_DIR names, such as the 6.7 MB FY2024 NDAA (BILLS-118hr2670enr.xml), which is too
// big to commit. Download them from https://www.govinfo.gov/content/pkg/BILLS-<id>/xml/BILLS-<id>.xml.
func TestParseXML_LargeBills(t *testing.T) {
	dir := os.Getenv("BILLTEXT_XML_DIR")
	if dir == "" {
		t.Skip("BILLTEXT_XML_DIR not set")
	}
	files, err := filepath.Glob(filepath.Join(dir, "BILLS-*.xml"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no BILLS-*.xml files in %s", dir)
	}
	for _, f := range files {
		t.Run(filepath.Base(f), func(t *testing.T) {
			data, readErr := os.ReadFile(f)
			if readErr != nil {
				t.Fatal(readErr)
			}
			sections, parseErr := billtext.ParseXML(data)
			if parseErr != nil {
				t.Fatalf("ParseXML: %v", parseErr)
			}
			t.Logf("%d sections", countKind(sections, "section"))
			checkTextKept(t, data, sections)
		})
	}
}
