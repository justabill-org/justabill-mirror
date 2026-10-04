package obs

import (
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strconv"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/semconv/v1.43.0/httpconv"
	"go.opentelemetry.io/otel/trace"
)

// Default ports for server.port when the URL names none.
const (
	httpPort  = 80
	httpsPort = 443
)

// HTTPTransport wraps base (http.DefaultTransport when nil) so that each request is a CLIENT span
// named "{method}" and a http.client.request.duration point, with server.address, server.port and
// url.full without its query or credentials. A transport error or a status of 400 or more marks
// the span as an error with error.type.
//
// It never sends traceparent, tracestate or baggage. Every host the Go services call (the Census
// geocoder, Congress.gov, GovInfo, the House and Senate) is someone else's, and the design keeps
// our trace context to our own hosts.
//
// Call it after obs.Start (or obstest.New): it takes its tracer and meter when it's called.
func HTTPTransport(base http.RoundTripper) http.RoundTripper {
	if base == nil {
		base = http.DefaultTransport
	}

	duration, err := httpconv.NewClientRequestDuration(otel.Meter(scope))
	if err != nil {
		otel.Handle(err)
	}

	return &transport{base: base, tracer: otel.Tracer(scope), duration: duration}
}

type transport struct {
	base     http.RoundTripper
	tracer   trace.Tracer
	duration httpconv.ClientRequestDuration
}

// RoundTrip implements [http.RoundTripper].
func (t *transport) RoundTrip(req *http.Request) (*http.Response, error) {
	start := time.Now()
	method, methodAttrs := requestMethod(req.Method)
	host, port := hostPort(req.URL)

	attrs := slices.Concat(methodAttrs, []attribute.KeyValue{
		semconv.ServerAddress(host), semconv.ServerPort(port), semconv.URLFull(withoutQuery(req.URL)),
	})
	ctx, span := t.tracer.Start(req.Context(), spanName(method, ""), trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(attrs...))
	// Deferred so that a panicking base transport still ends the span.
	defer span.End()

	resp, err := t.base.RoundTrip(req.WithContext(ctx))

	var after []attribute.KeyValue

	switch {
	case err != nil:
		cause := withoutURL(err)
		after = append(after, semconv.ErrorType(cause))
		span.SetStatus(codes.Error, Redact(cause.Error()))
	case resp.StatusCode >= http.StatusBadRequest:
		after = append(after, semconv.HTTPResponseStatusCode(resp.StatusCode),
			semconv.ErrorTypeKey.String(strconv.Itoa(resp.StatusCode)))
		span.SetStatus(codes.Error, "")
	default:
		after = append(after, semconv.HTTPResponseStatusCode(resp.StatusCode))
	}

	span.SetAttributes(after...)
	t.duration.Record(ctx, time.Since(start).Seconds(), httpconv.RequestMethodAttr(method), host, port, after...)

	return resp, err
}

// hostPort is server.address and server.port: the URL's host and its port, or the scheme's default.
func hostPort(u *url.URL) (string, int) {
	if p, err := strconv.Atoi(u.Port()); err == nil {
		return u.Hostname(), p
	}

	if u.Scheme == "http" {
		return u.Hostname(), httpPort
	}

	return u.Hostname(), httpsPort
}

// withoutQuery is the URL with no query, fragment or credentials: queries carry API keys and
// addresses.
func withoutQuery(u *url.URL) string {
	clean := url.URL{Scheme: u.Scheme, Host: u.Host, Path: u.Path, RawPath: u.RawPath}

	return clean.String()
}

// withoutURL is the error inside the [*url.Error] that [http.Client] adds, whose message holds the
// whole URL.
func withoutURL(err error) error {
	if ue, ok := errors.AsType[*url.Error](err); ok && ue.Err != nil {
		return ue.Err
	}

	return err
}
