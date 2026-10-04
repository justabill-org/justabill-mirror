package obs

import (
	"context"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	genai "go.opentelemetry.io/otel/semconv/v1.41.0"
	"go.opentelemetry.io/otel/semconv/v1.41.0/genaiconv"
	"go.opentelemetry.io/otel/trace"

	names "github.com/justabill-org/justabill/obs/semconv"
)

// The GenAI conventions are still in development, and go.opentelemetry.io/otel/semconv dropped them
// after v1.41.0 (they moved to their own repository), so they come from v1.41.0 here, pinned as the
// design asks ("OTel practices we follow", 4).

// GenAICall is one generate_content operation on Gemini in Vertex AI, from [GenAI] to
// [GenAICall.End].
type GenAICall struct {
	ctx   context.Context //nolint:containedctx // End records the metrics in the call's span context.
	span  trace.Span
	start time.Time
	model string
	meter metric.Meter
}

// GenAIResultMetric is the counter a GenAI call's outcome counts in.
type GenAIResultMetric int

const (
	// GenAISummaryResults counts the call in justabill.summary.results: a bill or diff summary. It's
	// the zero value.
	GenAISummaryResults GenAIResultMetric = iota
	// GenAILawChangeResults counts the call in justabill.law_change.results: a bill's law-change
	// explanation (design #149), kept apart so that summary alerts measure summaries only.
	GenAILawChangeResults
)

// GenAIResult is how a GenAI call ended, for [GenAICall.End].
type GenAIResult struct {
	// ResponseModel is the model version that answered (gen_ai.response.model), if known.
	ResponseModel string
	// InputTokens, OutputTokens and ReasoningTokens are the token counts the API reported, with
	// the thinking tokens apart from OutputTokens, as Vertex AI reports them. gen_ai.usage.output_tokens
	// is their sum, since both are billed as output, and gen_ai.usage.reasoning.output_tokens the
	// thinking part.
	InputTokens, OutputTokens, ReasoningTokens int64
	// SummaryOutcome is justabill.summary.outcome: ok, safety_blocked, empty or error.
	SummaryOutcome string
	// Reason says why a call that wasn't ok failed, such as a finish reason or an HTTP status. It
	// becomes the span's status description, through [Redact].
	Reason string
	// ResultMetric is the counter the outcome counts in: justabill.summary.results unless it says
	// otherwise. Token usage and duration count every call alike, since both are spend.
	ResultMetric GenAIResultMetric
}

// GenAI starts a CLIENT span "generate_content {model}" for one summary or law-change request to
// Gemini on Vertex AI, with the GenAI conventions' gen_ai.operation.name, gen_ai.provider.name and
// gen_ai.request.model. Pass the returned context to the call, and end it with [GenAICall.End].
//
// The span covers the whole request, retries and answer checks included, so that its outcome is
// the request's. It never records the prompt or the answer.
//
// Call it after obs.Start (or obstest.New): it takes its tracer and meter when it's called.
func GenAI(ctx context.Context, model string) (context.Context, *GenAICall) {
	name := string(genaiconv.OperationNameGenerateContent) + " " + model
	ctx, span := otel.Tracer(scope).Start(ctx, name, //nolint:spancheck // GenAICall.End ends it
		trace.WithSpanKind(trace.SpanKindClient), trace.WithAttributes(
			genai.GenAIOperationNameGenerateContent, genai.GenAIProviderNameGCPVertexAI,
			genai.GenAIRequestModel(model)))

	call := &GenAICall{ctx: ctx, span: span, start: time.Now(), model: model, meter: otel.Meter(scope)}

	return ctx, call //nolint:spancheck // the caller ends the span with GenAICall.End
}

// End ends the call's span with res and records gen_ai.client.operation.duration,
// gen_ai.client.token.usage (input and output), and the outcome on res.ResultMetric's counter
// (justabill.summary.results or justabill.law_change.results). A result that isn't ok marks the
// span as an error, with error.type set to the summary outcome.
func (c *GenAICall) End(res GenAIResult) {
	defer c.span.End()

	attrs := []attribute.KeyValue{genai.GenAIRequestModel(c.model)}
	if res.ResponseModel != "" {
		attrs = append(attrs, genai.GenAIResponseModel(res.ResponseModel))
	}

	if res.SummaryOutcome != names.SummaryOutcomeOK {
		attrs = append(attrs, genai.ErrorTypeKey.String(res.SummaryOutcome))
		c.span.SetStatus(codes.Error, Redact(res.Reason))
	}

	c.span.SetAttributes(append(attrs,
		genai.GenAIUsageInputTokens(int(res.InputTokens)),
		genai.GenAIUsageOutputTokens(int(res.OutputTokens+res.ReasoningTokens)),
		genai.GenAIUsageReasoningOutputTokens(int(res.ReasoningTokens)),
		names.SummaryOutcomeKey.String(res.SummaryOutcome),
	)...)

	c.record(res, attrs)
}

func (c *GenAICall) record(res GenAIResult, attrs []attribute.KeyValue) {
	op, provider := genaiconv.OperationNameGenerateContent, genaiconv.ProviderNameGCPVertexAI

	duration, err := genaiconv.NewClientOperationDuration(c.meter)
	if err != nil {
		otel.Handle(err)
	}

	duration.Record(c.ctx, time.Since(c.start).Seconds(), op, provider, attrs...)

	tokens, err := genaiconv.NewClientTokenUsage(c.meter)
	if err != nil {
		otel.Handle(err)
	}

	// Token usage is recorded only for a call that got an answer, as the conventions ask.
	if res.InputTokens > 0 || res.OutputTokens > 0 {
		usage := []attribute.KeyValue{genai.GenAIRequestModel(c.model)}
		if res.ResponseModel != "" {
			usage = append(usage, genai.GenAIResponseModel(res.ResponseModel))
		}

		tokens.Record(c.ctx, res.InputTokens, op, provider, genaiconv.TokenTypeInput, usage...)
		tokens.Record(c.ctx, res.OutputTokens+res.ReasoningTokens, op, provider, genaiconv.TokenTypeOutput,
			usage...)
	}

	c.results(res.ResultMetric).Add(c.ctx, 1, metric.WithAttributes(
		names.SummaryOutcomeKey.String(res.SummaryOutcome), genai.GenAIRequestModel(c.model)))
}

// results returns the counter of kind.
func (c *GenAICall) results(kind GenAIResultMetric) metric.Int64Counter {
	if kind == GenAILawChangeResults {
		return newCounter(c.meter, names.LawChangeResultsName, names.LawChangeResultsUnit,
			names.LawChangeResultsDescription)
	}

	return newCounter(c.meter, names.SummaryResultsName, names.SummaryResultsUnit, names.SummaryResultsDescription)
}
