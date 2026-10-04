package middleware

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
)

// Logger returns a middleware that logs each request to log: the method,
// matched route, status, duration and the [RequestID]. It logs the route
// pattern rather than the raw path, and never the client's address, user
// agent, headers or query: client IPs stay in the load balancer's logs. Put
// it inside obs.HTTPHandler, so the record carries the request span's trace
// and span IDs.
func Logger(log *slog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			wrapped := &statusWriter{ResponseWriter: w, status: http.StatusOK}

			next.ServeHTTP(wrapped, r)

			log.InfoContext(r.Context(), "request",
				"method", r.Method,
				"route", RoutePattern(r),
				"status", wrapped.status,
				"duration_ms", time.Since(start).Milliseconds(),
				requestIDAttr, chimw.GetReqID(r.Context()),
			)
		})
	}
}

// RoutePattern is the chi route pattern r matched, such as
// "/api/v1/bills/{id}", or "" outside a chi router or before routing. It's
// the [obs.Route] the API gives obs.HTTPHandler and obs.RecoverHandler.
func RoutePattern(r *http.Request) string {
	if rc := chi.RouteContext(r.Context()); rc != nil {
		return rc.RoutePattern()
	}
	return ""
}

// statusWriter records the status for the log line. Unwrap lets [http.ResponseController] reach the
// underlying writer's Flush and deadlines.
type statusWriter struct {
	http.ResponseWriter

	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
