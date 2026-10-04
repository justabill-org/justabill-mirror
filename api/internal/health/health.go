// Package health answers the API's probes: /healthz for the liveness probe, /readyz for the
// readiness probe and the load balancer's health check, and /health, an alias of /readyz kept
// until #53's uptime checks move to /readyz (docs/design/71-api-hardening.md, "Health").
//
// [Probes.Wrap] serves them ahead of the API's router, so they skip CORS, the rate limiter and
// the access log.
package health

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"sync"
	"sync/atomic"
	"time"
)

const (
	// CheckTimeout bounds each readiness check. A check that hasn't answered by then fails.
	CheckTimeout = 2 * time.Second
	// ReadyTTL is how long a readiness result is reused, so public probes through the load
	// balancer cost at most one Spanner query per TTL per pod.
	ReadyTTL = 2 * time.Second

	statusOK   = "ok"
	statusFail = "fail"

	// The check names /readyz reports.
	checkSpanner  = "spanner"
	checkCache    = "cache"
	checkDraining = "draining"
)

// errNotConfigured fails the Spanner check when there's no client.
var errNotConfigured = errors.New("not configured")

// Pinger checks that a dependency answers. [*spannerdb.Client] and [*cache.Cache] implement it.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Option configures [Probes].
type Option func(*Probes)

// WithCache makes /readyz report the Redis cache. The cache is optional, so its failure is
// reported but never makes the API unready. Without it, /readyz doesn't list the cache.
func WithCache(c Pinger) Option {
	return func(p *Probes) { p.cache = c }
}

// WithClock replaces [time.Now], for tests.
func WithClock(now func() time.Time) Option {
	return func(p *Probes) { p.now = now }
}

// Probes answers /healthz, /readyz and /health. It doesn't log requests; it logs when readiness
// changes.
type Probes struct {
	log     *slog.Logger
	spanner Pinger
	cache   Pinger
	now     func() time.Time

	draining atomic.Bool

	// mu guards the cached result and lets one probe at a time run the checks, so concurrent
	// probes wait for that run and share its result instead of each querying Spanner.
	mu        sync.Mutex
	checkedAt time.Time
	last      result
}

// result is one run of the readiness checks.
type result struct {
	ready  bool
	checks map[string]string
}

// New returns Probes whose readiness depends on spanner. A nil spanner is never ready.
func New(log *slog.Logger, spanner Pinger, opts ...Option) *Probes {
	p := &Probes{log: log, spanner: spanner, now: time.Now}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// Drain makes /readyz fail from now on, the first step of a graceful shutdown: the load
// balancer stops sending new requests while the server still answers the ones in flight.
// /healthz stays up, so the kubelet doesn't restart a process that is shutting down.
func (p *Probes) Drain() { p.draining.Store(true) }

// Wrap returns a handler that answers GET and HEAD on /healthz, /readyz and /health itself and
// passes every other request to next.
func (p *Probes) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			next.ServeHTTP(w, r)
			return
		}
		switch r.URL.Path {
		case "/healthz":
			p.Healthz(w, r)
		case "/readyz", "/health":
			p.Readyz(w, r)
		default:
			next.ServeHTTP(w, r)
		}
	})
}

// probeBody is the JSON answer of the probes. It names checks and says ok or fail, never why.
type probeBody struct {
	Status string            `json:"status"`
	Checks map[string]string `json:"checks,omitempty"`
}

// Healthz answers 200 while the process serves. It checks nothing else: a Spanner outage should
// make the pod unready, not restart it in a loop.
func (p *Probes) Healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, probeBody{Status: statusOK})
}

// Readyz answers 200 when Spanner answers and the server isn't draining, 503 otherwise. The
// cache is reported but doesn't count.
func (p *Probes) Readyz(w http.ResponseWriter, r *http.Request) {
	if p.draining.Load() {
		writeJSON(w, http.StatusServiceUnavailable,
			probeBody{Status: statusFail, Checks: map[string]string{checkDraining: statusFail}})
		return
	}
	res := p.check(r.Context())
	code, status := http.StatusOK, statusOK
	if !res.ready {
		code, status = http.StatusServiceUnavailable, statusFail
	}
	writeJSON(w, code, probeBody{Status: status, Checks: res.checks})
}

// check runs the readiness checks, or reuses a result younger than ReadyTTL.
func (p *Probes) check(ctx context.Context) result {
	p.mu.Lock()
	defer p.mu.Unlock()
	if !p.checkedAt.IsZero() && p.now().Sub(p.checkedAt) < ReadyTTL {
		return p.last
	}

	// A probe that hangs up mustn't turn into a cached failure.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), CheckTimeout)
	defer cancel()

	var cacheErr error
	var wg sync.WaitGroup
	if p.cache != nil {
		wg.Go(func() { cacheErr = ping(ctx, p.cache) })
	}
	spannerErr := ping(ctx, p.spanner)
	wg.Wait()

	res := result{ready: spannerErr == nil, checks: map[string]string{checkSpanner: okOrFail(spannerErr)}}
	if p.cache != nil {
		res.checks[checkCache] = okOrFail(cacheErr)
	}
	p.logChange(ctx, res, spannerErr, cacheErr)
	p.last, p.checkedAt = res, p.now()
	return res
}

// logChange logs a check that starts failing or recovers, not every probe.
func (p *Probes) logChange(ctx context.Context, res result, spannerErr, cacheErr error) {
	first := p.checkedAt.IsZero()
	for name, err := range map[string]error{checkSpanner: spannerErr, checkCache: cacheErr} {
		state, known := res.checks[name]
		if !known {
			continue
		}
		before := p.last.checks[name]
		switch {
		case state == statusFail && (first || before == statusOK):
			p.log.WarnContext(ctx, "readiness check failing", "check", name, "error", err.Error())
		case state == statusOK && before == statusFail:
			p.log.InfoContext(ctx, "readiness check recovered", "check", name)
		}
	}
}

// ping runs pinger.Ping, giving up when ctx is done even if Ping doesn't return.
func ping(ctx context.Context, pinger Pinger) error {
	if pinger == nil {
		return errNotConfigured
	}
	done := make(chan error, 1)
	go func() { done <- pinger.Ping(ctx) }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		return ctx.Err()
	}
}

func okOrFail(err error) string {
	if err != nil {
		return statusFail
	}
	return statusOK
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
