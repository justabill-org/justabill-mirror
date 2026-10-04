package obs

import (
	"context"
	"log/slog"
	"time"
)

// Main runs a command-line program with telemetry and returns its exit code, to pass to
// [os.Exit]. It calls [Start] with cfg, runs fn with the process's logger,
// logs fn's error at ERROR, and flushes within [ShutdownTimeout] before it returns: 0 when fn
// succeeded, 1 when Start or fn failed.
func Main(ctx context.Context, cfg Config, fn func(context.Context, *slog.Logger) error) int {
	tel, err := Start(ctx, cfg)
	if err != nil {
		// Start failed before it made the process's logger, so this one writes to stderr alone.
		cfg = withDefaults(cfg)
		slog.New(newStderrHandler(cfg.Stderr, cfg.ProjectID)).ErrorContext(ctx, "starting telemetry",
			slog.String("error", err.Error()))

		return 1
	}
	defer tel.ShutdownWithin(ShutdownTimeout)

	if err = fn(ctx, tel.Logger()); err != nil {
		tel.Logger().ErrorContext(ctx, cfg.Service+" failed", slog.String("error", Redact(err.Error())))

		return 1
	}

	return 0
}

// ShutdownWithin is [Telemetry.Shutdown] with a timeout of d, logging a failure instead of
// returning it: a deferred call at the end of a command or server.
func (t *Telemetry) ShutdownWithin(d time.Duration) {
	ctx, cancel := context.WithTimeout(context.Background(), d)
	defer cancel()

	if err := t.Shutdown(ctx); err != nil {
		t.logger.WarnContext(ctx, "flushing telemetry", slog.String("error", err.Error()))
	}
}
