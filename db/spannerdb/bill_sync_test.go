package spannerdb_test

import (
	"slices"
	"testing"
	"time"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/testdb"
)

func billUpdatedAt(t *testing.T, client *spanner.Client, billID string) spanner.NullTime {
	t.Helper()
	row, err := client.Single().ReadRow(t.Context(), "bills", spanner.Key{billID}, []string{"updated_at"})
	if err != nil {
		t.Fatalf("read %s: %v", billID, err)
	}
	var updated spanner.NullTime
	if err = row.Columns(&updated); err != nil {
		t.Fatal(err)
	}
	return updated
}

func TestUpsertBillLeavesUpdatedAtToMarkBillSynced(t *testing.T) {
	store, client := newLinkStore(t)
	ctx := t.Context()
	row := repository.BillRow{ID: "hr-119-500", Congress: testdb.FixtureCongress, BillType: "hr", Number: 500,
		Title: "A new bill"}

	if err := store.UpsertBill(ctx, row); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	if got := billUpdatedAt(t, client, row.ID); got.Valid {
		t.Fatalf("updated_at = %v after UpsertBill of a new bill, want NULL until it's marked synced", got)
	}

	before := time.Now()
	if err := store.MarkBillSynced(ctx, row.ID); err != nil {
		t.Fatalf("mark: %v", err)
	}
	marked := billUpdatedAt(t, client, row.ID)
	if !marked.Valid || marked.Time.Before(before.Add(-time.Second)) {
		t.Fatalf("updated_at = %v after MarkBillSynced, want about %v", marked, before)
	}

	// A later upsert (a re-sync that then fails partway) keeps the last complete sync's time.
	row.Title = "A retitled bill"
	if err := store.UpsertBill(ctx, row); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	if got := billUpdatedAt(t, client, row.ID); !got.Valid || !got.Time.Equal(marked.Time) {
		t.Errorf("updated_at = %v after a second UpsertBill, want it unchanged at %v", got, marked.Time)
	}
}

func TestMarkBillSyncedUnknownBill(t *testing.T) {
	store, _ := newLinkStore(t)
	if err := store.MarkBillSynced(t.Context(), unsyncedBill); err == nil {
		t.Error("MarkBillSynced of a bill that isn't stored succeeded, want an error")
	}
}

func TestListBillIDsSyncedSince(t *testing.T) {
	store, client := newLinkStore(t)
	ctx := t.Context()
	t0 := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	set := func(billID string, at time.Time) {
		t.Helper()
		if _, err := client.Apply(ctx, []*spanner.Mutation{
			spanner.Update("bills", []string{"bill_id", "updated_at"}, []any{billID, at}),
		}); err != nil {
			t.Fatal(err)
		}
	}
	// The fixture's HR 1 is never marked (NULL). S 1 synced before T0, HR 2 exactly at T0,
	// HR 3 after it, and the 118th's HR 1 after it but in another congress.
	testdb.SeedBill(ctx, t, client, "hr-119-2", testdb.FixtureCongress, "hr", 2, "Two")
	testdb.SeedBill(ctx, t, client, "hr-119-3", testdb.FixtureCongress, "hr", 3, "Three")
	set(testdb.FixtureSenateBill, t0.Add(-time.Minute))
	set("hr-119-2", t0)
	set("hr-119-3", t0.Add(time.Hour))
	set(testdb.FixturePrevHouseBill, t0.Add(time.Hour))

	got, err := store.ListBillIDsSyncedSince(ctx, testdb.FixtureCongress, t0)
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(got)
	if want := []string{"hr-119-2", "hr-119-3"}; !slices.Equal(got, want) {
		t.Errorf("synced since T0 = %v, want %v", got, want)
	}
}

func TestListMissingVotedBillIDs(t *testing.T) {
	store, client := newLinkStore(t)
	ctx := t.Context()
	day := time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC)
	vote := func(id string, billID *string, congress int) {
		t.Helper()
		testdb.SeedCongressionalVote(ctx, t, client, id, billID, congress, "House", day)
	}
	// The fixture's roll calls name HR 1 and S 1 of the 119th, both stored; mark them synced.
	for _, id := range []string{testdb.FixtureHouseBill, testdb.FixtureSenateBill} {
		if err := store.MarkBillSynced(ctx, id); err != nil {
			t.Fatal(err)
		}
	}
	// Add roll calls on
	// a stored bill (present), two on HR 900 and one on S 5 (missing), one with no bill (a
	// nomination), and one on a missing bill of the 118th.
	vote("house-119-s1-roll101", new(testdb.FixtureHouseBill), testdb.FixtureCongress)
	vote("house-119-s1-roll102", new("hr-119-900"), testdb.FixtureCongress)
	vote("house-119-s1-roll103", new("hr-119-900"), testdb.FixtureCongress)
	vote("senate-119-s1-vote00104", new("s-119-5"), testdb.FixtureCongress)
	vote("senate-119-s1-vote00105", nil, testdb.FixtureCongress)
	vote("house-118-s1-roll106", new("hr-118-900"), testdb.FixturePrevCongress)

	got, err := store.ListMissingVotedBillIDs(ctx, testdb.FixtureCongress)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"hr-119-900", "s-119-5"}; !slices.Equal(got, want) {
		t.Errorf("missing voted bills = %v, want %v (missing only, once each, sorted)", got, want)
	}

	// A row whose sync didn't finish (updated_at NULL) is still missing, so a rerun retries it.
	testdb.SeedBill(ctx, t, client, "hr-119-900", testdb.FixtureCongress, "hr", 900, "Nine hundred")
	if got, err = store.ListMissingVotedBillIDs(ctx, testdb.FixtureCongress); err != nil ||
		!slices.Equal(got, []string{"hr-119-900", "s-119-5"}) {
		t.Errorf("after storing HR 900 unsynced: %v, %v; want [hr-119-900 s-119-5]", got, err)
	}

	// Once HR 900 is marked synced, only S 5 is left.
	if err = store.MarkBillSynced(ctx, "hr-119-900"); err != nil {
		t.Fatal(err)
	}
	if got, err = store.ListMissingVotedBillIDs(ctx, testdb.FixtureCongress); err != nil ||
		!slices.Equal(got, []string{"s-119-5"}) {
		t.Errorf("after syncing HR 900: %v, %v; want [s-119-5]", got, err)
	}
}
