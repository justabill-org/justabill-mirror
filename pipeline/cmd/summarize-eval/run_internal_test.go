package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/justabill-org/justabill/pipeline/internal/ai"
)

// fakeSummarizer answers from a table keyed by bill ID; bills not in it get an ok summary.
type fakeSummarizer struct {
	mu      sync.Mutex
	model   string
	answers map[string]ai.Result
	calls   []string
}

func (f *fakeSummarizer) Config() ai.Config { return ai.Config{Model: f.model, ThinkingLevel: "LOW"} }

func (f *fakeSummarizer) SummarizeBill(_ context.Context, bc ai.BillContext) (*ai.BillSummary, error) {
	f.mu.Lock()
	f.calls = append(f.calls, bc.BillID)
	res, ok := f.answers[bc.BillID]
	f.mu.Unlock()
	if !ok {
		return &ai.BillSummary{
			Result: ai.Result{ //nolint:modernize // BillSummary embeds Result; the literal needs the name
				Outcome: ai.OutcomeOK, Model: f.model, PromptVersion: "bill-v2",
				Usage: ai.Usage{InputTokens: 1000, OutputTokens: 200}, Latency: 1500 * time.Millisecond,
			},
			ShortSummary: "A landmark bill about " + bc.Title + ".",
			LongSummary:  "It does things.",
			WhoItAffects: "Agencies.",
		}, nil
	}
	return &ai.BillSummary{Result: res}, &ai.AttemptError{Outcome: res.Outcome, Reason: res.Reason}
}

func testBills() []evalBill {
	return []evalBill{
		{BillID: "hr-119-1", Category: categoryLong, Title: "Reconciliation"},
		{BillID: "hr-119-2", Category: categoryFloorVote, Title: "Landmark Act"},
		{BillID: "s-119-3", Category: categoryRandom, Title: "Parks"},
	}
}

func newTestRunner(t *testing.T, workers int) (*runner, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "results.jsonl")
	res, err := openResults(path)
	if err != nil {
		t.Fatal(err)
	}
	return &runner{
		bills:   testBills(),
		texts:   map[string]string{"hr-119-1": "<bill/>", "hr-119-2": "<bill/>", "s-119-3": "<bill/>"},
		results: res,
		check:   newChecker(),
		workers: workers,
		logger:  slog.New(slog.DiscardHandler),
		now:     func() time.Time { return time.Date(2026, 9, 27, 1, 0, 0, 0, time.UTC) },
	}, path
}

func TestRunRecordsEveryBillAndChecksIt(t *testing.T) {
	r, path := newTestRunner(t, 2)
	s := &fakeSummarizer{model: "m", answers: map[string]ai.Result{
		"s-119-3": {Outcome: ai.OutcomeBlocked, Reason: "SAFETY", Model: "m"},
	}}
	if err := r.run(t.Context(), s); err != nil {
		t.Fatal(err)
	}
	reread, err := openResults(path)
	if err != nil {
		t.Fatal(err)
	}
	got := reread.latest("m")
	if len(got) != 3 {
		t.Fatalf("%d records, want 3", len(got))
	}
	ok := got["hr-119-1"]
	if ok.Outcome != "ok" || ok.InputTokens != 1000 || ok.LatencyMS != 1500 || ok.ThinkingLevel != "LOW" ||
		ok.ShortSummary == "" || !ok.RanAt.Equal(r.now()) {
		t.Errorf("hr-119-1 = %+v", ok)
	}
	// "landmark" isn't in hr-119-1's text or title, but it is in hr-119-2's title.
	if len(ok.LoadedTerms) != 1 || ok.LoadedTerms[0] != "landmark" {
		t.Errorf("hr-119-1 loaded terms = %v, want [landmark]", ok.LoadedTerms)
	}
	if lt := got["hr-119-2"].LoadedTerms; len(lt) != 0 {
		t.Errorf("hr-119-2 loaded terms = %v, want none: the title uses the word", lt)
	}
	if b := got["s-119-3"]; b.Outcome != "blocked" || b.Reason != "SAFETY" {
		t.Errorf("s-119-3 = %+v, want blocked", b)
	}
}

func TestRunResumesAndRetriesOnlyErrors(t *testing.T) {
	r, _ := newTestRunner(t, 1)
	first := &fakeSummarizer{model: "m", answers: map[string]ai.Result{
		"hr-119-2": {Outcome: ai.OutcomeError, Reason: "timeout"},
		"s-119-3":  {Outcome: ai.OutcomeInvalid, Reason: "short_summary empty"},
	}}
	if err := r.run(t.Context(), first); err != nil {
		t.Fatal(err)
	}
	second := &fakeSummarizer{model: "m"}
	if err := r.run(t.Context(), second); err != nil {
		t.Fatal(err)
	}
	if len(second.calls) != 1 || second.calls[0] != "hr-119-2" {
		t.Errorf("second run called %v, want only the bill that ended in an error", second.calls)
	}
	if got := r.results.latest("m")["hr-119-2"].Outcome; got != "ok" {
		t.Errorf("hr-119-2's latest outcome = %s, want ok", got)
	}
	// Another model starts from scratch.
	other := &fakeSummarizer{model: "n"}
	if err := r.run(t.Context(), other); err != nil || len(other.calls) != 3 {
		t.Errorf("model n: err %v, %d calls; want 3", err, len(other.calls))
	}
}

func TestRunStopsWhenTheModelIsNotServedOrBudgetIsSpent(t *testing.T) {
	for _, reason := range []string{"http 404 404 Not Found", "http 429 429 Too Many Requests"} {
		t.Run(reason, func(t *testing.T) {
			r, path := newTestRunner(t, 1)
			s := &fakeSummarizer{model: "m", answers: map[string]ai.Result{
				"hr-119-1": {Outcome: ai.OutcomeError, Reason: reason},
			}}
			err := r.run(t.Context(), s)
			if !errors.Is(err, errStopModel) {
				t.Fatalf("err = %v, want errStopModel", err)
			}
			if len(s.calls) != 1 {
				t.Errorf("%d calls after the stop, want 1", len(s.calls))
			}
			if _, statErr := os.Stat(path); !errors.Is(statErr, os.ErrNotExist) {
				t.Error("a stopped attempt was recorded; a rerun should retry it")
			}
		})
	}
}

func TestOpenResultsRejectsACorruptFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "results.jsonl")
	if err := os.WriteFile(path, []byte("{\"bill_id\":\"x\"}\nnot json\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := openResults(path); err == nil || !strings.Contains(err.Error(), "parse") {
		t.Errorf("err = %v, want a parse error", err)
	}
}

func TestSplitModels(t *testing.T) {
	got := splitModels(" gemini-3.5-flash, ,gemini-3.8-flash,")
	if len(got) != 2 || got[0] != "gemini-3.5-flash" || got[1] != "gemini-3.8-flash" {
		t.Errorf("splitModels = %v", got)
	}
}

func TestWriteReports(t *testing.T) {
	r, _ := newTestRunner(t, 1)
	if err := r.run(t.Context(), &fakeSummarizer{model: "gemini-3.8-flash"}); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	models := []string{"gemini-3.8-flash", "gemini-3.5-flash"}
	if err := writeReports(dir, models, r.bills, r.results, r.now()); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"report-gemini-3.8-flash.md", "report-gemini-3.5-flash.md", "compare.md"} {
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil || len(data) == 0 {
			t.Errorf("%s: %v, %d bytes", name, err, len(data))
		}
	}
}
