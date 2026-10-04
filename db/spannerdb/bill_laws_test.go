package spannerdb_test

import (
	"slices"
	"testing"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

// TestBillLaws checks that UpsertBill stores a bill's laws (#709), GetByID and List read them
// back, and an upsert without laws clears them.
func TestBillLaws(t *testing.T) {
	store, client := newLinkStore(t)
	repo := spannerdb.NewBillRepo(&spannerdb.Client{Spanner: client})
	ctx := t.Context()
	laws := []model.BillLaw{{Type: model.BillLawTypePublic, Number: "119-95"}}
	row := repository.BillRow{ID: "s-119-4530", Congress: testdb.FixtureCongress, BillType: "s", Number: 4530,
		Title: "A law", Laws: laws}

	if err := store.UpsertBill(ctx, row); err != nil {
		t.Fatalf("upsert: %v", err)
	}
	got, err := repo.GetByID(ctx, row.ID)
	if err != nil || got == nil {
		t.Fatalf("GetByID = %v, %v", got, err)
	}
	if !slices.Equal(got.Laws, laws) {
		t.Errorf("GetByID laws = %v, want %v", got.Laws, laws)
	}

	billType := "s"
	list, err := repo.List(ctx, model.ListParams{Limit: 20, BillType: &billType})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	for _, b := range list.Items {
		var want []model.BillLaw
		if b.ID == row.ID {
			want = laws
		}
		if !slices.Equal(b.Laws, want) {
			t.Errorf("List: %s laws = %v, want %v", b.ID, b.Laws, want)
		}
	}

	row.Laws = nil
	if err = store.UpsertBill(ctx, row); err != nil {
		t.Fatalf("re-upsert: %v", err)
	}
	if got, err = repo.GetByID(ctx, row.ID); err != nil || got == nil {
		t.Fatalf("GetByID after re-upsert = %v, %v", got, err)
	}
	if got.Laws != nil {
		t.Errorf("laws after an upsert without them = %v, want none", got.Laws)
	}
	if v := billLawsColumn(t, client, row.ID); v.Valid {
		t.Errorf("bills.laws = %v after an upsert without laws, want NULL", v)
	}
}

// TestBillLawsUnreadable checks that a laws value that isn't a list of laws reads as none.
func TestBillLawsUnreadable(t *testing.T) {
	_, client := newLinkStore(t)
	repo := spannerdb.NewBillRepo(&spannerdb.Client{Spanner: client})
	for _, value := range []any{map[string]any{"type": "Public Law"}, []any{}} {
		if _, err := client.Apply(t.Context(), []*spanner.Mutation{spanner.Update("bills",
			[]string{"bill_id", "laws"}, []any{testdb.FixtureHouseBill, spanner.NullJSON{Value: value, Valid: true}}),
		}); err != nil {
			t.Fatal(err)
		}
		got, err := repo.GetByID(t.Context(), testdb.FixtureHouseBill)
		if err != nil || got == nil {
			t.Fatalf("GetByID = %v, %v", got, err)
		}
		if got.Laws != nil {
			t.Errorf("laws = %v for stored %v, want none", got.Laws, value)
		}
	}
}

func billLawsColumn(t *testing.T, client *spanner.Client, billID string) spanner.NullJSON {
	t.Helper()
	row, err := client.Single().ReadRow(t.Context(), "bills", spanner.Key{billID}, []string{"laws"})
	if err != nil {
		t.Fatalf("read %s: %v", billID, err)
	}
	var laws spanner.NullJSON
	if err = row.Columns(&laws); err != nil {
		t.Fatal(err)
	}
	return laws
}
