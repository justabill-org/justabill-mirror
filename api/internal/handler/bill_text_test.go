package handler_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/justabill-org/justabill/db/model"
)

const (
	billA = "hr-119-1"
	billB = "s-119-1"

	// Text version and diff IDs are UUIDs (GENERATE_UUID() in the schema), and the handlers
	// refuse anything else (#915).
	versionV1       = "0b7a3c52-6a1e-4c0e-9d0e-3f1b2a4c5d01"
	versionV2       = "0b7a3c52-6a1e-4c0e-9d0e-3f1b2a4c5d02"
	versionParsed   = "0b7a3c52-6a1e-4c0e-9d0e-3f1b2a4c5d11"
	versionUnparsed = "0b7a3c52-6a1e-4c0e-9d0e-3f1b2a4c5d12"
	versionEmpty    = "0b7a3c52-6a1e-4c0e-9d0e-3f1b2a4c5d13"
	diffD1          = "5d2e8f10-1c3b-4a7d-8e9f-0a1b2c3d4e01"
)

// textRouter serves the text and diff routes the way cmd/server mounts them.
func textRouter(repo *mockBillRepo) http.Handler {
	h := newTestHandler(repo)
	r := chi.NewRouter()
	r.Get("/api/v1/bills/{id}/text/{vid}", h.GetBillText)
	r.Get("/api/v1/bills/{id}/diffs/{did}", h.GetBillDiff)
	return r
}

func get(t *testing.T, router http.Handler, path string) *httptest.ResponseRecorder {
	t.Helper()
	w := httptest.NewRecorder()
	router.ServeHTTP(w, httptest.NewRequest(http.MethodGet, path, nil))
	return w
}

func errorBody(t *testing.T, w *httptest.ResponseRecorder) string {
	t.Helper()
	var body map[string]string
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	return body["error"]
}

func TestGetBillText_ScopedToBill(t *testing.T) {
	repo := &mockBillRepo{texts: map[string]*model.BillText{
		billKey(billB, versionV1): {ID: "t1", TextVersionID: versionV1, Format: "xml", Content: "text of B"},
	}}
	router := textRouter(repo)

	w := get(t, router, "/api/v1/bills/"+billB+"/text/"+versionV1)
	if w.Code != http.StatusOK {
		t.Fatalf("own bill: status = %d, want 200", w.Code)
	}
	var text model.BillText
	if err := json.NewDecoder(w.Body).Decode(&text); err != nil {
		t.Fatalf("decode text: %v", err)
	}
	if text.ID != "t1" || text.TextVersionID != versionV1 || text.Content != "text of B" {
		t.Errorf("text = %+v", text)
	}

	w = get(t, router, "/api/v1/bills/"+billA+"/text/"+versionV1)
	if w.Code != http.StatusNotFound {
		t.Fatalf("other bill: status = %d, want 404", w.Code)
	}
	if msg := errorBody(t, w); msg != "text content not found" {
		t.Errorf("other bill: error = %q", msg)
	}
}

func TestGetBillDiff_ScopedToBill(t *testing.T) {
	repo := &mockBillRepo{
		diffByID: map[string]*model.BillTextDiff{
			billKey(billB, diffD1): {
				ID: diffD1, BillID: billB, FromVersionID: versionV1, ToVersionID: versionV2,
				DiffContent: json.RawMessage(`{"changes":[]}`),
			},
		},
		diffSummary: &model.BillTextDiffSummary{DiffID: diffD1, Summary: "what changed"},
	}
	router := textRouter(repo)

	w := get(t, router, "/api/v1/bills/"+billA+"/diffs/"+diffD1)
	if w.Code != http.StatusNotFound {
		t.Fatalf("other bill: status = %d, want 404", w.Code)
	}
	if msg := errorBody(t, w); msg != "diff not found" {
		t.Errorf("other bill: error = %q", msg)
	}
	if repo.summaryReads != 0 {
		t.Errorf("other bill: summary read %d times, want 0", repo.summaryReads)
	}

	w = get(t, router, "/api/v1/bills/"+billB+"/diffs/"+diffD1)
	if w.Code != http.StatusOK {
		t.Fatalf("own bill: status = %d, want 200", w.Code)
	}
	var body struct {
		Diff    model.BillTextDiff         `json:"diff"`
		Summary *model.BillTextDiffSummary `json:"summary"`
	}
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode diff: %v", err)
	}
	if body.Diff.ID != diffD1 || body.Diff.BillID != billB {
		t.Errorf("diff = %+v", body.Diff)
	}
	if body.Summary == nil || body.Summary.Summary != "what changed" {
		t.Errorf("summary = %+v", body.Summary)
	}
}

func TestGetBillText_RawXMLOnlyWithoutSectionsOrOnRequest(t *testing.T) {
	sections := json.RawMessage(`[{"id":"s1","header":"Sec. 1","content":"short title"}]`)
	repo := &mockBillRepo{texts: map[string]*model.BillText{
		billKey(billA, versionParsed): {
			ID:            "t1",
			TextVersionID: versionParsed,
			Content:       "<bill/>",
			Sections:      sections,
		},
		billKey(billA, versionUnparsed): {ID: "t2", TextVersionID: versionUnparsed, Content: "<bill/>"},
		billKey(billA, versionEmpty): {
			ID:            "t3",
			TextVersionID: versionEmpty,
			Content:       "<bill/>",
			Sections:      json.RawMessage(` [ ] `),
		},
	}}
	router := textRouter(repo)

	for _, tc := range []struct {
		name, path string
		wantRaw    bool
	}{
		{"parsed sections", "/" + versionParsed, false},
		{"parsed sections, raw asked for", "/" + versionParsed + "?raw=1", true},
		{"no sections", "/" + versionUnparsed, true},
		{"empty sections", "/" + versionEmpty, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := get(t, router, "/api/v1/bills/"+billA+"/text"+tc.path)
			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200", w.Code)
			}
			var body map[string]json.RawMessage
			if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
				t.Fatalf("decode: %v", err)
			}
			if _, ok := body["content"]; ok != tc.wantRaw {
				t.Errorf("content sent = %v, want %v", ok, tc.wantRaw)
			}
		})
	}
	// The handler blanks its own copy of the text, never the repository's.
	if repo.texts[billKey(billA, versionParsed)].Content != "<bill/>" {
		t.Error("the repository's text lost its content")
	}
}

func TestBillTextDiff_ListOmitsMissingContent(t *testing.T) {
	out, err := json.Marshal(model.BillTextDiff{ID: "d1", DiffStats: json.RawMessage(`{"sections_added":1}`)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(out), "diff_content") {
		t.Errorf("metadata-only diff = %s, want no diff_content", out)
	}
}
