package repository

import "time"

// AggregateSnapshotParams sets the eligibility rules of an [AggregateSnapshot]
// (docs/design/89-aggregate-analytics.md, "The rules, in one place").
type AggregateSnapshotParams struct {
	// AsOf is the snapshot time: the account-age and activity windows end here.
	AsOf time.Time
	// MinAccountAge is how old an account must be for its votes to count (48 h by default).
	MinAccountAge time.Duration
	// YoungAccountAge marks eligible accounts younger than this as young, for the new-account
	// share hold rule (7 days by default).
	YoungAccountAge time.Duration
	// RequireAppCheck counts only votes that passed App Check when they were cast.
	RequireAppCheck bool
	// BurstWindow is the recent window of the burst hold rule (1 h); BaselineWindow is the
	// trailing window before it that the burst is compared with (7 days).
	BurstWindow    time.Duration
	BaselineWindow time.Duration
}

// AggregateVoteGroup counts the eligible Yea and Nay votes on one bill from users in one state
// and district. State is "" for users who haven't set one, and District is nil for users
// without a district; such votes still count nationally (and in the state when it's set). The
// aggregation job rolls groups up into national, state and district cells.
type AggregateVoteGroup struct {
	BillID   string
	State    string
	District *int
	Yea      int
	Nay      int
	// Recent counts the group's votes cast (or last changed) in the burst window, and Baseline
	// those in the baseline window before it.
	Recent   int
	Baseline int
	// New counts votes cast after the last publish of the cell of each scope that the group
	// belongs to (every vote when that cell was never published), and Young how many of those
	// came from young accounts. Index them with the model.AggregateScope* constants.
	New   map[string]int
	Young map[string]int
}

// Reasons a vote is left out of the aggregates, the keys of [AggregateSnapshot.Ineligible].
// A vote is counted under the first reason that applies, in this order.
const (
	IneligibleExcluded   = "excluded"
	IneligibleYoung      = "young_account"
	IneligibleNoAppCheck = "app_check"
)

// AggregateSnapshot is the aggregation job's read of user_votes at one point in time.
type AggregateSnapshot struct {
	// Groups holds the eligible Yea and Nay votes by bill, state and district.
	Groups []AggregateVoteGroup
	// Ineligible counts the Yea and Nay votes left out, by reason (the Ineligible* constants).
	Ineligible map[string]int
	// CellBills lists the bills that have a published or held cell, so cells whose votes are
	// all gone (deleted accounts) are revisited too.
	CellBills []string
}

// SeatPosition is a member's position on a bill under the scorecard rule (db/scoring), with
// the seat they held in that roll call's congress: ScopeKey is their district ("CA-12",
// at-large "AK-0") for the House and their state ("CA") for the Senate. Vote is normalized
// (model.Position*).
type SeatPosition struct {
	MemberID string
	Congress int
	ScopeKey string
	BillID   string
	Vote     string
}

// AggregateCohort names a group of accounts for the aggregates CLI's exclude command: every
// account whose sign_in_provider is Provider, created in [CreatedFrom, CreatedTo).
type AggregateCohort struct {
	Provider    string
	CreatedFrom time.Time
	CreatedTo   time.Time
}
