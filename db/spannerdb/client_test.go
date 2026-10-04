package spannerdb_test

import (
	"strings"
	"testing"
	"time"

	"cloud.google.com/go/spanner/apiv1/spannerpb"

	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

// NewClient connects by project, instance and database, as the API and the
// pipeline do, and Ping reaches the database through it.
func TestNewClient_Ping(t *testing.T) {
	_, db := testdb.NewEmpty(t)

	c, err := spannerdb.NewClient(t.Context(), db.Project, db.Instance, db.ID)
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer c.Close()

	if err = c.Ping(t.Context()); err != nil {
		t.Errorf("Ping: %v", err)
	}
}

// WithDatabaseRole sets the client's database role and WithBatchPriority its
// query priority; without options the client assumes no role and leaves every
// priority to Spanner's default (high).
func TestClientConfig(t *testing.T) {
	low := spannerpb.RequestOptions_PRIORITY_LOW
	tests := []struct {
		name          string
		opts          []spannerdb.Option
		wantRole      string
		wantQueryPrio spannerpb.RequestOptions_Priority
	}{
		{name: "no options"},
		{name: "a role", opts: []spannerdb.Option{spannerdb.WithDatabaseRole("api")}, wantRole: "api"},
		{name: "an empty role", opts: []spannerdb.Option{spannerdb.WithDatabaseRole("")}},
		{name: "batch priority", opts: []spannerdb.Option{spannerdb.WithBatchPriority()}, wantQueryPrio: low},
		{
			name:     "a role and batch priority",
			opts:     []spannerdb.Option{spannerdb.WithDatabaseRole("api"), spannerdb.WithBatchPriority()},
			wantRole: "api", wantQueryPrio: low,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := spannerdb.ClientConfig(tt.opts...)
			if got.DatabaseRole != tt.wantRole {
				t.Errorf("DatabaseRole = %q, want %q", got.DatabaseRole, tt.wantRole)
			}
			if got.QueryOptions.Priority != tt.wantQueryPrio {
				t.Errorf("QueryOptions.Priority = %v, want %v", got.QueryOptions.Priority, tt.wantQueryPrio)
			}
			// Point reads (the lease) and commits keep the default priority.
			if got.ReadOptions.Priority != spannerpb.RequestOptions_PRIORITY_UNSPECIFIED {
				t.Errorf("ReadOptions.Priority = %v, want unspecified", got.ReadOptions.Priority)
			}
			if commit := got.TransactionOptions.CommitPriority; commit != spannerpb.RequestOptions_PRIORITY_UNSPECIFIED {
				t.Errorf("TransactionOptions.CommitPriority = %v, want unspecified", commit)
			}
		})
	}
}

// A client at batch priority runs the pipeline's queries, DML and lease as a
// default client does. The emulator accepts the priority but doesn't schedule
// by it, so this checks only that Spanner takes the requests; production's
// effect shows in instance/cpu/utilization_by_priority (#867).
func TestNewClient_WithBatchPriority(t *testing.T) {
	ctx := t.Context()
	store, admin := newSummaryStore(t)
	seedSummaryBill(t, store, admin, testdb.FixtureCongress, 1, 100, []string{"rh", "ih"}, "rh", "ih")

	parts := strings.Split(admin.DatabaseName(), "/")
	c, err := spannerdb.NewClient(ctx, parts[1], parts[3], parts[5], spannerdb.WithBatchPriority())
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer c.Close()
	batch := spannerdb.NewPipelineStore(c)

	// A query: the summary queue, as the default client sees it.
	assertRows(t, queue(t, batch, nil), queue(t, store, nil))
	assertRows(t, queue(t, batch, nil), []string{billID(1) + "|rh|hash-rh|2"})
	// The diff sweep's read, then its DML in a read-write transaction (BatchUpdate).
	pairs, err := batch.QueryMissingDiffPairs(ctx, 10)
	if err != nil || len(pairs) != 1 {
		t.Errorf("QueryMissingDiffPairs at batch priority = %+v, %v; want the ih→rh pair", pairs, err)
	}
	if _, err = batch.DeleteNonConsecutiveDiffs(ctx); err != nil {
		t.Errorf("DeleteNonConsecutiveDiffs at batch priority: %v", err)
	}
	// The lease's statements ask for high priority on the same client.
	lease := spannerdb.NewLeaseStore(c)
	_, ok, err := lease.AcquireLease(ctx, "batch-priority-test", "holder-1", time.Minute)
	if err != nil || !ok {
		t.Errorf("AcquireLease at batch priority = %v, %v; want true, nil", ok, err)
	}
	if err = lease.ReleaseLease(ctx, "batch-priority-test", "holder-1"); err != nil {
		t.Errorf("ReleaseLease at batch priority: %v", err)
	}
}

// A client with the api database role connects and reads. The emulator
// doesn't enforce roles, so this checks only the wiring; production's
// enforcement is checked with docs/runbooks/api-database-role.md.
func TestNewClient_WithDatabaseRole(t *testing.T) {
	ctx := t.Context()
	admin := testdb.New(t)
	testdb.SeedCongress(ctx, t, admin, 119)
	testdb.SeedBill(ctx, t, admin, "hr-119-1", 119, "hr", 1, "A bill")

	// projects/<project>/instances/<instance>/databases/<id>
	parts := strings.Split(admin.DatabaseName(), "/")
	c, err := spannerdb.NewClient(ctx, parts[1], parts[3], parts[5], spannerdb.WithDatabaseRole("api"))
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	defer c.Close()

	bill, err := spannerdb.NewBillRepo(c).GetByID(ctx, "hr-119-1")
	if err != nil {
		t.Fatalf("GetByID with the api role: %v", err)
	}
	if bill.Title != "A bill" {
		t.Errorf("Title = %q, want %q", bill.Title, "A bill")
	}
}
