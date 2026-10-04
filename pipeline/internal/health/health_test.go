package health_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/justabill-org/justabill/pipeline/internal/health"
	"github.com/justabill-org/justabill/pipeline/internal/scheduler"
)

// t0 is the fake clock's start. Tests read it and never change it.
var t0 = time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC) //nolint:gochecknoglobals // a fixed test time

type fakeJobs []scheduler.JobState

func (f fakeJobs) Snapshot() []scheduler.JobState { return slices.Clone(f) }

// fakeDB answers Ping with err and counts the calls.
type fakeDB struct {
	mu       sync.Mutex
	err      error
	calls    int
	deadline time.Duration // the ping context's time left, from the last call
	block    bool          // wait for the context instead of answering
}

func (f *fakeDB) Ping(ctx context.Context) error {
	f.mu.Lock()
	f.calls++
	if d, ok := ctx.Deadline(); ok {
		f.deadline = time.Until(d)
	}
	err, block := f.err, f.block
	f.mu.Unlock()
	if block {
		<-ctx.Done()
		return ctx.Err()
	}
	return err
}

func (f *fakeDB) set(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakeDB) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

type fakeLease health.Lease

func (f fakeLease) Lease() health.Lease { return health.Lease(f) }

// clock is a settable time source.
type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *clock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func newServer(jobs health.Jobs, db health.Pinger, opts ...health.Option) (*health.Server, *clock) {
	c := &clock{t: t0}
	opts = append([]health.Option{health.WithClock(c.now)}, opts...)
	return health.New(slog.New(slog.DiscardHandler), jobs, db, opts...), c
}

func get(t *testing.T, s *health.Server, path string) (int, map[string]any) {
	t.Helper()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, http.NoBody))
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("%s Content-Type = %q", path, ct)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s: decoding %q: %v", path, rec.Body.String(), err)
	}
	return rec.Code, body
}

func running(name string, timeout time.Duration, started time.Time) scheduler.JobState {
	return scheduler.JobState{Name: name, Timeout: timeout, Running: true, LastStart: started}
}

func TestHealthzOKWithoutJobs(t *testing.T) {
	s, _ := newServer(fakeJobs{}, &fakeDB{})
	if code, body := get(t, s, "/healthz"); code != http.StatusOK || body["status"] != "ok" {
		t.Errorf("/healthz = %d %v, want 200 ok", code, body)
	}
}

func TestHealthzStuckJob(t *testing.T) {
	cases := []struct {
		name string
		job  scheduler.JobState
		want int
	}{
		{"idle", scheduler.JobState{Name: "j", Timeout: time.Hour, LastStart: t0.Add(-5 * time.Hour)}, http.StatusOK},
		{"within timeout", running("j", time.Hour, t0.Add(-59*time.Minute)), http.StatusOK},
		{"within grace", running("j", time.Hour, t0.Add(-time.Hour-health.StuckGrace)), http.StatusOK},
		{"past grace", running("j", time.Hour, t0.Add(-time.Hour-health.StuckGrace-time.Second)), 503},
		{"no timeout", running("j", 0, t0.Add(-48*time.Hour)), http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := &fakeDB{err: errors.New("spanner down")}
			s, _ := newServer(fakeJobs{tc.job}, db)
			code, body := get(t, s, "/healthz")
			if code != tc.want {
				t.Errorf("/healthz = %d %v, want %d", code, body, tc.want)
			}
			if code == http.StatusServiceUnavailable {
				if stuck, _ := body["stuck_jobs"].([]any); len(stuck) != 1 || stuck[0] != "j" {
					t.Errorf("stuck_jobs = %v, want [j]", body["stuck_jobs"])
				}
			}
			if db.count() != 0 {
				t.Errorf("liveness pinged the database %d times", db.count())
			}
		})
	}
}

// A job that's stuck now becomes stuck as time passes, without a new snapshot.
func TestHealthzUsesTheClock(t *testing.T) {
	s, c := newServer(fakeJobs{running("sync-bills", 3*time.Hour, t0)}, &fakeDB{})
	if code, _ := get(t, s, "/healthz"); code != http.StatusOK {
		t.Fatalf("/healthz at start = %d", code)
	}
	c.advance(3*time.Hour + health.StuckGrace + time.Minute)
	if code, _ := get(t, s, "/healthz"); code != http.StatusServiceUnavailable {
		t.Errorf("/healthz after timeout + grace = %d, want 503", code)
	}
}

func TestReadyzPingsWithTimeoutAndCaches(t *testing.T) {
	db := &fakeDB{}
	s, c := newServer(fakeJobs{}, db)
	if code, body := get(t, s, "/readyz"); code != http.StatusOK || body["status"] != "ok" {
		t.Fatalf("/readyz = %d %v, want 200 ok", code, body)
	}
	if db.deadline <= 0 || db.deadline > health.PingTimeout {
		t.Errorf("ping deadline %v, want within %v", db.deadline, health.PingTimeout)
	}

	db.set(errors.New("unavailable"))
	c.advance(health.ReadyTTL - time.Second)
	if code, _ := get(t, s, "/readyz"); code != http.StatusOK || db.count() != 1 {
		t.Errorf("/readyz within TTL = %d after %d pings, want the cached 200 after 1", code, db.count())
	}

	c.advance(time.Second)
	if code, body := get(t, s, "/readyz"); code != http.StatusServiceUnavailable ||
		body["status"] != "database_unreachable" || db.count() != 2 {
		t.Errorf("/readyz after TTL = %d %v after %d pings, want 503 after 2", code, body, db.count())
	}

	db.set(nil)
	c.advance(health.ReadyTTL)
	if code, _ := get(t, s, "/readyz"); code != http.StatusOK {
		t.Errorf("/readyz after recovery = %d, want 200", code)
	}
}

func TestReadyzSlowDatabase(t *testing.T) {
	db := &fakeDB{block: true}
	s, _ := newServer(fakeJobs{}, db)
	start := time.Now()
	if code, _ := get(t, s, "/readyz"); code != http.StatusServiceUnavailable {
		t.Errorf("/readyz with a hung database = %d, want 503", code)
	}
	if elapsed := time.Since(start); elapsed > health.PingTimeout+time.Second {
		t.Errorf("/readyz took %v, want about %v", elapsed, health.PingTimeout)
	}
}

// The ping outlives a probe that hangs up, so a cancelled request doesn't cache a failure.
func TestReadyzIgnoresRequestCancellation(t *testing.T) {
	s, _ := newServer(fakeJobs{}, &fakeDB{})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(ctx, http.MethodGet, "/readyz", http.NoBody))
	if rec.Code != http.StatusOK {
		t.Errorf("/readyz with a cancelled request = %d, want 200", rec.Code)
	}
}

func TestDrain(t *testing.T) {
	db := &fakeDB{}
	s, _ := newServer(fakeJobs{}, db)
	s.Drain()
	if code, body := get(t, s, "/readyz"); code != http.StatusServiceUnavailable || body["status"] != "shutting_down" {
		t.Errorf("/readyz while draining = %d %v, want 503 shutting_down", code, body)
	}
	if code, _ := get(t, s, "/healthz"); code != http.StatusOK {
		t.Errorf("/healthz while draining = %d, want 200", code)
	}
	if _, body := get(t, s, "/status"); body["shutting_down"] != true {
		t.Errorf("/status shutting_down = %v, want true", body["shutting_down"])
	}
	if db.count() != 0 {
		t.Errorf("a draining /readyz pinged the database")
	}
}

func TestStatusWithoutLease(t *testing.T) {
	jobs := fakeJobs{
		{
			Name: "sync-bills", Timeout: 3 * time.Hour, LastStatus: scheduler.StatusFailed,
			LastStart: t0.Add(-2 * time.Hour), LastFinish: t0.Add(-time.Hour), NextRun: t0.Add(3 * time.Hour),
		},
		running("sync-votes", 2*time.Hour, t0.Add(-3*time.Hour)),
		{Name: "sync-texts", Timeout: 90 * time.Minute, NextRun: t0.Add(time.Minute)},
	}
	s, _ := newServer(jobs, &fakeDB{})
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/status", http.NoBody))
	if rec.Code != http.StatusOK {
		t.Fatalf("/status = %d", rec.Code)
	}
	want := `{"role":"leader","lease":null,"shutting_down":false,"jobs":[` +
		`{"name":"sync-bills","running":false,"timeout_seconds":10800,"last_status":"failed",` +
		`"last_start":"2026-10-04T10:00:00Z","last_finish":"2026-10-04T11:00:00Z",` +
		`"next_run":"2026-10-04T15:00:00Z"},` +
		`{"name":"sync-votes","running":true,"stuck":true,"timeout_seconds":7200,` +
		`"last_start":"2026-10-04T09:00:00Z"},` +
		`{"name":"sync-texts","running":false,"timeout_seconds":5400,"next_run":"2026-10-04T12:01:00Z"}]}`
	if got := strings.TrimSpace(rec.Body.String()); got != want {
		t.Errorf("/status =\n%s\nwant\n%s", got, want)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("Cache-Control = %q, want no-store", cc)
	}
}

// A standby replica (#260) is healthy and ready, and /status names the holder.
func TestStandbyIsHealthyAndReady(t *testing.T) {
	lease := fakeLease{Role: health.RoleStandby, Holder: "pipeline-7d9f-abc12345", ExpiresAt: t0.Add(time.Minute)}
	s, _ := newServer(fakeJobs{{Name: "sync-bills", Timeout: 3 * time.Hour}}, &fakeDB{}, health.WithLease(lease))
	if code, _ := get(t, s, "/healthz"); code != http.StatusOK {
		t.Errorf("standby /healthz = %d, want 200", code)
	}
	if code, _ := get(t, s, "/readyz"); code != http.StatusOK {
		t.Errorf("standby /readyz = %d, want 200", code)
	}
	_, body := get(t, s, "/status")
	l, _ := body["lease"].(map[string]any)
	if body["role"] != health.RoleStandby || l["holder"] != "pipeline-7d9f-abc12345" ||
		l["expires_at"] != "2026-10-04T12:01:00Z" {
		t.Errorf("/status = %v, want the standby role and the holder's lease", body)
	}
}

func TestOnlyGET(t *testing.T) {
	s, _ := newServer(fakeJobs{}, &fakeDB{})
	for _, path := range []string{"/healthz", "/readyz", "/status"} {
		rec := httptest.NewRecorder()
		s.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodPost, path, http.NoBody))
		if rec.Code != http.StatusMethodNotAllowed {
			t.Errorf("POST %s = %d, want 405", path, rec.Code)
		}
	}
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/", http.NoBody))
	if rec.Code != http.StatusNotFound {
		t.Errorf("GET / = %d, want 404", rec.Code)
	}
}

func TestListenAndShutdown(t *testing.T) {
	s, _ := newServer(fakeJobs{}, &fakeDB{})
	addr, err := s.Listen("127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen: %v", err)
	}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "http://"+addr.String()+"/healthz", http.NoBody)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /healthz = %d, want 200", resp.StatusCode)
	}

	// The port is taken while the server runs.
	other, _ := newServer(fakeJobs{}, &fakeDB{})
	if _, listenErr := other.Listen(addr.String()); listenErr == nil {
		t.Error("a second Listen on the same address succeeded")
	}

	if err = s.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}
	if after, doErr := http.DefaultClient.Do(req); doErr == nil {
		_ = after.Body.Close()
		t.Error("the server still answers after Shutdown")
	}
}

// Shutdown closes the listening socket even when the Serve goroutine hasn't started yet (#513):
// [http.Server.Shutdown] knows only the listeners Serve has registered, so without the fix the
// kernel kept accepting connections until the goroutine ran. Each round races Shutdown against
// that goroutine's start.
func TestShutdownRightAfterListenClosesTheSocket(t *testing.T) {
	const rounds = 200
	var d net.Dialer
	for range rounds {
		s, _ := newServer(fakeJobs{}, &fakeDB{})
		addr, err := s.Listen("127.0.0.1:0")
		if err != nil {
			t.Fatalf("Listen: %v", err)
		}
		if err = s.Shutdown(t.Context()); err != nil {
			t.Fatalf("Shutdown: %v", err)
		}
		if conn, dialErr := d.DialContext(t.Context(), "tcp", addr.String()); dialErr == nil {
			_ = conn.Close()
			t.Fatal("the socket still accepts connections after Shutdown returned")
		}
	}
}

func TestShutdownWithoutListen(t *testing.T) {
	s, _ := newServer(fakeJobs{}, &fakeDB{})
	if err := s.Shutdown(t.Context()); err != nil {
		t.Errorf("Shutdown before Listen = %v, want nil", err)
	}
}
