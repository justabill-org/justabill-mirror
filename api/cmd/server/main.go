// Server runs the Just a Bill REST API: the Chi router, its middleware and the handlers,
// backed by Cloud Spanner and an optional Redis cache.
//
// Settings come from flags or the matching environment variables (PORT, REDIS_URL,
// SPANNER_PROJECT, SPANNER_DATABASE_ROLE, TRUSTED_PROXY_HOPS, ACCOUNTS_ENABLED and the rest in
// .env.example). Secrets (REDIS_URL, API_SERVER_KEYS) can instead come from the file their _FILE
// variable names.
//
//	go run ./cmd/server --port 8080
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/justabill-org/justabill/api/internal/appenv"
	"github.com/justabill-org/justabill/api/internal/auth"
	"github.com/justabill-org/justabill/api/internal/cache"
	"github.com/justabill-org/justabill/api/internal/district"
	"github.com/justabill-org/justabill/api/internal/handler"
	"github.com/justabill-org/justabill/api/internal/health"
	mw "github.com/justabill-org/justabill/api/internal/middleware"
	"github.com/justabill-org/justabill/api/internal/secretfile"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/obs"
)

const (
	readTimeout = 10 * time.Second
	// readHeaderTimeout cuts off a client that trickles its headers
	// (slowloris) long before readTimeout would.
	readHeaderTimeout = 5 * time.Second
	writeTimeout      = 30 * time.Second
	// defaultIdleTimeout outlasts the 600 s keepalive of Google's external
	// Application Load Balancer, so the API never closes a connection the
	// load balancer is about to reuse (which shows up as a stray 502).
	defaultIdleTimeout = 620 * time.Second
	// maxHeaderBytes caps request headers; larger ones get a 431.
	maxHeaderBytes  = 16 << 10
	shutdownTimeout = 10 * time.Second
	// gzipLevel is the gzip/deflate level for JSON responses: 5 gets a big
	// bill's text to about a seventh of its size at a fraction of level 9's CPU.
	gzipLevel       = 5
	publicRateLimit = 60
	// accountUserRateLimit is the account routes' limit per signed-in
	// identity: 60 votes a minute, plus loading the most votes the web reads
	// (GET /me/votes, 101 pages of 100) and the rest of a page's calls, with
	// room to spare. The daily vote cap (AGG_DAILY_VOTE_CAP) still applies.
	accountUserRateLimit = 240
	// accountIPRateLimit is the account routes' limit per IP for requests
	// without a valid ID token: anonymous, or with a bad or expired one.
	accountIPRateLimit = 30
	// repsRateLimit is POST /reps's own limit per IP, on top of the public one: every call is a
	// Census geocoder request (docs/design/71-api-hardening.md).
	repsRateLimit = 10
	// defaultWebServerRateLimit is WEB_SERVER_RATE_LIMIT's default: the web
	// app's server-side calls per minute per pod, for every visitor it renders
	// for (docs/design/71-api-hardening.md).
	defaultWebServerRateLimit = 1200
	// searchRateLimit is a bill search's own limit per IP, on top of the public one: every
	// uncached search runs two full-text searches and a count on Spanner (#619).
	searchRateLimit = 20
	// defaultWebServerSearchRateLimit is WEB_SERVER_SEARCH_RATE_LIMIT's default: the web app's
	// server-side searches per minute per pod, for every visitor it renders for, counted within
	// WEB_SERVER_RATE_LIMIT too (#619).
	defaultWebServerSearchRateLimit = 300
	// API_SERVER_KEYS holds at most two keys, the current one and the next
	// during a rotation, each at least minServerKeyLength characters.
	maxServerKeys      = 2
	minServerKeyLength = 32
	// Defaults for AGG_DAILY_VOTE_CAP and AGG_DISTRICT_CHANGE_DAYS
	// (docs/design/89-aggregate-analytics.md, "The rules, in one place").
	defaultDailyVoteCap       = 500
	defaultDistrictChangeDays = 30
	day                       = 24 * time.Hour
	// serviceName is the API's OpenTelemetry service.name.
	serviceName = "justabill-api"
	// defaultRedisURL is the --redis-url default, used while REDIS_URL is unset.
	defaultRedisURL = "redis://localhost:6379"
)

func main() {
	rootCmd := &cobra.Command{
		Use:   "api-server",
		Short: "Just a Bill API server",
		RunE:  runServer,
	}

	rootCmd.Flags().String("port", "8080", "HTTP listen port")
	rootCmd.Flags().String("redis-url", defaultRedisURL, "Redis connection URL")
	rootCmd.Flags().String("spanner-project", "", "Spanner project")
	rootCmd.Flags().String("spanner-instance", "", "Spanner instance")
	rootCmd.Flags().String("spanner-database", "", "Spanner database")

	_ = viper.BindPFlag("port", rootCmd.Flags().Lookup("port"))
	_ = viper.BindPFlag("redis_url", rootCmd.Flags().Lookup("redis-url"))
	_ = viper.BindPFlag("spanner_project", rootCmd.Flags().Lookup("spanner-project"))
	_ = viper.BindPFlag("spanner_instance", rootCmd.Flags().Lookup("spanner-instance"))
	_ = viper.BindPFlag("spanner_database", rootCmd.Flags().Lookup("spanner-database"))

	viper.SetConfigName(".env")
	viper.SetConfigType("env")
	viper.AddConfigPath(".")
	viper.AddConfigPath("..")
	_ = viper.ReadInConfig()

	viper.AutomaticEnv()

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func runServer(_ *cobra.Command, _ []string) error {
	port := viper.GetString("port")
	project := viper.GetString("spanner_project")
	instance := viper.GetString("spanner_instance")
	database := viper.GetString("spanner_database")

	if project == "" || instance == "" || database == "" {
		return errors.New("SPANNER_PROJECT, SPANNER_INSTANCE, and SPANNER_DATABASE are required")
	}

	settings, err := authSettings()
	if err != nil {
		return err
	}

	// Telemetry comes first: the Spanner, Redis and geocoder clients take the
	// OpenTelemetry providers when they're created.
	tel, err := startTelemetry(settings.Production)
	if err != nil {
		return err
	}
	defer shutdownTelemetry(tel)
	logger := newLogger(tel.Logger().Handler())
	slog.SetDefault(logger)

	agg, err := aggregateSettings()
	if err != nil {
		return err
	}
	geocoder, err := censusGeocoderURL(logger)
	if err != nil {
		return err
	}
	edge, err := edgeSettings(settings.Production, logger)
	if err != nil {
		return err
	}
	timing, err := serverTimings()
	if err != nil {
		return err
	}

	ctx := context.Background()
	sc, err := openSpanner(ctx, project, instance, database, logger)
	if err != nil {
		return err
	}
	defer sc.Close()

	var probeOpts []health.Option
	c := openCache(ctx, edge.redisURL, logger)
	if c != nil {
		defer c.Close()
		probeOpts = append(probeOpts, health.WithCache(c))
	}

	users := spannerdb.NewUserRepo(sc)
	accounts, opts, err := setUpAccounts(ctx, settings, users, logger)
	if err != nil {
		return err
	}

	h := handler.New(sc.Spanner, append(opts,
		handler.WithWriteRules(agg.rules),
		agg.reader(sc),
		handler.WithDistrict(district.NewCensusLookupAt(geocoder)),
		handler.WithBills(spannerdb.NewBillRepo(sc)),
		handler.WithMembers(spannerdb.NewMemberRepo(sc)),
		handler.WithUsers(users),
		handler.WithVotes(spannerdb.NewVoteRepo(sc)),
		handler.WithCongresses(spannerdb.NewCongressRepo(sc)),
		handler.WithScorecard(spannerdb.NewScorecard(sc)),
		handler.WithGraph(spannerdb.NewGraphRepo(sc)),
		handler.WithLaw(spannerdb.NewLawRepo(sc)),
	)...)
	if c != nil {
		h.SetCache(c)
	}

	// The probes answer ahead of the router, outside CORS, the limiter and the access log.
	probes := health.New(logger, sc, probeOpts...)
	r := buildRouter(h, logger, accounts, edge)

	return serveUntilSignal(newServer(":"+port, probes.Wrap(r), timing.idle), probes, timing.drain, logger)
}

// cachePingTimeout bounds the startup check that Redis answers.
const cachePingTimeout = 5 * time.Second

// openCache returns the Redis cache, or nil when redisURL is empty (the cache is off) or
// invalid. A Redis that doesn't answer yet only logs a warning: the client keeps dialing, so the
// API caches once Redis is up (#457), and /readyz reports the cache without failing on it.
func openCache(ctx context.Context, redisURL string, logger *slog.Logger) *cache.Cache {
	if redisURL == "" {
		logger.InfoContext(ctx, "REDIS_URL is empty: serving without a cache")
		return nil
	}
	c, err := cache.New(redisURL)
	if err != nil {
		logger.WarnContext(ctx, "redis cache misconfigured, continuing without cache", "error", err)
		return nil
	}
	pingCtx, cancel := context.WithTimeout(ctx, cachePingTimeout)
	defer cancel()
	if err = c.Ping(pingCtx); err != nil {
		logger.WarnContext(ctx, "redis cache unavailable, serving uncached until it answers", "error", err)
	}
	return c
}

// serveUntilSignal binds srv's address and serves it until SIGINT or SIGTERM,
// then drains and shuts down as [serve] does.
func serveUntilSignal(srv *http.Server, probes *health.Probes, drain time.Duration, logger *slog.Logger) error {
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	context.AfterFunc(ctx, stop) // a second signal kills the process
	var lc net.ListenConfig
	ln, err := lc.Listen(ctx, "tcp", srv.Addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", srv.Addr, err)
	}
	return serve(ctx, srv, ln, probes, drain, logger)
}

// serve runs srv on ln until ctx is done. Then /readyz fails for drain, so the
// load balancer stops sending new requests while the server still answers,
// and srv shuts down gracefully within shutdownTimeout.
func serve(
	ctx context.Context, srv *http.Server, ln net.Listener, probes *health.Probes, drain time.Duration,
	logger *slog.Logger,
) error {
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	logger.InfoContext(ctx, "starting api server", "addr", ln.Addr().String())

	select {
	case err := <-served:
		return fmt.Errorf("server failed: %w", err)
	case <-ctx.Done():
	}

	stopCtx := context.WithoutCancel(ctx)
	probes.Drain()
	if drain > 0 {
		logger.InfoContext(stopCtx, "draining: /readyz fails until shutdown", "drain", drain.String())
		time.Sleep(drain)
	}
	logger.InfoContext(stopCtx, "shutting down server")
	shutdownCtx, cancel := context.WithTimeout(stopCtx, shutdownTimeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("shutdown: %w", err)
	}
	return nil
}

// newServer returns the API's HTTP server with its timeouts and header cap.
func newServer(addr string, h http.Handler, idle time.Duration) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           h,
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idle,
		MaxHeaderBytes:    maxHeaderBytes,
	}
}

// timings are the server's configurable durations.
type timings struct {
	idle  time.Duration // HTTP_IDLE_TIMEOUT
	drain time.Duration // SHUTDOWN_DRAIN
}

// serverTimings reads HTTP_IDLE_TIMEOUT and SHUTDOWN_DRAIN.
func serverTimings() (timings, error) {
	idle, err := httpIdleTimeout()
	if err != nil {
		return timings{}, err
	}
	drain, err := shutdownDrain()
	if err != nil {
		return timings{}, err
	}
	return timings{idle: idle, drain: drain}, nil
}

// httpIdleTimeout reads HTTP_IDLE_TIMEOUT, a Go duration such as "620s",
// defaulting to defaultIdleTimeout. Behind a load balancer it must stay above
// the load balancer's own keepalive timeout.
func httpIdleTimeout() (time.Duration, error) {
	raw := strings.TrimSpace(viper.GetString("http_idle_timeout"))
	if raw == "" {
		return defaultIdleTimeout, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d <= 0 {
		return 0, fmt.Errorf("HTTP_IDLE_TIMEOUT=%q: want a positive duration such as 620s", raw)
	}
	return d, nil
}

// shutdownDrain reads SHUTDOWN_DRAIN, how long /readyz fails after SIGTERM
// before the server shuts down, so the load balancer has stopped sending
// requests by then. It defaults to 0, right when nothing routes by readiness
// (local dev); on GKE it's about 15s, and the pod's termination grace period
// must cover it plus shutdownTimeout.
func shutdownDrain() (time.Duration, error) {
	raw := strings.TrimSpace(viper.GetString("shutdown_drain"))
	if raw == "" {
		return 0, nil
	}
	d, err := time.ParseDuration(raw)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("SHUTDOWN_DRAIN=%q: want a duration such as 15s, 0s or more", raw)
	}
	return d, nil
}

// startTelemetry sets up OpenTelemetry for the API with obs.Start, from the
// standard OTEL_* variables. Without an OTLP endpoint (as in local dev) it only
// logs, as JSON on stderr.
func startTelemetry(production bool) (*obs.Telemetry, error) {
	env := obs.EnvDevelopment
	if production {
		env = obs.EnvProduction
	}
	tel, err := obs.Start(context.Background(), obs.Config{Service: serviceName, Environment: env})
	if err != nil {
		return nil, fmt.Errorf("setting up telemetry: %w", err)
	}
	return tel, nil
}

// shutdownTelemetry flushes what's left to export, within obs.ShutdownTimeout.
func shutdownTelemetry(tel *obs.Telemetry) {
	ctx, cancel := context.WithTimeout(context.Background(), obs.ShutdownTimeout)
	defer cancel()
	if err := tel.Shutdown(ctx); err != nil {
		tel.Logger().WarnContext(ctx, "flushing telemetry", "error", err)
	}
}

// edge is what the router needs to face browsers, proxies and the web app's
// server.
type edge struct {
	hops int      // TRUSTED_PROXY_HOPS
	cors *mw.CORS // the CORS_ORIGIN allowlist
	// callers picks out the web app's server (API_SERVER_KEYS); nil puts
	// every caller in the per-IP buckets.
	callers              mw.CallerClass
	webServerLimit       int // WEB_SERVER_RATE_LIMIT
	webServerSearchLimit int // WEB_SERVER_SEARCH_RATE_LIMIT
	// gate is ACCESS_MODE=private's gate, which lets everything through when
	// the API is public; nil (as in tests) is the same.
	gate     func(http.Handler) http.Handler
	redisURL string // REDIS_URL, or the file REDIS_URL_FILE names
}

// edgeSettings reads TRUSTED_PROXY_HOPS, CORS_ORIGIN, API_SERVER_KEYS,
// WEB_SERVER_RATE_LIMIT, WEB_SERVER_SEARCH_RATE_LIMIT, ACCESS_MODE and
// ACCESS_ALLOW_CIDRS, and REDIS_URL.
func edgeSettings(production bool, logger *slog.Logger) (edge, error) {
	hops, err := trustedProxyHops(production)
	if err != nil {
		return edge{}, err
	}
	ctx := context.Background()
	if hops == 0 {
		logger.WarnContext(ctx,
			"TRUSTED_PROXY_HOPS=0: rate limiting by the TCP peer address; behind a load balancer, set its hop count")
	}
	cors, err := mw.NewCORS(viper.GetString("cors_origin"), production)
	if err != nil {
		return edge{}, err
	}
	keys, err := serverKeys()
	if err != nil {
		return edge{}, err
	}
	limit, err := positiveInt("web_server_rate_limit", defaultWebServerRateLimit)
	if err != nil {
		return edge{}, err
	}
	searchLimit, err := positiveInt("web_server_search_rate_limit", defaultWebServerSearchRateLimit)
	if err != nil {
		return edge{}, err
	}
	if len(keys) > 0 {
		logger.InfoContext(ctx, "API_SERVER_KEYS set: the web app's server has its own rate limit",
			"keys", len(keys), "web_server_rate_limit", limit, "web_server_search_rate_limit", searchLimit)
	}
	callers := mw.ServerKeys(keys)
	gate, err := accessGate(callers, len(keys), logger)
	if err != nil {
		return edge{}, err
	}
	redis, err := redisURL()
	if err != nil {
		return edge{}, err
	}
	return edge{
		hops: hops, cors: cors, callers: callers, webServerLimit: limit, webServerSearchLimit: searchLimit,
		gate: gate, redisURL: redis,
	}, nil
}

// accessGate reads ACCESS_MODE and ACCESS_ALLOW_CIDRS and returns the private
// mode's gate, which admits callers' trusted callers (keys is how many server
// keys there are) and the allowlist. When the API is public it passes every
// request through. A bad mode or CIDR is an error in every environment.
func accessGate(callers mw.CallerClass, keys int, logger *slog.Logger) (func(http.Handler) http.Handler, error) {
	mode, err := mw.ParseAccessMode(viper.GetString("access_mode"))
	if err != nil {
		return nil, err
	}
	allow, err := mw.ParseAllowCIDRs(viper.GetString("access_allow_cidrs"))
	if err != nil {
		return nil, err
	}
	ctx := context.Background()
	if mode == mw.AccessPublic {
		if len(allow) > 0 {
			logger.WarnContext(ctx, "ACCESS_ALLOW_CIDRS is ignored: ACCESS_MODE is public")
		}
		return func(next http.Handler) http.Handler { return next }, nil
	}
	if keys == 0 && len(allow) == 0 {
		logger.WarnContext(ctx, "ACCESS_MODE=private with no API_SERVER_KEYS or ACCESS_ALLOW_CIDRS: "+
			"every request but the probes gets 404")
	}
	logger.InfoContext(ctx, "ACCESS_MODE=private: only the web app's server and ACCESS_ALLOW_CIDRS are served",
		"server_keys", keys, "allow_cidrs", len(allow))
	return mw.PrivateAccess(callers, allow), nil
}

// redisURL reads REDIS_URL, or the file REDIS_URL_FILE names: production's URL
// carries the Redis password. A REDIS_URL set but empty turns the cache off (the
// e2e API, #834); viper reads an empty variable as unset, so it's checked in the
// environment itself. With neither, it's the --redis-url flag's default, local
// Redis.
func redisURL() (string, error) {
	u, err := secretfile.Resolve(explicitSetting, "redis_url")
	if err != nil || u != "" {
		return u, err
	}
	if v, ok := os.LookupEnv("REDIS_URL"); ok && strings.TrimSpace(v) == "" {
		return "", nil
	}
	return viper.GetString("redis_url"), nil
}

// explicitSetting is viper's value for key when it was set (by a flag, the
// environment or .env), and "" when it only has a flag default. A default
// mustn't count as setting both a variable and its _FILE form.
func explicitSetting(key string) string {
	if !viper.IsSet(key) {
		return ""
	}
	return viper.GetString(key)
}

// serverKeys reads API_SERVER_KEYS, or the file API_SERVER_KEYS_FILE names:
// up to two comma-separated keys that put the web app's server-side calls in
// their own rate-limit bucket (and, with ACCESS_MODE=private, let them in).
// Empty turns that off. Errors never quote a key.
func serverKeys() ([]string, error) {
	raw, err := secretfile.Resolve(explicitSetting, "api_server_keys")
	if err != nil {
		return nil, err
	}
	var keys []string
	for k := range strings.SplitSeq(raw, ",") {
		if k = strings.TrimSpace(k); k != "" {
			keys = append(keys, k)
		}
	}
	if len(keys) > maxServerKeys {
		return nil, fmt.Errorf("API_SERVER_KEYS has %d keys: want at most %d, the current and the next",
			len(keys), maxServerKeys)
	}
	for i, k := range keys {
		if len(k) < minServerKeyLength {
			return nil, fmt.Errorf("API_SERVER_KEYS key %d is %d characters: want at least %d "+
				"(openssl rand -base64 32)", i+1, len(k), minServerKeyLength)
		}
	}
	return keys, nil
}

// accountMiddleware is what the account routes need: the auth middleware and
// App Check for the vote and import routes.
type accountMiddleware struct {
	authn    *mw.Auth
	appCheck func(http.Handler) http.Handler
}

// newLogger is the server's logger on h, the handler obs.Start built. Every
// *Context call made while serving a request carries the request's request_id.
func newLogger(h slog.Handler) *slog.Logger {
	return slog.New(mw.RequestIDHandler(h))
}

// buildRouter builds the API's routes. accounts is nil when accounts are
// disabled, and then no account route exists at all. e holds the validated
// edge settings.
func buildRouter(h *handler.Handler, logger *slog.Logger, accounts *accountMiddleware, e edge) *chi.Mux {
	r := chi.NewRouter()
	r.Use(mw.RequestID)
	// The request's SERVER span and http.server.request.duration, named by
	// route; everything below runs inside the span, so its logs carry the
	// trace and span IDs.
	r.Use(obs.HTTPHandler(mw.RoutePattern))
	r.Use(mw.ClientIP(e.hops, logger))
	// The web server's renders count as the visitor's own requests when it
	// names the visitor (docs/design/607-per-visitor-web-limits.md): before
	// anything reads the client IP.
	r.Use(mw.Visitor(e.callers, logger))
	r.Use(obs.RecoverHandler(logger, mw.RoutePattern))
	r.Use(middleware.Timeout(writeTimeout))
	r.Use(mw.Logger(logger))
	// JSON bodies are gzipped for clients that accept it: a big bill's text or
	// detail is megabytes uncompressed (#450).
	r.Use(middleware.Compress(gzipLevel, "application/json"))
	if e.gate != nil {
		// Ahead of CORS and the limiter, whose headers and 429s would give a
		// private API away.
		r.Use(e.gate)
	}
	r.Use(e.cors.Handler)
	// The public limit covers every request except the account routes', which
	// have limits of their own, keyed by account (mountAccountRoutes). So it
	// wraps the public routes and the 404s (set before Route, whose subrouter
	// inherits it), not the whole mux.
	public := mw.RateLimit(publicRateLimit,
		mw.LogAs(mw.ClassIP, logger), mw.TrustedCallers(e.callers, e.webServerLimit))
	r.NotFound(public(http.NotFoundHandler()).ServeHTTP)

	r.Route("/api/v1", func(r chi.Router) {
		// Bodies are capped per group, never nested: see mw.MaxBytes.
		r.Group(func(r chi.Router) {
			r.Use(public, mw.MaxBytes(mw.DefaultMaxBody))
			mountPublicRoutes(r, h, accounts, searchLimit(e, logger), logger)
		})
		if accounts != nil {
			mountAccountRoutes(r, h, accounts, logger)
		}
	})

	return r
}

// mountPublicRoutes mounts the routes anyone can call. search is GET /bills's
// limiter for searches, on top of the public one.
func mountPublicRoutes(
	r chi.Router, h *handler.Handler, accounts *accountMiddleware, search func(http.Handler) http.Handler,
	logger *slog.Logger,
) {
	r.Method(http.MethodGet, "/bills", searchesThrough(search, listBillsRoute(h, accounts)))
	r.Method(http.MethodGet, "/bills/counts", searchesThrough(search, http.HandlerFunc(h.CountBills)))
	r.Get("/bills/{id}", h.GetBill)
	r.Get("/bills/{id}/actions", h.GetBillActions)
	r.Get("/bills/{id}/votes", h.GetBillVotes)
	r.Get("/bills/{id}/text", h.ListTextVersions)
	r.Get("/bills/{id}/diffs", h.ListDiffs)
	r.Get("/bills/{id}/amendments", h.ListAmendments)
	r.Get("/bills/{id}/gao-reports", h.ListGAOReports)
	r.Get("/bills/{id}/related", h.GetRelatedBills)
	r.Get("/bills/{id}/companion-votes", h.GetCompanionVotes)
	r.Get("/bills/{id}/law-changes", h.GetBillLawChanges)
	r.Get("/bills/{id}/aggregates", h.GetBillAggregates)
	r.Get("/bills/{id}/aggregates/{scope_key}", h.GetScopeAggregate)
	r.Get("/bills/{id}/text/{vid}", h.GetBillText)
	r.Get("/bills/{id}/diffs/{did}", h.GetBillDiff)
	r.Get("/bill-index", h.GetBillIndex)
	r.Get("/bill-statuses", h.GetBillStatuses)
	r.Get("/congresses", h.ListCongresses)
	r.Get("/policy-areas", h.ListPolicyAreas)
	r.Get("/law/{title}/{section}", h.GetLawSection)
	r.Get("/members", h.ListMembers)
	r.Get("/members/{id}", h.GetMember)
	r.Get("/members/{id}/collaborators", h.GetCollaborators)
	r.Get("/members/{id}/positions", h.GetMemberPositions)
	r.Get("/members/{id}/alignment", h.GetMemberAlignment)
	r.With(mw.RateLimit(repsRateLimit, mw.LogAs(mw.ClassReps, logger))).Post("/reps", h.FindReps)
}

// searchLimit is bill search's own limiter (#619): searchRateLimit a minute
// per IP, and one bucket of e.webServerSearchLimit for the web app's server.
func searchLimit(e edge, logger *slog.Logger) func(http.Handler) http.Handler {
	opts := []mw.RateLimitOption{mw.LogAs(mw.ClassSearch, logger)}
	if e.callers != nil {
		// Its own class, so a 429 from this bucket names WEB_SERVER_SEARCH_RATE_LIMIT's bucket
		// rather than WEB_SERVER_RATE_LIMIT's.
		classify := func(r *http.Request) string {
			if e.callers(r) == mw.ClassWebServer {
				return mw.ClassWebServerSearch
			}
			return mw.ClassIP
		}
		opts = append(opts, mw.TrustedCallers(classify, e.webServerSearchLimit))
	}
	return mw.RateLimit(searchRateLimit, opts...)
}

// searchesThrough sends GET /bills and /bills/counts requests with a search (q
// or search, as the handlers read them) through limit before next, and the rest straight to
// next, so browsing the list doesn't spend a search's allowance.
func searchesThrough(limit func(http.Handler) http.Handler, next http.Handler) http.Handler {
	limited := limit(next)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if q := r.URL.Query(); q.Get("q") != "" || q.Get("search") != "" {
			limited.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// listBillsRoute is GET /bills. Its one personal variant, unvoted=true, needs
// the signed-in user, so with accounts on those requests go through the auth
// middleware (an invalid token is a 401, as on the account routes). Every
// other list request skips it: the public list doesn't depend on who asks,
// and a sign-in outage mustn't take it down.
func listBillsRoute(h *handler.Handler, accounts *accountMiddleware) http.Handler {
	list := http.HandlerFunc(h.ListBills)
	if accounts == nil {
		return list
	}
	signedIn := accounts.authn.Handler(list)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("unvoted") == "true" {
			signedIn.ServeHTTP(w, r)
			return
		}
		list.ServeHTTP(w, r)
	})
}

// mountAccountRoutes mounts the routes that need a signed-in user. They
// share one rate limiter, which authenticates the request and counts it per
// account, or per IP without a valid token (#494), and the vote and import
// routes also go through App Check. votes:import sits in a group of its own
// because its body cap is above the default.
func mountAccountRoutes(r chi.Router, h *handler.Handler, accounts *accountMiddleware, logger *slog.Logger) {
	appCheck := accounts.appCheck
	if appCheck == nil {
		appCheck = func(next http.Handler) http.Handler { return next }
	}
	limit := mw.SignedInRateLimit(accounts.authn.Handler, accountUserRateLimit, accountIPRateLimit, logger)

	r.Group(func(r chi.Router) {
		r.Use(mw.MaxBytes(handler.MaxImportBody), limit, mw.RequireAuth)
		r.With(appCheck).Post("/me/votes:import", h.ImportMyVotes)
	})

	r.Group(func(r chi.Router) {
		r.Use(mw.MaxBytes(mw.DefaultMaxBody), limit)

		// These work before the account exists.
		r.With(mw.RequirePrincipal).Post("/me", h.CreateMe)
		r.With(mw.RequirePrincipal).Delete("/me", h.DeleteMe)

		r.Group(func(r chi.Router) {
			r.Use(mw.RequireAuth)
			r.Get("/me", h.GetMe)
			r.Patch("/me", h.UpdateMe)
			r.Get("/me/export", h.ExportMe)
			r.With(appCheck).Post("/bills/{id}/vote", h.CastVote)
			r.Delete("/bills/{id}/vote", h.DeleteVote)
			r.Get("/me/votes", h.GetMyVotes)
			r.Get("/me/favorites", h.GetMyFavorites)
			r.Post("/me/favorites/{billID}", h.AddFavorite)
			r.Delete("/me/favorites/{billID}", h.RemoveFavorite)
			r.Get("/me/scorecard", h.GetScorecard)
			r.Get("/me/compare/{memberID}", h.CompareMember)
		})
	})
}

// setUpAccounts creates the Identity Platform client, the auth middleware
// and, unless APP_CHECK_MODE is off, the App Check verifier when accounts are
// enabled. All are nil when they're off.
func setUpAccounts(
	ctx context.Context, s auth.Settings, users mw.UserLookup, logger *slog.Logger,
) (*accountMiddleware, []handler.Option, error) {
	logger.InfoContext(ctx, "account routes", "enabled", s.Enabled, "auth_project", s.ProjectID,
		"auth_emulator", s.EmulatorHost != "", "app_check", s.AppCheck)
	if !s.Enabled {
		return nil, nil, nil
	}
	accounts, err := auth.NewFirebase(ctx, s.ProjectID)
	if err != nil {
		return nil, nil, fmt.Errorf("accounts are enabled but sign-in can't be set up: %w", err)
	}
	authn := mw.NewAuth(accounts, users, logger)
	var checker auth.AppChecker
	if s.AppCheck != auth.AppCheckOff {
		if checker, err = auth.NewFirebaseAppCheck(ctx, s.ProjectID); err != nil {
			return nil, nil, fmt.Errorf("APP_CHECK_MODE=%s but App Check can't be set up: %w", s.AppCheck, err)
		}
	}
	return &accountMiddleware{authn: authn, appCheck: mw.AppCheck(checker, s.AppCheck, logger)},
		[]handler.Option{handler.WithAccounts(accounts, authn.Forget)}, nil
}

// censusGeocoderURL reads CENSUS_GEOCODER_URL, where find-my-reps sends
// addresses: a stub for local and CI smoke tests
// (docs/design/84-e2e-smoke-tests.md). Unset, as in production, it's the Census
// Bureau geocoder. An override is logged, since every address goes there.
func censusGeocoderURL(logger *slog.Logger) (string, error) {
	raw := strings.TrimSpace(viper.GetString("census_geocoder_url"))
	if raw == "" {
		return district.CensusGeocoderURL, nil
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("CENSUS_GEOCODER_URL=%q must be an http or https URL", raw)
	}
	logger.WarnContext(context.Background(), "CENSUS_GEOCODER_URL is set: addresses go to it, not the Census Bureau",
		"census_geocoder_url", raw)
	return raw, nil
}

// aggregates holds the aggregate analytics settings (docs/design/89-aggregate-analytics.md).
type aggregates struct {
	rules  handler.WriteRules
	public bool // AGGREGATES_PUBLIC
}

// aggregateSettings reads the per-account write limits and AGGREGATES_PUBLIC, which serves the
// aggregate read routes when true. It's off by default: the job runs dark until the maintainer has
// reviewed what it would publish.
func aggregateSettings() (aggregates, error) {
	rules, err := writeRules()
	if err != nil {
		return aggregates{}, err
	}
	a := aggregates{rules: rules}
	if raw := strings.TrimSpace(viper.GetString("aggregates_public")); raw != "" {
		if a.public, err = strconv.ParseBool(raw); err != nil {
			return aggregates{}, fmt.Errorf("AGGREGATES_PUBLIC=%q: want true or false", raw)
		}
	}
	return a, nil
}

// reader is the handler option that serves the aggregate routes from sc, or none while they're
// off, and then they answer 404.
func (a aggregates) reader(sc *spannerdb.Client) handler.Option {
	if !a.public {
		return func(*handler.Handler) {}
	}
	return handler.WithAggregates(spannerdb.NewAggregateReader(sc))
}

// writeRules reads AGG_DAILY_VOTE_CAP and AGG_DISTRICT_CHANGE_DAYS, the
// per-account write limits. 0 turns a limit off.
func writeRules() (handler.WriteRules, error) {
	voteCap, err := nonNegativeInt("agg_daily_vote_cap", defaultDailyVoteCap)
	if err != nil {
		return handler.WriteRules{}, err
	}
	days, err := nonNegativeInt("agg_district_change_days", defaultDistrictChangeDays)
	if err != nil {
		return handler.WriteRules{}, err
	}
	return handler.WriteRules{DailyVoteCap: voteCap, DistrictChangeInterval: time.Duration(days) * day}, nil
}

// positiveInt reads the setting key as a whole number above 0, or fallback
// when it's unset.
func positiveInt(key string, fallback int) (int, error) {
	n, err := nonNegativeInt(key, fallback)
	if err != nil || n == 0 {
		return 0, fmt.Errorf("%s=%q: want a whole number above 0", strings.ToUpper(key), viper.GetString(key))
	}
	return n, nil
}

// nonNegativeInt reads the viper key as a whole number, or fallback when unset.
func nonNegativeInt(key string, fallback int) (int, error) {
	raw := strings.TrimSpace(viper.GetString(key))
	if raw == "" {
		return fallback, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("%s=%q: want a whole number, 0 or more", strings.ToUpper(key), raw)
	}
	return n, nil
}

// trustedProxyHops reads TRUSTED_PROXY_HOPS, the number of reverse proxies in
// front of the API. It has no default: 0 (use the TCP peer address) is right
// for local dev and wrong behind a load balancer, where every client would
// share one rate-limit bucket, and too low a count lets clients spoof their
// IP. So the value has to be set on purpose, and production refuses 0.
func trustedProxyHops(production bool) (int, error) {
	raw := strings.TrimSpace(viper.GetString("trusted_proxy_hops"))
	if raw == "" {
		msg := "TRUSTED_PROXY_HOPS is required: set 0 when clients connect directly (local dev), " +
			"or the number of proxies in front of the API (2 behind Google's external Application Load Balancer)"
		if viper.IsSet("trusted_proxies") {
			msg += "; TRUSTED_PROXIES was renamed to TRUSTED_PROXY_HOPS"
		}
		return 0, errors.New(msg)
	}
	hops, err := strconv.Atoi(raw)
	if err != nil || hops < 0 {
		return 0, fmt.Errorf("TRUSTED_PROXY_HOPS=%q: want a whole number, 0 or more", raw)
	}
	if hops == 0 && production {
		return 0, errors.New("TRUSTED_PROXY_HOPS=0 with APP_ENV=production: behind a load balancer every " +
			"client would share one rate-limit bucket; set the load balancer's hop count")
	}
	return hops, nil
}

// authSettings reads and validates APP_ENV, ACCOUNTS_ENABLED,
// AUTH_PROJECT_ID, FIREBASE_AUTH_EMULATOR_HOST and APP_CHECK_MODE. Accounts default to on
// outside production and off in production.
func authSettings() (auth.Settings, error) {
	env, err := appenv.Parse(viper.GetString("app_env"))
	if err != nil {
		return auth.Settings{}, err
	}
	s := auth.Settings{
		Production:   env == appenv.Production,
		Enabled:      env != appenv.Production,
		ProjectID:    viper.GetString("auth_project_id"),
		EmulatorHost: viper.GetString("firebase_auth_emulator_host"),
	}
	if viper.IsSet("accounts_enabled") {
		s.Enabled = viper.GetBool("accounts_enabled")
	}
	if s.AppCheck, err = auth.ParseAppCheckMode(viper.GetString("app_check_mode")); err != nil {
		return auth.Settings{}, err
	}
	if err = s.Validate(); err != nil {
		return auth.Settings{}, err
	}
	// The Admin SDK reads the emulator host from the process environment, so
	// a value that only came from .env has to be exported for it to apply.
	if s.EmulatorHost != "" && os.Getenv("FIREBASE_AUTH_EMULATOR_HOST") == "" {
		if err = os.Setenv("FIREBASE_AUTH_EMULATOR_HOST", s.EmulatorHost); err != nil {
			return auth.Settings{}, fmt.Errorf("export FIREBASE_AUTH_EMULATOR_HOST: %w", err)
		}
	}
	return s, nil
}
