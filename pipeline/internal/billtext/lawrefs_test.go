package billtext_test

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/pipeline/internal/billtext"
)

const payGo = "nonusc:Section %s of the Statutory Pay-As-You-Go Act of 2010"

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "lawrefs", name))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// refSet lists refs as "section kind".
func refSet(refs []billtext.LawRef) []string {
	out := make([]string, 0, len(refs))
	for _, r := range refs {
		out = append(out, r.SectionID+" "+r.Kind)
	}
	return out
}

// TestParseLawRefsFixtures checks the exact (section, kind) set of real bill XML from GovInfo.
func TestParseLawRefsFixtures(t *testing.T) {
	for _, tc := range []struct {
		name, file string
		want       []string
	}{
		{
			// Section 1848(t) and (c)(2)(B)(iv)(V) of the Social Security Act: two amendments to
			// one section, merged into one row.
			name: "Medicare date fix", file: "BILLS-119hr879ih.xml",
			want: []string{"/us/usc/t42/s1395w-4 amends"},
		},
		{
			// Four titles of Medicare changes: amendments, a new section inserted into part E
			// (adds, on the part's US Code citation), and cites in definitions.
			name: "omnibus titles", file: "BILLS-119hr9693ih.xml",
			want: []string{
				"/us/usc/t42/s1315a amends",
				"/us/usc/t42/s1395 cites",
				"/us/usc/t42/s1395kk cites",
				"/us/usc/t42/s1395l amends",
				"/us/usc/t42/s1395m amends",
				"/us/usc/t42/s1395w-4 amends",
				"/us/usc/t42/s1395x adds",
				"/us/usc/t42/s1396 cites",
				"/us/usc/t42/s1397aa cites",
				"nonusc:Section 4005 of the 21st Century Cures Act cites",
			},
		},
		{
			name: "repeal of a subsection", file: "BILLS-119hr3404ih.xml",
			want: []string{"/us/usc/t29/s2612 repeals"},
		},
		{
			name: "repeal of an Act", file: "BILLS-119hr1603ih.xml",
			want: []string{"/us/usc/t12/s5481 repeals"},
		},
		{
			// A new section 226 of the Internal Revenue Code (title 26, from the chapter-level
			// citation and the quoted section's number), and a new paragraph in section 62(a).
			name: "new-section insert", file: "BILLS-119hr9978ih.xml",
			want: []string{"/us/usc/t26/s226 adds", "/us/usc/t26/s62 amends"},
		},
		{name: "resolution with no citations", file: "BILLS-119hres24ih.xml", want: []string{}},
		{
			// The Statutory Pay-As-You-Go Act of 2010 is only ever named, never cited in the US Code.
			name: "Act cited by popular name only", file: "BILLS-119hr9879ih.xml",
			want: []string{
				strings.Replace(payGo, "%s", "2", 1) + " amends",
				strings.Replace(payGo, "%s", "3", 1) + " amends",
				strings.Replace(payGo, "%s", "3A", 1) + " cites",
				strings.Replace(payGo, "%s", "4", 1) + " amends",
				strings.Replace(payGo, "%s", "5", 1) + " amends",
				strings.Replace(payGo, "%s", "6", 1) + " amends",
			},
		},
		{
			// Title 5 is positive law: "Section 8103(a) of title 5, United States Code" has no tag.
			name: "untagged positive-law title", file: "BILLS-119hr9869ih.xml",
			want: []string{"/us/usc/t5/s8103 amends"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refs, err := billtext.ParseLawRefs(readFixture(t, tc.file))
			if err != nil {
				t.Fatalf("ParseLawRefs: %v", err)
			}
			if got := refSet(refs); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("refs =\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tc.want, "\n"))
			}
			for _, r := range refs {
				if (r.Instruction != "") != (r.Kind != model.LawRefCites) {
					t.Errorf("%s %s: instruction %q", r.SectionID, r.Kind, r.Instruction)
				}
				if len(r.Instruction) > billtext.MaxInstruction {
					t.Errorf("%s: instruction is %d bytes", r.SectionID, len(r.Instruction))
				}
			}
		})
	}
}

func TestParseLawRefsMergesOneSection(t *testing.T) {
	refs, err := billtext.ParseLawRefs(readFixture(t, "BILLS-119hr879ih.xml"))
	if err != nil || len(refs) != 1 {
		t.Fatalf("ParseLawRefs = %v, %v; want one reference", refs, err)
	}
	r := refs[0]
	if r.CiteText != "42 U.S.C. 1395w–4(t); 42 U.S.C. 1395w–4(c)(2)(B)(iv)(V)" {
		t.Errorf("cite = %q", r.CiteText)
	}
	if r.SubsectionPath != "(t), (c)(2)(B)(iv)(V)" {
		t.Errorf("subsection = %q", r.SubsectionPath)
	}
	if r.BillSection != "HA9FE2A2D10C94940A5E0CED8C9A6D9DB" {
		t.Errorf("bill section = %q", r.BillSection)
	}
	for _, part := range []string{
		"Section 1848(t) of the Social Security Act (42 U.S.C. 1395w–4(t)) is amended— (1) in the header, " +
			"by striking “2024” and inserting “2025”",
		"“ (F) such services furnished on or after April 1, 2025",
		"\n\nSection 1848(c)(2)(B)(iv)(V) of the Social Security Act",
	} {
		if !strings.Contains(r.Instruction, part) {
			t.Errorf("instruction lacks %q:\n%s", part, r.Instruction)
		}
	}
}

// bill wraps legis-body content in a bill document.
func bill(body string) []byte {
	return []byte(`<?xml version="1.0"?><!DOCTYPE bill PUBLIC "-//US Congress//DTDs/bill.dtd//EN" "bill.dtd">
<bill><form><official-title>To amend section 5 of the Title Act (<external-xref legal-doc="usc"
parsable-cite="usc/1/1">1 U.S.C. 1</external-xref>), which is amended.</official-title></form>
<legis-body>` + body + `</legis-body></bill>`)
}

func xref(cite, display string) string {
	return `<external-xref legal-doc="usc" parsable-cite="` + cite + `">` + display + `</external-xref>`
}

func TestParseLawRefsCitation(t *testing.T) {
	data := bill(`<section id="S2"><enum>2.</enum><header>Update</header><text>Section 1848(t) of the Social
Security Act (` + xref("usc/42/1395w-4", "42 U.S.C. 1395w–4(t)") + `) is amended by adding at the end the
following new paragraph:</text><quoted-block><paragraph><enum>(3)</enum><text>New text.</text></paragraph>
<after-quoted-block>.</after-quoted-block></quoted-block></section>`)
	refs, err := billtext.ParseLawRefs(data)
	if err != nil {
		t.Fatal(err)
	}
	want := []billtext.LawRef{{
		SectionID: "/us/usc/t42/s1395w-4", Kind: model.LawRefAmends, CiteText: "42 U.S.C. 1395w–4(t)",
		SubsectionPath: "(t)", BillSection: "S2",
		Instruction: "Section 1848(t) of the Social Security Act (42 U.S.C. 1395w–4(t)) is amended by adding at " +
			"the end the following new paragraph: “ (3) New text. .”",
	}}
	if !reflect.DeepEqual(refs, want) {
		t.Errorf("refs = %+v\nwant %+v", refs, want)
	}
}

func TestParseLawRefsKinds(t *testing.T) {
	x42 := xref("usc/42/1395", "42 U.S.C. 1395")
	for _, tc := range []struct {
		name, body string
		want       []string
	}{
		{"amended", `<section><text>Section 1 (` + x42 + `) is amended by striking “a”.</text></section>`,
			[]string{"/us/usc/t42/s1395 amends"}},
		{"further amended", `<section><text>Section 1 (` + x42 + `), as amended by section 2, is further
			amended—</text></section>`, []string{"/us/usc/t42/s1395 amends"}},
		{"repealed", `<section><text>Section 1 (` + x42 + `) is hereby repealed.</text></section>`,
			[]string{"/us/usc/t42/s1395 repeals"}},
		{"adds a new section", `<section><text>Part A (` + x42 + ` et seq.) is amended by inserting after section
			1866 the following new section:</text></section>`, []string{"/us/usc/t42/s1395 adds"}},
		{"adds a subsection only amends", `<section><text>Section 1 (` + x42 + `) is amended by adding at the end
			the following new subsection:</text></section>`, []string{"/us/usc/t42/s1395 amends"}},
		{"both of a list are amended", `<section><text>Section 1 (` + x42 + `) and section 2 (` +
			xref("usc/42/1396", "42 U.S.C. 1396") + `) are each amended by striking “a”.</text></section>`,
			[]string{"/us/usc/t42/s1395 amends", "/us/usc/t42/s1396 amends"}},
		{"cites", `<section><text>The term has the meaning given in section 1 (` + x42 + `).</text></section>`,
			[]string{"/us/usc/t42/s1395 cites"}},
		{"the verb is in another sentence", `<section><text>As defined in section 1 (` + x42 + `). Section 2
			of this Act is amended.</text></section>`, []string{"/us/usc/t42/s1395 cites"}},
		{"quoted text only cites", `<section><text>Section 1 of this Act is amended by inserting “section 1 (` +
			`<quote>` + x42 + `</quote>)”.</text><quoted-block><section><text>Under section 2 (` +
			xref("usc/42/1396", "42 U.S.C. 1396") + `) is amended.</text></section></quoted-block></section>`,
			[]string{"/us/usc/t42/s1395 cites", "/us/usc/t42/s1396 cites"}},
		{"items of a list of amendments", `<section><text>Title XVIII of the Social Security Act is
			amended—</text><paragraph><enum>(1)</enum><text>in section 1814(a) (` + xref("usc/42/1395f",
			"42 U.S.C. 1395f(a)") + `), by striking “a”; and</text></paragraph></section>`,
			[]string{"/us/usc/t42/s1395f amends"}},
		{"items of a list of repeals", `<section><text>The following provisions of law are repealed:</text>
			<paragraph><text>Section 814 (` + xref("usc/10/4271", "10 U.S.C. 4271") + `).</text></paragraph>
			</section>`, []string{"/us/usc/t10/s4271 repeals"}},
		{"struck and replaced section", `<section><text>The Military Selective Service Act (` + xref("usc/50/3801",
			"50 U.S.C. 3801 et seq.") + `) is amended by striking section 3 (` + xref("usc/50/3802",
			"50 U.S.C. 3802") + `) and inserting the following new section 3:</text></section>`,
			[]string{"/us/usc/t50/s3801 amends", "/us/usc/t50/s3802 amends"}},
		{"statutory note", `<section><text>Section 1(b) (` + xref("usc/6/101", "6 U.S.C. 101 note") + `) is
			amended.</text></section>`, []string{"/us/usc/t6/s101/note amends"}},
		{"untagged Internal Revenue Code", `<section><text>Section 45X(b) of the Internal Revenue Code of 1986 is
			amended.</text></section>`, []string{"/us/usc/t26/s45X amends"}},
		{"untagged title", `<section><text>Section 552 of title 5, United States Code, is
			repealed.</text></section>`, []string{"/us/usc/t5/s552 repeals"}},
		{"Act with its US Code citation", `<section><text>Section 1848 of the Social Security Act (` +
			xref("usc/42/1395w-4", "42 U.S.C. 1395w–4") + `) is amended.</text></section>`,
			[]string{"/us/usc/t42/s1395w-4 amends"}},
		{"Act without a US Code citation", `<section><text>Section 5(b) of the Social Security Act is
			amended.</text></section>`, []string{"nonusc:Section 5 of the Social Security Act amends"}},
		{"Act for a fiscal year", `<section><text>Section 5143 of the National Defense Authorization Act for
			Fiscal Year 2012 is repealed.</text></section>`,
			[]string{"nonusc:Section 5143 of the National Defense Authorization Act for Fiscal Year 2012 repeals"}},
		{"new section of a positive-law title", `<section><text>Chapter 55 of title 5, United States Code, is
			amended by inserting after section 5545b the following new section:</text><quoted-block>
			<section><enum>§ 5545c.</enum><header>Pay</header><text>Text.</text></section></quoted-block>
			</section>`, []string{"/us/usc/t5/s5545c adds"}},
		{"chapter citation names the title", `<section><text>Part VII of ` +
			`<external-xref legal-doc="usc-chapter" parsable-cite="usc-chapter/26/1">chapter 1</external-xref>
			of such Code is amended by adding at the end the following new section:</text><quoted-block>
			<section><enum>226.</enum><text>Text.</text></section></quoted-block></section>`,
			[]string{"/us/usc/t26/s226 adds"}},
		{"a quoted paragraph isn't a new section", `<section><text>Chapter 55 of title 5, United States Code, is
			amended by adding at the end the following new section:</text><quoted-block><paragraph>
			<enum>(3)</enum><text>Text.</text></paragraph></quoted-block></section>`, []string{}},
		{"appendix titles and malformed cites are skipped", `<section><text>Rule 1 (` +
			xref("usc/5a/1", "5 U.S.C. App. 1") + `; ` + xref("usc/42", "42 U.S.C.") + `; ` +
			xref("statute/1", "1 Stat. 1") + `) is amended.</text></section>`, []string{}},
		{"a citation outside a text cites", `<section><header>Amendments to ` + x42 + `</header></section>`,
			[]string{"/us/usc/t42/s1395 cites"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			refs, err := billtext.ParseLawRefs(bill(tc.body))
			if err != nil {
				t.Fatal(err)
			}
			if got := refSet(refs); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("refs = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestParseLawRefsInstructionCap(t *testing.T) {
	long := strings.Repeat("The Secretary shall carry out this paragraph. ", 200)
	refs, err := billtext.ParseLawRefs(bill(`<section><text>Section 1 (` + xref("usc/42/1395", "42 U.S.C. 1395") +
		`) is amended by adding at the end the following:</text><quoted-block><paragraph><text>` + long +
		`</text></paragraph></quoted-block></section>`))
	if err != nil || len(refs) != 1 {
		t.Fatalf("ParseLawRefs = %v, %v", refs, err)
	}
	if n := len(refs[0].Instruction); n > billtext.MaxInstruction || n < billtext.MaxInstruction-100 {
		t.Errorf("instruction is %d bytes, want just under %d", n, billtext.MaxInstruction)
	}
}

func TestParseLawRefsResolution(t *testing.T) {
	refs, err := billtext.ParseLawRefs([]byte(`<resolution><resolution-body><section><text>Section 3 (` +
		xref("usc/2/1", "2 U.S.C. 1") + `) is amended.</text></section></resolution-body></resolution>`))
	if err != nil {
		t.Fatal(err)
	}
	if got := refSet(refs); !reflect.DeepEqual(got, []string{"/us/usc/t2/s1 amends"}) {
		t.Errorf("refs = %q", got)
	}
}

func TestParseLawRefsErrors(t *testing.T) {
	for name, data := range map[string]string{
		"empty":     "  ",
		"not XML":   "SECTION 1. Short title.",
		"html page": "<html><body><p>Page not found</p></body></html>",
		"truncated": "<bill><legis-body><section><text>Section 1 (<external-xref",
	} {
		if refs, err := billtext.ParseLawRefs([]byte(data)); err == nil {
			t.Errorf("%s: ParseLawRefs = %v, want an error", name, refs)
		}
	}
}

func TestUSCSectionID(t *testing.T) {
	for _, tc := range []struct{ title, section, want string }{
		{"42", "1395w-4", "/us/usc/t42/s1395w-4"},
		{"42", "1395w–4", "/us/usc/t42/s1395w-4"},
		{" 05", " 552 ", "/us/usc/t5/s552"},
		{"5a", "1", ""},
		{"42", "", ""},
		{"42", "1395 note", ""},
		{"42", strings.Repeat("1", 200), ""},
	} {
		if got := billtext.USCSectionID(tc.title, tc.section); got != tc.want {
			t.Errorf("USCSectionID(%q, %q) = %q, want %q", tc.title, tc.section, got, tc.want)
		}
	}
}
