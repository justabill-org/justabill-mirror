// Package scoring holds the scorecard rule, final-passage-v1: which roll calls count as a
// member's position on a bill, how clerk vote values are normalized, and how a user's votes
// are scored against a member's positions. It's pure Go with no database access, so every
// scorecard path (GetScorecard, CompareWithMember, the public positions endpoint) shares it.
//
// Design: docs/design/69-scorecard-methodology.md.
package scoring

import (
	"cmp"
	"slices"
	"strings"
	"time"

	"github.com/justabill-org/justabill/db/model"
)

// RuleName names the rule. API responses return it and the Methodology page quotes it; bump
// it whenever the rule's outcome changes.
const RuleName = "final-passage-v1"

// Normalized vote values, the public positions API's values.
const (
	Yea       = model.PositionYea
	Nay       = model.PositionNay
	Present   = model.PositionPresent
	NotVoting = model.PositionNotVoting
	// Other is any value that isn't a yes, no, present or absent (a Speaker candidate's name,
	// "Guilty"). It's treated like not voting.
	Other = model.PositionOther
)

// Candidate is one recorded roll call (it has a roll number, so it isn't a voice vote) that a
// member voted on and that is linked to a bill. Vote is the clerk's raw value on the way into
// PickPositions and the normalized one on the way out. VoteRepo.MemberPositions maps the
// result to model.MemberPosition for the API.
type Candidate struct {
	BillID     string
	BillTitle  string
	VoteID     string
	Chamber    string
	Congress   int
	Session    int
	RollNumber int
	VoteDate   time.Time
	Question   string
	Vote       string
}

const (
	chamberHouse  = "house"
	chamberSenate = "senate"
)

// houseFinalPrefixes are the House Clerk's <vote-question> forms that are a vote on the bill
// itself, lowercased. The House records a veto override as "Passage, Objections of the
// President To The Contrary Notwithstanding", with no leading "On".
func houseFinalPrefixes() []string {
	return []string{
		"on passage",
		"passage, objections of the president",
		"on motion to suspend the rules and pass",
		"on motion to suspend the rules and agree",
		"on motion to suspend the rules and concur",
		"on agreeing to the resolution",
		"on agreeing to the conference report",
		"on motion to concur in",
		"on motion that the house agree to the senate amendment",
		"on motion to agree to the senate amendment",
	}
}

// senateFinalPrefixes are the Senate's <vote_question_text> forms that are a vote on the
// measure itself, lowercased. The bare "On the Motion" covers commit, waive, recess and
// concur, so only the concurrence long form counts.
func senateFinalPrefixes() []string {
	return []string{
		"on passage of the bill",
		"on the joint resolution",
		"on the concurrent resolution",
		"on the resolution",
		"on the conference report",
		"on overriding the veto",
		"on the motion (motion to concur in the house amendment",
	}
}

// fold lowercases s and collapses runs of whitespace to one space.
func fold(s string) string {
	return strings.ToLower(strings.Join(strings.Fields(s), " "))
}

// IsFinalVote reports whether a roll call with this question is a final action on the bill in
// that chamber: passage, suspension, agreeing to a resolution, a conference report, concurring
// in the other chamber's amendment, or a veto override. Any other form, including one the
// allowlist doesn't know, is excluded.
func IsFinalVote(chamber, question string) bool {
	q := fold(question)
	var prefixes []string
	switch fold(chamber) {
	case chamberHouse:
		prefixes = houseFinalPrefixes()
	case chamberSenate:
		if strings.HasPrefix(q, "on the resolution of ratification") {
			return false
		}
		prefixes = senateFinalPrefixes()
	default:
		return false
	}
	return slices.ContainsFunc(prefixes, func(p string) bool { return strings.HasPrefix(q, p) })
}

// NormalizeVote maps a clerk's vote value to yea, nay, present, not_voting or other, ignoring
// case and extra whitespace. Already-normalized values map to themselves. The House records "Aye"/"No" on recorded votes and "Yea"/"Nay" on
// yea-and-nay votes.
func NormalizeVote(raw string) string {
	switch fold(raw) {
	case "yea", "aye":
		return Yea
	case "nay", "no":
		return Nay
	case "present", "present, giving live pair":
		return Present
	case "not voting", NotVoting:
		return NotVoting
	default:
		return Other
	}
}

// later reports whether a was taken after b: by date, then session, then roll number. House
// dates carry no time of day, so same-day votes are ordered by roll number.
func later(a, b Candidate) bool {
	if !a.VoteDate.Equal(b.VoteDate) {
		return a.VoteDate.After(b.VoteDate)
	}
	if a.Session != b.Session {
		return a.Session > b.Session
	}
	if a.RollNumber != b.RollNumber {
		return a.RollNumber > b.RollNumber
	}
	return a.VoteID > b.VoteID
}

// PickPositions applies the rule to a member's candidate roll calls: it keeps final-action
// votes on bills, takes the latest per bill, and normalizes its vote. The result has one
// position per bill, newest first (ties by bill ID). Candidates without a bill are ignored.
func PickPositions(candidates []Candidate) []Candidate {
	latest := make(map[string]Candidate)
	for _, c := range candidates {
		if c.BillID == "" || !IsFinalVote(c.Chamber, c.Question) {
			continue
		}
		if cur, ok := latest[c.BillID]; !ok || later(c, cur) {
			latest[c.BillID] = c
		}
	}
	positions := make([]Candidate, 0, len(latest))
	for _, p := range latest {
		p.Vote = NormalizeVote(p.Vote)
		positions = append(positions, p)
	}
	slices.SortFunc(positions, func(a, b Candidate) int {
		if c := b.VoteDate.Compare(a.VoteDate); c != 0 {
			return c
		}
		return cmp.Compare(a.BillID, b.BillID)
	})
	return positions
}
