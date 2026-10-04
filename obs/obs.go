// Package obs is Just a Bill's one observability library. A process calls Start once to set up
// OpenTelemetry traces, metrics and logs, and application code uses only the OpenTelemetry API
// (go.opentelemetry.io/otel) and obs helpers. Only this module imports the SDK and the exporters.
//
// Everything is configured with the standard OTEL_* environment variables, and obs supplies
// defaults only. Without an OTLP endpoint obs is inert: the providers stay no-ops and logs go to
// stderr as JSON in the Cloud Logging field format. See docs/design/53-observability.md.
package obs

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"time"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/contrib/detectors/gcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/log/global"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

// ShutdownTimeout is how long a process should give Shutdown to flush on SIGTERM.
const ShutdownTimeout = 5 * time.Second

// scope is the instrumentation scope of the slog bridge.
const scope = "github.com/justabill-org/justabill/obs"

// ErrNoService is returned by Start when Config.Service is empty.
var ErrNoService = errors.New("obs: Config.Service is required")

// Config describes the process. Only Service is required; OTEL_SERVICE_NAME and
// OTEL_RESOURCE_ATTRIBUTES override the resource attributes set from it.
type Config struct {
	// Service is service.name, for example "justabill-api".
	Service string
	// Version is service.version. Empty means the VCS revision in the binary's build info.
	Version string
	// Environment is deployment.environment.name: production, preview or development (the default).
	Environment string
	// Level is the lowest level logged. Defaults to slog.LevelInfo.
	Level slog.Leveler
	// Stderr receives the JSON log lines. Defaults to os.Stderr.
	Stderr io.Writer
	// ProjectID is the Google Cloud project that the trace field of stderr lines points to.
	// Defaults to GOOGLE_CLOUD_PROJECT; without either, the field holds the bare trace ID.
	ProjectID string

	// detectors replaces the GCP resource detector in tests.
	detectors []resource.Detector
}

// Telemetry is what Start set up. Its Shutdown flushes and stops every provider.
type Telemetry struct {
	logger    *slog.Logger
	signals   signals
	shutdowns []func(context.Context) error
}

// Start sets up OpenTelemetry for the process: the resource, the W3C tracecontext and baggage
// propagators, and a tracer, meter and logger provider for each signal that has an OTLP endpoint.
// It installs them as the globals and returns the process's logger. Call Shutdown before exit.
//
// Without an endpoint, or with OTEL_SDK_DISABLED=true, the providers stay no-ops and the logger
// writes JSON to Config.Stderr only. With OTLP logs on, the logger sends every record through the
// otelslog bridge and still writes WARN and above to stderr, so errors survive a Collector outage.
func Start(ctx context.Context, cfg Config) (*Telemetry, error) {
	if cfg.Service == "" {
		return nil, ErrNoService
	}

	cfg = withDefaults(cfg)
	stderr := newStderrHandler(cfg.Stderr, cfg.ProjectID)
	errLog := slog.New(leveled(stderr, slog.LevelWarn))
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		errLog.WarnContext(context.Background(), "opentelemetry error", slog.Any("error", err))
	}))

	tel := &Telemetry{logger: slog.New(leveled(stderr, cfg.Level))}

	sigs, err := enabledSignals()
	if err != nil {
		return nil, err
	}

	if !sigs.any() {
		return tel, nil
	}

	res, err := newResource(ctx, cfg)
	if err != nil {
		// A detector failing (for example, no metadata server) still leaves a usable resource.
		errLog.WarnContext(ctx, "opentelemetry resource detection", slog.Any("error", err))
	}

	if startErr := tel.startProviders(ctx, sigs, res); startErr != nil {
		return nil, errors.Join(startErr, tel.Shutdown(ctx))
	}

	if sigs.logs {
		bridge := otelslog.NewHandler(scope, otelslog.WithLoggerProvider(global.GetLoggerProvider()))
		tel.logger = slog.New(slog.NewMultiHandler(
			leveled(bridge, cfg.Level),
			leveled(stderr, maxLevel(cfg.Level, slog.LevelWarn)),
		))
	}

	return tel, nil
}

// Logger returns the process's logger. Pass it down; nothing in obs is a package-level logger.
func (t *Telemetry) Logger() *slog.Logger {
	return t.logger
}

// Exporting reports whether any signal is exported over OTLP.
func (t *Telemetry) Exporting() bool {
	return t.signals.any()
}

// Shutdown flushes and stops the providers within ctx's deadline. It is safe to call on an inert
// Telemetry and more than once.
func (t *Telemetry) Shutdown(ctx context.Context) error {
	var errs []error
	for _, shutdown := range t.shutdowns {
		errs = append(errs, shutdown(ctx))
	}

	t.shutdowns = nil

	return errors.Join(errs...)
}

func (t *Telemetry) startProviders(ctx context.Context, sigs signals, res *resource.Resource) error {
	t.signals = sigs

	if sigs.traces {
		exp, err := newSpanExporter(ctx)
		if err != nil {
			return err
		}

		opts := []sdktrace.TracerProviderOption{sdktrace.WithBatcher(exp), sdktrace.WithResource(res)}
		if sampler := defaultSampler(res); sampler != nil {
			opts = append(opts, sdktrace.WithSampler(sampler))
		}

		tp := sdktrace.NewTracerProvider(opts...)
		otel.SetTracerProvider(tp)
		t.shutdowns = append(t.shutdowns, tp.Shutdown)
	}

	if sigs.metrics {
		exp, err := newMetricExporter(ctx)
		if err != nil {
			return err
		}

		mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(sdkmetric.NewPeriodicReader(exp)),
			sdkmetric.WithResource(res))
		otel.SetMeterProvider(mp)
		t.shutdowns = append(t.shutdowns, mp.Shutdown)
	}

	if sigs.logs {
		exp, err := newLogExporter(ctx)
		if err != nil {
			return err
		}

		lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewBatchProcessor(exp)),
			sdklog.WithResource(res))
		global.SetLoggerProvider(lp)
		t.shutdowns = append(t.shutdowns, lp.Shutdown)
	}

	return nil
}

func withDefaults(cfg Config) Config {
	if cfg.Environment == "" {
		cfg.Environment = EnvDevelopment
	}

	if cfg.Level == nil {
		cfg.Level = slog.LevelInfo
	}

	if cfg.Stderr == nil {
		cfg.Stderr = os.Stderr
	}

	if cfg.ProjectID == "" {
		cfg.ProjectID = os.Getenv("GOOGLE_CLOUD_PROJECT")
	}

	if cfg.detectors == nil {
		cfg.detectors = []resource.Detector{gcp.NewDetector()}
	}

	return cfg
}

func maxLevel(a, b slog.Leveler) slog.Level {
	return max(a.Level(), b.Level())
}
