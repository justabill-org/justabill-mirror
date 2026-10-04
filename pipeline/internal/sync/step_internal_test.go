package sync

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	gosync "sync"
	"testing"
	"time"

	"github.com/justabill-org/justabill/db/repository"
)

// stepStore records sync_state writes. GetSyncState returns state (nil: no row). sync_retry is
// fakeRetries; any other PipelineStore method panics through its nil embedded interface.
type stepStore struct {
	fakeRetries

	mu        gosync.Mutex
	state     *repository.SyncStateRow
	successes []repository.SyncRun
	failures  []repository.SyncRun
	// failCtxErr is the error of the context RecordSyncFailure was called with.
	failCtxErr error
	// checkpoints are the last_offset values SaveSyncCheckpoint wrote, in order.
	checkpoints []string
}

func (f *stepStore) SaveSyncCheckpoint(_ context.Context, _ string, _ int, offset *string, _ int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.checkpoints = append(f.checkpoints, *offset)
	return nil
}

func (f *stepStore) GetSyncState(context.Context, string, int) (*repository.SyncStateRow, error) {
	return f.state, nil
}

func (f *stepStore) RecordSyncSuccess(_ context.Context, run repository.SyncRun) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.successes = append(f.successes, run)
	return nil
}

func (f *stepStore) RecordSyncFailure(ctx context.Context, run repository.SyncRun) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.failures = append(f.failures, run)
	f.failCtxErr = ctx.Err()
	return nil
}

// The watermark is the run's start: a bill updated while the run was going is listed again.
func TestRunStep_SuccessRecordsTheRunStart(t *testing.T) {
	store := &stepStore{}
	s := newBackfillService(store)
	before := time.Now()
	var during time.Time

	err := s.runStep(t.Context(), stepBills, 119, false, func(context.Context) (int, error) {
		during = time.Now()
		time.Sleep(5 * time.Millisecond)
		return 7, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(store.successes) != 1 || len(store.failures) != 0 {
		t.Fatalf("successes %+v, failures %+v; want one success", store.successes, store.failures)
	}
	got := store.successes[0]
	if got.Step != stepBills || got.Congress != 119 || got.ItemsSynced != 7 {
		t.Errorf("success = %+v", got)
	}
	if got.StartedAt.Before(before) || got.StartedAt.After(during) {
		t.Errorf("StartedAt = %v, want the run's start, between %v and %v", got.StartedAt, before, during)
	}
}

func TestRunStep_ErrorRecordsARedactedFailure(t *testing.T) {
	store := &stepStore{}
	s := newBackfillService(store)
	cause := &url.Error{
		Op:  "Get",
		URL: "https://api.congress.gov/v3/bill/119?format=json&offset=250",
		Err: errors.New("EOF"),
	}

	err := s.runStep(t.Context(), stepBills, 119, false, func(context.Context) (int, error) {
		return 3, fmt.Errorf("list bills: %w: api_key=SECRET", cause)
	})
	if err == nil {
		t.Fatal("runStep = nil, want the step's error")
	}
	if len(store.successes) != 0 || len(store.failures) != 1 {
		t.Fatalf("successes %+v, failures %+v; want one failure", store.successes, store.failures)
	}
	msg := store.failures[0].Error
	if strings.Contains(msg, "SECRET") || !strings.HasPrefix(msg, "list bills: ") {
		t.Errorf("last_error = %q, want it redacted with its context kept", msg)
	}
	if store.failures[0].StartedAt.IsZero() {
		t.Error("failure has no StartedAt")
	}
}

func TestRunStep_TruncatesLongErrors(t *testing.T) {
	store := &stepStore{}
	s := newBackfillService(store)

	_ = s.runStep(t.Context(), stepTexts, 119, false, func(context.Context) (int, error) {
		return 0, errors.New(strings.Repeat("x", 5000))
	})
	if n := len(store.failures[0].Error); n > 1100 {
		t.Errorf("last_error is %d bytes, want about 1 KiB", n)
	}
}

// A step whose context is cancelled can return nil after skipping work (workerPool stops
// early). That is a failure, and it's recorded on a context that is still live.
func TestRunStep_CancelledRunIsAFailure(t *testing.T) {
	store := &stepStore{}
	s := newBackfillService(store)
	ctx, cancel := context.WithCancel(t.Context())

	err := s.runStep(ctx, stepBills, 119, false, func(context.Context) (int, error) {
		cancel()
		return 2, nil
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("runStep = %v, want context.Canceled", err)
	}
	if len(store.successes) != 0 || len(store.failures) != 1 {
		t.Fatalf("successes %+v, failures %+v; want one failure", store.successes, store.failures)
	}
	if store.failCtxErr != nil {
		t.Errorf("failure recorded on a dead context: %v", store.failCtxErr)
	}
}

func TestRunStep_LimitedRunRecordsNoSuccess(t *testing.T) {
	store := &stepStore{}
	s := newBackfillService(store)

	if err := s.runStep(t.Context(), stepBills, 119, true, func(context.Context) (int, error) {
		return 10, nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(store.successes) != 0 || len(store.failures) != 0 {
		t.Errorf("successes %+v, failures %+v; want nothing recorded", store.successes, store.failures)
	}

	_ = s.runStep(t.Context(), stepBills, 119, true, func(context.Context) (int, error) {
		return 0, errors.New("list bills: 503")
	})
	if len(store.failures) != 1 {
		t.Errorf("a limited run's failure wasn't recorded: %+v", store.failures)
	}
}

// billsAPI serves a one-page bill list of two bills and answers every other request with a
// 404, so each bill's detail fetch fails (logged, not a step failure). onDetail runs on each
// detail request. It records the list requests' query strings.
type billsAPI struct {
	mu       gosync.Mutex
	queries  []string
	onDetail func()
}

func (a *billsAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/bill/119" {
		a.mu.Lock()
		a.queries = append(a.queries, r.URL.RawQuery)
		a.mu.Unlock()
		fmt.Fprint(w, `{"bills": [{"number": "1", "type": "HR"}, {"number": "2", "type": "HR"}],
			"pagination": {"count": 2}}`)
		return
	}
	if a.onDetail != nil {
		a.onDetail()
	}
	http.NotFound(w, r)
}

func TestSyncBills_Outcomes(t *testing.T) {
	watermark := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name         string
		state        *repository.SyncStateRow
		limit        int
		cancel       bool
		wantSince    bool
		wantSuccess  bool
		wantFailures int
	}{
		{name: "full run records success", wantSuccess: true},
		{name: "a watermark lists updates since it",
			state: &repository.SyncStateRow{LastSyncedAt: watermark}, wantSince: true, wantSuccess: true},
		{name: "a never-succeeded row lists everything",
			state: &repository.SyncStateRow{ErrorCount: 2}, wantSuccess: true},
		{name: "a limited run records nothing", limit: 1},
		{name: "a cancelled run records a failure", cancel: true, wantFailures: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &stepStore{state: tt.state}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			api := &billsAPI{}
			if tt.cancel {
				api.onDetail = cancel
			}
			s := serviceWithAPI(t, store, api.ServeHTTP)

			err := s.SyncBills(ctx, 119, tt.limit)
			if (err != nil) != tt.cancel {
				t.Fatalf("SyncBills = %v", err)
			}
			if got := len(store.successes) == 1; got != tt.wantSuccess {
				t.Errorf("success recorded = %v (%+v), want %v", got, store.successes, tt.wantSuccess)
			}
			if len(store.failures) != tt.wantFailures {
				t.Errorf("failures = %+v, want %d", store.failures, tt.wantFailures)
			}
			checkBillListQuery(t, api.queries, tt.wantSince)
		})
	}
}

// checkBillListQuery checks the one bill-list request asked for updates since the watermark
// minus the 5-minute buffer, or for everything.
func checkBillListQuery(t *testing.T, queries []string, wantSince bool) {
	t.Helper()
	if len(queries) != 1 {
		t.Fatalf("list queries = %v, want one", queries)
	}
	if got := strings.Contains(queries[0], "fromDateTime"); got != wantSince {
		t.Errorf("list query %q, want fromDateTime: %v", queries[0], wantSince)
	}
	if wantSince && !strings.Contains(queries[0], "fromDateTime=2026-09-26T11:55:00Z") {
		t.Errorf("list query %q, want the watermark minus the 5-minute buffer", queries[0])
	}
}

// brokenSummaryStore fails the summary queue and the diff queue.
type brokenSummaryStore struct {
	stepStore
}

func (*brokenSummaryStore) CountSummaryAttemptsSince(context.Context, time.Time) (int, error) {
	return 0, nil
}

func (*brokenSummaryStore) CountBillsToSummarize(
	context.Context, repository.SummaryQueueQuery,
) (map[int]int, error) {
	return nil, errors.New("backlog: deadline exceeded")
}

func (*brokenSummaryStore) QueryBillsToSummarize(
	context.Context, repository.SummaryQueueQuery,
) ([]repository.SummaryQueueItem, error) {
	return nil, errors.New("bills query: deadline exceeded")
}

func (*brokenSummaryStore) QueryDiffsToSummarize(
	context.Context, repository.DiffSummaryQueueQuery,
) ([]repository.DiffRef, error) {
	return nil, errors.New("diffs query: deadline exceeded")
}

// Both query errors used to be logged and the step recorded as a success (#80, finding F10).
func TestSyncSummaries_QueryErrorsFailTheStep(t *testing.T) {
	store := &brokenSummaryStore{}
	summarizer, _ := fakeGemini(t, "{}")
	s := newBackfillService(store)
	s.summarizer = summarizer
	s.summaryJob = DefaultSummaryJobConfig()
	s.summaryJob.DiffsEnabled = true

	err := s.SyncSummaries(t.Context(), 119, 10)
	if err == nil || !strings.Contains(err.Error(), "bills query") || !strings.Contains(err.Error(), "diffs query") {
		t.Fatalf("SyncSummaries = %v, want both query errors", err)
	}
	if len(store.successes) != 0 || len(store.failures) != 1 || store.failures[0].Step != stepSummaries {
		t.Errorf("successes %+v, failures %+v; want one summaries failure", store.successes, store.failures)
	}
}

// A row with no watermark (only failures so far) must not make members skip itself.
func TestSyncMembers_SkipsOnlyAfterARecentSuccess(t *testing.T) {
	tests := []struct {
		name      string
		state     *repository.SyncStateRow
		wantFetch bool
	}{
		{"no row", nil, true},
		{"failures only", &repository.SyncStateRow{ErrorCount: 3, ConsecutiveFailures: 3}, true},
		{"succeeded an hour ago", &repository.SyncStateRow{LastSyncedAt: time.Now().Add(-time.Hour)}, false},
		{"succeeded two days ago", &repository.SyncStateRow{LastSyncedAt: time.Now().Add(-48 * time.Hour)}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &stepStore{state: tt.state}
			fetched := false
			s := serviceWithAPI(t, store, func(w http.ResponseWriter, _ *http.Request) {
				fetched = true
				fmt.Fprint(w, `{"members": [], "pagination": {"count": 0}}`)
			})
			if err := s.SyncMembers(t.Context(), 119); err != nil {
				t.Fatal(err)
			}
			if fetched != tt.wantFetch {
				t.Errorf("fetched members = %v, want %v", fetched, tt.wantFetch)
			}
			if got := len(store.successes) == 1; got != tt.wantFetch {
				t.Errorf("success recorded = %v, want %v", got, tt.wantFetch)
			}
		})
	}
}

func TestGetGovInfoLastSync_NoWatermarkLooksBack24Hours(t *testing.T) {
	s := newBackfillService(&stepStore{state: &repository.SyncStateRow{ErrorCount: 1}})
	got, err := s.getGovInfoLastSync(t.Context(), 119)
	if err != nil {
		t.Fatal(err)
	}
	if ago := time.Since(got); ago < 23*time.Hour || ago > 25*time.Hour {
		t.Errorf("since = %v (%v ago), want about 24 hours ago", got, ago)
	}
}
