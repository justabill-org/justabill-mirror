package sync

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"maps"
	"net/http"
	"net/http/httptest"
	"reflect"
	"slices"
	"strconv"
	"strings"
	gosync "sync"
	"sync/atomic"
	"testing"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"google.golang.org/genai"

	"github.com/justabill-org/justabill/db/repository"
	"github.com/justabill-org/justabill/obs"
	"github.com/justabill-org/justabill/obs/obstest"
	"github.com/justabill-org/justabill/obs/semconv"
	"github.com/justabill-org/justabill/pipeline/internal/ai"
)

// jobNow is the summary job's clock in these tests.
func jobNow() time.Time { return time.Date(2026, time.October, 5, 14, 30, 0, 0, time.UTC) }

// summaryStore is a summary queue in memory. It serves queue items (up to the query's limit) and
// bill contexts, and records bill and diff attempts. Any other PipelineStore method panics
// through the nil embedded interface, so a disabled path that queries diffs fails the test.
type summaryStore struct {
	repository.PipelineStore

	used    int
	backlog map[int]int
	items   []repository.SummaryQueueItem
	loadErr map[string]error
	// crs is the latest CRS summary LoadBillContext returns for a bill (none if absent).
	crs map[string]*repository.SummaryCRSContext
	// rules is the disapproved rule LoadBillContext returns for a bill (none if absent).
	rules     map[string]*repository.SummaryRuleContext
	diffs     []repository.DiffRef
	recordErr error
	// diffRecordErr fails RecordDiffSummaryAttempt for the diffs it names.
	diffRecordErr map[string]error

	mu           gosync.Mutex
	since        time.Time
	queries      []repository.SummaryQueueQuery
	attempts     []repository.SummaryAttemptRow
	diffQueries  []repository.DiffSummaryQueueQuery
	diffAttempts []repository.DiffSummaryAttemptRow
}

func (f *summaryStore) CountSummaryAttemptsSince(_ context.Context, since time.Time) (int, error) {
	f.since = since
	return f.used, nil
}

func (f *summaryStore) CountBillsToSummarize(context.Context, repository.SummaryQueueQuery) (map[int]int, error) {
	return f.backlog, nil
}

func (f *summaryStore) QueryBillsToSummarize(
	_ context.Context, q repository.SummaryQueueQuery,
) ([]repository.SummaryQueueItem, error) {
	f.queries = append(f.queries, q)
	return f.items[:min(q.Limit, len(f.items))], nil
}

// LoadBillContext returns a context whose title, text and hash name the bill.
func (f *summaryStore) LoadBillContext(
	_ context.Context, billID, versionID string,
) (*repository.SummaryBillContext, error) {
	if err := f.loadErr[billID]; err != nil {
		return nil, err
	}
	statusDate := time.Date(2026, time.September, 30, 0, 0, 0, 0, time.UTC)
	return &repository.SummaryBillContext{
		BillID: billID, Congress: 119, BillType: "hr", Number: 1, Title: "Title of " + billID,
		Status: "Introduced", StatusDate: &statusDate, Committees: []string{"Ways and Means Committee"},
		VersionID: versionID, VersionCode: "ih", VersionName: "Introduced in House",
		ContentHash: "hash-" + billID, Text: "SEC. 1. Text of " + billID + ".", CRS: f.crs[billID],
		Rule: f.rules[billID],
	}, nil
}

func (f *summaryStore) RecordSummaryAttempt(_ context.Context, a repository.SummaryAttemptRow) error {
	if f.recordErr != nil {
		return f.recordErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.attempts = append(f.attempts, a)
	return nil
}

func (f *summaryStore) QueryDiffsToSummarize(
	_ context.Context, q repository.DiffSummaryQueueQuery,
) ([]repository.DiffRef, error) {
	f.diffQueries = append(f.diffQueries, q)
	return f.diffs[:min(q.Limit, len(f.diffs))], nil
}

func (f *summaryStore) RecordDiffSummaryAttempt(_ context.Context, a repository.DiffSummaryAttemptRow) error {
	if err := f.diffRecordErr[a.DiffID]; err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.diffAttempts = append(f.diffAttempts, a)
	return nil
}

// diffSummaries returns the diff summaries recorded, as "diff|summary", sorted.
func (f *summaryStore) diffSummaries() []string {
	var out []string
	for _, a := range f.diffAttempts {
		if a.Summary != nil {
			out = append(out, a.DiffID+"|"+a.Summary.Summary)
		}
	}
	slices.Sort(out)
	return out
}

// attemptsByBill returns the recorded attempts keyed by bill.
func (f *summaryStore) attemptsByBill() map[string]repository.SummaryAttemptRow {
	out := make(map[string]repository.SummaryAttemptRow, len(f.attempts))
	for _, a := range f.attempts {
		out[a.BillID] = a
	}
	return out
}

// queueItems returns n queue items, hr-119-1 to hr-119-n.
func queueItems(n int) []repository.SummaryQueueItem {
	items := make([]repository.SummaryQueueItem, 0, n)
	for i := 1; i <= n; i++ {
		id := "hr-119-" + strconv.Itoa(i)
		items = append(items, repository.SummaryQueueItem{BillID: id, VersionID: "v-" + id, VersionCode: "ih"})
	}
	return items
}

// fakeSummarizer answers each bill or diff with the outcome set for its ID (ok by default; "" for
// no text), and records the bill contexts and the most calls in flight at once.
type fakeSummarizer struct {
	outcomes map[string]ai.Outcome
	// noCRS is Config's NoCRSContext (AI_CRS_CONTEXT=false).
	noCRS bool
	// noRule is Config's NoRuleContext (AI_RULE_CONTEXT=false).
	noRule bool
	// delay is how long each call takes.
	delay time.Duration

	mu       gosync.Mutex
	contexts []ai.BillContext
	inFlight atomic.Int32
	maxSeen  atomic.Int32
	diffs    atomic.Int32
}

func (f *fakeSummarizer) Config() ai.Config {
	return ai.Config{
		Model: "gemini-test", RequestType: ai.RequestTypeStandard, NoCRSContext: f.noCRS, NoRuleContext: f.noRule,
	}
}

func (*fakeSummarizer) result(outcome ai.Outcome, reason string) ai.Result {
	return ai.Result{
		Outcome: outcome, Reason: reason, Model: "gemini-test", ModelVersion: "gemini-test-001",
		RequestType: ai.RequestTypeStandard, InputTruncated: true,
		Usage:   ai.Usage{InputTokens: 1200, OutputTokens: 300, ThinkingTokens: 7},
		Latency: 1500 * time.Millisecond,
	}
}

func (f *fakeSummarizer) SummarizeBill(_ context.Context, bc ai.BillContext) (*ai.BillSummary, error) {
	n := f.inFlight.Add(1)
	defer f.inFlight.Add(-1)
	for {
		seen := f.maxSeen.Load()
		if n <= seen || f.maxSeen.CompareAndSwap(seen, n) {
			break
		}
	}
	time.Sleep(f.delay)
	f.mu.Lock()
	f.contexts = append(f.contexts, bc)
	f.mu.Unlock()

	outcome, ok := f.outcomes[bc.BillID]
	if !ok {
		outcome = ai.OutcomeOK
	}
	if outcome == "" {
		return nil, errors.New("bill " + bc.BillID + ": no text to summarize")
	}
	out := &ai.BillSummary{Result: f.result(outcome, "")}
	out.PromptVersion = ai.PromptVersionBill
	if outcome != ai.OutcomeOK {
		out.Reason = "reason-" + string(outcome)
		return out, &ai.AttemptError{Outcome: outcome, Reason: out.Reason}
	}
	out.ShortSummary, out.LongSummary, out.WhoItAffects = "Short.", "Long.", "Federal agencies."
	return out, nil
}

// SummarizeDiff answers each diff with the outcome set for its diff ID in outcomes (ok by default).
func (f *fakeSummarizer) SummarizeDiff(_ context.Context, dc ai.DiffContext) (*ai.DiffSummary, error) {
	f.diffs.Add(1)
	outcome, ok := f.outcomes[dc.DiffID]
	if !ok {
		outcome = ai.OutcomeOK
	}
	if outcome == "" {
		return nil, errors.New("diff " + dc.DiffID + ": no changes to summarize")
	}
	out := &ai.DiffSummary{Result: f.result(outcome, "")}
	out.PromptVersion = ai.PromptVersionDiff
	if outcome != ai.OutcomeOK {
		out.Reason = "reason-" + string(outcome)
		return out, &ai.AttemptError{Outcome: outcome, Reason: out.Reason}
	}
	out.Summary = "Changed " + dc.DiffID
	return out, nil
}

// jobService is a Service with the fake store and summarizer, the default job settings, the test
// clock and a JSON logger writing to the returned buffer.
func jobService(store *summaryStore, sum summarizer) (*Service, *bytes.Buffer) {
	logs := &bytes.Buffer{}
	return &Service{
		store: store, summarizer: sum, summaryJob: DefaultSummaryJobConfig(), clock: jobNow,
		logger: slog.New(slog.NewJSONHandler(logs, nil)),
	}, logs
}

// logLines returns the JSON log lines whose msg is msg.
func logLines(t *testing.T, logs *bytes.Buffer, msg string) []map[string]any {
	t.Helper()
	var out []map[string]any
	for line := range strings.SplitSeq(strings.TrimSpace(logs.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("log line %q: %v", line, err)
		}
		if m["msg"] == msg {
			out = append(out, m)
		}
	}
	return out
}

func TestSyncSummaries_BatchIsBoundedByTheCap(t *testing.T) {
	tests := []struct {
		name      string
		used      int
		limit     int
		wantLimit int
	}{
		{"fresh day takes a full batch", 0, 0, DefaultSummaryBatch},
		{"near the cap takes what's left", 2950, 0, 50},
		{"a run limit lowers the batch", 0, 10, 10},
		{"a run limit above the batch doesn't raise it", 0, 500, DefaultSummaryBatch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &summaryStore{used: tt.used, items: queueItems(300)}
			sum := &fakeSummarizer{}
			s, logs := jobService(store, sum)

			n, err := s.syncSummaries(t.Context(), 119, tt.limit)
			if err != nil {
				t.Fatal(err)
			}
			if len(store.queries) != 1 || store.queries[0].Limit != tt.wantLimit {
				t.Fatalf("queue queries %+v, want one with limit %d", store.queries, tt.wantLimit)
			}
			if n != tt.wantLimit || len(sum.contexts) != tt.wantLimit || len(store.attempts) != tt.wantLimit {
				t.Errorf("summarized %d, called %d, recorded %d; want %d of each", n, len(sum.contexts),
					len(store.attempts), tt.wantLimit)
			}
			if want := time.Date(2026, time.October, 5, 0, 0, 0, 0, time.UTC); !store.since.Equal(want) {
				t.Errorf("counted attempts since %v, want the start of the UTC day %v", store.since, want)
			}
			budget := logLines(t, logs, "summary_budget")
			if len(budget) != 1 || budget[0]["used"] != float64(tt.used) ||
				budget[0]["cap"] != float64(DefaultSummaryDailyCap) {
				t.Errorf("summary_budget logs = %v", budget)
			}
		})
	}
}

func TestSyncSummaries_CapReachedMakesNoCall(t *testing.T) {
	store := &summaryStore{used: DefaultSummaryDailyCap, items: queueItems(5), backlog: map[int]int{0: 5}}
	sum := &fakeSummarizer{}
	s, logs := jobService(store, sum)
	s.summaryJob.DiffsEnabled = true // the cap stops diffs too

	n, err := s.syncSummaries(t.Context(), 119, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 0 || len(store.queries) != 0 || len(sum.contexts) != 0 || sum.diffs.Load() != 0 {
		t.Errorf("n=%d queries=%d bill calls=%d diff calls=%d; want nothing", n, len(store.queries),
			len(sum.contexts), sum.diffs.Load())
	}
	budget := logLines(t, logs, "summary_budget")
	if len(budget) != 1 || budget[0]["remaining"] != float64(0) {
		t.Errorf("summary_budget logs = %v, want one with remaining 0", budget)
	}
	if len(logLines(t, logs, "summary_backlog")) != 1 {
		t.Error("no summary_backlog log when the cap is reached")
	}
}

func TestSyncSummaries_QueueQueryAndBacklog(t *testing.T) {
	store := &summaryStore{backlog: map[int]int{
		repository.SummaryTierVoted: 3, repository.SummaryTierRecent: 40, repository.SummaryTierOther: 900,
	}}
	s, logs := jobService(store, &fakeSummarizer{})
	s.summaryJob.RecentDays, s.summaryJob.ResummarizeOnPromptChange = 14, true

	if _, err := s.syncSummaries(t.Context(), 120, 0); err != nil {
		t.Fatal(err)
	}
	want := repository.SummaryQueueQuery{
		Congress: 120, Limit: DefaultSummaryBatch, PromptVersion: ai.PromptVersionBill, Model: "gemini-test",
		Now: jobNow(), RecentDays: 14, ResummarizeOnPromptChange: true, CRSContext: true,
		RuleContext: true,
	}
	if len(store.queries) != 1 || store.queries[0] != want {
		t.Errorf("queue queries = %+v, want %+v", store.queries, want)
	}
	backlog := logLines(t, logs, "summary_backlog")
	if len(backlog) != 1 {
		t.Fatalf("summary_backlog logs = %v, want one", backlog)
	}
	for key, n := range map[string]float64{"total": 943, "voted": 3, "recent": 40, "other": 900, "prompt_change": 0} {
		if backlog[0][key] != n {
			t.Errorf("summary_backlog %s = %v, want %v", key, backlog[0][key], n)
		}
	}
}

// The job runs AI_WORKERS summaries at once.
func TestSyncSummaries_Workers(t *testing.T) {
	store := &summaryStore{items: queueItems(24)}
	sum := &fakeSummarizer{delay: 30 * time.Millisecond}
	s, _ := jobService(store, sum)

	if _, err := s.syncSummaries(t.Context(), 119, 0); err != nil {
		t.Fatal(err)
	}
	if got := sum.maxSeen.Load(); got != DefaultSummaryWorkers {
		t.Errorf("at most %d calls in flight, want %d", got, DefaultSummaryWorkers)
	}
}

func TestSyncSummaries_AttemptBookkeeping(t *testing.T) {
	items := queueItems(6)
	store := &summaryStore{items: items}
	sum := &fakeSummarizer{outcomes: map[string]ai.Outcome{
		items[1].BillID: ai.OutcomeBlocked,
		items[2].BillID: ai.OutcomeError,
		items[3].BillID: ai.OutcomeInvalid,
		items[4].BillID: ai.OutcomeTruncatedOutput,
		items[5].BillID: "", // no text
	}}
	s, logs := jobService(store, sum)

	n, err := s.syncSummaries(t.Context(), 119, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || len(store.attempts) != len(items) {
		t.Fatalf("summarized %d, recorded %d attempts; want 1 and %d", n, len(store.attempts), len(items))
	}

	got := store.attemptsByBill()
	ok := got[items[0].BillID]
	wantSummary := repository.BillSummaryRow{
		BillID: items[0].BillID, ShortSummary: "Short.", LongSummary: "Long.", WhoItAffects: "Federal agencies.",
		ModelUsed: "gemini-test", SourceVersionID: items[0].VersionID, SourceVersionCode: "ih",
		SourceContentHash: "hash-" + items[0].BillID, PromptVersion: ai.PromptVersionBill,
		ModelVersion: "gemini-test-001", InputTruncated: true, InputTokens: 1200, OutputTokens: 300,
		ThinkingTokens: 7,
	}
	if ok.Outcome != "ok" || ok.Summary == nil || *ok.Summary != wantSummary {
		t.Errorf("ok attempt = %+v (summary %+v), want the summary with its provenance %+v", ok, ok.Summary,
			wantSummary)
	}
	for i, want := range []string{"ok", "blocked", "error", "invalid", "truncated_output", "error"} {
		a := got[items[i].BillID]
		if a.Outcome != want || a.ContentHash != "hash-"+items[i].BillID || a.PromptVersion != ai.PromptVersionBill ||
			a.Model != "gemini-test" || !a.AttemptedAt.Equal(jobNow()) {
			t.Errorf("%s: attempt %+v, want outcome %s for its hash, bill-v3, gemini-test at %v", items[i].BillID,
				a, want, jobNow())
		}
		if i > 0 && a.Summary != nil {
			t.Errorf("%s: a %s attempt stored a summary", items[i].BillID, want)
		}
	}
	if r := got[items[1].BillID].Reason; r != "reason-blocked" {
		t.Errorf("blocked reason = %q", r)
	}
	if r := got[items[5].BillID].Reason; !strings.Contains(r, "no text") {
		t.Errorf("no-text reason = %q", r)
	}

	// The summarizer got the bill's metadata and text from LoadBillContext.
	i := slices.IndexFunc(sum.contexts, func(c ai.BillContext) bool { return c.BillID == items[0].BillID })
	if bc := sum.contexts[i]; bc.Title != "Title of "+items[0].BillID ||
		bc.Text != "SEC. 1. Text of "+items[0].BillID+"." || bc.VersionName != "Introduced in House" ||
		len(bc.Committees) != 1 || bc.StatusDate.IsZero() {
		t.Errorf("bill context = %+v", bc)
	}

	checkSummaryResults(t, logs, len(items))
}

// checkSummaryResults checks there's one summary_result line per bill, with every field.
func checkSummaryResults(t *testing.T, logs *bytes.Buffer, want int) {
	t.Helper()
	results := logLines(t, logs, "summary_result")
	if len(results) != want {
		t.Fatalf("%d summary_result lines, want %d", len(results), want)
	}
	fields := []string{
		"bill_id", "kind", "outcome", "reason", "model", "model_version", "prompt_version", "input_tokens",
		"output_tokens", "thinking_tokens", "input_truncated", "latency_ms", "request_type",
	}
	for _, r := range results {
		for _, f := range fields {
			if _, ok := r[f]; !ok {
				t.Errorf("summary_result %v has no %s", r, f)
			}
		}
		if r["kind"] != "bill" || r["model"] != "gemini-test" || r["request_type"] != "standard" {
			t.Errorf("summary_result = %v", r)
		}
		wantLevel := "INFO"
		if r["outcome"] != "ok" {
			wantLevel = "WARN"
		}
		if r["level"] != wantLevel {
			t.Errorf("summary_result %v at %v, want %s", r["outcome"], r["level"], wantLevel)
		}
		if r["outcome"] == "ok" && (r["input_tokens"] != float64(1200) || r["latency_ms"] != float64(1500) ||
			r["model_version"] != "gemini-test-001" || r["input_truncated"] != true) {
			t.Errorf("ok summary_result = %v", r)
		}
	}
}

// A bill the store can't load or record fails the step; the others are still recorded.
func TestSyncSummaries_StoreErrorsFailTheStep(t *testing.T) {
	items := queueItems(3)
	store := &summaryStore{items: items, loadErr: map[string]error{items[1].BillID: errors.New("spanner: deadline")}}
	sum := &fakeSummarizer{}
	s, _ := jobService(store, sum)

	n, err := s.syncSummaries(t.Context(), 119, 0)
	if err == nil || !strings.Contains(err.Error(), "1 of 3 bills") {
		t.Fatalf("err = %v, want 1 of 3 bills not loaded or recorded", err)
	}
	if n != 2 || len(store.attempts) != 2 || len(sum.contexts) != 2 {
		t.Errorf("summarized %d, recorded %d, called %d; want 2 of each", n, len(store.attempts), len(sum.contexts))
	}

	store = &summaryStore{items: items, recordErr: errors.New("spanner: aborted")}
	s, _ = jobService(store, &fakeSummarizer{})
	if _, err = s.syncSummaries(t.Context(), 119, 0); err == nil || !strings.Contains(err.Error(), "3 of 3 bills") {
		t.Errorf("err = %v, want 3 of 3 bills not recorded", err)
	}
}

// cancelingSummarizer cancels the run during its call and then fails, as a timed-out request does.
type cancelingSummarizer struct {
	fakeSummarizer

	cancel context.CancelFunc
}

func (c *cancelingSummarizer) SummarizeBill(ctx context.Context, _ ai.BillContext) (*ai.BillSummary, error) {
	c.cancel()
	out := &ai.BillSummary{Result: c.result(ai.OutcomeError, "canceled")}
	return out, errors.Join(ctx.Err(), &ai.AttemptError{Outcome: ai.OutcomeError, Reason: "canceled"})
}

// A run stopped by its timeout or a shutdown doesn't charge the bill a failed attempt.
func TestSyncSummaries_InterruptedIsNotRecorded(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	store := &summaryStore{items: queueItems(1)}
	s, _ := jobService(store, &cancelingSummarizer{cancel: cancel})

	if _, err := s.syncSummaries(ctx, 119, 0); err != nil {
		t.Fatal(err)
	}
	if len(store.attempts) != 0 {
		t.Errorf("recorded %+v for an interrupted run", store.attempts)
	}
}

func TestSyncSummaries_Diffs(t *testing.T) {
	diffs := []repository.DiffRef{
		{DiffID: "d1", BillID: "hr-119-1", DiffContent: json.RawMessage(`{"added":["SEC. 2."]}`)},
		{DiffID: "d2", BillID: "hr-119-1", DiffContent: json.RawMessage(`{"added":["SEC. 3."]}`)},
	}

	// Off by default: no diff is summarized.
	store := &summaryStore{items: queueItems(2), diffs: diffs}
	sum := &fakeSummarizer{}
	s, _ := jobService(store, sum)
	if _, err := s.syncSummaries(t.Context(), 119, 0); err != nil {
		t.Fatal(err)
	}
	if sum.diffs.Load() != 0 || len(store.diffQueries) != 0 {
		t.Errorf("summarized %d diffs with diff summaries off", sum.diffs.Load())
	}

	// On: diffs take what's left of the day's budget after the bills, and log summary_result.
	store = &summaryStore{used: DefaultSummaryDailyCap - 3, items: queueItems(2), diffs: diffs}
	sum = &fakeSummarizer{}
	s, logs := jobService(store, sum)
	s.summaryJob.DiffsEnabled = true
	n, err := s.syncSummaries(t.Context(), 119, 0)
	if err != nil {
		t.Fatal(err)
	}
	wantQuery := repository.DiffSummaryQueueQuery{
		Congress: 119, Limit: 1, PromptVersion: "diff-v2", Model: "gemini-test", Now: jobNow(),
	}
	if len(store.diffQueries) != 1 || store.diffQueries[0] != wantQuery {
		t.Errorf("diff queries = %+v, want %+v", store.diffQueries, wantQuery)
	}
	if got := store.diffSummaries(); n != 3 || !slices.Equal(got, []string{"d1|Changed d1"}) {
		t.Errorf("n=%d diff summaries=%v; want 2 bills, then 1 diff", n, got)
	}
	var diffResults []map[string]any
	for _, r := range logLines(t, logs, "summary_result") {
		if r["kind"] == "diff" {
			diffResults = append(diffResults, r)
		}
	}
	if len(diffResults) != 1 || diffResults[0]["diff_id"] != "d1" || diffResults[0]["prompt_version"] != "diff-v2" {
		t.Errorf("diff summary_result lines = %v", diffResults)
	}
}

// diffRefs returns n diffs of hr-119-1, d1 to dn.
func diffRefs(n int) []repository.DiffRef {
	out := make([]repository.DiffRef, 0, n)
	for i := 1; i <= n; i++ {
		id := "d" + strconv.Itoa(i)
		out = append(out, repository.DiffRef{
			DiffID: id, BillID: "hr-119-1", DiffContent: json.RawMessage(`{"added":["SEC. ` + id + `."]}`),
		})
	}
	return out
}

// Every diff call is recorded, with the summary and its provenance only when it's ok (#440).
func TestSyncSummaries_DiffAttemptBookkeeping(t *testing.T) {
	store := &summaryStore{diffs: diffRefs(5)}
	sum := &fakeSummarizer{outcomes: map[string]ai.Outcome{
		"d2": ai.OutcomeBlocked, "d3": ai.OutcomeError, "d4": ai.OutcomeTruncatedOutput, "d5": "", // no changes
	}}
	s, logs := jobService(store, sum)
	s.summaryJob.DiffsEnabled = true

	n, err := s.syncSummaries(t.Context(), 119, 0)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 || len(store.diffAttempts) != 5 {
		t.Fatalf("summarized %d, recorded %d diff attempts; want 1 and 5", n, len(store.diffAttempts))
	}
	got := make(map[string]repository.DiffSummaryAttemptRow, len(store.diffAttempts))
	for _, a := range store.diffAttempts {
		got[a.DiffID] = a
	}
	wantSummary := repository.DiffSummaryRow{
		DiffID: "d1", Summary: "Changed d1", ModelUsed: "gemini-test", PromptVersion: ai.PromptVersionDiff,
		ModelVersion: "gemini-test-001", InputTokens: 1200, OutputTokens: 300,
	}
	if ok := got["d1"]; ok.Summary == nil || *ok.Summary != wantSummary {
		t.Errorf("ok attempt = %+v (summary %+v), want %+v", ok, ok.Summary, wantSummary)
	}
	for id, want := range map[string]string{
		"d1": "ok", "d2": "blocked", "d3": "error", "d4": "truncated_output", "d5": "error",
	} {
		a := got[id]
		if a.Outcome != want || a.PromptVersion != ai.PromptVersionDiff || a.Model != "gemini-test" ||
			!a.AttemptedAt.Equal(jobNow()) {
			t.Errorf("%s: attempt %+v, want outcome %s, diff-v2, gemini-test at %v", id, a, want, jobNow())
		}
		if id != "d1" && a.Summary != nil {
			t.Errorf("%s: a %s attempt stored a summary", id, want)
		}
	}
	if r := got["d2"].Reason; r != "reason-blocked" {
		t.Errorf("blocked reason = %q", r)
	}
	if r := got["d5"].Reason; !strings.Contains(r, "no changes") {
		t.Errorf("no-changes reason = %q", r)
	}
	results := logLines(t, logs, "summary_result")
	if len(results) != 5 {
		t.Errorf("%d summary_result lines, want one per diff", len(results))
	}
}

// A diff attempt the store can't record fails the step; the others are still recorded.
func TestSyncSummaries_DiffStoreErrorFailsTheStep(t *testing.T) {
	store := &summaryStore{diffs: diffRefs(3), diffRecordErr: map[string]error{"d2": errors.New("spanner: aborted")}}
	s, _ := jobService(store, &fakeSummarizer{})
	s.summaryJob.DiffsEnabled = true

	n, err := s.syncSummaries(t.Context(), 119, 0)
	if err == nil || !strings.Contains(err.Error(), "1 of 3 diffs") {
		t.Fatalf("err = %v, want 1 of 3 diffs not recorded", err)
	}
	if got := store.diffSummaries(); n != 2 || !slices.Equal(got, []string{"d1|Changed d1", "d3|Changed d3"}) {
		t.Errorf("n=%d diff summaries=%v, want d1 and d3", n, got)
	}
}

func (c *cancelingSummarizer) SummarizeDiff(ctx context.Context, _ ai.DiffContext) (*ai.DiffSummary, error) {
	c.cancel()
	out := &ai.DiffSummary{Result: c.result(ai.OutcomeError, "canceled")}
	return out, errors.Join(ctx.Err(), &ai.AttemptError{Outcome: ai.OutcomeError, Reason: "canceled"})
}

// A run stopped during a diff call doesn't charge the diff a failed attempt.
func TestSyncSummaries_InterruptedDiffIsNotRecorded(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	store := &summaryStore{diffs: diffRefs(1)}
	s, _ := jobService(store, &cancelingSummarizer{cancel: cancel})
	s.summaryJob.DiffsEnabled = true

	if _, err := s.syncSummaries(ctx, 119, 0); err != nil {
		t.Fatal(err)
	}
	if len(store.diffAttempts) != 0 {
		t.Errorf("recorded %+v for an interrupted run", store.diffAttempts)
	}
}

// A summary run counts each bill and diff on justabill.pipeline.items, and the job's finished
// record adds them up: a stored summary is ok, a blocked bill or diff skipped (it's never retried),
// and anything else that will be retried failed.
func TestSyncSummaries_CountsItems(t *testing.T) {
	tel := obstest.New(t)
	items := queueItems(4)
	store := &summaryStore{
		items:   items,
		loadErr: map[string]error{items[3].BillID: errors.New("spanner: deadline")},
		diffs: []repository.DiffRef{
			{DiffID: "d1", BillID: "hr-119-1", DiffContent: json.RawMessage(`{"added":["SEC. 2."]}`)},
			{DiffID: "d2", BillID: "hr-119-1", DiffContent: json.RawMessage(`{"added":["SEC. 3."]}`)},
			{DiffID: "d3", BillID: "hr-119-1", DiffContent: json.RawMessage(`{"added":["SEC. 4."]}`)},
		},
	}
	sum := &fakeSummarizer{outcomes: map[string]ai.Outcome{
		items[1].BillID: ai.OutcomeBlocked, items[2].BillID: ai.OutcomeError, "d2": ai.OutcomeInvalid,
		"d3": ai.OutcomeBlocked,
	}}
	s, _ := jobService(store, sum)
	s.summaryJob.DiffsEnabled = true

	outcome, err := obs.Job(t.Context(), tel.Logger, "sync-summaries", func(ctx context.Context) error {
		_, err := s.syncSummaries(ctx, 119, 0)
		return err
	})
	if outcome != semconv.JobOutcomeFailed || err == nil || !strings.Contains(err.Error(), "1 of 4 bills") {
		t.Fatalf("Job = %q, %v; want failed with 1 of 4 bills not loaded", outcome, err)
	}

	m, ok := tel.Metric(t, semconv.PipelineItemsName)
	if !ok {
		t.Fatalf("no %s metric", semconv.PipelineItemsName)
	}
	points, _ := m.Data.(metricdata.Sum[int64])
	got := map[string]int64{}
	for _, dp := range points.DataPoints {
		o, _ := dp.Attributes.Value(semconv.ItemOutcomeKey)
		got[o.AsString()] = dp.Value
	}
	// Bills: 1 ok, 1 blocked, 1 error, 1 not loaded. Diffs: 1 ok, 1 invalid, 1 blocked.
	if want := map[string]int64{"ok": 2, "skipped": 2, "failed": 3}; !maps.Equal(got, want) {
		t.Errorf("items = %v, want %v", got, want)
	}

	finished := finishedAttrs(t, tel)
	if finished[string(semconv.JobItemsOKKey)] != "2" || finished[string(semconv.JobItemsFailedKey)] != "3" {
		t.Errorf("finished record = %v, want 2 items ok and 3 failed", finished)
	}
}

// finishedAttrs returns the attributes of the one justabill.pipeline.job.finished record.
func finishedAttrs(t *testing.T, tel *obstest.Telemetry) map[string]string {
	t.Helper()
	var found []map[string]string
	for _, r := range tel.Logs() {
		if r.Body().AsString() != semconv.PipelineJobFinishedEvent {
			continue
		}
		attrs := map[string]string{}
		r.WalkAttributes(func(kv attribute.KeyValue) bool {
			attrs[string(kv.Key)] = kv.Value.String()
			return true
		})
		found = append(found, attrs)
	}
	if len(found) != 1 {
		t.Fatalf("%d %s records, want 1", len(found), semconv.PipelineJobFinishedEvent)
	}
	return found[0]
}

// fakeGemini answers every GenerateContent call with body and records each prompt.
func fakeGemini(t *testing.T, body string) (*ai.Summarizer, *[]string) {
	t.Helper()
	var (
		mu      gosync.Mutex
		prompts []string
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		mu.Lock()
		prompts = append(prompts, string(raw))
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	client, err := genai.NewClient(t.Context(), &genai.ClientConfig{
		Project: "test-project", Location: "global", Backend: genai.BackendVertexAI,
		HTTPClient: srv.Client(), HTTPOptions: genai.HTTPOptions{BaseURL: srv.URL},
	})
	if err != nil {
		t.Fatal(err)
	}
	s, err := ai.NewSummarizerWithClient(client, ai.Config{})
	if err != nil {
		t.Fatal(err)
	}
	return s, &prompts
}

func answer(text, finishReason string) string {
	quoted, _ := json.Marshal(text)
	return `{"candidates":[{"content":{"role":"model","parts":[{"text":` + string(quoted) + `}]},` +
		`"finishReason":"` + finishReason + `"}],"modelVersion":"gemini-3.8-flash-002",` +
		`"usageMetadata":{"promptTokenCount":900,"candidatesTokenCount":120,"thoughtsTokenCount":0}}`
}

// End to end through the real summarizer on a fake Gemini: the prompt cites the bill from its
// loaded metadata, and the stored summary carries the served model version and token counts.
func TestSyncSummaries_WithTheRealSummarizer(t *testing.T) {
	store := &summaryStore{items: queueItems(1)}
	summarizer, prompts := fakeGemini(t, answer(
		`{"short_summary":"Short.","long_summary":"Long.","who_it_affects":"Federal agencies."}`, "STOP"))
	s, _ := jobService(store, nil)
	s.SetSummarizer(summarizer)

	if _, err := s.syncSummaries(t.Context(), 119, 0); err != nil {
		t.Fatal(err)
	}
	if len(*prompts) != 1 || !strings.Contains((*prompts)[0], "Title of "+store.items[0].BillID) {
		t.Errorf("prompts = %v, want one with the loaded title", *prompts)
	}
	if len(store.attempts) != 1 || store.attempts[0].Summary == nil {
		t.Fatalf("attempts = %+v, want one with a summary", store.attempts)
	}
	sum := store.attempts[0].Summary
	if sum.ModelUsed != ai.DefaultModel || sum.ModelVersion != "gemini-3.8-flash-002" || sum.InputTokens != 900 ||
		sum.OutputTokens != 120 || sum.WhoItAffects != "Federal agencies." {
		t.Errorf("summary = %+v", sum)
	}
	a := store.attempts[0]
	if a.Model != ai.DefaultModel || a.RequestType != ai.RequestTypeStandard || a.BatchID != "" {
		t.Errorf("attempt model, request type, batch = %q, %q, %q; want %q, %q and none",
			a.Model, a.RequestType, a.BatchID, ai.DefaultModel, ai.RequestTypeStandard)
	}
}

func TestSyncSummaries_BlockedByTheRealSummarizer(t *testing.T) {
	store := &summaryStore{items: queueItems(1)}
	summarizer, _ := fakeGemini(t, answer("", "SAFETY"))
	s, _ := jobService(store, nil)
	s.SetSummarizer(summarizer)

	if _, err := s.syncSummaries(t.Context(), 119, 0); err != nil {
		t.Fatal(err)
	}
	if len(store.attempts) != 1 || store.attempts[0].Outcome != "blocked" || store.attempts[0].Reason != "SAFETY" ||
		store.attempts[0].Summary != nil {
		t.Errorf("attempts = %+v, want one blocked by SAFETY with no summary", store.attempts)
	}
}

func TestSetSummarizer_NilTurnsSummariesOff(t *testing.T) {
	s, _ := jobService(&summaryStore{}, &fakeSummarizer{})
	s.SetSummarizer(nil)
	if s.summarizer != nil {
		t.Fatal("a nil *ai.Summarizer left a non-nil summarizer")
	}
	if err := s.SyncSummaries(t.Context(), 119, 0); err != nil {
		t.Errorf("SyncSummaries without a summarizer = %v", err)
	}
}

func TestSummaryJobConfigFrom(t *testing.T) {
	get := func(env map[string]string) func(string) string {
		return func(key string) string { return env[key] }
	}
	cfg, err := SummaryJobConfigFrom(get(nil))
	if err != nil || cfg != DefaultSummaryJobConfig() {
		t.Fatalf("defaults = %+v, %v", cfg, err)
	}
	if cfg.Batch != 200 || cfg.Workers != 8 || cfg.DailyCap != 3000 || cfg.RecentDays != 30 ||
		cfg.ResummarizeOnPromptChange || cfg.DiffsEnabled || cfg.LawBatch != 50 || cfg.LawDailyCap != 500 {
		t.Errorf("defaults = %+v, want the design's", cfg)
	}

	cfg, err = SummaryJobConfigFrom(get(map[string]string{
		"ai_summary_batch": "50", "ai_workers": " 4 ", "ai_daily_request_cap": "8000", "ai_recent_days": "14",
		"ai_resummarize_on_prompt_change": "true", "ai_diff_summaries_enabled": "1",
		"ai_law_batch": "20", "ai_law_daily_cap": "100",
	}))
	want := SummaryJobConfig{
		Batch: 50, Workers: 4, DailyCap: 8000, RecentDays: 14, ResummarizeOnPromptChange: true, DiffsEnabled: true,
		LawBatch: 20, LawDailyCap: 100,
	}
	if err != nil || cfg != want {
		t.Errorf("overrides = %+v, %v; want %+v", cfg, err, want)
	}

	for key, value := range map[string]string{
		"ai_summary_batch": "0", "ai_workers": "-2", "ai_daily_request_cap": "lots", "ai_recent_days": "1.5",
		"ai_diff_summaries_enabled": "sometimes", "ai_law_daily_cap": "0",
	} {
		if _, err = SummaryJobConfigFrom(get(map[string]string{key: value})); err == nil ||
			!strings.Contains(err.Error(), strings.ToUpper(key)) {
			t.Errorf("%s=%q: err = %v, want an error naming %s", key, value, err, strings.ToUpper(key))
		}
	}
}

// With the CRS context on, the prompt gets the bill's latest CRS summary and the summary stores its
// hash; with it off (AI_CRS_CONTEXT=false), neither, and the queue doesn't apply the CRS rule.
func TestSyncSummaries_CRSContext(t *testing.T) {
	items := queueItems(2)
	crs := &repository.SummaryCRSContext{
		ActionDesc: "Reported to House", ActionDate: time.Date(2025, time.June, 1, 0, 0, 0, 0, time.UTC),
		Text: "This bill extends tax provisions.", ContentHash: "crs-hash-1",
	}
	for _, noCRS := range []bool{false, true} {
		store := &summaryStore{items: items, crs: map[string]*repository.SummaryCRSContext{items[0].BillID: crs}}
		sum := &fakeSummarizer{noCRS: noCRS}
		s, _ := jobService(store, sum)
		if _, err := s.syncSummaries(t.Context(), 119, 0); err != nil {
			t.Fatal(err)
		}

		if len(store.queries) != 1 || store.queries[0].CRSContext == noCRS {
			t.Errorf("noCRS %v: queue queries = %+v, want CRSContext %v", noCRS, store.queries, !noCRS)
		}
		var wantCRS *ai.CRSContext
		wantHash := ""
		if !noCRS {
			wantCRS = &ai.CRSContext{VersionDesc: crs.ActionDesc, ActionDate: crs.ActionDate, Text: crs.Text}
			wantHash = crs.ContentHash
		}
		for _, bc := range sum.contexts {
			want := wantCRS
			if bc.BillID != items[0].BillID {
				want = nil
			}
			if !reflect.DeepEqual(bc.CRSSummary, want) {
				t.Errorf("noCRS %v: %s CRS context = %+v, want %+v", noCRS, bc.BillID, bc.CRSSummary, want)
			}
		}
		got := store.attemptsByBill()
		if h := got[items[0].BillID].Summary.SourceCRSHash; h != wantHash {
			t.Errorf("noCRS %v: source CRS hash = %q, want %q", noCRS, h, wantHash)
		}
		if h := got[items[1].BillID].Summary.SourceCRSHash; h != "" {
			t.Errorf("noCRS %v: source CRS hash of a bill without one = %q, want none", noCRS, h)
		}
	}
}

// overdraftRule is a matched disapproved withdrawal as LoadBillContext returns it.
func overdraftRule() *repository.SummaryRuleContext {
	effective := time.Date(2025, time.October, 1, 0, 0, 0, 0, time.UTC)
	return &repository.SummaryRuleContext{
		Title: "Overdraft Lending", Agency: "Bureau of Consumer Financial Protection", ContextHash: "rule-hash-1",
		Document: &repository.SummaryRuleDocument{
			Title: "Overdraft Lending: Very Large Financial Institutions", Agencies: []string{"CFPB"},
			DocType: "Rule", Action: "Final rule.", Citation: "89 FR 106768",
			Published: time.Date(2024, time.December, 30, 0, 0, 0, 0, time.UTC), EffectiveOn: &effective,
			Abstract: "The CFPB amends Regulation Z.",
		},
		Withdrawn: &repository.SummaryRuleDocument{
			Title: "Overdraft Guidance", Citation: "88 FR 100",
			Published: time.Date(2023, time.January, 3, 0, 0, 0, 0, time.UTC),
		},
	}
}

// With the rule context on, the prompt gets a CRA resolution's disapproved rule and the summary
// stores its context hash; with it off (AI_RULE_CONTEXT=false), neither, and the queue doesn't
// apply the rule-change rule.
func TestSyncSummaries_RuleContext(t *testing.T) {
	items := queueItems(2)
	rule := overdraftRule()
	effective := *rule.Document.EffectiveOn
	for _, noRule := range []bool{false, true} {
		store := &summaryStore{items: items, rules: map[string]*repository.SummaryRuleContext{items[0].BillID: rule}}
		sum := &fakeSummarizer{noRule: noRule}
		s, _ := jobService(store, sum)
		if _, err := s.syncSummaries(t.Context(), 119, 0); err != nil {
			t.Fatal(err)
		}

		if len(store.queries) != 1 || store.queries[0].RuleContext == noRule || !store.queries[0].CRSContext {
			t.Errorf("noRule %v: queue queries = %+v, want RuleContext %v", noRule, store.queries, !noRule)
		}
		var wantRule *ai.RuleContext
		wantHash := ""
		if !noRule {
			wantRule = &ai.RuleContext{
				Title: rule.Title, Agency: rule.Agency,
				Document: &ai.RuleDocument{
					Title: rule.Document.Title, Agencies: []string{"CFPB"}, DocType: "Rule", Action: "Final rule.",
					Citation: "89 FR 106768", Published: rule.Document.Published, EffectiveOn: effective,
					Abstract: "The CFPB amends Regulation Z.",
				},
				Withdrawn: &ai.RuleDocument{
					Title: "Overdraft Guidance", Citation: "88 FR 100", Published: rule.Withdrawn.Published,
				},
			}
			wantHash = rule.ContextHash
		}
		for _, bc := range sum.contexts {
			want := wantRule
			if bc.BillID != items[0].BillID {
				want = nil
			}
			if !reflect.DeepEqual(bc.Rule, want) {
				t.Errorf("noRule %v: %s rule context = %+v, want %+v", noRule, bc.BillID, bc.Rule, want)
			}
		}
		got := store.attemptsByBill()
		if h := got[items[0].BillID].Summary.SourceRuleHash; h != wantHash {
			t.Errorf("noRule %v: source rule hash = %q, want %q", noRule, h, wantHash)
		}
		if h := got[items[1].BillID].Summary.SourceRuleHash; h != "" {
			t.Errorf("noRule %v: source rule hash of a bill that isn't a CRA resolution = %q, want none", noRule, h)
		}
	}
}
