// Package health serves the pipeline's probe endpoints on their own port: /healthz for the
// liveness probe, /readyz for the readiness probe and /status for operators through
// kubectl port-forward (docs/design/80-pipeline-operability.md, "Health server").
package health

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"github.com/justabill-org/justabill/pipeline/internal/redact"
	"github.com/justabill-org/justabill/pipeline/internal/scheduler"
)

const (
	// DefaultAddr is where the server listens when PIPELINE_HEALTH_ADDR is unset.
	DefaultAddr = ":8081"
	// StuckGrace is how long a run may go past its job's timeout before /healthz fails. A job
	// that honours cancellation returns well within it; one still running has ignored its
	// context, and only a restart frees it.
	StuckGrace = 5 * time.Minute
	// PingTimeout bounds the readiness check's SELECT 1.
	PingTimeout = 2 * time.Second
	// ReadyTTL is how long a readiness result is reused, so probes don't each query Spanner.
	ReadyTTL = 10 * time.Second

	readHeaderTimeout = 5 * time.Second
	writeTimeout      = 10 * time.Second
	idleTimeout       = time.Minute
	maxHeaderBytes    = 8 << 10
)

// The roles /status reports.
const (
	RoleLeader  = "leader"  // this process runs the jobs
	RoleStandby = "standby" // another process holds the lease; this one waits for it
)

// Jobs reports the scheduler's job states. [*scheduler.Scheduler] implements it.
type Jobs interface {
	Snapshot() []scheduler.JobState
}

// Pinger checks that the database answers. [*spannerdb.Client] implements it.
type Pinger interface {
	Ping(ctx context.Context) error
}

// Lease is this process's view of the pipeline lease, for /status.
type Lease struct {
	Role      string    // RoleLeader or RoleStandby
	Holder    string    // the lease holder's ID; empty when nobody holds it
	ExpiresAt time.Time // when the holder's lease lapses unless renewed
}

// LeaseSource reports the pipeline lease (#260's elector).
type LeaseSource interface {
	Lease() Lease
}

// Option configures a Server.
type Option func(*Server)

// WithLease makes /status report the lease from src. Without it, the process runs every job
// itself, and /status reports the leader role with no lease.
func WithLease(src LeaseSource) Option {
	return func(s *Server) { s.lease = src }
}

// WithClock replaces [time.Now], for tests.
func WithClock(now func() time.Time) Option {
	return func(s *Server) { s.now = now }
}

// Server answers the probes. It doesn't log requests: kubelet probes every few seconds.
type Server struct {
	log   *slog.Logger
	jobs  Jobs
	db    Pinger
	lease LeaseSource
	now   func() time.Time

	draining atomic.Bool

	// mu guards the readiness cache and lets one probe at a time ping the database.
	mu        sync.Mutex
	checkedAt time.Time
	ready     bool

	srv *http.Server
	// ln is the socket Listen bound. Shutdown closes it itself: http.Server.Shutdown closes
	// only the listeners Serve has registered, and Serve runs in a goroutine that may not have
	// started yet (#513).
	ln net.Listener
}

// New returns a Server that reads job states from jobs and checks readiness with db.
func New(log *slog.Logger, jobs Jobs, db Pinger, opts ...Option) *Server {
	s := &Server{log: log, jobs: jobs, db: db, now: time.Now}
	for _, opt := range opts {
		opt(s)
	}
	return s
}

// Drain marks the process not ready, for the first step of the shutdown sequence. Liveness
// stays up, so kubelet doesn't restart a process that is shutting down.
func (s *Server) Drain() { s.draining.Store(true) }

// Handler returns the probe routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", s.healthz)
	mux.HandleFunc("GET /readyz", s.readyz)
	mux.HandleFunc("GET /status", s.status)
	return mux
}

// Listen binds addr and serves [Server.Handler] in the background. Binding happens before it
// returns, so a port in use fails startup. It returns the bound address.
func (s *Server) Listen(addr string) (net.Addr, error) {
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("health server: %w", err)
	}
	s.ln = ln
	s.srv = &http.Server{
		Handler:           s.Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readHeaderTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		MaxHeaderBytes:    maxHeaderBytes,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelWarn),
	}
	go func() {
		if serveErr := s.srv.Serve(ln); !errors.Is(serveErr, http.ErrServerClosed) {
			s.log.Error("health server stopped", "error", serveErr.Error())
		}
	}()
	return ln.Addr(), nil
}

// Shutdown stops the server started by [Server.Listen], waiting for open requests until ctx
// is done. When it returns, the socket is closed, whether or not the background Serve has
// started. It does nothing if Listen wasn't called.
func (s *Server) Shutdown(ctx context.Context) error {
	if s.srv == nil {
		return nil
	}
	err := s.srv.Shutdown(ctx)
	// Serve closes the listener too once it runs; whichever closes it second gets net.ErrClosed.
	if closeErr := s.ln.Close(); closeErr != nil && !errors.Is(closeErr, net.ErrClosed) {
		err = errors.Join(err, closeErr)
	}
	if err != nil {
		return fmt.Errorf("health server shutdown: %w", err)
	}
	return nil
}

// probeBody is the JSON answer of /healthz and /readyz.
type probeBody struct {
	Status string   `json:"status"`
	Stuck  []string `json:"stuck_jobs,omitempty"`
}

// healthz fails only when a run has outlived its timeout by StuckGrace. It never checks the
// database: a Spanner outage should make the pod unready, not restart it in a loop.
func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	var stuck []string
	now := s.now()
	for _, st := range s.jobs.Snapshot() {
		if isStuck(st, now) {
			stuck = append(stuck, st.Name)
		}
	}
	if len(stuck) > 0 {
		writeJSON(w, http.StatusServiceUnavailable, probeBody{Status: "stuck", Stuck: stuck})
		return
	}
	writeJSON(w, http.StatusOK, probeBody{Status: "ok"})
}

// isStuck reports whether st is a run past its timeout plus StuckGrace. A job without a
// timeout is never stuck.
func isStuck(st scheduler.JobState, now time.Time) bool {
	return st.Running && st.Timeout > 0 && now.Sub(st.LastStart) > st.Timeout+StuckGrace
}

func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	switch {
	case s.draining.Load():
		writeJSON(w, http.StatusServiceUnavailable, probeBody{Status: "shutting_down"})
	case !s.checkReady(r.Context()):
		writeJSON(w, http.StatusServiceUnavailable, probeBody{Status: "database_unreachable"})
	default:
		writeJSON(w, http.StatusOK, probeBody{Status: "ok"})
	}
}

// checkReady pings the database, or reuses a result younger than ReadyTTL. It logs when
// readiness changes, not on every probe.
func (s *Server) checkReady(ctx context.Context) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.checkedAt.IsZero() && s.now().Sub(s.checkedAt) < ReadyTTL {
		return s.ready
	}
	// A probe that hangs up mustn't turn into a cached failure.
	pingCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), PingTimeout)
	defer cancel()
	err := s.db.Ping(pingCtx)
	ready := err == nil
	switch {
	case !ready && (s.ready || s.checkedAt.IsZero()):
		s.log.WarnContext(ctx, "pipeline not ready: database ping failed", "error", redact.Error(err))
	case ready && !s.ready && !s.checkedAt.IsZero():
		s.log.InfoContext(ctx, "pipeline ready: database ping succeeded")
	}
	s.ready, s.checkedAt = ready, s.now()
	return ready
}

// statusBody is the JSON answer of /status. It holds no error text, keys or data.
type statusBody struct {
	Role         string     `json:"role"`
	Lease        *leaseBody `json:"lease"`
	ShuttingDown bool       `json:"shutting_down"`
	Jobs         []jobBody  `json:"jobs"`
}

type leaseBody struct {
	Holder    string    `json:"holder"`
	ExpiresAt time.Time `json:"expires_at,omitzero"`
}

type jobBody struct {
	Name           string    `json:"name"`
	Running        bool      `json:"running"`
	Stuck          bool      `json:"stuck,omitempty"`
	TimeoutSeconds int64     `json:"timeout_seconds,omitempty"`
	LastStatus     string    `json:"last_status,omitempty"`
	LastStart      time.Time `json:"last_start,omitzero"`
	LastFinish     time.Time `json:"last_finish,omitzero"`
	NextRun        time.Time `json:"next_run,omitzero"`
}

func (s *Server) status(w http.ResponseWriter, _ *http.Request) {
	body := statusBody{Role: RoleLeader, ShuttingDown: s.draining.Load(), Jobs: []jobBody{}}
	if s.lease != nil {
		l := s.lease.Lease()
		body.Role = l.Role
		body.Lease = &leaseBody{Holder: l.Holder, ExpiresAt: l.ExpiresAt}
	}
	now := s.now()
	for _, st := range s.jobs.Snapshot() {
		body.Jobs = append(body.Jobs, jobBody{
			Name:           st.Name,
			Running:        st.Running,
			Stuck:          isStuck(st, now),
			TimeoutSeconds: int64(st.Timeout / time.Second),
			LastStatus:     string(st.LastStatus),
			LastStart:      st.LastStart,
			LastFinish:     st.LastFinish,
			NextRun:        st.NextRun,
		})
	}
	writeJSON(w, http.StatusOK, body)
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}
