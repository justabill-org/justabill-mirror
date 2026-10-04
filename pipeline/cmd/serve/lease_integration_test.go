package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
	"github.com/justabill-org/justabill/pipeline/internal/health"
	"github.com/justabill-org/justabill/pipeline/internal/leader"
	"github.com/justabill-org/justabill/pipeline/internal/scheduler"
)

// The design's lease timings scaled down 10 times, which leaves each lease call a second (half
// the renewal interval) on an emulator busy with -race and other packages' tests (#517). The
// standby retry is shorter still, so a takeover takes about a second.
const (
	testTTL        = 6 * time.Second
	testRenewEvery = 2 * time.Second
	testLossAfter  = 5 * time.Second
	testRetryEvery = 750 * time.Millisecond
)

// instance is one serve process in the integration test.
type instance struct {
	el     *leader.Elector
	hs     *health.Server
	ran    chan struct{} // gets a value each time the instance's job starts
	cancel context.CancelFunc
	done   chan struct{}
}

func startInstance(t *testing.T, sc *spannerdb.Client, host string) *instance {
	t.Helper()
	logger := slog.New(slog.DiscardHandler)
	in := &instance{ran: make(chan struct{}, 8), done: make(chan struct{})}
	s := scheduler.New(logger, scheduler.WithStartWindow(10*time.Millisecond))
	s.Register(scheduler.Job{Name: "probe", Interval: time.Hour, Run: func(ctx context.Context) error {
		in.ran <- struct{}{}
		<-ctx.Done() // runs until the lease ends
		return ctx.Err()
	}})
	in.el = leader.New(spannerdb.NewLeaseStore(sc), host+"-0badc0de", logger,
		leader.WithTimings(testTTL, testRenewEvery, testLossAfter, testRetryEvery))
	in.hs = health.New(logger, s, sc, health.WithLease(in.el))

	var ctx context.Context
	ctx, in.cancel = context.WithCancel(t.Context())
	go func() {
		defer close(in.done)
		serve(ctx, logger, s, in.el, in.hs)
	}()
	t.Cleanup(func() {
		in.cancel()
		<-in.done
	})
	return in
}

// statusLease is the role and lease holder an instance's /status reports.
type statusLease struct{ role, holder string }

// status reads the instance's /status.
func (in *instance) status(t *testing.T) statusLease {
	t.Helper()
	rec := httptest.NewRecorder()
	in.hs.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/status", http.NoBody))
	var body struct {
		Role  string `json:"role"`
		Lease *struct {
			Holder    string    `json:"holder"`
			ExpiresAt time.Time `json:"expires_at"`
		} `json:"lease"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("/status: %v", err)
	}
	if body.Lease == nil || body.Lease.ExpiresAt.IsZero() {
		t.Fatalf("/status has no lease: %s", rec.Body.String())
	}
	return statusLease{body.Role, body.Lease.Holder}
}

// Two serve instances on one database: exactly one runs jobs, and when the leader shuts down,
// the other takes over within one standby retry (15 s in production, plus the job start window).
func TestServe_TwoInstancesOneLeader(t *testing.T) {
	sc := &spannerdb.Client{Spanner: testdb.New(t)}

	a := startInstance(t, sc, "pipeline-a")
	waitFor(t, a.ran, "the first instance to run its job")
	b := startInstance(t, sc, "pipeline-b")

	// Two renewals and several standby retries later, B still hasn't run anything.
	time.Sleep(2 * testRenewEvery)
	select {
	case <-b.ran:
		t.Fatal("both instances ran the job")
	default:
	}
	if got := a.status(t); got != (statusLease{health.RoleLeader, a.el.Holder()}) {
		t.Errorf("leader /status = %+v", got)
	}
	if got := b.status(t); got != (statusLease{health.RoleStandby, a.el.Holder()}) {
		t.Errorf("standby /status = %+v, want standby naming %s", got, a.el.Holder())
	}

	// SIGTERM to the leader: it releases the lease, and B takes over at its next retry. That's one
	// retry plus one lease call (at most half a renewal interval) and the job start window, well
	// short of the TTL that B would wait out if A hadn't released the lease.
	a.cancel()
	waitFor(t, a.done, "the leader to shut down")
	released := time.Now()
	takeoverWithin := testRetryEvery + testRenewEvery
	select {
	case <-b.ran:
	case <-time.After(takeoverWithin + time.Second):
		t.Fatal("the standby didn't take over after the leader released the lease")
	}
	if took := time.Since(released); took > takeoverWithin {
		t.Errorf("takeover took %v, want about one retry (%v)", took, testRetryEvery)
	}
	if got := b.status(t); got != (statusLease{health.RoleLeader, b.el.Holder()}) {
		t.Errorf("new leader /status = %+v", got)
	}
	select {
	case <-a.ran:
		t.Error("the old leader ran its job again after shutdown")
	default:
	}
}
