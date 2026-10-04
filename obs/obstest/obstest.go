// Package obstest records telemetry in memory for tests. New installs recording tracer, meter and
// logger providers as the OpenTelemetry globals for the rest of the test and restores the previous
// ones afterwards, so tests that use it must not run in parallel with each other.
package obstest

import (
	"context"
	"log/slog"
	"sync"
	"testing"

	"go.opentelemetry.io/contrib/bridges/otelslog"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/log/global"
	lognoop "go.opentelemetry.io/otel/log/noop"
	metricnoop "go.opentelemetry.io/otel/metric/noop"
	"go.opentelemetry.io/otel/propagation"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

// scope is the instrumentation scope of Logger, the same as obs's slog bridge.
const scope = "github.com/justabill-org/justabill/obs"

// Telemetry holds what the code under test recorded.
type Telemetry struct {
	// Spans records every ended span; every span is sampled.
	Spans *tracetest.SpanRecorder
	// Reader collects metrics on demand; see Metrics.
	Reader *sdkmetric.ManualReader
	// Logger writes through the OpenTelemetry slog bridge into Logs, as obs's logger does.
	Logger *slog.Logger

	logs *recorder
}

// New installs the recording providers and the W3C propagators until the test ends, then resets
// the providers to no-ops and restores the previous propagator.
func New(t testing.TB) *Telemetry {
	t.Helper()

	prevProp := otel.GetTextMapPropagator()

	tel := &Telemetry{
		Spans:  tracetest.NewSpanRecorder(),
		Reader: sdkmetric.NewManualReader(),
		logs:   &recorder{},
	}

	tp := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(tel.Spans))
	mp := sdkmetric.NewMeterProvider(sdkmetric.WithReader(tel.Reader))
	lp := sdklog.NewLoggerProvider(sdklog.WithProcessor(sdklog.NewSimpleProcessor(tel.logs)))

	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	global.SetLoggerProvider(lp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, propagation.Baggage{}))

	tel.Logger = otelslog.NewLogger(scope, otelslog.WithLoggerProvider(lp))

	t.Cleanup(func() {
		ctx := context.Background()
		_ = tp.Shutdown(ctx)
		_ = mp.Shutdown(ctx)
		_ = lp.Shutdown(ctx)

		// The SDK can't hand the globals back to their initial delegating state, and setting
		// them to it logs an error, so they end as no-ops.
		otel.SetTracerProvider(tracenoop.NewTracerProvider())
		otel.SetMeterProvider(metricnoop.NewMeterProvider())
		global.SetLoggerProvider(lognoop.NewLoggerProvider())
		otel.SetTextMapPropagator(prevProp)
	})

	return tel
}

// Ended returns the spans that have ended, oldest first.
func (tel *Telemetry) Ended() []sdktrace.ReadOnlySpan {
	return tel.Spans.Ended()
}

// Metrics collects every metric recorded so far.
func (tel *Telemetry) Metrics(t testing.TB) metricdata.ResourceMetrics {
	t.Helper()

	var rm metricdata.ResourceMetrics
	if err := tel.Reader.Collect(t.Context(), &rm); err != nil {
		t.Fatalf("collect metrics: %v", err)
	}

	return rm
}

// Metric returns the named metric, or false when nothing recorded it.
func (tel *Telemetry) Metric(t testing.TB, name string) (metricdata.Metrics, bool) {
	t.Helper()

	for _, sm := range tel.Metrics(t).ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == name {
				return m, true
			}
		}
	}

	return metricdata.Metrics{}, false
}

// Logs returns the log records emitted so far, oldest first.
func (tel *Telemetry) Logs() []sdklog.Record {
	return tel.logs.all()
}

// recorder is an in-memory log exporter.
type recorder struct {
	mu      sync.Mutex
	records []sdklog.Record
}

func (r *recorder) Export(_ context.Context, records []sdklog.Record) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for i := range records {
		r.records = append(r.records, records[i].Clone())
	}

	return nil
}

func (*recorder) Shutdown(context.Context) error { return nil }

func (*recorder) ForceFlush(context.Context) error { return nil }

func (r *recorder) all() []sdklog.Record {
	r.mu.Lock()
	defer r.mu.Unlock()

	return append([]sdklog.Record(nil), r.records...)
}
