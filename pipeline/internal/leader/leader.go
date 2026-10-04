// Package leader elects the one pipeline process that may call upstream APIs, through a lease
// row in Spanner (docs/design/80-pipeline-operability.md, Decision 1A and "Lease semantics").
//
// A pipeline-serve replica campaigns with [Elector.Run]: it runs its jobs only while it holds
// the lease, and goes back to standby when it loses it. pipeline-backfill takes the same lease
// with [Elector.Hold] and fails when another process holds it.
package leader

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/health"
	"github.com/justabill-org/justabill/pipeline/internal/redact"
)

// The design's timings. The leader renews well inside the TTL and gives up before it can lapse,
// so two processes never both believe they hold the lease while their clocks run at one rate.
const (
	DefaultTTL        = 60 * time.Second
	DefaultRenewEvery = 20 * time.Second
	DefaultLossAfter  = 50 * time.Second
	DefaultRetryEvery = 15 * time.Second
	// releaseTimeout bounds the release on shutdown, which gets a fresh context.
	releaseTimeout = 5 * time.Second
	// attemptDivisor bounds one lease call to a fraction of the renewal interval, so a hung
	// call leaves time for another renewal before the loss threshold.
	attemptDivisor = 2
	// holderSuffixBytes random bytes (8 hex characters) tell apart two processes on one host.
	holderSuffixBytes = 4
)

// ErrHeld is returned by [Elector.Hold] when another process holds the lease; the error names it.
var ErrHeld = errors.New("the pipeline lease is held by another process")

// ErrLost is the cause (see [context.Cause]) of a leader context cancelled because the lease
// was lost.
var ErrLost = errors.New("lost the pipeline lease")

// Option configures an Elector.
type Option func(*Elector)

// WithClock replaces [time.Now] and [time.After], for tests. now must carry a monotonic reading
// in production, which [time.Now] does.
func WithClock(now func() time.Time, after func(time.Duration) <-chan time.Time) Option {
	return func(e *Elector) { e.now, e.after = now, after }
}

// WithTimings replaces the design's TTL, renewal interval, loss threshold and standby retry
// interval, for tests. lossAfter must be shorter than ttl.
func WithTimings(ttl, renewEvery, lossAfter, retryEvery time.Duration) Option {
	return func(e *Elector) {
		e.ttl, e.renewEvery, e.lossAfter, e.retryEvery = ttl, renewEvery, lossAfter, retryEvery
	}
}

// WithName replaces the lease name, for tests that share a database.
func WithName(name string) Option {
	return func(e *Elector) { e.name = name }
}

// Elector takes and keeps the pipeline lease for one process.
type Elector struct {
	store  repository.LeaseStore
	holder string
	name   string
	log    *slog.Logger
	now    func() time.Time
	after  func(time.Duration) <-chan time.Time

	ttl, renewEvery, lossAfter, retryEvery time.Duration

	mu    sync.Mutex
	state health.Lease
}

// New returns an Elector for holder (see [HolderID]) that logs to log.
func New(store repository.LeaseStore, holder string, log *slog.Logger, opts ...Option) *Elector {
	e := &Elector{
		store:      store,
		holder:     holder,
		name:       repository.PipelineLeaseName,
		log:        log,
		now:        time.Now,
		after:      time.After,
		ttl:        DefaultTTL,
		renewEvery: DefaultRenewEvery,
		lossAfter:  DefaultLossAfter,
		retryEvery: DefaultRetryEvery,
		state:      health.Lease{Role: health.RoleStandby},
	}
	for _, opt := range opts {
		opt(e)
	}
	return e
}

// HolderID returns this process's lease holder ID: $HOSTNAME (the pod name) plus 8 random hex
// characters, so a restarted pod with the same name is a new holder.
func HolderID() (string, error) {
	host := os.Getenv("HOSTNAME")
	if host == "" {
		var err error
		if host, err = os.Hostname(); err != nil {
			return "", fmt.Errorf("hostname: %w", err)
		}
	}
	b := make([]byte, holderSuffixBytes)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("holder suffix: %w", err)
	}
	return host + "-" + hex.EncodeToString(b), nil
}

// Holder returns this process's holder ID.
func (e *Elector) Holder() string { return e.holder }

// Lease reports the lease as this process last saw it, for the health server's /status.
func (e *Elector) Lease() health.Lease {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state
}

// Run campaigns for the lease until ctx is done. Whenever this process holds it, Run calls lead
// with a context that is cancelled when the lease is lost or ctx is done, and keeps renewing the
// lease until lead returns. lead must return only after its context is done and its work has
// stopped: Run doesn't campaign again before that, so the work never runs twice. After a loss,
// Run goes back to standby and retries every retry interval; when ctx is done, it releases the
// lease with a fresh context and returns.
func (e *Elector) Run(ctx context.Context, lead func(ctx context.Context)) {
	for {
		if e.tryAcquire(ctx, e.attemptLimit()) {
			leaderCtx, stop := e.keep(ctx)
			lead(leaderCtx)
			stop()
			if ctx.Err() != nil {
				e.release(ctx)
				return
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-e.after(e.retryEvery):
		}
	}
}

// Hold takes the lease for work that runs once, such as a backfill. When another process holds
// it, Hold returns an error wrapping [ErrHeld] that names the holder, or with wait set, retries
// every retry interval until it gets the lease or ctx is done. On success it returns a context
// that is cancelled if the lease is lost, and a release function to call when the work is done.
func (e *Elector) Hold(ctx context.Context, wait bool) (context.Context, func(), error) {
	for {
		held, err := e.attempt(ctx, e.attemptLimit())
		if held {
			break
		}
		switch {
		case wait:
			if err != nil {
				e.logAttemptError(ctx, err)
			}
		case err != nil:
			return nil, nil, fmt.Errorf("taking the pipeline lease: %w", err)
		default:
			l := e.Lease()
			return nil, nil, fmt.Errorf("%w: %s until %s", ErrHeld, l.Holder, l.ExpiresAt.Format(time.RFC3339))
		}
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-e.after(e.retryEvery):
		}
	}
	leaderCtx, stop := e.keep(ctx)
	return leaderCtx, func() {
		stop()
		e.release(ctx)
	}, nil
}

// tryAcquire is attempt for callers that retry: it logs an error instead of returning it.
func (e *Elector) tryAcquire(ctx context.Context, limit time.Duration) bool {
	held, err := e.attempt(ctx, limit)
	if err != nil {
		e.logAttemptError(ctx, err)
	}
	return held
}

func (e *Elector) logAttemptError(ctx context.Context, err error) {
	// A shutdown cancels the call in flight; that isn't worth a warning.
	if ctx.Err() == nil {
		e.log.WarnContext(ctx, "lease attempt failed", "error", redact.Error(err))
	}
}

// attemptLimit is how long one lease call may take: half the renewal interval.
func (e *Elector) attemptLimit() time.Duration { return e.renewEvery / attemptDivisor }

// attempt tries once to take or renew the lease, bounded by limit, and records the outcome. It
// reports whether this process holds the lease; on an error, the recorded state doesn't change.
func (e *Elector) attempt(ctx context.Context, limit time.Duration) (bool, error) {
	callCtx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	lease, acquired, err := e.store.AcquireLease(callCtx, e.name, e.holder, e.ttl)
	if err != nil {
		return false, err
	}

	e.mu.Lock()
	prev := e.state
	e.state = health.Lease{Role: health.RoleStandby, Holder: lease.Holder, ExpiresAt: lease.ExpiresAt}
	if acquired {
		e.state.Role = health.RoleLeader
	}
	e.mu.Unlock()

	switch {
	case acquired && prev.Role != health.RoleLeader:
		e.log.InfoContext(ctx, "lease_acquired", "holder", e.holder, "expires_at", lease.ExpiresAt)
	case !acquired && prev.Holder != lease.Holder:
		e.log.InfoContext(ctx, "lease held by another process", "holder", lease.Holder,
			"expires_at", lease.ExpiresAt)
	}
	return acquired, nil
}

// keep renews the lease in the background. The returned context is cancelled when the lease is
// lost, with cause [ErrLost], or when ctx is done; stop ends the renewals and waits for them.
func (e *Elector) keep(ctx context.Context) (context.Context, func()) {
	leaderCtx, cancel := context.WithCancelCause(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		if e.renew(leaderCtx) {
			cancel(ErrLost)
		}
	}()
	return leaderCtx, func() {
		cancel(nil)
		<-done
	}
}

// renew renews the lease every renewal interval until ctx is done or the lease is lost: another
// process took it, or no renewal has succeeded for the loss threshold by the local clock. It
// reports whether the lease was lost.
//
// Renewals are due a renewal interval after the previous one started, however long it took, and
// each gets at most the time left before the loss threshold. So a renewal that hangs until its
// bound and fails still leaves room for another try before the leader gives up (#517).
func (e *Elector) renew(ctx context.Context) bool {
	lastOK := e.now()
	next := lastOK.Add(e.renewEvery)
	for {
		giveUp := lastOK.Add(e.lossAfter)
		wake := next
		if giveUp.Before(wake) {
			wake = giveUp
		}
		select {
		case <-ctx.Done():
			return false
		case <-e.after(wake.Sub(e.now())):
		}
		if ctx.Err() != nil {
			return false
		}
		start := e.now()
		since := start.Sub(lastOK)
		if since >= e.lossAfter {
			e.lost(ctx, "no renewal succeeded for "+since.Round(time.Second).String())
			return true
		}
		next = start.Add(e.renewEvery)
		if e.tryAcquire(ctx, min(e.attemptLimit(), e.lossAfter-since)) {
			// The TTL runs from a moment after start, so counting from start is conservative.
			lastOK = start
			continue
		}
		if e.Lease().Role != health.RoleLeader {
			e.lost(ctx, "another process took the lease")
			return true
		}
	}
}

// lost records the end of this process's leadership.
func (e *Elector) lost(ctx context.Context, reason string) {
	e.mu.Lock()
	e.state.Role = health.RoleStandby
	holder := e.state.Holder
	e.mu.Unlock()
	e.log.WarnContext(ctx, "lease_lost", "holder", e.holder, "reason", reason, "current_holder", holder)
}

// release ends the lease with a fresh context, since ctx may be cancelled by now.
func (e *Elector) release(ctx context.Context) {
	relCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), releaseTimeout)
	defer cancel()

	e.mu.Lock()
	e.state.Role = health.RoleStandby
	e.mu.Unlock()
	if err := e.store.ReleaseLease(relCtx, e.name, e.holder); err != nil {
		e.log.WarnContext(relCtx, "lease release failed; it lapses by itself", "error", redact.Error(err),
			"ttl", e.ttl.String())
		return
	}
	e.log.InfoContext(relCtx, "lease released", "holder", e.holder)
}
