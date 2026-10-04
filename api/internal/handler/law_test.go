package handler_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/justabill-org/justabill/api/internal/cache"
	"github.com/justabill-org/justabill/db/cachekey"
	"github.com/justabill-org/justabill/db/model"
)

type mockLawRepo struct {
	changes *model.BillLawChanges
	section *model.USCSection
	rp      *model.USCReleasePoint
	err     error
	rpErr   error

	changeCalls  int
	sectionCalls int
	rpCalls      int
	billID       string
	alsoLimit    int
	sectionID    string
}

func (m *mockLawRepo) Section(_ context.Context, sectionID string) (*model.USCSection, error) {
	m.sectionCalls++
	m.sectionID = sectionID
	return m.section, m.err
}

func (m *mockLawRepo) CurrentReleasePoint(_ context.Context) (*model.USCReleasePoint, error) {
	m.rpCalls++
	return m.rp, m.rpErr
}

func (m *mockLawRepo) BillLawChanges(_ context.Context, _ string) ([]model.BillLawChange, error) {
	return nil, errors.New("not used by handlers")
}

func (m *mockLawRepo) BillLawChangeEntries(
	_ context.Context, billID string, alsoLimit int,
) (*model.BillLawChanges, error) {
	m.changeCalls++
	m.billID, m.alsoLimit = billID, alsoLimit
	return m.changes, m.err
}

func newLawRouter(l *mockLawRepo, c *cache.Cache) *chi.Mux {
	h := newTestHandler(&mockBillRepo{})
	h.Law = l
	if c != nil {
		h.SetCache(c)
	}
	r := chi.NewRouter()
	r.Get("/api/v1/bills/{id}/law-changes", h.GetBillLawChanges)
	r.Get("/api/v1/law/{title}/{section}", h.GetLawSection)
	return r
}

const lawSectionID = "/us/usc/t42/s1395w-4"

func sampleLawChanges(id string) *model.BillLawChanges {
	title, number, heading := 42, "1395w-4", "Payment for physicians' services"
	noteTitle, noteNumber := 10, "4271"
	explanation := "Moves the deadline from 2025 to 2026."
	return &model.BillLawChanges{
		BillID: id, VersionID: new("v1"), VersionCode: new("ih"),
		Explained: &model.LawChangeProvenance{
			ModelUsed: "gemini-3.5-flash", PromptVersion: "law-v1", ReleasePoint: "119-111",
			GeneratedAt: time.Date(2026, 9, 29, 0, 0, 0, 0, time.UTC),
		},
		Changes: []model.LawChangeEntry{
			{
				SectionID: lawSectionID, InUSCode: true, Loaded: true, TitleNumber: &title, SectionNumber: &number,
				Heading: &heading, ChangeKind: model.LawRefAmends, SubsectionPath: new("(t)"),
				Explanation: &explanation,
				AlsoChangedBy: []model.SectionBill{
					{BillID: "s-119-3", Congress: 119, BillType: "s", Number: 3, RefKinds: []string{"amends"}},
				},
			},
			{
				SectionID:  model.NonUSCSectionPrefix + "Section 5 of the Social Security Act",
				ChangeKind: model.LawRefAmends, AlsoChangedBy: []model.SectionBill{},
			},
			{
				SectionID: "/us/usc/t10/s4271" + model.USCNoteSuffix, InUSCode: true, IsNote: true,
				TitleNumber: &noteTitle, SectionNumber: &noteNumber, ChangeKind: model.LawRefAmends,
				CiteText: new("10 U.S.C. 4271 note"), Explanation: &explanation, AlsoChangedBy: []model.SectionBill{},
			},
		},
	}
}

func TestGetBillLawChanges(t *testing.T) {
	l := &mockLawRepo{changes: sampleLawChanges("hr-119-1"), rp: &model.USCReleasePoint{ReleasePoint: "119-111"}}
	w := serve(newLawRouter(l, nil), "/api/v1/bills/hr-119-1/law-changes")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body %s", w.Code, w.Body)
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body["bill_id"] != "hr-119-1" || body["ai_generated"] != true || body["version_code"] != "ih" {
		t.Errorf("body = %v, want the bill, its version and ai_generated", body)
	}
	if rp, ok := body["current_release_point"].(map[string]any); !ok || rp["release_point"] != "119-111" {
		t.Errorf("current_release_point = %v, want 119-111", body["current_release_point"])
	}
	if ex, ok := body["explained"].(map[string]any); !ok || ex["model_used"] != "gemini-3.5-flash" {
		t.Errorf("explained = %v, want the provenance", body["explained"])
	}
	changes, _ := body["changes"].([]any)
	if len(changes) != 3 {
		t.Fatalf("changes = %v, want 3", body["changes"])
	}
	section, _ := changes[0].(map[string]any)
	if section["in_us_code"] != true || section["is_note"] != false {
		t.Errorf("section entry = %v, want in the US Code, not a note", section)
	}
	nonusc, _ := changes[1].(map[string]any)
	if nonusc["in_us_code"] != false || nonusc["explanation"] != nil || nonusc["title_number"] != nil {
		t.Errorf("nonusc entry = %v, want not in the US Code, explanation null", nonusc)
	}
	note, _ := changes[2].(map[string]any)
	if note["in_us_code"] != true || note["is_note"] != true || note["title_number"] != float64(10) ||
		note["section_number"] != "4271" || note["explanation"] == nil {
		t.Errorf("note entry = %v, want a statutory note under 10 U.S.C. 4271 with its explanation", note)
	}
	if l.billID != "hr-119-1" || l.alsoLimit != 5 {
		t.Errorf("repo got (%q, %d), want (hr-119-1, 5)", l.billID, l.alsoLimit)
	}
}

func TestGetBillLawChanges_NoRefs(t *testing.T) {
	l := &mockLawRepo{changes: &model.BillLawChanges{BillID: "hr-119-1", Changes: []model.LawChangeEntry{}}}
	w := serve(newLawRouter(l, nil), "/api/v1/bills/hr-119-1/law-changes")
	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var body map[string]any
	if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	changes, ok := body["changes"].([]any)
	if !ok || len(changes) != 0 || body["ai_generated"] != false || body["current_release_point"] != nil {
		t.Errorf("body = %v, want an empty list, not AI, no release point", body)
	}
}

func TestGetBillLawChanges_Errors(t *testing.T) {
	tests := []struct {
		name string
		path string
		repo *mockLawRepo
		want int
	}{
		{"unknown bill", "/api/v1/bills/hr-119-9/law-changes", &mockLawRepo{}, http.StatusNotFound},
		{"malformed id", "/api/v1/bills/xx-1/law-changes", &mockLawRepo{}, http.StatusBadRequest},
		{"query fails", "/api/v1/bills/hr-119-1/law-changes", &mockLawRepo{err: errors.New("boom")},
			http.StatusInternalServerError},
		{"query times out", "/api/v1/bills/hr-119-1/law-changes", &mockLawRepo{err: context.DeadlineExceeded},
			http.StatusGatewayTimeout},
		{"release point fails", "/api/v1/bills/hr-119-1/law-changes", &mockLawRepo{rpErr: errors.New("boom")},
			http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if w := serve(newLawRouter(tt.repo, nil), tt.path); w.Code != tt.want {
				t.Errorf("status = %d, want %d; body %s", w.Code, tt.want, w.Body)
			}
		})
	}
}

func TestGetLawSection(t *testing.T) {
	heading := "Payment for physicians' services"
	l := &mockLawRepo{
		section: &model.USCSection{
			SectionID: lawSectionID, TitleNumber: 42, SectionNumber: "1395w-4", Heading: &heading,
			Text: "(a) Payment based on fee schedule", Status: "current", ReleasePoint: "119-100",
		},
		rp: &model.USCReleasePoint{ReleasePoint: "119-111"},
	}
	r := newLawRouter(l, nil)
	for _, path := range []string{"/api/v1/law/42/1395w-4", "/api/v1/law/42/1395w%E2%80%934"} {
		w := serve(r, path)
		if w.Code != http.StatusOK {
			t.Fatalf("%s: status = %d, want 200; body %s", path, w.Code, w.Body)
		}
		var body map[string]any
		if err := json.NewDecoder(w.Body).Decode(&body); err != nil {
			t.Fatalf("decode: %v", err)
		}
		rp, _ := body["current_release_point"].(map[string]any)
		if body["section_id"] != lawSectionID || body["heading"] != heading || body["status"] != "current" ||
			body["release_point"] != "119-100" || rp["release_point"] != "119-111" {
			t.Errorf("%s: body = %v, want the section and the current release point", path, body)
		}
		if l.sectionID != lawSectionID {
			t.Errorf("%s: repo got %q, want %q", path, l.sectionID, lawSectionID)
		}
	}
}

func TestGetLawSection_Errors(t *testing.T) {
	tests := []struct {
		name string
		path string
		repo *mockLawRepo
		want int
	}{
		{"not loaded", "/api/v1/law/42/99999", &mockLawRepo{}, http.StatusNotFound},
		{"title zero", "/api/v1/law/0/1", &mockLawRepo{}, http.StatusBadRequest},
		{"title too big", "/api/v1/law/100/1", &mockLawRepo{}, http.StatusBadRequest},
		{"title not a number", "/api/v1/law/5a/1", &mockLawRepo{}, http.StatusBadRequest},
		{"bad section", "/api/v1/law/42/13%2095", &mockLawRepo{}, http.StatusBadRequest},
		{"leading hyphen", "/api/v1/law/42/-1", &mockLawRepo{}, http.StatusBadRequest},
		{"section too long", "/api/v1/law/42/" + strings.Repeat("1", 41), &mockLawRepo{}, http.StatusBadRequest},
		{"query fails", "/api/v1/law/42/1", &mockLawRepo{err: errors.New("boom")}, http.StatusInternalServerError},
		{"release point fails", "/api/v1/law/42/1", &mockLawRepo{rpErr: errors.New("boom")},
			http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if w := serve(newLawRouter(tt.repo, nil), tt.path); w.Code != tt.want {
				t.Errorf("status = %d, want %d; body %s", w.Code, tt.want, w.Body)
			}
			if tt.want == http.StatusBadRequest && tt.repo.sectionCalls != 0 {
				t.Error("a malformed path reached the repository")
			}
		})
	}
}

// With Redis: a repeated call is served from the cache, a new release point or the pipeline's
// delete of the bill's key misses it, and a section's key is per release point.
func TestLawCache_Redis(t *testing.T) {
	c := testCache(t)
	ctx := t.Context()
	id := fmt.Sprintf("hr-119-%d", time.Now().UnixNano())
	sectionKey := "law:section:119-111:" + lawSectionID
	t.Cleanup(func() {
		for _, key := range []string{cachekey.BillLawChanges(id), "law:release-point", sectionKey} {
			_ = c.Delete(context.Background(), key)
		}
	})
	_ = c.Delete(ctx, "law:release-point")
	_ = c.Delete(ctx, sectionKey)

	l := &mockLawRepo{
		changes: sampleLawChanges(id), rp: &model.USCReleasePoint{ReleasePoint: "119-111"},
		section: &model.USCSection{SectionID: lawSectionID, TitleNumber: 42, SectionNumber: "1395w-4"},
	}
	r := newLawRouter(l, c)
	path := "/api/v1/bills/" + id + "/law-changes"
	for range 2 {
		if w := serve(r, path); w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200", w.Code)
		}
	}
	if l.changeCalls != 1 || l.rpCalls != 1 {
		t.Errorf("repo calls = %d changes, %d release points; want 1 and 1 (both cached)", l.changeCalls, l.rpCalls)
	}

	// A new release point (once its cache expires) misses the cached copy.
	_ = c.Delete(ctx, "law:release-point")
	l.rp = &model.USCReleasePoint{ReleasePoint: "119-112"}
	w := serve(r, path)
	if l.changeCalls != 2 || !strings.Contains(w.Body.String(), `"119-112"`) {
		t.Errorf("after a new release point: %d calls, body %s; want a fresh read", l.changeCalls, w.Body)
	}

	// The pipeline deletes the key when it stores new explanations.
	_ = c.Delete(ctx, cachekey.BillLawChanges(id))
	serve(r, path)
	if l.changeCalls != 3 {
		t.Errorf("after the key's delete: %d calls, want 3", l.changeCalls)
	}

	_ = c.Delete(ctx, "law:release-point")
	l.rp = &model.USCReleasePoint{ReleasePoint: "119-111"}
	for range 2 {
		if w = serve(r, "/api/v1/law/42/1395w-4"); w.Code != http.StatusOK {
			t.Fatalf("section status = %d, want 200", w.Code)
		}
	}
	if l.sectionCalls != 1 {
		t.Errorf("section calls = %d, want 1 (cached)", l.sectionCalls)
	}
}
