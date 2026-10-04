// Package scheduler runs the pipeline's recurring jobs: each job in its own serial loop, first
// shortly after start and then on a jittered interval, every run under a deadline
// (docs/design/80-pipeline-operability.md, "Scheduler semantics").
package scheduler

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"slices"
	"sync"
	"time"

	"github.com/justabill-org/justabill/obs"
	"github.com/justabill-org/justabill/pipeline/internal/redact"
)

const (
	// DefaultStartWindow is the default window for a job's first run: a random delay in
	// [0, DefaultStartWindow) after Start, so the jobs don't all hit upstream at once after a deploy.
	DefaultStartWindow = 2 * time.Minute
	// jitterFraction spreads later runs over interval ±10%.
	jitterFraction = 0.1
)

// Status is how a run ended.
type Status string

// The five ways a run ends. They are the justabill.job.outcome values of the job's telemetry.
const (
	StatusOK          Status = "ok"          // Run returned nil before its deadline.
	StatusFailed      Status = "failed"      // Run returned an error.
	StatusTimeout     Status = "timeout"     // The run passed Job.Timeout.
	StatusCanceled    Status = "canceled"    // The scheduler's context was cancelled (shutdown).
	StatusUnavailable Status = "unavailable" // Run returned an error wrapping obs.ErrUnavailable.
)

// Job is a recurring pipeline task.
type Job struct {
	Name string
	// Interval is the wait between the end of one run and the start of the next, ±10%.
	Interval time.Duration
	// RetryInterval, when set, replaces Interval after a run that didn't end ok (failed, timed
	// out or unavailable), so a job that waits long between runs retries a failure sooner.
	RetryInterval time.Duration
	// Timeout is the run's deadline. It should be shorter than Interval. Zero means none.
	Timeout time.Duration
	// Run does the work. It must return soon after ctx is done.
	Run func(ctx context.Context) error
}

// JobState is a job's entry in [Scheduler.Snapshot].
type JobState struct {
	Name       string
	Timeout    time.Duration // the job's Timeout, for the health server's stuck-job check
	Running    bool
	LastStart  time.Time // zero until the first run starts
	LastFinish time.Time // zero until the first run finishes
	LastStatus Status    // empty until the first run finishes
	NextRun    time.Time // zero while running and after the loop stopped
}

// Option configures a Scheduler.
type Option func(*Scheduler)

// WithClock replaces the wall clock: now reads the time and after waits, like [time.After].
// Tests use it to drive the scheduler without sleeping.
func WithClock(now func() time.Time, after func(time.Duration) <-chan time.Time) Option {
	return func(s *Scheduler) {
		s.now = now
		s.after = after
	}
}

// WithRand replaces the random source for the start delay and jitter. f returns a number in
// [0, 1) and must be safe for concurrent use.
func WithRand(f func() float64) Option {
	return func(s *Scheduler) { s.rand = f }
}

// WithStartWindow sets the window for each job's first run (default [DefaultStartWindow]).
func WithStartWindow(d time.Duration) Option {
	return func(s *Scheduler) { s.startWindow = d }
}

// Scheduler runs registered jobs until its context is cancelled.
type Scheduler struct {
	log         *slog.Logger
	now         func() time.Time
	after       func(time.Duration) <-chan time.Time
	rand        func() float64
	startWindow time.Duration

	jobs []Job
	wg   sync.WaitGroup

	mu     sync.Mutex
	states []JobState
}

// New returns a Scheduler with no jobs that logs to log.
func New(log *slog.Logger, opts ...Option) *Scheduler {
	s := &Scheduler{
		log:   log,
		now:   time.Now,
		after: time.After,
		// Spreading run times isn't security-relevant, so math/rand is fine.
		rand:        rand.Float64,
		startWindow: DefaultStartWindow,
	}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Register adds job to the jobs that [Scheduler.Start] runs. Call it before Start.
func (s *Scheduler) Register(job Job) {
	s.jobs = append(s.jobs, job)
	s.states = append(s.states, JobState{Name: job.Name, Timeout: job.Timeout})
}

// Start starts one loop per registered job and returns. Each loop waits a random delay within
// the start window, runs the job, then waits Interval ±10% after the run ends (RetryInterval
// after a run that didn't end ok, if set) and repeats, so a job never overlaps itself and a slow
// run doesn't queue another. Cancelling ctx stops new
// runs and cancels the running ones; [Scheduler.Wait] waits for them to return.
func (s *Scheduler) Start(ctx context.Context) {
	for i := range s.jobs {
		s.wg.Go(func() { s.loop(ctx, i) })
	}
}

// Wait blocks until every job loop has returned, or until ctx is done. It returns the names
// of the jobs still running at that point, which is empty when they all returned.
func (s *Scheduler) Wait(ctx context.Context) []string {
	done := make(chan struct{})
	go func() {
		s.wg.Wait()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
	}
	var running []string
	for _, st := range s.Snapshot() {
		if st.Running {
			running = append(running, st.Name)
		}
	}
	return running
}

// Snapshot returns every job's state, in registration order, for the health server's /status.
func (s *Scheduler) Snapshot() []JobState {
	s.mu.Lock()
	defer s.mu.Unlock()
	return slices.Clone(s.states)
}

func (s *Scheduler) loop(ctx context.Context, i int) {
	defer s.update(i, func(st *JobState) { st.NextRun = time.Time{} })

	delay := time.Duration(s.rand() * float64(s.startWindow))
	for {
		next := s.now().Add(delay)
		s.update(i, func(st *JobState) { st.NextRun = next })
		select {
		case <-ctx.Done():
			return
		case <-s.after(delay):
		}
		// Both cases can be ready at once, and select picks one at random.
		if ctx.Err() != nil {
			return
		}
		delay = s.jitter(s.jobs[i].next(s.runOnce(ctx, i)))
	}
}

// next returns the wait before the job's next run after a run that ended with status.
func (j Job) next(status Status) time.Duration {
	if status != StatusOK && j.RetryInterval > 0 {
		return j.RetryInterval
	}
	return j.Interval
}

// runOnce runs job i once and returns how the run ended.
func (s *Scheduler) runOnce(ctx context.Context, i int) Status {
	job := s.jobs[i]
	start := s.now()
	s.update(i, func(st *JobState) {
		st.Running = true
		st.LastStart = start
		st.NextRun = time.Time{}
	})

	// obs.Job makes the run a trace, records its metrics and logs justabill.pipeline.job.finished.
	runCtx, cancel := runContext(ctx, job.Timeout)
	outcome, _ := obs.Job(runCtx, s.log, job.Name, func(ctx context.Context) error {
		return redact.URLError(job.Run(ctx))
	})
	cancel()

	finish := s.now()
	s.update(i, func(st *JobState) {
		st.Running = false
		st.LastFinish = finish
		st.LastStatus = Status(outcome)
	})
	return Status(outcome)
}

// runContext returns the context for one run: with a deadline when timeout is set, without
// one otherwise.
func runContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	if timeout > 0 {
		return context.WithTimeout(ctx, timeout)
	}
	return context.WithCancel(ctx)
}

// jitter returns interval scaled by a random factor in [0.9, 1.1).
func (s *Scheduler) jitter(interval time.Duration) time.Duration {
	factor := 1 - jitterFraction + 2*jitterFraction*s.rand()
	return time.Duration(float64(interval) * factor)
}

func (s *Scheduler) update(i int, f func(*JobState)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	f(&s.states[i])
}
