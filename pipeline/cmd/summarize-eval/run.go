package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/justabill-org/justabill/pipeline/internal/ai"
)

// record is one line of results.jsonl: one bill summarized by one model, with the bill's CRS
// summary in the prompt when CRS is set and its disapproved rule when Rule is. CRSCopied is the
// share of the summary's 8-word sequences found in the bill's CRS summary, for bills that have
// one; RuleSignals are the automatic checks of a CRA resolution's summary.
type record struct {
	BillID         string       `json:"bill_id"`
	Category       string       `json:"category"`
	Model          string       `json:"model"`
	CRS            bool         `json:"crs,omitempty"`
	CRSCopied      *float64     `json:"crs_copied_8grams,omitempty"`
	Rule           bool         `json:"rule,omitempty"`
	RuleSignals    *ruleSignals `json:"rule_signals,omitempty"`
	ModelVersion   string       `json:"model_version,omitempty"`
	PromptVersion  string       `json:"prompt_version,omitempty"`
	ThinkingLevel  string       `json:"thinking_level"`
	Outcome        string       `json:"outcome"`
	Reason         string       `json:"reason,omitempty"`
	ShortSummary   string       `json:"short_summary,omitempty"`
	LongSummary    string       `json:"long_summary,omitempty"`
	WhoItAffects   string       `json:"who_it_affects,omitempty"`
	InputTokens    int64        `json:"input_tokens"`
	OutputTokens   int64        `json:"output_tokens"`
	ThinkingTokens int64        `json:"thinking_tokens"`
	LatencyMS      int64        `json:"latency_ms"`
	InputTruncated bool         `json:"input_truncated,omitempty"`
	LoadedTerms    []string     `json:"loaded_terms,omitempty"`
	PartyNames     []string     `json:"party_names,omitempty"`
	RanAt          time.Time    `json:"ran_at"`
}

// billSummarizer is the part of *ai.Summarizer the eval uses.
type billSummarizer interface {
	SummarizeBill(ctx context.Context, bc ai.BillContext) (*ai.BillSummary, error)
	Config() ai.Config
}

// results is results.jsonl: every attempt, appended as it finishes. The last record for a bill
// and model is the one that counts, so a rerun retries only what ended in an error.
type results struct {
	mu      sync.Mutex
	path    string
	records []record
}

// openResults reads the records already in path, if any.
func openResults(path string) (*results, error) {
	r := &results{path: path}
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return r, nil
	}
	if err != nil {
		return nil, fmt.Errorf("open results: %w", err)
	}
	defer f.Close()
	scanner := bufio.NewScanner(f)
	const maxLine = 1 << 20
	scanner.Buffer(make([]byte, 0, maxLine), maxLine)
	for scanner.Scan() {
		var rec record
		if err = json.Unmarshal(scanner.Bytes(), &rec); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		r.records = append(r.records, rec)
	}
	if err = scanner.Err(); err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return r, nil
}

// append writes rec to the file and keeps it.
func (r *results) append(rec record) error {
	line, err := json.Marshal(rec)
	if err != nil {
		return fmt.Errorf("encode result: %w", err)
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	f, err := os.OpenFile(r.path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("open results: %w", err)
	}
	if _, err = f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		return fmt.Errorf("write results: %w", err)
	}
	if err = f.Close(); err != nil {
		return fmt.Errorf("close results: %w", err)
	}
	r.records = append(r.records, rec)
	return nil
}

// variant is what a run adds to the prompt: the bills' CRS summaries (--crs) or the rules CRA
// resolutions disapprove (--rule). The zero value adds nothing.
type variant struct {
	crs  bool
	rule bool
}

// label names a run of model: the model, with crsSuffix or ruleSuffix for what the prompt carried.
func (v variant) label(model string) string {
	return model + cond(v.crs, crsSuffix, "") + cond(v.rule, ruleSuffix, "")
}

// labelModel is the model of a run label.
func labelModel(label string) string {
	return strings.TrimSuffix(strings.TrimSuffix(label, ruleSuffix), crsSuffix)
}

// label is the run the record belongs to.
func (rec record) label() string { return variant{crs: rec.CRS, rule: rec.Rule}.label(rec.Model) }

// latest returns the last record per bill for a run label.
func (r *results) latest(label string) map[string]record {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make(map[string]record)
	for _, rec := range r.records {
		if rec.label() == label {
			out[rec.BillID] = rec
		}
	}
	return out
}

// errStopModel ends a model's run early: the proxy doesn't serve it (404), or the daily budget
// is spent (429 after the summarizer's retries). Rerunning resumes where it stopped.
var errStopModel = errors.New("stop model")

// runner summarizes the bills with one model, giving it what variant says: the bills' CRS
// summaries or their disapproved rules.
type runner struct {
	bills   []evalBill
	texts   map[string]string
	crs     map[string]*ai.CRSContext
	variant variant
	results *results
	check   checker
	workers int
	logger  *slog.Logger
	now     func() time.Time
}

// run summarizes every bill that has no record yet for the summarizer's model, or whose last
// record is an error. It returns errStopModel (wrapped) if the model can't go on.
func (r *runner) run(ctx context.Context, s billSummarizer) error {
	label := r.variant.label(s.Config().Model)
	todo := r.todo(label)
	r.logger.InfoContext(ctx, "summarize", "run", label, "bills", len(todo), "already_done", len(r.bills)-len(todo))

	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	jobs := make(chan evalBill)
	var wg sync.WaitGroup
	for range max(r.workers, 1) {
		wg.Go(func() {
			for b := range jobs {
				if ctx.Err() != nil {
					continue // stopped: drain the queue without calling the model
				}
				if err := r.one(ctx, s, b); err != nil {
					cancel(err)
				}
			}
		})
	}
	send(ctx, jobs, todo)
	wg.Wait()
	if err := context.Cause(ctx); err != nil && !errors.Is(err, context.Canceled) {
		return err
	}
	return nil
}

// todo lists the bills a run still needs: no record yet, or an error last time.
func (r *runner) todo(label string) []evalBill {
	done := r.results.latest(label)
	var todo []evalBill
	for _, b := range r.bills {
		if rec, ok := done[b.BillID]; !ok || rec.Outcome == string(ai.OutcomeError) {
			todo = append(todo, b)
		}
	}
	return todo
}

// send queues bills until ctx ends, then closes jobs.
func send(ctx context.Context, jobs chan<- evalBill, bills []evalBill) {
	defer close(jobs)
	for _, b := range bills {
		select {
		case <-ctx.Done():
			return
		case jobs <- b:
		}
	}
}

// one summarizes b and records the attempt. A stop condition isn't recorded, so a rerun tries the
// bill again.
func (r *runner) one(ctx context.Context, s billSummarizer, b evalBill) error {
	text := r.texts[b.BillID]
	bc := b.billContext(text)
	if r.variant.crs {
		bc.CRSSummary = r.crs[b.BillID]
	}
	if r.variant.rule {
		bc.Rule = b.Rule.context()
	}
	out, err := s.SummarizeBill(ctx, bc)
	if out == nil {
		return fmt.Errorf("%s: %w", b.BillID, err)
	}
	if ctx.Err() != nil {
		return nil //nolint:nilerr // another worker stopped the run; this attempt was cut short
	}
	if out.Outcome == ai.OutcomeError {
		if reason := stopReason(out.Reason); reason != "" {
			return fmt.Errorf("%w %s: %s (%s)", errStopModel, s.Config().Model, reason, out.Reason)
		}
	}
	rec := newRecord(b, s.Config(), out, r.now())
	rec.CRS, rec.Rule = r.variant.crs, r.variant.rule
	summary := strings.Join([]string{out.ShortSummary, out.LongSummary, out.WhoItAffects}, "\n")
	// The checks read what the model was given: the rule too, in a +rule run.
	source := b.Title + "\n" + text
	if r.variant.rule {
		source += "\n" + b.Rule.sourceText()
	}
	rec.LoadedTerms, rec.PartyNames = r.check.check(summary, source)
	if cs := r.crs[b.BillID]; cs != nil && out.Outcome == ai.OutcomeOK {
		share := copiedShare(summary, cs.Text, copyGram)
		rec.CRSCopied = &share
	}
	if b.Rule != nil && out.Outcome == ai.OutcomeOK {
		rec.RuleSignals = checkRule(summary, b.Rule, b.Title, text)
	}
	r.logger.InfoContext(ctx, "summary_result", "bill_id", b.BillID, "run", rec.label(),
		"outcome", rec.Outcome, "reason", rec.Reason, "input_tokens", rec.InputTokens,
		"output_tokens", rec.OutputTokens, "thinking_tokens", rec.ThinkingTokens, "latency_ms", rec.LatencyMS)
	return r.results.append(rec)
}

// stopReason says why an error reason should stop the model's run, or "" if it shouldn't.
func stopReason(reason string) string {
	switch {
	case strings.HasPrefix(reason, "http 404"):
		return "the endpoint doesn't serve this model"
	case strings.HasPrefix(reason, "http 429"):
		return "rate limited or out of daily budget after retries"
	default:
		return ""
	}
}

func newRecord(b evalBill, cfg ai.Config, out *ai.BillSummary, now time.Time) record {
	return record{
		BillID:         b.BillID,
		Category:       b.Category,
		Model:          cfg.Model,
		ModelVersion:   out.ModelVersion,
		PromptVersion:  out.PromptVersion,
		ThinkingLevel:  cfg.ThinkingLevel,
		Outcome:        string(out.Outcome),
		Reason:         out.Reason,
		ShortSummary:   out.ShortSummary,
		LongSummary:    out.LongSummary,
		WhoItAffects:   out.WhoItAffects,
		InputTokens:    out.Usage.InputTokens,
		OutputTokens:   out.Usage.OutputTokens,
		ThinkingTokens: out.Usage.ThinkingTokens,
		LatencyMS:      out.Latency.Milliseconds(),
		InputTruncated: out.InputTruncated,
		RanAt:          now.UTC(),
	}
}
