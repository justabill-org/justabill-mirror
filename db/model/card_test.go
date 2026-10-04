package model_test

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/model"
)

// hr187CRS is H.R. 187's (119th) Public Law CRS summary as the pipeline stores it: it opens with
// the short title.
const hr187CRS = "Modernizing Access to our Public Waters Act or the MAPWaters Act of 2025\n\n" +
	"This act directs the Forest Service and the Department of the Interior to standardize and publish data " +
	"relating to the public's access to federal waterways for recreational use.\n\n" +
	"(Sec. 3) The Forest Service and Interior must jointly develop and adopt interagency standards."

func TestCRSLead(t *testing.T) {
	tests := []struct {
		name, text, want string
	}{
		{"short title first", hr187CRS, "This act directs the Forest Service and the Department of the Interior to " +
			"standardize and publish data relating to the public's access to federal waterways for recreational use."},
		{
			"sentence first",
			"This bill authorizes the Capitol Police Board to waive the mandatory retirement age.\n\nMore.",
			"This bill authorizes the Capitol Police Board to waive the mandatory retirement age.",
		},
		{
			"ends in a colon",
			"Short Title Act\n\nThis bill does the following things in law:\n\n- one",
			"This bill does the following things in law:",
		},
		{
			"ends in a parenthesis",
			"Title\n\nThe bill amends the law (Sec. 2 of the act as amended)",
			"The bill amends the law (Sec. 2 of the act as amended)",
		},
		{"six words is a title", "Title\n\nOne two three four five six.", "Title"},
		{"no sentence", "Short Title Act\n\nAnother heading", "Short Title Act"},
		{
			"blank lines with spaces",
			"  \n \nShort Title Act\n  \n\tThis bill does seven things in the law.  ",
			"This bill does seven things in the law.",
		},
		{"empty", " \n\n ", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := model.CRSLead(tt.text); got != tt.want {
				t.Errorf("CRSLead() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestCRSParagraphs(t *testing.T) {
	got := model.CRSParagraphs("a\n\nb \n \n\n c\nd\n\n")
	if want := []string{"a", "b", "c\nd"}; !reflect.DeepEqual(got, want) {
		t.Errorf("CRSParagraphs() = %q, want %q", got, want)
	}
	if empty := model.CRSParagraphs(""); len(empty) != 0 {
		t.Errorf("CRSParagraphs(\"\") = %q, want none", empty)
	}
}

func TestParseLawAction(t *testing.T) {
	tests := []struct {
		text, lawType, number string
		ok                    bool
	}{
		{"Became Public Law No: 119-95.", model.LawTypePublic, "119-95", true},
		{"Became Private Law No: 118-1.", model.LawTypePrivate, "118-1", true},
		{"Signed by President.", "", "", false},
		{"Became Public Law No: .", "", "", false},
		{"Presented to President.", "", "", false},
	}
	for _, tt := range tests {
		lawType, number, ok := model.ParseLawAction(tt.text)
		if lawType != tt.lawType || number != tt.number || ok != tt.ok {
			t.Errorf("ParseLawAction(%q) = %q, %q, %v; want %q, %q, %v",
				tt.text, lawType, number, ok, tt.lawType, tt.number, tt.ok)
		}
	}
	if got, want := model.LawActionPattern(), `Became (Public|Private) Law No:\s*(\d+-\d+)`; got != want {
		t.Errorf("LawActionPattern() = %q, want %q", got, want)
	}
}

func TestBillCardFactsEmpty(t *testing.T) {
	if !(model.BillCardFacts{Passage: []model.PassageEntry{}}).Empty() {
		t.Error("no facts: Empty() = false")
	}
	for name, f := range map[string]model.BillCardFacts{
		"crs":         {CRS: &model.CardCRS{}},
		"passage":     {Passage: []model.PassageEntry{{}}},
		"enacted":     {Enacted: &model.Enactment{}},
		"law changes": {LawChangeCount: 1},
	} {
		if f.Empty() {
			t.Errorf("%s: Empty() = true", name)
		}
	}
}

// TestBillCardFactsJSON pins the shape the API serves: null crs and enacted, an empty passage
// list, and no tallies on an unrecorded passage.
func TestBillCardFactsJSON(t *testing.T) {
	date := time.Date(2025, time.December, 16, 0, 0, 0, 0, time.UTC)
	q := "Voice Vote: Passed Senate without amendment by Voice Vote."
	got, err := json.Marshal(model.BillCardFacts{
		Passage: []model.PassageEntry{{Chamber: "Senate", Method: model.PassageVoice, Date: date, Question: &q}},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := `{"crs":null,"passage":[{"chamber":"Senate","method":"voice","date":"2025-12-16T00:00:00Z",` +
		`"question":"Voice Vote: Passed Senate without amendment by Voice Vote."}],"enacted":null,"law_change_count":0}`
	if string(got) != want {
		t.Errorf("json = %s\nwant   %s", got, want)
	}
}
