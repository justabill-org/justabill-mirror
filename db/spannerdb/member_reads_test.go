package spannerdb_test

import (
	"maps"
	"slices"
	"testing"
	"time"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

// The read paths behind a member's page and a roll call's breakdown, and the pipeline's vote and
// LIS lookups (#225).

func TestMemberGetByID(t *testing.T) {
	client := testdb.New(t)
	ctx := t.Context()
	testdb.SeedFixture(ctx, t, client)
	// Ada's earlier term, so the terms come back newest first.
	testdb.SeedMemberTerm(ctx, t, client, testdb.FixtureHouseDem, testdb.FixturePrevCongress, "House", "CA",
		new(12), "D")
	repo := spannerdb.NewMemberRepo(&spannerdb.Client{Spanner: client})

	m, err := repo.GetByID(ctx, testdb.FixtureHouseDem)
	if err != nil {
		t.Fatal(err)
	}
	if m == nil {
		t.Fatalf("GetByID(%s) = nil, want the member", testdb.FixtureHouseDem)
	}
	if m.FirstName != "Ada" || m.LastName != "Alvarez" || m.BirthYear != nil || m.PhotoURL != nil {
		t.Errorf("GetByID = %+v, want Ada Alvarez with no birth year or photo", m)
	}
	var congresses []int
	for _, term := range m.Terms {
		congresses = append(congresses, term.Congress)
	}
	if !slices.Equal(congresses, []int{testdb.FixtureCongress, testdb.FixturePrevCongress}) {
		t.Fatalf("terms = %v, want [119 118]", congresses)
	}
	if term := m.Terms[0]; term.Chamber != "House" || term.State != "CA" || term.District == nil ||
		*term.District != 12 || term.Party != "D" || term.EndDate != nil {
		t.Errorf("current term = %+v, want House CA-12, D, no end date", term)
	}

	// A senator has no district.
	senator, err := repo.GetByID(ctx, testdb.FixtureSenatorRep)
	if err != nil || senator == nil || len(senator.Terms) != 1 || senator.Terms[0].District != nil {
		t.Errorf("GetByID(senator) = %+v, %v; want one term with no district", senator, err)
	}
	if missing, getErr := repo.GetByID(ctx, "Z999999"); getErr != nil || missing != nil {
		t.Errorf("GetByID(missing) = %+v, %v; want nil, nil", missing, getErr)
	}
}

func TestMemberGetRecentVotes(t *testing.T) {
	client := testdb.New(t)
	ctx := t.Context()
	testdb.SeedFixture(ctx, t, client)
	// A later roll call with no bill (a Speaker election) and an earlier one on a bill that isn't
	// stored yet.
	testdb.SeedCongressionalVote(ctx, t, client, "house-119-s1-roll009", nil, testdb.FixtureCongress, "House",
		time.Date(2025, time.May, 1, 17, 0, 0, 0, time.UTC))
	testdb.SeedMemberVote(ctx, t, client, "house-119-s1-roll009", testdb.FixtureHouseDem, "Jeffries")
	testdb.SeedCongressionalVote(ctx, t, client, "house-119-s1-roll000", new("hr-119-77"),
		testdb.FixtureCongress, "House", time.Date(2025, time.February, 1, 17, 0, 0, 0, time.UTC))
	testdb.SeedMemberVote(ctx, t, client, "house-119-s1-roll000", testdb.FixtureHouseDem, "Nay")
	repo := spannerdb.NewMemberRepo(&spannerdb.Client{Spanner: client})

	got, err := repo.GetRecentVotes(ctx, testdb.FixtureHouseDem, 10)
	if err != nil {
		t.Fatal(err)
	}
	if ids := voteIDs(got); !slices.Equal(ids, []string{"house-119-s1-roll009", testdb.FixtureHouseVote,
		"house-119-s1-roll000"}) {
		t.Fatalf("GetRecentVotes = %v, want newest first", ids)
	}
	speaker, passage, unstored := got[0], got[1], got[2]
	if speaker.BillID != nil || speaker.BillTitle != nil || speaker.MemberVote != "Jeffries" {
		t.Errorf("speaker vote = %+v, want no bill and the member's pick", speaker)
	}
	if deref(passage.BillID) != testdb.FixtureHouseBill || deref(passage.BillTitle) != "Companion Act" ||
		deref(passage.Question) != "On Passage" || deref(passage.Result) != "Passed" ||
		passage.MemberVote != "Yea" || passage.Chamber != "House" || passage.VoteDate.IsZero() {
		t.Errorf("passage vote = %+v, want HR 1's passage, Yea", passage)
	}
	if deref(unstored.BillID) != "hr-119-77" || unstored.BillTitle != nil {
		t.Errorf("vote on an unstored bill = %+v, want its bill ID and no title", unstored)
	}

	if got, err = repo.GetRecentVotes(ctx, testdb.FixtureHouseDem, 1); err != nil {
		t.Fatal(err)
	}
	if ids := voteIDs(got); !slices.Equal(ids, []string{"house-119-s1-roll009"}) {
		t.Errorf("GetRecentVotes(limit 1) = %v, want only the newest", ids)
	}
	none, err := repo.GetRecentVotes(ctx, "Z999999", 10)
	if err != nil || none == nil || len(none) != 0 {
		t.Errorf("GetRecentVotes(missing) = %v, %v; want an empty, non-nil list", none, err)
	}
}

// Votes on the same day come back in the order scoring.later uses: session, then roll number,
// then vote ID, so the member page doesn't shuffle them between loads (#788). The IDs sort the
// other way from the roll numbers, so ordering by vote ID alone fails too.
func TestMemberGetRecentVotesSameDay(t *testing.T) {
	client := testdb.New(t)
	ctx := t.Context()
	testdb.SeedFixture(ctx, t, client)
	type rollCall struct {
		VoteID     string    `spanner:"vote_id"`
		Congress   int64     `spanner:"congress"`
		Chamber    string    `spanner:"chamber"`
		Session    int64     `spanner:"session"`
		RollNumber int64     `spanner:"roll_number"`
		VoteDate   time.Time `spanner:"vote_date"`
	}
	sameDay := time.Date(2026, time.January, 3, 17, 0, 0, 0, time.UTC)
	var muts []*spanner.Mutation
	for _, rc := range []rollCall{
		{"house-119-s1-roll999", testdb.FixtureCongress, "House", 1, 999, sameDay},
		{"house-119-s2-roll001", testdb.FixtureCongress, "House", 2, 1, sameDay},
		{"house-119-s1-roll1000", testdb.FixtureCongress, "House", 1, 1000, sameDay},
	} {
		m, err := spanner.InsertStruct("congressional_votes", rc)
		if err != nil {
			t.Fatal(err)
		}
		muts = append(muts, m, spanner.Insert("member_votes", []string{"vote_id", "member_id", "vote"},
			[]any{rc.VoteID, testdb.FixtureHouseDem, "Yea"}))
	}
	if _, err := client.Apply(ctx, muts); err != nil {
		t.Fatal(err)
	}
	repo := spannerdb.NewMemberRepo(&spannerdb.Client{Spanner: client})

	got, err := repo.GetRecentVotes(ctx, testdb.FixtureHouseDem, 10)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"house-119-s2-roll001", "house-119-s1-roll1000", "house-119-s1-roll999",
		testdb.FixtureHouseVote}
	if ids := voteIDs(got); !slices.Equal(ids, want) {
		t.Errorf("GetRecentVotes = %v, want %v", ids, want)
	}
}

func voteIDs(votes []model.MemberVoteSummary) []string {
	ids := make([]string, 0, len(votes))
	for _, v := range votes {
		ids = append(ids, v.VoteID)
	}
	return ids
}

func TestVoteGetMemberVotes(t *testing.T) {
	client := testdb.New(t)
	ctx := t.Context()
	testdb.SeedFixture(ctx, t, client)
	repo := spannerdb.NewVoteRepo(&spannerdb.Client{Spanner: client})

	got, err := repo.GetMemberVotes(ctx, testdb.FixtureHouseVote)
	if err != nil {
		t.Fatal(err)
	}
	byMember := map[string]string{}
	for _, v := range got {
		if v.CongressionalVoteID != testdb.FixtureHouseVote {
			t.Errorf("GetMemberVotes returned %+v from another vote", v)
		}
		byMember[v.MemberID] = v.Vote
	}
	want := map[string]string{testdb.FixtureHouseDem: "Yea", testdb.FixtureHouseRep: "Nay"}
	if !maps.Equal(byMember, want) {
		t.Errorf("GetMemberVotes = %v, want %v", byMember, want)
	}

	// A voice vote has no member rows, like a vote that doesn't exist.
	testdb.SeedCongressionalVote(ctx, t, client, "house-119-voice-20250601", new(testdb.FixtureHouseBill),
		testdb.FixtureCongress, "House", time.Date(2025, time.June, 1, 17, 0, 0, 0, time.UTC))
	for _, id := range []string{"house-119-voice-20250601", "no-such-vote"} {
		none, getErr := repo.GetMemberVotes(ctx, id)
		if getErr != nil || none == nil || len(none) != 0 {
			t.Errorf("GetMemberVotes(%s) = %v, %v; want an empty, non-nil list", id, none, getErr)
		}
	}
}

func TestPipelineExistingVoteIDs(t *testing.T) {
	store, _ := newLinkStore(t)
	ctx := t.Context()

	got, err := store.ExistingVoteIDs(ctx, []string{testdb.FixtureHouseVote, "house-119-s1-roll999",
		testdb.FixtureSenateVote})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]bool{testdb.FixtureHouseVote: true, testdb.FixtureSenateVote: true}
	if !maps.Equal(got, want) {
		t.Errorf("ExistingVoteIDs = %v, want %v", got, want)
	}
	if got, err = store.ExistingVoteIDs(ctx, nil); err != nil || len(got) != 0 {
		t.Errorf("ExistingVoteIDs(nil) = %v, %v; want an empty map", got, err)
	}
}

func TestPipelineLISLookup(t *testing.T) {
	store, _ := newLinkStore(t)
	ctx := t.Context()

	got, err := store.LISLookup(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{testdb.FixtureSenatorLIS: testdb.FixtureSenatorRep}; !maps.Equal(got, want) {
		t.Fatalf("LISLookup = %v, want %v", got, want)
	}

	// UpdateMemberLisID fills in a missing LIS ID but never replaces one; a missing member is a
	// no-op.
	if err = store.UpdateMemberLisID(ctx, testdb.FixtureHouseRep, "S902"); err != nil {
		t.Fatal(err)
	}
	if err = store.UpdateMemberLisID(ctx, testdb.FixtureSenatorRep, "S903"); err != nil {
		t.Fatal(err)
	}
	if err = store.UpdateMemberLisID(ctx, "Z999999", "S904"); err != nil {
		t.Fatal(err)
	}
	if got, err = store.LISLookup(ctx); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{testdb.FixtureSenatorLIS: testdb.FixtureSenatorRep, "S902": testdb.FixtureHouseRep}
	if !maps.Equal(got, want) {
		t.Errorf("LISLookup after UpdateMemberLisID = %v, want %v", got, want)
	}
}
