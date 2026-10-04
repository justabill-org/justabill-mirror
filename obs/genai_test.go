package obs_test

import (
	"testing"

	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/trace"

	"github.com/justabill-org/justabill/obs"
	"github.com/justabill-org/justabill/obs/obstest"
	names "github.com/justabill-org/justabill/obs/semconv"
)

const testModel = "gemini-3.8-flash"

// summaryResults returns justabill.summary.results by outcome.
func summaryResults(t *testing.T, tel *obstest.Telemetry) map[string]int64 {
	t.Helper()

	return results(t, tel, names.SummaryResultsName)
}

// results returns the results counter name by justabill.summary.outcome.
func results(t *testing.T, tel *obstest.Telemetry, name string) map[string]int64 {
	t.Helper()

	m, ok := tel.Metric(t, name)
	if !ok {
		t.Fatalf("no %s metric", name)
	}

	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("%s = %#v, want a counter", name, m.Data)
	}

	got := map[string]int64{}

	for _, p := range sum.DataPoints {
		outcome, _ := p.Attributes.Value(names.SummaryOutcomeKey)
		if model, _ := p.Attributes.Value("gen_ai.request.model"); model.AsString() != testModel {
			t.Errorf("%s model = %q, want %q", name, model.AsString(), testModel)
		}

		got[outcome.AsString()] = p.Value
	}

	return got
}

func TestGenAIOK(t *testing.T) {
	tel := obstest.New(t)

	ctx, call := obs.GenAI(t.Context(), testModel)
	if !trace.SpanFromContext(ctx).SpanContext().IsValid() {
		t.Fatal("GenAI's context carries no span")
	}

	call.End(obs.GenAIResult{
		ResponseModel: testModel + "-001", InputTokens: 1200, OutputTokens: 300, ReasoningTokens: 450,
		SummaryOutcome: names.SummaryOutcomeOK,
	})

	span := onlySpan(t, tel)
	if span.Name() != "generate_content "+testModel || span.SpanKind() != trace.SpanKindClient {
		t.Errorf("span = %q kind %v, want generate_content %s CLIENT", span.Name(), span.SpanKind(), testModel)
	}

	if span.Status().Code != codes.Unset {
		t.Errorf("span status = %v, want unset", span.Status())
	}

	wantAttrs(t, "span", span.Attributes(), map[string]string{
		"gen_ai.operation.name":                "generate_content",
		"gen_ai.provider.name":                 "gcp.vertex_ai",
		"gen_ai.request.model":                 testModel,
		"gen_ai.response.model":                testModel + "-001",
		"gen_ai.usage.input_tokens":            "1200",
		"gen_ai.usage.output_tokens":           "750",
		"gen_ai.usage.reasoning.output_tokens": "450",
		"justabill.summary.outcome":            "ok",
	})

	p := onlyPoint(t, tel, "gen_ai.client.operation.duration")
	wantAttrs(t, "operation duration", p.Attributes.ToSlice(), map[string]string{
		"gen_ai.operation.name": "generate_content", "gen_ai.provider.name": "gcp.vertex_ai",
		"gen_ai.request.model": testModel, "gen_ai.response.model": testModel + "-001",
	})

	checkTokenUsage(t, tel, map[string]int64{"input": 1200, "output": 750})

	if got := summaryResults(t, tel); len(got) != 1 || got["ok"] != 1 {
		t.Errorf("summary results = %v, want ok 1", got)
	}
}

// checkTokenUsage checks the sums of gen_ai.client.token.usage by token type.
func checkTokenUsage(t *testing.T, tel *obstest.Telemetry, want map[string]int64) {
	t.Helper()

	m, ok := tel.Metric(t, "gen_ai.client.token.usage")
	if !ok {
		t.Fatal("no gen_ai.client.token.usage metric")
	}

	h, ok := m.Data.(metricdata.Histogram[int64])
	if !ok {
		t.Fatalf("token usage = %#v, want an int64 histogram", m.Data)
	}

	got := map[string]int64{}

	for _, p := range h.DataPoints {
		typ, _ := p.Attributes.Value("gen_ai.token.type")
		got[typ.AsString()] = p.Sum
	}

	if len(got) != len(want) || got["input"] != want["input"] || got["output"] != want["output"] {
		t.Errorf("token usage = %v, want %v", got, want)
	}
}

func TestGenAISafetyBlocked(t *testing.T) {
	tel := obstest.New(t)

	_, call := obs.GenAI(t.Context(), testModel)
	call.End(obs.GenAIResult{
		InputTokens: 900, SummaryOutcome: names.SummaryOutcomeSafetyBlocked, Reason: "prompt SAFETY",
	})

	span := onlySpan(t, tel)
	if span.Status().Code != codes.Error || span.Status().Description != "prompt SAFETY" {
		t.Errorf("span status = %v, want error prompt SAFETY", span.Status())
	}

	attrs := attrMap(span.Attributes())
	if got := attrs["error.type"].AsString(); got != "safety_blocked" {
		t.Errorf("error.type = %q, want safety_blocked", got)
	}

	if got := attrs["gen_ai.request.model"].AsString(); got != testModel {
		t.Errorf("gen_ai.request.model = %q, want %q", got, testModel)
	}

	if _, ok := attrs["gen_ai.response.model"]; ok {
		t.Error("gen_ai.response.model set without a response model")
	}

	if got := summaryResults(t, tel); got["safety_blocked"] != 1 {
		t.Errorf("summary results = %v, want safety_blocked 1", got)
	}
}

func TestGenAIErrorRecordsNoTokens(t *testing.T) {
	tel := obstest.New(t)

	_, call := obs.GenAI(t.Context(), testModel)
	call.End(obs.GenAIResult{SummaryOutcome: names.SummaryOutcomeError, Reason: "http 503 UNAVAILABLE"})

	if _, ok := tel.Metric(t, "gen_ai.client.token.usage"); ok {
		t.Error("token usage recorded for a call with no answer")
	}

	p := onlyPoint(t, tel, "gen_ai.client.operation.duration")
	if v, _ := p.Attributes.Value("error.type"); v.AsString() != "error" {
		t.Errorf("operation duration error.type = %q, want error", v.AsString())
	}

	if got := summaryResults(t, tel); got["error"] != 1 {
		t.Errorf("summary results = %v, want error 1", got)
	}
}

// TestGenAILawChangeResults covers #480: a law-change call counts in justabill.law_change.results,
// not justabill.summary.results, and still records its duration and token usage.
func TestGenAILawChangeResults(t *testing.T) {
	tel := obstest.New(t)

	_, call := obs.GenAI(t.Context(), testModel)
	call.End(obs.GenAIResult{
		InputTokens: 5000, OutputTokens: 800, ReasoningTokens: 200, SummaryOutcome: names.SummaryOutcomeOK,
		ResultMetric: obs.GenAILawChangeResults,
	})

	if got := results(t, tel, names.LawChangeResultsName); len(got) != 1 || got["ok"] != 1 {
		t.Errorf("law change results = %v, want ok 1", got)
	}

	if _, ok := tel.Metric(t, names.SummaryResultsName); ok {
		t.Errorf("a law-change call counted in %s", names.SummaryResultsName)
	}

	onlyPoint(t, tel, "gen_ai.client.operation.duration")
	checkTokenUsage(t, tel, map[string]int64{"input": 5000, "output": 1000})
}

func TestGenAILawChangeError(t *testing.T) {
	tel := obstest.New(t)

	_, call := obs.GenAI(t.Context(), testModel)
	call.End(obs.GenAIResult{
		SummaryOutcome: names.SummaryOutcomeError, Reason: "http 503", ResultMetric: obs.GenAILawChangeResults,
	})

	if got := results(t, tel, names.LawChangeResultsName); got["error"] != 1 {
		t.Errorf("law change results = %v, want error 1", got)
	}

	if span := onlySpan(t, tel); span.Status().Code != codes.Error {
		t.Errorf("span status = %v, want error", span.Status())
	}
}
