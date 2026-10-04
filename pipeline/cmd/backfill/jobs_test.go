package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel/codes"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/obs/obstest"
	"github.com/justabill-org/justabill/obs/semconv"
	"github.com/justabill-org/justabill/pipeline/internal/congress"
	"github.com/justabill-org/justabill/pipeline/internal/fedreg"
	psync "github.com/justabill-org/justabill/pipeline/internal/sync"
)

// TestRunAllRunsEachStepAsAJob checks a step is a pipeline job named backfill-<step>, and that
// the run stops at the first step that fails.
func TestRunAllRunsEachStepAsAJob(t *testing.T) {
	tel := obstest.New(t)

	err := stepRunner{}.runAll(t.Context(), tel.Logger, []string{"unknown", "bills"})
	if err == nil || !strings.Contains(err.Error(), `step unknown: step "unknown" has no runner`) {
		t.Fatalf("runAll = %v, want the unknown step's error", err)
	}

	spans := tel.Ended()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1: the run stops at the failed step", len(spans))
	}
	if spans[0].Name() != "pipeline.job backfill-unknown" || spans[0].Status().Code != codes.Error {
		t.Errorf("span = %q %v, want a failed pipeline.job backfill-unknown", spans[0].Name(), spans[0].Status())
	}

	var finished int
	for _, r := range tel.Logs() {
		if r.Body().AsString() == semconv.PipelineJobFinishedEvent {
			finished++
		}
	}
	if finished != 1 {
		t.Errorf("got %d %s records, want 1", finished, semconv.PipelineJobFinishedEvent)
	}
}

// crsStepStore is the part of the store the crs-summaries step uses; anything else panics.
type crsStepStore struct {
	repository.PipelineStore

	successes []repository.SyncRun
	stored    int
}

func (*crsStepStore) GetSyncState(context.Context, string, int) (*repository.SyncStateRow, error) {
	return nil, nil //nolint:nilnil // no sync_state row yet
}

func (f *crsStepStore) RecordSyncSuccess(_ context.Context, run repository.SyncRun) error {
	f.successes = append(f.successes, run)
	return nil
}

func (*crsStepStore) StoredCRSSummaries(context.Context, []string) (repository.StoredCRSSummaries, error) {
	return repository.StoredCRSSummaries{}, nil
}

func (f *crsStepStore) UpsertCRSSummaries(_ context.Context, rows []repository.CRSSummaryRow) error {
	f.stored += len(rows)
	return nil
}

// TestRunCRSSummariesStep checks --steps crs-summaries --congress 118 lists the 118th's
// summaries from its first day.
func TestRunCRSSummariesStep(t *testing.T) {
	var paths, froms []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		froms = append(froms, r.URL.Query().Get("fromDateTime"))
		_, _ = w.Write([]byte(`{"pagination":{"count":1},"summaries":[{"actionDate":"2023-02-01",
			"actionDesc":"Introduced in House","bill":{"congress":118,"type":"HR","number":"82"},
			"currentChamber":"House","lastSummaryUpdateDate":"2023-05-01T10:00:00Z","text":"<p>Summary.</p>",
			"updateDate":"2023-05-01T10:05:00Z","versionCode":"00"}]}`))
	}))
	defer srv.Close()
	store := &crsStepStore{}
	svc := psync.New(store, congress.NewClientWithBaseURL(srv.Client(), srv.URL), srv.Client())

	if err := (stepRunner{svc: svc, congress: 118}).run(t.Context(), "crs-summaries"); err != nil {
		t.Fatalf("run crs-summaries: %v", err)
	}
	if !slices.Equal(paths, []string{"/summaries/118"}) || !slices.Equal(froms, []string{"2023-01-03T00:00:00Z"}) {
		t.Errorf("requests = %v from %v, want /summaries/118 from 2023-01-03", paths, froms)
	}
	if store.stored != 1 || len(store.successes) != 1 || store.successes[0].Congress != 118 {
		t.Errorf("stored %d, successes %+v; want 1 summary and one 118th success", store.stored, store.successes)
	}
}

// craStepStore is the part of the store the cra-rules step uses; anything else panics.
type craStepStore struct {
	repository.PipelineStore

	checks    []repository.CRARuleCheck
	rows      []repository.CRARuleRow
	docs      int
	successes []repository.SyncRun
}

func (f *craStepStore) ListCRARuleChecks(context.Context, int, string, int) ([]repository.CRARuleCheck, error) {
	return f.checks, nil
}

func (f *craStepStore) UpsertFRDocument(context.Context, repository.FRDocumentRow) (bool, error) {
	f.docs++
	return true, nil
}

func (f *craStepStore) UpsertCRARule(_ context.Context, r repository.CRARuleRow) (bool, error) {
	f.rows = append(f.rows, r)
	return true, nil
}

func (f *craStepStore) RecordSyncSuccess(_ context.Context, run repository.SyncRun) error {
	f.successes = append(f.successes, run)
	return nil
}

// failingTransport fails the test on any request: the step it serves must make none.
type failingTransport struct{ t *testing.T }

func (f failingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	f.t.Errorf("unexpected Congress.gov request: %s %s", r.Method, r.URL.Path)
	return nil, errors.New("no requests allowed")
}

// TestRunCRARulesStep checks --steps cra-rules checks the loaded resolutions from what Spanner holds
// and the Federal Register, without any Congress.gov request.
func TestRunCRARulesStep(t *testing.T) {
	day, err := os.ReadFile("../../internal/sync/testdata/fedreg/day-2024-12-30.json")
	if err != nil {
		t.Fatal(err)
	}
	fr := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write(day) }))
	defer fr.Close()
	introduced := time.Date(2025, time.February, 4, 0, 0, 0, 0, time.UTC)
	hash := "h1"
	store := &craStepStore{checks: []repository.CRARuleCheck{{
		BillID: "sjres-119-18", BillType: "sjres", Number: 18, IntroducedDate: &introduced, TextHash: &hash,
		Title: `A joint resolution disapproving the rule submitted by the Bureau of Consumer Financial Protection ` +
			`relating to "Overdraft Lending: Very Large Financial Institutions".`,
		Text: `<text>That Congress disapproves the final rule submitted by the Bureau of Consumer Financial ` +
			`Protection relating to <quote>Overdraft Lending: Very Large Financial Institutions</quote> (89 Fed. ` +
			`Reg. 106768 (December 30, 2024)), and such rule shall have no force or effect.</text>`,
	}}}
	noCongress := &http.Client{Transport: failingTransport{t}}
	svc := psync.New(store, congress.NewClientWithBaseURL(noCongress, "https://api.congress.gov/v3"), noCongress)
	svc.SetFederalRegister(fedreg.NewClientWithBaseURL(fr.Client(), fr.URL))

	if err = (stepRunner{svc: svc, congress: 119}).run(t.Context(), "cra-rules"); err != nil {
		t.Fatalf("run cra-rules: %v", err)
	}
	if len(store.rows) != 1 || store.rows[0].Status != repository.CRAStatusMatched || store.docs != 1 {
		t.Errorf("rows %+v, %d documents; want one match and its document", store.rows, store.docs)
	}
	if len(store.successes) != 1 || store.successes[0].Step != "cra_rules" {
		t.Errorf("successes = %+v, want one cra_rules run", store.successes)
	}
}
