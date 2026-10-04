package main

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/justabill-org/justabill/api/internal/handler"
	"github.com/justabill-org/justabill/db/fixture"
	"github.com/justabill-org/justabill/db/spannerdb"
	"github.com/justabill-org/justabill/db/testdb"
	"github.com/justabill-org/justabill/obs/obstest"
)

const billRoute = "/api/v1/bills/{id}"

// telemetryRouter is the public router with no repositories, logging to tel.
// Build it after obstest.New: the HTTP middleware takes the providers then.
func telemetryRouter(t *testing.T, tel *obstest.Telemetry, opts ...handler.Option) http.Handler {
	t.Helper()
	h := handler.New(nil, opts...)
	h.SetLogger(tel.Logger)
	return buildRouter(h, tel.Logger, nil, devEdge(t, 0))
}

// serverSpan returns the one SERVER span recorded.
func serverSpan(t *testing.T, spans []sdktrace.ReadOnlySpan) sdktrace.ReadOnlySpan {
	t.Helper()
	var found []sdktrace.ReadOnlySpan
	for _, s := range spans {
		if s.SpanKind() == trace.SpanKindServer {
			found = append(found, s)
		}
	}
	if len(found) != 1 {
		t.Fatalf("got %d server spans, want 1", len(found))
	}
	return found[0]
}

func attrs(kvs []attribute.KeyValue) map[attribute.Key]attribute.Value {
	m := map[attribute.Key]attribute.Value{}
	for _, kv := range kvs {
		m[kv.Key] = kv.Value
	}
	return m
}

// The request log line is written inside the request span, so it carries its
// trace and span IDs, next to the request ID.
func TestRouterRequestLogCarriesTraceAndRequestID(t *testing.T) {
	tel := obstest.New(t)
	r := telemetryRouter(t, tel)

	r.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/api/v1/not-a-route", nil))

	span := serverSpan(t, tel.Ended())
	var found bool
	for _, rec := range tel.Logs() {
		if rec.Body().AsString() != "request" {
			continue
		}
		found = true
		if rec.TraceID() != span.SpanContext().TraceID() || rec.SpanID() != span.SpanContext().SpanID() {
			t.Error("the request log record isn't linked to the server span")
		}
		var requestID string
		rec.WalkAttributes(func(kv attribute.KeyValue) bool {
			if kv.Key == "request_id" {
				requestID = kv.Value.AsString()
			}
			return true
		})
		if requestID == "" {
			t.Error("the request log record has no request_id")
		}
	}
	if !found {
		t.Fatal("no request log record")
	}
}

// A panicking handler becomes a 500 with an exception record, and the server
// span is an error named by its route.
func TestRouterRecoversPanics(t *testing.T) {
	tel := obstest.New(t)
	h := handler.New(nil)
	h.SetLogger(slog.New(slog.DiscardHandler))
	r := buildRouter(h, tel.Logger, nil, devEdge(t, 0))
	r.Get("/boom/{id}", func(http.ResponseWriter, *http.Request) { panic("boom") })

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/boom/1", nil))
	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}

	span := serverSpan(t, tel.Ended())
	if span.Name() != "GET /boom/{id}" || span.Status().Code != codes.Error ||
		attrs(span.Attributes())["error.type"].AsString() != "500" {
		t.Errorf("span %q %v %v, want an error span named by route", span.Name(), span.Status(), span.Attributes())
	}

	var exceptions int
	for _, rec := range tel.Logs() {
		if rec.Body().AsString() == "exception" {
			exceptions++
		}
	}
	if exceptions != 1 {
		t.Errorf("got %d exception records, want 1", exceptions)
	}
}

// An incoming sampled traceparent makes the server span its child; a 4xx isn't
// an error.
func TestRouterContinuesBrowserTrace(t *testing.T) {
	tel := obstest.New(t)
	r := telemetryRouter(t, tel)

	const traceID, parentID = "4bf92f3577b34da6a3ce929d0e0e4736", "00f067aa0ba902b7"
	req := httptest.NewRequest(http.MethodGet, "/api/v1/not-a-route", nil)
	req.Header.Set("Traceparent", "00-"+traceID+"-"+parentID+"-01")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)
	if rr.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", rr.Code)
	}

	span := serverSpan(t, tel.Ended())
	if span.SpanContext().TraceID().String() != traceID || span.Parent().SpanID().String() != parentID {
		t.Errorf("span %v with parent %v, want a child of the incoming traceparent", span.SpanContext(), span.Parent())
	}
	if span.Status().Code != codes.Unset {
		t.Errorf("span status %v, want no error for a 404", span.Status())
	}
}

// Against the emulator, GET /api/v1/bills/{id} is one server span named by
// route, one http.server.request.duration point without the ID, and Spanner
// client spans in the same trace under it.
func TestRouterTracesSpannerUnderRequest(t *testing.T) {
	tel := obstest.New(t)
	client := testdb.New(t) // after obstest.New: the gRPC client takes the tracer provider when it dials
	testdb.SeedFixture(t.Context(), t, client)
	sc := &spannerdb.Client{Spanner: client}
	r := telemetryRouter(t, tel, handler.WithBills(spannerdb.NewBillRepo(sc)),
		handler.WithVotes(spannerdb.NewVoteRepo(sc)))

	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/bills/"+fixture.HouseBill, nil))
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d: %s", rr.Code, rr.Body)
	}

	spans := tel.Ended()
	server := serverSpan(t, spans)
	a := attrs(server.Attributes())
	if server.Name() != "GET "+billRoute || a["http.route"].AsString() != billRoute ||
		a["http.request.method"].AsString() != http.MethodGet || a["http.response.status_code"].AsInt64() != 200 {
		t.Errorf("server span %q %v", server.Name(), server.Attributes())
	}

	if n, names := spannerClientSpansUnder(spans, server); n == 0 {
		t.Errorf("no Spanner client span under the request span; spans: %v", names)
	}

	m, ok := tel.Metric(t, "http.server.request.duration")
	if !ok {
		t.Fatal("no http.server.request.duration")
	}
	for _, kv := range attrsOfMetric(t, m) {
		if strings.Contains(kv.Value.String(), fixture.HouseBill) {
			t.Errorf("metric attribute %s = %q holds the bill ID", kv.Key, kv.Value.String())
		}
	}
}

// attrsOfMetric returns the attributes of every point of a histogram.
func attrsOfMetric(t *testing.T, m metricdata.Metrics) []attribute.KeyValue {
	t.Helper()
	h, ok := m.Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("%s is %T, want a histogram", m.Name, m.Data)
	}
	var out []attribute.KeyValue
	for _, p := range h.DataPoints {
		out = append(out, p.Attributes.ToSlice()...)
	}
	return out
}

// spannerClientSpansUnder counts the Spanner gRPC client spans that descend
// from server, and lists every span for the failure message.
func spannerClientSpansUnder(spans []sdktrace.ReadOnlySpan, server sdktrace.ReadOnlySpan) (int, []string) {
	byID := map[trace.SpanID]sdktrace.ReadOnlySpan{}
	for _, s := range spans {
		byID[s.SpanContext().SpanID()] = s
	}
	under := func(s sdktrace.ReadOnlySpan) bool {
		for p := s.Parent(); p.IsValid(); {
			if p.SpanID() == server.SpanContext().SpanID() {
				return true
			}
			parent, ok := byID[p.SpanID()]
			if !ok {
				return false
			}
			p = parent.Parent()
		}
		return false
	}
	n := 0
	names := make([]string, 0, len(spans))
	for _, s := range spans {
		names = append(names, s.Name()+" ("+s.SpanKind().String()+")")
		if s.SpanKind() == trace.SpanKindClient && strings.Contains(s.Name(), "Spanner") && under(s) {
			n++
		}
	}
	return n, names
}
