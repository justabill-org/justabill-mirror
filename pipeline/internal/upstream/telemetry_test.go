package upstream_test

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/justabill-org/justabill/obs/obstest"
	"github.com/justabill-org/justabill/obs/semconv"
	"github.com/justabill-org/justabill/pipeline/internal/upstream"
)

// TestTelemetry covers the #53 acceptance criterion for Congress.gov: a response reporting
// 120 requests left sets the quota gauge, a 429 shows on http.client.request.duration, and
// neither the key nor any trace header leaves the process.
func TestTelemetry(t *testing.T) {
	tel := obstest.New(t)
	sawTraceHeader := make(chan string, 4)
	srv := newServer(t, quotaThen429(sawTraceHeader))
	b := fastBudget(t)
	upstream.SetCooldowns(b, time.Millisecond, time.Millisecond, time.Millisecond)
	h := host(b)
	h.APIKey = testKey
	log, logs := newLogger()
	s := &sleeps{}
	c := newClient(t, log, map[string]upstream.Host{srv.host(): h}, upstream.Hooks{Sleep: s.sleep})

	// A caller that puts a key in the query still doesn't get it recorded.
	target := srv.URL + "/v3/bill?format=json&api_key=" + testKey
	for range 2 {
		if _, err := get(context.Background(), c, target, nil); err != nil {
			t.Fatal(err)
		}
	}

	checkQuotaGauge(t, tel, 120)
	checkStatuses(t, tel, map[int]int{http.StatusOK: 2, http.StatusTooManyRequests: 1})

	spans := tel.Ended()
	if len(spans) != 3 {
		t.Fatalf("got %d spans, want one per attempt (3)", len(spans))
	}
	checkNoKey(t, spans)
	if spans[1].Status().Code != codes.Error {
		t.Errorf("429 attempt span status = %v, want error", spans[1].Status())
	}
	if strings.Contains(logs.String(), testKey) {
		t.Errorf("logs leak the key: %s", logs.String())
	}
	select {
	case h := <-sawTraceHeader:
		t.Errorf("upstream got a %s header", h)
	default:
	}
}

// quotaThen429 answers the first request with 120 requests of quota left, the second with a 429,
// and the rest with 200. It reports any trace header it receives on saw.
func quotaThen429(saw chan<- string) func(http.ResponseWriter, *http.Request, int) {
	return func(w http.ResponseWriter, r *http.Request, hit int) {
		for _, h := range []string{"Traceparent", "Tracestate", "Baggage"} {
			if r.Header.Get(h) != "" {
				saw <- h
			}
		}
		switch hit {
		case 1:
			w.Header().Set("X-Ratelimit-Limit", "5000")
			w.Header().Set("X-Ratelimit-Remaining", "120")
		case 2:
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte("ok"))
	}
}

// checkNoKey fails if any span attribute holds the API key or an api_key parameter.
func checkNoKey(t *testing.T, spans []sdktrace.ReadOnlySpan) {
	t.Helper()
	for _, span := range spans {
		for _, kv := range span.Attributes() {
			if v := kv.Value.String(); strings.Contains(v, testKey) || strings.Contains(v, "api_key") {
				t.Errorf("span %s attribute %s = %q leaks the key", span.Name(), kv.Key, v)
			}
		}
	}
}

func checkQuotaGauge(t *testing.T, tel *obstest.Telemetry, want int64) {
	t.Helper()
	m, ok := tel.Metric(t, semconv.UpstreamQuotaRemainingName)
	if !ok {
		t.Fatalf("no %s metric", semconv.UpstreamQuotaRemainingName)
	}
	g, ok := m.Data.(metricdata.Gauge[int64])
	if !ok || len(g.DataPoints) != 1 {
		t.Fatalf("%s = %#v, want one gauge point", semconv.UpstreamQuotaRemainingName, m.Data)
	}
	p := g.DataPoints[0]
	if p.Value != want {
		t.Errorf("quota remaining = %d, want %d", p.Value, want)
	}
	if v, _ := p.Attributes.Value("server.address"); v.AsString() != "127.0.0.1" || p.Attributes.Len() != 1 {
		t.Errorf("quota attributes = %v, want server.address 127.0.0.1 only", p.Attributes.ToSlice())
	}
}

// checkStatuses checks the http.client.request.duration counts by status code.
func checkStatuses(t *testing.T, tel *obstest.Telemetry, want map[int]int) {
	t.Helper()
	m, ok := tel.Metric(t, "http.client.request.duration")
	if !ok {
		t.Fatal("no http.client.request.duration metric")
	}
	h, ok := m.Data.(metricdata.Histogram[float64])
	if !ok {
		t.Fatalf("http.client.request.duration = %#v, want a histogram", m.Data)
	}
	got := map[int]int{}
	for _, p := range h.DataPoints {
		status, _ := p.Attributes.Value("http.response.status_code")
		got[int(status.AsInt64())] += int(p.Count)
	}
	if len(got) != len(want) {
		t.Errorf("request counts by status = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("requests with status %d = %d, want %d", k, got[k], v)
		}
	}
}
