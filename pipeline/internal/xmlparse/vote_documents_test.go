package xmlparse_test

import (
	"os"
	"testing"

	"github.com/justabill-org/justabill/pipeline/internal/xmlparse"
)

func loadSenate(t *testing.T, name string) *xmlparse.SenateVoteResult {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	result, err := xmlparse.ParseSenateVote(data)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return result
}

func loadHouse(t *testing.T, name string) *xmlparse.HouseVoteResult {
	t.Helper()
	data, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}
	result, err := xmlparse.ParseHouseVote(data)
	if err != nil {
		t.Fatalf("parse %s: %v", name, err)
	}
	return result
}

// The Senate fixtures are real roll calls from the 119th Congress, trimmed to a few members.
func TestParseSenateVote_Document(t *testing.T) {
	tests := []struct {
		file        string
		wantDoc     xmlparse.SenateDocument
		wantAmended string
	}{
		{
			file:    "senate_vote_119_1_00372.xml", // passage of H.R. 1
			wantDoc: xmlparse.SenateDocument{Congress: 119, Type: "H.R.", Number: "1"},
		},
		{
			file:        "senate_vote_119_1_00616.xml", // S.Amdt. 3937 to H.R. 5371
			wantDoc:     xmlparse.SenateDocument{Congress: 119, Type: "S.Amdt."},
			wantAmended: "H.R. 5371",
		},
		{
			file:    "senate_vote_119_1_00607.xml", // nomination PN25-37
			wantDoc: xmlparse.SenateDocument{Congress: 119, Type: "PN", Number: "25-37"},
		},
		{
			file:        "senate_vote_119_1_00072.xml", // S.Amdt. 407 to S.Con.Res. 7
			wantDoc:     xmlparse.SenateDocument{Congress: 119, Type: "S.Amdt."},
			wantAmended: "S.Con.Res. 7",
		},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			result := loadSenate(t, tt.file)
			if result.Document != tt.wantDoc {
				t.Errorf("Document = %+v, want %+v", result.Document, tt.wantDoc)
			}
			if result.AmendmentToDocument != tt.wantAmended {
				t.Errorf("AmendmentToDocument = %q, want %q", result.AmendmentToDocument, tt.wantAmended)
			}
		})
	}
}

func TestParseSenateVote_OldFixtureHasNoDocument(t *testing.T) {
	result := loadSenate(t, "senate_vote.xml")
	if result.Document != (xmlparse.SenateDocument{}) || result.AmendmentToDocument != "" {
		t.Errorf("document = %+v, amended = %q; want both empty", result.Document, result.AmendmentToDocument)
	}
}

// The House fixtures keep the clerk's raw values; the sync normalizes them at ingest.
func TestParseHouseVote_RawValues(t *testing.T) {
	tests := []struct {
		file         string
		wantVoteType string
		wantLegisNum string
		wantVotes    []string
	}{
		{"house_2025_roll021_recorded.xml", "RECORDED VOTE", "H RES 53", []string{"No", "Aye", "Not Voting"}},
		{"house_2025_roll002_speaker.xml", "YEA-AND-NAY", "", []string{"Jeffries", "Johnson (LA)", "Emmer"}},
		{"house_2025_roll001_quorum.xml", "QUORUM", "QUORUM", []string{"Present", "Not Voting"}},
	}
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			result := loadHouse(t, tt.file)
			if result.VoteType != tt.wantVoteType {
				t.Errorf("VoteType = %q, want %q", result.VoteType, tt.wantVoteType)
			}
			if result.LegisNum != tt.wantLegisNum {
				t.Errorf("LegisNum = %q, want %q", result.LegisNum, tt.wantLegisNum)
			}
			if len(result.Votes) != len(tt.wantVotes) {
				t.Fatalf("got %d votes, want %d", len(result.Votes), len(tt.wantVotes))
			}
			for i, want := range tt.wantVotes {
				if result.Votes[i].Vote != want {
					t.Errorf("Votes[%d].Vote = %q, want %q", i, result.Votes[i].Vote, want)
				}
			}
		})
	}
}
