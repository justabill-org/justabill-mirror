package fixture

import (
	"time"

	"cloud.google.com/go/civil"
	"cloud.google.com/go/spanner"
)

// IDs of the rows only UpgradeTables seeds. The users are invented: one
// signed up since sign-in providers and App Check were recorded, one before.
const (
	upgradeUser       = "00000957-0001-4000-8000-000000000001"
	upgradeLegacyUser = "00000957-0001-4000-8000-000000000002"
	upgradeAction     = "00000957-0002-4000-8000-000000000001"
	upgradeDiff       = "00000957-0003-4000-8000-000000000001"
	upgradeBatch      = "00000957-0004-4000-8000-000000000001"
	// upgradeIntroduced is LawBill's introduced text, the diff's older side.
	upgradeIntroduced = "00000957-0005-4000-8000-000000000001"

	upgradeCommittee   = "hswm00"
	upgradeSubject     = "income-tax-credits"
	upgradePolicyArea  = "taxation"
	upgradeAmendment   = "hamdt-119-12"
	upgradeGAOReport   = "GAO-25-100001"
	upgradeReleasePt   = "119-12"
	upgradeUSCSection  = "/us/usc/t20/s9141"
	upgradeContentHash = "9f2c6d0e5b1a7c3e8d4f6a2b0c9e7d5f3a1b8c6e4d2f0a9b7c5e3d1f8a6b4c2e"
	upgradeModel       = "gemini-2.5-flash"
	upgradeScopeKey    = "CA-12"
	upgradeHolder      = "pipeline-serve-0"
	upgradeReleaseURL  = "https://uscode.house.gov/download/releasepoints/us/pl/119/12/xml_uscAll@119-12.zip"
	upgradeDiffPrompt  = "diff-v1"
	upgradeLawPrompt   = "law-v1"

	upgradeAmendmentNum = 12
	upgradeUSCTitle     = 20
	upgradeItemsSynced  = 6
	upgradeYeaPct       = 100
	upgradeLeaseTTL     = 10 * time.Minute
)

type committeeRow struct {
	CommitteeID   string `spanner:"committee_id"`
	Name          string `spanner:"name"`
	Chamber       string `spanner:"chamber"`
	CommitteeType string `spanner:"committee_type"`
}

type namedRow struct {
	ID   string `spanner:"subject_id"`
	Name string `spanner:"name"`
}

type policyAreaRow struct {
	ID   string `spanner:"policy_area_id"`
	Name string `spanner:"name"`
}

type sponsorshipRow struct {
	BillID        string           `spanner:"bill_id"`
	MemberID      string           `spanner:"member_id"`
	Role          string           `spanner:"role"`
	SponsoredDate spanner.NullDate `spanner:"sponsored_date"`
	IsOriginal    spanner.NullBool `spanner:"is_original"`
}

type billCommitteeRow struct {
	BillID       string    `spanner:"bill_id"`
	CommitteeID  string    `spanner:"committee_id"`
	Activity     string    `spanner:"activity"`
	ActivityDate time.Time `spanner:"activity_date"`
}

type billSubjectRow struct {
	BillID    string `spanner:"bill_id"`
	SubjectID string `spanner:"subject_id"`
}

type relationRow struct {
	BillID        string `spanner:"bill_id"`
	RelatedBillID string `spanner:"related_bill_id"`
	RelationType  string `spanner:"relation_type"`
	IdentifiedBy  string `spanner:"identified_by"`
}

type actionRow struct {
	BillID        string             `spanner:"bill_id"`
	ActionID      string             `spanner:"action_id"`
	ActionDate    civil.Date         `spanner:"action_date"`
	ActionText    string             `spanner:"action_text"`
	ActionType    string             `spanner:"action_type"`
	SourceSystem  string             `spanner:"source_system"`
	CommitteeCode spanner.NullString `spanner:"committee_code"`
	SortOrder     int64              `spanner:"sort_order"`
}

type diffRow struct {
	BillID        string           `spanner:"bill_id"`
	DiffID        string           `spanner:"diff_id"`
	FromVersionID string           `spanner:"from_version_id"`
	ToVersionID   string           `spanner:"to_version_id"`
	DiffStats     spanner.NullJSON `spanner:"diff_stats"`
	DiffContent   spanner.NullJSON `spanner:"diff_content"`
	GeneratedAt   time.Time        `spanner:"generated_at"`
	IsEmpty       bool             `spanner:"is_empty"`
}

type diffSummaryRow struct {
	DiffID        string    `spanner:"diff_id"`
	Summary       string    `spanner:"summary"`
	ModelUsed     string    `spanner:"model_used"`
	GeneratedAt   time.Time `spanner:"generated_at"`
	PromptVersion string    `spanner:"prompt_version"`
}

type amendmentRow struct {
	BillID          string           `spanner:"bill_id"`
	AmendmentID     string           `spanner:"amendment_id"`
	Congress        int64            `spanner:"congress"`
	AmendmentType   string           `spanner:"amendment_type"`
	AmendmentNumber int64            `spanner:"amendment_number"`
	Purpose         string           `spanner:"purpose"`
	SponsorID       string           `spanner:"sponsor_id"`
	SubmittedDate   spanner.NullDate `spanner:"submitted_date"`
	Chamber         string           `spanner:"chamber"`
}

// userRow leaves sign_in_provider, district and the timestamps since
// migrations NULL for the legacy user, as older code wrote them.
type userRow struct {
	UserID         string             `spanner:"user_id"`
	AuthUID        string             `spanner:"auth_uid"`
	State          spanner.NullString `spanner:"state"`
	District       spanner.NullInt64  `spanner:"district"`
	CreatedAt      time.Time          `spanner:"created_at"`
	SignInProvider spanner.NullString `spanner:"sign_in_provider"`
}

// userVoteRow's RecordedAt and AppCheckOK are NULL for a vote written before
// migration 21.
type userVoteRow struct {
	UserID     string           `spanner:"user_id"`
	BillID     string           `spanner:"bill_id"`
	Vote       string           `spanner:"vote"`
	VotedAt    time.Time        `spanner:"voted_at"`
	AppCheckOK spanner.NullBool `spanner:"app_check_ok"`
	RecordedAt spanner.NullTime `spanner:"recorded_at"`
}

type favoriteRow struct {
	UserID    string    `spanner:"user_id"`
	BillID    string    `spanner:"bill_id"`
	CreatedAt time.Time `spanner:"created_at"`
}

type gaoReportRow struct {
	ReportID      string           `spanner:"report_id"`
	Title         string           `spanner:"title"`
	ReportNumber  string           `spanner:"report_number"`
	PublishedDate spanner.NullDate `spanner:"published_date"`
}

type billGAORow struct {
	BillID   string    `spanner:"bill_id"`
	ReportID string    `spanner:"report_id"`
	LinkedAt time.Time `spanner:"linked_at"`
}

type syncStateRow struct {
	Step         string    `spanner:"step"`
	Congress     int64     `spanner:"congress"`
	LastSyncedAt time.Time `spanner:"last_synced_at"`
	ItemsSynced  int64     `spanner:"items_synced"`
	ErrorCount   int64     `spanner:"error_count"`
}

type aggregateRow struct {
	BillID      string            `spanner:"bill_id"`
	Scope       string            `spanner:"scope"`
	ScopeKey    string            `spanner:"scope_key"`
	Status      string            `spanner:"status"`
	YeaPct      spanner.NullInt64 `spanner:"yea_pct"`
	NayPct      spanner.NullInt64 `spanner:"nay_pct"`
	PublishedAt spanner.NullTime  `spanner:"published_at"`
	ComputedAt  time.Time         `spanner:"computed_at"`
}

type alignmentRow struct {
	MemberID      string    `spanner:"member_id"`
	Congress      int64     `spanner:"congress"`
	ScopeKey      string    `spanner:"scope_key"`
	BillsCompared int64     `spanner:"bills_compared"`
	BillsAgreed   int64     `spanner:"bills_agreed"`
	ComputedAt    time.Time `spanner:"computed_at"`
}

type retryRow struct {
	Step          string    `spanner:"step"`
	Congress      int64     `spanner:"congress"`
	ItemID        string    `spanner:"item_id"`
	Attempts      int64     `spanner:"attempts"`
	FirstFailedAt time.Time `spanner:"first_failed_at"`
	LastFailedAt  time.Time `spanner:"last_failed_at"`
	LastError     string    `spanner:"last_error"`
}

type leaseRow struct {
	Name       string    `spanner:"name"`
	Holder     string    `spanner:"holder"`
	AcquiredAt time.Time `spanner:"acquired_at"`
	ExpiresAt  time.Time `spanner:"expires_at"`
}

type releasePointRow struct {
	ReleasePoint string    `spanner:"release_point"`
	SourceURL    string    `spanner:"source_url"`
	LoadedAt     time.Time `spanner:"loaded_at"`
	SectionCount int64     `spanner:"section_count"`
}

type uscSectionRow struct {
	SectionID     string    `spanner:"section_id"`
	TitleNumber   int64     `spanner:"title_number"`
	SectionNumber string    `spanner:"section_number"`
	Heading       string    `spanner:"heading"`
	Text          string    `spanner:"text"`
	Status        string    `spanner:"status"`
	PositiveLaw   bool      `spanner:"positive_law"`
	ReleasePoint  string    `spanner:"release_point"`
	ContentHash   string    `spanner:"content_hash"`
	UpdatedAt     time.Time `spanner:"updated_at"`
}

type lawRefRow struct {
	BillID    string `spanner:"bill_id"`
	VersionID string `spanner:"version_id"`
	SectionID string `spanner:"section_id"`
	RefKind   string `spanner:"ref_kind"`
	CiteText  string `spanner:"cite_text"`
}

type lawChangeRow struct {
	BillID            string    `spanner:"bill_id"`
	SectionID         string    `spanner:"section_id"`
	ChangeKind        string    `spanner:"change_kind"`
	Explanation       string    `spanner:"explanation"`
	SourceContentHash string    `spanner:"source_content_hash"`
	ReleasePoint      string    `spanner:"release_point"`
	ModelUsed         string    `spanner:"model_used"`
	PromptVersion     string    `spanner:"prompt_version"`
	GeneratedAt       time.Time `spanner:"generated_at"`
}

// attemptRow is a row of summary_attempts or law_change_attempts.
type attemptRow struct {
	BillID        string    `spanner:"bill_id"`
	ContentHash   string    `spanner:"content_hash"`
	PromptVersion string    `spanner:"prompt_version"`
	Model         string    `spanner:"model"`
	Outcome       string    `spanner:"outcome"`
	Attempts      int64     `spanner:"attempts"`
	AttemptedAt   time.Time `spanner:"attempted_at"`
}

type diffAttemptRow struct {
	DiffID        string    `spanner:"diff_id"`
	PromptVersion string    `spanner:"prompt_version"`
	Model         string    `spanner:"model"`
	Outcome       string    `spanner:"outcome"`
	Attempts      int64     `spanner:"attempts"`
	AttemptedAt   time.Time `spanner:"attempted_at"`
}

type crsSummaryRow struct {
	BillID          string     `spanner:"bill_id"`
	VersionCode     string     `spanner:"version_code"`
	ActionDate      civil.Date `spanner:"action_date"`
	ActionDesc      string     `spanner:"action_desc"`
	TextHTML        string     `spanner:"text_html"`
	Text            string     `spanner:"text"`
	ContentHash     string     `spanner:"content_hash"`
	CRSUpdatedAt    time.Time  `spanner:"crs_updated_at"`
	SourceUpdatedAt time.Time  `spanner:"source_updated_at"`
	SyncedAt        time.Time  `spanner:"synced_at"`
}

type batchRow struct {
	BatchID       string    `spanner:"batch_id"`
	Congress      int64     `spanner:"congress"`
	Model         string    `spanner:"model"`
	PromptVersion string    `spanner:"prompt_version"`
	State         string    `spanner:"state"`
	BillCount     int64     `spanner:"bill_count"`
	InputURI      string    `spanner:"input_uri"`
	CreatedAt     time.Time `spanner:"created_at"`
}

// UnseededTables returns the tables in db/schema.sql that UpgradeTables
// leaves empty, each with the reason. Every other table gets at least one row.
func UnseededTables() map[string]string {
	return map[string]string{
		"SchemaMigrations": "wrench writes it as it migrates; a seeded row would change the database's version",
	}
}

// UpgradeTables returns Tables' rows plus a row or more in every other table
// production holds data in, parents before children, for the migration
// upgrade test (testdb.SeedFixtureForSchema). They stay out of Tables, so the
// integration and browser tests that seed the fixture see no users, votes or
// pipeline bookkeeping they didn't add. Rows written by older code keep NULL
// where it wrote nothing, such as a user vote from before migration 21.
func UpgradeTables() []Table {
	tables := Tables()
	tables = append(tables, billDetailTables()...)
	tables = append(tables, userTables()...)
	return append(tables, pipelineTables()...)
}

// upgradeTimes are the times the upgrade rows were written.
type upgradeTimes struct {
	introduced civil.Date
	amended    civil.Date
	reported   civil.Date
	enacted    civil.Date
	synced     time.Time
}

func newUpgradeTimes() upgradeTimes {
	return upgradeTimes{
		introduced: civil.DateOf(time.Date(2025, time.January, 3, 0, 0, 0, 0, time.UTC)),
		amended:    civil.DateOf(time.Date(2025, time.February, 12, 0, 0, 0, 0, time.UTC)),
		reported:   civil.DateOf(time.Date(2025, time.April, 13, 0, 0, 0, 0, time.UTC)),
		enacted:    civil.DateOf(time.Date(2025, time.July, 15, 0, 0, 0, 0, time.UTC)),
		synced:     time.Date(2025, time.August, 1, 6, 0, 0, 0, time.UTC),
	}
}

// billDetailTables returns the rows the pipeline adds around the fixture's
// bills: link tables, actions, a second text version and the diff to the
// enrolled one, an amendment and a GAO report.
func billDetailTables() []Table {
	at := newUpgradeTimes()
	referred := time.Date(2025, time.January, 3, 15, 0, 0, 0, time.UTC)
	return []Table{
		{"committees", []any{committeeRow{upgradeCommittee, "Ways and Means Committee", chamberHouse, "Standing"}}},
		{"subjects", []any{namedRow{upgradeSubject, "Income tax credits"}}},
		{"policy_areas", []any{policyAreaRow{upgradePolicyArea, "Taxation"}}},
		{"bill_sponsorships", []any{
			sponsorshipRow{HouseBill, HouseDem, "sponsor", spanner.NullDate{}, spanner.NullBool{}},
			sponsorshipRow{
				HouseBill, HouseRep, "cosponsor", spanner.NullDate{Date: at.introduced, Valid: true},
				spanner.NullBool{Bool: true, Valid: true},
			},
		}},
		{"bill_committees", []any{billCommitteeRow{HouseBill, upgradeCommittee, "Referred To", referred}}},
		{"bill_subjects", []any{billSubjectRow{HouseBill, upgradeSubject}}},
		{"bill_relations", []any{relationRow{HouseBill, SenateBill, "Identical bill", "CRS"}}},
		{"bill_actions", []any{actionRow{
			HouseBill, upgradeAction, at.introduced, "Referred to the House Committee on Ways and Means.",
			"IntroReferral", "House floor actions", spanner.NullString{StringVal: upgradeCommittee, Valid: true}, 1,
		}}},
		{"bill_text_versions", []any{textVersionRow{
			LawBill, upgradeIntroduced, "Introduced in House", "ih", at.introduced, spanner.NullJSON{}, 0,
		}}},
		{"bill_text_diffs", []any{diffRow{
			LawBill, upgradeDiff, upgradeIntroduced, LawTextVersion,
			jsonCol(`{"added":1,"removed":0,"changed":0}`), jsonCol(`[]`), at.synced, false,
		}}},
		{"bill_text_diff_summaries", []any{diffSummaryRow{
			upgradeDiff, "Adds a grant program for evening library hours.", upgradeModel, at.synced, upgradeDiffPrompt,
		}}},
		{"amendments", []any{amendmentRow{
			HouseBill, upgradeAmendment, Congress, "HAMDT", upgradeAmendmentNum, "To raise the credit's income limit.",
			HouseRep, spanner.NullDate{Date: at.amended, Valid: true}, chamberHouse,
		}}},
		{"gao_reports", []any{gaoReportRow{
			upgradeGAOReport, "Library Grants: Evening Hours Program", upgradeGAOReport,
			spanner.NullDate{Date: at.reported, Valid: true},
		}}},
		{"bill_gao_reports", []any{billGAORow{LawBill, upgradeGAOReport, at.synced}}},
	}
}

// userTables returns two users, their votes and a favorite. The legacy user
// and their vote are as code before migration 21 and sign-in providers wrote
// them.
func userTables() []Table {
	signedUp := time.Date(2025, time.February, 10, 18, 0, 0, 0, time.UTC)
	voted := signedUp.Add(time.Hour)
	legacySignUp := time.Date(2024, time.November, 6, 9, 30, 0, 0, time.UTC)
	legacyVoted := legacySignUp.Add(time.Hour)
	return []Table{
		{"users", []any{
			userRow{
				upgradeUser, "upgrade-auth-uid-1", spanner.NullString{StringVal: "CA", Valid: true},
				nullInt(distCA), signedUp, spanner.NullString{StringVal: "google.com", Valid: true},
			},
			userRow{
				upgradeLegacyUser, "upgrade-auth-uid-2", spanner.NullString{}, spanner.NullInt64{},
				legacySignUp, spanner.NullString{},
			},
		}},
		{"user_votes", []any{
			userVoteRow{
				upgradeUser, HouseBill, "yea", voted,
				spanner.NullBool{Bool: true, Valid: true}, spanner.NullTime{Time: voted, Valid: true},
			},
			userVoteRow{upgradeLegacyUser, LawBill, "nay", legacyVoted, spanner.NullBool{}, spanner.NullTime{}},
		}},
		{"user_favorites", []any{favoriteRow{upgradeUser, LawBill, voted}}},
	}
}

// pipelineTables returns the pipeline's bookkeeping and derived rows: sync
// state, aggregates, the US Code and law changes, summary attempts and batches.
func pipelineTables() []Table {
	at := newUpgradeTimes()
	synced := at.synced
	return []Table{
		{"diff_summary_attempts", []any{diffAttemptRow{upgradeDiff, upgradeDiffPrompt, upgradeModel, "ok", 1, synced}}},
		{"sync_state", []any{syncStateRow{"bills", Congress, synced, upgradeItemsSynced, 0}}},
		{"vote_aggregates", []any{
			aggregateRow{
				HouseBill, "national", "", "published", nullInt(upgradeYeaPct), nullInt(0),
				spanner.NullTime{Time: synced, Valid: true}, synced,
			},
			aggregateRow{
				HouseBill, "district", upgradeScopeKey, "suppressed", spanner.NullInt64{}, spanner.NullInt64{},
				spanner.NullTime{}, synced,
			},
		}},
		{"rep_alignment", []any{alignmentRow{HouseDem, Congress, upgradeScopeKey, 1, 1, synced}}},
		{"sync_retry", []any{retryRow{"bills", Congress, LawBill, 1, synced.Add(-time.Hour), synced, "status 503"}}},
		{"pipeline_leases", []any{leaseRow{"pipeline", upgradeHolder, synced, synced.Add(upgradeLeaseTTL)}}},
		{"usc_release_points", []any{releasePointRow{upgradeReleasePt, upgradeReleaseURL, synced, 1}}},
		{
			"usc_sections",
			[]any{
				uscSectionRow{
					upgradeUSCSection,
					upgradeUSCTitle,
					"9141",
					"Grants to States",
					"The Director shall award grants to States.",
					"current",
					false,
					upgradeReleasePt,
					upgradeContentHash,
					synced,
				},
			},
		},
		{"bill_law_refs", []any{lawRefRow{LawBill, LawTextVersion, upgradeUSCSection, "amends", "20 U.S.C. 9141"}}},
		{"bill_law_changes", []any{lawChangeRow{
			LawBill, upgradeUSCSection, "amends", "Lets the grants pay for evening hours.", upgradeContentHash,
			upgradeReleasePt, upgradeModel, upgradeLawPrompt, synced,
		}}},
		{"summary_attempts", []any{attemptRow{LawBill, upgradeContentHash, billPrompt, upgradeModel, "ok", 1, synced}}},
		{"law_change_attempts", []any{
			attemptRow{LawBill, upgradeContentHash, upgradeLawPrompt, upgradeModel, "ok", 1, synced},
		}},
		{"bill_crs_summaries", []any{crsSummaryRow{
			LawBill, lawVersionCode, at.enacted, "Public Law",
			"<p>This bill sets up library grants.</p>", "This bill sets up library grants.",
			upgradeContentHash, synced, synced, synced,
		}}},
		{"summary_batches", []any{batchRow{
			upgradeBatch, Congress, upgradeModel, billPrompt, "JOB_STATE_SUCCEEDED", 1,
			"gs://upgrade-test/batches/input.jsonl", synced,
		}}},
	}
}
