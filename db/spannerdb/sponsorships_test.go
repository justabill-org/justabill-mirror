package spannerdb_test

import (
	"reflect"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

func TestBillGetSponsorships(t *testing.T) {
	store, client := newLinkStore(t)
	ctx := t.Context()
	repo := spannerdb.NewBillRepo(&spannerdb.Client{Spanner: client})
	introduced := time.Date(2025, time.January, 3, 0, 0, 0, 0, time.UTC)
	later := time.Date(2025, time.February, 10, 0, 0, 0, 0, time.UTC)

	seed := map[string][]repository.BillSponsorshipRow{
		repository.SponsorRoleSponsor: {{MemberID: testdb.FixtureHouseDem, SponsoredDate: &introduced}},
		repository.SponsorRoleCosponsor: {
			{MemberID: unsyncedMember},
			{MemberID: testdb.FixtureSenatorRep, SponsoredDate: &later},
			{MemberID: testdb.FixtureHouseRep, SponsoredDate: &introduced, IsOriginal: true},
		},
	}
	for role, rows := range seed {
		if err := store.ReplaceBillSponsorships(ctx, testdb.FixtureHouseBill, role, rows); err != nil {
			t.Fatalf("seed %s: %v", role, err)
		}
	}
	if err := store.ReplaceBillSponsorships(ctx, testdb.FixturePrevHouseBill, repository.SponsorRoleSponsor,
		[]repository.BillSponsorshipRow{{MemberID: testdb.FixtureHouseRep}}); err != nil {
		t.Fatalf("seed 118th sponsor: %v", err)
	}

	got, err := repo.GetSponsorships(ctx, testdb.FixtureHouseBill)
	if err != nil {
		t.Fatalf("sponsorships: %v", err)
	}
	// Sponsor first, then cosponsors by date; the unsynced member has no name or term and
	// no date, so it comes last. The senator has no district.
	want := []model.BillSponsorship{
		{
			BioguideID: testdb.FixtureHouseDem, FirstName: "Ada", LastName: "Alvarez",
			Role: repository.SponsorRoleSponsor, Party: new("D"), State: new("CA"),
			District: new(12), SponsoredDate: &introduced,
		},
		{
			BioguideID: testdb.FixtureHouseRep, FirstName: "Ben", LastName: "Brooks",
			Role: repository.SponsorRoleCosponsor, Party: new("R"), State: new("TX"),
			District: new(7), SponsoredDate: &introduced, IsOriginal: true,
		},
		{
			BioguideID: testdb.FixtureSenatorRep, FirstName: "Cora", LastName: "Chen",
			Role: repository.SponsorRoleCosponsor, Party: new("R"), State: new("OH"), SponsoredDate: &later,
		},
		{BioguideID: unsyncedMember, Role: repository.SponsorRoleCosponsor},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("sponsorships = %+v, want %+v", got, want)
	}

	// The fixture has no 118th terms, so party and state are unknown.
	got, err = repo.GetSponsorships(ctx, testdb.FixturePrevHouseBill)
	if err != nil {
		t.Fatalf("118th sponsorships: %v", err)
	}
	want = []model.BillSponsorship{{
		BioguideID: testdb.FixtureHouseRep, FirstName: "Ben", LastName: "Brooks",
		Role: repository.SponsorRoleSponsor,
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("118th sponsorships = %+v, want %+v", got, want)
	}

	// No link rows yet: an empty, non-nil slice, so the API encodes [].
	got, err = repo.GetSponsorships(ctx, testdb.FixtureSenateBill)
	if err != nil {
		t.Fatalf("sponsorships (no rows): %v", err)
	}
	if got == nil || len(got) != 0 {
		t.Errorf("sponsorships without link rows = %#v, want empty slice", got)
	}
}
