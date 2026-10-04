package spannerdb_test

import (
	"reflect"
	"testing"
	"time"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

type spotcheckVoteRow struct {
	VoteID     string             `spanner:"vote_id"`
	BillID     spanner.NullString `spanner:"bill_id"`
	Congress   int64              `spanner:"congress"`
	Chamber    string             `spanner:"chamber"`
	Session    int64              `spanner:"session"`
	RollNumber int64              `spanner:"roll_number"`
	VoteDate   time.Time          `spanner:"vote_date"`
}

type spotcheckVersionRow struct {
	BillID      string `spanner:"bill_id"`
	VersionID   string `spanner:"version_id"`
	VersionType string `spanner:"version_type"`
	VersionCode string `spanner:"version_code"`
	SortOrder   int64  `spanner:"sort_order"`
}

type spotcheckTextRow struct {
	TextID      string `spanner:"text_id"`
	VersionID   string `spanner:"version_id"`
	Format      string `spanner:"format"`
	Content     string `spanner:"content"`
	ContentHash string `spanner:"content_hash"`
}

type spotcheckSummaryRow struct {
	BillID       string `spanner:"bill_id"`
	ShortSummary string `spanner:"short_summary"`
}

// newSpotcheckReader seeds the fixture plus rows for every coverage count: session-numbered
// roll calls with a gap, a vote on a bill that isn't stored, a senator with no LIS ID, one
// summary and two text versions, one of them without text.
func newSpotcheckReader(t *testing.T) *spannerdb.SpotcheckReader {
	t.Helper()
	client := testdb.New(t)
	ctx := t.Context()
	testdb.SeedFixture(ctx, t, client)
	testdb.SeedMember(ctx, t, client, "D000004", "Dana", "Diaz")
	testdb.SeedMemberTerm(ctx, t, client, "D000004", testdb.FixtureCongress, "Senate", "OH", nil, "D")

	when := time.Date(2026, time.March, 4, 17, 0, 0, 0, time.UTC)
	muts := []*spanner.Mutation{
		mustInsertStruct(t, "congressional_votes", spotcheckVoteRow{
			"house-119-s2-roll001", nullStr(testdb.FixtureHouseBill), 119, "House", 2, 1, when}),
		mustInsertStruct(t, "congressional_votes", spotcheckVoteRow{
			"house-119-s2-roll003", spanner.NullString{}, 119, "House", 2, 3, when}),
		mustInsertStruct(t, "congressional_votes", spotcheckVoteRow{
			"senate-119-s1-vote00002", nullStr("sres-119-9"), 119, "Senate", 1, 2, when}),
		mustInsertStruct(t, "congressional_votes", spotcheckVoteRow{
			"house-118-s2-roll009", nullStr(testdb.FixturePrevHouseBill), 118, "House", 2, 9, when}),
		mustInsertStruct(t, "bill_summaries", spotcheckSummaryRow{testdb.FixtureHouseBill, "A short summary."}),
		mustInsertStruct(
			t,
			"bill_text_versions",
			spotcheckVersionRow{testdb.FixtureHouseBill, "v1", "Introduced", "ih", 1},
		),
		mustInsertStruct(
			t,
			"bill_text_versions",
			spotcheckVersionRow{testdb.FixtureHouseBill, "v2", "Engrossed", "eh", 2},
		),
		mustInsertStruct(t, "bill_texts", spotcheckTextRow{"t1", "v1", "xml", "<bill/>", "hash"}),
	}
	if _, err := client.Apply(ctx, muts); err != nil {
		t.Fatal(err)
	}

	r := spannerdb.NewSpotcheckReader(&spannerdb.Client{Spanner: client})
	t.Cleanup(r.Close)
	return r
}

func TestSpotcheckStoredRollCall(t *testing.T) {
	r := newSpotcheckReader(t)

	got, err := r.StoredRollCall(t.Context(), testdb.FixtureSenateVote)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil {
		t.Fatal("fixture Senate vote not found")
	}
	if got.BillID == nil || *got.BillID != testdb.FixtureSenateBill ||
		got.Question == nil || *got.Question != "On Passage of the Bill" ||
		got.Result == nil || *got.Result != "Bill Passed" ||
		!got.VoteDate.Equal(time.Date(2025, time.April, 1, 17, 0, 0, 0, time.UTC)) {
		t.Errorf("vote = %+v", got)
	}
	want := []repository.StoredPosition{
		{MemberID: testdb.FixtureSenatorRep, LISID: testdb.FixtureSenatorLIS, Vote: "Yea"},
	}
	if !reflect.DeepEqual(got.Positions, want) {
		t.Errorf("positions = %+v, want %+v", got.Positions, want)
	}

	house, err := r.StoredRollCall(t.Context(), testdb.FixtureHouseVote)
	if err != nil {
		t.Fatal(err)
	}
	if len(house.Positions) != 2 || house.Positions[0].LISID != "" {
		t.Errorf("house positions = %+v, want two without LIS IDs", house.Positions)
	}

	missing, err := r.StoredRollCall(t.Context(), "house-119-s2-roll999")
	if err != nil || missing != nil {
		t.Errorf("missing roll call = %+v, %v; want nil, nil", missing, err)
	}
}

func TestSpotcheckCoverage(t *testing.T) {
	r := newSpotcheckReader(t)

	got, err := r.Coverage(t.Context(), testdb.FixtureCongress)
	if err != nil {
		t.Fatal(err)
	}
	want := &repository.StoredCoverage{
		BillsByType: map[string]int{"hr": 2, "s": 1, "hjres": 1, "sjres": 1},
		// The fixture's own roll calls have no session, so they aren't counted.
		RollCalls: []repository.StoredRollCallCount{
			{Chamber: "House", Session: 2, Distinct: 2, Highest: 3},
			{Chamber: "Senate", Session: 1, Distinct: 1, Highest: 2},
		},
		MembersByChamber:         map[string]int{"House": 2, "Senate": 2},
		SenatorsWithoutLISID:     []string{"D000004"},
		VotedBills:               3,
		MissingVotedBills:        []string{"sres-119-9"},
		VotedBillsWithoutSummary: []string{testdb.FixtureSenateBill},
		TextVersions:             3,
		VersionsWithoutText:      1,
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("coverage =\n%+v\nwant\n%+v", got, want)
	}
}
