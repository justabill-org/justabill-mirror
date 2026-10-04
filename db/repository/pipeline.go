package repository

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/justabill-org/justabill/db/model"
)

// MemberRow holds data for upserting a member.
type MemberRow struct {
	BioguideID string
	FirstName  string
	LastName   string
	PhotoURL   *string
}

// MemberTermRow holds data for upserting a member term. StartDate and EndDate are the
// dates the member held the seat within the congress. A nil StartDate leaves the stored value
// alone. A nil EndDate means the member still holds the seat and clears any stored end, so a
// member who returns is current again. The member-list sync writes EndDate with year precision
// (January 1 of the year they left, at the earliest the congress's first day).
type MemberTermRow struct {
	MemberID  string
	Congress  int
	Chamber   string
	State     string
	District  *int
	Party     string
	StartDate *time.Time
	EndDate   *time.Time
}

// CongressRow holds a congress's dates for UpsertCongress. EndDate is the first day of the
// next congress, as seed-congress stores it.
type CongressRow struct {
	Number    int
	StartDate time.Time
	EndDate   time.Time
}

// BillRow holds data for upserting a bill. Laws are the laws the bill became; none writes NULL.
type BillRow struct {
	ID             string
	Congress       int
	BillType       string
	Number         int
	Title          string
	IntroducedDate *time.Time
	OriginChamber  *string
	PolicyArea     *string
	LatestAction   json.RawMessage
	Sponsors       json.RawMessage
	Laws           []model.BillLaw
}

// BillActionRow holds data for inserting a bill action.
type BillActionRow struct {
	ActionDate   time.Time
	ActionText   string
	ActionType   *string
	ActionCode   *string
	SourceSystem *string
	SortOrder    int
}

// TextVersionRow holds data for inserting a text version.
type TextVersionRow struct {
	BillID      string
	VersionType string
	VersionCode string
	Date        *time.Time
	Formats     json.RawMessage
	SortOrder   int
}

// TextVersionSyncResult counts what UpsertBillTextVersions did to one bill's versions.
type TextVersionSyncResult struct {
	Inserted int
	Updated  int
	Pruned   int
	// PrunedCodes lists the version codes of the pruned versions, for logging.
	PrunedCodes []string
	// Duplicates counts input rows dropped because an earlier row had the same version code.
	Duplicates int
	// RefetchCodes lists the version codes of updated versions whose text was fetched and whose
	// formats now list other URLs, a corrected print: their texts were queued for a refetch.
	RefetchCodes []string
}

// AmendmentRow holds data for upserting an amendment.
type AmendmentRow struct {
	ID              string
	BillID          string
	Congress        int
	AmendmentType   string
	AmendmentNumber int
	Description     *string
	Purpose         *string
	LatestAction    json.RawMessage
	Chamber         string
}

// CongressionalVoteRow holds data for upserting a congressional vote.
type CongressionalVoteRow struct {
	ID         string
	BillID     *string
	Congress   int
	Chamber    string
	Session    *int
	RollNumber *int
	VoteDate   time.Time
	Question   *string
	Result     *string
	Yeas       *int
	Nays       *int
	Present    *int
	NotVoting  *int
}

// MemberVoteRow is one member's position on a roll call, in its stored (normalized) form.
type MemberVoteRow struct {
	MemberID string
	Vote     string
}

// TextVersionRef is a reference to an unfetched text version.
type TextVersionRef struct {
	ID          string
	BillID      string
	VersionCode string
	Formats     json.RawMessage
	// Refetch is true when the version has a text that UpsertBillTextVersions queued for a
	// refetch; store the new text with RefetchBillText, not InsertBillText.
	Refetch bool
}

// Cell limits of bill_texts (#451): a STRING(MAX) cell holds at most MaxTextChars characters,
// and a BYTES(MAX) or JSON cell at most MaxCellBytes bytes.
const (
	MaxTextChars = 2_621_440
	MaxCellBytes = 10 << 20
)

// BillTextRow holds data for inserting bill text content. Content goes in bill_texts.content:
// the download when it has at most [MaxTextChars] characters, else its plain text, else empty.
// ContentGz is the download gzipped, and ContentHash the SHA-256 of the download in hex, so a
// reader can tell whether Content is the download. A text too large to store at all has neither
// Content nor ContentGz, and empty Sections.
type BillTextRow struct {
	TextVersionID string
	Format        string
	Content       string
	ContentGz     []byte
	ContentHash   string
	Sections      json.RawMessage
	FetchedAt     time.Time
}

// PreviousVersionInfo holds info about a previous text version.
type PreviousVersionInfo struct {
	VersionID string
	Sections  json.RawMessage
}

// BillTextDiffRow holds data for inserting a diff.
type BillTextDiffRow struct {
	BillID        string
	FromVersionID string
	ToVersionID   string
	DiffStats     json.RawMessage
	DiffContent   json.RawMessage
	GeneratedAt   time.Time
	// IsEmpty records that the pair was diffed and no section changed, so the diff sweep doesn't
	// diff it again. Readers never return an empty diff (docs/design/401-remember-empty-diffs.md).
	IsEmpty bool
}

// DiffPair names two consecutive fetched versions of a bill: the pair a diff compares, oldest
// to newest.
type DiffPair struct {
	BillID        string
	FromVersionID string
	ToVersionID   string
}

// TextRefetch says what RefetchBillText did.
type TextRefetch struct {
	// Changed is true when the new text's content hash differs, so the text was replaced.
	Changed bool
	// Deleted counts the diffs at either end of a changed text, and their summaries, that
	// were deleted for the diff sweep and sync-summaries to recompute.
	Deleted DeletedDiffs
}

// DeletedDiffs counts what DeleteNonConsecutiveDiffs removed. Summaries counts the AI summaries
// deleted with those diffs, and Attempts their summary attempts.
type DeletedDiffs struct {
	Diffs     int
	Summaries int
	Attempts  int
}

// DiffReplacement says what ReplaceBillTextDiff did.
type DiffReplacement struct {
	// Found is false when the pair has no stored diff, so nothing was written.
	Found bool
	// Changed is true when the new content or is_empty flag differs from the stored diff's, so it
	// was replaced.
	Changed bool
	// SummaryDeleted is true when the replaced diff's AI summary was deleted, for
	// sync-summaries to write again.
	SummaryDeleted bool
	// AttemptDeleted is true when the replaced diff's summary attempt was deleted, so a block or
	// backoff earned by the old content no longer holds the new content back (#529).
	AttemptDeleted bool
}

// BillSummaryRow holds data for upserting a bill summary. The provenance fields record the text,
// prompt and model the summary came from; empty strings are stored as NULL, and a NULL
// SourceContentHash makes the bill due again in QueryBillsToSummarize. WhoItAffects is stored
// in the why_it_matters column (docs/design/199-who-it-affects-rename.md).
type BillSummaryRow struct {
	BillID       string
	ShortSummary string
	LongSummary  string
	WhoItAffects string
	ModelUsed    string

	SourceVersionID   string
	SourceVersionCode string
	SourceContentHash string
	// SourceCRSHash is the content_hash of the CRS summary the prompt carried, or empty for none.
	// While the CRS rule is on, a hash that differs from the bill's latest CRS summary's makes the
	// bill due again (docs/design/197-crs-summaries.md).
	SourceCRSHash string
	// SourceRuleHash is the bill_cra_rules.context_hash of the disapproved rule the prompt carried,
	// or empty for none. While the rule context is on, a hash that differs from the bill's row's
	// makes the bill due again (docs/design/590-cra-disapproved-rules.md).
	SourceRuleHash string
	PromptVersion  string
	// ModelVersion is the version the API reports it served, e.g. "gemini-3.8-flash-001".
	ModelVersion   string
	InputTruncated bool
	InputTokens    int
	OutputTokens   int
	ThinkingTokens int
}

// Summary attempt outcomes stored in summary_attempts.outcome. They match the ai package's
// Outcome values.
const (
	SummaryOutcomeOK              = "ok"
	SummaryOutcomeBlocked         = "blocked"
	SummaryOutcomeTruncatedOutput = "truncated_output"
	SummaryOutcomeInvalid         = "invalid"
	SummaryOutcomeError           = "error"
	// SummaryOutcomeBatchPending holds a bill exported to a batch job out of the synchronous
	// queue until its results are imported or the hold is released
	// (docs/design/198-corpus-resummarization.md). It isn't a failure: it resets the failure
	// count as ok does.
	SummaryOutcomeBatchPending = "batch_pending"
)

// Summary request types stored in summary_attempts.request_type. They match the ai package's
// RequestType values. Attempts of type batch don't count toward the synchronous daily cap.
const (
	SummaryRequestStandard = "standard"
	SummaryRequestFlex     = "flex"
	SummaryRequestBatch    = "batch"
)

// Summary batch states stored in summary_batches.state, besides the Vertex AI JOB_STATE_* values
// a batch holds while its job runs.
const (
	// SummaryBatchExporting is a batch whose input is being written; it has no job yet.
	SummaryBatchExporting = "exporting"
	// SummaryBatchImported is a batch whose results were imported.
	SummaryBatchImported = "imported"
	// SummaryBatchReleased is a batch whose job failed, was cancelled or expired, or ran past its
	// hold, and whose bills went back to the synchronous queue.
	SummaryBatchReleased = "released"
)

// SummaryQueueQuery selects the bills QueryBillsToSummarize returns.
type SummaryQueueQuery struct {
	Congress int
	Limit    int
	// PromptVersion and Model identify the generation that would run. A blocked attempt only
	// holds a bill back for the same content hash, prompt version and model.
	PromptVersion string
	Model         string
	// Now is the time next_attempt_at is compared with and RecentDays counts back from.
	Now time.Time
	// RecentDays is the width of the second priority tier: bills whose status_date is within
	// this many days of Now.
	RecentDays int
	// ResummarizeOnPromptChange also returns bills whose summary matches the latest text but
	// came from another prompt version, after every other due bill.
	ResummarizeOnPromptChange bool
	// CRSContext also returns bills whose summary was written with another CRS summary than the
	// bill's latest one, or with none while the bill has one, in their priority tier. A bill whose
	// latest attempt was blocked for its current text stays held back
	// (docs/design/197-crs-summaries.md, "Prompt bill-v3 and freshness").
	CRSContext bool
	// RuleContext also returns CRA resolutions whose summary was written with other disapproved
	// rule data than their bill_cra_rules row's, or with none, in their priority tier. As with
	// CRSContext, a bill whose latest attempt was blocked for its current text stays held back
	// (docs/design/590-cra-disapproved-rules.md, "Prompt context").
	RuleContext bool
	// PassedChamber keeps only bills that passed at least one chamber or became law: a Library of
	// Congress passage, presentment, signing or public-law action code, a "Passed/agreed to in"
	// action, or a passed_house or later status (#660). Off, the queue covers every bill.
	PassedChamber bool
}

// Summary queue tiers, in the order QueryBillsToSummarize returns them.
const (
	SummaryTierVoted        = 0 // the bill has a congressional_votes row
	SummaryTierRecent       = 1 // status_date within RecentDays
	SummaryTierOther        = 2
	SummaryTierPromptChange = 3 // current text, older prompt version
)

// SummaryQueueItem is one bill due for a summary: the latest text version that has stored text.
// It carries no text content; LoadBillContext loads it per bill.
type SummaryQueueItem struct {
	BillID      string
	VersionID   string
	VersionCode string
	ContentHash string
	Tier        int
}

// SummaryBillContext is the public metadata and stored text of one bill version, the input to a
// summary.
type SummaryBillContext struct {
	BillID       string
	Congress     int
	BillType     string
	Number       int
	Title        string
	PolicyArea   string
	Status       string
	StatusDate   *time.Time
	LatestAction string
	Committees   []string
	Subjects     []string
	VersionID    string
	VersionCode  string
	// VersionName is the version's type as Congress.gov names it, e.g. "Introduced in House".
	VersionName string
	ContentHash string
	Text        string
	// CRS is the bill's latest CRS summary, or nil when it has none.
	CRS *SummaryCRSContext
	// Rule is the rule the bill disapproves, or nil when it isn't a CRA resolution or hasn't been
	// checked yet.
	Rule *SummaryRuleContext
}

// SummaryRuleContext is the rule a CRA resolution disapproves as the summarizer gets it, from its
// bill_cra_rules row: the rule as the resolution names it, the Federal Register document matched
// to it (nil when unmatched) and, for a disapproved withdrawal, the withdrawn document.
// ContextHash is the row's context_hash, stored with the AI summary.
type SummaryRuleContext struct {
	Title       string
	Agency      string
	Document    *SummaryRuleDocument
	Withdrawn   *SummaryRuleDocument
	ContextHash string
}

// SummaryRuleDocument is a federal_register_documents row as the summarizer gets it. Agencies are
// the agencies' names in the document's order; EffectiveOn is nil and Action and Abstract are
// empty when the document has none.
type SummaryRuleDocument struct {
	Title       string
	Agencies    []string
	DocType     string
	Action      string
	Citation    string
	Published   time.Time
	EffectiveOn *time.Time
	Abstract    string
}

// SummaryCRSContext is the latest CRS summary of a bill as the summarizer gets it: the action it
// describes, its plain text and the content_hash stored with the AI summary.
type SummaryCRSContext struct {
	ActionDesc  string
	ActionDate  time.Time
	Text        string
	ContentHash string
}

// SummaryAttemptRow is one summarization attempt for a bill. Summary, when set, is written in the
// same transaction, so a summary is never stored without its attempt.
type SummaryAttemptRow struct {
	BillID        string
	ContentHash   string
	PromptVersion string
	Model         string
	Outcome       string
	Reason        string
	AttemptedAt   time.Time
	// RequestType is how the summary was requested (SummaryRequestStandard, Flex or Batch);
	// empty is stored as NULL. BatchID is the summary_batches row of a batch attempt.
	RequestType string
	BatchID     string
	// RetryAt, when set, is when a failed attempt is due again, in place of the backoff: a batch
	// line that failed in Vertex AI rather than in the model is due at once. It doesn't apply to
	// ok or blocked attempts, and the failure still counts.
	RetryAt *time.Time
	Summary *BillSummaryRow
}

// SummaryBatch is a summary_batches row: one Vertex AI batch job over many bills. The counts and
// times are nil until the job reports them.
type SummaryBatch struct {
	BatchID       string
	Congress      int
	Model         string
	PromptVersion string
	// JobName is the job's resource name (projects/.../batchPredictionJobs/...), empty until the
	// job is created.
	JobName     string
	State       string
	BillCount   int
	InputURI    string
	OutputURI   string
	OKCount     *int
	FailedCount *int
	CreatedAt   time.Time
	FinishedAt  *time.Time
	ImportedAt  *time.Time
}

// SummaryBatchUpdate changes a batch's state and whichever other fields are set: empty strings
// and nil pointers leave a column as it is.
type SummaryBatchUpdate struct {
	BatchID     string
	State       string
	JobName     string
	OutputURI   string
	OKCount     *int
	FailedCount *int
	FinishedAt  *time.Time
	ImportedAt  *time.Time
}

// SummaryBatchHolds are the batch_pending attempts for the bills exported to one batch. Each bill
// is held for its ContentHash with PromptVersion and Model, from At until Until.
type SummaryBatchHolds struct {
	BatchID       string
	PromptVersion string
	Model         string
	At            time.Time
	Until         time.Time
	Bills         []SummaryQueueItem
}

// LawChangeQueueQuery selects the bills QueryBillsToExplainLaw returns
// (docs/design/149-law-aware-assistant.md, "Explanations").
type LawChangeQueueQuery struct {
	Congress int
	// Limit is the most bills returned; CountBillsToExplainLaw ignores it.
	Limit int
	// PromptVersion and Model are the explainer's: a bill explained with another prompt is due
	// again, and an attempt holds a bill back only for the same text, prompt and model.
	PromptVersion string
	Model         string
	// Now is compared with next_attempt_at.
	Now time.Time
}

// LawChangeQueueItem is one bill due for a law-change explanation: its latest text version that
// has stored text.
type LawChangeQueueItem struct {
	BillID      string
	VersionID   string
	ContentHash string
}

// LawChangeContext is the input to one bill's law-change explanation: the bill's title and AI
// short summary, and its text version's references that change the law.
type LawChangeContext struct {
	BillID   string
	Congress int
	BillType string
	Number   int
	Title    string
	// ShortSummary is the bill's AI short summary, or empty when it has none.
	ShortSummary string
	VersionID    string
	VersionCode  string
	ContentHash  string
	// Refs are the version's amends, repeals and adds references, by bill section and then
	// section ID. A section can appear once per kind.
	Refs []LawChangeRef
}

// LawChangeRef is a reference that changes a section of law, with the section's current text
// when the section is loaded.
type LawChangeRef struct {
	SectionID      string
	RefKind        string
	SubsectionPath string
	Instruction    string
	BillSectionRef string
	// Loaded reports whether the section has a usc_sections row; Heading and CurrentText are
	// empty when it doesn't.
	Loaded      bool
	Heading     string
	CurrentText string
}

// LawChangeAttemptRow is one law-change explanation attempt for a bill. Changes, when non-nil,
// replaces the bill's bill_law_changes rows in the same transaction, so explanations are never
// stored without their attempt.
type LawChangeAttemptRow struct {
	BillID        string
	ContentHash   string
	PromptVersion string
	Model         string
	Outcome       string
	Reason        string
	AttemptedAt   time.Time
	Changes       []BillLawChangeRow
}

// DiffRef is a reference for diff summarization.
type DiffRef struct {
	DiffID      string
	BillID      string
	DiffContent json.RawMessage
}

// DiffSummaryQueueQuery selects the diffs QueryDiffsToSummarize returns.
type DiffSummaryQueueQuery struct {
	Congress int
	Limit    int
	// PromptVersion and Model identify the generation that would run. A blocked attempt only
	// holds a diff back for the same prompt version and model.
	PromptVersion string
	Model         string
	// Now is the time next_attempt_at is compared with.
	Now time.Time
}

// DiffSummaryRow is a diff's summary and its provenance. Empty provenance strings are stored as
// NULL.
type DiffSummaryRow struct {
	DiffID        string
	Summary       string
	ModelUsed     string
	PromptVersion string
	// ModelVersion is the version the API reports it served, e.g. "gemini-3.8-flash-001".
	ModelVersion string
	InputTokens  int
	OutputTokens int
}

// DiffSummaryAttemptRow is one summarization attempt for a diff. Summary, when set, is written in
// the same transaction, so a diff summary is never stored without its attempt. A diff's content
// never changes under its ID, so unlike [SummaryAttemptRow] it carries no content hash.
type DiffSummaryAttemptRow struct {
	DiffID        string
	PromptVersion string
	Model         string
	Outcome       string
	Reason        string
	AttemptedAt   time.Time
	Summary       *DiffSummaryRow
}

// BillStatusRow holds a lifecycle stage entry for the audit trail.
type BillStatusRow struct {
	BillID     string
	Status     string
	StatusDate time.Time
	StatusRank int
}

// GAOReportRow holds data for upserting a GAO report.
type GAOReportRow struct {
	ReportID      string
	Title         string
	ReportNumber  *string
	ReportType    *string
	PublishedDate *time.Time
	Summary       *string
	PDFURL        *string
	HTMLURL       *string
}

// GAORecheckAfter is how long a bill's GAO search stays fresh before
// [PipelineStore.ListBillsForGAOCheck] returns the bill again.
const GAORecheckAfter = 30 * 24 * time.Hour

// Sponsorship roles stored in bill_sponsorships.role.
const (
	SponsorRoleSponsor   = "sponsor"
	SponsorRoleCosponsor = "cosponsor"
)

// BillSponsorshipRow links a member to a bill they sponsored or cosponsored.
type BillSponsorshipRow struct {
	MemberID      string
	SponsoredDate *time.Time
	IsOriginal    bool
}

// BillCommitteeRow is one committee activity on a bill. The committee itself
// (ID, name, chamber, type) is upserted into committees alongside the link.
type BillCommitteeRow struct {
	CommitteeID   string
	CommitteeName string
	Chamber       *string
	CommitteeType *string
	Activity      string
	ActivityDate  *time.Time
}

// BillRelationRow links a bill to a related bill. RelatedBillID is built like
// bill IDs (<type>-<congress>-<number>) and need not exist yet.
type BillRelationRow struct {
	RelatedBillID string
	RelationType  string
	IdentifiedBy  *string
}

// BillLinkSource is a bill's stored relationship JSON, the input that
// backfill-links turns into link rows. A nil column was never synced.
type BillLinkSource struct {
	BillID         string
	IntroducedDate *time.Time
	Sponsors       json.RawMessage
	Cosponsors     json.RawMessage
	Committees     json.RawMessage
	Subjects       json.RawMessage
	RelatedBills   json.RawMessage
}

// StoredBillActions is a bill and its stored actions, the input that backfill status-history
// derives the bill's status from again. A bill with no stored actions has none.
type StoredBillActions struct {
	BillID  string
	Actions []BillActionRow
}

// SyncStateRow is a sync step's progress and health for one congress.
type SyncStateRow struct {
	Step     string
	Congress int
	// LastSyncedAt is the watermark: the start of the step's last complete, successful,
	// unlimited run. It is zero when the step has never succeeded.
	LastSyncedAt time.Time
	LastOffset   *string
	ItemsSynced  int
	// ErrorCount, LastError and LastErrorAt describe every failure so far; success keeps them.
	// A success with a warning (SyncRun.Warning) also sets LastError and LastErrorAt.
	ErrorCount  int
	LastError   *string
	LastErrorAt time.Time
	// LastAttemptAt is the start of the latest run, successful or not.
	LastAttemptAt time.Time
	// ConsecutiveFailures counts failed runs since the last success.
	ConsecutiveFailures int
}

// SyncRun is one finished run of a sync step, recorded by RecordSyncSuccess or
// RecordSyncFailure.
type SyncRun struct {
	Step      string
	Congress  int
	StartedAt time.Time
	// Watermark is what a successful run records as last_synced_at when it's earlier than
	// StartedAt: a run that resumed an interrupted one covers the changes since that run's
	// start, not only its own (pipeline sync-bills, #459). Zero means StartedAt.
	Watermark time.Time
	// ItemsSynced is what a successful run processed.
	ItemsSynced int
	// Error is a failed run's message, already redacted and truncated by the caller.
	Error string
	// Warning is a successful run's problem worth seeing without the logs, such as senators
	// the votes step couldn't place. RecordSyncSuccess stores it as last_error without
	// counting a failure. Redacted and truncated by the caller, like Error.
	Warning string
}

// Sync retry steps: the steps whose failed items are queued in sync_retry
// (docs/design/67-upstream-quota-retries.md).
const (
	RetryStepBills   = "bills"
	RetryStepGovInfo = "govinfo"
)

// The give-up policy for sync_retry. The maintainer may still change it (the design's open
// question 5), so each rule is one constant.
const (
	// MaxRetryAttempts is the failure count at which an item is given up.
	MaxRetryAttempts = 10
	// RetryBaseDelay is the wait after an item's first failure; it doubles with each attempt.
	RetryBaseDelay = 30 * time.Minute
	// RetryMaxDelay caps the doubling.
	RetryMaxDelay = 24 * time.Hour
	// MaxRetryErrorBytes caps the stored last_error.
	MaxRetryErrorBytes = 1024
)

// NextRetryAt is when an item that has now failed attempts times is due again:
// failedAt + min(RetryBaseDelay·2^(attempts−1), RetryMaxDelay). It reports false when the item
// is given up, after MaxRetryAttempts failures or on a permanent error.
func NextRetryAt(attempts int, failedAt time.Time, permanent bool) (time.Time, bool) {
	if permanent || attempts >= MaxRetryAttempts {
		return time.Time{}, false
	}
	delay := RetryBaseDelay
	for i := 1; i < attempts && delay < RetryMaxDelay; i++ {
		delay *= 2
	}
	return failedAt.Add(min(delay, RetryMaxDelay)), true
}

// ItemFailure is one item (a bill or a GovInfo package) that still failed after the upstream
// client's own retries, recorded by RecordItemFailure.
type ItemFailure struct {
	Step     string
	Congress int
	// ItemID is the bill ID (hr-119-43) or the GovInfo package ID.
	ItemID string
	// Error is the failure's message, already redacted by the caller. The store keeps at most
	// MaxRetryErrorBytes of it.
	Error string
	// Permanent gives the item up at once (for example an HTTP 404).
	Permanent bool
	FailedAt  time.Time
}

// RetryItem is a sync_retry row.
type RetryItem struct {
	Step          string
	Congress      int
	ItemID        string
	Attempts      int
	FirstFailedAt time.Time
	LastFailedAt  time.Time
	// NextAttemptAt is zero when the item is given up.
	NextAttemptAt time.Time
	LastError     string
}

// RetryStats counts a step's sync_retry rows: due now, waiting for their next attempt, and
// given up.
type RetryStats struct {
	Due     int
	Waiting int
	GivenUp int
}

// ErrMemberNotFound means the member isn't in the members table.
var ErrMemberNotFound = errors.New("member not found")

// LisIDChange is what SetMemberLisID changed.
type LisIDChange struct {
	// Previous is the member's LIS ID before the call, or "" if they had none.
	Previous string
	// TakenFrom is the other member that held the LIS ID and had it cleared, or "".
	TakenFrom string
}

// SenatorName is a senator's name and state in one congress, for matching the names in the
// Senate's roll-call XML when the senator's LIS ID isn't stored.
type SenatorName struct {
	BioguideID string
	FirstName  string
	LastName   string
	State      string
}

// USCSectionRow holds a US Code section for UpsertUSCSections. SectionID uses a hyphen where
// USLM has an en dash ("/us/usc/t42/s1395w-4"), and ContentHash covers everything else in the row.
type USCSectionRow struct {
	SectionID     string
	TitleNumber   int
	SectionNumber string
	Heading       *string
	Text          string
	Status        string
	PositiveLaw   bool
	ReleasePoint  string
	ContentHash   string
}

// CRSSummaryRow holds one CRS summary version for UpsertCRSSummaries
// (docs/design/197-crs-summaries.md). BillID needn't have a bills row yet. TextHTML is the text as
// Congress.gov publishes it, Text its plain-text rendering, and ContentHash the sha256 of TextHTML.
// CRSUpdatedAt is lastSummaryUpdateDate and SourceUpdatedAt is updateDate.
type CRSSummaryRow struct {
	BillID          string
	VersionCode     string
	ActionDate      time.Time
	ActionDesc      string
	Chamber         *string
	TextHTML        string
	Text            string
	ContentHash     string
	CRSUpdatedAt    time.Time
	SourceUpdatedAt time.Time
}

// CRSSummaryKey names one stored CRS summary version.
type CRSSummaryKey struct {
	BillID      string
	VersionCode string
}

// StoredCRSSummaries is what [PipelineStore.StoredCRSSummaries] found for a set of bills: the
// content hash and lastSummaryUpdateDate of each stored summary version, and which of the bills
// have a bills row.
type StoredCRSSummaries struct {
	Versions map[CRSSummaryKey]StoredCRSVersion
	Bills    map[string]bool
}

// StoredCRSVersion is a stored summary version's content hash and CRSUpdatedAt, which the sync
// compares to decide whether a listed summary changed.
type StoredCRSVersion struct {
	ContentHash  string
	CRSUpdatedAt time.Time
}

// USCReleasePointRow records a loaded US Code release point.
type USCReleasePointRow struct {
	ReleasePoint  string
	PublishedDate *time.Time
	SourceURL     string
	LoadedAt      time.Time
	SectionCount  int
}

// BillLawRefRow is one reference from a bill text version to a section of law. SectionID is a
// US Code section ID or model.NonUSCSectionPrefix plus the citation; RefKind is one of the
// model.LawRef* kinds.
type BillLawRefRow struct {
	SectionID      string
	RefKind        string
	CiteText       *string
	SubsectionPath *string
	Instruction    *string
	BillSectionRef *string
}

// LawRefSource is a stored bill text version, the input the law-reference backfill parses.
// Formats is the version's Congress.gov formats JSON, which names its GovInfo package, and
// Content is the text as downloaded (unzipped from bill_texts.content_gz when needed).
type LawRefSource struct {
	BillID      string
	VersionID   string
	VersionCode string
	Formats     json.RawMessage
	Format      string
	Content     string
}

// BillLawChangeRow is the explanation of how a bill's latest text changes one section of law.
// Explanation is empty when the section wasn't explained: it came past the explainer's input or
// output caps, or the model left it out.
type BillLawChangeRow struct {
	SectionID         string
	ChangeKind        string
	Explanation       string
	SourceContentHash string
	ReleasePoint      string
	ModelUsed         string
	PromptVersion     string
}

// PipelineStore defines all database operations for the pipeline.
type PipelineStore interface {
	// Members
	UpsertMember(ctx context.Context, m MemberRow) error
	UpsertMemberTerm(ctx context.Context, t MemberTermRow) error
	UpdateMemberLisID(ctx context.Context, bioguideID, lisID string) error
	// ListMemberIDs returns the bioguide ID of every stored member, in no particular order.
	ListMemberIDs(ctx context.Context) ([]string, error)

	// Bills. UpsertBill leaves updated_at alone; MarkBillSynced sets it once the bill and every
	// sub-resource have synced, so updated_at means "last complete sync" and a bill whose sync
	// failed partway keeps its old value.
	UpsertBill(ctx context.Context, b BillRow) error
	MarkBillSynced(ctx context.Context, billID string) error
	// ListBillIDsSyncedSince returns the IDs of a congress's bills whose updated_at >= since.
	ListBillIDsSyncedSince(ctx context.Context, congress int, since time.Time) ([]string, error)
	// ListMissingVotedBillIDs returns, sorted and once each, the bill IDs a congress's roll calls
	// name that have no bills row yet, or whose row was never completely synced (updated_at NULL:
	// a sync that failed or was interrupted after UpsertBill). These are the bills the
	// backfill's voted-bills step loads, so a rerun picks up where a partial run stopped.
	ListMissingVotedBillIDs(ctx context.Context, congress int) ([]string, error)
	ReplaceBillActions(ctx context.Context, billID string, actions []BillActionRow) error
	// UpsertBillTextVersions matches versions to the bill's stored ones by code, then by type, and
	// updates them in place so version IDs, texts, diffs and diff summaries survive a re-sync. New
	// versions are inserted. When versions is non-empty, stored versions it doesn't list are pruned
	// with their texts, diffs, diff summaries and attempts. An updated version whose text was fetched and
	// whose formats now list other URLs keeps its text but is queued for a refetch.
	UpsertBillTextVersions(
		ctx context.Context, billID string, versions []TextVersionRow,
	) (TextVersionSyncResult, error)
	UpdateBillJSON(ctx context.Context, billID, column string, value json.RawMessage) error
	UpdateBillStatus(ctx context.Context, billID, status string, date *time.Time) error
	// ReplaceBillStatus sets a bill's current status and its date and replaces its status
	// history with entries, in one transaction, so a stage it no longer has is dropped.
	ReplaceBillStatus(ctx context.Context, billID, status string, date *time.Time, entries []BillStatusRow) error
	// ListStoredBillActions pages through a congress's bills in bill_id order, starting after
	// afterBillID ("" for the first page), each with its stored actions in sort_order.
	ListStoredBillActions(ctx context.Context, congress int, afterBillID string, limit int) ([]StoredBillActions, error)
	UpsertAmendment(ctx context.Context, a AmendmentRow) error

	// Ontology link tables. Each call replaces the bill's existing links (for
	// sponsorships, only those with the given role) in one transaction.
	ReplaceBillSponsorships(ctx context.Context, billID, role string, rows []BillSponsorshipRow) error
	ReplaceBillCommittees(ctx context.Context, billID string, rows []BillCommitteeRow) error
	ReplaceBillSubjects(ctx context.Context, billID string, subjectNames []string) error
	ReplaceBillRelations(ctx context.Context, billID string, rows []BillRelationRow) error
	// ListBillLinkSources pages through a congress's bills in bill_id order,
	// starting after afterBillID ("" for the first page).
	ListBillLinkSources(ctx context.Context, congress int, afterBillID string, limit int) ([]BillLinkSource, error)

	// Congresses (serve follows the current congress, #116)
	// UpsertCongress writes a congress's dates. A new row isn't current; an existing row keeps
	// its is_current.
	UpsertCongress(ctx context.Context, c CongressRow) error
	// SetCurrentCongress marks the congress current and every other row not current, in one
	// transaction, and reports whether any row changed. The congress's row must exist.
	SetCurrentCongress(ctx context.Context, congress int) (bool, error)
	// CountMemberTerms returns how many member terms the congress has, in both chambers.
	CountMemberTerms(ctx context.Context, congress int) (int, error)

	// Votes
	ExistingVoteIDs(ctx context.Context, voteIDs []string) (map[string]bool, error)
	UpsertCongressionalVote(ctx context.Context, v CongressionalVoteRow) error
	// StoreRollCall writes a roll call's row and its members' positions in one commit, so an
	// interrupted write never leaves a vote stored with only some of its members (#455).
	StoreRollCall(ctx context.Context, v CongressionalVoteRow, members []MemberVoteRow) error

	// Texts & Diffs
	// QueryUnfetchedTextVersions returns the versions with no text yet and those queued for a
	// refetch (Refetch set), by bill and then version order, at most limit of them (all when limit
	// is zero or less).
	QueryUnfetchedTextVersions(ctx context.Context, limit int) ([]TextVersionRef, error)
	InsertBillText(ctx context.Context, t BillTextRow) error
	// RefetchBillText stores the refetched text of a version queued for a refetch, and does
	// nothing when the version isn't queued (its text was refetched or pruned meanwhile). With
	// the same content hash it only records FetchedAt, so diffs and summaries stay. With a new
	// hash it replaces the text and deletes the diffs at either end with their summaries and
	// summary attempts.
	RefetchBillText(ctx context.Context, t BillTextRow) (TextRefetch, error)
	FindPreviousVersion(ctx context.Context, billID, versionID string) (*PreviousVersionInfo, error)
	LoadSections(ctx context.Context, versionID string) (json.RawMessage, error)
	// UpdateBillTextSections replaces the parsed sections stored with a version's text, leaving
	// the text itself alone. It's how reparse-texts applies a new parser to stored texts.
	UpdateBillTextSections(ctx context.Context, versionID string, sections json.RawMessage) error
	InsertBillTextDiff(ctx context.Context, d BillTextDiffRow) error
	// DeleteNonConsecutiveDiffs deletes every diff, with its summary, whose from and to are not
	// consecutive fetched versions of its bill, oldest to newest: backwards diffs, diffs that skip
	// a version fetched later, and diffs whose version is gone.
	DeleteNonConsecutiveDiffs(ctx context.Context) (DeletedDiffs, error)
	// QueryMissingDiffPairs returns consecutive fetched versions of a bill that have no diff row
	// (an empty diff counts as one) and whose texts differ, by bill and then version order, at
	// most limit of them (all when limit is zero or less).
	QueryMissingDiffPairs(ctx context.Context, limit int) ([]DiffPair, error)
	// QueryStoredDiffPairs returns the version pair of every stored diff, by bill and then
	// version order.
	QueryStoredDiffPairs(ctx context.Context) ([]DiffPair, error)
	// ReplaceBillTextDiff replaces the stats, content and is_empty flag of a pair's stored diff.
	// When the content and flag are unchanged it writes nothing, so the diff keeps its summary; when
	// either changed, the summary is deleted. A pair with no stored diff is left alone.
	ReplaceBillTextDiff(ctx context.Context, d BillTextDiffRow) (DiffReplacement, error)

	// Summaries
	UpsertBillSummary(ctx context.Context, s BillSummaryRow) error
	// QueryBillsToSummarize returns up to q.Limit bills of q.Congress that are due for a summary,
	// one row per bill for its latest text version with stored text: bills with no summary, or a
	// summary of other text, unless an attempt for that text, prompt and model blocks them or
	// waits for its next_attempt_at. Rows come in tier order (SummaryTierVoted first), then by
	// status_date, newest first, then bill_id.
	QueryBillsToSummarize(ctx context.Context, q SummaryQueueQuery) ([]SummaryQueueItem, error)
	// CountBillsToSummarize counts the bills QueryBillsToSummarize would return with no limit,
	// by tier. q.Limit is ignored; a tier with no due bills is absent from the map.
	CountBillsToSummarize(ctx context.Context, q SummaryQueueQuery) (map[int]int, error)
	// LoadBillContext loads a bill's metadata and the stored text of one of its versions.
	LoadBillContext(ctx context.Context, billID, versionID string) (*SummaryBillContext, error)
	// RecordSummaryAttempt replaces the bill's attempt row. attempts counts consecutive
	// failures for the same content hash, prompt and model and restarts for new ones; a
	// failure is retried after 1 h, 4 h, then every 24 h, and a blocked outcome is never
	// retried for that hash, prompt and model. A batch_pending row before it counts as no
	// failure, like ok. It refuses a batch_pending outcome: HoldSummaryBatch writes those.
	// An attempt with a BatchID is a batch result: it's written only while the bill's row is
	// that batch's batch_pending hold for the same content hash. When that batch has already
	// recorded the line (a re-import) it writes nothing and returns nil; otherwise it returns
	// [ErrStaleBatchAttempt].
	RecordSummaryAttempt(ctx context.Context, a SummaryAttemptRow) error
	// CountSummaryAttemptsSince counts the bills and diffs whose latest attempt is at or after
	// since and wasn't a batch attempt: the attempts that count toward the synchronous daily cap.
	CountSummaryAttemptsSince(ctx context.Context, since time.Time) (int, error)
	// QueryDiffsToSummarize returns up to q.Limit diffs of q.Congress's bills that have no
	// summary, ordered by bill, never an empty one (is_empty). A diff whose last attempt for the
	// same prompt version and model was blocked is never returned; one that failed otherwise
	// waits for its next_attempt_at.
	QueryDiffsToSummarize(ctx context.Context, q DiffSummaryQueueQuery) ([]DiffRef, error)
	// RecordDiffSummaryAttempt replaces the diff's attempt row, and writes a.Summary when set,
	// in one transaction. Retries and blocks follow RecordSummaryAttempt's rules.
	RecordDiffSummaryAttempt(ctx context.Context, a DiffSummaryAttemptRow) error

	// Summary batches (docs/design/198-corpus-resummarization.md)
	// CreateSummaryBatch inserts a batch row; it fails if the batch ID exists.
	CreateSummaryBatch(ctx context.Context, b SummaryBatch) error
	// UpdateSummaryBatch sets a batch's state and the other fields u sets. It returns an error
	// wrapping ErrNotFound if the batch doesn't exist.
	UpdateSummaryBatch(ctx context.Context, u SummaryBatchUpdate) error
	// SummaryBatch reads one batch, or returns an error wrapping ErrNotFound.
	SummaryBatch(ctx context.Context, batchID string) (*SummaryBatch, error)
	// OpenSummaryBatches lists the batches not yet imported or released, oldest first.
	OpenSummaryBatches(ctx context.Context) ([]SummaryBatch, error)
	// HoldSummaryBatch writes a batch_pending attempt (request type batch) for each of h.Bills,
	// replacing its attempt row, in commits of at most 1,000 bills. A failed commit leaves the
	// earlier ones in place; writing the same holds again is harmless.
	HoldSummaryBatch(ctx context.Context, h SummaryBatchHolds) error
	// ReleaseSummaryBatch makes every bill still held by the batch due at now, and returns how
	// many it released.
	ReleaseSummaryBatch(ctx context.Context, batchID string, now time.Time) (int, error)

	// GAO Reports
	UpsertGAOReport(ctx context.Context, r GAOReportRow) error
	LinkBillGAOReport(ctx context.Context, billID, reportID string) error
	// ListBillsForGAOCheck returns up to limit bill IDs of a congress to search for GAO reports:
	// never-checked bills first, then those last checked more than [GAORecheckAfter] ago, each
	// group most recently active (status_date) first.
	ListBillsForGAOCheck(ctx context.Context, congress, limit int) ([]string, error)
	// MarkGAOChecked records that the bill's GAO search succeeded now.
	MarkGAOChecked(ctx context.Context, billID string) error

	// CRA resolutions and the rules they disapprove (docs/design/590-cra-disapproved-rules.md)
	// ListCRARuleChecks returns up to limit of the congress's CRA resolutions (sjres and hjres
	// whose title matches [CRATitlePattern]) that are due for a lookup: those with no
	// bill_cra_rules row, whose latest stored text's hash differs from the row's, whose row has
	// another matcher version than matcherVersion, or that are unmatched and were checked more
	// than [CRARecheckAfter] ago. Never-checked ones come first, then the most recently
	// introduced. All of them when limit is zero or less.
	ListCRARuleChecks(ctx context.Context, congress int, matcherVersion string, limit int) ([]CRARuleCheck, error)
	// UpsertFRDocument writes a Federal Register document unless the stored one has the same
	// content hash, and reports whether it wrote.
	UpsertFRDocument(ctx context.Context, d FRDocumentRow) (bool, error)
	// UpsertCRARule writes a resolution's bill_cra_rules row, stamping checked_at, and reports
	// whether anything a reader sees changed: anything but the source text hash, the matcher
	// version and checked_at. It fails if the bill doesn't exist.
	UpsertCRARule(ctx context.Context, r CRARuleRow) (bool, error)

	// GovInfo
	FindUnfetchedVersion(ctx context.Context, billID, versionCode string) (string, error)

	// Senators
	LISLookup(ctx context.Context) (map[string]string, error)
	// SetMemberLisID gives a member an LIS ID in one read-write transaction, first clearing it
	// from any other member that holds it (members.lis_id is unique). It returns
	// ErrMemberNotFound, and changes nothing, when the member isn't stored. Only the Senate's own
	// member feed calls it; UpdateMemberLisID, which never overwrites, serves other sources.
	SetMemberLisID(ctx context.Context, bioguideID, lisID string) (LisIDChange, error)
	// ListSenators returns every member with a Senate term in the congress.
	ListSenators(ctx context.Context, congress int) ([]SenatorName, error)
	// IncompleteVoteIDs returns the congress's roll calls in a chamber that have fewer
	// member_votes rows than yeas + nays + present + not_voting. Rows without totals (voice
	// votes and unanimous consent) are never incomplete.
	IncompleteVoteIDs(ctx context.Context, congress int, chamber string) ([]string, error)

	// Aggregates (docs/design/89-aggregate-analytics.md). The job reads the previous cells,
	// with their bookkeeping, to apply the republish and hold rules, then writes new ones.
	ListVoteAggregates(ctx context.Context, billIDs []string) ([]model.VoteAggregate, error)
	UpsertVoteAggregates(ctx context.Context, cells []model.VoteAggregate) error
	// ReviseVoteAggregates reads every stored cell of the bills and writes the cells revise
	// returns, in one read-write transaction, so a change made between the read and the write
	// (the aggregates CLI's hold or release, or a concurrent job run) is never overwritten.
	// revise may run more than once if the transaction retries, so it must not keep state
	// between calls.
	ReviseVoteAggregates(
		ctx context.Context, billIDs []string, revise func(stored []model.VoteAggregate) ([]model.VoteAggregate, error),
	) error
	// CountAggregateCohort counts the accounts in the cohort that aren't excluded yet.
	CountAggregateCohort(ctx context.Context, c AggregateCohort) (int64, error)
	// ExcludeAggregateCohort sets agg_excluded_at to at on the cohort's accounts that aren't
	// excluded yet, so their votes leave the aggregates at the job's next run. It's idempotent,
	// and returns how many accounts it excluded (a lower bound: partitioned DML).
	ExcludeAggregateCohort(ctx context.Context, c AggregateCohort, at time.Time) (int64, error)
	UpsertRepAlignments(ctx context.Context, rows []model.RepAlignment) error
	// AggregateSnapshot reads the eligible votes, grouped by bill, state and district, and the
	// ineligible ones by reason, in one read-only transaction.
	AggregateSnapshot(ctx context.Context, p AggregateSnapshotParams) (*AggregateSnapshot, error)
	// BillSeatPositions returns every member's position on the given bills, with their seat.
	BillSeatPositions(ctx context.Context, billIDs []string) ([]SeatPosition, error)

	// Law (docs/design/149-law-aware-assistant.md)
	// UpsertUSCSections writes sections, in as many commits as their size needs.
	UpsertUSCSections(ctx context.Context, rows []USCSectionRow) error
	// USCSectionHashes returns the content hash of every stored section of a US Code title, by
	// section ID, so a loader can skip the unchanged ones.
	USCSectionHashes(ctx context.Context, title int) (map[string]string, error)
	RecordUSCReleasePoint(ctx context.Context, rp USCReleasePointRow) error
	// CurrentUSCReleasePoint returns the most recently loaded release point, or nil.
	CurrentUSCReleasePoint(ctx context.Context) (*model.USCReleasePoint, error)
	// ReplaceBillLawRefs replaces one text version's references to law in one transaction.
	ReplaceBillLawRefs(ctx context.Context, billID, versionID string, rows []BillLawRefRow) error
	// ListLawRefSources returns a page of a congress's fetched bill texts for the law-reference
	// backfill: those after (afterBillID, afterVersionID), in bill and version ID order, at most
	// limit of them.
	ListLawRefSources(
		ctx context.Context, congress int, afterBillID, afterVersionID string, limit int,
	) ([]LawRefSource, error)
	// ReplaceBillLawChanges replaces the bill's explanations in one transaction.
	ReplaceBillLawChanges(ctx context.Context, billID string, rows []BillLawChangeRow) error
	// QueryBillsToExplainLaw returns up to q.Limit bills of q.Congress that are due for a
	// law-change explanation, one row per bill for its latest text version with stored text. A
	// bill is due when that version amends, repeals or adds a section of law, and its
	// explanations aren't of that text and prompt, or a section they cover was loaded after
	// them. An attempt for the same text, prompt and model holds it back as in
	// QueryBillsToSummarize. Laws come first (status became_law, or laws listed), then bills
	// that passed both chambers (to_president, signed, vetoed), each by status_date, newest
	// first; then the rest, those with roll-call votes first, then by status_date, newest first.
	// A NULL status_date comes last in its group, and bill_id breaks ties.
	QueryBillsToExplainLaw(ctx context.Context, q LawChangeQueueQuery) ([]LawChangeQueueItem, error)
	// CountBillsToExplainLaw counts the bills QueryBillsToExplainLaw would return with no limit.
	CountBillsToExplainLaw(ctx context.Context, q LawChangeQueueQuery) (int, error)
	// LoadLawChangeContext loads one bill version's input to a law-change explanation.
	LoadLawChangeContext(ctx context.Context, billID, versionID string) (*LawChangeContext, error)
	// RecordLawChangeAttempt replaces the bill's law_change_attempts row, with the retry rules
	// of RecordSummaryAttempt, and writes a.Changes when set.
	RecordLawChangeAttempt(ctx context.Context, a LawChangeAttemptRow) error
	// CountLawChangeAttemptsSince counts bills whose latest law-change attempt is at or after
	// since.
	CountLawChangeAttemptsSince(ctx context.Context, since time.Time) (int, error)

	// CRS summaries (docs/design/197-crs-summaries.md)
	// UpsertCRSSummaries writes summary versions keyed on (bill, version code), whether or not
	// the bill has been synced.
	UpsertCRSSummaries(ctx context.Context, rows []CRSSummaryRow) error
	// StoredCRSSummaries reads, for the given bills, every stored summary version's hash and
	// which bills have a bills row, in one consistent read.
	StoredCRSSummaries(ctx context.Context, billIDs []string) (StoredCRSSummaries, error)

	// Sync State
	GetSyncState(ctx context.Context, step string, congress int) (*SyncStateRow, error)
	// RecordSyncSuccess moves the watermark to the run's start (or its earlier Watermark),
	// stores ItemsSynced, clears last_offset and consecutive_failures, and keeps the error
	// history. A run with a Warning
	// also writes it to last_error and last_error_at, leaving the failure counts alone.
	RecordSyncSuccess(ctx context.Context, run SyncRun) error
	// RecordSyncFailure counts a failed run, whether or not the step has a row yet, and
	// leaves the watermark alone.
	RecordSyncFailure(ctx context.Context, run SyncRun) error
	// SaveSyncCheckpoint stores a resumable position (last_offset) and a progress count
	// without touching the watermark or the health columns.
	SaveSyncCheckpoint(ctx context.Context, step string, congress int, offset *string, items int) error

	// Sync retry (docs/design/67-upstream-quota-retries.md)
	// DueRetries returns up to limit of the step's items whose next attempt is at or before
	// now, the longest overdue first. Given-up items are never due.
	DueRetries(ctx context.Context, step string, congress int, now time.Time, limit int) ([]RetryItem, error)
	// RecordItemFailure adds one failed attempt to the item's row, creating it if needed, and
	// sets its next attempt with NextRetryAt.
	RecordItemFailure(ctx context.Context, f ItemFailure) error
	// ClearItemRetries deletes the items' rows, typically after they synced, in one write.
	// Items with no row are ignored.
	ClearItemRetries(ctx context.Context, step string, congress int, itemIDs []string) error
	// RetryStats counts the step's due, waiting and given-up items as of now.
	RetryStats(ctx context.Context, step string, congress int, now time.Time) (RetryStats, error)
}
