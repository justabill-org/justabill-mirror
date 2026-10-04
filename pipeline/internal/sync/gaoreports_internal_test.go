package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/repository"
)

// gaoStore serves the GAO candidates and records links, checked marks and the step's outcome.
// Any other PipelineStore method panics through the nil embedded interface.
type gaoStore struct {
	repository.PipelineStore

	candidates []string
	listErr    error
	upsertErr  error
	linkErr    error
	markErr    error

	mu        gosync.Mutex
	limit     int
	reports   []repository.GAOReportRow
	linked    []string
	checked   []string
	successes []repository.SyncRun
	failures  []repository.SyncRun
}

func (f *gaoStore) ListBillsForGAOCheck(_ context.Context, _, limit int) ([]string, error) {
	f.limit = limit
	return f.candidates, f.listErr
}

func (f *gaoStore) UpsertGAOReport(_ context.Context, r repository.GAOReportRow) error {
	if f.upsertErr != nil {
		return f.upsertErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reports = append(f.reports, r)
	return nil
}

func (f *gaoStore) LinkBillGAOReport(_ context.Context, billID, reportID string) error {
	if f.linkErr != nil {
		return f.linkErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.linked = append(f.linked, billID+"|"+reportID)
	return nil
}

func (f *gaoStore) MarkGAOChecked(_ context.Context, billID string) error {
	if f.markErr != nil {
		return f.markErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checked = append(f.checked, billID)
	return nil
}

func (f *gaoStore) RecordSyncSuccess(_ context.Context, run repository.SyncRun) error {
	f.successes = append(f.successes, run)
	return nil
}

func (f *gaoStore) RecordSyncFailure(_ context.Context, run repository.SyncRun) error {
	f.failures = append(f.failures, run)
	return nil
}

// gaoGovInfo answers GAO searches by citation: HR 1 has a report, HR 2 has none, HR 3's search
// fails, and HR 4's report can't be fetched.
func gaoGovInfo(w http.ResponseWriter, r *http.Request) {
	if r.Method == http.MethodPost {
		var body struct {
			Query string `json:"query"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		switch body.Query {
		case `collection:GAOREPORTS billscitation:"h.r. 1"`:
			fmt.Fprint(w, `{"count": 1, "results": [{"packageId": "GAOREPORTS-GAO-26-1", "title": "One"}]}`)
		case `collection:GAOREPORTS billscitation:"h.r. 2"`:
			fmt.Fprint(w, `{"count": 0, "results": []}`)
		case `collection:GAOREPORTS billscitation:"h.r. 4"`:
			fmt.Fprint(w, `{"count": 1, "results": [{"packageId": "GAOREPORTS-GAO-26-4", "title": "Four"}]}`)
		default:
			w.WriteHeader(http.StatusInternalServerError)
		}
		return
	}
	if r.URL.Path == "/packages/GAOREPORTS-GAO-26-1/summary" {
		fmt.Fprint(w, `{"packageId": "GAOREPORTS-GAO-26-1", "docClass": "GAO-26-1", "dateIssued": "2026-09-01"}`)
		return
	}
	w.WriteHeader(http.StatusInternalServerError)
}

func TestSyncGAOReports_MarksOnlySuccessfulSearches(t *testing.T) {
	store := &gaoStore{candidates: []string{"hr-119-1", "hr-119-2", "hr-119-3", "hr-119-4"}}
	s := serviceWithGovInfo(t, store, gaoGovInfo)

	if err := s.SyncGAOReports(context.Background(), 119, 1000); err != nil {
		t.Fatal(err)
	}
	if store.limit != 1000 {
		t.Errorf("ListBillsForGAOCheck limit = %d, want 1000", store.limit)
	}
	if want := []string{"hr-119-1|GAOREPORTS-GAO-26-1"}; !slices.Equal(store.linked, want) {
		t.Errorf("linked = %v, want %v", store.linked, want)
	}
	// A search with a hit or without one marks the bill; a failed search or a hit that couldn't
	// be linked doesn't, so the next run tries again.
	slices.Sort(store.checked)
	if want := []string{"hr-119-1", "hr-119-2"}; !slices.Equal(store.checked, want) {
		t.Errorf("checked = %v, want %v", store.checked, want)
	}
	if len(store.successes) != 1 || store.successes[0].Step != stepGAOReports || store.successes[0].ItemsSynced != 1 {
		t.Errorf("sync_state successes = %+v, want one %s run with 1 item", store.successes, stepGAOReports)
	}
}

func TestSyncGAOReports_WithoutGovInfoSkips(t *testing.T) {
	s := newTestService(t, &gaoStore{})
	if err := s.SyncGAOReports(context.Background(), 119, 1000); err != nil {
		t.Fatal(err)
	}
}

func TestSyncGAOReports_ListFailureFailsTheStep(t *testing.T) {
	store := &gaoStore{listErr: errFakeStore}
	s := serviceWithGovInfo(t, store, gaoGovInfo)

	err := s.SyncGAOReports(t.Context(), 119, 10)
	if err == nil || !strings.Contains(err.Error(), "list bills for GAO sync") {
		t.Fatalf("err = %v, want the list failure", err)
	}
	if len(store.failures) != 1 || len(store.successes) != 0 {
		t.Errorf("successes %+v, failures %+v; want one failure", store.successes, store.failures)
	}
}

// A report's row takes its number, date and links from the package summary.
func TestLinkGAOReport_StoresTheSummary(t *testing.T) {
	store := &gaoStore{}
	s := serviceWithGovInfo(t, store, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"packageId": "GAOREPORTS-GAO-26-7", "docClass": "GAO-26-7", "dateIssued": "2026-09-01",
			"download": {"pdfLink": "https://example.test/7.pdf", "txtLink": "https://example.test/7.htm"}}`)
	})

	if !s.linkGAOReport(t.Context(), "hr-119-7", "GAOREPORTS-GAO-26-7", "Seven") {
		t.Fatal("linkGAOReport = false, want true")
	}
	if len(store.reports) != 1 {
		t.Fatalf("reports = %+v, want one", store.reports)
	}
	r := store.reports[0]
	if r.ReportID != "GAOREPORTS-GAO-26-7" || r.Title != "Seven" || deref(r.ReportNumber) != "GAO-26-7" ||
		deref(r.PDFURL) != "https://example.test/7.pdf" || deref(r.HTMLURL) != "https://example.test/7.htm" ||
		r.PublishedDate == nil || !r.PublishedDate.Equal(time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC)) {
		t.Errorf("report = %+v, want the summary's fields", r)
	}
	if want := []string{"hr-119-7|GAOREPORTS-GAO-26-7"}; !slices.Equal(store.linked, want) {
		t.Errorf("linked = %v, want %v", store.linked, want)
	}
}

func TestLinkGAOReport_UnparsableDateIsLeftOut(t *testing.T) {
	store := &gaoStore{}
	s := serviceWithGovInfo(t, store, func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprint(w, `{"packageId": "GAOREPORTS-GAO-26-7", "dateIssued": "September 2026"}`)
	})

	if !s.linkGAOReport(t.Context(), "hr-119-7", "GAOREPORTS-GAO-26-7", "Seven") {
		t.Fatal("linkGAOReport = false, want true")
	}
	if r := store.reports[0]; r.PublishedDate != nil || r.ReportNumber != nil || r.PDFURL != nil || r.HTMLURL != nil {
		t.Errorf("report = %+v, want no date, number or links", r)
	}
}

// A bill is marked checked only when its search and every link were stored; otherwise the next
// run tries it again.
func TestSyncGAOReports_FailuresLeaveTheBillUnchecked(t *testing.T) {
	tests := []struct {
		name   string
		bill   string
		store  *gaoStore
		linked int
	}{
		{name: "invalid bill ID", bill: "hr-119", store: &gaoStore{}},
		{name: "report upsert", bill: "hr-119-1", store: &gaoStore{upsertErr: errFakeStore}},
		{name: "bill link", bill: "hr-119-1", store: &gaoStore{linkErr: errFakeStore}},
		{name: "checked mark", bill: "hr-119-1", store: &gaoStore{markErr: errFakeStore}, linked: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.store.candidates = []string{tt.bill}
			s := serviceWithGovInfo(t, tt.store, gaoGovInfo)

			if err := s.SyncGAOReports(t.Context(), 119, 10); err != nil {
				t.Fatal(err)
			}
			if len(tt.store.checked) != 0 {
				t.Errorf("checked = %v, want none", tt.store.checked)
			}
			if len(tt.store.linked) != tt.linked {
				t.Errorf("linked = %v, want %d", tt.store.linked, tt.linked)
			}
			if len(tt.store.successes) != 1 || tt.store.successes[0].ItemsSynced != tt.linked {
				t.Errorf("successes = %+v, want one run with %d items", tt.store.successes, tt.linked)
			}
		})
	}
}

func TestBillTypeToCitation(t *testing.T) {
	tests := map[string]string{
		"hr": "h.r. 5", "s": "s. 5", "hjres": "h.j.res. 5", "sjres": "s.j.res. 5", "hconres": "h.con.res. 5",
		"sconres": "s.con.res. 5", "hres": "h.res. 5", "sres": "s.res. 5", "hamdt": "hamdt 5",
	}
	for billType, want := range tests {
		if got := billTypeToCitation(billType, 5); got != want {
			t.Errorf("billTypeToCitation(%q, 5) = %q, want %q", billType, got, want)
		}
	}
}
