// Package model holds the types the API returns and the repositories read: bills, members,
// votes, congresses, users, and the published vote aggregates.
package model

import (
	"encoding/json"
	"time"
)

// Congress represents a congressional session.
type Congress struct {
	Number    int        `json:"number"`
	StartDate time.Time  `json:"start_date"`
	EndDate   *time.Time `json:"end_date,omitempty"`
	IsCurrent bool       `json:"is_current"`
	// HasVotes is true once a roll call of this congress is loaded, so the web shows the
	// congress in its scorecard and /vote switch (#243). Only List fills it.
	HasVotes bool `json:"has_votes"`
}

// Member represents a member of Congress.
type Member struct {
	BioguideID  string  `json:"bioguide_id"`
	FirstName   string  `json:"first_name"`
	LastName    string  `json:"last_name"`
	BirthYear   *int    `json:"birth_year,omitempty"`
	PhotoURL    *string `json:"photo_url,omitempty"`
	OfficialURL *string `json:"official_url,omitempty"`
}

// MemberDetail extends Member with term information.
type MemberDetail struct {
	BioguideID  string              `json:"bioguide_id"`
	FirstName   string              `json:"first_name"`
	LastName    string              `json:"last_name"`
	BirthYear   *int                `json:"birth_year,omitempty"`
	PhotoURL    *string             `json:"photo_url,omitempty"`
	OfficialURL *string             `json:"official_url,omitempty"`
	Terms       []MemberTerm        `json:"terms"`
	RecentVotes []MemberVoteSummary `json:"recent_votes"`
}

// MemberVoteSummary shows a member's vote on a specific roll call.
type MemberVoteSummary struct {
	VoteID     string    `json:"vote_id"`
	BillID     *string   `json:"bill_id,omitempty"`
	BillTitle  *string   `json:"bill_title,omitempty"`
	VoteDate   time.Time `json:"vote_date"`
	Question   *string   `json:"question,omitempty"`
	Result     *string   `json:"result,omitempty"`
	MemberVote string    `json:"member_vote"`
	Chamber    string    `json:"chamber"`
}

// MemberTerm represents a member's term in a congress.
type MemberTerm struct {
	MemberID  string     `json:"member_id"`
	Congress  int        `json:"congress"`
	Chamber   string     `json:"chamber"`
	State     string     `json:"state"`
	District  *int       `json:"district,omitempty"`
	Party     string     `json:"party"`
	StartDate *time.Time `json:"start_date,omitempty"`
	EndDate   *time.Time `json:"end_date,omitempty"`
}

// Bill represents a piece of legislation.
//
// Sponsors, Cosponsors, Committees, Subjects and RelatedBills are the Congress.gov JSON the
// pipeline stores on the bill row. It writes the link tables (bill_sponsorships, bill_committees,
// bill_subjects, bill_relations) from the same data, and readers prefer those. The JSON is kept
// as a read cache and as the fallback for bills without link rows (design #31, question 4).
//
// Laws lists the laws the bill became, from Congress.gov (#709); it's empty for a bill that isn't
// law, and for a row the pipeline hasn't synced since the column was added.
type Bill struct {
	ID             string          `json:"id"`
	Congress       int             `json:"congress"`
	BillType       string          `json:"bill_type"`
	Number         int             `json:"number"`
	Title          string          `json:"title"`
	IntroducedDate *time.Time      `json:"introduced_date,omitempty"`
	OriginChamber  *string         `json:"origin_chamber,omitempty"`
	LatestAction   json.RawMessage `json:"latest_action,omitempty"`
	CurrentStatus  *string         `json:"current_status,omitempty"`
	StatusDate     *time.Time      `json:"status_date,omitempty"`
	PolicyArea     *string         `json:"policy_area,omitempty"`
	Sponsors       json.RawMessage `json:"sponsors,omitempty"`
	Cosponsors     json.RawMessage `json:"cosponsors,omitempty"`
	Committees     json.RawMessage `json:"committees,omitempty"`
	Subjects       json.RawMessage `json:"subjects,omitempty"`
	RelatedBills   json.RawMessage `json:"related_bills,omitempty"`
	Laws           []BillLaw       `json:"laws,omitempty"`
	UpdatedAt      *time.Time      `json:"updated_at,omitempty"`
	SyncedAt       *time.Time      `json:"synced_at,omitempty"`
}

// The law types Congress.gov's bill endpoint lists in a bill's laws.
const (
	BillLawTypePublic  = "Public Law"
	BillLawTypePrivate = "Private Law"
)

// BillLaw is a law a bill became, as Congress.gov's bill endpoint lists it.
type BillLaw struct {
	// Type is [BillLawTypePublic] or [BillLawTypePrivate].
	Type string `json:"type"`
	// Number is the law's number, its congress and then its place in that congress's sequence
	// of public or private laws: "119-95".
	Number string `json:"number"`
}

// BillIndexEntry is one bill in a congress's bill index (the web sitemap): its ID and when the
// pipeline last changed its row.
type BillIndexEntry struct {
	ID        string     `json:"id"`
	UpdatedAt *time.Time `json:"updated_at,omitempty"`
}

// BillStatusIndexEntry is one bill in a congress's status index (My votes, #853): its ID and its
// current_status, for a bill that has passed a chamber or gone further.
type BillStatusIndexEntry struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

// BillStatusEntry records when a bill reached a lifecycle stage.
type BillStatusEntry struct {
	Status     string    `json:"status"`
	StatusDate time.Time `json:"status_date"`
	StatusRank int       `json:"status_rank"`
}

// BillAction represents an action taken on a bill.
type BillAction struct {
	ID            string          `json:"id"`
	BillID        string          `json:"bill_id"`
	ActionDate    time.Time       `json:"action_date"`
	ActionTime    *string         `json:"action_time,omitempty"`
	ActionText    string          `json:"action_text"`
	ActionType    *string         `json:"action_type,omitempty"`
	ActionCode    *string         `json:"action_code,omitempty"`
	SourceSystem  *string         `json:"source_system,omitempty"`
	CommitteeCode *string         `json:"committee_code,omitempty"`
	RecordedVote  json.RawMessage `json:"recorded_vote,omitempty"`
	SortOrder     int             `json:"sort_order"`
}

// BillSummary holds AI-generated summaries for a bill. WhoItAffects is stored in the
// bill_summaries.why_it_matters column (docs/design/199-who-it-affects-rename.md).
type BillSummary struct {
	BillID       string     `json:"bill_id"`
	ShortSummary *string    `json:"short_summary,omitempty"`
	LongSummary  *string    `json:"long_summary,omitempty"`
	WhoItAffects *string    `json:"who_it_affects,omitempty"`
	ModelUsed    *string    `json:"model_used,omitempty"`
	GeneratedAt  *time.Time `json:"generated_at,omitempty"`
	// SourceVersionCode is the GovInfo code of the text version the summary was written from,
	// e.g. "rh". Summaries written before provenance was recorded have none.
	SourceVersionCode *string `json:"source_version_code,omitempty"`
	// SourceVersionName is that version's name as Congress.gov lists it, e.g. "Reported in
	// House", when the bill still has a text version with SourceVersionCode.
	SourceVersionName *string `json:"source_version_name,omitempty"`
	// WithCRSSummary is true when the prompt carried the bill's CRS summary as context (prompt
	// bill-v3, docs/design/197-crs-summaries.md), so the disclaimer can say so.
	WithCRSSummary bool `json:"with_crs_summary,omitempty"`
	// WithRuleContext is true when the prompt carried the rule a CRA resolution disapproves as
	// context (prompt bill-v4, docs/design/590-cra-disapproved-rules.md).
	WithRuleContext bool `json:"with_rule_context,omitempty"`
}

// CRSSummary is a Congressional Research Service summary of one version of a bill, as
// Congress.gov publishes it. Text is the plain-text rendering (paragraphs separated by blank
// lines, list items starting with "• "); the published HTML is never exposed.
type CRSSummary struct {
	BillID string `json:"bill_id"`
	// VersionCode is CRS's code for the version described ("00", "07", "49"). Codes aren't ordered.
	VersionCode string `json:"version_code"`
	// ActionDate and ActionDesc name the action the summary describes ("Introduced in House").
	ActionDate time.Time `json:"action_date"`
	ActionDesc string    `json:"action_desc"`
	Chamber    *string   `json:"chamber,omitempty"`
	Text       string    `json:"text"`
	// UpdatedAt is when CRS last updated the summary (lastSummaryUpdateDate).
	UpdatedAt time.Time `json:"updated_at"`
}

// BillTextVersion represents a version of a bill's text.
type BillTextVersion struct {
	ID          string          `json:"id"`
	BillID      string          `json:"bill_id"`
	VersionType string          `json:"version_type"`
	VersionCode string          `json:"version_code"`
	Date        *time.Time      `json:"date,omitempty"`
	Formats     json.RawMessage `json:"formats"`
	SortOrder   int             `json:"sort_order"`
	SyncedAt    *time.Time      `json:"synced_at,omitempty"`
}

// BillText holds the actual text content of a bill version.
type BillText struct {
	ID            string          `json:"id"`
	TextVersionID string          `json:"text_version_id"`
	Format        string          `json:"format"`
	Content       string          `json:"content,omitempty"`
	ContentHash   string          `json:"content_hash"`
	Sections      json.RawMessage `json:"sections,omitempty"`
	FetchedAt     *time.Time      `json:"fetched_at,omitempty"`
}

// BillTextDiff represents a diff between two bill text versions. DiffContent is empty when
// only the diff's metadata was read (a bill's list of diffs).
type BillTextDiff struct {
	ID            string          `json:"id"`
	BillID        string          `json:"bill_id"`
	FromVersionID string          `json:"from_version_id"`
	ToVersionID   string          `json:"to_version_id"`
	DiffStats     json.RawMessage `json:"diff_stats,omitempty"`
	DiffContent   json.RawMessage `json:"diff_content,omitempty"`
	GeneratedAt   *time.Time      `json:"generated_at,omitempty"`
}

// BillTextDiffSummary holds an AI-generated summary of a diff.
type BillTextDiffSummary struct {
	DiffID      string     `json:"diff_id"`
	Summary     string     `json:"summary"`
	ModelUsed   *string    `json:"model_used,omitempty"`
	GeneratedAt *time.Time `json:"generated_at,omitempty"`
}

// Amendment represents an amendment to a bill.
type Amendment struct {
	ID              string          `json:"id"`
	BillID          string          `json:"bill_id"`
	Congress        int             `json:"congress"`
	AmendmentType   string          `json:"amendment_type"`
	AmendmentNumber int             `json:"amendment_number"`
	Description     *string         `json:"description,omitempty"`
	Purpose         *string         `json:"purpose,omitempty"`
	SponsorID       *string         `json:"sponsor_id,omitempty"`
	LatestAction    json.RawMessage `json:"latest_action,omitempty"`
	SubmittedDate   *time.Time      `json:"submitted_date,omitempty"`
	Chamber         string          `json:"chamber"`
	SyncedAt        *time.Time      `json:"synced_at,omitempty"`
}

// CongressionalVote represents a recorded vote in Congress.
type CongressionalVote struct {
	ID         string    `json:"id"`
	BillID     *string   `json:"bill_id,omitempty"`
	Congress   int       `json:"congress"`
	Chamber    string    `json:"chamber"`
	Session    *int      `json:"session,omitempty"`
	RollNumber *int      `json:"roll_number,omitempty"`
	VoteDate   time.Time `json:"vote_date"`
	Question   *string   `json:"question,omitempty"`
	Result     *string   `json:"result,omitempty"`
	Yeas       *int      `json:"yeas,omitempty"`
	Nays       *int      `json:"nays,omitempty"`
	Present    *int      `json:"present,omitempty"`
	NotVoting  *int      `json:"not_voting,omitempty"`
}

// MemberVote records how a member voted on a congressional vote.
type MemberVote struct {
	CongressionalVoteID string `json:"congressional_vote_id"`
	MemberID            string `json:"member_id"`
	Vote                string `json:"vote"`
}

// User represents an application user. The only identity stored is AuthUID,
// the identity provider's opaque subject; email and name stay with the
// provider and never reach Spanner.
type User struct {
	ID        string    `json:"id"`
	AuthUID   string    `json:"-"`
	State     *string   `json:"state,omitempty"`
	District  *int      `json:"district,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	// SignInProvider is the firebase.sign_in_provider claim the account was
	// created with, e.g. google.com, or password for email-link sign-in.
	SignInProvider *string `json:"sign_in_provider,omitempty"`
	// DistrictChangedAt is when the state or district was last set.
	DistrictChangedAt *time.Time `json:"district_changed_at,omitempty"`
}

// UserUpdate holds optional fields for updating a user profile.
// Nil pointer means "don't change this field".
type UserUpdate struct {
	State    *string `json:"state,omitempty"`
	District *int    `json:"district,omitempty"`
	// DistrictChangeInterval is the least time between two changes that set
	// the state or district (design 89's district-change rule). Zero means no
	// limit. Clearing the state is always allowed.
	DistrictChangeInterval time.Duration `json:"-"`
}

// MaxSignInProviderLen is the width of users.sign_in_provider; longer
// provider IDs are cut to it.
const MaxSignInProviderLen = 32

// VoteChecks are the write-side controls on a cast vote
// (docs/design/89-aggregate-analytics.md).
type VoteChecks struct {
	// AppCheckOK is whether the request passed App Check, or nil when App
	// Check is off. It's stored as user_votes.app_check_ok.
	AppCheckOK *bool
	// DailyCap is the most bills a user may vote on in any 24 hours, counted
	// by user_votes.recorded_at (when the server stored the vote, so imported
	// votes count too); changing a vote cast in that window doesn't count
	// again. Zero means no cap.
	DailyCap int
}

// MaxImportVotes caps the number of votes one ImportVotes call accepts.
const MaxImportVotes = 1000

// ImportResult is what an ImportVotes call did.
type ImportResult struct {
	// Imported is how many votes it stored.
	Imported int
	// Capped lists, in request order, the bills whose votes it left out
	// because storing them would pass the daily vote cap. They weren't
	// stored, and a later import can add them.
	Capped []string
}

// UserExport is everything stored about one user, for self-serve export.
// AggExcludedAt is users.agg_excluded_at: when the account's votes were left
// out of the public aggregates (design 89's cohort exclusion), or null.
type UserExport struct {
	User          User           `json:"user"`
	AuthUID       string         `json:"auth_uid"`
	AggExcludedAt *time.Time     `json:"agg_excluded_at"`
	Votes         []UserVote     `json:"votes"`
	Favorites     []UserFavorite `json:"favorites"`
}

// UserVote records a user's vote on a bill.
type UserVote struct {
	UserID  string    `json:"user_id"`
	BillID  string    `json:"bill_id"`
	Vote    string    `json:"vote"`
	VotedAt time.Time `json:"voted_at"`
	// Title is the bill's title, read from bills by GetVotes so a scorecard
	// row can name the bill on any device. It isn't stored with the vote, and
	// is empty when the bill row is missing or the reader doesn't join it.
	Title string `json:"title,omitempty"`
	// AppCheckOK is user_votes.app_check_ok. Only the account export reads
	// it, so every stored field reaches the user's download.
	AppCheckOK *bool `json:"app_check_ok,omitempty"`
	// RecordedAt is user_votes.recorded_at, when the server stored the vote
	// (VotedAt is when the user voted, which an import takes from the
	// browser). Only the account export reads it; votes stored before the
	// column existed have none.
	RecordedAt *time.Time `json:"recorded_at,omitempty"`
}

// UserFavorite records a user's favorited bill.
type UserFavorite struct {
	UserID    string    `json:"user_id"`
	BillID    string    `json:"bill_id"`
	CreatedAt time.Time `json:"created_at"`
}

// GAOReport represents a Government Accountability Office report.
type GAOReport struct {
	ReportID      string     `json:"report_id"`
	Title         string     `json:"title"`
	ReportNumber  *string    `json:"report_number,omitempty"`
	ReportType    *string    `json:"report_type,omitempty"`
	PublishedDate *time.Time `json:"published_date,omitempty"`
	Summary       *string    `json:"summary,omitempty"`
	PDFURL        *string    `json:"pdf_url,omitempty"`
	HTMLURL       *string    `json:"html_url,omitempty"`
	SyncedAt      *time.Time `json:"synced_at,omitempty"`
}

// SyncState tracks pipeline synchronization progress.
type SyncState struct {
	Step         string    `json:"step"`
	Congress     int       `json:"congress"`
	LastSyncedAt time.Time `json:"last_synced_at"`
	LastOffset   *string   `json:"last_offset,omitempty"`
	ItemsSynced  int       `json:"items_synced"`
	ErrorCount   int       `json:"error_count"`
	LastError    *string   `json:"last_error,omitempty"`
}

// RepScore holds the alignment between a user and a representative under the scorecard rule
// (db/scoring). Only bills the user voted yea or nay on and the member has a position on count.
type RepScore struct {
	MemberID   string `json:"member_id"`
	MemberName string `json:"member_name"`
	Chamber    string `json:"chamber"`
	Party      string `json:"party"`
	// MatchingVotes counts compared bills where both voted the same way.
	MatchingVotes int `json:"matching_votes"`
	// TotalCompared counts bills where both the user and the member voted yea or nay.
	TotalCompared int `json:"total_compared"`
	// MemberAbsent counts bills where the member was present, didn't vote or cast another value.
	// They're left out of the percentage.
	MemberAbsent int `json:"member_absent"`
	// AlignmentPct is round(100 × MatchingVotes / TotalCompared, 2), or nil when nothing is compared.
	AlignmentPct *float64 `json:"alignment_pct"`
	// Rule names the scorecard rule, e.g. final-passage-v1.
	Rule string `json:"rule"`
}

// VoteComparison shows how a user and a member voted on the same bill, and which roll call
// is the member's position. Chamber and Congress are the roll call's, so a senator's House
// votes say House.
type VoteComparison struct {
	BillID    string    `json:"bill_id"`
	BillTitle string    `json:"bill_title"`
	VoteID    string    `json:"vote_id"`
	Chamber   string    `json:"chamber"`
	Congress  int       `json:"congress"`
	VoteDate  time.Time `json:"vote_date"`
	Question  *string   `json:"question"`
	// UserVote is yea or nay.
	UserVote string `json:"user_vote"`
	// MemberVote is yea, nay, present, not_voting or other.
	MemberVote string `json:"member_vote"`
	// Counted is true when the member voted yea or nay, so the bill is in the percentage.
	Counted bool `json:"counted"`
	// Matches is true when the bill is counted and both voted the same way.
	Matches bool `json:"matches"`
}

// BillSponsorship is a member who sponsored or cosponsored a bill, read from bill_sponsorships.
// Party, State and District come from the member's term in the bill's congress and are nil
// when there is none. FirstName and LastName are empty when the member isn't synced yet.
type BillSponsorship struct {
	BioguideID    string     `json:"bioguide_id"`
	FirstName     string     `json:"first_name"`
	LastName      string     `json:"last_name"`
	Role          string     `json:"role"`
	Party         *string    `json:"party,omitempty"`
	State         *string    `json:"state,omitempty"`
	District      *int       `json:"district,omitempty"`
	SponsoredDate *time.Time `json:"sponsored_date,omitempty"`
	IsOriginal    bool       `json:"is_original"`
}

// RelatedBill is a bill linked to another in the civic_graph ontology: through an explicit
// Congress.gov relation (RelationTypes), shared legislative subjects (SharedSubjects), or both.
type RelatedBill struct {
	BillID         string   `json:"bill_id"`
	Congress       int      `json:"congress"`
	BillType       string   `json:"bill_type"`
	Number         int      `json:"number"`
	Title          string   `json:"title"`
	CurrentStatus  *string  `json:"current_status,omitempty"`
	RelationTypes  []string `json:"relation_types"`
	SharedSubjects int      `json:"shared_subjects"`
}

// Collaborator is a member who sponsored or cosponsored bills together with another member
// in one congress. Party, State and Chamber come from the collaborator's term in that congress.
type Collaborator struct {
	BioguideID  string  `json:"bioguide_id"`
	FirstName   string  `json:"first_name"`
	LastName    string  `json:"last_name"`
	Party       *string `json:"party,omitempty"`
	State       *string `json:"state,omitempty"`
	Chamber     *string `json:"chamber,omitempty"`
	SharedBills int     `json:"shared_bills"`
}

// CompanionVote is one member's vote on a roll call about a bill's companion: an identical
// bill in the other chamber. Party comes from the member's term in the roll call's chamber.
type CompanionVote struct {
	CompanionBillID string    `json:"companion_bill_id"`
	VoteID          string    `json:"vote_id"`
	Chamber         string    `json:"chamber"`
	VoteDate        time.Time `json:"vote_date"`
	Question        *string   `json:"question,omitempty"`
	Result          *string   `json:"result,omitempty"`
	MemberID        string    `json:"member_id"`
	FirstName       string    `json:"first_name"`
	LastName        string    `json:"last_name"`
	Party           *string   `json:"party,omitempty"`
	Vote            string    `json:"vote"`
}
