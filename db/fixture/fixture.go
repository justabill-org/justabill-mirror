// Package fixture holds a small, connected slice of Congress: the rows the Go
// integration tests seed through testdb.SeedFixture and the browser smoke
// tests seed through db/cmd/e2e-seed (docs/design/84-e2e-smoke-tests.md), so
// both share one picture of the world.
package fixture

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"cloud.google.com/go/civil"
	"cloud.google.com/go/spanner"
)

// IDs in the fixture. HR 1 and S 1 share a number on purpose, and HR 1 exists
// in two congresses, so tests catch bill identities that drop the type or the
// congress. HR 808 of the 119th became law and has a summary and a text version,
// so the browser tests can reach /vote's cards and the bill text reader.
const (
	PrevCongress = 118
	Congress     = 119

	HouseBill     = "hr-119-1"
	SenateBill    = "s-119-1"
	PrevHouseBill = "hr-118-1"
	LawBill       = "hr-119-808"

	// LawTextVersion is LawBill's one text version, the enrolled bill, and
	// LawText its text.
	LawTextVersion = "e2e00119-0002-4000-8000-000000000001"
	LawText        = "e2e00119-0002-4000-8000-000000000002"

	HouseDem   = "A000001"
	HouseRep   = "B000002"
	SenatorRep = "C000003"
	SenatorLIS = "S901"

	HouseVote  = "house-119-s1-roll001"
	SenateVote = "senate-119-s1-vote00001"

	// Title is the title HR 1 and S 1 of the 119th share.
	Title = "Companion Act"
	// LawTitle is LawBill's title.
	LawTitle = "Lamplight Library Hours Act"
)

// Relationship JSON in the shape the pipeline stores from Congress.gov, so the
// link-table backfill can be tested against it.
const (
	houseSponsors = `[{"bioguideId":"A000001","fullName":"Rep. Ada Alvarez [D-CA-12]",` +
		`"party":"D","state":"CA"}]`
	houseCosponsors = `[{"bioguideId":"B000002","fullName":"Rep. Ben Brooks [R-TX-7]",` +
		`"party":"R","state":"TX","sponsorshipDate":"2025-01-03","isOriginalCosponsor":true}]`
	senateSponsors = `[{"bioguideId":"C000003","fullName":"Sen. Cora Chen [R-OH]",` +
		`"party":"R","state":"OH"}]`
	houseCommittees = `[{"name":"Ways and Means Committee","systemCode":"hswm00",` +
		`"chamber":"House","type":"Standing","activities":[{"name":"Referred To","date":"2025-01-03T15:00:00Z"}]}]`
	subjects     = `["Taxation","Income tax credits"]`
	houseRelated = `[{"congress":119,"number":1,"type":"S","title":"Companion Act",` +
		`"relationshipDetails":[{"type":"Identical bill","identifiedBy":"CRS"}]}]`
	senateRelated = `[{"congress":119,"number":1,"type":"HR","title":"Companion Act",` +
		`"relationshipDetails":[{"type":"Identical bill","identifiedBy":"CRS"}]}]`
	lawSponsors = `[{"bioguideId":"B000002","fullName":"Rep. Ben Brooks [R-TX-7]",` +
		`"party":"R","state":"TX"}]`
	lawFormats = `[{"type":"Formatted XML",` +
		`"url":"https://www.congress.gov/119/bills/hr808/BILLS-119hr808enr.xml"}]`
)

// LawBill's text: the download and the sections the pipeline parses from it.
// A title with a section and a subsection, so the reader's contents and its
// nested sections both render.
const (
	lawXML = `<bill><legis-body><title><enum>I</enum><header>Library hours</header>` +
		`<section><enum>101.</enum><header>Short title</header><text>This Act may be cited as the ` +
		`Lamplight Library Hours Act.</text></section>` +
		`<section><enum>102.</enum><header>Evening hours grants</header>` +
		`<subsection><enum>(a)</enum><header>In general</header><text>The Director may make grants ` +
		`to public libraries to stay open in the evening.</text></subsection></section>` +
		`</title></legis-body></bill>`
	lawSections = `[{"id":"T1","kind":"title","enum":"I","header":"Library hours","content":"",` +
		`"children":[` +
		`{"id":"S101","kind":"section","enum":"101.","header":"Short title",` +
		`"content":"This Act may be cited as the Lamplight Library Hours Act."},` +
		`{"id":"S102","kind":"section","enum":"102.","header":"Evening hours grants","content":"",` +
		`"children":[{"id":"S102a","kind":"subsection","enum":"(a)","header":"In general",` +
		`"content":"The Director may make grants to public libraries to stay open in the evening."}]}]}]`
)

// LawBill's summary, written like the pipeline's (bill-v3, from the text, with no CRS summary).
const (
	lawShortSummary = "Lets public libraries apply for grants to stay open in the evening."
	lawLongSummary  = "The law sets up a grant program for public libraries. Libraries can use the " +
		"grants to open later on weekday evenings."
	lawWhoItAffects = "Public libraries and the people who use them after work or school."
)

const (
	chamberHouse  = "House"
	chamberSenate = "Senate"
	distCA        = 12
	distTX        = 7
	prevTitle     = "Earlier Act"

	statusIntroduced = "introduced"
	statusBecameLaw  = "became_law"
	// Ranks of the statuses in bill_status_history, as the pipeline writes them.
	rankIntroduced = 1
	rankBecameLaw  = 10
)

type congressRow struct {
	Number    int64      `spanner:"number"`
	StartDate civil.Date `spanner:"start_date"`
	EndDate   civil.Date `spanner:"end_date"`
	IsCurrent bool       `spanner:"is_current"`
}

type billRow struct {
	BillID         string             `spanner:"bill_id"`
	Congress       int64              `spanner:"congress"`
	BillType       string             `spanner:"bill_type"`
	Number         int64              `spanner:"number"`
	Title          string             `spanner:"title"`
	IntroducedDate civil.Date         `spanner:"introduced_date"`
	OriginChamber  string             `spanner:"origin_chamber"`
	Sponsors       spanner.NullJSON   `spanner:"sponsors"`
	Cosponsors     spanner.NullJSON   `spanner:"cosponsors"`
	Committees     spanner.NullJSON   `spanner:"committees"`
	Subjects       spanner.NullJSON   `spanner:"subjects"`
	RelatedBills   spanner.NullJSON   `spanner:"related_bills"`
	CurrentStatus  spanner.NullString `spanner:"current_status"`
	StatusDate     spanner.NullDate   `spanner:"status_date"`
}

type statusRow struct {
	BillID     string     `spanner:"bill_id"`
	Status     string     `spanner:"status"`
	StatusDate civil.Date `spanner:"status_date"`
	StatusRank int64      `spanner:"status_rank"`
}

type textVersionRow struct {
	BillID      string           `spanner:"bill_id"`
	VersionID   string           `spanner:"version_id"`
	VersionType string           `spanner:"version_type"`
	VersionCode string           `spanner:"version_code"`
	Date        civil.Date       `spanner:"date"`
	Formats     spanner.NullJSON `spanner:"formats"`
	SortOrder   int64            `spanner:"sort_order"`
}

type textRow struct {
	TextID      string           `spanner:"text_id"`
	VersionID   string           `spanner:"version_id"`
	Format      string           `spanner:"format"`
	Content     string           `spanner:"content"`
	ContentHash string           `spanner:"content_hash"`
	Sections    spanner.NullJSON `spanner:"sections"`
	FetchedAt   time.Time        `spanner:"fetched_at"`
}

type summaryRow struct {
	BillID            string    `spanner:"bill_id"`
	ShortSummary      string    `spanner:"short_summary"`
	LongSummary       string    `spanner:"long_summary"`
	WhoItAffects      string    `spanner:"why_it_matters"`
	ModelUsed         string    `spanner:"model_used"`
	GeneratedAt       time.Time `spanner:"generated_at"`
	SourceVersionID   string    `spanner:"source_version_id"`
	SourceVersionCode string    `spanner:"source_version_code"`
	SourceContentHash string    `spanner:"source_content_hash"`
	PromptVersion     string    `spanner:"prompt_version"`
}

type memberRow struct {
	BioguideID string             `spanner:"bioguide_id"`
	FirstName  string             `spanner:"first_name"`
	LastName   string             `spanner:"last_name"`
	LISID      spanner.NullString `spanner:"lis_id"`
}

type termRow struct {
	MemberID string            `spanner:"member_id"`
	Congress int64             `spanner:"congress"`
	Chamber  string            `spanner:"chamber"`
	State    string            `spanner:"state"`
	District spanner.NullInt64 `spanner:"district"`
	Party    string            `spanner:"party"`
}

type rollCallRow struct {
	VoteID     string    `spanner:"vote_id"`
	BillID     string    `spanner:"bill_id"`
	Congress   int64     `spanner:"congress"`
	Chamber    string    `spanner:"chamber"`
	RollNumber int64     `spanner:"roll_number"`
	VoteDate   time.Time `spanner:"vote_date"`
	Question   string    `spanner:"question"`
	Result     string    `spanner:"result"`
}

type memberVoteRow struct {
	VoteID   string `spanner:"vote_id"`
	MemberID string `spanner:"member_id"`
	Vote     string `spanner:"vote"`
}

// Table is one table's fixture rows, as structs with spanner tags.
type Table struct {
	Name string
	Rows []any
}

// Mutations returns inserts for every fixture row, parents before children.
func Mutations() ([]*spanner.Mutation, error) {
	var muts []*spanner.Mutation
	for _, table := range Tables() {
		for _, row := range table.Rows {
			m, err := spanner.InsertStruct(table.Name, row)
			if err != nil {
				return nil, fmt.Errorf("fixture: %s row: %w", table.Name, err)
			}
			muts = append(muts, m)
		}
	}
	return muts, nil
}

// Tables returns the fixture's rows, parents before children.
func Tables() []Table {
	var tables []Table
	add := func(table string, rows ...any) {
		tables = append(tables, Table{Name: table, Rows: rows})
	}

	prevStart := civil.DateOf(time.Date(2023, time.January, 3, 0, 0, 0, 0, time.UTC))
	curStart := civil.DateOf(time.Date(2025, time.January, 3, 0, 0, 0, 0, time.UTC))
	curEnd := civil.DateOf(time.Date(2027, time.January, 3, 0, 0, 0, 0, time.UTC))
	noJSON := spanner.NullJSON{}
	noStatus, noDate := spanner.NullString{}, spanner.NullDate{}
	law := lawRows(curStart)
	cra := craRows()

	add("congresses",
		congressRow{PrevCongress, prevStart, curStart, false},
		congressRow{Congress, curStart, curEnd, true},
	)
	bills := []any{
		billRow{
			HouseBill, Congress, "hr", 1, Title, curStart, chamberHouse,
			jsonCol(houseSponsors), jsonCol(houseCosponsors), jsonCol(houseCommittees),
			jsonCol(subjects), jsonCol(houseRelated), noStatus, noDate,
		},
		billRow{
			SenateBill, Congress, "s", 1, Title, curStart, chamberSenate,
			jsonCol(senateSponsors), noJSON, noJSON, jsonCol(subjects), jsonCol(senateRelated), noStatus, noDate,
		},
		billRow{
			PrevHouseBill, PrevCongress, "hr", 1, prevTitle, prevStart, chamberHouse,
			noJSON, noJSON, noJSON, noJSON, noJSON, noStatus, noDate,
		},
		law.bill,
	}
	add("bills", append(bills, cra.bills...)...)
	add("bill_status_history", law.statuses...)
	add("bill_text_versions", law.version)
	add("bill_texts", law.text)
	add("bill_summaries", law.summary)
	add("federal_register_documents", cra.document)
	add("bill_cra_rules", cra.rules...)
	add("members",
		memberRow{HouseDem, "Ada", "Alvarez", spanner.NullString{}},
		memberRow{HouseRep, "Ben", "Brooks", spanner.NullString{}},
		memberRow{
			SenatorRep,
			"Cora",
			"Chen",
			spanner.NullString{StringVal: SenatorLIS, Valid: true},
		},
	)
	add("member_terms",
		termRow{HouseDem, Congress, chamberHouse, "CA", nullInt(distCA), "D"},
		termRow{HouseRep, Congress, chamberHouse, "TX", nullInt(distTX), "R"},
		termRow{SenatorRep, Congress, chamberSenate, "OH", spanner.NullInt64{}, "R"},
	)
	add("congressional_votes",
		rollCallRow{
			HouseVote, HouseBill, Congress, chamberHouse, 1,
			time.Date(2025, time.March, 4, 17, 0, 0, 0, time.UTC), "On Passage", "Passed",
		},
		rollCallRow{
			SenateVote, SenateBill, Congress, chamberSenate, 1,
			time.Date(2025, time.April, 1, 17, 0, 0, 0, time.UTC), "On Passage of the Bill", "Bill Passed",
		},
	)
	add("member_votes",
		memberVoteRow{HouseVote, HouseDem, "Yea"},
		memberVoteRow{HouseVote, HouseRep, "Nay"},
		memberVoteRow{SenateVote, SenatorRep, "Yea"},
	)

	return tables
}

// lawFixture is LawBill and its child rows.
type lawFixture struct {
	bill     billRow
	statuses []any
	version  textVersionRow
	text     textRow
	summary  summaryRow
}

// lawRows returns LawBill, introduced on introduced and law since the
// summer, with its status history, its enrolled text and a summary of it.
func lawRows(introduced civil.Date) lawFixture {
	enacted := civil.DateOf(time.Date(2025, time.July, 15, 0, 0, 0, 0, time.UTC))
	fetched := time.Date(2025, time.July, 20, 12, 0, 0, 0, time.UTC)
	hash := sha256.Sum256([]byte(lawXML))
	contentHash := hex.EncodeToString(hash[:])

	return lawFixture{
		bill: billRow{
			LawBill, Congress, "hr", 808, LawTitle, introduced, chamberHouse,
			jsonCol(lawSponsors), spanner.NullJSON{}, spanner.NullJSON{}, spanner.NullJSON{}, spanner.NullJSON{},
			spanner.NullString{StringVal: statusBecameLaw, Valid: true},
			spanner.NullDate{Date: enacted, Valid: true},
		},
		statuses: []any{
			statusRow{LawBill, statusIntroduced, introduced, rankIntroduced},
			statusRow{LawBill, statusBecameLaw, enacted, rankBecameLaw},
		},
		version: textVersionRow{
			LawBill, LawTextVersion, "Enrolled Bill", "enr", enacted, jsonCol(lawFormats), 1,
		},
		text: textRow{
			LawText, LawTextVersion, "Formatted XML", lawXML, contentHash, jsonCol(lawSections), fetched,
		},
		summary: summaryRow{
			LawBill, lawShortSummary, lawLongSummary, lawWhoItAffects, "gemini-2.5-flash", fetched,
			LawTextVersion, "enr", contentHash, "bill-v3",
		},
	}
}

func jsonCol(s string) spanner.NullJSON {
	return spanner.NullJSON{Value: json.RawMessage(s), Valid: true}
}

func nullInt(v int64) spanner.NullInt64 { return spanner.NullInt64{Int64: v, Valid: true} }
