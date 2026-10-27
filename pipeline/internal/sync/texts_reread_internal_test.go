package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"

	"go.opentelemetry.io/otel/sdk/metric/metricdata"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/obs"
	"github.com/justabill-org/justabill/obs/obstest"
	"github.com/justabill-org/justabill/obs/semconv"
	"github.com/justabill-org/justabill/pipeline/internal/congress"
)

// lawText is the text the re-read tests serve at every .htm path.
const lawText = "SEC. 1. SHORT TITLE.\nThis Act is the Enacted Act.\n"

// rereadServer is Congress.gov for the re-read tests: lists holds each bill's text versions
// body by API path ({srv} stands for the server's URL), a path missing from it answers 500, and
// any .htm path is a text. It counts the requests by path.
type rereadServer struct {
	*httptest.Server

	lists    map[string]string
	mu       sync.Mutex
	requests map[string]int
}

func newRereadServer(t *testing.T, lists map[string]string) *rereadServer {
	t.Helper()
	rs := &rereadServer{lists: lists, requests: map[string]int{}}
	rs.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		rs.mu.Lock()
		rs.requests[r.URL.Path]++
		rs.mu.Unlock()
		if strings.HasSuffix(r.URL.Path, ".htm") {
			_, _ = w.Write([]byte(lawText))
			return
		}
		body, ok := rs.lists[r.URL.Path]
		if !ok {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		_, _ = w.Write([]byte(strings.ReplaceAll(body, "{srv}", rs.URL)))
	}))
	t.Cleanup(rs.Close)
	return rs
}

func (rs *rereadServer) count(path string) int {
	rs.mu.Lock()
	defer rs.mu.Unlock()
	return rs.requests[path]
}

// rereadService is textsService with a Congress.gov client and text downloads served by rs.
func rereadService(store *textsStore, rs *rereadServer) (*Service, *bytes.Buffer) {
	svc, logs := textsService(store)
	svc.api = congress.NewClientWithBaseURL(rs.Client(), rs.URL)
	svc.http = rs.Client()
	return svc, logs
}

// queuedVersion is a queued version of bill whose stored formats list nothing.
func queuedVersion(bill, code string) repository.TextVersionRef {
	return repository.TextVersionRef{
		ID: bill + "-" + code, BillID: bill, VersionCode: code, Formats: json.RawMessage(`[]`),
	}
}

// syncTextsCountingItems runs syncBillTexts as a job and returns its justabill.pipeline.items
// counts by outcome.
func syncTextsCountingItems(t *testing.T, svc *Service) (int, map[string]int64) {
	t.Helper()
	tel := obstest.New(t)
	var total int
	if _, err := obs.Job(t.Context(), tel.Logger, "sync-texts", func(ctx context.Context) error {
		var err error
		total, err = svc.syncBillTexts(ctx, 119, 0)
		return err
	}); err != nil {
		t.Fatalf("syncBillTexts: %v", err)
	}
	got := map[string]int64{}
	if m, ok := tel.Metric(t, semconv.PipelineItemsName); ok {
		sum, _ := m.Data.(metricdata.Sum[int64])
		for _, dp := range sum.DataPoints {
			o, _ := dp.Attributes.Value(semconv.ItemOutcomeKey)
			got[o.AsString()] = dp.Value
		}
	}
	return total, got
}

const (
	hr7250Text = "/bill/119/hr/7250/text"
	// hr7250Posted lists hr-119-7250's Public Law with its files posted.
	hr7250Posted = `{"textVersions":[
		{"date":"2025-09-30T03:59:59Z","type":"Public Law","formats":[
			{"type":"Formatted Text","url":"{srv}/119/plaws/publ48/PLAW-119publ48.htm"},
			{"type":"PDF","url":"{srv}/119/plaws/publ48/PLAW-119publ48.pdf"}]},
		{"date":null,"type":"Enrolled Bill","formats":[
			{"type":"Formatted Text","url":"{srv}/119/bills/hr7250/BILLS-119hr7250enr.htm"}]}
	],"pagination":{"count":2}}`
)

// A Public Law stored before its files were posted is re-read from Congress.gov, and its text
// stored from the formats listed now.
func TestSyncBillTexts_RereadsAVersionWithoutFormats(t *testing.T) {
	rs := newRereadServer(t, map[string]string{hr7250Text: hr7250Posted})
	store := &textsStore{
		unfetched: []repository.TextVersionRef{queuedVersion("hr-119-7250", "pl")},
		sections:  map[string]json.RawMessage{},
	}
	svc, logs := rereadService(store, rs)

	total, items := syncTextsCountingItems(t, svc)

	if total != 1 || len(store.stored) != 1 || store.stored[0].TextVersionID != "hr-119-7250-pl" ||
		store.stored[0].Format != formatText {
		t.Fatalf("total %d, stored %+v; want hr-119-7250-pl's Formatted Text", total, store.stored)
	}
	if n := rs.count("/119/plaws/publ48/PLAW-119publ48.htm"); n != 1 {
		t.Errorf("the law's text was downloaded %d times, want once", n)
	}
	if !slices.Equal(store.upserts, []string{"hr-119-7250"}) || len(store.upserted) != 2 {
		t.Errorf("upserts %v of %d rows, want hr-119-7250's 2 versions once", store.upserts, len(store.upserted))
	}
	if want := map[string]int64{"ok": 1}; !maps.Equal(items, want) {
		t.Errorf("items = %v, want %v", items, want)
	}
	checkLogs(t, logs, `level=INFO msg="re-read text versions" bill_id=hr-119-7250 versions=2`)
	if strings.Contains(logs.String(), "level=WARN") {
		t.Errorf("logs have a warning:\n%s", logs)
	}
}

// A version that still lists no text format after the re-read, or is no longer listed, is
// skipped at INFO, not failed, and stays queued: the next run re-reads the bill again.
func TestSyncBillTexts_StillNoFormatAfterTheRereadStaysQueued(t *testing.T) {
	for _, tt := range []struct {
		name, list, reason string
	}{
		{
			name: "still empty",
			list: `{"textVersions":[{"date":"2025-09-30T03:59:59Z","type":"Public Law","formats":[]}],
				"pagination":{"count":1}}`,
			reason: "select text URL: no supported text format found in 0 formats",
		},
		{
			name: "no longer listed",
			list: `{"textVersions":[{"date":null,"type":"Enrolled Bill","formats":[
				{"type":"Formatted Text","url":"{srv}/119/bills/hr7250/BILLS-119hr7250enr.htm"}]}],
				"pagination":{"count":1}}`,
			reason: "no supported text format: the version is no longer listed",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rs := newRereadServer(t, map[string]string{hr7250Text: tt.list})
			store := &textsStore{
				unfetched: []repository.TextVersionRef{queuedVersion("hr-119-7250", "pl")},
				sections:  map[string]json.RawMessage{},
			}
			svc, logs := rereadService(store, rs)

			total, items := syncTextsCountingItems(t, svc)

			if total != 0 || len(store.stored) != 0 {
				t.Errorf("total %d, stored %+v; want nothing stored", total, store.stored)
			}
			if want := map[string]int64{"skipped": 1}; !maps.Equal(items, want) {
				t.Errorf("items = %v, want %v", items, want)
			}
			checkLogs(t, logs, `level=INFO msg="text version lists no text format yet, left queued" `+
				`text_version_id=hr-119-7250-pl bill_id=hr-119-7250 version_code=pl reason="`+tt.reason+`"`)
			if strings.Contains(logs.String(), "level=WARN") {
				t.Errorf("logs have a warning:\n%s", logs)
			}

			if _, err := svc.syncBillTexts(t.Context(), 119, 0); err != nil {
				t.Fatalf("second syncBillTexts: %v", err)
			}
			if n := rs.count(hr7250Text); n != 2 {
				t.Errorf("the bill's versions were read %d times in two runs, want once a run", n)
			}
		})
	}
}

// A bill with two versions that list no text format is re-read once, and both are stored.
func TestSyncBillTexts_RereadsABillOnce(t *testing.T) {
	rs := newRereadServer(t, map[string]string{hr7250Text: hr7250Posted})
	store := &textsStore{
		unfetched: []repository.TextVersionRef{
			queuedVersion("hr-119-7250", "enr"), queuedVersion("hr-119-7250", "pl"),
		},
		sections: map[string]json.RawMessage{},
	}
	svc, _ := rereadService(store, rs)

	total, items := syncTextsCountingItems(t, svc)

	if n := rs.count(hr7250Text); n != 1 || len(store.upserts) != 1 {
		t.Errorf("the bill's versions were read %d times and upserted %d, want once each", n, len(store.upserts))
	}
	if total != 2 || store.inserts["hr-119-7250-enr"] != 1 || store.inserts["hr-119-7250-pl"] != 1 {
		t.Errorf("total %d, inserts %v; want enr and pl stored once each", total, store.inserts)
	}
	if want := map[string]int64{"ok": 2}; !maps.Equal(items, want) {
		t.Errorf("items = %v, want %v", items, want)
	}
}

// A re-read that fails is logged and fails the version, and the run goes on with the next bill.
func TestSyncBillTexts_RereadFailureFailsOnlyTheVersion(t *testing.T) {
	for _, tt := range []struct {
		name    string
		lists   map[string]string
		store   *textsStore
		wantErr string
	}{
		{
			name:    "Congress.gov fails",
			lists:   map[string]string{},
			store:   &textsStore{},
			wantErr: "re-read text versions: fetch text_versions",
		},
		{
			name:    "the upsert fails",
			lists:   map[string]string{"/bill/119/s/195/text": hr7250Posted},
			store:   &textsStore{upsertErr: errFakeStore},
			wantErr: "re-read text versions: upsert text versions",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rs := newRereadServer(t, tt.lists)
			store := tt.store
			store.sections = map[string]json.RawMessage{}
			store.unfetched = []repository.TextVersionRef{
				queuedVersion("s-119-195", "pl"), unfetchedText(t, rs.URL, "hr-119-7251", "ih"),
			}
			svc, logs := rereadService(store, rs)

			total, items := syncTextsCountingItems(t, svc)

			if total != 1 || len(store.stored) != 1 || store.stored[0].TextVersionID != "hr-119-7251-ih" {
				t.Errorf("total %d, stored %+v; want only hr-119-7251-ih", total, store.stored)
			}
			if want := map[string]int64{"ok": 1, "failed": 1}; !maps.Equal(items, want) {
				t.Errorf("items = %v, want %v", items, want)
			}
			checkLogs(t, logs, `level=WARN msg="fetch text failed" text_version_id=s-119-195-pl`, tt.wantErr)
		})
	}
}
