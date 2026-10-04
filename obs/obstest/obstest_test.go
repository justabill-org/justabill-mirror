package obstest_test

import (
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/justabill-org/justabill/obs/obstest"
)

func TestRecordsSpans(t *testing.T) {
	tel := obstest.New(t)

	_, span := otel.Tracer("test").Start(t.Context(), "op")
	span.End()

	if spans := tel.Ended(); len(spans) != 1 || spans[0].Name() != "op" {
		t.Errorf("Ended() = %v, want one span named op", spans)
	}
}

func TestRecordsMetrics(t *testing.T) {
	tel := obstest.New(t)

	counter, err := otel.Meter("test").Int64Counter("test.count")
	if err != nil {
		t.Fatal(err)
	}

	counter.Add(t.Context(), 2)

	m, found := tel.Metric(t, "test.count")
	if !found {
		t.Fatal("test.count wasn't recorded")
	}

	if sum, _ := m.Data.(metricdata.Sum[int64]); len(sum.DataPoints) != 1 || sum.DataPoints[0].Value != 2 {
		t.Errorf("test.count = %+v, want 2", m.Data)
	}

	if _, missing := tel.Metric(t, "missing"); missing {
		t.Error("Metric(missing) reported a metric")
	}
}

func TestRecordsLogsWithTraceContext(t *testing.T) {
	tel := obstest.New(t)

	ctx, span := otel.Tracer("test").Start(t.Context(), "op")
	tel.Logger.InfoContext(ctx, "hello")
	span.End()

	logs := tel.Logs()
	if len(logs) != 1 || logs[0].Body().AsString() != "hello" {
		t.Fatalf("Logs() = %v, want one record with body hello", logs)
	}

	if logs[0].TraceID() != span.SpanContext().TraceID() {
		t.Error("the log record isn't correlated with the active span")
	}
}
