// Package ai generates plain-language bill summaries with Gemini on Vertex AI: one
// schema-constrained call per bill, with every outcome classified (docs/design/68-ai-summaries-gemini-3.md).
package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"google.golang.org/genai"

	"github.com/justabill-org/justabill/obs"
	"github.com/justabill-org/justabill/obs/semconv"
)

// Outcome classifies one summarization attempt.
type Outcome string

// Outcomes, in the order they're checked.
const (
	// OutcomeOK is a complete, valid summary.
	OutcomeOK Outcome = "ok"
	// OutcomeBlocked means a filter stopped the prompt or the answer. Retrying the same text won't help.
	OutcomeBlocked Outcome = "blocked"
	// OutcomeTruncatedOutput means the answer hit MaxOutputTokens.
	OutcomeTruncatedOutput Outcome = "truncated_output"
	// OutcomeInvalid means the answer wasn't valid JSON, or a field was empty or far too long.
	OutcomeInvalid Outcome = "invalid"
	// OutcomeError is anything else: an API or network error, or an unexpected finish reason.
	OutcomeError Outcome = "error"
)

// maxRetries is how many times a 429 or 5xx is retried within one call.
const maxRetries = 3

// jitterDivisor makes jitter add up to half the backoff.
const jitterDivisor = 2

// maxReasonLen caps error text kept in Result.Reason.
const maxReasonLen = 300

// Usage is the token accounting Vertex AI reports for a call.
type Usage struct {
	InputTokens    int64
	OutputTokens   int64
	ThinkingTokens int64
}

// Result is the outcome of one summarization call, with its provenance. It's returned for every
// outcome; the summary fields are set only when Outcome is OutcomeOK.
type Result struct {
	Outcome Outcome
	// Reason is the block reason, finish reason or error class when Outcome isn't ok.
	Reason         string
	Model          string
	ModelVersion   string
	PromptVersion  string
	RequestType    string
	InputTruncated bool
	Usage          Usage
	Latency        time.Duration
}

// BillSummary is a bill summary and its provenance.
type BillSummary struct {
	Result

	ShortSummary string
	LongSummary  string
	WhoItAffects string
}

// DiffSummary is a summary of the changes between two text versions, and its provenance.
type DiffSummary struct {
	Result

	Summary string
}

// AttemptError is the error returned with a Result whose Outcome isn't ok.
type AttemptError struct {
	Outcome Outcome
	Reason  string
}

// Error returns "summary <outcome>: <reason>".
func (e *AttemptError) Error() string {
	return fmt.Sprintf("summary %s: %s", e.Outcome, e.Reason)
}

// Summarizer generates summaries with one GenerateContent call per bill or diff.
type Summarizer struct {
	models *genai.Models
	cfg    Config
	// backoff is the wait before each retry; jitter adds up to half of it again.
	backoff []time.Duration
	sleep   func(context.Context, time.Duration) error
}

// NewSummarizer creates a Summarizer on Vertex AI with Application Default Credentials.
func NewSummarizer(ctx context.Context, cfg Config) (*Summarizer, error) {
	cfg = cfg.withDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	if cfg.Project == "" {
		return nil, errors.New("vertex ai: GCP project is required")
	}
	client, err := genai.NewClient(ctx, &genai.ClientConfig{
		Project:  cfg.Project,
		Location: cfg.Location,
		Backend:  genai.BackendVertexAI,
	})
	if err != nil {
		return nil, fmt.Errorf("create vertex ai client: %w", err)
	}
	return NewSummarizerWithClient(client, cfg)
}

// NewSummarizerWithClient creates a Summarizer on a client the caller built: tests, or the eval
// through a Gemini-API-compatible proxy. Project and Location in cfg aren't used to connect.
func NewSummarizerWithClient(client *genai.Client, cfg Config) (*Summarizer, error) {
	cfg = cfg.withDefaults()
	if err := cfg.validate(); err != nil {
		return nil, err
	}
	const (
		firstRetry  = 2 * time.Second
		secondRetry = 8 * time.Second
		thirdRetry  = 30 * time.Second
	)
	return &Summarizer{
		models:  client.Models,
		cfg:     cfg,
		backoff: []time.Duration{firstRetry, secondRetry, thirdRetry},
		sleep:   sleepContext,
	}, nil
}

// Config returns the effective settings, with defaults applied.
func (s *Summarizer) Config() Config { return s.cfg }

// SummarizeBill summarizes one bill with prompt bill-v4. The result is set for every outcome; the
// error is an *AttemptError when the outcome isn't ok.
func (s *Summarizer) SummarizeBill(ctx context.Context, bc BillContext) (*BillSummary, error) {
	req, err := s.cfg.BillRequest(bc)
	if err != nil {
		return nil, err
	}

	out := &BillSummary{Result: req.provenance()}
	ctx, call := obs.GenAI(ctx, s.cfg.Model)
	defer func() { call.End(genAIResult(&out.Result)) }()
	resp, err := s.call(ctx, &out.Result, req)
	if err != nil {
		return out, err
	}
	return out, readBill(out, resp)
}

// SummarizeDiff summarizes a change between versions with prompt diff-v2, on the same contract as
// SummarizeBill.
func (s *Summarizer) SummarizeDiff(ctx context.Context, dc DiffContext) (*DiffSummary, error) {
	req, err := s.cfg.diffRequest(dc)
	if err != nil {
		return nil, err
	}

	out := &DiffSummary{Result: req.provenance()}
	ctx, call := obs.GenAI(ctx, s.cfg.Model)
	defer func() { call.End(genAIResult(&out.Result)) }()
	resp, err := s.call(ctx, &out.Result, req)
	if err != nil {
		return out, err
	}
	return out, readDiff(out, resp)
}

// ReadBillResponse reads the answer to a BillRequest, with the outcome SummarizeBill gives the same
// response: a blocked prompt or answer, MAX_TOKENS or another finish reason, then the JSON and field
// checks. prov is the request's provenance (Model, PromptVersion, RequestType, InputTruncated); the
// result is set for every outcome, and the error is an *AttemptError when the outcome isn't ok. The
// Vertex AI batch import classifies its output lines with it.
func ReadBillResponse(prov Result, resp *genai.GenerateContentResponse) (*BillSummary, error) {
	out := &BillSummary{Result: prov}
	return out, readBill(out, resp)
}

// readBill fills out from a bill response.
func readBill(out *BillSummary, resp *genai.GenerateContentResponse) error {
	text, err := out.readResponse(resp)
	if err != nil {
		return err
	}
	var answer struct {
		ShortSummary string `json:"short_summary"`
		LongSummary  string `json:"long_summary"`
		WhoItAffects string `json:"who_it_affects"`
	}
	if err = json.Unmarshal([]byte(text), &answer); err != nil {
		return out.fail(OutcomeInvalid, "parse answer: "+err.Error())
	}
	out.ShortSummary, out.LongSummary, out.WhoItAffects = answer.ShortSummary, answer.LongSummary, answer.WhoItAffects
	if err = checkFields(
		field{fieldShortSummary, out.ShortSummary, shortSummaryHint},
		field{fieldLongSummary, out.LongSummary, longSummaryHint},
		field{fieldWhoItAffects, out.WhoItAffects, whoItAffectsHint},
	); err != nil {
		return out.fail(OutcomeInvalid, err.Error())
	}
	return nil
}

// readDiff fills out from a diff-v2 response.
func readDiff(out *DiffSummary, resp *genai.GenerateContentResponse) error {
	text, err := out.readResponse(resp)
	if err != nil {
		return err
	}
	var answer struct {
		Summary string `json:"summary"`
	}
	if err = json.Unmarshal([]byte(text), &answer); err != nil {
		return out.fail(OutcomeInvalid, "parse answer: "+err.Error())
	}
	out.Summary = answer.Summary
	if err = checkFields(field{fieldSummary, out.Summary, diffSummaryHint}); err != nil {
		return out.fail(OutcomeInvalid, err.Error())
	}
	return nil
}

// genAIResult is r as the generate_content span and metrics record it.
func genAIResult(r *Result) obs.GenAIResult {
	return obs.GenAIResult{
		ResponseModel:   r.ModelVersion,
		InputTokens:     r.Usage.InputTokens,
		OutputTokens:    r.Usage.OutputTokens,
		ReasoningTokens: r.Usage.ThinkingTokens,
		SummaryOutcome:  summaryOutcome(r.Outcome),
		Reason:          r.Reason,
	}
}

// summaryOutcome maps an Outcome to justabill.summary.outcome: a blocked prompt or answer is
// safety_blocked, an answer cut off or unusable is empty, and a failed call is error.
func summaryOutcome(o Outcome) string {
	switch o {
	case OutcomeOK:
		return semconv.SummaryOutcomeOK
	case OutcomeBlocked:
		return semconv.SummaryOutcomeSafetyBlocked
	case OutcomeTruncatedOutput, OutcomeInvalid:
		return semconv.SummaryOutcomeEmpty
	case OutcomeError:
		return semconv.SummaryOutcomeError
	}
	return semconv.SummaryOutcomeError
}

// fail sets a non-ok outcome and returns the matching error.
func (r *Result) fail(outcome Outcome, reason string) error {
	r.Outcome, r.Reason = outcome, reason
	return &AttemptError{Outcome: outcome, Reason: reason}
}

// call makes the request (with retries) and records its latency in r. A failed call sets r's
// outcome to error.
func (s *Summarizer) call(ctx context.Context, r *Result, req *Request) (*genai.GenerateContentResponse, error) {
	start := time.Now()
	resp, err := s.callWithRetry(ctx, req)
	r.Latency = time.Since(start)
	if err != nil {
		return nil, r.fail(OutcomeError, errorReason(err))
	}
	return resp, nil
}

// readResponse fills r's model version and usage from resp, classifies it, and returns the
// answer's text when the model finished normally.
func (r *Result) readResponse(resp *genai.GenerateContentResponse) (string, error) {
	if resp == nil {
		return "", r.fail(OutcomeError, "no response")
	}
	r.ModelVersion = resp.ModelVersion
	if u := resp.UsageMetadata; u != nil {
		r.Usage = Usage{
			InputTokens:    int64(u.PromptTokenCount),
			OutputTokens:   int64(u.CandidatesTokenCount),
			ThinkingTokens: int64(u.ThoughtsTokenCount),
		}
	}

	outcome, reason := classify(resp)
	if outcome != OutcomeOK {
		return "", r.fail(outcome, reason)
	}
	r.Outcome = OutcomeOK
	return resp.Text(), nil
}

// classify maps a response to an outcome, in the design's order: a prompt block, a blocking
// finish reason, MAX_TOKENS, then anything but STOP. JSON checks come after, in the caller.
func classify(resp *genai.GenerateContentResponse) (Outcome, string) {
	if pf := resp.PromptFeedback; pf != nil && pf.BlockReason != "" {
		return OutcomeBlocked, "prompt " + string(pf.BlockReason)
	}
	if len(resp.Candidates) == 0 || resp.Candidates[0] == nil {
		return OutcomeError, "no candidates"
	}
	reason := resp.Candidates[0].FinishReason
	switch reason { //nolint:exhaustive // every other reason is an error
	case genai.FinishReasonStop:
		return OutcomeOK, ""
	case genai.FinishReasonSafety, genai.FinishReasonRecitation, genai.FinishReasonSPII,
		genai.FinishReasonProhibitedContent, genai.FinishReasonBlocklist:
		return OutcomeBlocked, string(reason)
	case genai.FinishReasonMaxTokens:
		return OutcomeTruncatedOutput, string(reason)
	case "":
		return OutcomeError, "no finish reason"
	default:
		return OutcomeError, string(reason)
	}
}

// field is one answer field and its length hint.
type field struct {
	name  string
	value string
	hint  int
}

// checkFields rejects an empty field, or one over twice its length hint.
func checkFields(fields ...field) error {
	for _, f := range fields {
		if strings.TrimSpace(f.value) == "" {
			return fmt.Errorf("%s is empty", f.name)
		}
		if n := utf8.RuneCountInString(f.value); n > overlongFactor*f.hint {
			return fmt.Errorf("%s is %d characters, over twice its %d-character hint", f.name, n, f.hint)
		}
	}
	return nil
}

// callWithRetry makes the GenerateContent call, retrying 429 and 5xx with backoff and jitter. Each
// attempt has its own timeout.
func (s *Summarizer) callWithRetry(ctx context.Context, req *Request) (*genai.GenerateContentResponse, error) {
	for attempt := 0; ; attempt++ {
		resp, err := s.callOnce(ctx, req)
		if err == nil || attempt >= min(maxRetries, len(s.backoff)) || !retryable(err) {
			return resp, err
		}
		wait := s.backoff[attempt]
		wait += time.Duration(rand.Int64N(int64(wait/jitterDivisor) + 1)) //nolint:gosec // jitter, not security
		if sleepErr := s.sleep(ctx, wait); sleepErr != nil {
			return nil, errors.Join(err, sleepErr)
		}
	}
}

func (s *Summarizer) callOnce(ctx context.Context, req *Request) (*genai.GenerateContentResponse, error) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.timeout())
	defer cancel()
	resp, err := s.models.GenerateContent(ctx, req.Model, req.Contents, req.Config)
	if err != nil {
		return nil, fmt.Errorf("generate content: %w", err)
	}
	return resp, nil
}

// retryable reports whether err is a 429 or 5xx from the API.
func retryable(err error) bool {
	apiErr, ok := errors.AsType[genai.APIError](err)
	if !ok {
		return false
	}
	return apiErr.Code == http.StatusTooManyRequests || apiErr.Code >= http.StatusInternalServerError
}

// errorReason is a short description of a call error for logs and the attempts table.
func errorReason(err error) string {
	if apiErr, ok := errors.AsType[genai.APIError](err); ok {
		return fmt.Sprintf("http %d %s", apiErr.Code, apiErr.Status)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, context.Canceled) {
		return "canceled"
	}
	reason := err.Error()
	if len(reason) > maxReasonLen {
		reason = strings.ToValidUTF8(reason[:maxReasonLen], "")
	}
	return reason
}

func sleepContext(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return fmt.Errorf("wait to retry: %w", ctx.Err())
	case <-t.C:
		return nil
	}
}
