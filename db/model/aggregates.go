package model

import "time"

// Aggregate scopes: a vote_aggregates cell covers one bill nationally, in one state, or in one
// congressional district (docs/design/89-aggregate-analytics.md).
const (
	AggregateScopeNational = "national"
	AggregateScopeState    = "state"
	AggregateScopeDistrict = "district"
)

// Aggregate statuses. A published cell shows its rounded numbers; a held cell keeps its last
// published numbers under review; a suppressed cell has too few eligible votes and is never served.
const (
	AggregateStatusPublished  = "published"
	AggregateStatusSuppressed = "suppressed"
	AggregateStatusHeld       = "held"
)

// Hold reasons, stored in vote_aggregates.hold_reason. The aggregation job holds a cell for a
// burst of votes, a high share of new votes from young accounts, or a sharp swing in the Yea
// share; the aggregates CLI holds one by hand (manual). A human releases a held cell by setting
// it back to published with the reason released: the job then publishes its current counts once
// without the hold rules, and clears the reason.
const (
	HoldReasonBurst         = "burst"
	HoldReasonYoungAccounts = "young_accounts"
	HoldReasonSwing         = "swing"
	HoldReasonManual        = "manual"
	HoldReasonReleased      = "released"
)

// VoteAggregate is one cell of vote_aggregates: how eligible Just a Bill users in one scope voted
// on one bill, rounded and thresholded by the aggregation job. ScopeKey is "" for the national
// cell, a state code ("CA") or a district ("CA-12", at-large "AK-0").
//
// BasisYea, BasisNay and HoldReason are the job's bookkeeping for the republish and hold rules.
// They are never served: the JSON encoding drops them, and the API's AggregateReader doesn't read
// them.
type VoteAggregate struct {
	BillID      string     `json:"bill_id"`
	Scope       string     `json:"scope"`
	ScopeKey    string     `json:"scope_key"`
	Status      string     `json:"status"`
	YeaPct      *int       `json:"yea_pct,omitempty"`
	NayPct      *int       `json:"nay_pct,omitempty"`
	VotersFloor *int       `json:"voters_floor,omitempty"`
	PublishedAt *time.Time `json:"published_at,omitempty"`
	ComputedAt  time.Time  `json:"computed_at"`
	BasisYea    *int       `json:"-"`
	BasisNay    *int       `json:"-"`
	HoldReason  *string    `json:"-"`
}

// RepAlignment is how often the published majority of a member's constituency (their district,
// or their state for senators) agreed with the member's Yea/Nay roll-call votes in one congress.
type RepAlignment struct {
	MemberID      string    `json:"member_id"`
	Congress      int       `json:"congress"`
	ScopeKey      string    `json:"scope_key"`
	BillsCompared int       `json:"bills_compared"`
	BillsAgreed   int       `json:"bills_agreed"`
	ComputedAt    time.Time `json:"computed_at"`
}

// ConstituencyPosition is one member's position on a bill from the seat of one constituency: the
// district for a representative, the state for a senator, as member_terms held it in the roll
// call's congress. It's what the district-vs-rep card shows next to that constituency's
// aggregate cell. Vote is the position the scorecard rule picked (db/scoring), and VoteID,
// Chamber, VoteDate and Question name the roll call it came from.
type ConstituencyPosition struct {
	MemberID  string    `json:"member_id"`
	FirstName string    `json:"first_name"`
	LastName  string    `json:"last_name"`
	Party     string    `json:"party"`
	Congress  int       `json:"congress"`
	Vote      string    `json:"vote"`
	VoteID    string    `json:"vote_id"`
	Chamber   string    `json:"chamber"`
	VoteDate  time.Time `json:"vote_date"`
	Question  *string   `json:"question"`
}
