package spannerdb_test

import (
	"fmt"
	"slices"
	"testing"
	"time"

	"cloud.google.com/go/civil"
	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/testdb"
)

func billID119(n int) string { return fmt.Sprintf("hr-%d-%d", testdb.FixtureCongress, n) }

func TestListBillsForGAOCheck(t *testing.T) {
	store, client := newLinkStore(t)
	ctx := t.Context()
	now := time.Now()
	day := 24 * time.Hour
	set := func(billID string, checked spanner.NullTime, status spanner.NullDate) {
		t.Helper()
		if _, err := client.Apply(ctx, []*spanner.Mutation{
			spanner.Update("bills", []string{"bill_id", "gao_checked_at", "status_date"},
				[]any{billID, checked, status}),
		}); err != nil {
			t.Fatal(err)
		}
	}
	checked := func(ago time.Duration) spanner.NullTime { return spanner.NullTime{Time: now.Add(-ago), Valid: true} }
	status := func(daysAgo int) spanner.NullDate {
		return spanner.NullDate{Date: civil.DateOf(now.AddDate(0, 0, -daysAgo)), Valid: true}
	}
	for n := 2; n <= 6; n++ {
		testdb.SeedBill(ctx, t, client, billID119(n), testdb.FixtureCongress, "hr", n, "Bill")
	}
	// Never checked: HR 2 (active 3 days ago), S 1 (yesterday), the fixture's HR 808 (law since
	// July 2025), and HR 6 and the fixture's two CRA resolutions (no status date).
	// Due: HR 3 checked 40 days ago (active 10 days ago), HR 4 checked 31 days ago (5 days ago).
	// Fresh: HR 1 checked 5 days ago and HR 5 checked 29 days ago. The 118th's HR 1 is never
	// checked but in another congress.
	set(billID119(2), spanner.NullTime{}, status(3))
	set(testdb.FixtureSenateBill, spanner.NullTime{}, status(1))
	set(billID119(6), spanner.NullTime{}, spanner.NullDate{})
	set(billID119(3), checked(40*day), status(10))
	set(billID119(4), checked(31*day), status(5))
	set(testdb.FixtureHouseBill, checked(5*day), status(0))
	set(billID119(5), checked(29*day), status(0))

	got, err := store.ListBillsForGAOCheck(ctx, testdb.FixtureCongress, 1000)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		testdb.FixtureSenateBill, billID119(2), testdb.FixtureLawBill, testdb.FixtureCRAUnmatched, billID119(6),
		testdb.FixtureCRABill, billID119(4), billID119(3),
	}
	if !slices.Equal(got, want) {
		t.Errorf("ListBillsForGAOCheck = %v, want %v", got, want)
	}

	got, err = store.ListBillsForGAOCheck(ctx, testdb.FixtureCongress, 2)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got, want[:2]) {
		t.Errorf("ListBillsForGAOCheck(limit 2) = %v, want %v", got, want[:2])
	}
}

func TestMarkGAOChecked(t *testing.T) {
	store, client := newLinkStore(t)
	ctx := t.Context()

	before := time.Now()
	if err := store.MarkGAOChecked(ctx, testdb.FixtureHouseBill); err != nil {
		t.Fatalf("mark: %v", err)
	}
	row, err := client.Single().ReadRow(ctx, "bills", spanner.Key{testdb.FixtureHouseBill},
		[]string{"gao_checked_at"})
	if err != nil {
		t.Fatal(err)
	}
	var at spanner.NullTime
	if err = row.Columns(&at); err != nil {
		t.Fatal(err)
	}
	if !at.Valid || at.Time.Before(before.Add(-time.Second)) {
		t.Fatalf("gao_checked_at = %v, want about %v", at, before)
	}

	got, err := store.ListBillsForGAOCheck(ctx, testdb.FixtureCongress, 0)
	if err != nil {
		t.Fatal(err)
	}
	if slices.Contains(got, testdb.FixtureHouseBill) {
		t.Errorf("ListBillsForGAOCheck = %v after MarkGAOChecked(%s), want it left out", got, testdb.FixtureHouseBill)
	}
	if err = store.MarkGAOChecked(ctx, unsyncedBill); err == nil {
		t.Error("MarkGAOChecked of a bill that isn't stored succeeded, want an error")
	}
}
