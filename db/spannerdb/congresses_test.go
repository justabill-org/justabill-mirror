package spannerdb_test

import (
	"maps"
	"strconv"
	"testing"
	"time"

	"cloud.google.com/go/civil"
	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

const nextCongress = testdb.FixtureCongress + 1

type storedCongress struct {
	Number    int64            `spanner:"number"`
	StartDate civil.Date       `spanner:"start_date"`
	EndDate   spanner.NullDate `spanner:"end_date"`
	IsCurrent bool             `spanner:"is_current"`
}

// readCongresses returns every congresses row by number.
func readCongresses(t *testing.T, client *spanner.Client) map[int64]storedCongress {
	t.Helper()
	iter := client.Single().Query(t.Context(), spanner.NewStatement(
		"SELECT number, start_date, end_date, is_current FROM congresses"))
	out := map[int64]storedCongress{}
	if err := iter.Do(func(row *spanner.Row) error {
		var c storedCongress
		if err := row.ToStruct(&c); err != nil {
			return err
		}
		out[c.Number] = c
		return nil
	}); err != nil {
		t.Fatalf("read congresses: %v", err)
	}
	return out
}

func jan3(year int) time.Time {
	return time.Date(year, time.January, 3, 0, 0, 0, 0, time.UTC)
}

// A new congress row isn't current; rewriting the current congress's dates keeps it current.
func TestUpsertCongress(t *testing.T) {
	store, client := newLinkStore(t)
	ctx := t.Context()

	if err := store.UpsertCongress(ctx, repository.CongressRow{
		Number: nextCongress, StartDate: jan3(2027), EndDate: jan3(2029),
	}); err != nil {
		t.Fatalf("UpsertCongress(%d): %v", nextCongress, err)
	}
	if err := store.UpsertCongress(ctx, repository.CongressRow{
		Number: testdb.FixtureCongress, StartDate: jan3(2025), EndDate: jan3(2027),
	}); err != nil {
		t.Fatalf("UpsertCongress(%d): %v", testdb.FixtureCongress, err)
	}

	rows := readCongresses(t, client)
	next := rows[nextCongress]
	if next.IsCurrent {
		t.Errorf("new congress %d is current", nextCongress)
	}
	if next.StartDate != civil.DateOf(jan3(2027)) || next.EndDate != date(2029, time.January, 3) {
		t.Errorf("congress %d dates = %v to %v, want 2027-01-03 to 2029-01-03",
			nextCongress, next.StartDate, next.EndDate)
	}
	if !rows[testdb.FixtureCongress].IsCurrent {
		t.Errorf("congress %d lost is_current", testdb.FixtureCongress)
	}
}

// Moving is_current leaves exactly one current row, and a second move to the same congress
// changes nothing.
func TestSetCurrentCongress(t *testing.T) {
	store, client := newLinkStore(t)
	ctx := t.Context()
	if err := store.UpsertCongress(ctx, repository.CongressRow{
		Number: nextCongress, StartDate: jan3(2027), EndDate: jan3(2029),
	}); err != nil {
		t.Fatalf("UpsertCongress: %v", err)
	}

	for i, wantChanged := range []bool{true, false} {
		changed, err := store.SetCurrentCongress(ctx, nextCongress)
		if err != nil {
			t.Fatalf("SetCurrentCongress #%d: %v", i+1, err)
		}
		if changed != wantChanged {
			t.Errorf("SetCurrentCongress #%d changed = %t, want %t", i+1, changed, wantChanged)
		}
	}
	for number, c := range readCongresses(t, client) {
		if want := number == nextCongress; c.IsCurrent != want {
			t.Errorf("congress %d is_current = %t, want %t", number, c.IsCurrent, want)
		}
	}
}

// A congress with no row can't become current, and the current one stays current.
func TestSetCurrentCongressMissingRow(t *testing.T) {
	store, client := newLinkStore(t)

	if _, err := store.SetCurrentCongress(t.Context(), nextCongress); err == nil {
		t.Fatal("SetCurrentCongress on a congress with no row: no error")
	}
	if !readCongresses(t, client)[testdb.FixtureCongress].IsCurrent {
		t.Errorf("congress %d lost is_current", testdb.FixtureCongress)
	}
}

func TestCountMemberTerms(t *testing.T) {
	store, client := newLinkStore(t)
	ctx := t.Context()

	want := queryStrings(t, client,
		"SELECT CAST(COUNT(*) AS STRING) FROM member_terms WHERE congress = @congress",
		map[string]any{"congress": int64(testdb.FixtureCongress)})
	got, err := store.CountMemberTerms(ctx, testdb.FixtureCongress)
	if err != nil {
		t.Fatalf("CountMemberTerms(%d): %v", testdb.FixtureCongress, err)
	}
	if got == 0 || len(want) != 1 || strconv.Itoa(got) != want[0] {
		t.Errorf("CountMemberTerms(%d) = %d, want %v (and not 0)", testdb.FixtureCongress, got, want)
	}

	if got, err = store.CountMemberTerms(ctx, nextCongress); err != nil || got != 0 {
		t.Errorf("CountMemberTerms(%d) = %d, %v; want 0, nil", nextCongress, got, err)
	}
}

// List marks a congress as having votes only once one of its roll calls is loaded: the fixture
// has roll calls in the current congress and none in the previous one, and a voice vote (no roll
// number) doesn't count.
func TestCongressListHasVotes(t *testing.T) {
	_, client := newLinkStore(t)
	ctx := t.Context()
	repo := spannerdb.NewCongressRepo(&spannerdb.Client{Spanner: client})

	hasVotes := func() map[int]bool {
		t.Helper()
		congresses, err := repo.List(ctx)
		if err != nil {
			t.Fatalf("List: %v", err)
		}
		out := map[int]bool{}
		for _, c := range congresses {
			out[c.Number] = c.HasVotes
		}
		return out
	}
	want := map[int]bool{testdb.FixtureCongress: true, testdb.FixturePrevCongress: false}
	if got := hasVotes(); !maps.Equal(got, want) {
		t.Fatalf("has votes = %v, want %v", got, want)
	}

	voted := time.Date(2024, time.March, 1, 0, 0, 0, 0, time.UTC)
	testdb.SeedCongressionalVote(ctx, t, client, "h-voice-118", nil, testdb.FixturePrevCongress, "House", voted)
	if got := hasVotes(); got[testdb.FixturePrevCongress] {
		t.Errorf("a voice vote marked congress %d as having votes", testdb.FixturePrevCongress)
	}

	if _, err := client.Apply(ctx, []*spanner.Mutation{spanner.Insert("congressional_votes",
		[]string{"vote_id", "congress", "chamber", "vote_date", "roll_number"},
		[]any{"h-118-2-7", int64(testdb.FixturePrevCongress), "House", voted, int64(7)})}); err != nil {
		t.Fatalf("insert roll call: %v", err)
	}
	want[testdb.FixturePrevCongress] = true
	if got := hasVotes(); !maps.Equal(got, want) {
		t.Errorf("after a roll call: has votes = %v, want %v", got, want)
	}
}
