package scoring

import "math"

// The alignment percentage is rounded to two decimals: round(100 × m / c, 2).
const (
	pctScale   = 10000
	pctDivisor = 100
)

// Comparison is one bill that the user voted yea or nay on and the member has a position on.
type Comparison struct {
	// Position is the member's position, with its vote normalized.
	Position Candidate
	// UserVote is yea or nay.
	UserVote string
	// Counted is true when the member voted yea or nay, so the bill is in the percentage.
	Counted bool
	// Matches is true when the bill is counted and both sides voted the same way.
	Matches bool
}

// Result scores a user's votes against one member.
type Result struct {
	// Compared counts bills where both the user and the member voted yea or nay.
	Compared int
	// Matching counts compared bills where they voted the same way.
	Matching int
	// MemberAbsent counts bills where the member was present, didn't vote, or cast another
	// value. They're shown but left out of the percentage.
	MemberAbsent int
	// AlignmentPct is round(100 × Matching / Compared, 2), or nil when Compared is 0.
	AlignmentPct *float64
	// Rows lists every bill in the comparison, in the positions' order.
	Rows []Comparison
}

// normalizeUserVote returns yea or nay for a user's vote, or "" for skip and anything else.
func normalizeUserVote(raw string) string {
	switch fold(raw) {
	case Yea:
		return Yea
	case Nay:
		return Nay
	default:
		return ""
	}
}

// Score compares a user's votes (bill ID to yea, nay or skip) with a member's positions from
// PickPositions; raw vote values are normalized too. Bills the user skipped and bills without
// a member position aren't rows.
func Score(userVotes map[string]string, positions []Candidate) Result {
	res := Result{Rows: []Comparison{}}
	for _, p := range positions {
		userVote := normalizeUserVote(userVotes[p.BillID])
		if userVote == "" {
			continue
		}
		p.Vote = NormalizeVote(p.Vote)
		row := Comparison{Position: p, UserVote: userVote}
		switch p.Vote {
		case Yea, Nay:
			row.Counted = true
			row.Matches = p.Vote == userVote
			res.Compared++
			if row.Matches {
				res.Matching++
			}
		default:
			res.MemberAbsent++
		}
		res.Rows = append(res.Rows, row)
	}
	if res.Compared > 0 {
		pct := math.Round(float64(res.Matching)/float64(res.Compared)*pctScale) / pctDivisor
		res.AlignmentPct = &pct
	}
	return res
}
