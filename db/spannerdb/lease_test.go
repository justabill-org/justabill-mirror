package spannerdb_test

import (
	"testing"
	"time"

	"cloud.google.com/go/spanner"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
)

const (
	leaseTTL   = time.Minute
	shortTTL   = 300 * time.Millisecond
	holderA    = "pipeline-a-0badc0de"
	holderB    = "pipeline-b-cafef00d"
	leaseSlack = 2 * time.Second // the emulator's clock against the test's
)

func acquire(
	t *testing.T, store *spannerdb.LeaseRepository, holder string, ttl time.Duration,
) (repository.Lease, bool) {
	t.Helper()
	lease, ok, err := store.AcquireLease(t.Context(), repository.PipelineLeaseName, holder, ttl)
	if err != nil {
		t.Fatalf("AcquireLease(%s): %v", holder, err)
	}
	return lease, ok
}

func release(t *testing.T, store *spannerdb.LeaseRepository, holder string) {
	t.Helper()
	if err := store.ReleaseLease(t.Context(), repository.PipelineLeaseName, holder); err != nil {
		t.Fatalf("ReleaseLease(%s): %v", holder, err)
	}
}

// expiresIn checks that lease lapses about ttl from now.
func expiresIn(t *testing.T, lease repository.Lease, ttl time.Duration) {
	t.Helper()
	if d := time.Until(lease.ExpiresAt) - ttl; d < -leaseSlack || d > leaseSlack {
		t.Errorf("expires_at %v is %v from now, want about %v", lease.ExpiresAt, time.Until(lease.ExpiresAt), ttl)
	}
}

// The design's lease semantics: acquire, a second holder refused and told who holds it, renew,
// release keeping the row, and takeover by another holder after release.
func TestLease_AcquireRefuseRenewRelease(t *testing.T) {
	store := spannerdb.NewLeaseStore(&spannerdb.Client{Spanner: testdb.New(t)})

	first, ok := acquire(t, store, holderA, leaseTTL)
	if !ok || first.Holder != holderA || first.Name != repository.PipelineLeaseName {
		t.Fatalf("A's first acquire = %+v, %v; want A holding it", first, ok)
	}
	expiresIn(t, first, leaseTTL)

	refused, ok := acquire(t, store, holderB, leaseTTL)
	if ok {
		t.Fatal("B acquired a lease A holds")
	}
	if refused.Holder != holderA || !refused.ExpiresAt.Equal(first.ExpiresAt) {
		t.Errorf("B was told %+v, want A's lease %+v", refused, first)
	}

	renewed, ok := acquire(t, store, holderA, leaseTTL)
	if !ok || !renewed.ExpiresAt.After(first.ExpiresAt) {
		t.Errorf("A's renewal = %+v, %v; want expires_at after %v", renewed, ok, first.ExpiresAt)
	}
	if !renewed.AcquiredAt.Equal(first.AcquiredAt) {
		t.Errorf("renewal moved acquired_at from %v to %v", first.AcquiredAt, renewed.AcquiredAt)
	}

	// B releasing A's lease does nothing.
	release(t, store, holderB)
	if _, ok = acquire(t, store, holderB, leaseTTL); ok {
		t.Fatal("B acquired A's lease after B's release")
	}

	release(t, store, holderA)
	taken, ok := acquire(t, store, holderB, leaseTTL)
	if !ok || taken.Holder != holderB {
		t.Fatalf("B after A's release = %+v, %v; want B holding it", taken, ok)
	}
	if !taken.AcquiredAt.After(first.AcquiredAt) {
		t.Errorf("takeover kept acquired_at %v", taken.AcquiredAt)
	}
}

// A lapsed lease goes to whoever asks next, and a released lease can be taken back by its
// last holder.
func TestLease_TakeoverAfterExpiryAndReacquire(t *testing.T) {
	store := spannerdb.NewLeaseStore(&spannerdb.Client{Spanner: testdb.New(t)})

	if _, ok := acquire(t, store, holderA, shortTTL); !ok {
		t.Fatal("A didn't acquire an empty lease")
	}
	time.Sleep(2 * shortTTL)
	taken, ok := acquire(t, store, holderB, leaseTTL)
	if !ok || taken.Holder != holderB {
		t.Fatalf("B after A's lease lapsed = %+v, %v; want B holding it", taken, ok)
	}
	if _, ok = acquire(t, store, holderA, leaseTTL); ok {
		t.Fatal("A took the lease back while B holds it")
	}

	release(t, store, holderB)
	again, ok := acquire(t, store, holderB, leaseTTL)
	if !ok {
		t.Fatal("B couldn't re-acquire after releasing")
	}
	if !again.AcquiredAt.After(taken.AcquiredAt) {
		t.Errorf("re-acquire kept acquired_at %v from before the release", again.AcquiredAt)
	}
	expiresIn(t, again, leaseTTL)
}

// Release keeps the row, with expires_at at the release time, so the last holder stays visible.
func TestLease_ReleaseKeepsRow(t *testing.T) {
	client := testdb.New(t)
	store := spannerdb.NewLeaseStore(&spannerdb.Client{Spanner: client})

	acquire(t, store, holderA, leaseTTL)
	release(t, store, holderA)

	row, err := client.Single().ReadRow(t.Context(), "pipeline_leases",
		spanner.Key{repository.PipelineLeaseName}, []string{"holder", "expires_at"})
	if err != nil {
		t.Fatalf("reading the released row: %v", err)
	}
	var holder string
	var expiresAt time.Time
	if err = row.Columns(&holder, &expiresAt); err != nil {
		t.Fatal(err)
	}
	if holder != holderA || time.Until(expiresAt) > leaseSlack || time.Until(expiresAt) < -leaseSlack {
		t.Errorf("row after release: holder %s, expires_at %v; want A, about now", holder, expiresAt)
	}
}
