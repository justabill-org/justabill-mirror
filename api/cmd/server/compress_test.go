package main

import (
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/justabill-org/justabill/api/internal/handler"
	"github.com/justabill-org/justabill/db/model"
	"github.com/justabill-org/justabill/db/repository"
)

// listedBills is a BillRepo whose List returns one bill. Any other method
// panics on the nil embedded interface.
type listedBills struct {
	repository.BillRepo
}

func (listedBills) List(context.Context, model.ListParams) (*model.ListResult[model.Bill], error) {
	return &model.ListResult[model.Bill]{Items: []model.Bill{{ID: "hr-119-1", Title: "A bill"}}, Total: 1}, nil
}

// TestBuildRouter_GzipsJSON checks that JSON responses are gzipped for a
// client that accepts gzip, and sent as is to one that doesn't (#450).
func TestBuildRouter_GzipsJSON(t *testing.T) {
	log := slog.New(slog.DiscardHandler)
	h := handler.New(nil, handler.WithBills(listedBills{}))
	h.SetLogger(log)
	r := buildRouter(h, log, nil, devEdge(t, 0))

	for _, tc := range []struct {
		name, accept string
		gzipped      bool
	}{
		{"accepts gzip", "gzip, deflate, br", true},
		{"no Accept-Encoding", "", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/v1/bills", nil)
			if tc.accept != "" {
				req.Header.Set("Accept-Encoding", tc.accept)
			}
			rr := httptest.NewRecorder()
			r.ServeHTTP(rr, req)

			if rr.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", rr.Code)
			}
			if got := rr.Header().Get("Content-Encoding"); (got == "gzip") != tc.gzipped {
				t.Fatalf("Content-Encoding = %q, gzipped want %v", got, tc.gzipped)
			}
			got := decodeBills(t, rr, tc.gzipped)
			if len(got.Items) != 1 || got.Items[0].ID != "hr-119-1" {
				t.Errorf("body = %+v", got)
			}
		})
	}
}

// decodeBills decodes a bill list response, gunzipping it first when gzipped,
// in which case it must also vary on Accept-Encoding for shared caches.
func decodeBills(t *testing.T, rr *httptest.ResponseRecorder, gzipped bool) model.ListResult[model.Bill] {
	t.Helper()
	var body io.Reader = rr.Body
	if gzipped {
		if vary := rr.Header().Values("Vary"); !slices.Contains(vary, "Accept-Encoding") {
			t.Errorf("Vary = %q, want it to name Accept-Encoding", vary)
		}
		zr, err := gzip.NewReader(rr.Body)
		if err != nil {
			t.Fatalf("gzip reader: %v", err)
		}
		body = zr
	}
	var got model.ListResult[model.Bill]
	if err := json.NewDecoder(body).Decode(&got); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	return got
}
