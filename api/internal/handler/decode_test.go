package handler_test

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/justabill-org/justabill/api/internal/auth"
	"github.com/justabill-org/justabill/api/internal/auth/authtest"
	mw "github.com/justabill-org/justabill/api/internal/middleware"
)

// TestJSONBodiesOverTheCapGet413 sends each JSON route a body over the
// router's cap with no Content-Length (as a chunked upload would), so the
// middleware can't reject it up front and the handler's decoder hits the
// MaxBytesReader instead. Malformed bodies under the cap stay 400.
func TestJSONBodiesOverTheCapGet413(t *testing.T) {
	h := newAccountHandler(newAccountUsers(), authtest.New(), nil)
	routes := []struct {
		method, pattern, path string
		handle                http.HandlerFunc
	}{
		{http.MethodPatch, "/api/v1/me", "/api/v1/me", h.UpdateMe},
		{http.MethodPost, "/api/v1/bills/{id}/vote", "/api/v1/bills/119-hr-1/vote", h.CastVote},
	}
	huge := `{"vote":"` + strings.Repeat("x", mw.DefaultMaxBody) + `"}`
	for _, rt := range routes {
		r := chi.NewRouter()
		r.With(mw.MaxBytes(mw.DefaultMaxBody)).Method(rt.method, rt.pattern, rt.handle)
		for _, tt := range []struct {
			name, body, want string
			status           int
		}{
			{"over the cap", huge, "too large", http.StatusRequestEntityTooLarge},
			{"malformed", `{"vote":`, "invalid request body", http.StatusBadRequest},
		} {
			t.Run(rt.path+" "+tt.name, func(t *testing.T) {
				req := signedIn(rt.method, rt.path, "", "t", auth.Principal{UID: "u"}, "user-1")
				req.Body = io.NopCloser(strings.NewReader(tt.body))
				req.ContentLength = -1
				w := httptest.NewRecorder()
				r.ServeHTTP(w, req)
				if w.Code != tt.status || !strings.Contains(w.Body.String(), tt.want) {
					t.Fatalf("= %d %s, want %d with %q", w.Code, w.Body, tt.status, tt.want)
				}
				if ct := w.Header().Get("Content-Type"); ct != "application/json" {
					t.Errorf("Content-Type = %q, want application/json", ct)
				}
			})
		}
	}
}
