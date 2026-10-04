package middleware_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"

	mw "github.com/justabill-org/justabill/api/internal/middleware"
)

// requestIDPattern is a 128-bit ID in lowercase hex.
var requestIDPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)

func TestRequestID_SetsHeaderAndContext(t *testing.T) {
	var seen string
	h := mw.RequestID(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen = chimw.GetReqID(r.Context())
		if got := w.Header().Get(mw.RequestIDHeader); got != seen {
			t.Errorf("header before the handler = %q, want %q", got, seen)
		}
	}))

	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))

	id := rr.Header().Get(mw.RequestIDHeader)
	if !requestIDPattern.MatchString(id) {
		t.Fatalf("X-Request-Id = %q, want 32 hex characters", id)
	}
	if seen != id {
		t.Errorf("GetReqID = %q, want the header %q", seen, id)
	}
}

func TestRequestID_IgnoresInboundHeader(t *testing.T) {
	var seen string
	h := mw.RequestID(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		seen = chimw.GetReqID(r.Context())
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set(mw.RequestIDHeader, "spoofed")
	rr := httptest.NewRecorder()
	h.ServeHTTP(rr, req)

	if id := rr.Header().Get(mw.RequestIDHeader); id == "spoofed" || !requestIDPattern.MatchString(id) {
		t.Errorf("X-Request-Id = %q, want a new ID", id)
	}
	if seen == "spoofed" {
		t.Error("the handler saw the client's ID")
	}
}

func TestRequestID_UniquePerRequest(t *testing.T) {
	h := mw.RequestID(http.NotFoundHandler())
	ids := map[string]bool{}
	for range 100 {
		rr := httptest.NewRecorder()
		h.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/", nil))
		ids[rr.Header().Get(mw.RequestIDHeader)] = true
	}
	if len(ids) != 100 {
		t.Errorf("got %d distinct IDs in 100 requests", len(ids))
	}
}

func TestRequestID_OnPanicAndNotFound(t *testing.T) {
	r := chi.NewRouter()
	r.Use(mw.RequestID, chimw.Recoverer)
	r.Get("/boom", func(http.ResponseWriter, *http.Request) { panic("boom") })

	for path, want := range map[string]int{"/boom": http.StatusInternalServerError, "/nope": http.StatusNotFound} {
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, path, nil))
		if rr.Code != want {
			t.Errorf("%s: status = %d, want %d", path, rr.Code, want)
		}
		if id := rr.Header().Get(mw.RequestIDHeader); !requestIDPattern.MatchString(id) {
			t.Errorf("%s: X-Request-Id = %q", path, id)
		}
	}
}

// decodeLines decodes each JSON log line in buf.
func decodeLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(buf.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line isn't JSON: %q", line)
		}
		lines = append(lines, m)
	}
	return lines
}

func withReqID(id string) context.Context {
	return context.WithValue(context.Background(), chimw.RequestIDKey, id)
}

func TestRequestIDHandler_AddsIDFromContext(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(mw.RequestIDHandler(slog.NewJSONHandler(&buf, nil)))

	log.InfoContext(withReqID("abc"), "with id")
	log.InfoContext(context.Background(), "without id")
	log.With("k", "v").WithGroup("g").InfoContext(withReqID("def"), "grouped", "x", 1)

	lines := decodeLines(t, &buf)
	if len(lines) != 3 {
		t.Fatalf("got %d lines, want 3", len(lines))
	}
	if lines[0]["request_id"] != "abc" {
		t.Errorf("line 1 request_id = %v, want abc", lines[0]["request_id"])
	}
	if _, ok := lines[1]["request_id"]; ok {
		t.Errorf("line 2 has a request_id without one in the context: %v", lines[1])
	}
	g, _ := lines[2]["g"].(map[string]any)
	if lines[2]["k"] != "v" || g["request_id"] != "def" {
		t.Errorf("line 3 lost the wrapper through With/WithGroup: %v", lines[2])
	}
}

func TestRequestIDHandler_KeepsExplicitID(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(mw.RequestIDHandler(slog.NewJSONHandler(&buf, nil)))

	log.InfoContext(withReqID("ctx"), "explicit", "request_id", "own")

	if got := strings.Count(buf.String(), `"request_id"`); got != 1 {
		t.Fatalf("request_id appears %d times: %s", got, buf.String())
	}
	if lines := decodeLines(t, &buf); lines[0]["request_id"] != "own" {
		t.Errorf("request_id = %v, want own", lines[0]["request_id"])
	}
}

func TestRequestIDHandler_WrapsOnceAndDelegatesEnabled(t *testing.T) {
	var buf bytes.Buffer
	inner := slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})
	once := mw.RequestIDHandler(inner)
	if twice := mw.RequestIDHandler(once); twice != once {
		t.Error("wrapping twice added a second wrapper")
	}
	if once.Enabled(context.Background(), slog.LevelInfo) {
		t.Error("Enabled(Info) = true under a Warn handler")
	}
}
