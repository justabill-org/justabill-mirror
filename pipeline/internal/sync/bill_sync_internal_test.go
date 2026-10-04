package sync

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"slices"
	"strconv"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/congress"
)

// billSyncStore is a PipelineStore fake for whole-bill syncs. It records which bills were
// upserted and marked synced, and fails the writes named in failWrites. sync_retry is
// fakeRetries; any other method panics through its nil embedded interface.
type billSyncStore struct {
	fakeRetries

	mu         gosync.Mutex
	failWrites map[string]bool // "actions", "cosponsors", "mark", ...
	writes     []string        // every write that succeeded, by those names
	amendments []repository.AmendmentRow
	upserted   []string
	rows       map[string]repository.BillRow // the last UpsertBill of each bill
	marked     []string
	syncedIDs  []string // returned by ListBillIDsSyncedSince
	sinceErr   error    // ListBillIDsSyncedSince's error
	missing    []string // returned by ListMissingVotedBillIDs
	since      time.Time
	success    *repository.SyncRun // the last RecordSyncSuccess
	// state is what GetSyncState returns (nil: no row).
	state *repository.SyncStateRow
	// checkpoints are the last_offset values SaveSyncCheckpoint wrote, in order.
	checkpoints []string
}

func (f *billSyncStore) write(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.failWrites[name] {
		return fmt.Errorf("%w: %s", errFakeStore, name)
	}
	f.writes = append(f.writes, name)
	return nil
}

func (f *billSyncStore) UpsertBill(_ context.Context, b repository.BillRow) error {
	if err := f.write("bill"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.upserted = append(f.upserted, b.ID)
	if f.rows == nil {
		f.rows = map[string]repository.BillRow{}
	}
	f.rows[b.ID] = b
	return nil
}

func (f *billSyncStore) ListMissingVotedBillIDs(context.Context, int) ([]string, error) {
	return f.missing, nil
}

func (f *billSyncStore) MarkBillSynced(_ context.Context, billID string) error {
	if err := f.write("mark"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.marked = append(f.marked, billID)
	return nil
}

func (f *billSyncStore) ListBillIDsSyncedSince(_ context.Context, _ int, since time.Time) ([]string, error) {
	f.since = since
	return f.syncedIDs, f.sinceErr
}

func (f *billSyncStore) ReplaceBillActions(context.Context, string, []repository.BillActionRow) error {
	return f.write("actions")
}

func (f *billSyncStore) ReplaceBillStatus(
	context.Context, string, string, *time.Time, []repository.BillStatusRow,
) error {
	return f.write("status")
}

func (f *billSyncStore) UpsertCongressionalVote(context.Context, repository.CongressionalVoteRow) error {
	return nil
}

func (f *billSyncStore) ListMemberIDsByCongressChamber(context.Context, int, string) ([]string, error) {
	return nil, nil
}

func (f *billSyncStore) UpsertBillTextVersions(
	context.Context, string, []repository.TextVersionRow,
) (repository.TextVersionSyncResult, error) {
	return repository.TextVersionSyncResult{}, f.write("text_versions")
}

func (f *billSyncStore) UpdateBillJSON(_ context.Context, _, column string, _ json.RawMessage) error {
	return f.write(column)
}

func (f *billSyncStore) ReplaceBillSponsorships(
	context.Context,
	string,
	string,
	[]repository.BillSponsorshipRow,
) error {
	return f.write("sponsorships")
}

func (f *billSyncStore) ReplaceBillCommittees(context.Context, string, []repository.BillCommitteeRow) error {
	return f.write("committee_links")
}

func (f *billSyncStore) ReplaceBillSubjects(context.Context, string, []string) error {
	return f.write("subject_links")
}

func (f *billSyncStore) ReplaceBillRelations(context.Context, string, []repository.BillRelationRow) error {
	return f.write("relation_links")
}

func (f *billSyncStore) UpsertAmendment(_ context.Context, a repository.AmendmentRow) error {
	if err := f.write("amendments"); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.amendments = append(f.amendments, a)
	return nil
}

func (f *billSyncStore) GetSyncState(context.Context, string, int) (*repository.SyncStateRow, error) {
	return f.state, nil
}

func (f *billSyncStore) SaveSyncCheckpoint(_ context.Context, _ string, _ int, offset *string, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checkpoints = append(f.checkpoints, *offset)
	return nil
}

func (f *billSyncStore) RecordSyncSuccess(_ context.Context, run repository.SyncRun) error {
	f.success = &run
	return nil
}

func (f *billSyncStore) RecordSyncFailure(context.Context, repository.SyncRun) error {
	return nil
}

// billAPI serves a Congress.gov list of HR 1..n for the 119th, their details, and one-item
// sub-resources. Paths in failPaths answer 500, paths in missingPaths 404, and paths in bodies
// that body. It counts detail requests per bill.
type billAPI struct {
	n            int
	failPaths    map[string]bool
	missingPaths map[string]bool
	bodies       map[string]string

	mu      gosync.Mutex
	details map[string]int
}

func (a *billAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	if a.failPaths[path] {
		w.WriteHeader(http.StatusInternalServerError)
		return
	}
	if a.missingPaths[path] {
		http.NotFound(w, r)
		return
	}
	if body, ok := a.bodies[path]; ok {
		_, _ = w.Write([]byte(body))
		return
	}
	parts := strings.Split(strings.TrimPrefix(path, "/bill/119"), "/")
	switch len(parts) {
	case 1: // the list
		bills := make([]string, 0, a.n)
		for i := 1; i <= a.n; i++ {
			bills = append(bills, fmt.Sprintf(`{"congress":119,"number":"%d","type":"HR","title":"Bill %d"}`, i, i))
		}
		fmt.Fprintf(w, `{"pagination":{"count":%d},"bills":[%s]}`, a.n, strings.Join(bills, ","))
	case 3: // detail: "", "hr", "<n>"
		a.mu.Lock()
		a.details[parts[2]]++
		a.mu.Unlock()
		fmt.Fprintf(w, `{"bill":{"congress":119,"number":%q,"type":"HR","title":"Bill",
			"introducedDate":"2025-01-03","sponsors":[{"bioguideId":"S000001"}],
			"latestAction":{"actionDate":"2025-01-03","text":"Introduced"}}}`, parts[2])
	default:
		_, _ = w.Write([]byte(subResourceFixture(parts[3])))
	}
}

func subResourceFixture(name string) string {
	switch name {
	case "actions":
		return `{"actions":[{"actionDate":"2025-01-03","text":"Referred to the Committee on Ways and Means.",
			"type":"IntroReferral"}]}`
	case "text":
		return `{"textVersions":[{"type":"Introduced in House","date":"2025-01-03T00:00:00Z","formats":[]}]}`
	case "cosponsors":
		return `{"cosponsors":[{"bioguideId":"A000001","sponsorshipDate":"2025-01-05"}]}`
	case "committees":
		return `{"committees":[{"name":"Ways and Means","systemCode":"hswm00",
			"activities":[{"name":"Referred To","date":"2025-01-03T15:03:43Z"}]}]}`
	case "subjects":
		return `{"subjects":{"legislativeSubjects":[{"name":"Taxation"}]}}`
	case "relatedbills":
		return `{"relatedBills":[{"congress":119,"number":7,"type":"S","relationshipDetails":[{"type":"Related bill"}]}]}`
	case "amendments":
		return `{"amendments":[{"number":"1","type":"HAMDT","congress":119}]}`
	default:
		return `{}`
	}
}

func newBillSyncService(t *testing.T, store repository.PipelineStore, api *billAPI) *Service {
	t.Helper()
	api.details = map[string]int{}
	srv := httptest.NewServer(api)
	t.Cleanup(srv.Close)
	return &Service{
		store:  store,
		api:    congress.NewClientWithBaseURL(srv.Client(), srv.URL),
		http:   srv.Client(),
		logger: slog.New(slog.DiscardHandler),
	}
}

func hrSummary(n int) congress.BillSummary {
	return congress.BillSummary{Congress: 119, Number: strconv.Itoa(n), Type: "HR", Title: "Bill"}
}

func TestSyncOneBill_MarksSyncedOnlyWhenEveryPartSucceeds(t *testing.T) {
	store := &billSyncStore{}
	s := newBillSyncService(t, store, &billAPI{n: 1})

	if err := s.syncOneBill(t.Context(), hrSummary(1), 119); err != nil {
		t.Fatalf("syncOneBill: %v", err)
	}
	if !slices.Equal(store.marked, []string{"hr-119-1"}) {
		t.Errorf("marked = %v, want [hr-119-1]", store.marked)
	}
}

func TestSyncOneBill_FailedPartLeavesBillUnmarked(t *testing.T) {
	tests := []struct {
		name      string
		failPath  string // a sub-resource fetch that answers 500
		failWrite string // or a store write that fails
		wantInErr string
	}{
		{name: "actions fetch", failPath: "/bill/119/hr/1/actions", wantInErr: "fetch actions"},
		{name: "text versions fetch", failPath: "/bill/119/hr/1/text", wantInErr: "fetch text_versions"},
		{name: "cosponsors fetch", failPath: "/bill/119/hr/1/cosponsors", wantInErr: "fetch cosponsors"},
		{name: "committees fetch", failPath: "/bill/119/hr/1/committees", wantInErr: "fetch committees"},
		{name: "subjects fetch", failPath: "/bill/119/hr/1/subjects", wantInErr: "fetch subjects"},
		{name: "related bills fetch", failPath: "/bill/119/hr/1/relatedbills", wantInErr: "fetch related_bills"},
		{name: "amendments fetch", failPath: "/bill/119/hr/1/amendments", wantInErr: "fetch amendments"},
		{name: "actions write", failWrite: "actions", wantInErr: "replace bill actions"},
		{name: "status write", failWrite: "status", wantInErr: "replace bill status"},
		{name: "text versions write", failWrite: "text_versions", wantInErr: "upsert text versions"},
		{name: "sponsor links", failWrite: "sponsorships", wantInErr: "replace sponsors links"},
		{name: "committee json", failWrite: "committees", wantInErr: "update committees json"},
		{name: "subject links", failWrite: "subject_links", wantInErr: "replace subjects links"},
		{name: "amendment write", failWrite: "amendments", wantInErr: "upsert amendment hamdt-119-1"},
		{name: "marking", failWrite: "mark", wantInErr: "mark bill synced"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &billSyncStore{failWrites: map[string]bool{tt.failWrite: true}}
			s := newBillSyncService(t, store, &billAPI{n: 1, failPaths: map[string]bool{tt.failPath: true}})

			err := s.syncOneBill(t.Context(), hrSummary(1), 119)
			if err == nil || !strings.Contains(err.Error(), tt.wantInErr) {
				t.Fatalf("syncOneBill error = %v, want one containing %q", err, tt.wantInErr)
			}
			if len(store.marked) != 0 {
				t.Errorf("marked = %v after a failed part, want none", store.marked)
			}
			if !slices.Equal(store.upserted, []string{"hr-119-1"}) {
				t.Errorf("upserted = %v, want [hr-119-1]: the parts that worked are still stored", store.upserted)
			}
		})
	}
}

func TestSyncOneBill_JoinsEveryFailure(t *testing.T) {
	store := &billSyncStore{failWrites: map[string]bool{"actions": true, "cosponsors": true}}
	s := newBillSyncService(t, store, &billAPI{n: 1})

	err := s.syncOneBill(t.Context(), hrSummary(1), 119)
	for _, want := range []string{"replace bill actions", "update cosponsors json"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("error = %v, want it to contain %q", err, want)
		}
	}
}

func TestSyncBills_ResumeSkipsSyncedBills(t *testing.T) {
	since := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	store := &billSyncStore{syncedIDs: []string{"hr-119-1", "hr-119-3", "hr-119-99"}}
	api := &billAPI{n: 4}
	s := newBillSyncService(t, store, api)
	s.SetResumeSince(since)

	if err := s.SyncBills(t.Context(), 119, 0); err != nil {
		t.Fatalf("SyncBills: %v", err)
	}

	if !store.since.Equal(since) {
		t.Errorf("ListBillIDsSyncedSince since = %v, want %v", store.since, since)
	}
	if want := map[string]int{"2": 1, "4": 1}; !maps.Equal(api.details, want) {
		t.Errorf("detail requests = %v, want %v (only the unsynced bills, once each)", api.details, want)
	}
	slices.Sort(store.marked)
	if !slices.Equal(store.marked, []string{"hr-119-2", "hr-119-4"}) {
		t.Errorf("marked = %v, want [hr-119-2 hr-119-4]", store.marked)
	}
	if store.success == nil || store.success.ItemsSynced != 2 || !store.success.Watermark.Equal(since) {
		t.Errorf("sync success = %+v, want 2 items synced and the resume time as the watermark", store.success)
	}
}

func TestSyncBills_WithoutResumeSyncsEveryBill(t *testing.T) {
	store := &billSyncStore{syncedIDs: []string{"hr-119-1"}}
	api := &billAPI{n: 3, failPaths: map[string]bool{"/bill/119/hr/2/subjects": true}}
	s := newBillSyncService(t, store, api)

	if err := s.SyncBills(t.Context(), 119, 0); err != nil {
		t.Fatalf("SyncBills: %v", err)
	}

	if len(api.details) != 3 {
		t.Errorf("detail requests = %v, want all 3 bills", api.details)
	}
	if !store.since.IsZero() {
		t.Errorf("ListBillIDsSyncedSince was called without a resume time")
	}
	// HR 2 failed, so it counts as not synced.
	if store.success == nil || store.success.ItemsSynced != 2 {
		t.Errorf("sync success = %+v, want 2 items synced", store.success)
	}
}
