package repository

import (
	"context"
	"time"
)

// PipelineLeaseName names the one lease that serve and backfill share
// (docs/design/80-pipeline-operability.md, Decision 1A).
const PipelineLeaseName = "pipeline"

// Lease is a pipeline_leases row: who holds a named lease and until when, by Spanner's clock.
type Lease struct {
	Name   string
	Holder string
	// AcquiredAt is when Holder took the lease; renewals keep it.
	AcquiredAt time.Time
	// ExpiresAt is when the lease lapses unless Holder renews it. A released lease keeps its
	// row with ExpiresAt at the release time.
	ExpiresAt time.Time
}

// LeaseStore takes, renews and releases named leases. Every comparison uses the database's
// clock inside a read-write transaction, so the callers' clocks don't matter.
type LeaseStore interface {
	// AcquireLease takes the named lease for holder for ttl, or renews it when holder already
	// has it. It reports whether holder holds the lease now, and returns the row as it stands
	// after the call: when acquired is false, lease names the process that holds it.
	AcquireLease(ctx context.Context, name, holder string, ttl time.Duration) (lease Lease, acquired bool, err error)
	// ReleaseLease ends holder's lease now, so another process can take it without waiting for
	// the TTL. The row stays. It does nothing when holder doesn't hold the lease.
	ReleaseLease(ctx context.Context, name, holder string) error
}
