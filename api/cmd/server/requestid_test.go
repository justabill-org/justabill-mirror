package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/justabill-org/justabill/api/internal/handler"
	mw "github.com/justabill-org/justabill/api/internal/middleware"
	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
)

// failingBills is a BillRepo whose List fails. Any other method panics on
// the nil embedded interface.
type failingBills struct {
	repository.BillRepo
}

func (failingBills) List(context.Context, model.ListParams) (*model.ListResult[model.Bill], error) {
	return nil, errors.New("spanner is down")
}

// TestRequestID_InResponseAndEveryLogLine checks that the ID the server
// returns is the one in the handler's error log and the access log, and
// that a client's own X-Request-Id is ignored.
func TestRequestID_InResponseAndEveryLogLine(t *testing.T) {
	var buf bytes.Buffer
	log := newLogger(slog.NewJSONHandler(&buf, nil))
	h := handler.New(nil, handler.WithBills(failingBills{}))
	h.SetLogger(log)
	r := buildRouter(h, log, nil, devEdge(t, 0))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/bills", nil)
	req.Header.Set(mw.RequestIDHeader, "client-chosen")
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rr.Code)
	}
	id := rr.Header().Get(mw.RequestIDHeader)
	if len(id) != 32 || id == "client-chosen" {
		t.Fatalf("X-Request-Id = %q, want a server-generated 32-character ID", id)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	var msgs []string
	for _, line := range lines {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line isn't JSON: %q", line)
		}
		msg, _ := m["msg"].(string)
		msgs = append(msgs, msg)
		if m["request_id"] != id {
			t.Errorf("%q line: request_id = %v, want %q", m["msg"], m["request_id"], id)
		}
		if strings.Count(line, `"request_id"`) != 1 {
			t.Errorf("%q line has request_id more than once: %s", m["msg"], line)
		}
	}
	if len(msgs) < 2 {
		t.Errorf("want the error line and the access log line, got %v", msgs)
	}
}
