package model

import (
	"strings"
	"time"
)

// Canonical member vote values. Every roll-call position the pipeline stores is one of
// these, unless the clerk recorded something that isn't a yes/no/present/absent (a
// Speaker candidate's name, "Guilty"), which is stored as the clerk wrote it.
const (
	VoteYea       = "Yea"
	VoteNay       = "Nay"
	VotePresent   = "Present"
	VoteNotVoting = "Not Voting"
)

// NormalizeMemberVote maps a clerk's value to a canonical one, ignoring case and
// surrounding or repeated whitespace. The House records "Aye"/"No" on recorded votes and
// "Yea"/"Nay" on yea-and-nay votes; both map to Yea/Nay. ok is false when the value isn't
// a yes/no/present/absent (a Speaker candidate, "Guilty"); it's then returned trimmed.
func NormalizeMemberVote(raw string) (string, bool) {
	trimmed := strings.TrimSpace(raw)
	switch strings.ToLower(strings.Join(strings.Fields(trimmed), " ")) {
	case "yea", "aye":
		return VoteYea, true
	case "nay", "no":
		return VoteNay, true
	case "present", "present, giving live pair":
		return VotePresent, true
	case "not voting":
		return VoteNotVoting, true
	default:
		return trimmed, false
	}
}

// Positions a member can hold on a bill in the public positions API, lower-case so clients
// can compare them with a voter's yea/nay. PositionOther is any value that isn't a yes, no,
// present or absent (a Speaker candidate's name, "Guilty"); it's treated like not voting.
const (
	PositionYea       = "yea"
	PositionNay       = "nay"
	PositionPresent   = "present"
	PositionNotVoting = "not_voting"
	PositionOther     = "other"
)

// MemberPosition is a member's position on one bill under the scorecard rule (db/scoring):
// their vote on the latest final-action roll call on it. Chamber is that roll call's, so a
// senator's House votes say House.
type MemberPosition struct {
	BillID   string    `json:"bill_id"`
	Vote     string    `json:"vote"`
	VoteID   string    `json:"vote_id"`
	Chamber  string    `json:"chamber"`
	VoteDate time.Time `json:"vote_date"`
	Question *string   `json:"question"`
}
