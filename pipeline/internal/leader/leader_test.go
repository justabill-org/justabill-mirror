package leader_test

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/health"
	"github.com/justabill-org/justabill/pipeline/internal/leader"
)

// failsafe bounds every wait on the elector's goroutines, so a bug fails the test instead of
// hanging it. The tests never sleep.
const failsafe = 5 * time.Second

const (
	me    = "pipeline-7f9c-0badc0de"
	other = "pipeline-4d2e-cafef00d"
)

var errSpanner = errors.New("spanner: unavailable")

// waiter is one call to the fake clock's after.
type waiter struct {
	d  time.Duration
	ch chan time.Time
}

// fakeClock hands each after call to the test, which fires it. The elector has at most one
// wait outstanding: Run's while standby, the renewals' while leading.
type fakeClock struct {
	mu    sync.Mutex
	t     time.Time
	waits chan waiter
}

func newFakeClock() *fakeClock {
	return &fakeClock{t: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC), waits: make(chan waiter, 16)}
}

func (c *fakeClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

// advance moves the clock without waking anything, as a slow call or a late wake-up would.
func (c *fakeClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func (c *fakeClock) after(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	c.waits <- waiter{d: d, ch: ch}
	return ch
}

// fireNext waits for the elector's next call to after, checks its duration, runs change, then
// moves the clock past the wait and wakes it. A test changes the store only in change: once the
// elector waits, it has finished with the previous wake-up.
func (c *fakeClock) fireNext(t *testing.T, want time.Duration, change ...func()) {
	t.Helper()
	var w waiter
	select {
	case w = <-c.waits:
	case <-time.After(failsafe):
		t.Fatalf("the elector never waited (want a %v wait)", want)
	}
	if w.d != want {
		t.Fatalf("the elector waited %v, want %v", w.d, want)
	}
	for _, f := range change {
		f()
	}
	c.mu.Lock()
	c.t = c.t.Add(w.d)
	now := c.t
	c.mu.Unlock()
	w.ch <- now
}

// fakeStore is a lease table with one row. It doesn't model expiry: tests hand the lease over
// with setHolder.
type fakeStore struct {
	mu        sync.Mutex
	holder    string
	expiresAt time.Time
	err       error
	released  []string
	// releaseCtxErr is the context error ReleaseLease saw, which must be nil on shutdown.
	releaseCtxErr error
	// during runs inside each AcquireLease call before it answers; a test moves the fake clock in
	// it to model a slow call.
	during func()
	// budgets holds how long each AcquireLease call had before its context's deadline.
	budgets []time.Duration
}

func (f *fakeStore) AcquireLease(
	ctx context.Context, name, holder string, ttl time.Duration,
) (repository.Lease, bool, error) {
	f.mu.Lock()
	if deadline, ok := ctx.Deadline(); ok {
		f.budgets = append(f.budgets, time.Until(deadline))
	}
	during := f.during
	f.mu.Unlock()
	if during != nil {
		during()
	}

	f.mu.Lock()
	defer f.mu.Unlock()
	if f.err != nil {
		return repository.Lease{}, false, f.err
	}
	if err := ctx.Err(); err != nil {
		return repository.Lease{}, false, err
	}
	if f.holder == "" || f.holder == holder {
		f.holder = holder
		f.expiresAt = f.expiresAt.Add(ttl)
		return repository.Lease{Name: name, Holder: holder, ExpiresAt: f.expiresAt}, true, nil
	}
	return repository.Lease{Name: name, Holder: f.holder, ExpiresAt: f.expiresAt}, false, nil
}

func (f *fakeStore) ReleaseLease(ctx context.Context, _, holder string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.releaseCtxErr = ctx.Err()
	f.released = append(f.released, holder)
	if f.holder == holder {
		f.holder = ""
	}
	return nil
}

func (f *fakeStore) setHolder(h string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.holder = h
}

func (f *fakeStore) setErr(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeStore) setDuring(during func()) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.during = during
}

func (f *fakeStore) callBudgets() []time.Duration {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.budgets)
}

func (f *fakeStore) releases() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.released)
}

// recorder is a slog handler that keeps messages, safe for concurrent use.
type recorder struct {
	mu   sync.Mutex
	msgs []string
}

func (r *recorder) Enabled(context.Context, slog.Level) bool { return true }
func (r *recorder) WithAttrs([]slog.Attr) slog.Handler       { return r }
func (r *recorder) WithGroup(string) slog.Handler            { return r }

func (r *recorder) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.msgs = append(r.msgs, rec.Message)
	return nil
}

func (r *recorder) count(msg string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, m := range r.msgs {
		if m == msg {
			n++
		}
	}
	return n
}

func newElector(store *fakeStore, clk *fakeClock, rec *recorder) *leader.Elector {
	return leader.New(store, me, slog.New(rec), leader.WithClock(clk.now, clk.after))
}

// leadRecorder is a lead function that reports each term's context and when it returned.
type leadRecorder struct {
	terms   chan context.Context
	stopped chan struct{}
}

func newLeadRecorder() *leadRecorder {
	return &leadRecorder{terms: make(chan context.Context, 4), stopped: make(chan struct{}, 4)}
}

func (l *leadRecorder) lead(ctx context.Context) {
	l.terms <- ctx
	<-ctx.Done()
	l.stopped <- struct{}{}
}

func (l *leadRecorder) nextTerm(t *testing.T) context.Context {
	t.Helper()
	select {
	case ctx := <-l.terms:
		return ctx
	case <-time.After(failsafe):
		t.Fatal("the elector never started leading")
		return nil
	}
}

func (l *leadRecorder) waitStopped(t *testing.T) {
	t.Helper()
	select {
	case <-l.stopped:
	case <-time.After(failsafe):
		t.Fatal("the leader's context was never cancelled")
	}
}

func waitDone(t *testing.T, done <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-done:
	case <-time.After(failsafe):
		t.Fatalf("%s never returned", what)
	}
}

func checkLease(t *testing.T, e *leader.Elector, role, holder string) {
	t.Helper()
	if l := e.Lease(); l.Role != role || l.Holder != holder {
		t.Errorf("Lease() = %+v, want role %s, holder %s", l, role, holder)
	}
}

// Run leads while it holds the lease, renews every 20 s, steps down when another process takes
// the lease, retries every 15 s from standby, leads again, and releases the lease on shutdown.
func TestRun_LeadsStepsDownAndLeadsAgain(t *testing.T) {
	store, clk, rec := &fakeStore{}, newFakeClock(), &recorder{}
	e := newElector(store, clk, rec)
	l := newLeadRecorder()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.Run(ctx, l.lead)
	}()

	l.nextTerm(t)
	checkLease(t, e, health.RoleLeader, me)
	clk.fireNext(t, leader.DefaultRenewEvery) // renewal succeeds
	checkLease(t, e, health.RoleLeader, me)

	// Renewal refused: another process has it.
	clk.fireNext(t, leader.DefaultRenewEvery, func() { store.setHolder(other) })
	l.waitStopped(t)
	if rec.count("lease_lost") != 1 {
		t.Errorf("lease_lost logged %d times, want 1", rec.count("lease_lost"))
	}

	clk.fireNext(t, leader.DefaultRetryEvery) // standby retry: still held
	checkLease(t, e, health.RoleStandby, other)

	clk.fireNext(t, leader.DefaultRetryEvery, func() { store.setHolder("") }) // standby retry: free
	l.nextTerm(t)
	checkLease(t, e, health.RoleLeader, me)
	if rec.count("lease_acquired") != 2 {
		t.Errorf("lease_acquired logged %d times, want 2", rec.count("lease_acquired"))
	}

	cancel()
	l.waitStopped(t)
	waitDone(t, done, "Run")
	if got := store.releases(); !slices.Equal(got, []string{me}) {
		t.Errorf("released %v, want [%s]", got, me)
	}
	if store.releaseCtxErr != nil {
		t.Errorf("release ran with a done context: %v", store.releaseCtxErr)
	}
	checkLease(t, e, health.RoleStandby, me)
}

// Failed renewals keep the lead until none has succeeded for 50 s by the local clock; then the
// leader cancels its work before the 60 s TTL can lapse.
func TestRun_StepsDownWhenRenewalsFailFor50s(t *testing.T) {
	store, clk, rec := &fakeStore{}, newFakeClock(), &recorder{}
	e := newElector(store, clk, rec)
	l := newLeadRecorder()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go e.Run(ctx, l.lead)

	term := l.nextTerm(t)
	clk.fireNext(t, 20*time.Second, func() { store.setErr(errSpanner) }) // t=20s: fails
	clk.fireNext(t, 20*time.Second)                                      // t=40s: fails
	if term.Err() != nil {
		t.Fatal("stepped down before 50 s without a renewal")
	}
	clk.fireNext(t, 10*time.Second) // t=50s: gives up without another call
	l.waitStopped(t)
	checkLease(t, e, health.RoleStandby, me)
	if rec.count("lease attempt failed") != 2 || rec.count("lease_lost") != 1 {
		t.Errorf("logged %d failed attempts and %d losses, want 2 and 1",
			rec.count("lease attempt failed"), rec.count("lease_lost"))
	}

	// Back on standby, Run keeps trying.
	clk.fireNext(t, leader.DefaultRetryEvery, func() { store.setErr(nil) })
	l.nextTerm(t)
}

// A renewal that succeeds after a failure resets the 50 s count.
func TestRun_RenewalAfterFailureKeepsLead(t *testing.T) {
	store, clk, rec := &fakeStore{}, newFakeClock(), &recorder{}
	e := newElector(store, clk, rec)
	l := newLeadRecorder()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go e.Run(ctx, l.lead)

	term := l.nextTerm(t)
	clk.fireNext(t, 20*time.Second, func() { store.setErr(errSpanner) }) // t=20s: fails
	clk.fireNext(t, 20*time.Second, func() { store.setErr(nil) })        // t=40s: succeeds
	// A full interval again, not the 10 s that was left before the success.
	clk.fireNext(t, 20*time.Second)
	clk.fireNext(t, 20*time.Second)
	if term.Err() != nil {
		t.Error("lost the lead after a successful renewal")
	}
	checkLease(t, e, health.RoleLeader, me)
}

// A renewal that hangs until its bound and fails doesn't push the next one back: the next renewal
// is still due 20 s after the failed one started, so the leader gets a second try before 50 s
// (#517).
func TestRun_SlowFailedRenewalGetsASecondTry(t *testing.T) {
	store, clk, rec := &fakeStore{}, newFakeClock(), &recorder{}
	e := newElector(store, clk, rec)
	l := newLeadRecorder()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go e.Run(ctx, l.lead)

	term := l.nextTerm(t)
	// t=20s: the renewal hangs for its whole 10 s bound, then fails.
	clk.fireNext(t, 20*time.Second, func() {
		store.setErr(errSpanner)
		store.setDuring(func() { clk.advance(leader.DefaultRenewEvery / 2) })
	})
	// t=30s: the next renewal is due at 40 s, not at 50 s, where the leader would give up untried.
	clk.fireNext(t, 10*time.Second, func() {
		store.setErr(nil)
		store.setDuring(nil)
	})
	clk.fireNext(t, 20*time.Second) // t=40s: succeeded, so the next one is a full interval later
	if term.Err() != nil {
		t.Error("lost the lead after one slow renewal")
	}
	checkLease(t, e, health.RoleLeader, me)
}

// A renewal that starts late gets only the time left before 50 s, so it can't run past the point
// where the leader must have stopped its work.
func TestRun_RenewalBoundedByTimeLeft(t *testing.T) {
	store, clk, rec := &fakeStore{}, newFakeClock(), &recorder{}
	e := newElector(store, clk, rec)
	l := newLeadRecorder()
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go e.Run(ctx, l.lead)

	l.nextTerm(t)
	clk.fireNext(t, 20*time.Second, func() { store.setErr(errSpanner) }) // t=20s: fails
	// The t=40s wake-up comes 3 s late: the renewal at 43 s gets 7 s, not the usual 10 s.
	clk.fireNext(t, 20*time.Second, func() { clk.advance(3 * time.Second) })
	clk.fireNext(t, 7*time.Second) // t=50s: gives up
	l.waitStopped(t)

	budgets := store.callBudgets()
	if len(budgets) != 3 {
		t.Fatalf("%d lease calls, want 3", len(budgets))
	}
	if got := budgets[1]; got <= 9*time.Second || got > 10*time.Second {
		t.Errorf("an on-time renewal had %v, want 10s", got)
	}
	if got := budgets[2]; got <= 6*time.Second || got > 7*time.Second {
		t.Errorf("the late renewal had %v, want the 7s left before the loss threshold", got)
	}
}

// A shutdown on standby returns without releasing anything.
func TestRun_ShutdownOnStandby(t *testing.T) {
	store, clk, rec := &fakeStore{holder: other}, newFakeClock(), &recorder{}
	e := newElector(store, clk, rec)
	l := newLeadRecorder()
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() {
		defer close(done)
		e.Run(ctx, l.lead)
	}()

	clk.fireNext(t, leader.DefaultRetryEvery)
	checkLease(t, e, health.RoleStandby, other)
	if rec.count("lease held by another process") != 1 {
		t.Errorf("logged the holder %d times over two refusals, want once", rec.count("lease held by another process"))
	}
	cancel()
	waitDone(t, done, "Run")
	if got := store.releases(); len(got) != 0 {
		t.Errorf("released %v on standby", got)
	}
}

// Hold refuses at once, naming the holder, when another process has the lease.
func TestHold_RefusedNamesHolder(t *testing.T) {
	store := &fakeStore{holder: other}
	e := newElector(store, newFakeClock(), &recorder{})
	_, _, err := e.Hold(t.Context(), false)
	if !errors.Is(err, leader.ErrHeld) || !strings.Contains(err.Error(), other) {
		t.Errorf("Hold = %v, want ErrHeld naming %s", err, other)
	}
}

// A database error isn't reported as someone else holding the lease.
func TestHold_StoreError(t *testing.T) {
	store := &fakeStore{err: errSpanner}
	e := newElector(store, newFakeClock(), &recorder{})
	_, _, err := e.Hold(t.Context(), false)
	if !errors.Is(err, errSpanner) || errors.Is(err, leader.ErrHeld) {
		t.Errorf("Hold = %v, want the store's error", err)
	}
}

// With wait, Hold retries every 15 s until the lease is free, keeps it renewed, and gives up its
// context when another process takes it. release ends the lease.
func TestHold_WaitsRenewsAndReleases(t *testing.T) {
	store, clk := &fakeStore{holder: other}, newFakeClock()
	e := newElector(store, clk, &recorder{})

	type held struct {
		ctx     context.Context
		release func()
		err     error
	}
	got := make(chan held, 1)
	go func() {
		ctx, release, err := e.Hold(t.Context(), true)
		got <- held{ctx, release, err}
	}()

	clk.fireNext(t, leader.DefaultRetryEvery)                                 // still held
	clk.fireNext(t, leader.DefaultRetryEvery, func() { store.setHolder("") }) // free
	var h held
	select {
	case h = <-got:
	case <-time.After(failsafe):
		t.Fatal("Hold never returned")
	}
	if h.err != nil {
		t.Fatalf("Hold: %v", h.err)
	}
	checkLease(t, e, health.RoleLeader, me)

	clk.fireNext(t, leader.DefaultRenewEvery) // renewed
	if h.ctx.Err() != nil {
		t.Fatal("the held context ended after a successful renewal")
	}
	h.release()
	if h.ctx.Err() == nil {
		t.Error("the held context outlived release")
	}
	if got := store.releases(); !slices.Equal(got, []string{me}) {
		t.Errorf("released %v, want [%s]", got, me)
	}
}

// Losing a held lease cancels the work's context.
func TestHold_LossCancels(t *testing.T) {
	store, clk := &fakeStore{}, newFakeClock()
	e := newElector(store, clk, &recorder{})
	ctx, release, err := e.Hold(t.Context(), false)
	if err != nil {
		t.Fatalf("Hold: %v", err)
	}
	defer release()
	clk.fireNext(t, leader.DefaultRenewEvery, func() { store.setHolder(other) })
	select {
	case <-ctx.Done():
	case <-time.After(failsafe):
		t.Fatal("the held context survived losing the lease")
	}
	if !errors.Is(context.Cause(ctx), leader.ErrLost) {
		t.Errorf("the held context's cause is %v, want ErrLost", context.Cause(ctx))
	}
}

// Waiting for the lease stops when the caller's context ends.
func TestHold_WaitCancelled(t *testing.T) {
	store, clk := &fakeStore{holder: other}, newFakeClock()
	e := newElector(store, clk, &recorder{})
	ctx, cancel := context.WithCancel(t.Context())
	errs := make(chan error, 1)
	go func() {
		_, _, err := e.Hold(ctx, true)
		errs <- err
	}()
	clk.fireNext(t, leader.DefaultRetryEvery)
	cancel()
	select {
	case err := <-errs:
		if !errors.Is(err, context.Canceled) {
			t.Errorf("Hold = %v, want context.Canceled", err)
		}
	case <-time.After(failsafe):
		t.Fatal("Hold kept waiting after its context ended")
	}
}

func TestHolderID(t *testing.T) {
	t.Setenv("HOSTNAME", "pipeline-7f9c")
	a, err := leader.HolderID()
	if err != nil {
		t.Fatal(err)
	}
	b, err := leader.HolderID()
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^pipeline-7f9c-[0-9a-f]{8}$`).MatchString(a) || a == b {
		t.Errorf("HolderID() = %q then %q, want the host plus 8 new hex characters each time", a, b)
	}
}

func TestNewStartsOnStandby(t *testing.T) {
	e := leader.New(&fakeStore{}, me, slog.New(slog.DiscardHandler))
	checkLease(t, e, health.RoleStandby, "")
	if e.Holder() != me {
		t.Errorf("Holder() = %q, want %q", e.Holder(), me)
	}
}
