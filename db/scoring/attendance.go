package scoring

// AttendanceRule names the rule that counts a member's missed votes: of the roll calls a member
// is listed on in a congress and chamber, quorum calls excluded (IsQuorumCall), a vote that
// normalizes to not_voting is missed, present is present, and any other recorded answer
// (yea, nay, a Speaker candidate's name, "Guilty") is a vote cast. Unlike the scorecard, where
// Other is no position, attendance counts Other as voting. The member page and the
// Methodology page quote the name; bump it whenever the rule's outcome changes.
//
// Design: docs/design/agent-1742-member-attendance.md.
const AttendanceRule = "attendance-v1"

// IsQuorumCall reports whether a roll call is a quorum call rather than a vote on a question:
// a House "Call by States" or "Call of the House" (the Clerk's QUORUM vote-type), ignoring case
// and extra whitespace. The Senate records no quorum calls as roll-call votes, so it always
// returns false there, as it does for an unknown chamber.
func IsQuorumCall(chamber, question string) bool {
	if fold(chamber) != chamberHouse {
		return false
	}
	switch fold(question) {
	case "call by states", "call of the house":
		return true
	default:
		return false
	}
}
