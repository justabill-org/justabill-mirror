package attendance_test

import (
	"slices"
	"testing"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/attendance"
)

const (
	house  = "House"
	senate = "Senate"
)

// roll is a roll call that isn't a quorum call.
func roll(chamber, id string) repository.RollCallRef {
	return repository.RollCallRef{VoteID: id, Chamber: chamber, Question: "On Passage"}
}

// row is a computed row for congress 119.
func row(
	member, chamber string, eligible, missed, present, members int64, median float64,
) repository.MemberAttendanceRow {
	return repository.MemberAttendanceRow{
		MemberID: member, Congress: 119, Chamber: chamber, Eligible: eligible, Missed: missed, Present: present,
		ChamberMembers: members, ChamberMedianPct: median,
	}
}

func count(member, chamber, vote string, n int64) repository.MemberVoteCount {
	return repository.MemberVoteCount{MemberID: member, Chamber: chamber, Vote: vote, Count: n}
}

// listed counts a member listed on n roll calls in a chamber, missed of them Not Voting.
func listed(member, chamber string, n, missed int64) []repository.MemberVoteCount {
	return []repository.MemberVoteCount{
		count(member, chamber, "Yea", n-missed),
		count(member, chamber, "Not Voting", missed),
	}
}

func bothChambers() []repository.RollCallRef {
	return []repository.RollCallRef{roll(house, "house-119-1-2"), roll(senate, "senate-119-1-1")}
}

func TestQuorumCallIDs(t *testing.T) {
	rolls := []repository.RollCallRef{
		{VoteID: "house-119-1-1", Chamber: house, Question: "Call by States"},
		{VoteID: "house-119-1-2", Chamber: house, Question: "Election of the Speaker"},
		{VoteID: "house-119-1-9", Chamber: house, Question: "Call of the House"},
		{VoteID: "house-119-1-10", Chamber: house, Question: ""},
		{VoteID: "senate-119-1-1", Chamber: senate, Question: "On Cloture on the Motion to Proceed S. 5"},
	}
	want := []string{"house-119-1-1", "house-119-1-9"}
	if got := attendance.QuorumCallIDs(rolls); !slices.Equal(got, want) {
		t.Errorf("QuorumCallIDs = %v, want %v", got, want)
	}
	if got := attendance.QuorumCallIDs(nil); len(got) != 0 {
		t.Errorf("QuorumCallIDs(nil) = %v, want none", got)
	}
}

func TestCompute(t *testing.T) {
	tests := []struct {
		name   string
		rolls  []repository.RollCallRef
		counts []repository.MemberVoteCount
		want   []repository.MemberAttendanceRow
	}{
		{
			// Aye and No are the House's recorded-vote forms; a Speaker candidate's name is a vote cast.
			name:  "not voting is missed, present is present, anything else is a vote",
			rolls: bothChambers(),
			counts: []repository.MemberVoteCount{
				count("A000001", house, "Aye", 3),
				count("A000001", house, "Jeffries", 1),
				count("A000001", house, "No", 3),
				count("A000001", house, "Not Voting", 2),
				count("A000001", house, "Present", 1),
			},
			want: []repository.MemberAttendanceRow{row("A000001", house, 10, 2, 1, 1, 20)},
		},
		{
			name:  "chamber medians: odd and even counts, one chamber at a time",
			rolls: bothChambers(),
			counts: slices.Concat(
				listed("H000003", house, 100, 10),
				listed("H000001", house, 100, 0),
				listed("H000002", house, 100, 4),
				listed("S000001", senate, 100, 1),
				listed("S000002", senate, 100, 3),
			),
			want: []repository.MemberAttendanceRow{
				row("H000001", house, 100, 0, 0, 3, 4),
				row("H000002", house, 100, 4, 0, 3, 4),
				row("H000003", house, 100, 10, 0, 3, 4),
				row("S000001", senate, 100, 1, 0, 2, 2),
				row("S000002", senate, 100, 3, 0, 2, 2),
			},
		},
		{
			// A member sworn in mid-congress, or a senator whose roll calls are still being fetched
			// again (#66): the share is over the roll calls they're listed on, and an absent row is
			// never a missed vote.
			name:  "a member listed on fewer roll calls is counted on their own",
			rolls: bothChambers(),
			counts: slices.Concat(
				listed("H000001", house, 100, 2),
				listed("H000002", house, 30, 3),
				listed("S000001", senate, 50, 1),
				listed("S000002", senate, 7, 0),
			),
			want: []repository.MemberAttendanceRow{
				row("H000001", house, 100, 2, 0, 2, 6),
				row("H000002", house, 30, 3, 0, 2, 6),
				row("S000001", senate, 50, 1, 0, 2, 1),
				row("S000002", senate, 7, 0, 0, 2, 1),
			},
		},
		{
			name:  "a congress with only one chamber's roll calls has rows for that chamber only",
			rolls: []repository.RollCallRef{roll(senate, "senate-119-1-1")},
			counts: slices.Concat(
				listed("S000001", senate, 10, 1),
				listed("S000002", senate, 10, 3),
			),
			want: []repository.MemberAttendanceRow{
				row("S000001", senate, 10, 1, 0, 2, 20),
				row("S000002", senate, 10, 3, 0, 2, 20),
			},
		},
		{
			// The House's only roll call is a quorum call, so a House count can't be eligible:
			// the member gets no row and stays out of the median, which is the other member's.
			name: "no eligible roll call: no row and no share in the median",
			rolls: []repository.RollCallRef{
				{VoteID: "house-119-1-1", Chamber: house, Question: "Call by States"},
				roll(senate, "senate-119-1-1"),
			},
			counts: slices.Concat(
				[]repository.MemberVoteCount{count("H000001", house, "Present", 1)},
				listed("S000001", senate, 4, 1),
			),
			want: []repository.MemberAttendanceRow{row("S000001", senate, 4, 1, 0, 1, 25)},
		},
		{
			name:   "no roll calls",
			counts: listed("H000001", house, 10, 1),
		},
		{
			name:  "no counts",
			rolls: bothChambers(),
		},
		{
			name:  "a member in both chambers in one congress has a row in each",
			rolls: bothChambers(),
			counts: slices.Concat(
				listed("M000001", house, 20, 2),
				listed("M000001", senate, 10, 5),
			),
			want: []repository.MemberAttendanceRow{
				row("M000001", house, 20, 2, 0, 1, 10),
				row("M000001", senate, 10, 5, 0, 1, 50),
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := attendance.Compute(119, tt.rolls, tt.counts)
			if !slices.Equal(got, tt.want) {
				t.Errorf("Compute =\n%+v\nwant\n%+v", got, tt.want)
			}
		})
	}
}

func TestMedian(t *testing.T) {
	tests := []struct {
		name   string
		shares []float64
		want   float64
	}{
		{"none", nil, 0},
		{"one", []float64{7.5}, 7.5},
		{"odd count, unsorted", []float64{10, 0, 4}, 4},
		{"even count: the mean of the two middle values", []float64{3, 1}, 2},
		{"even count, unsorted", []float64{9, 1, 5, 2}, 3.5},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := attendance.Median(tt.shares); got != tt.want {
				t.Errorf("Median(%v) = %v, want %v", tt.shares, got, tt.want)
			}
		})
	}
}

func TestMedianLeavesItsInputAlone(t *testing.T) {
	shares := []float64{10, 0, 4}
	attendance.Median(shares)
	if want := []float64{10, 0, 4}; !slices.Equal(shares, want) {
		t.Errorf("Median reordered its input to %v, want %v", shares, want)
	}
}
