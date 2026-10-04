package upstream_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/justabill-org/justabill/pipeline/internal/apikey"
	"github.com/justabill-org/justabill/pipeline/internal/upstream"
)

func TestRetries5xxThenSucceeds(t *testing.T) {
	srv := newServer(t, statusThen(http.StatusServiceUnavailable, http.StatusServiceUnavailable))
	sl := &sleeps{}
	c := newClient(t, slog.New(slog.DiscardHandler), map[string]upstream.Host{srv.host(): host(fastBudget(t))},
		upstream.Hooks{Sleep: sl.sleep})

	body, err := get(t.Context(), c, srv.URL+"/v3/bill", nil)
	if err != nil || body != "ok" {
		t.Fatalf("got %q, %v; want ok", body, err)
	}
	if srv.count() != 3 {
		t.Errorf("attempts = %d, want 3", srv.count())
	}
	waits := sl.got()
	if len(waits) != 2 {
		t.Fatalf("waits = %v, want 2", waits)
	}
	for i, bound := range []time.Duration{2 * time.Second, 4 * time.Second} {
		if waits[i] < 0 || waits[i] > bound {
			t.Errorf("wait %d = %v, want within [0, %v]", i+1, waits[i], bound)
		}
	}
}

func TestGivesUpAfterFiveAttempts(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		w.WriteHeader(http.StatusBadGateway)
	})
	sl := &sleeps{}
	log, logs := newLogger()
	c := newClient(t, log, map[string]upstream.Host{srv.host(): host(fastBudget(t))},
		upstream.Hooks{Sleep: sl.sleep, Jitter: func(d time.Duration) time.Duration { return d }})

	_, err := get(t.Context(), c, srv.URL+"/v3/bill/119?offset=250&api_key=leak", nil)
	se, ok := errors.AsType[*upstream.StatusError](err)
	if !ok {
		t.Fatalf("got %v, want a StatusError", err)
	}
	want := upstream.StatusError{Host: srv.host(), Path: "/v3/bill/119", Status: http.StatusBadGateway, Attempts: 5}
	if *se != want {
		t.Errorf("got %+v, want %+v", *se, want)
	}
	if upstream.IsPermanent(err) {
		t.Error("a 502 is not permanent")
	}
	if srv.count() != 5 {
		t.Errorf("attempts = %d, want 5", srv.count())
	}
	wantWaits := []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 16 * time.Second}
	if got := sl.got(); !equalDurations(got, wantWaits) {
		t.Errorf("backoff caps = %v, want %v", got, wantWaits)
	}
	if strings.Contains(se.Error(), "?") || strings.Contains(logs.String(), "leak") ||
		strings.Contains(logs.String(), "offset") {
		t.Errorf("query string leaked: err %q, logs %s", se.Error(), logs.String())
	}
	if n := logs.count("upstream_error"); n != 1 {
		t.Errorf("upstream_error logged %d times, want once per request", n)
	}
	if n := logs.count("upstream_retry"); n != 4 {
		t.Errorf("upstream_retry logged %d times, want 4", n)
	}
}

func equalDurations(a, b []time.Duration) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestRetryAfterSeconds(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, _ *http.Request, hit int) {
		if hit == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = io.WriteString(w, "ok")
	})
	b := fastBudget(t)
	sl := &sleeps{}
	c := newClient(t, slog.New(slog.DiscardHandler), map[string]upstream.Host{srv.host(): host(b)},
		upstream.Hooks{Sleep: sl.sleep})

	start := time.Now()
	if _, err := get(t.Context(), c, srv.URL+"/", nil); err != nil {
		t.Fatal(err)
	}
	if got := sl.got(); !equalDurations(got, []time.Duration{time.Second}) {
		t.Errorf("waits = %v, want [1s]", got)
	}
	// A 429's Retry-After pauses the whole budget, so the retry also waited there.
	if elapsed := time.Since(start); elapsed < 900*time.Millisecond {
		t.Errorf("retry went out after %v, want the budget paused for about 1s", elapsed)
	}
}

func TestRetryAfterHTTPDate(t *testing.T) {
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	srv := newServer(t, func(w http.ResponseWriter, _ *http.Request, hit int) {
		if hit == 1 {
			w.Header().Set("Retry-After", now.Add(3*time.Second).Format(http.TimeFormat))
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		_, _ = io.WriteString(w, "ok")
	})
	sl := &sleeps{}
	c := newClient(t, slog.New(slog.DiscardHandler), map[string]upstream.Host{srv.host(): host(fastBudget(t))},
		upstream.Hooks{Sleep: sl.sleep, Now: func() time.Time { return now }})

	if _, err := get(t.Context(), c, srv.URL+"/", nil); err != nil {
		t.Fatal(err)
	}
	if got := sl.got(); !equalDurations(got, []time.Duration{3 * time.Second}) {
		t.Errorf("waits = %v, want [3s]", got)
	}
}

func TestRetryAfterTooLongFailsAtOnce(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, _ *http.Request, _ int) {
		w.Header().Set("Retry-After", "7200")
		w.WriteHeader(http.StatusTooManyRequests)
	})
	c := newClient(t, slog.New(slog.DiscardHandler), map[string]upstream.Host{srv.host(): host(fastBudget(t))},
		upstream.Hooks{})

	_, err := get(t.Context(), c, srv.URL+"/", nil)
	se, ok := errors.AsType[*upstream.StatusError](err)
	if !ok || se.Status != http.StatusTooManyRequests || se.Attempts != 1 {
		t.Fatalf("got %v, want a StatusError for one 429 attempt", err)
	}
	if srv.count() != 1 {
		t.Errorf("attempts = %d, want 1", srv.count())
	}
}

func TestRateLimitCooldownIsSharedByCallers(t *testing.T) {
	srv := newServer(t, statusThen(http.StatusTooManyRequests))
	b := fastBudget(t)
	upstream.SetCooldowns(b, 400*time.Millisecond, time.Second, time.Second)
	log, logs := newLogger()
	c := newClient(t, log, map[string]upstream.Host{srv.host(): host(b)},
		upstream.Hooks{Jitter: func(time.Duration) time.Duration { return 0 }})

	start := time.Now()
	errA := make(chan error, 1)
	go func() {
		_, err := get(context.Background(), c, srv.URL+"/a", nil)
		errA <- err
	}()
	for b.Quota().CooldownUntil.IsZero() {
		if time.Since(start) > 2*time.Second {
			t.Fatal("the 429 never started a cooldown")
		}
		time.Sleep(time.Millisecond)
	}

	// A second caller waits out the cooldown too; its context ends the wait.
	ctx, cancel := context.WithTimeout(t.Context(), 100*time.Millisecond)
	defer cancel()
	if _, err := get(ctx, c, srv.URL+"/b", nil); !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("second caller got %v, want its deadline", err)
	}
	if srv.count() != 1 {
		t.Errorf("a request went out during the cooldown (%d hits)", srv.count())
	}
	if q := b.Quota(); !q.CooldownUntil.After(time.Now()) {
		t.Errorf("no cooldown running: %+v", q)
	}

	if err := <-errA; err != nil {
		t.Fatalf("first caller: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 350*time.Millisecond {
		t.Errorf("retry went out after %v, before the 400ms cooldown ended", elapsed)
	}
	if n := logs.count("quota_cooldown"); n != 1 {
		t.Errorf("quota_cooldown logged %d times, want 1", n)
	}
}

func TestPermanentStatusIsNotRetried(t *testing.T) {
	srv := newServer(t, statusThen(http.StatusNotFound))
	c := newClient(t, slog.New(slog.DiscardHandler), map[string]upstream.Host{srv.host(): host(fastBudget(t))},
		upstream.Hooks{})

	_, err := get(t.Context(), c, srv.URL+"/v3/member/X000000?format=json", nil)
	if !upstream.IsPermanent(err) {
		t.Errorf("got %v, want a permanent error", err)
	}
	if srv.count() != 1 {
		t.Errorf("attempts = %d, want 1", srv.count())
	}
}

func TestIsPermanent(t *testing.T) {
	for status, want := range map[int]bool{
		http.StatusBadRequest: true, http.StatusUnauthorized: true, http.StatusForbidden: true,
		http.StatusNotFound: true, http.StatusGone: true, http.StatusTooManyRequests: false,
		http.StatusInternalServerError: false, http.StatusServiceUnavailable: false,
	} {
		err := &upstream.StatusError{Host: "h", Path: "/", Status: status, Attempts: 1}
		if got := upstream.IsPermanent(err); got != want {
			t.Errorf("IsPermanent(%d) = %v, want %v", status, got, want)
		}
	}
	if upstream.IsPermanent(context.DeadlineExceeded) {
		t.Error("a timeout is not permanent")
	}
}

func TestStalledBodyHitsAttemptDeadline(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		w.Header().Set("Content-Length", "100")
		_, _ = io.WriteString(w, "partial")
		w.(http.Flusher).Flush()
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	})
	h := host(fastBudget(t))
	h.AttemptTimeout = 100 * time.Millisecond
	sl := &sleeps{}
	c := newClient(t, slog.New(slog.DiscardHandler), map[string]upstream.Host{srv.host(): h},
		upstream.Hooks{Sleep: sl.sleep})

	start := time.Now()
	_, err := get(t.Context(), c, srv.URL+"/", nil)
	if err == nil {
		t.Fatal("a stalled body succeeded")
	}
	if elapsed := time.Since(start); elapsed > 3*time.Second {
		t.Errorf("took %v; the attempt deadline didn't cover the body", elapsed)
	}
	if srv.count() != 5 {
		t.Errorf("attempts = %d, want 5 (a stalled body is retried)", srv.count())
	}
}

func TestBudgetWaitDoesNotCountAgainstAttemptTimeout(t *testing.T) {
	srv := newServer(t, statusThen())
	h := host(newBudget(t, 4, 1, 0))
	h.AttemptTimeout = 150 * time.Millisecond
	c := newClient(t, slog.New(slog.DiscardHandler), map[string]upstream.Host{srv.host(): h}, upstream.Hooks{})

	start := time.Now()
	for i := range 3 {
		if _, err := get(t.Context(), c, srv.URL+"/", nil); err != nil {
			t.Fatalf("request %d: %v", i+1, err)
		}
	}
	if elapsed := time.Since(start); elapsed < 450*time.Millisecond {
		t.Errorf("3 requests at 4/s took %v, want at least 500ms of pacing", elapsed)
	}
}

func TestBodyCap(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		n, _ := strconv.Atoi(r.URL.Query().Get("n"))
		_, _ = w.Write(bytes.Repeat([]byte("x"), n))
	})
	h := host(fastBudget(t))
	h.MaxBodyBytes = 10
	c := newClient(t, slog.New(slog.DiscardHandler), map[string]upstream.Host{srv.host(): h}, upstream.Hooks{})

	if body, err := get(t.Context(), c, srv.URL+"/?n=10", nil); err != nil || len(body) != 10 {
		t.Errorf("10 bytes: got %d bytes, %v", len(body), err)
	}
	_, err := get(t.Context(), c, srv.URL+"/?n=11", nil)
	if !errors.Is(err, upstream.ErrBodyTooLarge) || !upstream.IsPermanent(err) {
		t.Errorf("11 bytes: got %v, want a permanent ErrBodyTooLarge", err)
	}
	if srv.count() != 2 {
		t.Errorf("hits = %d, want 2 (no retry of an oversized body)", srv.count())
	}
}

func TestAPIKeyGoesOnlyToItsHost(t *testing.T) {
	var otherKey atomic.Value
	other := newServer(t, func(_ http.ResponseWriter, r *http.Request, _ int) {
		otherKey.Store(r.Header.Get(apikey.Header))
	})
	var apiKey, apiQuery atomic.Value
	api := newServer(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		if r.URL.Path == "/redirect" {
			http.Redirect(w, r, other.URL+"/landing", http.StatusFound)
			return
		}
		apiKey.Store(r.Header.Get(apikey.Header))
		apiQuery.Store(r.URL.RawQuery)
	})
	keyed := host(fastBudget(t))
	keyed.APIKey = testKey
	c := newClient(t, slog.New(slog.DiscardHandler), map[string]upstream.Host{
		api.host():   keyed,
		other.host(): host(fastBudget(t)),
	}, upstream.Hooks{})

	if _, err := get(t.Context(), c, api.URL+"/v3/bill?format=json", nil); err != nil {
		t.Fatal(err)
	}
	if apiKey.Load() != testKey {
		t.Errorf("API host got key %q, want %q", apiKey.Load(), testKey)
	}
	if q, _ := apiQuery.Load().(string); strings.Contains(q, testKey) {
		t.Errorf("key in query %q", q)
	}

	callerKey := http.Header{apikey.Header: {"caller-set"}}
	if _, err := get(t.Context(), c, api.URL+"/redirect", callerKey); err != nil {
		t.Fatal(err)
	}
	if got := otherKey.Load(); got != "" {
		t.Errorf("redirect target got key %q", got)
	}
	otherKey.Store("unset")
	if _, err := get(t.Context(), c, other.URL+"/", callerKey); err != nil {
		t.Fatal(err)
	}
	if got := otherKey.Load(); got != "" {
		t.Errorf("unkeyed host got the caller's key %q", got)
	}
}

func TestUnknownHostIsRefused(t *testing.T) {
	undeclared := newServer(t, statusThen())
	declared := newServer(t, func(w http.ResponseWriter, r *http.Request, _ int) {
		http.Redirect(w, r, undeclared.URL+"/", http.StatusFound)
	})
	c := newClient(t, slog.New(slog.DiscardHandler),
		map[string]upstream.Host{declared.host(): host(fastBudget(t))}, upstream.Hooks{})

	for _, target := range []string{undeclared.URL + "/", declared.URL + "/"} {
		_, err := get(t.Context(), c, target, nil)
		if !errors.Is(err, upstream.ErrUnknownHost) || !upstream.IsPermanent(err) {
			t.Errorf("%s: got %v, want ErrUnknownHost", target, err)
		}
	}
	if undeclared.count() != 0 {
		t.Errorf("undeclared host got %d requests", undeclared.count())
	}
}

func TestKeyIsNeverSentOverPlainHTTP(t *testing.T) {
	keyed := host(fastBudget(t))
	keyed.APIKey = testKey
	c := newClient(t, slog.New(slog.DiscardHandler),
		map[string]upstream.Host{"api.example.test": keyed}, upstream.Hooks{})

	if _, err := get(t.Context(), c, "http://api.example.test/v3/bill", nil); !errors.Is(err, upstream.ErrInsecureKey) {
		t.Errorf("got %v, want ErrInsecureKey", err)
	}
}

func TestNetworkErrorIsRetried(t *testing.T) {
	srv := newServer(t, func(w http.ResponseWriter, _ *http.Request, hit int) {
		if hit == 1 {
			conn, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				_ = conn.Close()
			}
			return
		}
		_, _ = io.WriteString(w, "ok")
	})
	c := newClient(t, slog.New(slog.DiscardHandler), map[string]upstream.Host{srv.host(): host(fastBudget(t))},
		upstream.Hooks{Sleep: (&sleeps{}).sleep})

	if body, err := get(t.Context(), c, srv.URL+"/", nil); err != nil || body != "ok" {
		t.Fatalf("got %q, %v", body, err)
	}
	if srv.count() != 2 {
		t.Errorf("attempts = %d, want 2", srv.count())
	}
}

func TestPOSTIsRetriedOnlyWhenMarkedIdempotent(t *testing.T) {
	var (
		mu     sync.Mutex
		bodies []string
	)
	srv := newServer(t, func(w http.ResponseWriter, r *http.Request, hit int) {
		b, _ := io.ReadAll(r.Body)
		mu.Lock()
		bodies = append(bodies, string(b))
		mu.Unlock()
		if hit <= 2 {
			w.WriteHeader(http.StatusServiceUnavailable)
		}
	})
	c := newClient(t, slog.New(slog.DiscardHandler), map[string]upstream.Host{srv.host(): host(fastBudget(t))},
		upstream.Hooks{Sleep: (&sleeps{}).sleep})
	post := func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, srv.URL+"/search",
			strings.NewReader(`{"query":"x"}`))
		if err != nil {
			return err
		}
		resp, err := c.Do(req)
		if err == nil {
			_ = resp.Body.Close()
		}
		return err
	}

	if err := post(t.Context()); err == nil {
		t.Error("an unmarked POST was retried")
	}
	if err := post(upstream.WithIdempotent(t.Context())); err != nil {
		t.Errorf("idempotent POST: %v", err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 3 || bodies[1] != bodies[2] {
		t.Errorf("bodies = %q, want 3 with the retry resending the body", bodies)
	}
}

func TestCancelledRequestIsNotLogged(t *testing.T) {
	srv := newServer(t, statusThen())
	log, logs := newLogger()
	c := newClient(t, log, map[string]upstream.Host{srv.host(): host(fastBudget(t))}, upstream.Hooks{})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := get(ctx, c, srv.URL+"/", nil); !errors.Is(err, context.Canceled) {
		t.Errorf("got %v, want context.Canceled", err)
	}
	if logs.count("upstream_error") != 0 {
		t.Errorf("a cancelled request was logged as an upstream error: %s", logs.String())
	}
}

// closeRecorder is a request body that records whether it was closed.
type closeRecorder struct {
	io.Reader

	closed atomic.Bool
}

func (c *closeRecorder) Close() error {
	c.closed.Store(true)
	return nil
}

func TestBodyIsClosedWhenNoAttemptIsMade(t *testing.T) {
	srv := newServer(t, statusThen())
	c := newClient(t, slog.New(slog.DiscardHandler), map[string]upstream.Host{srv.host(): host(fastBudget(t))},
		upstream.Hooks{})
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	for name, target := range map[string]string{
		"budget wait cancelled": srv.URL + "/search",
		"undeclared host":       "https://undeclared.example.test/search",
	} {
		body := &closeRecorder{Reader: strings.NewReader(`{"query":"x"}`)}
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, body)
		if err != nil {
			t.Fatal(err)
		}
		// Call the transport directly: RoundTrip itself must close the body.
		if _, rtErr := c.Transport.RoundTrip(req); rtErr == nil {
			t.Errorf("%s: RoundTrip succeeded", name)
		}
		if !body.closed.Load() {
			t.Errorf("%s: request body left open", name)
		}
	}
	if srv.count() != 0 {
		t.Errorf("server got %d requests", srv.count())
	}
}

func TestNewClientValidatesHosts(t *testing.T) {
	b := fastBudget(t)
	log := slog.New(slog.DiscardHandler)
	for name, h := range map[string]upstream.Host{
		"no budget":  {AttemptTimeout: time.Second, MaxBodyBytes: 1},
		"no timeout": {Budget: b, MaxBodyBytes: 1},
		"no cap":     {Budget: b, AttemptTimeout: time.Second},
	} {
		if _, err := upstream.NewClient(log, map[string]upstream.Host{"h": h}); err == nil {
			t.Errorf("%s: NewClient accepted it", name)
		}
	}
	if _, err := upstream.NewClient(nil, nil); err == nil {
		t.Error("NewClient accepted a nil logger")
	}
}
