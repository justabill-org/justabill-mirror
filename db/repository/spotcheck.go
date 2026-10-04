package repository

import (
	"context"
	"time"
)

// SpotcheckReader is the read-only view of a loaded congress that pipeline-spotcheck compares
// with the official record (design 77, item 3). Every read through one reader sees the same
// snapshot of the database, and nothing is ever written through it.
type SpotcheckReader interface {
	// StoredRollCall returns a roll call and every member position stored for it, or nil when
	// the roll call isn't stored.
	StoredRollCall(ctx context.Context, voteID string) (*StoredRollCall, error)
	// Coverage counts what's loaded for a congress.
	Coverage(ctx context.Context, congress int) (*StoredCoverage, error)
	// Close ends the reader's read-only transaction.
	Close()
}

// StoredRollCall is a congressional_votes row with its member_votes.
type StoredRollCall struct {
	VoteID    string
	BillID    *string
	VoteDate  time.Time
	Question  *string
	Result    *string
	Yeas      *int
	Nays      *int
	Present   *int
	NotVoting *int
	Positions []StoredPosition
}

// StoredPosition is one member's stored position on a roll call. LISID is the member's
// members.lis_id, empty when it has none; the Senate's official XML names senators by it.
type StoredPosition struct {
	MemberID string
	LISID    string
	Vote     string
}

// StoredCoverage counts what's loaded for one congress.
type StoredCoverage struct {
	// BillsByType counts bills per bill_type ("hr", "sjres", ...).
	BillsByType map[string]int
	// RollCalls has one entry per chamber and session that has any roll call.
	RollCalls []StoredRollCallCount
	// MembersByChamber counts members with a term in the congress, per chamber.
	MembersByChamber map[string]int
	// SenatorsWithoutLISID lists members with a Senate term in the congress and no lis_id.
	SenatorsWithoutLISID []string
	// VotedBills counts the distinct bills the congress's roll calls are linked to.
	VotedBills int
	// MissingVotedBills lists linked bills that have no bills row.
	MissingVotedBills []string
	// VotedBillsWithoutSummary lists linked bills that are stored but have no short summary.
	VotedBillsWithoutSummary []string
	// TextVersions counts the congress's bill text versions, and VersionsWithoutText those of
	// them with no stored text.
	TextVersions        int
	VersionsWithoutText int
}

// StoredRollCallCount counts the roll calls (votes with a roll number) stored for one chamber
// and session. Distinct counts each roll number once; Highest is the largest one.
type StoredRollCallCount struct {
	Chamber  string
	Session  int
	Distinct int
	Highest  int
}
