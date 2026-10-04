package scheduler_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/justabill-org/justabill/obs"
	"github.com/justabill-org/justabill/obs/obstest"
	names "github.com/justabill-org/justabill/obs/semconv"
	"github.com/justabill-org/justabill/pipeline/internal/scheduler"
)

// failsafe bounds every wait on the scheduler's goroutines, so a bug fails the test instead
// of hanging it. The tests never sleep.
const failsafe = 5 * time.Second

// waiter is one call to the fake clock's after.
type waiter struct {
	d  time.Duration
	ch chan time.Time
}

// fakeClock hands each after call to the test, which fires it by calling fire.
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

func (c *fakeClock) after(d time.Duration) <-chan time.Time {
	ch := make(chan time.Time, 1)
	c.waits <- waiter{d: d, ch: ch}
	return ch
}

// nextWait returns the scheduler's next call to after.
func (c *fakeClock) nextWait(t *testing.T) waiter {
	t.Helper()
	select {
	case w := <-c.waits:
		return w
	case <-time.After(failsafe):
		t.Fatal("the scheduler never waited")
		return waiter{}
	}
}

// fire moves the clock past w and wakes its waiter.
func (c *fakeClock) fire(w waiter) {
	c.mu.Lock()
	c.t = c.t.Add(w.d)
	now := c.t
	c.mu.Unlock()
	w.ch <- now
}

// recorder is a slog handler that keeps records, safe for concurrent use.
type recorder struct {
	mu      sync.Mutex
	records []map[string]any
}

func (r *recorder) Enabled(context.Context, slog.Level) bool { return true }
func (r *recorder) WithAttrs([]slog.Attr) slog.Handler       { return r }
func (r *recorder) WithGroup(string) slog.Handler            { return r }

func (r *recorder) Handle(_ context.Context, rec slog.Record) error {
	m := map[string]any{"msg": rec.Message, "level": rec.Level}
	rec.Attrs(func(a slog.Attr) bool {
		m[a.Key] = a.Value.Any()
		return true
	})
	r.mu.Lock()
	defer r.mu.Unlock()
	r.records = append(r.records, m)
	return nil
}

func (r *recorder) finished() []map[string]any {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []map[string]any
	for _, m := range r.records {
		if m["msg"] == names.PipelineJobFinishedEvent {
			out = append(out, m)
		}
	}
	return out
}

type harness struct {
	clock *fakeClock
	log   *recorder
	s     *scheduler.Scheduler
}

func newHarness(randValue float64, jobs ...scheduler.Job) *harness {
	h := &harness{clock: newFakeClock(), log: &recorder{}}
	h.s = scheduler.New(slog.New(h.log),
		scheduler.WithClock(h.clock.now, h.clock.after),
		scheduler.WithRand(func() float64 { return randValue }))
	for _, j := range jobs {
		h.s.Register(j)
	}
	return h
}

// start starts the scheduler and, at the end of the test, stops it and checks every loop returned.
func (h *harness) start(t *testing.T) context.CancelFunc {
	t.Helper()
	ctx, cancel := context.WithCancel(t.Context())
	h.s.Start(ctx)
	t.Cleanup(func() {
		cancel()
		h.wait(t)
	})
	return cancel
}

func (h *harness) wait(t *testing.T) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), failsafe)
	defer cancel()
	if running := h.s.Wait(ctx); running != nil {
		t.Fatalf("jobs still running: %v", running)
	}
}

// finished returns the one justabill.pipeline.job.finished event, failing unless there is
// exactly one. The scheduler logs a run's event before its loop waits again, so a test that saw
// the next wait sees the event.
func (h *harness) finished(t *testing.T) map[string]any {
	t.Helper()
	got := h.log.finished()
	if len(got) != 1 {
		t.Fatalf("want one %s event, got %v", names.PipelineJobFinishedEvent, got)
	}
	return got[0]
}

// near reports whether a and b are within a microsecond: jitter goes through float64.
func near(a, b time.Duration) bool {
	return (a - b).Abs() <= time.Microsecond
}

func TestFirstRunWithinStartWindowThenJitteredInterval(t *testing.T) {
	cases := []struct {
		name      string
		rand      float64
		wantFirst time.Duration
		wantNext  time.Duration
	}{
		{"low", 0, 0, 54 * time.Minute},
		{"middle", 0.5, time.Minute, time.Hour},
		{"high", 0.75, 90 * time.Second, 63 * time.Minute},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var runs atomic.Int32
			h := newHarness(tc.rand, scheduler.Job{
				Name: "sync-bills", Interval: time.Hour, Timeout: 30 * time.Minute,
				Run: func(context.Context) error { runs.Add(1); return nil },
			})
			h.start(t)

			first := h.clock.nextWait(t)
			if !near(first.d, tc.wantFirst) {
				t.Errorf("first delay = %v, want %v", first.d, tc.wantFirst)
			}
			h.clock.fire(first)
			next := h.clock.nextWait(t)
			if runs.Load() != 1 {
				t.Errorf("runs = %d, want 1", runs.Load())
			}
			if !near(next.d, tc.wantNext) {
				t.Errorf("next delay = %v, want %v", next.d, tc.wantNext)
			}
		})
	}
}

func TestRetryIntervalAfterARunThatDidNotSucceed(t *testing.T) {
	failed := errors.New("page status 500")
	unavailable := fmt.Errorf("%w: maintenance", obs.ErrUnavailable)
	cases := []struct {
		name  string
		retry time.Duration
		errs  []error
		want  []time.Duration
	}{
		{
			name: "retries sooner until a run succeeds", retry: time.Hour,
			errs: []error{failed, unavailable, nil, failed},
			want: []time.Duration{time.Hour, time.Hour, 24 * time.Hour, time.Hour},
		},
		{
			name: "no retry interval waits the interval", retry: 0,
			errs: []error{failed, nil},
			want: []time.Duration{24 * time.Hour, 24 * time.Hour},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var runs atomic.Int32
			// rand 0.5 makes the jitter factor exactly 1.
			h := newHarness(0.5, scheduler.Job{
				Name: "load-uscode", Interval: 24 * time.Hour, RetryInterval: tc.retry, Timeout: 2 * time.Hour,
				Run: func(context.Context) error {
					n := runs.Add(1)
					return tc.errs[min(int(n), len(tc.errs))-1]
				},
			})
			h.start(t)
			h.clock.fire(h.clock.nextWait(t)) // the first run, within the start window
			for i, want := range tc.want {
				next := h.clock.nextWait(t)
				if !near(next.d, want) {
					t.Errorf("wait after run %d (err %v) = %v, want %v", i+1, tc.errs[i], next.d, want)
				}
				if i < len(tc.want)-1 {
					h.clock.fire(next)
				}
			}
		})
	}
}

func TestStartWindowOption(t *testing.T) {
	clock := newFakeClock()
	s := scheduler.New(slog.New(slog.DiscardHandler),
		scheduler.WithClock(clock.now, clock.after),
		scheduler.WithRand(func() float64 { return 0.5 }),
		scheduler.WithStartWindow(10*time.Second))
	s.Register(scheduler.Job{Name: "j", Interval: time.Hour, Run: func(context.Context) error { return nil }})
	ctx, cancel := context.WithCancel(t.Context())
	s.Start(ctx)
	if w := clock.nextWait(t); w.d != 5*time.Second {
		t.Errorf("first delay = %v, want 5s", w.d)
	}
	cancel()
	if running := s.Wait(t.Context()); running != nil {
		t.Errorf("Wait = %v, want nil", running)
	}
}

func TestStatusesAndJobFinishedEvents(t *testing.T) {
	cases := []struct {
		name       string
		timeout    time.Duration
		run        func(context.Context) error
		wantStatus scheduler.Status
		wantError  string
		wantLevel  slog.Level
	}{
		{
			name: "ok", run: func(context.Context) error { return nil },
			wantStatus: scheduler.StatusOK, wantLevel: slog.LevelInfo,
		},
		{
			name: "failed", run: func(context.Context) error {
				return errors.New("GET https://api.congress.gov/v3/bill?api_key=hunter2&format=json: 500")
			},
			wantStatus: scheduler.StatusFailed, wantLevel: slog.LevelError,
			wantError: "GET https://api.congress.gov/v3/bill?api_key=[redacted]&format=json: 500",
		},
		{
			name: "unavailable", run: func(context.Context) error {
				return fmt.Errorf("%w: uscode: download page is a maintenance page", obs.ErrUnavailable)
			},
			wantStatus: scheduler.StatusUnavailable, wantLevel: slog.LevelWarn,
			wantError: "upstream source unavailable: uscode: download page is a maintenance page",
		},
		{
			name: "timeout", timeout: time.Millisecond,
			run: func(ctx context.Context) error {
				<-ctx.Done()
				return ctx.Err()
			},
			wantStatus: scheduler.StatusTimeout, wantLevel: slog.LevelError,
			wantError: context.DeadlineExceeded.Error(),
		},
		{
			// A job that swallows its deadline didn't finish its work.
			name: "timeout returning nil", timeout: time.Millisecond,
			run: func(ctx context.Context) error {
				<-ctx.Done()
				return nil
			},
			wantStatus: scheduler.StatusTimeout, wantLevel: slog.LevelError,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			h := newHarness(0, scheduler.Job{Name: "sync-votes", Interval: time.Hour, Timeout: tc.timeout, Run: tc.run})
			h.start(t)
			h.clock.fire(h.clock.nextWait(t))
			h.clock.nextWait(t) // the run is over once the loop waits again

			got := h.finished(t)
			if got[string(names.JobNameKey)] != "sync-votes" ||
				got[string(names.JobOutcomeKey)] != string(tc.wantStatus) ||
				got["level"] != tc.wantLevel {
				t.Errorf("finished = %v, want job sync-votes, outcome %s, level %v",
					got, tc.wantStatus, tc.wantLevel)
			}
			if _, ok := got[string(names.JobDurationKey)].(float64); !ok {
				t.Errorf("duration = %#v, want a float64", got[string(names.JobDurationKey)])
			}
			if errText, _ := got["error"].(string); errText != tc.wantError {
				t.Errorf("error = %q, want %q", errText, tc.wantError)
			}
			if st := h.s.Snapshot()[0]; st.LastStatus != tc.wantStatus {
				t.Errorf("snapshot status = %q, want %q", st.LastStatus, tc.wantStatus)
			}
		})
	}
}

func TestSnapshotTimesUseTheClock(t *testing.T) {
	var h *harness
	h = newHarness(0, scheduler.Job{
		Name: "sync-texts", Interval: time.Hour,
		Run: func(context.Context) error {
			h.clock.mu.Lock()
			h.clock.t = h.clock.t.Add(90 * time.Second)
			h.clock.mu.Unlock()
			return nil
		},
	})
	h.start(t)
	h.clock.fire(h.clock.nextWait(t))
	h.clock.nextWait(t)
	h.finished(t)
	if st := h.s.Snapshot()[0]; st.LastFinish.Sub(st.LastStart) != 90*time.Second {
		t.Errorf("snapshot run = %v to %v, want 90s", st.LastStart, st.LastFinish)
	}
}

func TestNoOverlap(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{}, 1)
	var running atomic.Bool
	h := newHarness(0.5)
	h.s.Register(scheduler.Job{
		Name: "slow", Interval: time.Minute,
		Run: func(context.Context) error {
			running.Store(true)
			defer running.Store(false)
			started <- struct{}{}
			<-release
			return nil
		},
	})
	h.start(t)
	h.clock.fire(h.clock.nextWait(t))
	<-started

	// While the run goes on for longer than its interval, the loop doesn't wait for a next tick.
	select {
	case w := <-h.clock.waits:
		t.Fatalf("scheduler waited %v while the job was running", w.d)
	default:
	}
	close(release)
	h.clock.nextWait(t)
	if running.Load() {
		t.Error("the next wait began before the run returned")
	}
}

func TestShutdownCancelsRunningJobs(t *testing.T) {
	started := make(chan struct{})
	var runs atomic.Int32
	h := newHarness(0, scheduler.Job{
		Name: "sync-bills", Interval: time.Hour, Timeout: time.Hour,
		Run: func(ctx context.Context) error {
			runs.Add(1)
			close(started)
			<-ctx.Done()
			return ctx.Err()
		},
	})
	cancel := h.start(t)
	h.clock.fire(h.clock.nextWait(t))
	<-started
	if st := h.s.Snapshot()[0]; !st.Running || st.LastStart.IsZero() || !st.NextRun.IsZero() {
		t.Errorf("snapshot while running = %+v", st)
	}

	cancel()
	h.wait(t)
	got := h.finished(t)
	if got[string(names.JobOutcomeKey)] != string(scheduler.StatusCanceled) || got["level"] != slog.LevelWarn {
		t.Errorf("finished = %v, want outcome canceled at warn", got)
	}
	st := h.s.Snapshot()[0]
	if st.Running || st.LastStatus != scheduler.StatusCanceled || !st.NextRun.IsZero() {
		t.Errorf("snapshot after shutdown = %+v", st)
	}
	if runs.Load() != 1 {
		t.Errorf("runs = %d, want 1", runs.Load())
	}
}

func TestNoRunAfterShutdown(t *testing.T) {
	var runs atomic.Int32
	h := newHarness(0, scheduler.Job{
		Name: "sync-members", Interval: time.Hour,
		Run: func(context.Context) error { runs.Add(1); return nil },
	})
	cancel := h.start(t)
	w := h.clock.nextWait(t)
	cancel()
	h.clock.fire(w) // the timer and the cancellation are both ready
	h.wait(t)
	if runs.Load() != 0 {
		t.Errorf("runs = %d, want 0", runs.Load())
	}
}

func TestWaitReportsStragglers(t *testing.T) {
	release := make(chan struct{})
	started := make(chan struct{})
	h := newHarness(0,
		scheduler.Job{
			Name: "stuck", Interval: time.Hour,
			Run: func(context.Context) error {
				close(started)
				<-release // ignores cancellation
				return nil
			},
		},
		scheduler.Job{Name: "idle", Interval: time.Hour, Run: func(context.Context) error { return nil }},
	)
	ctx, cancel := context.WithCancel(t.Context())
	h.s.Start(ctx)
	// Fire both first runs (delay 0). Idle's run is over once it waits for its next one;
	// stuck never waits again.
	for fired, idleDone := 0, false; fired < 2 || !idleDone; {
		if w := h.clock.nextWait(t); w.d == 0 {
			h.clock.fire(w)
			fired++
		} else {
			idleDone = true
		}
	}
	<-started
	cancel()

	expired, stop := context.WithCancel(context.Background())
	stop()
	if got := h.s.Wait(expired); !slices.Equal(got, []string{"stuck"}) {
		t.Errorf("Wait = %v, want [stuck]", got)
	}
	close(release)
	h.wait(t)
}

func TestSnapshotOrderAndNextRun(t *testing.T) {
	h := newHarness(0.5,
		scheduler.Job{
			Name: "a", Interval: time.Hour, Timeout: 30 * time.Minute,
			Run: func(context.Context) error { return nil },
		},
		scheduler.Job{Name: "b", Interval: time.Hour, Run: func(context.Context) error { return nil }},
	)
	start := h.clock.now()
	got := h.s.Snapshot()
	if len(got) != 2 || got[0].Name != "a" || got[1].Name != "b" || !got[0].NextRun.IsZero() {
		t.Fatalf("snapshot before start = %+v", got)
	}
	if got[0].Timeout != 30*time.Minute || got[1].Timeout != 0 {
		t.Errorf("snapshot timeouts = %v, %v, want 30m and 0", got[0].Timeout, got[1].Timeout)
	}
	h.start(t)
	h.clock.nextWait(t)
	h.clock.nextWait(t)
	for _, st := range h.s.Snapshot() {
		if !st.NextRun.Equal(start.Add(time.Minute)) || st.Running || st.LastStatus != "" {
			t.Errorf("snapshot of %s = %+v, want next run at start+1m", st.Name, st)
		}
	}
}

// A job gets a deadline only when it has a Timeout.
func TestRunDeadlineFollowsTimeout(t *testing.T) {
	for _, timeout := range []time.Duration{0, time.Hour} {
		deadlines := make(chan bool, 1)
		h := newHarness(0, scheduler.Job{
			Name: "j", Interval: 2 * time.Hour, Timeout: timeout,
			Run: func(ctx context.Context) error {
				_, ok := ctx.Deadline()
				deadlines <- ok
				return nil
			},
		})
		h.start(t)
		h.clock.fire(h.clock.nextWait(t))
		if got := <-deadlines; got != (timeout > 0) {
			t.Errorf("timeout %v: run has a deadline = %v", timeout, got)
		}
	}
}

func TestRunIsAJobTrace(t *testing.T) {
	tel := obstest.New(t)
	h := newHarness(0, scheduler.Job{
		Name: "sync-bills", Interval: time.Hour, Timeout: time.Hour,
		Run: func(context.Context) error { return errors.New("list bills: 500") },
	})
	h.start(t)
	h.clock.fire(h.clock.nextWait(t))
	h.clock.nextWait(t)
	h.finished(t)

	spans := tel.Ended()
	if len(spans) != 1 || spans[0].Name() != "pipeline.job sync-bills" || spans[0].Status().Code != codes.Error {
		t.Fatalf("spans = %v, want one failed pipeline.job sync-bills", spans)
	}
	m, ok := tel.Metric(t, names.PipelineJobDurationName)
	if !ok {
		t.Fatalf("no %s metric", names.PipelineJobDurationName)
	}
	h2, ok := m.Data.(metricdata.Histogram[float64])
	if !ok || len(h2.DataPoints) != 1 {
		t.Fatalf("%s = %#v, want one point", names.PipelineJobDurationName, m.Data)
	}
	if v, _ := h2.DataPoints[0].Attributes.Value(names.JobOutcomeKey); v.AsString() != names.JobOutcomeFailed {
		t.Errorf("duration outcome = %q, want failed", v.AsString())
	}
	if _, found := tel.Metric(t, names.PipelineJobLastSuccessName); found {
		t.Error("last_success recorded for a failed run")
	}
}
