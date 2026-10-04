package spannerdb_test

import (
	"errors"
	"slices"
	"testing"
	"time"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/testdb"
)

// lisIDs returns every stored member's LIS ID, "" for none.
func lisIDs(t *testing.T, client *spanner.Client) map[string]string {
	t.Helper()
	iter := client.Single().Read(t.Context(), "members", spanner.AllKeys(), []string{"bioguide_id", "lis_id"})
	ids := map[string]string{}
	if err := iter.Do(func(row *spanner.Row) error {
		var id string
		var lis spanner.NullString
		if err := row.Columns(&id, &lis); err != nil {
			return err
		}
		ids[id] = lis.StringVal
		return nil
	}); err != nil {
		t.Fatalf("read lis ids: %v", err)
	}
	return ids
}

// An LIS ID the old name fallback wrote to the wrong member moves to its owner in one
// transaction, which the unique index on lis_id requires.
func TestSetMemberLisID_MovesAnIDHeldByAnotherMember(t *testing.T) {
	store, client := newLinkStore(t)
	ctx := t.Context()
	testdb.SeedMember(ctx, t, client, "L000570", "Ben Ray", "Luján")

	change, err := store.SetMemberLisID(ctx, "L000570", testdb.FixtureSenatorLIS)
	if err != nil {
		t.Fatal(err)
	}
	if change != (repository.LisIDChange{TakenFrom: testdb.FixtureSenatorRep}) {
		t.Errorf("change = %+v, want taken from %s with no previous ID", change, testdb.FixtureSenatorRep)
	}
	ids := lisIDs(t, client)
	if ids["L000570"] != testdb.FixtureSenatorLIS || ids[testdb.FixtureSenatorRep] != "" {
		t.Errorf("lis ids = %v, want %s on L000570 only", ids, testdb.FixtureSenatorLIS)
	}

	// A member's own ID again is a no-op; a new one replaces it.
	if change, err = store.SetMemberLisID(ctx, "L000570", testdb.FixtureSenatorLIS); err != nil ||
		change != (repository.LisIDChange{Previous: testdb.FixtureSenatorLIS}) {
		t.Errorf("same ID again = %+v, %v; want previous %s and nothing taken", change, err, testdb.FixtureSenatorLIS)
	}
	if change, err = store.SetMemberLisID(ctx, "L000570", "S409"); err != nil ||
		change != (repository.LisIDChange{Previous: testdb.FixtureSenatorLIS}) {
		t.Errorf("new ID = %+v, %v; want previous %s", change, err, testdb.FixtureSenatorLIS)
	}
	if got := lisIDs(t, client)["L000570"]; got != "S409" {
		t.Errorf("lis_id = %q, want S409", got)
	}
}

func TestSetMemberLisID_UnknownMemberChangesNothing(t *testing.T) {
	store, client := newLinkStore(t)

	_, err := store.SetMemberLisID(t.Context(), "Z999999", testdb.FixtureSenatorLIS)
	if !errors.Is(err, repository.ErrMemberNotFound) {
		t.Fatalf("err = %v, want ErrMemberNotFound", err)
	}
	if got := lisIDs(t, client)[testdb.FixtureSenatorRep]; got != testdb.FixtureSenatorLIS {
		t.Errorf("%s lis_id = %q, want it kept", testdb.FixtureSenatorRep, got)
	}
}

func TestListSenators_FiltersByCongress(t *testing.T) {
	store, client := newLinkStore(t)
	ctx := t.Context()
	// A 118th senator and a 119th representative are never returned for the 119th.
	testdb.SeedMember(ctx, t, client, "P000001", "Pat", "Past")
	testdb.SeedMemberTerm(ctx, t, client, "P000001", testdb.FixturePrevCongress, "Senate", "OH", nil, "D")

	got, err := store.ListSenators(ctx, testdb.FixtureCongress)
	if err != nil {
		t.Fatal(err)
	}
	want := []repository.SenatorName{
		{BioguideID: testdb.FixtureSenatorRep, FirstName: "Cora", LastName: "Chen", State: "OH"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("119th senators = %+v, want %+v", got, want)
	}

	got, err = store.ListSenators(ctx, testdb.FixturePrevCongress)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0].BioguideID != "P000001" {
		t.Errorf("118th senators = %+v, want P000001 only", got)
	}
}

func TestIncompleteVoteIDs(t *testing.T) {
	store, client := newLinkStore(t)
	ctx := t.Context()
	date := time.Date(2025, time.June, 1, 17, 0, 0, 0, time.UTC)
	vote := func(id string, roll, yeas, nays, present, notVoting int) {
		t.Helper()
		if err := store.UpsertCongressionalVote(ctx, repository.CongressionalVoteRow{
			ID: id, Congress: testdb.FixtureCongress, Chamber: "Senate", Session: new(1), RollNumber: &roll,
			VoteDate: date, Yeas: &yeas, Nays: &nays, Present: &present, NotVoting: &notVoting,
		}); err != nil {
			t.Fatal(err)
		}
	}
	// Two senators on the roll, one member row: incomplete.
	vote("senate-119-s1-vote00010", 10, 1, 0, 0, 1)
	testdb.SeedMemberVote(ctx, t, client, "senate-119-s1-vote00010", testdb.FixtureSenatorRep, "Yea")
	// Totals but no member rows at all: incomplete.
	vote("senate-119-s1-vote00011", 11, 1, 0, 0, 0)
	// A tie the Vice President broke: 1 + 1 in the count, two member rows, the VP not among them.
	vote("senate-119-s1-vote00012", 12, 1, 1, 0, 0)
	testdb.SeedMemberVote(ctx, t, client, "senate-119-s1-vote00012", testdb.FixtureSenatorRep, "Yea")
	testdb.SeedMemberVote(ctx, t, client, "senate-119-s1-vote00012", testdb.FixtureHouseDem, "Nay")
	// A unanimous-consent row: no roll number, no totals, no member rows.
	if err := store.UpsertCongressionalVote(ctx, repository.CongressionalVoteRow{
		ID: "senate-119-uc-s-119-1-20250601", Congress: testdb.FixtureCongress, Chamber: "Senate", VoteDate: date,
	}); err != nil {
		t.Fatal(err)
	}

	got, err := store.IncompleteVoteIDs(ctx, testdb.FixtureCongress, "Senate")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"senate-119-s1-vote00010", "senate-119-s1-vote00011"}
	if !slices.Equal(got, want) {
		t.Errorf("incomplete Senate votes = %v, want %v", got, want)
	}

	if got, err = store.IncompleteVoteIDs(ctx, testdb.FixtureCongress, "House"); err != nil || len(got) != 0 {
		t.Errorf("incomplete House votes = %v, %v; want none (the fixture's vote has no totals)", got, err)
	}
}
