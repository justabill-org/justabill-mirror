package repository

import (
	"context"

	"github.com/justabill-org/justabill/db/model"
)

// BillRepo defines the bill repository interface used by handlers.
type BillRepo interface {
	List(ctx context.Context, params model.ListParams) (*model.ListResult[model.Bill], error)
	// CountByStatus counts the bills List's filters match, by current status, in one query
	// under the same WHERE as List. Paging, sort and the status filter are ignored.
	CountByStatus(ctx context.Context, params model.ListParams) (*model.BillCounts, error)
	GetByID(ctx context.Context, id string) (*model.Bill, error)
	GetActions(ctx context.Context, billID string) ([]model.BillAction, error)
	GetSummary(ctx context.Context, billID string) (*model.BillSummary, error)
	// GetSummaries returns the summaries of the given bills in one read, keyed by bill ID.
	// Bills without a summary are absent from the map.
	GetSummaries(ctx context.Context, billIDs []string) (map[string]model.BillSummary, error)
	// GetCRSSummary returns the bill's latest CRS summary: the one describing the latest
	// action, and of those the one CRS updated last. It is nil when the bill has none.
	GetCRSSummary(ctx context.Context, billID string) (*model.CRSSummary, error)
	// GetCRSLeads returns the lead of each given bill's latest CRS summary (the summary
	// GetCRSSummary returns, with [model.CRSLead] of its text) in one read, keyed by bill ID.
	// Bills without a CRS summary are absent from the map.
	GetCRSLeads(ctx context.Context, billIDs []string) (map[string]model.CardCRS, error)
	// GetCRARule returns the rule a CRA resolution disapproves, from its bill_cra_rules row and
	// the Federal Register documents that row names. It is nil for a bill that isn't a CRA
	// resolution or hasn't been checked yet, and its SearchURL is empty.
	GetCRARule(ctx context.Context, billID string) (*model.CRARule, error)
	// GetCardFacts returns the /vote card facts of the given bills (the latest CRS summary's
	// lead, each chamber's passage, the enactment and the count of law changes) from a fixed
	// number of reads, keyed by bill ID. Bills with no fact at all are absent from the map.
	GetCardFacts(ctx context.Context, billIDs []string) (map[string]model.BillCardFacts, error)
	GetTextVersions(ctx context.Context, billID string) ([]model.BillTextVersion, error)
	// GetDiffs returns the metadata of the bill's text diffs, without their diff_content,
	// leaving out empty ones (a pair with no section changes).
	GetDiffs(ctx context.Context, billID string) ([]model.BillTextDiff, error)
	GetAmendments(ctx context.Context, billID string) ([]model.Amendment, error)
	GetStatusHistory(ctx context.Context, billID string) ([]model.BillStatusEntry, error)
	// GetSponsorships returns the bill's sponsor and cosponsors from bill_sponsorships: the
	// sponsor first, then cosponsors by date. It is empty until the link rows are written.
	GetSponsorships(ctx context.Context, billID string) ([]model.BillSponsorship, error)
	// GetTextContent returns the text of one of the bill's text versions, or nil when the
	// version doesn't exist or belongs to another bill.
	GetTextContent(ctx context.Context, billID, versionID string) (*model.BillText, error)
	// GetDiffByID returns one of the bill's text diffs, or nil when the diff doesn't exist, is
	// empty or belongs to another bill.
	GetDiffByID(ctx context.Context, billID, diffID string) (*model.BillTextDiff, error)
	GetDiffSummary(ctx context.Context, diffID string) (*model.BillTextDiffSummary, error)
	ListGAOReports(ctx context.Context, billID string) ([]model.GAOReport, error)
	// Index returns every bill in the congress, ID and updated_at only, ordered by ID. It is
	// empty for a congress with no bills.
	Index(ctx context.Context, congress int) ([]model.BillIndexEntry, error)
	// StatusIndex returns the ID and current_status of every bill in the congress that has passed
	// a chamber or gone further (passed_house through became_law), ordered by ID. It is empty for
	// a congress with no such bills.
	StatusIndex(ctx context.Context, congress int) ([]model.BillStatusIndexEntry, error)
	// PolicyAreas returns the name of every policy area in policy_areas, A to Z. It is empty
	// before the pipeline has synced a bill with one.
	PolicyAreas(ctx context.Context) ([]string, error)
}

// MemberRepo defines the member repository interface used by handlers.
type MemberRepo interface {
	List(ctx context.Context, params model.ListParams) (*model.ListResult[model.Member], error)
	GetByID(ctx context.Context, bioguideID string) (*model.MemberDetail, error)
	GetByDistrict(ctx context.Context, state string, district int) ([]model.Member, error)
	GetSenators(ctx context.Context, state string) ([]model.Member, error)
	GetRecentVotes(ctx context.Context, bioguideID string, limit int) ([]model.MemberVoteSummary, error)
}

// UserRepo defines the user repository interface used by handlers.
type UserRepo interface {
	// CreateForAuthUID inserts a user with the given ID for authUID, or
	// returns the existing user for authUID (whose ID differs from id).
	// provider is the sign-in provider; an existing user without one gets it.
	CreateForAuthUID(ctx context.Context, id, authUID, provider string) (*model.User, error)
	GetByID(ctx context.Context, id string) (*model.User, error)
	GetByAuthUID(ctx context.Context, authUID string) (*model.User, error)
	// Update patches the user and returns it, or nil if there's no such user.
	// A change to the state or district within updates.DistrictChangeInterval
	// of the last one fails with a *DistrictChangeError; clearing the state
	// never does. A state or district that, with the stored one it keeps,
	// isn't a House seat fails with an error wrapping ErrInvalidSeat.
	Update(ctx context.Context, id string, updates model.UserUpdate) (*model.User, error)
	// Delete removes the user; their votes and favorites cascade.
	Delete(ctx context.Context, id string) error
	// CastVote records or changes a vote. It fails with ErrDailyVoteCap when
	// the vote would exceed checks.DailyCap, and with an error wrapping
	// ErrNotFound if the bill doesn't exist.
	CastVote(ctx context.Context, userID, billID, vote string, checks model.VoteChecks) error
	GetVotes(
		ctx context.Context, userID string, params model.ListParams,
	) (*model.ListResult[model.UserVote], error)
	// ImportVotes inserts votes for bills the user hasn't voted on. Votes
	// already stored win, and votes for bills that don't exist are skipped.
	// With checks.DailyCap set, it stores only as many as fit under the cap
	// and lists the rest in the result's Capped. checks.AppCheckOK is the
	// import request's App Check result, nil when App Check is off.
	ImportVotes(
		ctx context.Context, userID string, votes []model.UserVote, checks model.VoteChecks,
	) (model.ImportResult, error)
	// DeleteVote removes the user's vote on the bill. Removing a vote that
	// isn't there is not an error.
	DeleteVote(ctx context.Context, userID, billID string) error
	// AddFavorite adds the bill to the user's favorites. It returns an error
	// wrapping ErrNotFound if the bill doesn't exist.
	AddFavorite(ctx context.Context, userID, billID string) error
	RemoveFavorite(ctx context.Context, userID, billID string) error
	GetFavorites(
		ctx context.Context, userID string, params model.ListParams,
	) (*model.ListResult[model.UserFavorite], error)
	// Export returns the user's profile, votes and favorites, or nil if the
	// user doesn't exist.
	Export(ctx context.Context, userID string) (*model.UserExport, error)
}

// VoteRepo defines the vote repository interface used by handlers.
type VoteRepo interface {
	GetCongressionalVotes(ctx context.Context, billID string) ([]model.CongressionalVote, error)
	GetMemberVotes(ctx context.Context, voteID string) ([]model.MemberVote, error)
	// MemberPositions returns memberID's position on each bill with a final-action roll call in
	// congress (0 for every congress) under the scorecard rule, scoring.RuleName, newest first.
	// A member with no positions, or no such member, gets an empty slice.
	MemberPositions(ctx context.Context, memberID string, congress int) ([]model.MemberPosition, error)
}

// CongressRepo defines the congress repository interface used by handlers.
type CongressRepo interface {
	List(ctx context.Context) ([]model.Congress, error)
}

// GraphRepo runs the fixed, parameterised civic_graph queries (docs/design/31-bill-ontology.md).
// A limit of zero or less means the default, and every limit is capped.
type GraphRepo interface {
	// RelatedBills returns bills explicitly related to billID (either direction) and bills
	// sharing at least two legislative subjects with it, explicit relations first.
	RelatedBills(ctx context.Context, billID string, limit int) ([]model.RelatedBill, error)
	// Collaborators returns the members who sponsored or cosponsored the most bills of one
	// congress together with memberID, most shared bills first.
	Collaborators(ctx context.Context, memberID string, congress, limit int) ([]model.Collaborator, error)
	// CompanionVotes returns member votes on recorded roll calls (not voice votes) about
	// billID's identical bills in the other chamber, newest roll call first.
	CompanionVotes(ctx context.Context, billID string, limit int) ([]model.CompanionVote, error)
	// BillsChangingSection returns the bills of one congress, other than excludeBillID, whose
	// text amends, repeals or adds to the US Code section; bills that only cite it don't count.
	// Newest bills first.
	BillsChangingSection(
		ctx context.Context, sectionID, excludeBillID string, congress, limit int,
	) ([]model.SectionBill, error)
	// LawChangedByBill returns the bill's CHANGES_LAW edges, across its text versions, with
	// each section's heading, ordered by version and section.
	LawChangedByBill(ctx context.Context, billID string, limit int) ([]model.LawRef, error)
}

// LawRepo reads the US Code and the explanations of what bills change in it
// (docs/design/149-law-aware-assistant.md).
type LawRepo interface {
	// Section returns a US Code section's current text, or nil when it isn't loaded.
	Section(ctx context.Context, sectionID string) (*model.USCSection, error)
	// CurrentReleasePoint returns the most recently loaded release point, or nil before the
	// first load.
	CurrentReleasePoint(ctx context.Context) (*model.USCReleasePoint, error)
	// BillLawChanges returns the explanations of the bill's changes to law, by section ID.
	BillLawChanges(ctx context.Context, billID string) ([]model.BillLawChange, error)
	// BillLawChangeEntries returns what the bill's latest stored text changes in law, with the
	// explanations of that text and, per section, at most alsoLimit other bills of its congress
	// that change it too. It returns nil when the bill doesn't exist.
	BillLawChangeEntries(ctx context.Context, billID string, alsoLimit int) (*model.BillLawChanges, error)
}

// AggregateReader reads the published aggregates (docs/design/89-aggregate-analytics.md). It
// returns only published and held cells, never suppressed ones, and never the job's bookkeeping
// columns. The API reads aggregates through it and never counts user_votes itself.
type AggregateReader interface {
	// BillAggregates returns the bill's published and held cells: national first, then states
	// and districts by scope key.
	BillAggregates(ctx context.Context, billID string) ([]model.VoteAggregate, error)
	// BillAggregate returns the bill's published or held cell for one scope key ("" for
	// national, "CA", "CA-12"), or nil if there is none.
	BillAggregate(ctx context.Context, billID, scopeKey string) (*model.VoteAggregate, error)
	// MemberAlignment returns the member's rep_alignment rows, newest congress first.
	MemberAlignment(ctx context.Context, memberID string) ([]model.RepAlignment, error)
	// ConstituencyPositions returns the position on the bill, under the scorecard rule, of each
	// member who voted on its roll calls from the seat of scopeKey ("CA" for its senators,
	// "CA-12" or at-large "AK-0" for its representative), by last name. Members without a
	// recorded vote on the bill aren't listed.
	ConstituencyPositions(ctx context.Context, billID, scopeKey string) ([]model.ConstituencyPosition, error)
}

// Scorecard defines the scorecard service interface used by handlers. Both methods count only
// positions from the given congresses; an empty list means every loaded congress.
type Scorecard interface {
	// GetScorecard scores the user against each member holding one of their seats in the current
	// congress, on that member's positions in either chamber.
	GetScorecard(ctx context.Context, userID string, congresses []int) ([]model.RepScore, error)
	// CompareWithMember lists the bills the user and the member both have a vote on.
	CompareWithMember(
		ctx context.Context, userID, memberID string, congresses []int,
	) ([]model.VoteComparison, error)
}
