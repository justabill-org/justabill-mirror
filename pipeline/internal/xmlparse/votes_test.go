package xmlparse_test

import (
	"os"
	"testing"

	"github.com/justabill-org/justabill/pipeline/internal/xmlparse"
)

func TestParseHouseVote(t *testing.T) {
	data, err := os.ReadFile("testdata/house_vote.xml")
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
	if result.RollNumber != 23 {
		t.Errorf("expected roll number 23, got %d", result.RollNumber)
	}
	if result.Question != "On Passage" {
		t.Errorf("expected question 'On Passage', got %q", result.Question)
	}
	if result.Result != "Passed" {
		t.Errorf("expected result 'Passed', got %q", result.Result)
	}
	if len(result.Votes) != 3 {
		t.Fatalf("expected 3 votes, got %d", len(result.Votes))
	}

	voteMap := make(map[string]string)
	for _, v := range result.Votes {
		voteMap[v.MemberID] = v.Vote
	}
	if voteMap["A000001"] != "Yea" {
		t.Errorf("expected A000001 vote Yea, got %s", voteMap["A000001"])
	}
	if voteMap["B000002"] != "Nay" {
		t.Errorf("expected B000002 vote Nay, got %s", voteMap["B000002"])
	}
	if voteMap["C000003"] != "Not Voting" {
		t.Errorf("expected C000003 vote 'Not Voting', got %s", voteMap["C000003"])
	}

	if result.VoteDate != "15-Jan-2025" {
		t.Errorf("expected vote date '15-Jan-2025', got %q", result.VoteDate)
	}
	if result.YeaTotal != 220 {
		t.Errorf("expected yea total 220, got %d", result.YeaTotal)
	}
	if result.NayTotal != 210 {
		t.Errorf("expected nay total 210, got %d", result.NayTotal)
	}
	if result.PresentTotal != 1 {
		t.Errorf("expected present total 1, got %d", result.PresentTotal)
	}
	if result.NotVotingTotal != 4 {
		t.Errorf("expected not voting total 4, got %d", result.NotVotingTotal)
	}
}

func TestParseHouseVote_InvalidXML(t *testing.T) {
	_, err := xmlparse.ParseHouseVote([]byte("not xml at all"))
	if err == nil {
		t.Error("expected error for invalid XML")
	}
}

func TestParseSenateVote(t *testing.T) {
	data, err := os.ReadFile("testdata/senate_vote.xml")
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
	if result.VoteNumber != 42 {
		t.Errorf("expected vote number 42, got %d", result.VoteNumber)
	}
	if result.Question != "On the Motion" {
		t.Errorf("expected question 'On the Motion', got %q", result.Question)
	}
	if result.Result != "Motion Agreed to" {
		t.Errorf("expected result 'Motion Agreed to', got %q", result.Result)
	}
	if len(result.Votes) != 3 {
		t.Fatalf("expected 3 votes, got %d", len(result.Votes))
	}

	if result.VoteDate != "January 20, 2025, 03:15 PM" {
		t.Errorf("expected vote date 'January 20, 2025, 03:15 PM', got %q", result.VoteDate)
	}
	if result.VoteTitle != "A bill to do something important" {
		t.Errorf("expected vote title, got %q", result.VoteTitle)
	}
	if result.YeaTotal != 55 {
		t.Errorf("expected yea total 55, got %d", result.YeaTotal)
	}
	if result.NayTotal != 43 {
		t.Errorf("expected nay total 43, got %d", result.NayTotal)
	}
	if result.PresentTotal != 1 {
		t.Errorf("expected present total 1, got %d", result.PresentTotal)
	}
	if result.AbsentTotal != 1 {
		t.Errorf("expected absent total 1, got %d", result.AbsentTotal)
	}
}

func TestParseSenateVote_InvalidXML(t *testing.T) {
	_, err := xmlparse.ParseSenateVote([]byte("garbage"))
	if err == nil {
		t.Error("expected error for invalid XML")
	}
}
