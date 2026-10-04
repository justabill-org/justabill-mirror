package billtext_test

import (
	"reflect"
	"testing"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/pipeline/internal/billtext"
)

func TestParseMODSCitations(t *testing.T) {
	// H.R. 9869's MODS lists 5 U.S.C. 8103 twice (whole and "(a)"), chapter 81, and 15 U.S.C.
	// 9401(3), which the bill's XML doesn't tag.
	refs, err := billtext.ParseMODSCitations(readFixture(t, "BILLS-119hr9869ih.mods.xml"))
	if err != nil {
		t.Fatal(err)
	}
	want := []billtext.LawRef{
		{SectionID: "/us/usc/t15/s9401", Kind: model.LawRefCites, CiteText: "15 U.S.C. 9401(3)", SubsectionPath: "(3)"},
		{SectionID: "/us/usc/t5/s8103", Kind: model.LawRefCites, CiteText: "5 U.S.C. 8103; 5 U.S.C. 8103(a)"},
	}
	if !reflect.DeepEqual(refs, want) {
		t.Errorf("refs = %+v\nwant %+v", refs, want)
	}
}

func TestParseMODSCitationsEdgeCases(t *testing.T) {
	refs, err := billtext.ParseMODSCitations([]byte(`<mods xmlns="http://www.loc.gov/mods/v3"><extension>
		<USCode title="10"><section number="4271" detail="note"/><chapter number="3"/></USCode>
		<USCode title="5a"><section number="1"/></USCode>
		<section number="99"/>
		<USCode title="42"><section number="1395w–4" detail="(t)"/></USCode></extension></mods>`))
	if err != nil {
		t.Fatal(err)
	}
	want := []billtext.LawRef{
		{SectionID: "/us/usc/t10/s4271/note", Kind: model.LawRefCites, CiteText: "10 U.S.C. 4271 note"},
		{SectionID: "/us/usc/t42/s1395w-4", Kind: model.LawRefCites, CiteText: "42 U.S.C. 1395w-4(t)",
			SubsectionPath: "(t)"},
	}
	if !reflect.DeepEqual(refs, want) {
		t.Errorf("refs = %+v\nwant %+v", refs, want)
	}

	for name, data := range map[string]string{
		"empty":     "",
		"not MODS":  "<bill><legis-body/></bill>",
		"truncated": "<mods><extension><USCode",
	} {
		if bad, parseErr := billtext.ParseMODSCitations([]byte(data)); parseErr == nil {
			t.Errorf("%s: ParseMODSCitations = %v, want an error", name, bad)
		}
	}
}

func TestSupplementLawRefs(t *testing.T) {
	xml := []billtext.LawRef{
		{SectionID: "/us/usc/t5/s8103", Kind: model.LawRefAmends, Instruction: "is amended"},
		{SectionID: "nonusc:Section 2 of the X Act", Kind: model.LawRefCites},
	}
	mods := []billtext.LawRef{
		{SectionID: "/us/usc/t5/s8103", Kind: model.LawRefCites, CiteText: "5 U.S.C. 8103"},
		{SectionID: "/us/usc/t15/s9401", Kind: model.LawRefCites, CiteText: "15 U.S.C. 9401(3)"},
	}
	got := refSet(billtext.SupplementLawRefs(xml, mods))
	want := []string{
		"/us/usc/t15/s9401 cites", "/us/usc/t5/s8103 amends", "nonusc:Section 2 of the X Act cites",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("refs = %q, want %q", got, want)
	}
}

func TestMergeLawRefs(t *testing.T) {
	const sec = "/us/usc/t42/s1395"
	got := billtext.MergeLawRefs([]billtext.LawRef{
		{SectionID: sec, Kind: model.LawRefAmends, CiteText: "a", SubsectionPath: "(a)", Instruction: "one"},
		{SectionID: sec, Kind: model.LawRefCites, CiteText: "cited"},
		{SectionID: sec, Kind: model.LawRefAmends, CiteText: "b", SubsectionPath: "(b)", Instruction: "two",
			BillSection: "S3"},
		{SectionID: sec, Kind: model.LawRefAmends, CiteText: "a", SubsectionPath: "(a)", Instruction: "one"},
		{SectionID: sec, Kind: model.LawRefRepeals, CiteText: "c", SubsectionPath: "(c)", Instruction: "three"},
		{SectionID: sec, Kind: model.LawRefRepeals, CiteText: "d", Instruction: "four"},
	})
	want := []billtext.LawRef{
		{SectionID: sec, Kind: model.LawRefAmends, CiteText: "a; b", SubsectionPath: "(a), (b)",
			Instruction: "one\n\ntwo", BillSection: "S3"},
		// One citation of the whole section makes the path empty.
		{SectionID: sec, Kind: model.LawRefRepeals, CiteText: "c; d", Instruction: "three\n\nfour"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("merged = %+v\nwant %+v", got, want)
	}
}
