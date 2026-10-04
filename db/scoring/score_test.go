package scoring_test

import (
	"testing"

	"github.com/justabill-org/justabill/db/scoring"
)

func pos(bill, vote string) scoring.Candidate {
	return scoring.Candidate{BillID: bill, VoteID: "v-" + bill, Chamber: "House", Vote: vote}
}

func TestScore(t *testing.T) {
	tests := []struct {
		name      string
		userVotes map[string]string
		positions []scoring.Candidate
		compared  int
		matching  int
		absent    int
		wantPct   *float64
		rows      int
	}{
		{
			name:      "matching and mismatching",
			userVotes: map[string]string{"a": "yea", "b": "nay", "c": "yea"},
			positions: []scoring.Candidate{pos("a", "yea"), pos("b", "nay"), pos("c", "nay")},
			compared:  3, matching: 2, wantPct: new(66.67), rows: 3,
		},
		{
			name:      "member not voting or present is shown but not counted",
			userVotes: map[string]string{"a": "yea", "b": "nay", "c": "yea", "d": "nay"},
			positions: []scoring.Candidate{
				pos("a", "yea"), pos("b", scoring.NotVoting), pos("c", scoring.Present), pos("d", scoring.Other),
			},
			compared: 1, matching: 1, absent: 3, wantPct: new(100.0), rows: 4,
		},
		{
			name:      "user skip is never counted",
			userVotes: map[string]string{"a": "skip", "b": "SKIP", "c": "Yea"},
			positions: []scoring.Candidate{pos("a", "yea"), pos("b", "nay"), pos("c", "nay")},
			compared:  1, matching: 0, wantPct: new(0.0), rows: 1,
		},
		{
			name:      "nothing compared: null percentage",
			userVotes: map[string]string{"a": "yea"},
			positions: []scoring.Candidate{pos("a", scoring.NotVoting)},
			compared:  0, absent: 1, wantPct: nil, rows: 1,
		},
		{
			name:      "bills without a position aren't rows",
			userVotes: map[string]string{"a": "yea", "b": "nay"},
			positions: []scoring.Candidate{pos("c", "yea")},
			wantPct:   nil,
		},
		{
			name:      "no user votes",
			positions: []scoring.Candidate{pos("a", "yea")},
		},
		{
			name:      "raw clerk values are normalized",
			userVotes: map[string]string{"a": "yea", "b": "nay"},
			positions: []scoring.Candidate{pos("a", "Aye"), pos("b", "No")},
			compared:  2, matching: 2, wantPct: new(100.0), rows: 2,
		},
		{
			name:      "rounding to two decimals",
			userVotes: map[string]string{"a": "yea", "b": "yea", "c": "yea"},
			positions: []scoring.Candidate{pos("a", "yea"), pos("b", "nay"), pos("c", "nay")},
			compared:  3, matching: 1, wantPct: new(33.33), rows: 3,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := scoring.Score(tt.userVotes, tt.positions)
			if got.Compared != tt.compared || got.Matching != tt.matching || got.MemberAbsent != tt.absent {
				t.Errorf("compared/matching/absent = %d/%d/%d, want %d/%d/%d",
					got.Compared, got.Matching, got.MemberAbsent, tt.compared, tt.matching, tt.absent)
			}
			switch {
			case tt.wantPct == nil && got.AlignmentPct != nil:
				t.Errorf("AlignmentPct = %v, want nil", *got.AlignmentPct)
			case tt.wantPct != nil && (got.AlignmentPct == nil || *got.AlignmentPct != *tt.wantPct):
				t.Errorf("AlignmentPct = %v, want %v", got.AlignmentPct, *tt.wantPct)
			}
			if len(got.Rows) != tt.rows {
				t.Errorf("len(Rows) = %d, want %d", len(got.Rows), tt.rows)
			}
			if got.Rows == nil {
				t.Error("Rows is nil, want an empty slice")
			}
		})
	}
}

func TestScoreRows(t *testing.T) {
	positions := []scoring.Candidate{pos("a", "Aye"), pos("b", "Not Voting"), pos("c", "nay")}
	got := scoring.Score(map[string]string{"a": "nay", "b": "yea", "c": "nay"}, positions)

	want := []struct {
		bill, userVote, memberVote string
		counted, matches           bool
	}{
		{"a", scoring.Nay, scoring.Yea, true, false},
		{"b", scoring.Yea, scoring.NotVoting, false, false},
		{"c", scoring.Nay, scoring.Nay, true, true},
	}
	if len(got.Rows) != len(want) {
		t.Fatalf("got %d rows, want %d", len(got.Rows), len(want))
	}
	for i, w := range want {
		r := got.Rows[i]
		if r.Position.BillID != w.bill || r.UserVote != w.userVote || r.Position.Vote != w.memberVote ||
			r.Counted != w.counted || r.Matches != w.matches {
			t.Errorf("row %d = {%s %s %s counted=%v matches=%v}, want %+v",
				i, r.Position.BillID, r.UserVote, r.Position.Vote, r.Counted, r.Matches, w)
		}
	}
	if positions[0].Vote != "Aye" {
		t.Errorf("Score changed its input: vote %q", positions[0].Vote)
	}
}
