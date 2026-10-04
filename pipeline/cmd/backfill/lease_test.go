package main

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
	"github.com/justabill-org/justabill/pipeline/internal/leader"
)

const serveHolder = "pipeline-serve-0badc0de"

func newLeaseStore(t *testing.T) *spannerdb.LeaseRepository {
	t.Helper()
	store := spannerdb.NewLeaseStore(&spannerdb.Client{Spanner: testdb.New(t)})
	if ok, err := serveAcquires(t, store); err != nil || !ok {
		t.Fatalf("serve's acquire = %v, %v", ok, err)
	}
	return store
}

// serveAcquires tries to take the lease as a serve replica would.
func serveAcquires(t *testing.T, store *spannerdb.LeaseRepository) (bool, error) {
	t.Helper()
	_, ok, err := store.AcquireLease(t.Context(), repository.PipelineLeaseName, serveHolder, time.Minute)
	return ok, err
}

// A backfill while serve holds the lease exits with an error that names serve and says what to do.
func TestHoldLease_RefusedWhileServeHolds(t *testing.T) {
	store := newLeaseStore(t)
	_, _, err := holdLease(t.Context(), slog.New(slog.DiscardHandler), store, false)
	if !errors.Is(err, leader.ErrHeld) || !strings.Contains(err.Error(), serveHolder) ||
		!strings.Contains(err.Error(), "--wait-for-lease") {
		t.Errorf("holdLease = %v, want ErrHeld naming %s and --wait-for-lease", err, serveHolder)
	}
}

// With --wait-for-lease, the backfill waits until serve releases the lease, then holds it until
// it releases it itself.
func TestHoldLease_WaitsForServe(t *testing.T) {
	store := newLeaseStore(t)
	type held struct {
		ctx     context.Context
		release func()
		err     error
	}
	got := make(chan held, 1)
	go func() {
		ctx, release, err := holdLease(t.Context(), slog.New(slog.DiscardHandler), store, true,
			leader.WithTimings(leader.DefaultTTL, time.Second, 2*time.Second, 100*time.Millisecond))
		got <- held{ctx, release, err}
	}()

	select {
	case h := <-got:
		t.Fatalf("holdLease returned while serve held the lease: %v", h.err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := store.ReleaseLease(t.Context(), repository.PipelineLeaseName, serveHolder); err != nil {
		t.Fatal(err)
	}

	var h held
	select {
	case h = <-got:
	case <-time.After(5 * time.Second):
		t.Fatal("holdLease kept waiting after serve released the lease")
	}
	if h.err != nil {
		t.Fatalf("holdLease: %v", h.err)
	}
	if ok, err := serveAcquires(t, store); err != nil || ok {
		t.Errorf("serve took the lease back during the backfill (%v, %v)", ok, err)
	}
	h.release()
	if h.ctx.Err() == nil {
		t.Error("the backfill's context outlived release")
	}
	if ok, err := serveAcquires(t, store); err != nil || !ok {
		t.Errorf("serve couldn't take the lease after the backfill released it (%v, %v)", ok, err)
	}
}
