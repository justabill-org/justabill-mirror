package ai

import (
	"net/http"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/trace"

	"github.com/justabill-org/justabill/obs/obstest"
	"github.com/justabill-org/justabill/obs/semconv"
)

// TestSummarizeBill_SafetyBlockedTelemetry covers the #53 acceptance criterion: a summary blocked
// by safety filters counts on justabill.summary.results as safety_blocked, and its
// generate_content span carries the GenAI attributes.
func TestSummarizeBill_SafetyBlockedTelemetry(t *testing.T) {
	tel := obstest.New(t)
	fake := &fakeVertex{responses: []cannedResponse{candidate("", "SAFETY")}}
	s := newTestSummarizer(t, fake, Config{})

	if _, err := s.SummarizeBill(t.Context(), testBill(t)); err == nil {
		t.Fatal("SummarizeBill succeeded, want a blocked summary")
	}

	spans := tel.Ended()
	if len(spans) != 1 {
		t.Fatalf("got %d spans, want 1", len(spans))
	}
	span := spans[0]
	if span.Name() != "generate_content "+DefaultModel || span.SpanKind() != trace.SpanKindClient {
		t.Errorf("span = %q kind %v, want generate_content %s CLIENT", span.Name(), span.SpanKind(), DefaultModel)
	}
	if span.Status().Code != codes.Error || span.Status().Description != "SAFETY" {
		t.Errorf("span status = %v, want error SAFETY", span.Status())
	}
	attrs := map[attribute.Key]string{}
	for _, kv := range span.Attributes() {
		attrs[kv.Key] = kv.Value.String()
	}
	for k, want := range map[attribute.Key]string{
		"gen_ai.operation.name":                "generate_content",
		"gen_ai.provider.name":                 "gcp.vertex_ai",
		"gen_ai.request.model":                 DefaultModel,
		"gen_ai.response.model":                "gemini-3.8-flash-001",
		"gen_ai.usage.input_tokens":            "1200",
		"gen_ai.usage.output_tokens":           "750",
		"gen_ai.usage.reasoning.output_tokens": "450",
		"justabill.summary.outcome":            "safety_blocked",
		"error.type":                           "safety_blocked",
	} {
		if attrs[k] != want {
			t.Errorf("span %s = %q, want %q", k, attrs[k], want)
		}
	}

	m, ok := tel.Metric(t, semconv.SummaryResultsName)
	if !ok {
		t.Fatalf("no %s metric", semconv.SummaryResultsName)
	}
	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok || len(sum.DataPoints) != 1 {
		t.Fatalf("%s = %#v, want one point", semconv.SummaryResultsName, m.Data)
	}
	p := sum.DataPoints[0]
	outcome, _ := p.Attributes.Value(semconv.SummaryOutcomeKey)
	model, _ := p.Attributes.Value("gen_ai.request.model")
	if p.Value != 1 || outcome.AsString() != "safety_blocked" || model.AsString() != DefaultModel {
		t.Errorf("summary results = %d %v, want 1 safety_blocked for %s", p.Value, p.Attributes.ToSlice(), DefaultModel)
	}
}

// TestSummarizeDiff_Telemetry checks a diff summary is a generate_content span too, and that a
// retried call is one span with one result.
func TestSummarizeDiff_Telemetry(t *testing.T) {
	tel := obstest.New(t)
	fake := &fakeVertex{responses: []cannedResponse{
		apiError(http.StatusServiceUnavailable), candidate(`{"summary":"Section 2 now covers retirees."}`, "STOP"),
	}}
	s := newTestSummarizer(t, fake, Config{})

	if _, err := s.SummarizeDiff(t.Context(), DiffContext{DiffID: "d1", BillID: "hr-119-144", Diff: "x"}); err != nil {
		t.Fatalf("SummarizeDiff: %v", err)
	}
	if spans := tel.Ended(); len(spans) != 1 || spans[0].Status().Code != codes.Unset {
		t.Fatalf("spans = %v, want one ok generate_content span", spans)
	}
	m, ok := tel.Metric(t, semconv.SummaryResultsName)
	if !ok {
		t.Fatalf("no %s metric", semconv.SummaryResultsName)
	}
	sum, _ := m.Data.(metricdata.Sum[int64])
	if len(sum.DataPoints) != 1 || sum.DataPoints[0].Value != 1 {
		t.Errorf("summary results = %#v, want one ok", sum.DataPoints)
	}
}

// TestExplainLawChanges_Telemetry covers #480: a law-change call counts in
// justabill.law_change.results, not justabill.summary.results, and its tokens and duration still
// count in the gen_ai metrics.
func TestExplainLawChanges_Telemetry(t *testing.T) {
	tel := obstest.New(t)
	answer := lawAnswer([2]string{secPhysicians, "The bill would change a year."})
	fake := &fakeVertex{responses: []cannedResponse{candidate(answer, "STOP")}}
	s := newTestSummarizer(t, fake, Config{})

	if _, err := s.ExplainLawChanges(t.Context(), testLawBill()); err != nil {
		t.Fatalf("ExplainLawChanges: %v", err)
	}
	if spans := tel.Ended(); len(spans) != 1 || spans[0].Name() != "generate_content "+DefaultModel {
		t.Fatalf("spans = %v, want one generate_content span", spans)
	}
	if _, ok := tel.Metric(t, semconv.SummaryResultsName); ok {
		t.Errorf("a law-change call counted in %s", semconv.SummaryResultsName)
	}
	m, ok := tel.Metric(t, semconv.LawChangeResultsName)
	if !ok {
		t.Fatalf("no %s metric", semconv.LawChangeResultsName)
	}
	sum, _ := m.Data.(metricdata.Sum[int64])
	if len(sum.DataPoints) != 1 || sum.DataPoints[0].Value != 1 {
		t.Fatalf("law change results = %#v, want one point of 1", sum.DataPoints)
	}
	if outcome, _ := sum.DataPoints[0].Attributes.Value(semconv.SummaryOutcomeKey); outcome.AsString() != "ok" {
		t.Errorf("law change outcome = %q, want ok", outcome.AsString())
	}
	for _, name := range []string{"gen_ai.client.token.usage", "gen_ai.client.operation.duration"} {
		if _, found := tel.Metric(t, name); !found {
			t.Errorf("no %s for a law-change call", name)
		}
	}
}

func TestSummaryOutcome(t *testing.T) {
	for o, want := range map[Outcome]string{
		OutcomeOK:              semconv.SummaryOutcomeOK,
		OutcomeBlocked:         semconv.SummaryOutcomeSafetyBlocked,
		OutcomeTruncatedOutput: semconv.SummaryOutcomeEmpty,
		OutcomeInvalid:         semconv.SummaryOutcomeEmpty,
		OutcomeError:           semconv.SummaryOutcomeError,
		Outcome("new"):         semconv.SummaryOutcomeError,
	} {
		if got := summaryOutcome(o); got != want {
			t.Errorf("summaryOutcome(%q) = %q, want %q", o, got, want)
		}
	}
}
