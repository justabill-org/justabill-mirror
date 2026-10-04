package spannerdb_test

import (
	"reflect"
	"testing"
	"time"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/scoring"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

type rollCallRow struct {
	VoteID     string             `spanner:"vote_id"`
	BillID     spanner.NullString `spanner:"bill_id"`
	Congress   int64              `spanner:"congress"`
	Chamber    string             `spanner:"chamber"`
	RollNumber spanner.NullInt64  `spanner:"roll_number"`
	VoteDate   time.Time          `spanner:"vote_date"`
	Question   spanner.NullString `spanner:"question"`
}

func roll(n int64) spanner.NullInt64 { return spanner.NullInt64{Int64: n, Valid: true} }

type memberVoteRow struct {
	VoteID   string `spanner:"vote_id"`
	MemberID string `spanner:"member_id"`
	Vote     string `spanner:"vote"`
}

func nullStr(s string) spanner.NullString { return spanner.NullString{StringVal: s, Valid: true} }

func day(month time.Month, d int) time.Time { return time.Date(2025, month, d, 17, 0, 0, 0, time.UTC) }

// seedPositionVotes adds roll calls to the fixture's House vote on HR 1 (Ada Yea, Ben Nay on
// 2025-03-04) that exercise each part of the positions rule.
func seedPositionVotes(t *testing.T, client *spanner.Client) {
	t.Helper()
	house := "House"
	rolls := []rollCallRow{
		// A later procedural roll call on HR 1, recorded Aye/No: it isn't a final vote, so the
		// fixture's passage vote still decides both members' positions.
		{"house-119-s1-roll002", nullStr(testdb.FixtureHouseBill), 119, house, roll(2), day(time.March, 5),
			nullStr("On Motion to Recommit")},
		{"house-119-s1-roll003", nullStr("hr-119-2"), 119, house, roll(3), day(time.June, 1), nullStr("On Passage")},
		// The latest final vote on HR 2 has a value that isn't yea/nay/present/absent: it
		// decides, as other. HR 3's roll call has no question, so it isn't a final vote.
		{"house-119-s1-roll005", nullStr("hr-119-2"), 119, house, roll(5), day(time.June, 3), nullStr("On Passage")},
		{"house-119-s1-roll004", nullStr("hr-119-3"), 119, house, roll(4), day(time.June, 2), spanner.NullString{}},
		// Voice votes have no roll number. HR 4's has no member rows (#188); HR 1's is the
		// latest vote on it and carries the synthetic Yeas the pipeline wrote before #188.
		{"house-119-voice-20250701", nullStr("hr-119-4"), 119, house, spanner.NullInt64{}, day(time.July, 1),
			nullStr("Voice Vote: On passage")},
		{"house-119-voice-20250801", nullStr(testdb.FixtureHouseBill), 119, house, spanner.NullInt64{},
			day(time.August, 1), nullStr("Voice Vote: On passage")},
		// No bill (the Speaker election).
		{"house-119-s1-roll000", spanner.NullString{}, 119, house, roll(0), day(time.January, 3),
			nullStr("Election of the Speaker")},
		// Another congress.
		{"house-118-s1-roll001", nullStr(testdb.FixturePrevHouseBill), 118, house, roll(1),
			time.Date(2023, time.March, 1, 17, 0, 0, 0, time.UTC), nullStr("On Passage")},
	}
	votes := []memberVoteRow{
		{"house-119-s1-roll002", testdb.FixtureHouseDem, "No"},
		{"house-119-s1-roll002", testdb.FixtureHouseRep, "Aye"},
		{"house-119-s1-roll003", testdb.FixtureHouseDem, "Present"},
		{"house-119-s1-roll005", testdb.FixtureHouseDem, "Guilty"},
		{"house-119-s1-roll004", testdb.FixtureHouseDem, "Not Voting"},
		{"house-119-voice-20250801", testdb.FixtureHouseDem, "Yea"},
		{"house-119-voice-20250801", testdb.FixtureHouseRep, "Yea"},
		{"house-119-s1-roll000", testdb.FixtureHouseDem, "Jeffries"},
		{"house-118-s1-roll001", testdb.FixtureHouseDem, "Yea"},
	}
	var muts []*spanner.Mutation
	for _, row := range rolls {
		muts = append(muts, mustInsertStruct(t, "congressional_votes", row))
	}
	for _, row := range votes {
		muts = append(muts, mustInsertStruct(t, "member_votes", row))
	}
	if _, err := client.Apply(t.Context(), muts); err != nil {
		t.Fatalf("seed roll calls: %v", err)
	}
}

func mustInsertStruct(t *testing.T, table string, row any) *spanner.Mutation {
	t.Helper()
	m, err := spanner.InsertStruct(table, row)
	if err != nil {
		t.Fatalf("%s row: %v", table, err)
	}
	return m
}

func TestMemberPositions(t *testing.T) {
	client := testdb.New(t)
	testdb.SeedFixture(t.Context(), t, client)
	seedPositionVotes(t, client)
	repo := spannerdb.NewVoteRepo(&spannerdb.Client{Spanner: client})

	tests := []struct {
		name     string
		member   string
		congress int
		want     []model.MemberPosition
	}{
		{
			name:     "latest final vote per bill, normalized, procedural and voice votes ignored, newest first",
			member:   testdb.FixtureHouseDem,
			congress: testdb.FixtureCongress,
			want: []model.MemberPosition{
				{BillID: "hr-119-2", Vote: scoring.Other, VoteID: "house-119-s1-roll005",
					Chamber: "House", VoteDate: day(time.June, 3), Question: new("On Passage")},
				{BillID: testdb.FixtureHouseBill, Vote: model.PositionYea, VoteID: testdb.FixtureHouseVote,
					Chamber: "House", VoteDate: day(time.March, 4), Question: new("On Passage")},
			},
		},
		{
			name: "nay", member: testdb.FixtureHouseRep, congress: testdb.FixtureCongress,
			want: []model.MemberPosition{
				{BillID: testdb.FixtureHouseBill, Vote: model.PositionNay, VoteID: testdb.FixtureHouseVote,
					Chamber: "House", VoteDate: day(time.March, 4), Question: new("On Passage")},
			},
		},
		{
			name: "only the requested congress", member: testdb.FixtureHouseDem, congress: testdb.FixturePrevCongress,
			want: []model.MemberPosition{
				{
					BillID:   testdb.FixturePrevHouseBill,
					Vote:     model.PositionYea,
					VoteID:   "house-118-s1-roll001",
					Chamber:  "House",
					VoteDate: time.Date(2023, time.March, 1, 17, 0, 0, 0, time.UTC),
					Question: new("On Passage"),
				},
			},
		},
		{
			name: "congress 0 is every congress", member: testdb.FixtureHouseDem, congress: 0,
			want: []model.MemberPosition{
				{BillID: "hr-119-2", Vote: scoring.Other, VoteID: "house-119-s1-roll005",
					Chamber: "House", VoteDate: day(time.June, 3), Question: new("On Passage")},
				{BillID: testdb.FixtureHouseBill, Vote: model.PositionYea, VoteID: testdb.FixtureHouseVote,
					Chamber: "House", VoteDate: day(time.March, 4), Question: new("On Passage")},
				{
					BillID:   testdb.FixturePrevHouseBill,
					Vote:     model.PositionYea,
					VoteID:   "house-118-s1-roll001",
					Chamber:  "House",
					VoteDate: time.Date(2023, time.March, 1, 17, 0, 0, 0, time.UTC),
					Question: new("On Passage"),
				},
			},
		},
		{name: "unknown member", member: "Z999999", congress: testdb.FixtureCongress, want: []model.MemberPosition{}},
		{name: "no votes in congress", member: testdb.FixtureHouseRep, congress: testdb.FixturePrevCongress,
			want: []model.MemberPosition{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := repo.MemberPositions(t.Context(), tt.member, tt.congress)
			if err != nil {
				t.Fatalf("MemberPositions: %v", err)
			}
			for i := range got {
				got[i].VoteDate = got[i].VoteDate.UTC()
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("positions = %+v\nwant %+v", got, tt.want)
			}
		})
	}
}

func TestMemberVotesMemberIndex(t *testing.T) {
	client := testdb.New(t)
	got := queryStrings(t, client, `SELECT index_name FROM information_schema.indexes
		WHERE table_name = 'member_votes' AND index_type = 'INDEX'`, nil)
	if !reflect.DeepEqual(got, []string{"idx_member_votes_member"}) {
		t.Errorf("member_votes indexes = %v, want [idx_member_votes_member]", got)
	}
}

// TestGetCongressionalVotes_NewestFirst checks a bill's Votes tab lists its roll calls newest first,
// with the later roll number first on the same day (#453).
func TestGetCongressionalVotes_NewestFirst(t *testing.T) {
	client := testdb.New(t)
	testdb.SeedFixture(t.Context(), t, client)
	house, bill := "House", nullStr(testdb.FixtureHouseBill)
	sameDay := day(time.March, 10)
	var muts []*spanner.Mutation
	for _, row := range []rollCallRow{
		{"house-119-s1-roll020", bill, 119, house, roll(20), sameDay, nullStr("On Motion to Recommit")},
		{"house-119-s1-roll021", bill, 119, house, roll(21), sameDay, nullStr("On Passage")},
		{"house-119-s1-roll010", bill, 119, house, roll(10), day(time.February, 1), nullStr("On the Rule")},
	} {
		muts = append(muts, mustInsertStruct(t, "congressional_votes", row))
	}
	if _, err := client.Apply(t.Context(), muts); err != nil {
		t.Fatalf("seed roll calls: %v", err)
	}

	votes, err := spannerdb.NewVoteRepo(&spannerdb.Client{Spanner: client}).
		GetCongressionalVotes(t.Context(), testdb.FixtureHouseBill)
	if err != nil {
		t.Fatalf("GetCongressionalVotes: %v", err)
	}
	var got []string
	for _, v := range votes {
		got = append(got, v.ID)
	}
	// The fixture's own House vote on HR 1 is on 2025-03-04, between the two seeded days.
	want := []string{"house-119-s1-roll021", "house-119-s1-roll020", testdb.FixtureHouseVote, "house-119-s1-roll010"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("vote ids = %v, want %v", got, want)
	}
}
