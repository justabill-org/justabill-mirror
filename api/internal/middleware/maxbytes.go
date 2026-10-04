package middleware

import (
	"encoding/json"
	"net/http"
)

// keyError is the field every JSON error from this package uses.
const keyError = "error"

// DefaultMaxBody caps a request body on /api/v1: enough for a compare body
// covering about 1,000 bills (#72), far below what could exhaust a pod.
const DefaultMaxBody = 64 << 10

// MaxBytes caps the request body at limit bytes. A request whose
// Content-Length is over the cap gets a 413 with a JSON error before any
// handler runs; a body that turns out longer (chunked, or a lying length)
// fails the handler's read with *[http.MaxBytesError], which the handler maps
// to 413.
//
// Don't nest two MaxBytes: the outer one's Content-Length check runs first,
// so a route that needs a bigger body belongs in a group of its own.
func MaxBytes(limit int64) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.ContentLength > limit {
				// The unread body is left on the connection, so don't reuse it.
				w.Header().Set("Connection", "close")
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusRequestEntityTooLarge)
				_ = json.NewEncoder(w).Encode(map[string]string{keyError: "request body too large"})
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, limit)
			next.ServeHTTP(w, r)
		})
	}
}
