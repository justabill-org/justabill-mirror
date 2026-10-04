package spannerdb_test

import (
	"reflect"
	"slices"
	"testing"
	"time"

	"cloud.google.com/go/civil"
	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

type storedTerm struct {
	State     string            `spanner:"state"`
	District  spanner.NullInt64 `spanner:"district"`
	Party     string            `spanner:"party"`
	StartDate spanner.NullDate  `spanner:"start_date"`
	EndDate   spanner.NullDate  `spanner:"end_date"`
}

// readTerm reads a member's term in one congress and chamber.
func readTerm(t *testing.T, client *spanner.Client, memberID string, congress int, chamber string) storedTerm {
	t.Helper()
	key := spanner.Key{memberID, int64(congress), chamber}
	row, err := client.Single().ReadRow(t.Context(), "member_terms", key,
		[]string{"state", "district", "party", "start_date", "end_date"})
	if err != nil {
		t.Fatalf("read term %v: %v", key, err)
	}
	var term storedTerm
	if decodeErr := row.ToStruct(&term); decodeErr != nil {
		t.Fatalf("decode term: %v", decodeErr)
	}
	return term
}

func date(y int, m time.Month, d int) spanner.NullDate {
	return spanner.NullDate{Date: civil.Date{Year: y, Month: m, Day: d}, Valid: true}
}

// A member who switched chambers mid-congress (Schiff in the 118th: House CA-30 until
// 2024-12-08, Senate from 2024-12-09) has two dated terms in one congress.
func TestUpsertMemberTermDates(t *testing.T) {
	store, client := newLinkStore(t)
	ctx := t.Context()
	member := testdb.FixtureHouseDem
	houseStart := time.Date(2023, time.January, 3, 0, 0, 0, 0, time.UTC)
	houseEnd := time.Date(2024, time.December, 8, 0, 0, 0, 0, time.UTC)
	senateStart := time.Date(2024, time.December, 9, 0, 0, 0, 0, time.UTC)
	senateEnd := time.Date(2025, time.January, 3, 0, 0, 0, 0, time.UTC)

	for _, term := range []repository.MemberTermRow{
		{MemberID: member, Congress: testdb.FixturePrevCongress, Chamber: "House", State: "CA",
			District: new(30), Party: "D", StartDate: &houseStart, EndDate: &houseEnd},
		{MemberID: member, Congress: testdb.FixturePrevCongress, Chamber: "Senate", State: "CA",
			Party: "D", StartDate: &senateStart, EndDate: &senateEnd},
	} {
		if err := store.UpsertMemberTerm(ctx, term); err != nil {
			t.Fatalf("upsert %s term: %v", term.Chamber, err)
		}
	}

	wantHouse := storedTerm{"CA", spanner.NullInt64{Int64: 30, Valid: true}, "D",
		date(2023, time.January, 3), date(2024, time.December, 8)}
	wantSenate := storedTerm{State: "CA", Party: "D",
		StartDate: date(2024, time.December, 9), EndDate: date(2025, time.January, 3)}
	if got := readTerm(t, client, member, testdb.FixturePrevCongress, "House"); !reflect.DeepEqual(got, wantHouse) {
		t.Errorf("House term = %+v, want %+v", got, wantHouse)
	}
	if got := readTerm(t, client, member, testdb.FixturePrevCongress, "Senate"); !reflect.DeepEqual(got, wantSenate) {
		t.Errorf("Senate term = %+v, want %+v", got, wantSenate)
	}

	// A later upsert without dates updates the other columns and keeps the stored start date.
	// Its nil EndDate says the member holds the seat, so the end date is cleared.
	if err := store.UpsertMemberTerm(ctx, repository.MemberTermRow{
		MemberID: member,
		Congress: testdb.FixturePrevCongress,
		Chamber:  "House",
		State:    "CA",
		District: new(30),
		Party:    "I",
	}); err != nil {
		t.Fatalf("upsert without dates: %v", err)
	}
	wantHouse.Party = "I"
	wantHouse.EndDate = spanner.NullDate{}
	if got := readTerm(t, client, member, testdb.FixturePrevCongress, "House"); !reflect.DeepEqual(got, wantHouse) {
		t.Errorf("House term after dateless upsert = %+v, want %+v", got, wantHouse)
	}
}

// Terms without dates stay NULL, as the 119th's list-derived terms are today.
func TestUpsertMemberTermWithoutDates(t *testing.T) {
	store, client := newLinkStore(t)
	if err := store.UpsertMemberTerm(t.Context(), repository.MemberTermRow{
		MemberID: testdb.FixtureHouseRep,
		Congress: testdb.FixturePrevCongress,
		Chamber:  "House",
		State:    "TX",
		District: new(7),
		Party:    "R",
	}); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got := readTerm(t, client, testdb.FixtureHouseRep, testdb.FixturePrevCongress, "House")
	if got.StartDate.Valid || got.EndDate.Valid {
		t.Errorf("term dates = %v, %v; want NULL", got.StartDate, got.EndDate)
	}
}

// UpsertMemberTerm sets end_date when a member leaves and clears it when they return, as the
// member-list sync writes a current congress's terms (#231).
func TestUpsertMemberTermSetsAndClearsEndDate(t *testing.T) {
	store, client := newLinkStore(t)
	ctx := t.Context()
	left := time.Date(2025, time.January, 3, 0, 0, 0, 0, time.UTC)
	term := repository.MemberTermRow{
		MemberID: testdb.FixtureSenatorRep, Congress: testdb.FixtureCongress, Chamber: "Senate",
		State: "OH", Party: "R", EndDate: &left,
	}
	if err := store.UpsertMemberTerm(ctx, term); err != nil {
		t.Fatalf("upsert with end date: %v", err)
	}
	got := readTerm(t, client, term.MemberID, term.Congress, term.Chamber)
	if got.EndDate != date(2025, time.January, 3) {
		t.Errorf("end_date = %v, want 2025-01-03", got.EndDate)
	}

	term.EndDate = nil
	if err := store.UpsertMemberTerm(ctx, term); err != nil {
		t.Fatalf("upsert without end date: %v", err)
	}
	got = readTerm(t, client, term.MemberID, term.Congress, term.Chamber)
	if got.EndDate.Valid {
		t.Errorf("end_date = %v after the member returned, want NULL", got.EndDate)
	}
}

// Find-my-reps leaves out members whose current-congress term has ended: a representative who
// died in office (TX-7 here) and a senator who resigned (a third OH senator).
func TestFindRepsSkipsEndedTerms(t *testing.T) {
	store, client := newLinkStore(t)
	ctx := t.Context()
	left := time.Date(2025, time.January, 3, 0, 0, 0, 0, time.UTC)
	for _, m := range []struct {
		id    string
		term  repository.MemberTermRow
		first string
	}{
		{"D000004", repository.MemberTermRow{Chamber: "House", State: "TX", District: new(7), EndDate: &left}, "Former"},
		{"E000005", repository.MemberTermRow{Chamber: "Senate", State: "OH", EndDate: &left}, "Resigned"},
		{"F000006", repository.MemberTermRow{Chamber: "Senate", State: "OH"}, "Appointed"},
	} {
		if err := store.UpsertMember(ctx, repository.MemberRow{
			BioguideID: m.id, FirstName: m.first, LastName: "Member",
		}); err != nil {
			t.Fatalf("upsert member %s: %v", m.id, err)
		}
		m.term.MemberID, m.term.Congress, m.term.Party = m.id, testdb.FixtureCongress, "R"
		if err := store.UpsertMemberTerm(ctx, m.term); err != nil {
			t.Fatalf("upsert term %s: %v", m.id, err)
		}
	}
	repo := spannerdb.NewMemberRepo(&spannerdb.Client{Spanner: client})

	reps, err := repo.GetByDistrict(ctx, "TX", 7)
	if err != nil {
		t.Fatalf("GetByDistrict: %v", err)
	}
	if got, want := memberIDs(reps), []string{testdb.FixtureHouseRep}; !slices.Equal(got, want) {
		t.Errorf("GetByDistrict(TX, 7) = %v, want %v", got, want)
	}

	senators, err := repo.GetSenators(ctx, "OH")
	if err != nil {
		t.Fatalf("GetSenators: %v", err)
	}
	got := memberIDs(senators)
	slices.Sort(got)
	if want := []string{testdb.FixtureSenatorRep, "F000006"}; !slices.Equal(got, want) {
		t.Errorf("GetSenators(OH) = %v, want %v", got, want)
	}
}

func memberIDs(members []model.Member) []string {
	ids := make([]string, 0, len(members))
	for _, m := range members {
		ids = append(ids, m.BioguideID)
	}
	return ids
}

// ListMemberIDs lists every stored member, with or without terms, which is the roster a
// past-congress term load checks bioguide IDs against.
func TestListMemberIDs(t *testing.T) {
	store, _ := newLinkStore(t)
	ctx := t.Context()
	if err := store.UpsertMember(ctx, repository.MemberRow{
		BioguideID: "D000004", FirstName: "Roster", LastName: "Only",
	}); err != nil {
		t.Fatalf("upsert member: %v", err)
	}

	ids, err := store.ListMemberIDs(ctx)
	if err != nil {
		t.Fatalf("ListMemberIDs: %v", err)
	}
	slices.Sort(ids)
	want := []string{testdb.FixtureHouseDem, testdb.FixtureHouseRep, testdb.FixtureSenatorRep, "D000004"}
	if !slices.Equal(ids, want) {
		t.Errorf("ListMemberIDs = %v, want %v", ids, want)
	}
}
