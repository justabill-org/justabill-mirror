// Vercel-drain receives Vercel's log drain and team webhooks, checks their signatures, scrubs
// them and forwards them as OpenTelemetry log records through obs (design
// docs/design/53-observability.md, Decision 6). It runs as a small scale-to-zero service.
//
// Settings come from the environment:
//   - PORT: the listen port (default 8080);
//   - VERCEL_DRAIN_SECRET: the log drain's signature verification secret (required);
//   - VERCEL_WEBHOOK_SECRET: the team webhook's secret (required);
//   - VERCEL_DRAIN_SECRET_FILE, VERCEL_WEBHOOK_SECRET_FILE: files to read either secret from
//     instead (a mounted secret), not both forms;
//   - APP_ENV: development (the default) or production;
//   - the standard OTEL_* variables, which obs reads. Without an OTLP endpoint nothing is forwarded.
//
// Routes: POST /v1/drain, POST /v1/webhook and GET /healthz.
//
//	VERCEL_DRAIN_SECRET=… VERCEL_WEBHOOK_SECRET=… go run ./cmd/vercel-drain
package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/justabill-org/justabill/api/internal/appenv"
	"github.com/justabill-org/justabill/api/internal/secretfile"
	"github.com/justabill-org/justabill/api/internal/verceldrain"
	"github.com/justabill-org/justabill/obs"
)

const (
	serviceName       = "vercel-drain"
	defaultPort       = "8080"
	readHeaderTimeout = 5 * time.Second
	// Vercel gives a webhook 30 s to answer; a 5 MB drain batch needs well under that.
	readTimeout     = 20 * time.Second
	writeTimeout    = 25 * time.Second
	idleTimeout     = 60 * time.Second
	shutdownTimeout = 10 * time.Second
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "vercel-drain:", err)
		os.Exit(1)
	}
}

func run() error {
	env, err := appenv.Parse(os.Getenv("APP_ENV"))
	if err != nil {
		return err
	}

	environment := obs.EnvDevelopment
	if env == appenv.Production {
		environment = obs.EnvProduction
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	tel, err := obs.Start(ctx, obs.Config{Service: serviceName, Environment: environment})
	if err != nil {
		return fmt.Errorf("setting up telemetry: %w", err)
	}
	defer shutdownTelemetry(tel)

	logger := tel.Logger()

	drainSecret, err := secretfile.Resolve(envSetting, "vercel_drain_secret")
	if err != nil {
		return err
	}
	webhookSecret, err := secretfile.Resolve(envSetting, "vercel_webhook_secret")
	if err != nil {
		return err
	}
	receiver, err := verceldrain.New(verceldrain.Config{
		DrainSecret:   drainSecret,
		WebhookSecret: webhookSecret,
		Logger:        logger,
	})
	if err != nil {
		return err
	}

	if !tel.Exporting() {
		logger.WarnContext(ctx, "vercel-drain: no OTLP endpoint, so accepted lines and events are dropped")
	}

	srv := &http.Server{
		Addr:              net.JoinHostPort("", cmp.Or(os.Getenv("PORT"), defaultPort)),
		Handler:           receiver.Handler(),
		ReadHeaderTimeout: readHeaderTimeout,
		ReadTimeout:       readTimeout,
		WriteTimeout:      writeTimeout,
		IdleTimeout:       idleTimeout,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}

	return serve(ctx, srv, logger)
}

// serve runs srv until ctx is canceled (SIGINT or SIGTERM), then shuts it down gracefully.
func serve(ctx context.Context, srv *http.Server, logger *slog.Logger) error {
	errc := make(chan error, 1)

	go func() {
		logger.InfoContext(ctx, "starting vercel-drain", "addr", srv.Addr)
		errc <- srv.ListenAndServe()
	}()

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}

	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownTimeout)
	defer cancel()

	if err := srv.Shutdown(shutdownCtx); err != nil {
		return err
	}

	if err := <-errc; !errors.Is(err, http.ErrServerClosed) {
		return err
	}

	return nil
}

// shutdownTelemetry flushes what's left to export, within obs.ShutdownTimeout.
func shutdownTelemetry(tel *obs.Telemetry) {
	ctx, cancel := context.WithTimeout(context.Background(), obs.ShutdownTimeout)
	defer cancel()

	if err := tel.Shutdown(ctx); err != nil {
		tel.Logger().WarnContext(ctx, "flushing telemetry", "error", err)
	}
}

// envSetting reads the environment variable for a lowercase settings key:
// vercel_drain_secret is VERCEL_DRAIN_SECRET.
func envSetting(key string) string { return os.Getenv(strings.ToUpper(key)) }
