package obs_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"

	"github.com/justabill-org/justabill/obs"
)

func TestMainExitCodes(t *testing.T) {
	// Start stays inert without an OTLP endpoint. t.Setenv restores the variables afterwards.
	for _, k := range []string{"OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_ENDPOINT",
		"OTEL_EXPORTER_OTLP_METRICS_ENDPOINT", "OTEL_EXPORTER_OTLP_LOGS_ENDPOINT"} {
		t.Setenv(k, "")
		_ = os.Unsetenv(k)
	}

	var stderr bytes.Buffer

	cfg := obs.Config{Service: "justabill-test", Stderr: &stderr}

	var gotLogger *slog.Logger

	if code := obs.Main(t.Context(), cfg, func(_ context.Context, log *slog.Logger) error {
		gotLogger = log
		log.InfoContext(t.Context(), "ran")

		return nil
	}); code != 0 || gotLogger == nil {
		t.Errorf("Main = %d (logger %v), want 0 with a logger", code, gotLogger)
	}

	code := obs.Main(t.Context(), cfg, func(context.Context, *slog.Logger) error {
		return errors.New("seed: GET /v3/member?api_key=SECRET: 500")
	})
	if code != 1 {
		t.Errorf("Main with a failing run = %d, want 1", code)
	}

	out := stderr.String()
	if !strings.Contains(out, `"message":"ran"`) || !strings.Contains(out, "justabill-test failed") {
		t.Errorf("stderr = %s, want the run's line and the failure", out)
	}

	if strings.Contains(out, "SECRET") {
		t.Errorf("stderr leaks the key: %s", out)
	}

	var bad bytes.Buffer
	code = obs.Main(t.Context(), obs.Config{Stderr: &bad}, func(context.Context, *slog.Logger) error {
		t.Error("ran without telemetry")

		return nil
	})
	if code != 1 || !strings.Contains(bad.String(), "starting telemetry") {
		t.Errorf("Main without a service = %d, %s; want 1 and the error", code, bad.String())
	}
}
