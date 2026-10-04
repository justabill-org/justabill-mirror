package obs_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/log/global"
	lognoop "go.opentelemetry.io/otel/log/noop"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"

	"github.com/justabill-org/justabill/obs"
)

// otelEnv lists every variable these tests read; clearEnv unsets them so the host's don't leak in.
func otelEnv() []string {
	return []string{
		"OTEL_SDK_DISABLED", "OTEL_EXPORTER_OTLP_ENDPOINT", "OTEL_EXPORTER_OTLP_PROTOCOL",
		"OTEL_EXPORTER_OTLP_TRACES_ENDPOINT", "OTEL_EXPORTER_OTLP_METRICS_ENDPOINT",
		"OTEL_EXPORTER_OTLP_LOGS_ENDPOINT", "OTEL_EXPORTER_OTLP_TRACES_PROTOCOL",
		"OTEL_EXPORTER_OTLP_METRICS_PROTOCOL", "OTEL_EXPORTER_OTLP_LOGS_PROTOCOL",
		"OTEL_TRACES_EXPORTER", "OTEL_METRICS_EXPORTER", "OTEL_LOGS_EXPORTER",
		"OTEL_TRACES_SAMPLER", "OTEL_TRACES_SAMPLER_ARG", "OTEL_SERVICE_NAME", "OTEL_RESOURCE_ATTRIBUTES",
		"GOOGLE_CLOUD_PROJECT",
	}
}

// clearEnv unsets the OTel variables (the SDK treats a set but empty OTEL_TRACES_SAMPLER as an
// error) and resets the global providers to no-ops when the test ends. Tests that call it change
// process state and can't run in parallel.
func clearEnv(t *testing.T) {
	t.Helper()

	for _, k := range otelEnv() {
		t.Setenv(k, "") // restores the host's value afterwards
		if err := os.Unsetenv(k); err != nil {
			t.Fatal(err)
		}
	}

	t.Cleanup(func() {
		otel.SetTracerProvider(tracenoop.NewTracerProvider())
		otel.SetMeterProvider(metricnoop.NewMeterProvider())
		global.SetLoggerProvider(lognoop.NewLoggerProvider())
	})
}

func start(t *testing.T, cfg obs.Config) *obs.Telemetry {
	t.Helper()

	tel, err := obs.Start(t.Context(), obs.WithoutDetectors(cfg))
	if err != nil {
		t.Fatalf("Start: %v", err)
	}

	t.Cleanup(func() { _ = tel.Shutdown(context.Background()) })

	return tel
}

func lines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()

	var out []map[string]any
	for line := range strings.Lines(buf.String()) {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("stderr line %q isn't JSON: %v", line, err)
		}

		out = append(out, m)
	}

	return out
}

func TestStartRequiresService(t *testing.T) {
	clearEnv(t)

	if _, err := obs.Start(t.Context(), obs.Config{}); !errors.Is(err, obs.ErrNoService) {
		t.Fatalf("Start without a service: err = %v, want ErrNoService", err)
	}
}

func TestStartWithoutEndpointIsInert(t *testing.T) {
	clearEnv(t)

	var buf bytes.Buffer
	tel := start(t, obs.Config{Service: "justabill-test", Stderr: &buf, ProjectID: "proj"})

	if tel.Exporting() {
		t.Error("Exporting() = true without an endpoint")
	}

	if _, ok := otel.GetTracerProvider().(*sdktrace.TracerProvider); ok {
		t.Error("an SDK tracer provider was installed without an endpoint")
	}

	// A span from a local provider, to check the stderr line's trace correlation.
	tp := sdktrace.NewTracerProvider()
	ctx, span := tp.Tracer("test").Start(t.Context(), "op")
	tel.Logger().InfoContext(ctx, "hello", slog.String("bill", "hr-119-1"))
	tel.Logger().Debug("dropped at the default level")
	span.End()

	got := lines(t, &buf)
	if len(got) != 1 {
		t.Fatalf("stderr has %d lines, want 1: %s", len(got), buf.String())
	}

	sc := span.SpanContext()
	want := map[string]any{
		"severity":                             "INFO",
		"message":                              "hello",
		"bill":                                 "hr-119-1",
		"logging.googleapis.com/trace":         "projects/proj/traces/" + sc.TraceID().String(),
		"logging.googleapis.com/spanId":        sc.SpanID().String(),
		"logging.googleapis.com/trace_sampled": true,
	}
	for k, v := range want {
		if got[0][k] != v {
			t.Errorf("stderr %s = %v, want %v", k, got[0][k], v)
		}
	}

	if err := tel.Shutdown(t.Context()); err != nil {
		t.Errorf("Shutdown of an inert Telemetry: %v", err)
	}
}

func TestSeverityNames(t *testing.T) {
	clearEnv(t)

	var buf bytes.Buffer
	tel := start(t, obs.Config{Service: "justabill-test", Stderr: &buf, Level: slog.LevelDebug})
	log := tel.Logger().With(slog.String("service", "test")).WithGroup("g")
	log.Debug("d")
	log.Info("i")
	log.Warn("w", slog.String("level", "kept"))
	log.Error("e")

	got := lines(t, &buf)
	want := []string{"DEBUG", "INFO", "WARNING", "ERROR"}
	if len(got) != len(want) {
		t.Fatalf("got %d lines, want %d", len(got), len(want))
	}

	for i, sev := range want {
		if got[i]["severity"] != sev {
			t.Errorf("line %d severity = %v, want %s", i, got[i]["severity"], sev)
		}
	}

	if got[0]["service"] != "test" {
		t.Errorf("line 0 service = %v, want the With attribute", got[0]["service"])
	}

	// Attributes inside a group keep their own names.
	if g, _ := got[2]["g"].(map[string]any); g["level"] != "kept" {
		t.Errorf("grouped attribute = %v, want g.level=kept", got[2]["g"])
	}
}

func TestTraceFieldsStayAtRootInGroups(t *testing.T) {
	clearEnv(t)

	var buf bytes.Buffer
	tel := start(t, obs.Config{Service: "justabill-test", Stderr: &buf, ProjectID: "proj"})

	tp := sdktrace.NewTracerProvider()
	ctx, span := tp.Tracer("test").Start(t.Context(), "op")
	log := tel.Logger().With(slog.String("service", "test")).WithGroup("g").With(slog.String("in", "g"))
	log.InfoContext(ctx, "grouped", slog.String("bill", "hr-119-1"))
	log.WithGroup("h").InfoContext(ctx, "nested")
	span.End()

	got := lines(t, &buf)
	if len(got) != 2 {
		t.Fatalf("stderr has %d lines, want 2: %s", len(got), buf.String())
	}

	sc := span.SpanContext()
	for i, line := range got {
		if line["logging.googleapis.com/trace"] != "projects/proj/traces/"+sc.TraceID().String() {
			t.Errorf("line %d trace = %v, want it at the top level", i, line["logging.googleapis.com/trace"])
		}

		if line["logging.googleapis.com/spanId"] != sc.SpanID().String() {
			t.Errorf("line %d spanId = %v, want it at the top level", i, line["logging.googleapis.com/spanId"])
		}

		if line["service"] != "test" {
			t.Errorf("line %d service = %v, want the ungrouped With attribute", i, line["service"])
		}
	}

	g, _ := got[0]["g"].(map[string]any)
	if g["in"] != "g" || g["bill"] != "hr-119-1" {
		t.Errorf("group g = %v, want in=g and bill=hr-119-1", got[0]["g"])
	}

	if _, ok := g["logging.googleapis.com/trace"]; ok {
		t.Errorf("group g repeats the trace field: %v", g)
	}

	if nested, _ := got[1]["g"].(map[string]any); nested["in"] != "g" {
		t.Errorf("nested line group g = %v, want in=g", got[1]["g"])
	}
}

func TestSDKDisabled(t *testing.T) {
	clearEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
	t.Setenv("OTEL_SDK_DISABLED", "true")

	if tel := start(t, obs.Config{Service: "justabill-test", Stderr: io.Discard}); tel.Exporting() {
		t.Error("Exporting() = true with OTEL_SDK_DISABLED=true")
	}
}

func TestUnsupportedConfig(t *testing.T) {
	tests := []struct{ key, value string }{
		{"OTEL_TRACES_EXPORTER", "console"},
		{"OTEL_EXPORTER_OTLP_PROTOCOL", "http/json"},
	}
	for _, tt := range tests {
		t.Run(tt.key, func(t *testing.T) {
			clearEnv(t)
			t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
			t.Setenv(tt.key, tt.value)

			cfg := obs.WithoutDetectors(obs.Config{Service: "justabill-test", Stderr: io.Discard})
			if _, err := obs.Start(t.Context(), cfg); err == nil {
				t.Errorf("Start with %s=%s: no error", tt.key, tt.value)
			}
		})
	}
}

// collector is a fake OTLP/HTTP receiver that counts requests per path.
type collector struct {
	mu    sync.Mutex
	paths map[string]int
}

func newCollector(t *testing.T) (*collector, string) {
	t.Helper()

	c := &collector{paths: map[string]int{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		c.mu.Lock()
		c.paths[r.URL.Path]++
		c.mu.Unlock()
		w.Header().Set("Content-Type", "application/x-protobuf")
	}))
	t.Cleanup(srv.Close)

	return c, srv.URL
}

func (c *collector) count(path string) int {
	c.mu.Lock()
	defer c.mu.Unlock()

	return c.paths[path]
}

func TestExportsAllSignalsAndFlushesOnShutdown(t *testing.T) {
	clearEnv(t)

	c, url := newCollector(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", url)

	var buf bytes.Buffer
	tel := start(t, obs.Config{Service: "justabill-test", Stderr: &buf})
	if !tel.Exporting() {
		t.Fatal("Exporting() = false with an endpoint")
	}

	ctx, span := otel.Tracer("test").Start(t.Context(), "op")
	counter, cerr := otel.Meter("test").Int64Counter("test.count")
	if cerr != nil {
		t.Fatal(cerr)
	}

	counter.Add(ctx, 1)
	tel.Logger().InfoContext(ctx, "only over OTLP")
	tel.Logger().WarnContext(ctx, "over OTLP and stderr")
	span.End()

	ctx, cancel := context.WithTimeout(t.Context(), obs.ShutdownTimeout)
	defer cancel()

	if err := tel.Shutdown(ctx); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	for _, path := range []string{"/v1/traces", "/v1/metrics", "/v1/logs"} {
		if c.count(path) == 0 {
			t.Errorf("nothing was exported to %s by Shutdown", path)
		}
	}

	got := lines(t, &buf)
	if len(got) != 1 || got[0]["message"] != "over OTLP and stderr" {
		t.Errorf("stderr with OTLP logs on should keep WARN and above only, got %s", buf.String())
	}

	if err := tel.Shutdown(t.Context()); err != nil {
		t.Errorf("second Shutdown: %v", err)
	}
}

func TestSignalExporterNone(t *testing.T) {
	clearEnv(t)

	c, url := newCollector(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", url)
	t.Setenv("OTEL_TRACES_EXPORTER", "none")
	t.Setenv("OTEL_LOGS_EXPORTER", "none")

	var buf bytes.Buffer
	tel := start(t, obs.Config{Service: "justabill-test", Stderr: &buf})

	_, span := otel.Tracer("test").Start(t.Context(), "op")
	span.End()
	tel.Logger().Info("stderr only")

	if err := tel.Shutdown(t.Context()); err != nil {
		t.Fatalf("Shutdown: %v", err)
	}

	if c.count("/v1/traces") != 0 || c.count("/v1/logs") != 0 {
		t.Errorf("traces or logs exported with their exporter set to none: %v", c.paths)
	}

	// With OTLP logs off, stderr is the only log output and keeps the configured level.
	if got := lines(t, &buf); len(got) != 1 {
		t.Errorf("stderr = %s, want the INFO line", buf.String())
	}
}

func TestErrorHandlerLogsToStderr(t *testing.T) {
	clearEnv(t)

	var buf bytes.Buffer
	start(t, obs.Config{Service: "justabill-test", Stderr: &buf})
	otel.Handle(errors.New("export failed"))

	got := lines(t, &buf)
	if len(got) != 1 || got[0]["severity"] != "WARNING" || got[0]["error"] != "export failed" {
		t.Errorf("stderr = %s, want one WARNING with the error", buf.String())
	}
}

func TestShutdownRespectsDeadline(t *testing.T) {
	clearEnv(t)

	// Nothing listens here, so flushing can only end by the deadline.
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
	tel := start(t, obs.Config{Service: "justabill-test", Stderr: io.Discard})

	_, span := otel.Tracer("test").Start(t.Context(), "op")
	span.End()

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	began := time.Now()
	_ = tel.Shutdown(ctx)

	if elapsed := time.Since(began); elapsed > 2*time.Second {
		t.Errorf("Shutdown took %v with a 200ms deadline", elapsed)
	}
}

func TestGRPCProtocol(t *testing.T) {
	clearEnv(t)
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", "http://127.0.0.1:1")
	t.Setenv("OTEL_EXPORTER_OTLP_PROTOCOL", "grpc")

	tel := start(t, obs.Config{Service: "justabill-test", Stderr: io.Discard})
	if !tel.Exporting() {
		t.Fatal("Exporting() = false over gRPC")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 200*time.Millisecond)
	defer cancel()

	_ = tel.Shutdown(ctx)
}
