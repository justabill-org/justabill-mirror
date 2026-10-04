package sync

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/congress"
	"github.com/justabill-org/justabill/pipeline/internal/govinfo"
	"github.com/justabill-org/justabill/pipeline/internal/upstream"
)

// fakeRetries is an in-memory sync_retry for PipelineStore fakes: embed it in place of
// repository.PipelineStore. DueRetries returns due[step]; RecordItemFailure and
// ClearItemRetries record their arguments, or fail with recordErr and clearErr. Any method
// neither it nor the embedding fake defines panics through the nil embedded interface.
type fakeRetries struct {
	repository.PipelineStore

	retryMu   gosync.Mutex
	due       map[string][]string
	dueErr    error
	recordErr error
	clearErr  error
	failures  []repository.ItemFailure
	cleared   map[string][]string // step -> IDs, in the order cleared
	clears    int                 // ClearItemRetries calls
}

func (f *fakeRetries) DueRetries(
	_ context.Context, step string, congressNum int, _ time.Time, limit int,
) ([]repository.RetryItem, error) {
	if f.dueErr != nil {
		return nil, f.dueErr
	}
	ids := f.due[step]
	items := make([]repository.RetryItem, 0, len(ids))
	for _, id := range ids[:min(limit, len(ids))] {
		items = append(items, repository.RetryItem{Step: step, Congress: congressNum, ItemID: id})
	}
	return items, nil
}

func (f *fakeRetries) RecordItemFailure(_ context.Context, failure repository.ItemFailure) error {
	if f.recordErr != nil {
		return f.recordErr
	}
	f.retryMu.Lock()
	defer f.retryMu.Unlock()
	f.failures = append(f.failures, failure)
	return nil
}

func (f *fakeRetries) ClearItemRetries(_ context.Context, step string, _ int, ids []string) error {
	if f.clearErr != nil {
		return f.clearErr
	}
	f.retryMu.Lock()
	defer f.retryMu.Unlock()
	if f.cleared == nil {
		f.cleared = map[string][]string{}
	}
	f.cleared[step] = append(f.cleared[step], ids...)
	f.clears++
	return nil
}

// failedItems maps each recorded failure's item ID to whether it was permanent.
func (f *fakeRetries) failedItems() map[string]bool {
	f.retryMu.Lock()
	defer f.retryMu.Unlock()
	got := map[string]bool{}
	for _, fl := range f.failures {
		got[fl.ItemID] = fl.Permanent
	}
	return got
}

func (f *fakeRetries) clearedSorted(step string) []string {
	f.retryMu.Lock()
	defer f.retryMu.Unlock()
	return slices.Sorted(slices.Values(f.cleared[step]))
}

func TestPermanentFailure(t *testing.T) {
	status := func(code int) error {
		return &url.Error{Op: "Get", URL: "https://api.congress.gov/v3/bill", Err: &upstream.StatusError{Status: code}}
	}
	notFound := fmt.Errorf("%w: %w", congress.ErrNotFound, status(http.StatusNotFound))
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{name: "nil", err: nil},
		{name: "plain error", err: errors.New("connection reset")},
		{name: "cancelled", err: fmt.Errorf("fetch actions: %w", context.Canceled)},
		{name: "server error", err: fmt.Errorf("fetch actions: %w", status(http.StatusInternalServerError))},
		{name: "rate limited", err: status(http.StatusTooManyRequests)},
		{name: "404 from upstream", err: fmt.Errorf("fetch actions: %w", notFound), want: true},
		{name: "410", err: status(http.StatusGone), want: true},
		{name: "403", err: status(http.StatusForbidden), want: true},
		{name: "404 from a plain client", err: fmt.Errorf("%w: congress API error: 404", congress.ErrNotFound),
			want: true},
		{name: "unknown bill", err: fmt.Errorf("get bill detail: %w: %w", errUnknownBill, notFound), want: true},
		{name: "oversized body", err: fmt.Errorf("fetch text: %w", upstream.ErrBodyTooLarge), want: true},
		{name: "every cause permanent", err: errors.Join(notFound, status(http.StatusGone)), want: true},
		{name: "one cause transient", err: errors.Join(notFound, status(http.StatusBadGateway))},
		{name: "a store write failed too", err: errors.Join(notFound, errors.New("spanner: aborted"))},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := permanentFailure(tt.err); got != tt.want {
				t.Errorf("permanentFailure(%v) = %v, want %v", tt.err, got, tt.want)
			}
		})
	}
}

func TestChunk(t *testing.T) {
	var got [][]int
	for c := range chunk([]int{1, 2, 3, 4, 5}, 2) {
		got = append(got, c)
	}
	if want := [][]int{{1, 2}, {3, 4}, {5}}; !slices.EqualFunc(got, want, slices.Equal) {
		t.Errorf("chunk = %v, want %v", got, want)
	}
	for range chunk([]int{}, 2) {
		t.Error("chunk of an empty slice yielded")
	}
}

// Bill 2's actions answer 500 every time: it is recorded for a retry, bill 1 is cleared, and
// the step succeeds, so the watermark moves.
func TestSyncBills_RecordsAFailedBillAndMovesTheWatermark(t *testing.T) {
	store := &billSyncStore{}
	s := newBillSyncService(t, store, &billAPI{n: 2, failPaths: map[string]bool{"/bill/119/hr/2/actions": true}})

	if err := s.SyncBills(t.Context(), 119, 0); err != nil {
		t.Fatalf("SyncBills: %v", err)
	}
	if got, want := store.failedItems(), map[string]bool{"hr-119-2": false}; !maps.Equal(got, want) {
		t.Errorf("recorded failures (id: permanent) = %v, want %v", got, want)
	}
	f := store.failures[0]
	if f.Step != repository.RetryStepBills || f.Congress != 119 || f.FailedAt.IsZero() ||
		!strings.Contains(f.Error, "fetch actions") {
		t.Errorf("failure = %+v, want the bills step, the 119th, a time and the actions error", f)
	}
	if got := store.clearedSorted(repository.RetryStepBills); !slices.Equal(got, []string{"hr-119-1"}) {
		t.Errorf("cleared = %v, want [hr-119-1]", got)
	}
	if store.success == nil || store.success.ItemsSynced != 1 {
		t.Errorf("sync success = %+v, want one recorded with 1 item", store.success)
	}
}

func TestSyncBills_PermanentFailureIsGivenUp(t *testing.T) {
	store := &billSyncStore{}
	s := newBillSyncService(t, store, &billAPI{n: 2, missingPaths: map[string]bool{"/bill/119/hr/2/actions": true}})

	if err := s.SyncBills(t.Context(), 119, 0); err != nil {
		t.Fatalf("SyncBills: %v", err)
	}
	if got, want := store.failedItems(), map[string]bool{"hr-119-2": true}; !maps.Equal(got, want) {
		t.Errorf("recorded failures (id: permanent) = %v, want %v", got, want)
	}
	if store.success == nil {
		t.Error("a permanent failure failed the step")
	}
}

// The next run takes the due retries as well as the listing, each bill once, and clears the
// ones that sync now.
func TestSyncBills_RetriesDueBills(t *testing.T) {
	store := &billSyncStore{}
	store.due = map[string][]string{repository.RetryStepBills: {"hr-119-7", "hr-119-2"}}
	api := &billAPI{n: 2}
	s := newBillSyncService(t, store, api)

	if err := s.SyncBills(t.Context(), 119, 0); err != nil {
		t.Fatalf("SyncBills: %v", err)
	}
	if want := map[string]int{"1": 1, "2": 1, "7": 1}; !maps.Equal(api.details, want) {
		t.Errorf("detail requests = %v, want %v (the due bill 7 and the listed bills, once each)", api.details, want)
	}
	want := []string{"hr-119-1", "hr-119-2", "hr-119-7"}
	if got := store.clearedSorted(repository.RetryStepBills); !slices.Equal(got, want) {
		t.Errorf("cleared = %v, want %v", got, want)
	}
	if len(store.failures) != 0 {
		t.Errorf("failures = %+v, want none", store.failures)
	}
}

func TestMergeDueBills(t *testing.T) {
	store := &billSyncStore{}
	s := newBillSyncService(t, store, &billAPI{})
	retries := s.newRetryList(repository.RetryStepBills, 119)
	listed := []congress.BillSummary{hrSummary(1), hrSummary(2)}
	listed[1].Title = "Listed"

	got := s.mergeDueBills(t.Context(), retries, []string{"s-119-4", "hr-119-2", "hr-118-9", "junk"}, listed, 119)

	ids := make([]string, 0, len(got))
	for _, bs := range got {
		ids = append(ids, billID(bs, 119))
	}
	if want := []string{"s-119-4", "hr-119-2", "hr-119-1"}; !slices.Equal(ids, want) {
		t.Errorf("merged = %v, want %v (due first, each once)", ids, want)
	}
	if got[1].Title != "Listed" {
		t.Errorf("due bill 2 = %+v, want its list entry", got[1])
	}
	// The other congress's ID and the junk one can never sync here, so they are cleared.
	if want := []string{"hr-118-9", "junk"}; !slices.Equal(retries.synced, want) {
		t.Errorf("dropped = %v, want %v", retries.synced, want)
	}
}

func TestSyncBills_LimitCoversDueBills(t *testing.T) {
	store := &billSyncStore{}
	store.due = map[string][]string{repository.RetryStepBills: {"hr-119-7"}}
	api := &billAPI{n: 3}
	s := newBillSyncService(t, store, api)

	if err := s.SyncBills(t.Context(), 119, 2); err != nil {
		t.Fatalf("SyncBills: %v", err)
	}
	if want := map[string]int{"7": 1, "1": 1}; !maps.Equal(api.details, want) {
		t.Errorf("detail requests = %v, want %v (the due bill, then the listing, 2 in all)", api.details, want)
	}
}

// When a failure can't be recorded, or the due list can't be read, the step fails and the
// watermark stays where it is.
func TestSyncBills_RetryStoreErrorsFailTheStep(t *testing.T) {
	storeErr := errors.New("spanner: unavailable")
	tests := []struct {
		name    string
		store   func(*billSyncStore)
		wantErr string
	}{
		{name: "record", store: func(f *billSyncStore) { f.recordErr = storeErr }, wantErr: "record bills failure"},
		{name: "due", store: func(f *billSyncStore) { f.dueErr = storeErr }, wantErr: "load due bills retries"},
		{name: "clear", store: func(f *billSyncStore) { f.clearErr = storeErr }, wantErr: "clear bills retries"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &billSyncStore{}
			tt.store(store)
			s := newBillSyncService(t, store, &billAPI{n: 2, failPaths: map[string]bool{"/bill/119/hr/2/text": true}})

			err := s.SyncBills(t.Context(), 119, 0)
			if !errors.Is(err, storeErr) || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("SyncBills = %v, want %q wrapping the store error", err, tt.wantErr)
			}
			if store.success != nil {
				t.Errorf("sync success recorded: %+v", store.success)
			}
		})
	}
}

// A cancelled run records no item failures (they're the cancellation's) and still clears
// the bills it synced.
func TestSyncBills_CancelledRunRecordsNoItemFailures(t *testing.T) {
	store := &stepStore{}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s := serviceWithAPI(t, store, (&billsAPI{onDetail: cancel}).ServeHTTP)

	if err := s.SyncBills(ctx, 119, 0); !errors.Is(err, context.Canceled) {
		t.Fatalf("SyncBills = %v, want context.Canceled", err)
	}
	if len(store.fakeRetries.failures) != 0 {
		t.Errorf("item failures = %+v, want none", store.fakeRetries.failures)
	}
}

func TestRetryList_ClearsInBatches(t *testing.T) {
	store := &billSyncStore{}
	s := newBillSyncService(t, store, &billAPI{})
	retries := s.newRetryList(repository.RetryStepBills, 119)
	for i := range clearRetriesBatch + 1 {
		retries.succeeded(fmt.Sprintf("hr-119-%d", i+1))
	}

	if err := retries.finish(t.Context()); err != nil {
		t.Fatal(err)
	}
	if store.clears != 2 || len(store.cleared[repository.RetryStepBills]) != clearRetriesBatch+1 {
		t.Errorf("clears = %d of %d IDs, want 2 of %d", store.clears,
			len(store.cleared[repository.RetryStepBills]), clearRetriesBatch+1)
	}
}

// govinfoRetryAPI lists BILLS-119hr1ih and BILLS-119hr2ih. Every package summary answers
// with status[packageID], or with a summary that has no download (nothing to fetch) when
// there's none.
func govinfoRetryAPI(status map[string]int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if id, ok := strings.CutSuffix(strings.TrimPrefix(r.URL.Path, "/packages/"), "/summary"); ok {
			if code := status[id]; code != 0 {
				w.WriteHeader(code)
				return
			}
			fmt.Fprintf(w, `{"packageId": %q}`, id)
			return
		}
		fmt.Fprint(w, `{"packages": [{"packageId": "BILLS-119hr1ih"}, {"packageId": "BILLS-119hr2ih"}]}`)
	}
}

// serviceWithUpstreamGovInfo is serviceWithGovInfo over the real upstream client, so an HTTP
// error comes back as an *upstream.StatusError, as in production.
func serviceWithUpstreamGovInfo(t *testing.T, store repository.PipelineStore, handler http.HandlerFunc) *Service {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	budget, err := upstream.NewBudget("govinfo", 1000, 100, 0)
	if err != nil {
		t.Fatal(err)
	}
	client, err := upstream.NewClient(slog.New(slog.DiscardHandler), map[string]upstream.Host{
		srv.Listener.Addr().String(): {Budget: budget, AttemptTimeout: 5 * time.Second, MaxBodyBytes: 1 << 20},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &Service{
		store:   store,
		govinfo: govinfo.NewClientWithBaseURL(client, srv.URL),
		logger:  slog.New(slog.DiscardHandler),
	}
}

func TestSyncGovInfoChanges_RecordsFailuresAndRetriesDuePackages(t *testing.T) {
	store := &syncStateStore{since: time.Now().Add(-time.Hour)}
	store.due = map[string][]string{repository.RetryStepGovInfo: {"BILLS-119hr9ih", "BILLS-119hr1ih"}}
	s := serviceWithUpstreamGovInfo(t, store, govinfoRetryAPI(map[string]int{
		"BILLS-119hr2ih": http.StatusNotFound,
		"BILLS-119hr9ih": http.StatusForbidden,
	}))

	if err := s.SyncGovInfoChanges(t.Context(), 119); err != nil {
		t.Fatalf("SyncGovInfoChanges: %v", err)
	}
	want := map[string]bool{"BILLS-119hr2ih": true, "BILLS-119hr9ih": true}
	if got := store.failedItems(); !maps.Equal(got, want) {
		t.Errorf("recorded failures (id: permanent) = %v, want %v", got, want)
	}
	if got := store.cleared[repository.RetryStepGovInfo]; !slices.Equal(got, []string{"BILLS-119hr1ih"}) {
		t.Errorf("cleared = %v, want [BILLS-119hr1ih] once", got)
	}
	if len(store.upserted) != 1 || store.upserted[0].ItemsSynced != 1 {
		t.Errorf("sync state writes = %+v, want one success with 1 item", store.upserted)
	}
}

func TestSyncGovInfoChanges_TransientFailureIsRetried(t *testing.T) {
	store := &syncStateStore{since: time.Now().Add(-time.Hour)}
	s := serviceWithGovInfo(t, store, govinfoRetryAPI(map[string]int{"BILLS-119hr2ih": http.StatusBadGateway}))

	if err := s.SyncGovInfoChanges(t.Context(), 119); err != nil {
		t.Fatalf("SyncGovInfoChanges: %v", err)
	}
	if got, want := store.failedItems(), map[string]bool{"BILLS-119hr2ih": false}; !maps.Equal(got, want) {
		t.Errorf("recorded failures (id: permanent) = %v, want %v", got, want)
	}
}

func TestSyncGovInfoChanges_RecordErrorFailsTheStep(t *testing.T) {
	store := &syncStateStore{since: time.Now().Add(-time.Hour)}
	store.recordErr = errors.New("spanner: unavailable")
	s := serviceWithGovInfo(t, store, govinfoRetryAPI(map[string]int{"BILLS-119hr2ih": http.StatusBadGateway}))

	if err := s.SyncGovInfoChanges(t.Context(), 119); !errors.Is(err, store.recordErr) {
		t.Fatalf("SyncGovInfoChanges = %v, want the record error", err)
	}
	if len(store.upserted) != 0 || len(store.errors) != 1 {
		t.Errorf("sync state: successes %+v, failures %q; want one failure", store.upserted, store.errors)
	}
}
