package obs

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/propagation"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/semconv/v1.43.0/httpconv"
	"go.opentelemetry.io/otel/trace"
)

// The HTTP helpers follow the stable HTTP semantic conventions, but record only an allowlist of
// attributes: method, scheme, route, status code and error type on the server, plus the host and
// the URL without its query on the client. They never record url.query, client.address,
// network.peer.*, user_agent.original or any header, which otelhttp records unconditionally.

// Route returns the route pattern a request matched, such as "/api/v1/bills/{id}", or "" when it
// matched none. Routers such as chi fill the pattern in while they serve the request, so
// HTTPHandler and RecoverHandler call it only after the wrapped handler has returned or panicked.
type Route func(*http.Request) string

// otherMethod is the semantic conventions' value for a method outside the known set.
const otherMethod = "_OTHER"

// HTTPHandler returns middleware that serves each request inside a SERVER span and records its
// http.server.request.duration. An incoming W3C traceparent becomes the span's parent, so a sampled
// browser trace continues here. Once the request is served, the span is renamed "{method}
// {route}" and gets http.route from route (which may be nil); without a route it stays "{method}".
// A 5xx marks the span as an error with error.type set to the status code; a 4xx doesn't.
//
// Call it after obs.Start (or obstest.New): it takes its tracer and meter when it's called.
func HTTPHandler(route Route) func(http.Handler) http.Handler {
	tracer := otel.Tracer(scope)

	duration, err := httpconv.NewServerRequestDuration(otel.Meter(scope))
	if err != nil {
		otel.Handle(err)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			method, methodAttrs := requestMethod(r.Method)
			scheme := "http"
			if r.TLS != nil {
				scheme = "https"
			}

			ctx := otel.GetTextMapPropagator().Extract(r.Context(), propagation.HeaderCarrier(r.Header))
			ctx, span := tracer.Start(ctx, spanName(method, ""), trace.WithSpanKind(trace.SpanKindServer),
				trace.WithAttributes(append(methodAttrs, semconv.URLScheme(scheme))...))
			defer span.End()

			r = r.WithContext(ctx)
			sw := &statusWriter{ResponseWriter: w}

			finish := func(status int) {
				attrs := []attribute.KeyValue{semconv.HTTPResponseStatusCode(status)}
				rt := routeOf(route, r)
				if rt != "" {
					attrs = append(attrs, semconv.HTTPRoute(rt))
				}

				if status >= http.StatusInternalServerError {
					attrs = append(attrs, semconv.ErrorTypeKey.String(strconv.Itoa(status)))
					span.SetStatus(codes.Error, "")
				}

				span.SetName(spanName(method, rt))
				span.SetAttributes(attrs...)
				duration.Record(ctx, time.Since(start).Seconds(), httpconv.RequestMethodAttr(method), scheme, attrs...)
			}

			defer func() {
				// A panic that nothing inside recovered still ends the span, as the 500 net/http
				// turns it into, before it goes on up. [http.ErrAbortHandler] is a deliberate abort,
				// not a server error: the span keeps whatever status the handler wrote.
				p := recover()
				if p == nil {
					return
				}

				status := http.StatusInternalServerError
				if isAbort(p) {
					status = sw.statusOr200()
				}

				finish(status)
				panic(p)
			}()

			next.ServeHTTP(sw, r)
			finish(sw.statusOr200())
		})
	}
}

// RecoverHandler returns middleware that turns a panic into a 500: it logs an ERROR record named
// "exception" with exception.type, exception.message (through Redact) and exception.stacktrace,
// plus http.route from route (which may be nil), and adds the same exception event to the request's
// span. Put it inside HTTPHandler so the span sees the 500. Like net/http, it lets
// [http.ErrAbortHandler] through, and it writes no status when the handler already wrote one.
func RecoverHandler(log *slog.Logger, route Route) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			sw := &statusWriter{ResponseWriter: w}

			defer func() {
				p := recover()
				if p == nil {
					return
				}

				if isAbort(p) {
					panic(p)
				}

				recordPanic(r.Context(), log, p, routeOf(route, r))

				if sw.status == 0 {
					w.WriteHeader(http.StatusInternalServerError)
				}
			}()

			next.ServeHTTP(sw, r)
		})
	}
}

// isAbort reports whether the recovered value p is [http.ErrAbortHandler], which net/http treats as
// a deliberate abort rather than a server error.
func isAbort(p any) bool {
	err, ok := p.(error)

	return ok && errors.Is(err, http.ErrAbortHandler)
}

// recordPanic logs a recovered panic as an exception record and adds it to the span in ctx.
func recordPanic(ctx context.Context, log *slog.Logger, p any, route string) {
	excType := fmt.Sprintf("%T", p)
	if err, ok := p.(error); ok {
		excType = semconv.ErrorType(err).Value.AsString()
	}

	msg := Redact(fmt.Sprint(p))
	stack := string(debug.Stack())

	span := trace.SpanFromContext(ctx)
	span.AddEvent(semconv.ExceptionEventName, trace.WithAttributes(
		semconv.ExceptionType(excType), semconv.ExceptionMessage(msg), semconv.ExceptionStacktrace(stack)))
	span.SetStatus(codes.Error, "panic")

	attrs := []slog.Attr{
		slog.String(string(semconv.ExceptionTypeKey), excType),
		slog.String(string(semconv.ExceptionMessageKey), msg),
		slog.String(string(semconv.ExceptionStacktraceKey), stack),
	}
	if route != "" {
		attrs = append(attrs, slog.String(string(semconv.HTTPRouteKey), route))
	}

	log.LogAttrs(ctx, slog.LevelError, semconv.ExceptionEventName, attrs...)
}

// requestMethod returns the method as the conventions record it, with http.request.method and,
// for an unknown method, http.request.method_original.
func requestMethod(m string) (string, []attribute.KeyValue) {
	switch m {
	case http.MethodConnect, http.MethodDelete, http.MethodGet, http.MethodHead, http.MethodOptions,
		http.MethodPatch, http.MethodPost, http.MethodPut, http.MethodTrace:
		return m, []attribute.KeyValue{semconv.HTTPRequestMethodKey.String(m)}
	}

	return otherMethod, []attribute.KeyValue{
		semconv.HTTPRequestMethodKey.String(otherMethod),
		semconv.HTTPRequestMethodOriginal(m),
	}
}

// spanName is "{method} {route}", "{method}" without a route, and "HTTP" for an unknown method.
func spanName(method, route string) string {
	if method == otherMethod {
		method = "HTTP"
	}

	if route == "" {
		return method
	}

	return method + " " + route
}

func routeOf(route Route, r *http.Request) string {
	if route == nil {
		return ""
	}

	return route(r)
}

// statusWriter remembers the first status written. Unwrap lets [http.ResponseController] reach the
// underlying writer's Flush and deadlines.
type statusWriter struct {
	http.ResponseWriter

	status int
}

func (w *statusWriter) WriteHeader(code int) {
	if w.status == 0 {
		w.status = code
	}

	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if w.status == 0 {
		w.status = http.StatusOK
	}

	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}

// statusOr200 is the status sent, or 200 when the handler wrote nothing (net/http sends 200).
func (w *statusWriter) statusOr200() int {
	if w.status == 0 {
		return http.StatusOK
	}

	return w.status
}
