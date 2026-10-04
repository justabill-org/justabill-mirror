package spannerdb

import (
	"context"
	"fmt"
	"time"

	"cloud.google.com/go/spanner"
	"cloud.google.com/go/spanner/apiv1/spannerpb"

	"github.com/justabill-org/justabill/db/repository"
)

// --- Pipeline lease (docs/design/80-pipeline-operability.md, "Lease semantics") ---

// LeaseRepository implements repository.LeaseStore on the pipeline_leases table.
type LeaseRepository struct {
	client *spanner.Client
}

// NewLeaseStore creates a LeaseStore backed by Spanner.
func NewLeaseStore(c *Client) *LeaseRepository { return &LeaseRepository{client: c.Spanner} }

const (
	paramLeaseName   = "name"
	paramLeaseHolder = "holder"
)

// leaseRecord is a pipeline_leases row as Spanner stores it.
type leaseRecord struct {
	Name       string    `spanner:"name"`
	Holder     string    `spanner:"holder"`
	AcquiredAt time.Time `spanner:"acquired_at"`
	ExpiresAt  time.Time `spanner:"expires_at"`
}

// renewLeaseSQL takes the lease when it has lapsed and extends it when holder has it.
// acquired_at moves only when the lease changes hands or holder takes it back after it lapsed.
// Every SET expression reads the row as it was before the update.
const renewLeaseSQL = `UPDATE pipeline_leases
	SET holder = @holder,
		acquired_at = IF(holder = @holder AND expires_at > CURRENT_TIMESTAMP(), acquired_at, CURRENT_TIMESTAMP()),
		expires_at = TIMESTAMP_ADD(CURRENT_TIMESTAMP(), INTERVAL @ttl_ms MILLISECOND)
	WHERE name = @name AND (holder = @holder OR expires_at <= CURRENT_TIMESTAMP())`

// insertLeaseSQL creates the lease row the first time anyone takes the lease.
const insertLeaseSQL = `INSERT OR IGNORE INTO pipeline_leases (name, holder, acquired_at, expires_at)
	VALUES (@name, @holder, CURRENT_TIMESTAMP(), TIMESTAMP_ADD(CURRENT_TIMESTAMP(), INTERVAL @ttl_ms MILLISECOND))`

// AcquireLease runs the design's acquire-or-renew UPDATE, then INSERT OR IGNORE when no row
// matched, and reads the row back, all in one read-write transaction.
func (r *LeaseRepository) AcquireLease(
	ctx context.Context, name, holder string, ttl time.Duration,
) (repository.Lease, bool, error) {
	params := map[string]any{paramLeaseName: name, paramLeaseHolder: holder, "ttl_ms": ttl.Milliseconds()}
	var rec leaseRecord
	var acquired bool
	_, err := r.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		n, err := leaseUpdate(ctx, txn, spanner.Statement{SQL: renewLeaseSQL, Params: params})
		if err != nil {
			return fmt.Errorf("renewing lease: %w", err)
		}
		if n == 0 {
			if n, err = leaseUpdate(ctx, txn, spanner.Statement{SQL: insertLeaseSQL, Params: params}); err != nil {
				return fmt.Errorf("inserting lease: %w", err)
			}
		}
		acquired = n > 0
		row, err := txn.ReadRow(ctx, "pipeline_leases", spanner.Key{name},
			[]string{"name", paramLeaseHolder, "acquired_at", "expires_at"})
		if err != nil {
			return fmt.Errorf("reading lease: %w", err)
		}
		return row.ToStruct(&rec)
	})
	if err != nil {
		return repository.Lease{}, false, err
	}
	return repository.Lease(rec), acquired, nil
}

// ReleaseLease sets holder's expires_at to now; the row stays.
func (r *LeaseRepository) ReleaseLease(ctx context.Context, name, holder string) error {
	_, err := r.client.ReadWriteTransaction(ctx, func(ctx context.Context, txn *spanner.ReadWriteTransaction) error {
		_, err := leaseUpdate(ctx, txn, spanner.Statement{
			SQL: `UPDATE pipeline_leases SET expires_at = CURRENT_TIMESTAMP()
				WHERE name = @name AND holder = @holder AND expires_at > CURRENT_TIMESTAMP()`,
			Params: map[string]any{paramLeaseName: name, paramLeaseHolder: holder},
		})
		return err
	})
	if err != nil {
		return fmt.Errorf("releasing lease: %w", err)
	}
	return nil
}

// leaseUpdate runs a lease statement at high priority, whatever the client's query priority: the
// pipeline's client runs its statements at low priority ([WithBatchPriority]), and a renewal
// starved by user traffic would drop the lease and stop every job.
func leaseUpdate(ctx context.Context, txn *spanner.ReadWriteTransaction, stmt spanner.Statement) (int64, error) {
	return txn.UpdateWithOptions(ctx, stmt, spanner.QueryOptions{Priority: spannerpb.RequestOptions_PRIORITY_HIGH})
}
