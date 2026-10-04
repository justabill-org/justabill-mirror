package xmlparse_test

import (
	"os"
	"slices"
	"testing"

	"github.com/justabill-org/justabill/pipeline/internal/xmlparse"
)

// The fixture is the real feed of 2026-09-27, trimmed to five senators. Its names carry
// trailing spaces ("Tammy ") and no accents ("Lujan").
func TestParseSenateMembers(t *testing.T) {
	data, err := os.ReadFile("testdata/senators/cvc_member_data.xml")
	if err != nil {
		t.Fatal(err)
	}
	got, err := xmlparse.ParseSenateMembers(data)
	if err != nil {
		t.Fatal(err)
	}
	want := []xmlparse.SenateMember{
		{LISID: "S428", BioguideID: "A000382", FirstName: "Angela D.", LastName: "Alsobrooks", State: "MD"},
		{LISID: "S354", BioguideID: "B001230", FirstName: "Tammy", LastName: "Baldwin", State: "WI"},
		{LISID: "S409", BioguideID: "L000570", FirstName: "Ben Ray", LastName: "Lujan", State: "NM"},
		{LISID: "S313", BioguideID: "S000033", FirstName: "Bernard", LastName: "Sanders", State: "VT"},
		{LISID: "S270", BioguideID: "S000148", FirstName: "Charles E.", LastName: "Schumer", State: "NY"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("members =\n%+v\nwant\n%+v", got, want)
	}
}

func TestParseSenateMembers_Errors(t *testing.T) {
	if _, err := xmlparse.ParseSenateMembers([]byte("<senators><senator>")); err == nil {
		t.Error("truncated XML parsed without error")
	}
	got, err := xmlparse.ParseSenateMembers([]byte("<senators></senators>"))
	if err != nil || len(got) != 0 {
		t.Errorf("empty feed = %v, %v; want no members", got, err)
	}
}

func TestParseSenateVote_SeparateNames(t *testing.T) {
	r := loadSenate(t, "senate_vote_119_1_00372.xml")
	i := slices.IndexFunc(r.Votes, func(v xmlparse.IndividualVote) bool { return v.MemberID == "S409" })
	if i < 0 {
		t.Fatal("S409 not in the fixture")
	}
	if v := r.Votes[i]; v.FirstName != "Ben" || v.LastName != "Lujan" || v.State != "NM" {
		t.Errorf("S409 = %+v, want Ben / Lujan / NM", v)
	}
}
