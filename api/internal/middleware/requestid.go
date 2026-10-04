package middleware

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"net/http"

	chimw "github.com/go-chi/chi/v5/middleware"
)

// RequestIDHeader is the response header that carries the request's ID. It goes
// out on the wire as X-Request-Id, [http.CanonicalHeaderKey]'s form.
const RequestIDHeader = "X-Request-ID"

// requestIDAttr is the log attribute that carries the request's ID.
const requestIDAttr = "request_id"

// requestIDBytes is the ID's length before hex encoding: 128 bits.
const requestIDBytes = 16

// RequestID returns a middleware that gives every request a new 128-bit random
// ID in hex. It sets the ID as the [RequestIDHeader] response header before
// the next handler runs, so error and panic responses carry it too, and stores
// it under chi's [chimw.RequestIDKey], where [chimw.GetReqID] finds it. An
// inbound X-Request-Id is ignored: clients can't choose the ID in our logs.
func RequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := newRequestID()
		w.Header().Set(RequestIDHeader, id)
		ctx := context.WithValue(r.Context(), chimw.RequestIDKey, id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// newRequestID returns 16 random bytes in hex.
func newRequestID() string {
	b := make([]byte, requestIDBytes)
	_, _ = rand.Read(b) // crypto/rand.Read never returns an error.
	return hex.EncodeToString(b)
}

// RequestIDHandler wraps h so that every record logged with a context that
// holds a request ID (the *Context methods of [slog.Logger]) gets a request_id
// attribute. A record that already has a top-level request_id keeps its own.
// Wrapping a handler that RequestIDHandler returned returns it unchanged.
func RequestIDHandler(h slog.Handler) slog.Handler {
	if _, ok := h.(requestIDHandler); ok {
		return h
	}
	return requestIDHandler{Handler: h}
}

// requestIDHandler is the [slog.Handler] RequestIDHandler returns.
type requestIDHandler struct {
	slog.Handler
}

// Handle adds request_id from ctx, if there is one, and passes r on.
func (h requestIDHandler) Handle(ctx context.Context, r slog.Record) error {
	if id := chimw.GetReqID(ctx); id != "" && !hasAttr(r, requestIDAttr) {
		r = r.Clone()
		r.AddAttrs(slog.String(requestIDAttr, id))
	}
	return h.Handler.Handle(ctx, r)
}

// WithAttrs keeps the wrapper around the handler with attrs.
func (h requestIDHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return requestIDHandler{Handler: h.Handler.WithAttrs(attrs)}
}

// WithGroup keeps the wrapper around the handler with the group. The
// request_id then lands in that group, like any attribute added later.
func (h requestIDHandler) WithGroup(name string) slog.Handler {
	return requestIDHandler{Handler: h.Handler.WithGroup(name)}
}

// hasAttr reports whether r has a top-level attribute named key.
func hasAttr(r slog.Record, key string) bool {
	found := false
	r.Attrs(func(a slog.Attr) bool {
		found = a.Key == key
		return !found
	})
	return found
}
