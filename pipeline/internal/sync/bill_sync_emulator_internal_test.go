package sync

import (
	"maps"
	"testing"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

// billSyncTables are the tables a bill sync writes rows to for each bill.
func billSyncTables() []string {
	return []string{
		"bill_actions", "bill_text_versions", "bill_sponsorships", "bill_committees", "bill_subjects",
		"bill_relations", "bill_status_history", "amendments",
	}
}

// emulatorInt runs a query that returns one INT64, with an optional @bill parameter.
func emulatorInt(t *testing.T, client *spanner.Client, sql, bill string) int64 {
	t.Helper()
	var n int64
	stmt := spanner.Statement{SQL: sql, Params: map[string]any{"bill": bill}}
	err := client.Single().Query(t.Context(), stmt).Do(func(row *spanner.Row) error {
		return row.Columns(&n)
	})
	if err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return n
}

// billRowCounts counts bill's rows in each of billSyncTables.
func billRowCounts(t *testing.T, client *spanner.Client, bill string) map[string]int64 {
	t.Helper()
	counts := map[string]int64{}
	for _, table := range billSyncTables() {
		counts[table] = emulatorInt(t, client, "SELECT COUNT(*) FROM "+table+" WHERE bill_id = @bill", bill)
	}
	return counts
}

// A full bill sync, from the Congress.gov list to every table, on the Spanner store: the bill rows,
// their links and sub-resources, the watermark, and a failed bill in sync_retry. Run again with
// the failure fixed, it stores the failed bill, clears its retry and writes no second copy of
// anything (#226).
func TestEmulator_SyncBillsWritesEveryTable(t *testing.T) {
	client := testdb.New(t)
	ctx := t.Context()
	testdb.SeedCongress(ctx, t, client, 119)
	api := &billAPI{n: 2, failPaths: map[string]bool{"/bill/119/hr/2/subjects": true}}
	s := newBillSyncService(t, spannerdb.NewPipelineStore(&spannerdb.Client{Spanner: client}), api)

	if err := s.SyncBills(ctx, 119, 0); err != nil {
		t.Fatalf("first SyncBills: %v", err)
	}

	first := billRowCounts(t, client, "hr-119-1")
	for _, table := range billSyncTables() {
		if first[table] == 0 {
			t.Errorf("%s has no rows for hr-119-1 after the sync", table)
		}
	}
	row, err := client.Single().ReadRow(ctx, "bills", spanner.Key{"hr-119-1"},
		[]string{"title", "current_status", "introduced_date"})
	if err != nil {
		t.Fatalf("read hr-119-1: %v", err)
	}
	var title, status spanner.NullString
	var introduced spanner.NullDate
	if err = row.Columns(&title, &status, &introduced); err != nil {
		t.Fatal(err)
	}
	if title.StringVal != "Bill" || status.StringVal != "in_committee" || introduced.String() != "2025-01-03" {
		t.Errorf("hr-119-1 = title %v, status %v, introduced %v; want Bill, in_committee, 2025-01-03",
			title, status, introduced)
	}
	if n := emulatorInt(t, client, "SELECT COUNT(*) FROM bills WHERE bill_id = @bill", "hr-119-2"); n != 1 {
		t.Errorf("hr-119-2 rows = %d, want 1: the parts that worked are stored", n)
	}
	const retrySQL = "SELECT COUNT(*) FROM sync_retry WHERE step = 'bills' AND item_id = @bill"
	if n := emulatorInt(t, client, retrySQL, "hr-119-2"); n != 1 {
		t.Errorf("sync_retry rows for hr-119-2 = %d, want 1", n)
	}
	const stateSQL = "SELECT items_synced FROM sync_state WHERE step = 'bills' AND congress = 119 " +
		"AND last_synced_at IS NOT NULL"
	if n := emulatorInt(t, client, stateSQL, ""); n != 1 {
		t.Errorf("bills watermark items_synced = %d, want 1 (hr-119-2 failed)", n)
	}

	api.failPaths = nil
	if err = s.SyncBills(ctx, 119, 0); err != nil {
		t.Fatalf("second SyncBills: %v", err)
	}

	if n := emulatorInt(t, client, retrySQL, "hr-119-2"); n != 0 {
		t.Errorf("sync_retry rows for hr-119-2 = %d after it synced, want 0", n)
	}
	if n := emulatorInt(t, client, stateSQL, ""); n != 2 {
		t.Errorf("bills watermark items_synced = %d, want 2", n)
	}
	for _, bill := range []string{"hr-119-1", "hr-119-2"} {
		if got := billRowCounts(t, client, bill); !maps.Equal(got, first) {
			t.Errorf("%s rows after the second run = %v, want %v (one copy of each)", bill, got, first)
		}
	}
}
