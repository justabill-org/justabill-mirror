package middleware_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	mw "github.com/justabill-org/justabill/api/internal/middleware"
)

// A handler behind Logger can still flush through [http.ResponseController].
func TestLogger_UnwrapsForResponseController(t *testing.T) {
	handler := mw.Logger(slog.New(slog.DiscardHandler))(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if err := http.NewResponseController(w).Flush(); err != nil {
			t.Errorf("Flush: %v", err)
		}
	}))

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/api/v1/bills", nil))

	if !rr.Flushed {
		t.Error("the recorder wasn't flushed")
	}
}

func TestLogger_LogsRequestToInjectedLogger(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))

	handler := mw.Logger(logger)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/bills", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if rr.Code != http.StatusTeapot {
		t.Errorf("expected %d, got %d", http.StatusTeapot, rr.Code)
	}

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("expected one JSON log entry, got %q: %v", buf.String(), err)
	}
	if entry["msg"] != "request" {
		t.Errorf("expected msg 'request', got %v", entry["msg"])
	}
	if entry["method"] != http.MethodGet {
		t.Errorf("expected method GET, got %v", entry["method"])
	}
	for _, k := range []string{"path", "remote"} {
		if _, ok := entry[k]; ok {
			t.Errorf("access log has %q: %v", k, entry)
		}
	}
	if status, _ := entry["status"].(float64); int(status) != http.StatusTeapot {
		t.Errorf("expected status %d, got %v", http.StatusTeapot, entry["status"])
	}
}

func TestLogger_LogsRoutePatternAndRequestID(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(mw.RequestIDHandler(slog.NewJSONHandler(&buf, nil)))

	r := chi.NewRouter()
	r.Use(mw.RequestID, mw.Logger(logger))
	r.Get("/api/v1/bills/{id}", func(http.ResponseWriter, *http.Request) {})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/bills/hr-1-119", nil)
	req.RemoteAddr = "203.0.113.9:1234"
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("expected one JSON log entry, got %q: %v", buf.String(), err)
	}
	if entry["route"] != "/api/v1/bills/{id}" {
		t.Errorf("route = %v, want the pattern", entry["route"])
	}
	if id := rr.Header().Get(mw.RequestIDHeader); id == "" || entry["request_id"] != id {
		t.Errorf("request_id = %v, want the response header %q", entry["request_id"], id)
	}
	if strings.Contains(buf.String(), "hr-1-119") || strings.Contains(buf.String(), "203.0.113.9") {
		t.Errorf("access log has the raw path or the client address: %s", buf.String())
	}
}

func TestRoutePattern_OutsideChi(t *testing.T) {
	if got := mw.RoutePattern(httptest.NewRequest(http.MethodGet, "/x", nil)); got != "" {
		t.Errorf("RoutePattern = %q, want empty", got)
	}
}
