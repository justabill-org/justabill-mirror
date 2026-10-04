package obs_test

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/baggage"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	"github.com/justabill-org/justabill/obs"
	"github.com/justabill-org/justabill/obs/obstest"
)

func TestHTTPTransportSpanWithoutPropagation(t *testing.T) {
	tel := obstest.New(t)

	var sent http.Header

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sent = r.Header.Clone()
		_, _ = io.WriteString(w, "{}")
	}))
	t.Cleanup(srv.Close)

	member, err := baggage.NewMember("user", "someone")
	if err != nil {
		t.Fatal(err)
	}

	bag, err := baggage.New(member)
	if err != nil {
		t.Fatal(err)
	}

	ctx, parent := otel.Tracer("test").Start(baggage.ContextWithBaggage(t.Context(), bag), "parent")

	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		srv.URL+"/geocoder/geographies/onelineaddress?address=1+Main+St&benchmark=4", nil)
	if err != nil {
		t.Fatal(err)
	}

	client := &http.Client{Transport: obs.HTTPTransport(nil)}

	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}

	_ = resp.Body.Close()

	parent.End()

	for _, h := range []string{"Traceparent", "Tracestate", "Baggage"} {
		if v := sent.Get(h); v != "" {
			t.Errorf("sent %s: %q to a foreign host", h, v)
		}
	}

	spans := tel.Ended()
	if len(spans) != 2 {
		t.Fatalf("got %d spans, want the client span and its parent", len(spans))
	}

	span := spans[0]
	if span.Name() != http.MethodGet || span.SpanKind() != trace.SpanKindClient ||
		span.Parent().SpanID() != parent.SpanContext().SpanID() {
		t.Errorf("span %q (%v) with parent %v, want a GET client span under the parent",
			span.Name(), span.SpanKind(), span.Parent())
	}

	u, err := url.Parse(srv.URL)
	if err != nil {
		t.Fatal(err)
	}

	want := map[string]string{
		"http.request.method":       "GET",
		"server.address":            u.Hostname(),
		"server.port":               u.Port(),
		"url.full":                  srv.URL + "/geocoder/geographies/onelineaddress",
		"http.response.status_code": "200",
	}
	wantAttrs(t, "span", span.Attributes(), want)

	delete(want, "url.full")
	point := onlyPoint(t, tel, "http.client.request.duration")
	wantAttrs(t, "metric", point.Attributes.ToSlice(), want)
}

// roundTripFunc is a RoundTripper from a function.
type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestHTTPTransportFailures(t *testing.T) {
	tests := []struct {
		name, url     string
		status        int
		err           error
		wantErrorType string
		wantPort      int
	}{
		{name: "not found", url: "http://example.test/x", status: http.StatusNotFound,
			wantErrorType: "404", wantPort: 80},
		{name: "server error", url: "https://example.test/x", status: http.StatusBadGateway,
			wantErrorType: "502", wantPort: 443},
		{
			name: "transport error",
			url:  "https://example.test/x?address=1+Main",
			err: &url.Error{
				Op:  "Get",
				URL: "https://example.test/x?address=1+Main",
				Err: context.DeadlineExceeded,
			},
			wantErrorType: "context.deadlineExceededError",
			wantPort:      443,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tel := obstest.New(t)

			rt := obs.HTTPTransport(roundTripFunc(func(r *http.Request) (*http.Response, error) {
				if tt.err != nil {
					return nil, tt.err
				}

				return &http.Response{StatusCode: tt.status, Body: http.NoBody, Request: r}, nil
			}))

			req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, tt.url, nil)
			if err != nil {
				t.Fatal(err)
			}

			resp, err := rt.RoundTrip(req)
			if !errors.Is(err, tt.err) {
				t.Fatalf("err = %v, want %v", err, tt.err)
			}

			if resp != nil {
				_ = resp.Body.Close()
			}

			checkFailedClientSpan(t, tel, tt.wantErrorType, tt.wantPort)
		})
	}
}

func TestHTTPTransportEndsSpanOnPanic(t *testing.T) {
	tel := obstest.New(t)

	defer func() {
		if p := recover(); p != "boom" {
			t.Errorf("recovered %v, want the panic to go on up", p)
		}

		onlySpan(t, tel)
	}()

	rt := obs.HTTPTransport(roundTripFunc(func(*http.Request) (*http.Response, error) { panic("boom") }))

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, "https://example.test/x", nil)
	if err != nil {
		t.Fatal(err)
	}

	resp, err := rt.RoundTrip(req)
	if err == nil {
		_ = resp.Body.Close()
	}
}

// checkFailedClientSpan checks the one client span recorded: an error with wantErrorType, the
// port, and neither the URL's query nor the URL in the status description.
func checkFailedClientSpan(t *testing.T, tel *obstest.Telemetry, wantErrorType string, wantPort int) {
	t.Helper()

	span := onlySpan(t, tel)
	attrs := attrMap(span.Attributes())

	if span.Status().Code != codes.Error || attrs["error.type"].String() != wantErrorType {
		t.Errorf("span %v with error.type %q, want an error with %q",
			span.Status(), attrs["error.type"].String(), wantErrorType)
	}

	if strings.Contains(span.Status().Description, "Main") {
		t.Errorf("status description holds the URL: %q", span.Status().Description)
	}

	if got := attrs["server.port"].String(); got != strconv.Itoa(wantPort) {
		t.Errorf("server.port = %s, want %d", got, wantPort)
	}

	if got := attrs["url.full"].AsString(); got != "http://example.test/x" && got != "https://example.test/x" {
		t.Errorf("url.full = %q, want no query", got)
	}
}
