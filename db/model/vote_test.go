package model_test

import (
	"testing"

	"github.com/justabill-org/justabill/db/model"
)

func TestNormalizeMemberVote(t *testing.T) {
	tests := []struct {
		raw    string
		want   string
		wantOK bool
	}{
		{"Yea", model.VoteYea, true},
		{"YEA", model.VoteYea, true},
		{"Aye", model.VoteYea, true},
		{" aye ", model.VoteYea, true},
		{"Nay", model.VoteNay, true},
		{"No", model.VoteNay, true},
		{"no\n", model.VoteNay, true},
		{"Present", model.VotePresent, true},
		{"Present, Giving Live Pair", model.VotePresent, true},
		{"present,  giving live pair", model.VotePresent, true},
		{"Not Voting", model.VoteNotVoting, true},
		{"not  voting", model.VoteNotVoting, true},
		{"NOT VOTING", model.VoteNotVoting, true},
		// Values that aren't a yes/no/present/absent pass through trimmed.
		{"Johnson (LA)", "Johnson (LA)", false},
		{" Jeffries ", "Jeffries", false},
		{"Guilty", "Guilty", false},
		{"Not Guilty", "Not Guilty", false},
		{"", "", false},
	}
	for _, tt := range tests {
		got, ok := model.NormalizeMemberVote(tt.raw)
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("NormalizeMemberVote(%q) = (%q, %v), want (%q, %v)", tt.raw, got, ok, tt.want, tt.wantOK)
		}
	}
}
