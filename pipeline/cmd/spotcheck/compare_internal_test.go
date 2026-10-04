package main

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
)

// storedFrom builds the stored roll call the pipeline would write for rc: canonical positions,
// the clerk's wall-clock date as UTC. lis maps a Senate LIS ID to the member's bioguide ID.
func storedFrom(rc *officialRollCall, lis map[string]string) *repository.StoredRollCall {
	date, _ := time.Parse(isoDate, rc.Date)
	s := &repository.StoredRollCall{
		VoteID:    "v",
		VoteDate:  date.Add(14 * time.Hour),
		Question:  new(rc.Question),
		Result:    new(rc.Result),
		Yeas:      new(rc.Yeas),
		Nays:      new(rc.Nays),
		Present:   new(rc.Present),
		NotVoting: new(rc.NotVoting),
	}
	if rc.BillID != "" {
		s.BillID = new(rc.BillID)
	}
	for _, id := range sortedKeys(rc.Positions) {
		vote, _ := model.NormalizeMemberVote(rc.Positions[id])
		p := repository.StoredPosition{MemberID: id, Vote: vote}
		if rc.Chamber == chamberSenate {
			p = repository.StoredPosition{MemberID: lis[id], LISID: id, Vote: vote}
		}
		s.Positions = append(s.Positions, p)
	}
	return s
}

func houseOfficial() *officialRollCall {
	return &officialRollCall{
		Chamber: chamberHouse, Congress: 119, Session: 1, Number: 21,
		Question: "On Agreeing to the Resolution", Result: "Passed", Date: "2025-01-22",
		Yeas: 1, Nays: 1, NotVoting: 1, BillID: "hres-119-53",
		Positions: map[string]string{"A000370": "No", "A000055": "Aye", "B001298": "Not Voting"},
	}
}

func senateOfficial() *officialRollCall {
	return &officialRollCall{
		Chamber: chamberSenate, Congress: 119, Session: 1, Number: 72,
		Question: "On the Amendment", Result: "Amendment Rejected (1-1)", Date: "2025-02-20",
		Yeas: 1, Nays: 1, BillID: "sconres-119-7",
		Positions: map[string]string{"S428": "Yea", "S429": "Nay"},
	}
}

func senateLIS() map[string]string { return map[string]string{"S428": "A000382", "S429": "B001299"} }

func TestCompareMatches(t *testing.T) {
	c := newComparer()
	for _, off := range []*officialRollCall{houseOfficial(), senateOfficial()} {
		if diffs := c.compare(off, storedFrom(off, senateLIS())); diffs != nil {
			t.Errorf("%s: diffs %v, want none", off.Chamber, diffs)
		}
	}
}

func TestCompareDifferences(t *testing.T) {
	tests := []struct {
		name   string
		off    func() *officialRollCall
		change func(s *repository.StoredRollCall)
		want   []string
	}{
		{
			name: "yea flipped to nay",
			off:  houseOfficial,
			change: func(s *repository.StoredRollCall) {
				for i := range s.Positions {
					if s.Positions[i].MemberID == "A000055" {
						s.Positions[i].Vote = model.VoteNay
					}
				}
			},
			want: []string{`member A000055: official "Yea", stored "Nay"`},
		},
		{
			name:   "missing and extra members",
			off:    houseOfficial,
			change: func(s *repository.StoredRollCall) { s.Positions[0].MemberID = "Z000001" },
			want: []string{
				`member A000055: official "Yea", not stored`,
				`member Z000001: stored "Yea", not in the official record`,
			},
		},
		{
			name:   "raw clerk value stored",
			off:    houseOfficial,
			change: func(s *repository.StoredRollCall) { s.Positions[1].Vote = "No" },
			want:   []string{`member A000370: official "Nay", stored "No"`},
		},
		{
			name: "fields and totals",
			off:  houseOfficial,
			change: func(s *repository.StoredRollCall) {
				s.Question = new("On Passage")
				s.Result = nil
				s.BillID = nil
				s.VoteDate = time.Date(2025, time.January, 21, 12, 0, 0, 0, time.UTC)
				s.Yeas = new(2)
				s.Present = nil
			},
			want: []string{
				`question: official "On Agreeing to the Resolution", stored "On Passage"`,
				`result: official "Passed", stored ""`,
				`bill: official "hres-119-53", stored ""`,
				"date: official 2025-01-22, stored 2025-01-21",
				"yeas: official 1, stored 2",
				"present: official 0, stored none",
			},
		},
		{
			name:   "senator without an LIS ID",
			off:    senateOfficial,
			change: func(s *repository.StoredRollCall) { s.Positions[1].LISID = "" },
			want: []string{
				`member B001299: stored "Nay", but the member has no LIS ID`,
				`member S429: official "Nay", not stored`,
			},
		},
	}
	c := newComparer()
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			off := tt.off()
			stored := storedFrom(off, senateLIS())
			tt.change(stored)
			got := c.compare(off, stored)
			if !slices.Equal(got, tt.want) {
				t.Errorf("diffs\n%s\nwant\n%s", strings.Join(got, "\n"), strings.Join(tt.want, "\n"))
			}
		})
	}
}

func TestCompareNotStored(t *testing.T) {
	if got := newComparer().compare(houseOfficial(), nil); !slices.Equal(got, []string{"not stored"}) {
		t.Errorf("got %v", got)
	}
}

// A vote stored as the true instant (an 11 PM Eastern vote is the next day in UTC) still
// matches the clerk's date.
func TestCompareDateInEastern(t *testing.T) {
	c := newComparer()
	if c.eastern == time.UTC {
		t.Skip("no zone database")
	}
	off := senateOfficial()
	stored := storedFrom(off, senateLIS())
	stored.VoteDate = time.Date(2025, time.February, 21, 4, 9, 0, 0, time.UTC) // 11:09 PM EST on the 20th
	if diffs := c.compare(off, stored); diffs != nil {
		t.Errorf("diffs %v, want none", diffs)
	}
}
