package main

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/obs"
	"github.com/justabill-org/justabill/pipeline/internal/health"
	"github.com/justabill-org/justabill/pipeline/internal/leader"
	"github.com/justabill-org/justabill/pipeline/internal/scheduler"
	"github.com/justabill-org/justabill/pipeline/internal/uscode"
)

func TestSyncJobsTable(t *testing.T) {
	jobs := syncJobs(nil, newCongressTracker(&fakeCongresses{}, 0, slog.New(slog.DiscardHandler)), nil)
	want := map[string][2]time.Duration{
		"sync-bills":         {4 * time.Hour, 3 * time.Hour},
		"sync-members":       {24 * time.Hour, time.Hour},
		"sync-votes":         {6 * time.Hour, 2 * time.Hour},
		"sync-texts":         {2 * time.Hour, 90 * time.Minute},
		"sync-summaries":     {30 * time.Minute, 25 * time.Minute},
		"sync-law-changes":   {time.Hour, 50 * time.Minute},
		"sync-govinfo":       {30 * time.Minute, 25 * time.Minute},
		"load-uscode":        {24 * time.Hour, 2 * time.Hour},
		"sync-gao":           {24 * time.Hour, 2 * time.Hour},
		"sync-crs-summaries": {6 * time.Hour, 30 * time.Minute},
		"sync-cra-rules":     {6 * time.Hour, 30 * time.Minute},
	}
	if len(jobs) != len(want) {
		t.Fatalf("got %d jobs, want %d", len(jobs), len(want))
	}
	for _, j := range jobs {
		w, ok := want[j.Name]
		if !ok {
			t.Errorf("unexpected job %q", j.Name)
			continue
		}
		if j.Interval != w[0] || j.Timeout != w[1] {
			t.Errorf("%s: interval %v timeout %v, want %v and %v", j.Name, j.Interval, j.Timeout, w[0], w[1])
		}
		if j.Timeout >= j.Interval {
			t.Errorf("%s: timeout %v isn't shorter than interval %v", j.Name, j.Timeout, j.Interval)
		}
		if j.Run == nil {
			t.Errorf("%s: no Run", j.Name)
		}
		// Only load-uscode, which runs daily, retries sooner (#866).
		if wantRetry := map[string]time.Duration{"load-uscode": time.Hour}[j.Name]; j.RetryInterval != wantRetry {
			t.Errorf("%s: retry interval %v, want %v", j.Name, j.RetryInterval, wantRetry)
		}
	}
}

// PIPELINE_CRA_RULES=false leaves sync-cra-rules out; unset or true keeps it; a non-boolean is an error.
func TestWithCRARules(t *testing.T) {
	jobs := syncJobs(nil, newCongressTracker(&fakeCongresses{}, 0, slog.New(slog.DiscardHandler)), nil)
	has := func(js []scheduler.Job) bool {
		return slices.ContainsFunc(js, func(j scheduler.Job) bool { return j.Name == "sync-cra-rules" })
	}
	for value, want := range map[string]bool{"": true, "true": true, "false": false, "0": false} {
		got, err := withCRARules(jobs, value)
		if err != nil {
			t.Fatalf("withCRARules(%q): %v", value, err)
		}
		if has(got) != want {
			t.Errorf("withCRARules(%q) has sync-cra-rules = %v, want %v", value, has(got), want)
		}
		if !want && len(got) != len(jobs)-1 {
			t.Errorf("withCRARules(%q) = %d jobs, want %d", value, len(got), len(jobs)-1)
		}
	}
	if !has(jobs) {
		t.Error("withCRARules changed the table it was given")
	}
	if _, err := withCRARules(jobs, "off"); err == nil || !strings.Contains(err.Error(), "PIPELINE_CRA_RULES") {
		t.Errorf("withCRARules(off) = %v, want an error naming PIPELINE_CRA_RULES", err)
	}
}

// The load-uscode job runs the loader's scheduled run: a failed download page is a failure, and a
// maintenance page is the site being unavailable.
func TestLoadUSCodeJobRunsTheLoader(t *testing.T) {
	cases := []struct {
		name            string
		handler         http.HandlerFunc
		want            error
		wantUnavailable bool
	}{
		{
			name:    "page fails",
			handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusServiceUnavailable) },
			want:    uscode.ErrHTTPStatus,
		},
		{
			name: "maintenance page",
			handler: func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte("<html><head><title>Under Maintenance</title></head></html>"))
			},
			want:            uscode.ErrSiteUnavailable,
			wantUnavailable: true,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			loader := uscode.NewLoader(nil, slog.New(slog.DiscardHandler), uscode.WithPageURL(srv.URL))
			tr := newCongressTracker(&fakeCongresses{}, 0, slog.New(slog.DiscardHandler))
			for _, j := range syncJobs(nil, tr, loader) {
				if j.Name != "load-uscode" {
					continue
				}
				err := j.Run(t.Context())
				if !errors.Is(err, tc.want) || errors.Is(err, obs.ErrUnavailable) != tc.wantUnavailable {
					t.Errorf("Run = %v, want %v (unavailable %v)", err, tc.want, tc.wantUnavailable)
				}
				return
			}
			t.Error("no load-uscode job")
		})
	}
}

type okPinger struct{}

func (okPinger) Ping(context.Context) error { return nil }

// freeLease is a lease table nobody else uses.
type freeLease struct{ released chan string }

func (f freeLease) AcquireLease(
	_ context.Context, name, holder string, ttl time.Duration,
) (repository.Lease, bool, error) {
	return repository.Lease{Name: name, Holder: holder, ExpiresAt: time.Now().Add(ttl)}, true, nil
}

func (f freeLease) ReleaseLease(_ context.Context, _, holder string) error {
	f.released <- holder
	return nil
}

// serve runs the shutdown sequence once its context is done: the process turns unready, the
// lease is released and the health server stops.
func TestServeShutdownSequence(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	s := scheduler.New(logger, scheduler.WithStartWindow(time.Millisecond))
	store := freeLease{released: make(chan string, 1)}
	el := leader.New(store, "pipeline-0-0badc0de", logger)
	hs := health.New(logger, s, okPinger{}, health.WithLease(el))
	addr, err := hs.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	jobStarted := make(chan struct{})
	s.Register(scheduler.Job{Name: "probe", Interval: time.Hour, Run: func(ctx context.Context) error {
		close(jobStarted)
		cancel() // shut down while the job runs
		<-ctx.Done()
		return ctx.Err()
	}})
	done := make(chan struct{})
	go func() {
		defer close(done)
		serve(ctx, logger, s, el, hs)
	}()
	waitFor(t, jobStarted, "the leader's job to start")
	waitFor(t, done, "serve to return")

	select {
	case h := <-store.released:
		if h != el.Holder() {
			t.Errorf("released %q, want %q", h, el.Holder())
		}
	default:
		t.Error("serve returned without releasing the lease")
	}
	if st := s.Snapshot(); st[0].Running || st[0].LastStatus != scheduler.StatusCanceled {
		t.Errorf("job after shutdown = %+v, want canceled and not running", st[0])
	}
	rec := httptest.NewRecorder()
	hs.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", http.NoBody))
	if rec.Code != http.StatusServiceUnavailable {
		t.Errorf("/readyz after shutdown = %d, want 503", rec.Code)
	}
	var d net.Dialer
	if conn, dialErr := d.DialContext(t.Context(), "tcp", addr.String()); dialErr == nil {
		_ = conn.Close()
		t.Error("the health server still accepts connections after serve returned")
	}
}

func waitFor(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(10 * time.Second):
		t.Fatalf("timed out waiting for %s", what)
	}
}

// After a lost lease, leadJobs returns only once every job has returned, however long that takes,
// so the elector can't start them again while one still runs.
func TestLeadJobs_LossWaitsForEveryJob(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	s := scheduler.New(logger, scheduler.WithStartWindow(time.Millisecond))
	started, finish := make(chan struct{}), make(chan struct{})
	s.Register(scheduler.Job{Name: "slow", Interval: time.Hour, Run: func(ctx context.Context) error {
		close(started)
		<-ctx.Done()
		<-finish // ignores cancellation until the test lets it go
		return ctx.Err()
	}})

	leaderCtx, lose := context.WithCancel(t.Context())
	returned := make(chan struct{})
	go func() {
		defer close(returned)
		leadJobs(t.Context(), logger, s)(leaderCtx)
	}()
	waitFor(t, started, "the job to start")
	lose()
	select {
	case <-returned:
		t.Fatal("leadJobs returned while a job still ran")
	case <-time.After(100 * time.Millisecond):
	}
	close(finish)
	waitFor(t, returned, "leadJobs to return after the job did")
}

func TestAggregateJob(t *testing.T) {
	logger := slog.New(slog.DiscardHandler)
	job, err := aggregateJob(nil, func(string) string { return "" }, logger)
	if err != nil {
		t.Fatalf("aggregateJob: %v", err)
	}
	if job.Name != "aggregate-votes" || job.Interval != time.Hour || job.Timeout != 50*time.Minute || job.Run == nil {
		t.Errorf("job = %s every %v, timeout %v; want aggregate-votes every 1h, timeout 50m", job.Name,
			job.Interval, job.Timeout)
	}

	bad := func(key string) string {
		if key == "agg_min_cell_votes" {
			return "few"
		}
		return ""
	}
	if _, err = aggregateJob(nil, bad, logger); err == nil {
		t.Error("aggregateJob with AGG_MIN_CELL_VOTES=few: no error, want one")
	}
}
