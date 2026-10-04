package xmlparse_test

import (
	"os"
	"testing"

	"github.com/justabill-org/justabill/pipeline/internal/xmlparse"
)

func loadHouseRoll014(t *testing.T) *xmlparse.HouseVoteResult {
	t.Helper()
	data, err := os.ReadFile("testdata/real_house_roll014.xml")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	result, err := xmlparse.ParseHouseVote(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	return result
}

func loadSenateVote001(t *testing.T) *xmlparse.SenateVoteResult {
	t.Helper()
	data, err := os.ReadFile("testdata/real_senate_vote001.xml")
	if err != nil {
		t.Fatalf("read fixture: %v", err)
	}

	result, err := xmlparse.ParseSenateVote(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result == nil {
		t.Fatal("expected non-nil result")
	}
	return result
}

func TestParseHouseVote_RealRoll014_Metadata(t *testing.T) {
	result := loadHouseRoll014(t)

	if result.Congress != 119 {
		t.Errorf("expected congress 119, got %d", result.Congress)
	}
	if result.RollNumber != 14 {
		t.Errorf("expected roll number 14, got %d", result.RollNumber)
	}
	if result.Question != "On Motion to Suspend the Rules and Pass" {
		t.Errorf("question = %q", result.Question)
	}
	if result.Result != "Passed" {
		t.Errorf("result = %q", result.Result)
	}
	if result.VoteDate != "15-Jan-2025" {
		t.Errorf("expected vote date '15-Jan-2025', got %q", result.VoteDate)
	}
	if result.LegisNum != "H R 144" {
		t.Errorf("expected legis num 'H R 144', got %q", result.LegisNum)
	}
	if result.VoteDesc != "Tennessee Valley Authority Salary Transparency Act" {
		t.Errorf("expected vote desc, got %q", result.VoteDesc)
	}
}

func TestParseHouseVote_RealRoll014_Totals(t *testing.T) {
	result := loadHouseRoll014(t)

	if result.YeaTotal != 423 {
		t.Errorf("expected yea total 423, got %d", result.YeaTotal)
	}
	if result.NayTotal != 0 {
		t.Errorf("expected nay total 0, got %d", result.NayTotal)
	}
	if result.NotVotingTotal != 10 {
		t.Errorf("expected not voting total 10, got %d", result.NotVotingTotal)
	}
}

func TestParseHouseVote_RealRoll014_Votes(t *testing.T) {
	result := loadHouseRoll014(t)

	if len(result.Votes) != 10 {
		t.Fatalf("expected 10 votes, got %d", len(result.Votes))
	}

	voteMap := make(map[string]string)
	for _, v := range result.Votes {
		voteMap[v.MemberID] = v.Vote
	}
	if voteMap["C001068"] != "Yea" {
		t.Errorf("Cohen (C001068) expected Yea, got %s", voteMap["C001068"])
	}
	if voteMap["G000589"] != "Not Voting" {
		t.Errorf("Good (G000589) expected Not Voting, got %s", voteMap["G000589"])
	}
}

func TestParseHouseVote_RealRoll014_PartyExtraction(t *testing.T) {
	result := loadHouseRoll014(t)

	for _, v := range result.Votes {
		if v.MemberID == "P000197" {
			if v.Party != "D" {
				t.Errorf("Pelosi party = %q, want D", v.Party)
			}
			if v.State != "CA" {
				t.Errorf("Pelosi state = %q, want CA", v.State)
			}
		}
	}
}

func TestParseSenateVote_RealVote001_Metadata(t *testing.T) {
	result := loadSenateVote001(t)

	if result.Congress != 119 {
		t.Errorf("expected congress 119, got %d", result.Congress)
	}
	if result.Session != 1 {
		t.Errorf("expected session 1, got %d", result.Session)
	}
	if result.VoteNumber != 1 {
		t.Errorf("expected vote number 1, got %d", result.VoteNumber)
	}
	if result.Question != "On Cloture on the Motion to Proceed S. 5" {
		t.Errorf("question = %q", result.Question)
	}
	if result.Result != "Cloture on the Motion to Proceed Agreed to" {
		t.Errorf("result = %q", result.Result)
	}
	if result.VoteDate != "January 9, 2025, 02:54 PM" {
		t.Errorf("expected vote date 'January 9, 2025, 02:54 PM', got %q", result.VoteDate)
	}
	if result.VoteTitle != "Motion to Invoke Cloture: Motion to Proceed to S. 5" {
		t.Errorf("expected vote title, got %q", result.VoteTitle)
	}
}

func TestParseSenateVote_RealVote001_Totals(t *testing.T) {
	result := loadSenateVote001(t)

	if result.YeaTotal != 84 {
		t.Errorf("expected yea total 84, got %d", result.YeaTotal)
	}
	if result.NayTotal != 9 {
		t.Errorf("expected nay total 9, got %d", result.NayTotal)
	}
	if result.AbsentTotal != 6 {
		t.Errorf("expected absent total 6, got %d", result.AbsentTotal)
	}
}

func TestParseSenateVote_RealVote001_Votes(t *testing.T) {
	result := loadSenateVote001(t)

	if len(result.Votes) != 10 {
		t.Fatalf("expected 10 votes, got %d", len(result.Votes))
	}

	voteMap := make(map[string]string)
	for _, v := range result.Votes {
		voteMap[v.MemberID] = v.Vote
	}
	if voteMap["S428"] != "Yea" {
		t.Errorf("Alsobrooks expected Yea, got %s", voteMap["S428"])
	}
	if voteMap["S370"] != "Nay" {
		t.Errorf("Booker expected Nay, got %s", voteMap["S370"])
	}
	if voteMap["S313"] != "Nay" {
		t.Errorf("Sanders expected Nay, got %s", voteMap["S313"])
	}
	if voteMap["S288"] != "Not Voting" {
		t.Errorf("Murkowski expected Not Voting, got %s", voteMap["S288"])
	}
}
