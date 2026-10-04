package sync

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"google.golang.org/genai"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/pipeline/internal/ai"
)

// A submit that fails part way says which step failed, and one that fails after holding its bills
// releases them.
func TestSubmitSummaryBatch_Failures(t *testing.T) {
	boom := errors.New("spanner down")
	tests := []struct {
		name  string
		max   int
		fail  map[string]error
		setup func(*batchStore, *fakeBatchAPI)
		want  string
		// released is whether the batch's holds were released.
		released bool
	}{
		{name: "no max", want: "positive max"},
		{name: "count fails", max: 5, fail: map[string]error{"count": boom}, want: "count bills to summarize"},
		{name: "query fails", max: 5, fail: map[string]error{"query": boom}, want: "query bills to summarize"},
		{
			name: "no bill has text", max: 5,
			setup: func(s *batchStore, _ *fakeBatchAPI) {
				s.loadErr = map[string]error{"hr-119-20": boom, "hr-119-21": boom}
			},
			want: "no due bill has text",
		},
		{name: "batch not recorded", max: 5, fail: map[string]error{"create": boom}, want: "record summary batch"},
		{name: "hold fails", max: 5, fail: map[string]error{"hold": boom}, want: "hold batch bills", released: true},
		{
			name: "job without a name", max: 5,
			setup: func(_ *batchStore, api *fakeBatchAPI) { api.noName = true },
			want:  "no job name", released: true,
		},
		{
			name: "job not recorded", max: 5, fail: map[string]error{"update:" + string(genai.JobStatePending): boom},
			want: "record batch job",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newBatchStore(tieredItems(map[int]int{2: 2}))
			store.fail = tt.fail
			api := &fakeBatchAPI{}
			if tt.setup != nil {
				tt.setup(store, api)
			}
			s, _, _ := batchService(t, store, api, 1)

			_, err := s.SubmitSummaryBatch(
				t.Context(),
				SummaryBatchRequest{Congress: 119, Tiers: []int{2}, Max: tt.max},
			)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want one containing %q", err, tt.want)
			}
			if got := len(store.releases) == 1; got != tt.released {
				t.Errorf("releases = %v, want released %v", store.releases, tt.released)
			}
		})
	}
}

// Without the batch path, a dry run builds its requests with the summarizer's settings, or the
// default model when there's no summarizer.
func TestSubmitSummaryBatch_DryRunWithoutBatches(t *testing.T) {
	for _, tt := range []struct {
		name      string
		sum       summarizer
		wantModel string
	}{
		{name: "summarizer", sum: &fakeSummarizer{}, wantModel: "gemini-test"},
		{name: "no summarizer", wantModel: ai.DefaultModel},
	} {
		t.Run(tt.name, func(t *testing.T) {
			store := &summaryStore{items: tieredItems(map[int]int{2: 1})}
			s, _ := jobService(store, tt.sum)

			plan, err := s.SubmitSummaryBatch(t.Context(), SummaryBatchRequest{
				Congress: 119, Tiers: []int{2}, Max: 5, DryRun: true,
			})
			if err != nil || plan.Bills != 1 {
				t.Fatalf("plan = %+v, err %v; want one bill", plan, err)
			}
			if q := store.queries[0]; q.Model != tt.wantModel {
				t.Errorf("model = %q, want %q", q.Model, tt.wantModel)
			}
		})
	}
}

func TestSubmitSummaryBatch_DryRunCancelled(t *testing.T) {
	store := &summaryStore{items: tieredItems(map[int]int{2: 3})}
	s, _ := jobService(store, nil)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if _, err := s.SubmitSummaryBatch(ctx, SummaryBatchRequest{
		Congress: 119, Tiers: []int{2}, Max: 5, DryRun: true,
	}); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

// A poll that fails on one batch reports the step that failed and leaves the batch open.
func TestPollSummaryBatches_Failures(t *testing.T) {
	boom := errors.New("spanner down")
	young, old := jobNow().Add(-time.Hour), jobNow().Add(-DefaultSummaryBatchHold)
	tests := []struct {
		name    string
		api     *fakeBatchAPI
		state   genai.JobState
		created time.Time
		fail    map[string]error
		setup   func(*batchStore, *memFiles)
		want    string
	}{
		{
			name: "list fails",
			api:  &fakeBatchAPI{},
			fail: map[string]error{"open": boom},
			want: "list open summary batches",
		},
		{
			name: "state not stored", api: &fakeBatchAPI{state: genai.JobStateRunning}, state: genai.JobStatePending,
			created: young, fail: map[string]error{"update:" + string(genai.JobStateRunning): boom},
			want: "store job state",
		},
		{
			name: "cancel fails", api: &fakeBatchAPI{state: genai.JobStateRunning, cancelErr: true},
			state: genai.JobStateRunning, created: old, want: "cancel batch job past its hold",
		},
		{
			name: "release fails", api: &fakeBatchAPI{state: genai.JobStateFailed}, state: genai.JobStateRunning,
			created: young, fail: map[string]error{"release": boom}, want: "release batch bills",
		},
		{
			name: "release not marked", api: &fakeBatchAPI{state: genai.JobStateFailed}, state: genai.JobStateFailed,
			created: young, fail: map[string]error{"update:" + repository.SummaryBatchReleased: boom},
			want: "mark batch released",
		},
		{
			name: "import release fails", api: &fakeBatchAPI{state: genai.JobStateSucceeded},
			state: genai.JobStateSucceeded, created: young, fail: map[string]error{"release": boom},
			want: "release bills with no output",
		},
		{
			name: "import not marked", api: &fakeBatchAPI{state: genai.JobStateSucceeded},
			state: genai.JobStateSucceeded, created: young,
			fail: map[string]error{"update:" + repository.SummaryBatchImported: boom}, want: "mark batch imported",
		},
		{
			name: "input isn't input.jsonl", api: &fakeBatchAPI{state: genai.JobStateSucceeded},
			state: genai.JobStateSucceeded, created: young,
			setup: func(s *batchStore, _ *memFiles) {
				b := s.batches["b1"]
				b.InputURI = "gs://" + testBucket + "/summaries/b1/other.jsonl"
				s.batches["b1"] = b
			},
			want: "isn't an input.jsonl",
		},
		{
			name: "no output directory", api: &fakeBatchAPI{state: genai.JobStateSucceeded},
			state: genai.JobStateSucceeded, created: young,
			setup: func(s *batchStore, _ *memFiles) {
				b := s.batches["b1"]
				b.OutputURI = ""
				s.batches["b1"] = b
			},
			want: "no output directory",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := newBatchStore(nil)
			s, files, _ := batchService(t, store, tt.api, 1)
			openBatch(t, store, files, testJobName, string(tt.state), tt.created)
			store.fail = tt.fail
			if tt.setup != nil {
				tt.setup(store, files)
			}

			if err := s.PollSummaryBatches(t.Context(), 0); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Errorf("err = %v, want one containing %q", err, tt.want)
			}
			if b := store.batch(t, "b1"); b.State == repository.SummaryBatchReleased ||
				b.State == repository.SummaryBatchImported {
				t.Errorf("state = %s, want the batch left open", b.State)
			}
		})
	}
}

// A job state newer than the SDK counts as open: the batch waits out its hold.
func TestPollSummaryBatches_UnknownStateIsOpen(t *testing.T) {
	store := newBatchStore(nil)
	api := &fakeBatchAPI{state: "JOB_STATE_SOMETHING_NEW"}
	s, files, _ := batchService(t, store, api, 1)
	openBatch(t, store, files, testJobName, "JOB_STATE_SOMETHING_NEW", jobNow().Add(-time.Hour))

	if err := s.PollSummaryBatches(t.Context(), 0); err != nil {
		t.Fatal(err)
	}
	if len(store.releases) != 0 || len(api.calls()) != 1 {
		t.Errorf("releases %v, calls %v; want one get and nothing released", store.releases, api.calls())
	}
}

func TestPollSummaryBatches_Cancelled(t *testing.T) {
	store := newBatchStore(nil)
	api := &fakeBatchAPI{state: genai.JobStateRunning}
	s, files, _ := batchService(t, store, api, 1)
	openBatch(t, store, files, testJobName, string(genai.JobStateRunning), jobNow().Add(-time.Hour))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	if err := s.PollSummaryBatches(ctx, 0); !errors.Is(err, context.Canceled) || len(api.calls()) != 0 {
		t.Errorf("err = %v, calls %v; want context.Canceled before any call", err, api.calls())
	}
}
