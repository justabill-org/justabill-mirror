// Package attendance applies the attendance rule (scoring.AttendanceRule) to a congress's
// recorded roll calls: each member's eligible, missed and present roll calls per chamber, and
// each chamber's median missed share. It's pure Go with no I/O; the vote sync reads the counts
// and writes the rows (sync.Service.computeAttendance).
//
// Design: docs/design/agent-1742-member-attendance.md.
package attendance

import (
	"cmp"
	"slices"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/scoring"
)

// QuorumCallIDs returns the vote IDs of the roll calls that are quorum calls
// (scoring.IsQuorumCall), in the order of rolls: the ones CountMemberVotes must leave out.
func QuorumCallIDs(rolls []repository.RollCallRef) []string {
	var ids []string
	for _, r := range rolls {
		if scoring.IsQuorumCall(r.Chamber, r.Question) {
			ids = append(ids, r.VoteID)
		}
	}
	return ids
}

// memberKey is one member's roll calls in one chamber.
type memberKey struct {
	member, chamber string
}

// Compute builds a congress's member_attendance rows from its roll calls and the member vote
// counts that CountMemberVotes returned with QuorumCallIDs(rolls) left out. A vote that
// normalizes to not_voting is missed, present is present, and every other value, a Speaker
// candidate's name included, is a vote cast (unlike the scorecard, which gives Other no position).
//
// A member's eligible roll calls are the ones they're listed on, so a member sworn in
// mid-congress, or a senator some of whose roll calls are still to be fetched again, is counted
// over their own rows and never misses a roll call they have no row for. A member with no
// eligible roll call gets no row, and only chambers with a roll call that isn't a quorum call
// have rows. Each chamber's rows carry how many members have a row there and the median of
// their missed/eligible × 100. Rows are ordered by member, then chamber.
func Compute(
	congress int, rolls []repository.RollCallRef, counts []repository.MemberVoteCount,
) []repository.MemberAttendanceRow {
	chambers := make(map[string]bool)
	for _, r := range rolls {
		if !scoring.IsQuorumCall(r.Chamber, r.Question) {
			chambers[r.Chamber] = true
		}
	}

	byMember := make(map[memberKey]*repository.MemberAttendanceRow)
	for _, c := range counts {
		if !chambers[c.Chamber] || c.Count <= 0 {
			continue
		}
		k := memberKey{c.MemberID, c.Chamber}
		r := byMember[k]
		if r == nil {
			r = &repository.MemberAttendanceRow{MemberID: c.MemberID, Congress: congress, Chamber: c.Chamber}
			byMember[k] = r
		}
		r.Eligible += c.Count
		switch scoring.NormalizeVote(c.Vote) {
		case scoring.NotVoting:
			r.Missed += c.Count
		case scoring.Present:
			r.Present += c.Count
		}
	}

	shares := make(map[string][]float64)
	for _, r := range byMember {
		shares[r.Chamber] = append(shares[r.Chamber], missedPct(r))
	}
	medians := make(map[string]float64, len(shares))
	for chamber, s := range shares {
		medians[chamber] = Median(s)
	}
	rows := make([]repository.MemberAttendanceRow, 0, len(byMember))
	for _, r := range byMember {
		r.ChamberMembers = int64(len(shares[r.Chamber]))
		r.ChamberMedianPct = medians[r.Chamber]
		rows = append(rows, *r)
	}
	slices.SortFunc(rows, func(a, b repository.MemberAttendanceRow) int {
		return cmp.Or(cmp.Compare(a.MemberID, b.MemberID), cmp.Compare(a.Chamber, b.Chamber))
	})
	return rows
}

// missedPct is a row's missed share of its eligible roll calls, in percent. Eligible is never 0:
// Compute makes a row only for a positive count.
func missedPct(r *repository.MemberAttendanceRow) float64 {
	return float64(r.Missed) * 100 / float64(r.Eligible)
}

// Median returns the median of shares, the mean of the two middle values for an even count, or
// 0 for none. It doesn't reorder shares.
func Median(shares []float64) float64 {
	if len(shares) == 0 {
		return 0
	}
	const two = 2
	s := slices.Sorted(slices.Values(shares))
	mid := len(s) / two
	if len(s)%two == 1 {
		return s[mid]
	}
	return (s[mid-1] + s[mid]) / two
}
