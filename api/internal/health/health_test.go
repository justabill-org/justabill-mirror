package health_test

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/justabill-org/justabill/api/internal/health"
)

// fakePinger answers Ping with err and counts the calls. With hang set, Ping ignores its
// context and waits until the test ends.
type fakePinger struct {
	mu    sync.Mutex
	err   error
	calls int
	hang  chan struct{}
}

func (f *fakePinger) Ping(context.Context) error {
	f.mu.Lock()
	f.calls++
	err, hang := f.err, f.hang
	f.mu.Unlock()
	if hang != nil {
		<-hang
	}
	return err
}

func (f *fakePinger) set(err error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.err = err
}

func (f *fakePinger) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

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

// probeBody mirrors the probes' JSON.
type probeBody struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks"`
}

func get(t *testing.T, h http.Handler, path string) (int, probeBody, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, path, http.NoBody))
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("%s Content-Type = %q", path, ct)
	}
	if cc := rec.Header().Get("Cache-Control"); cc != "no-store" {
		t.Errorf("%s Cache-Control = %q, want no-store", path, cc)
	}
	var body probeBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("%s: decoding %q: %v", path, rec.Body.String(), err)
	}
	return rec.Code, body, rec.Body.String()
}

// notFound stands in for the API router behind the probes.
var notFound = http.NotFoundHandler() //nolint:gochecknoglobals // a stateless test handler

func newProbes(spanner health.Pinger, opts ...health.Option) (http.Handler, *health.Probes, *clock) {
	c := &clock{t: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)}
	opts = append([]health.Option{health.WithClock(c.now)}, opts...)
	p := health.New(slog.New(slog.DiscardHandler), spanner, opts...)
	return p.Wrap(notFound), p, c
}

func TestHealthzWithoutSpanner(t *testing.T) {
	h, _, _ := newProbes(nil)

	if code, body, _ := get(t, h, "/healthz"); code != http.StatusOK || body.Status != "ok" {
		t.Errorf("/healthz = %d %+v, want 200 ok", code, body)
	}
	if code, body, _ := get(t, h, "/readyz"); code != http.StatusServiceUnavailable ||
		body.Checks["spanner"] != "fail" {
		t.Errorf("/readyz = %d %+v, want 503 with spanner fail", code, body)
	}
}

func TestReadyzSpannerFails(t *testing.T) {
	db := &fakePinger{err: errors.New("rpc error: code = Unavailable desc = secret detail")}
	h, _, _ := newProbes(db, health.WithCache(&fakePinger{}))

	code, body, raw := get(t, h, "/readyz")
	if code != http.StatusServiceUnavailable || body.Status != "fail" {
		t.Errorf("/readyz = %d %+v, want 503 fail", code, body)
	}
	want := map[string]string{"spanner": "fail", "cache": "ok"}
	if len(body.Checks) != len(want) || body.Checks["spanner"] != want["spanner"] || body.Checks["cache"] != "ok" {
		t.Errorf("checks = %v, want %v", body.Checks, want)
	}
	if strings.Contains(raw, "secret") || strings.Contains(raw, "rpc") {
		t.Errorf("body %q carries error text", raw)
	}
}

func TestReadyzRedisDownIsStillReady(t *testing.T) {
	h, _, _ := newProbes(&fakePinger{}, health.WithCache(&fakePinger{err: errors.New("connection refused")}))

	code, body, _ := get(t, h, "/readyz")
	if code != http.StatusOK || body.Status != "ok" {
		t.Errorf("/readyz = %d %+v, want 200 ok", code, body)
	}
	if body.Checks["spanner"] != "ok" || body.Checks["cache"] != "fail" {
		t.Errorf("checks = %v, want spanner ok and cache fail", body.Checks)
	}
}

func TestReadyzWithoutCacheDoesNotListIt(t *testing.T) {
	h, _, _ := newProbes(&fakePinger{})

	_, body, _ := get(t, h, "/readyz")
	if _, listed := body.Checks["cache"]; listed || body.Checks["spanner"] != "ok" {
		t.Errorf("checks = %v, want only spanner ok", body.Checks)
	}
}

func TestReadyzHungSpannerTimesOut(t *testing.T) {
	hang := make(chan struct{})
	t.Cleanup(func() { close(hang) })
	h, _, _ := newProbes(&fakePinger{hang: hang}, health.WithCache(&fakePinger{hang: hang}))

	start := time.Now()
	code, body, _ := get(t, h, "/readyz")
	took := time.Since(start)
	if code != http.StatusServiceUnavailable || body.Checks["spanner"] != "fail" || body.Checks["cache"] != "fail" {
		t.Errorf("/readyz = %d %+v, want 503 with both checks failing", code, body)
	}
	if took < health.CheckTimeout-100*time.Millisecond || took > health.CheckTimeout+time.Second {
		t.Errorf("/readyz took %v, want about %v", took, health.CheckTimeout)
	}
}

func TestReadyzCachesTheResult(t *testing.T) {
	db := &fakePinger{}
	h, _, c := newProbes(db)

	for range 10 {
		if code, _, _ := get(t, h, "/readyz"); code != http.StatusOK {
			t.Fatalf("/readyz = %d, want 200", code)
		}
		c.advance(health.ReadyTTL / 10)
	}
	if n := db.count(); n != 1 {
		t.Errorf("10 probes within the TTL pinged Spanner %d times, want 1", n)
	}

	// Past the TTL, the next probe checks again and sees the failure.
	db.set(errors.New("down"))
	if code, _, _ := get(t, h, "/readyz"); code != http.StatusServiceUnavailable || db.count() != 2 {
		t.Errorf("/readyz after the TTL = %d after %d pings, want 503 after 2", code, db.count())
	}
}

func TestReadyzConcurrentProbesShareOneCheck(t *testing.T) {
	db := &fakePinger{}
	h, _, _ := newProbes(db)

	var wg sync.WaitGroup
	for range 10 {
		wg.Go(func() {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), http.MethodGet, "/readyz", http.NoBody))
			if rec.Code != http.StatusOK {
				t.Errorf("/readyz = %d, want 200", rec.Code)
			}
		})
	}
	wg.Wait()
	if n := db.count(); n != 1 {
		t.Errorf("10 concurrent probes pinged Spanner %d times, want 1", n)
	}
}

func TestDrainFailsReadinessOnly(t *testing.T) {
	db := &fakePinger{}
	h, p, _ := newProbes(db)

	p.Drain()
	for _, path := range []string{"/readyz", "/health"} {
		code, body, _ := get(t, h, path)
		if code != http.StatusServiceUnavailable || body.Status != "fail" || body.Checks["draining"] != "fail" {
			t.Errorf("%s while draining = %d %+v, want 503 with draining fail", path, code, body)
		}
	}
	if code, _, _ := get(t, h, "/healthz"); code != http.StatusOK {
		t.Errorf("/healthz while draining = %d, want 200", code)
	}
	if n := db.count(); n != 0 {
		t.Errorf("draining probes pinged Spanner %d times, want 0", n)
	}
}

func TestHealthIsAnAliasOfReadyz(t *testing.T) {
	h, _, _ := newProbes(&fakePinger{err: errors.New("down")})

	if code, body, _ := get(t, h, "/health"); code != http.StatusServiceUnavailable ||
		body.Checks["spanner"] != "fail" {
		t.Errorf("/health = %d %+v, want /readyz's 503", code, body)
	}
}

func TestWrapPassesOtherRequestsThrough(t *testing.T) {
	h, _, _ := newProbes(&fakePinger{})

	cases := []struct {
		method, path string
		want         int
	}{
		{http.MethodHead, "/readyz", http.StatusOK},
		{http.MethodHead, "/healthz", http.StatusOK},
		{http.MethodPost, "/healthz", http.StatusNotFound},
		{http.MethodDelete, "/readyz", http.StatusNotFound},
		{http.MethodGet, "/readyz/", http.StatusNotFound},
		{http.MethodGet, "/api/v1/bills", http.StatusNotFound},
	}
	for _, tc := range cases {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequestWithContext(t.Context(), tc.method, tc.path, http.NoBody))
		if rec.Code != tc.want {
			t.Errorf("%s %s = %d, want %d", tc.method, tc.path, rec.Code, tc.want)
		}
	}
}

// recordingHandler keeps the messages of the records it handles.
type recordingHandler struct {
	mu   sync.Mutex
	msgs []string
}

func (r *recordingHandler) Enabled(context.Context, slog.Level) bool { return true }

func (r *recordingHandler) Handle(_ context.Context, rec slog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	check := ""
	rec.Attrs(func(a slog.Attr) bool {
		if a.Key == "check" {
			check = a.Value.String()
		}
		return true
	})
	r.msgs = append(r.msgs, rec.Message+": "+check)
	return nil
}

func (r *recordingHandler) WithAttrs([]slog.Attr) slog.Handler { return r }

func (r *recordingHandler) WithGroup(string) slog.Handler { return r }

func TestReadyzLogsChangesOnly(t *testing.T) {
	logs := &recordingHandler{}
	db := &fakePinger{err: errors.New("down")}
	c := &clock{t: time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)}
	h := health.New(slog.New(logs), db, health.WithClock(c.now), health.WithCache(&fakePinger{})).Wrap(notFound)

	for _, down := range []bool{true, true, false, false} {
		if !down {
			db.set(nil)
		}
		get(t, h, "/readyz")
		c.advance(health.ReadyTTL)
	}
	want := []string{"readiness check failing: spanner", "readiness check recovered: spanner"}
	if strings.Join(logs.msgs, "|") != strings.Join(want, "|") {
		t.Errorf("logs = %q, want %q", logs.msgs, want)
	}
}
