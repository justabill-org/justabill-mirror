package obs_test

import (
	"crypto/tls"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	otellog "go.opentelemetry.io/otel/log"
	sdklog "go.opentelemetry.io/otel/sdk/log"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/justabill-org/justabill/obs"
	"github.com/justabill-org/justabill/obs/obstest"
)

const billRoute = "/api/v1/bills/{id}"

// fixedRoute is a Route that always matched pattern.
func fixedRoute(pattern string) obs.Route {
	return func(*http.Request) string { return pattern }
}

// serve runs one request through HTTPHandler(route) around h and returns the response.
func serve(route obs.Route, h http.HandlerFunc, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	obs.HTTPHandler(route)(h).ServeHTTP(rec, r)

	return rec
}

// onlySpan returns the one span recorded.
func onlySpan(t *testing.T, tel *obstest.Telemetry) sdktrace.ReadOnlySpan {
	t.Helper()

	spans := tel.Ended()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}

	return spans[0]
}

func attrMap(kvs []attribute.KeyValue) map[attribute.Key]attribute.Value {
	m := make(map[attribute.Key]attribute.Value, len(kvs))
	for _, kv := range kvs {
		m[kv.Key] = kv.Value
	}

	return m
}

// wantAttrs checks that kvs holds exactly want, compared as strings.
func wantAttrs(t *testing.T, what string, kvs []attribute.KeyValue, want map[string]string) {
	t.Helper()

	got := attrMap(kvs)
	if len(got) != len(want) {
		t.Errorf("%s attributes = %v, want exactly %v", what, kvs, want)
	}

	for k, v := range want {
		if g, ok := got[attribute.Key(k)]; !ok || g.String() != v {
			t.Errorf("%s %s = %q, want %q", what, k, g.String(), v)
		}
	}
}

// onlyPoint returns the one histogram data point of the named metric.
func onlyPoint(t *testing.T, tel *obstest.Telemetry, name string) metricdata.HistogramDataPoint[float64] {
	t.Helper()

	m, ok := tel.Metric(t, name)
	if !ok {
		t.Fatalf("no %s metric", name)
	}

	h, ok := m.Data.(metricdata.Histogram[float64])
	if !ok || len(h.DataPoints) != 1 {
		t.Fatalf("%s = %#v, want one histogram point", name, m.Data)
	}

	if m.Unit != "s" {
		t.Errorf("%s unit = %q, want s", name, m.Unit)
	}

	return h.DataPoints[0]
}

func TestHTTPHandlerNamesSpanAndMetricByRoute(t *testing.T) {
	tel := obstest.New(t)

	r := httptest.NewRequest(http.MethodGet, "/api/v1/bills/hr1-119?address=1+Main+St", nil)
	r.Header.Set("User-Agent", "test-agent")
	r.Header.Set("X-Forwarded-For", "203.0.113.9")
	r.RemoteAddr = "198.51.100.7:1234"

	rec := serve(fixedRoute(billRoute), func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("{}"))
	}, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d", rec.Code)
	}

	span := onlySpan(t, tel)
	if span.Name() != "GET "+billRoute || span.SpanKind() != trace.SpanKindServer {
		t.Errorf("span = %q (%v), want GET %s (server)", span.Name(), span.SpanKind(), billRoute)
	}

	if span.Status().Code != codes.Unset {
		t.Errorf("status = %v, want unset", span.Status())
	}

	// Exactly these: no url.path, url.query, client.address, user agent or headers.
	want := map[string]string{
		"http.request.method":       "GET",
		"url.scheme":                "http",
		"http.route":                billRoute,
		"http.response.status_code": "200",
	}
	wantAttrs(t, "span", span.Attributes(), want)

	point := onlyPoint(t, tel, "http.server.request.duration")
	wantAttrs(t, "metric", point.Attributes.ToSlice(), want)

	if point.Count != 1 {
		t.Errorf("count = %d, want 1", point.Count)
	}
}

func TestHTTPHandlerStatus(t *testing.T) {
	tests := []struct {
		status    int
		wantCode  codes.Code
		wantError string
	}{
		{http.StatusNotFound, codes.Unset, ""},
		{http.StatusTooManyRequests, codes.Unset, ""},
		{http.StatusInternalServerError, codes.Error, "500"},
		{http.StatusServiceUnavailable, codes.Error, "503"},
	}

	for _, tt := range tests {
		t.Run(http.StatusText(tt.status), func(t *testing.T) {
			tel := obstest.New(t)

			serve(fixedRoute(billRoute), func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				w.WriteHeader(http.StatusOK) // superfluous; the first status counts
			}, httptest.NewRequest(http.MethodGet, "/api/v1/bills/x", nil))

			span := onlySpan(t, tel)
			if span.Status().Code != tt.wantCode {
				t.Errorf("span status = %v, want %v", span.Status().Code, tt.wantCode)
			}

			attrs := attrMap(span.Attributes())
			if got := attrs["error.type"].String(); got != tt.wantError {
				t.Errorf("error.type = %q, want %q", got, tt.wantError)
			}

			if got := attrs["http.response.status_code"].AsInt64(); got != int64(tt.status) {
				t.Errorf("status code = %d, want %d", got, tt.status)
			}

			point := onlyPoint(t, tel, "http.server.request.duration")
			if got, _ := point.Attributes.Value("error.type"); got.String() != tt.wantError {
				t.Errorf("metric error.type = %q, want %q", got.String(), tt.wantError)
			}
		})
	}
}

func TestHTTPHandlerWithoutRoute(t *testing.T) {
	for name, route := range map[string]obs.Route{"nil": nil, "unmatched": fixedRoute("")} {
		t.Run(name, func(t *testing.T) {
			tel := obstest.New(t)

			r := httptest.NewRequest(http.MethodOptions, "/nowhere", nil)
			r.TLS = &tls.ConnectionState{}
			serve(route, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			}, r)

			span := onlySpan(t, tel)
			if span.Name() != http.MethodOptions {
				t.Errorf("name = %q, want OPTIONS", span.Name())
			}

			wantAttrs(t, "span", span.Attributes(), map[string]string{
				"http.request.method":       "OPTIONS",
				"url.scheme":                "https",
				"http.response.status_code": "204",
			})
		})
	}
}

func TestHTTPHandlerUnknownMethod(t *testing.T) {
	tel := obstest.New(t)

	serve(fixedRoute(billRoute), func(http.ResponseWriter, *http.Request) {},
		httptest.NewRequest("PROPFIND", "/api/v1/bills/x", nil))

	span := onlySpan(t, tel)
	if span.Name() != "HTTP "+billRoute {
		t.Errorf("name = %q, want HTTP %s", span.Name(), billRoute)
	}

	attrs := attrMap(span.Attributes())
	if attrs["http.request.method"].AsString() != "_OTHER" ||
		attrs["http.request.method_original"].AsString() != "PROPFIND" {
		t.Errorf("attributes = %v, want _OTHER with the original method", span.Attributes())
	}
}

func TestHTTPHandlerContinuesIncomingTrace(t *testing.T) {
	tel := obstest.New(t)

	const (
		traceID = "4bf92f3577b34da6a3ce929d0e0e4736"
		spanID  = "00f067aa0ba902b7"
	)

	r := httptest.NewRequest(http.MethodGet, "/api/v1/bills/x", nil)
	r.Header.Set("Traceparent", "00-"+traceID+"-"+spanID+"-01")

	var inner trace.SpanContext

	serve(fixedRoute(billRoute), func(_ http.ResponseWriter, r *http.Request) {
		inner = trace.SpanContextFromContext(r.Context())
	}, r)

	span := onlySpan(t, tel)
	if span.SpanContext().TraceID().String() != traceID || span.Parent().SpanID().String() != spanID ||
		!span.Parent().IsRemote() {
		t.Errorf("span %v has parent %v, want a child of %s/%s", span.SpanContext(), span.Parent(), traceID, spanID)
	}

	if inner.SpanID() != span.SpanContext().SpanID() {
		t.Error("the handler's context doesn't hold the server span")
	}
}

func TestHTTPHandlerUnwrapAndImplicitStatus(t *testing.T) {
	tel := obstest.New(t)

	rec := serve(fixedRoute(billRoute), func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("partial"))
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("flush through the wrapper: %v", err)
		}
	}, httptest.NewRequest(http.MethodGet, "/api/v1/bills/x", nil))

	if !rec.Flushed {
		t.Error("the recorder wasn't flushed")
	}

	if got := attrMap(onlySpan(t, tel).Attributes())["http.response.status_code"].AsInt64(); got != 200 {
		t.Errorf("status code = %d, want 200", got)
	}
}

func TestHTTPHandlerEndsSpanOnUnrecoveredPanic(t *testing.T) {
	tel := obstest.New(t)

	defer func() {
		if p := recover(); p != "boom" {
			t.Errorf("recovered %v, want the panic to go on up", p)
		}

		span := onlySpan(t, tel)
		if span.Status().Code != codes.Error || attrMap(span.Attributes())["error.type"].String() != "500" {
			t.Errorf("span = %v %v, want an error with error.type 500", span.Status(), span.Attributes())
		}
	}()

	serve(fixedRoute(billRoute), func(http.ResponseWriter, *http.Request) { panic("boom") },
		httptest.NewRequest(http.MethodGet, "/api/v1/bills/x", nil))
}

func TestHTTPHandlerAbortIsNotAServerError(t *testing.T) {
	tel := obstest.New(t)

	defer func() {
		if p := recover(); p != http.ErrAbortHandler { //nolint:errorlint // the exact value re-panicked
			t.Errorf("recovered %v, want http.ErrAbortHandler", p)
		}

		span := onlySpan(t, tel)
		attrs := attrMap(span.Attributes())
		if span.Status().Code == codes.Error || attrs["http.response.status_code"].AsInt64() != 200 {
			t.Errorf("span = %v %v, want no error and the 200 the handler wrote", span.Status(), span.Attributes())
		}

		if _, ok := attrs["error.type"]; ok {
			t.Errorf("aborted request has error.type %v", attrs["error.type"])
		}
	}()

	serve(fixedRoute(billRoute), func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("partial"))
		panic(http.ErrAbortHandler)
	}, httptest.NewRequest(http.MethodGet, "/api/v1/bills/x", nil))
}

// recovering serves r through HTTPHandler and RecoverHandler around h.
func recovering(tel *obstest.Telemetry, h http.HandlerFunc, r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	route := fixedRoute(billRoute)
	obs.HTTPHandler(route)(obs.RecoverHandler(tel.Logger, route)(h)).ServeHTTP(rec, r)

	return rec
}

func logAttrs(r *sdklog.Record) map[string]string {
	m := map[string]string{}
	r.WalkAttributes(func(kv attribute.KeyValue) bool {
		m[string(kv.Key)] = kv.Value.String()
		return true
	})

	return m
}

func TestRecoverHandlerLogsExceptionAndAnswers500(t *testing.T) {
	tel := obstest.New(t)

	rec := recovering(tel, func(http.ResponseWriter, *http.Request) {
		panic("lookup failed for jane@example.com at 192.0.2.1")
	}, httptest.NewRequest(http.MethodGet, "/api/v1/bills/x", nil))

	if rec.Code != http.StatusInternalServerError {
		t.Errorf("status = %d, want 500", rec.Code)
	}

	logs := tel.Logs()
	if len(logs) != 1 {
		t.Fatalf("got %d log records, want 1", len(logs))
	}

	record := logs[0]
	if record.Body().AsString() != "exception" || record.Severity() != otellog.SeverityError {
		t.Errorf("record = %q at %v, want exception at ERROR", record.Body().AsString(), record.Severity())
	}

	span := onlySpan(t, tel)
	if record.TraceID() != span.SpanContext().TraceID() || record.SpanID() != span.SpanContext().SpanID() {
		t.Error("the exception record isn't linked to the request span")
	}

	attrs := logAttrs(&record)
	if attrs["exception.type"] != "string" || attrs["http.route"] != billRoute {
		t.Errorf("attributes = %v", attrs)
	}

	if want := "lookup failed for [redacted] at [redacted]"; attrs["exception.message"] != want {
		t.Errorf("exception.message = %q, want %q", attrs["exception.message"], want)
	}

	if !strings.Contains(attrs["exception.stacktrace"], "http_test.go") {
		t.Errorf("stack trace doesn't reach the panicking handler:\n%s", attrs["exception.stacktrace"])
	}

	if span.Status().Code != codes.Error || attrMap(span.Attributes())["error.type"].String() != "500" {
		t.Errorf("span = %v %v, want an error with error.type 500", span.Status(), span.Attributes())
	}

	events := span.Events()
	if len(events) != 1 || events[0].Name != "exception" ||
		attrMap(events[0].Attributes)["exception.message"].AsString() != attrs["exception.message"] {
		t.Errorf("span events = %v, want one exception event", events)
	}
}

// panicError is an error type for exception.type.
type panicError struct{}

func (panicError) Error() string { return "typed" }

func TestRecoverHandlerErrorTypeAndWrittenStatus(t *testing.T) {
	tel := obstest.New(t)

	rec := recovering(tel, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		panic(panicError{})
	}, httptest.NewRequest(http.MethodGet, "/api/v1/bills/x", nil))

	if rec.Code != http.StatusAccepted {
		t.Errorf("status = %d, want the 202 already written", rec.Code)
	}

	logs := tel.Logs()
	if len(logs) != 1 {
		t.Fatalf("got %d log records, want 1", len(logs))
	}

	if got := logAttrs(&logs[0])["exception.type"]; got != "github.com/justabill-org/justabill/obs_test.panicError" {
		t.Errorf("exception.type = %q", got)
	}
}

func TestRecoverHandlerWithoutRoute(t *testing.T) {
	tel := obstest.New(t)

	rec := httptest.NewRecorder()
	obs.RecoverHandler(tel.Logger, nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(errors.New("plain"))
	})).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))

	logs := tel.Logs()
	if rec.Code != http.StatusInternalServerError || len(logs) != 1 {
		t.Fatalf("status %d with %d records, want 500 and one record", rec.Code, len(logs))
	}

	attrs := logAttrs(&logs[0])
	if _, ok := attrs["http.route"]; ok {
		t.Errorf("http.route logged without a route: %v", attrs)
	}
}

func TestRecoverHandlerLetsAbortThrough(t *testing.T) {
	tel := obstest.New(t)

	defer func() {
		if p := recover(); !errors.Is(p.(error), http.ErrAbortHandler) {
			t.Errorf("recovered %v, want http.ErrAbortHandler", p)
		}

		if logs := tel.Logs(); len(logs) != 0 {
			t.Errorf("an aborted request was logged as an exception: %v", logs)
		}
	}()

	obs.RecoverHandler(tel.Logger, nil)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic(http.ErrAbortHandler)
	})).ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, "/", nil))
}

func TestHTTPHandlerRecordsNoForbiddenKeys(t *testing.T) {
	tel := obstest.New(t)

	r := httptest.NewRequest(http.MethodPost, "/api/v1/reps?address=1+Main", strings.NewReader(`{"address":"x"}`))
	r.Header.Set("Authorization", "Bearer secret")
	serve(fixedRoute("/api/v1/reps"), func(http.ResponseWriter, *http.Request) {}, r)

	for _, kv := range onlySpan(t, tel).Attributes() {
		key := string(kv.Key)
		if slices.ContainsFunc([]string{"url.", "client.", "user_agent.", "network.", "http.request.header"},
			func(p string) bool { return strings.HasPrefix(key, p) && key != "url.scheme" }) {
			t.Errorf("recorded %s", key)
		}
	}
}
