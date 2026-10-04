package middleware_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	mw "github.com/justabill-org/justabill/api/internal/middleware"
)

// readAll is a handler that reads the whole body and reports what happened:
// 200 with the length, or 413 when the body ran over its cap.
func readAll(w http.ResponseWriter, r *http.Request) {
	b, err := io.ReadAll(r.Body)
	if _, tooBig := errors.AsType[*http.MaxBytesError](err); tooBig {
		w.WriteHeader(http.StatusRequestEntityTooLarge)
		return
	}
	if err != nil {
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	_ = json.NewEncoder(w).Encode(len(b))
}

func TestMaxBytes(t *testing.T) {
	const limit = 1024
	tests := []struct {
		name       string
		size       int
		chunked    bool
		status     int
		earlyError bool // rejected by the middleware, not the handler's read
	}{
		{name: "under the cap", size: limit - 1, status: http.StatusOK},
		{name: "exactly the cap", size: limit, status: http.StatusOK},
		{name: "over the cap", size: limit + 1, status: http.StatusRequestEntityTooLarge, earlyError: true},
		{name: "chunked under the cap", size: limit, chunked: true, status: http.StatusOK},
		{name: "chunked over the cap", size: limit + 1, chunked: true, status: http.StatusRequestEntityTooLarge},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var reached bool
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				reached = true
				readAll(w, r)
			})
			req := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(strings.Repeat("a", tt.size)))
			if tt.chunked {
				req.Body = io.NopCloser(strings.NewReader(strings.Repeat("a", tt.size)))
				req.ContentLength = -1
			}
			w := httptest.NewRecorder()
			mw.MaxBytes(limit)(next).ServeHTTP(w, req)

			if w.Code != tt.status {
				t.Fatalf("status = %d, want %d", w.Code, tt.status)
			}
			if reached == tt.earlyError {
				t.Errorf("handler reached = %v, want %v", reached, !tt.earlyError)
			}
			if !tt.earlyError {
				return
			}
			var body map[string]string
			if err := json.NewDecoder(w.Body).Decode(&body); err != nil || body["error"] == "" {
				t.Errorf("body = %v (%v), want a JSON error", body, err)
			}
			if w.Header().Get("Connection") != "close" {
				t.Errorf("Connection = %q, want close", w.Header().Get("Connection"))
			}
		})
	}
}
