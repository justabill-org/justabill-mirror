package sync

import (
	"log/slog"
	"maps"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

// The re-derivation on the Spanner store (#659): a law the old rules left without its House
// passage, and a vetoed bill they called signed, get the stages their stored actions show, and
// a second run changes nothing.
func TestEmulator_RederiveStatusHistory(t *testing.T) {
	client := testdb.New(t)
	ctx := t.Context()
	testdb.SeedCongress(ctx, t, client, 119)
	testdb.SeedBill(ctx, t, client, "s-119-4530", 119, "s", 4530, "A law")
	testdb.SeedBill(ctx, t, client, "hr-119-131", 119, "hr", 131, "A vetoed bill")
	store := spannerdb.NewPipelineStore(&spannerdb.Client{Spanner: client})

	law := []repository.BillActionRow{
		storedAction("2026-05-28", "E40000", "Became Public Law No: 119-90."),
		storedAction("2026-05-19", "8000", "Passed/agreed to in House: On motion to suspend the rules and pass "+
			"the bill Agreed to by voice vote."),
		storedAction("2026-03-10", "17000", "Passed/agreed to in Senate: Passed Senate without amendment by "+
			"Unanimous Consent."),
	}
	floor, president := houseFloor, "President"
	vetoed := []repository.BillActionRow{
		storedAction("2026-01-08", "33000", "Failed of passage in House over veto On passage, the objections "+
			"of the President to the contrary notwithstanding Failed by the Yeas and Nays: (2/3 required)"),
		{ActionText: "Vetoed by President.", ActionCode: new("E30000"), SourceSystem: &floor, ActionType: &president,
			ActionDate: time.Date(2025, time.December, 29, 0, 0, 0, 0, time.UTC)},
		storedAction("2025-12-29", "31000", "Vetoed by President."),
	}
	for id, actions := range map[string][]repository.BillActionRow{"s-119-4530": law, "hr-119-131": vetoed} {
		for i := range actions {
			actions[i].SortOrder = i + 1
		}
		if err := store.ReplaceBillActions(ctx, id, actions); err != nil {
			t.Fatal(err)
		}
	}
	// What the old rules stored: no House passage for the law, and "signed" for the veto.
	day := time.Date(2026, time.May, 28, 0, 0, 0, 0, time.UTC)
	if err := store.ReplaceBillStatus(ctx, "hr-119-131", "signed", &day, []repository.BillStatusRow{
		{BillID: "hr-119-131", Status: "signed", StatusDate: day, StatusRank: rankSigned},
	}); err != nil {
		t.Fatal(err)
	}

	s := &Service{store: store, logger: slog.New(slog.DiscardHandler)}
	bills := spannerdb.NewBillRepo(&spannerdb.Client{Spanner: client})
	want := map[string]map[string]string{
		"s-119-4530": {
			statusPassedSenate: "2026-03-10", statusPassedHouse: "2026-05-19", statusBecameLaw: "2026-05-28",
		},
		"hr-119-131": {statusVetoed: "2025-12-29"},
	}
	for run := 1; run <= 2; run++ {
		if err := s.RederiveStatusHistory(ctx, 119, 1); err != nil {
			t.Fatalf("run %d: %v", run, err)
		}
		for id, stages := range want {
			if got := storedHistory(t, bills, id); !maps.Equal(got, stages) {
				t.Errorf("run %d: %s history = %v, want %v", run, id, got, stages)
			}
		}
	}
	b, err := bills.GetByID(ctx, "hr-119-131")
	if err != nil || b == nil || deref(b.CurrentStatus) != statusVetoed {
		t.Errorf("hr-119-131 = %+v, %v; want current status vetoed", b, err)
	}
}

// storedHistory reads a bill's stored status history as status → date.
func storedHistory(t *testing.T, bills *spannerdb.BillRepository, billID string) map[string]string {
	t.Helper()
	history, err := bills.GetStatusHistory(t.Context(), billID)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, h := range history {
		got[h.Status] = h.StatusDate.Format(time.DateOnly)
	}
	return got
}
