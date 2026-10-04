package spannerdb_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/testdb"
)

func houseRollCall(id string, roll, yeas, nays int) repository.CongressionalVoteRow {
	present, notVoting := 0, 0
	return repository.CongressionalVoteRow{
		ID: id, Congress: testdb.FixtureCongress, Chamber: "House", Session: new(1), RollNumber: &roll,
		VoteDate: time.Date(2025, time.June, 2, 17, 0, 0, 0, time.UTC),
		Yeas:     &yeas, Nays: &nays, Present: &present, NotVoting: &notVoting,
	}
}

func TestStoreRollCall(t *testing.T) {
	store, client := newLinkStore(t)
	ctx := t.Context()
	const id = "house-119-s1-roll900"

	err := store.StoreRollCall(ctx, houseRollCall(id, 900, 1, 1), []repository.MemberVoteRow{
		{MemberID: testdb.FixtureHouseDem, Vote: "Yea"},
		{MemberID: testdb.FixtureSenatorRep, Vote: "Nay"},
	})
	if err != nil {
		t.Fatal(err)
	}
	// Storing it again overwrites positions and adds none twice.
	err = store.StoreRollCall(ctx, houseRollCall(id, 900, 1, 1), []repository.MemberVoteRow{
		{MemberID: testdb.FixtureHouseDem, Vote: "Nay"},
		{MemberID: testdb.FixtureSenatorRep, Vote: "Nay"},
	})
	if err != nil {
		t.Fatal(err)
	}

	got := queryStrings(t, client,
		"SELECT member_id, vote FROM member_votes WHERE vote_id = @id ORDER BY member_id",
		map[string]any{"id": id})
	want := []string{testdb.FixtureHouseDem + "|Nay", testdb.FixtureSenatorRep + "|Nay"}
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("member votes = %v, want %v", got, want)
	}
	ids, err := store.IncompleteVoteIDs(ctx, testdb.FixtureCongress, "House")
	if err != nil || len(ids) != 0 {
		t.Errorf("incomplete House votes = %v, %v; want none", ids, err)
	}
}

// One bad member row fails the whole write: no vote row is left behind with some of its members,
// which the next sync would skip for good (#455).
func TestStoreRollCall_AllOrNothing(t *testing.T) {
	store, client := newLinkStore(t)
	const id = "house-119-s1-roll901"

	err := store.StoreRollCall(t.Context(), houseRollCall(id, 901, 2, 0), []repository.MemberVoteRow{
		{MemberID: testdb.FixtureHouseDem, Vote: "Yea"},
		{MemberID: strings.Repeat("X", 37), Vote: "Yea"}, // over member_id's STRING(36)
	})
	if err == nil {
		t.Fatal("StoreRollCall with an oversized member ID succeeded, want an error")
	}

	for _, table := range []string{"congressional_votes", "member_votes"} {
		if got := queryStrings(t, client, "SELECT vote_id FROM "+table+" WHERE vote_id = @id",
			map[string]any{"id": id}); len(got) != 0 {
			t.Errorf("%s has %v after a failed write, want nothing", table, got)
		}
	}
}

// A House roll call stored before #455 with fewer member rows than its totals is incomplete.
func TestIncompleteVoteIDs_House(t *testing.T) {
	store, _ := newLinkStore(t)
	ctx := t.Context()

	if err := store.StoreRollCall(ctx, houseRollCall("house-119-s1-roll902", 902, 2, 1),
		[]repository.MemberVoteRow{{MemberID: testdb.FixtureHouseDem, Vote: "Yea"}}); err != nil {
		t.Fatal(err)
	}
	got, err := store.IncompleteVoteIDs(ctx, testdb.FixtureCongress, "House")
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"house-119-s1-roll902"}; !slices.Equal(got, want) {
		t.Errorf("incomplete House votes = %v, want %v", got, want)
	}
}
