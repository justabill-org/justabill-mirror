package obs_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"go.opentelemetry.io/otel/codes"
	otellog "go.opentelemetry.io/otel/log"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
	"go.opentelemetry.io/otel/trace"

	"github.com/justabill-org/justabill/obs"
	"github.com/justabill-org/justabill/obs/obstest"
	names "github.com/justabill-org/justabill/obs/semconv"
)

// finishedRecord returns the one justabill.pipeline.job.finished record.
func finishedRecord(t *testing.T, tel *obstest.Telemetry) (otellog.Severity, map[string]string) {
	t.Helper()

	var found []map[string]string

	var severity otellog.Severity

	for _, r := range tel.Logs() {
		if r.Body().AsString() == names.PipelineJobFinishedEvent {
			found = append(found, logAttrs(&r))
			severity = r.Severity()
		}
	}

	if len(found) != 1 {
		t.Fatalf("got %d %s records, want 1", len(found), names.PipelineJobFinishedEvent)
	}

	return severity, found[0]
}

// lastSuccess returns the job's last_success gauge value, or false when it has none.
func lastSuccess(t *testing.T, tel *obstest.Telemetry, job string) (int64, bool) {
	t.Helper()

	m, ok := tel.Metric(t, names.PipelineJobLastSuccessName)
	if !ok {
		return 0, false
	}

	g, ok := m.Data.(metricdata.Gauge[int64])
	if !ok {
		t.Fatalf("%s = %#v, want an int64 gauge", names.PipelineJobLastSuccessName, m.Data)
	}

	for _, p := range g.DataPoints {
		if v, _ := p.Attributes.Value(names.JobNameKey); v.AsString() == job {
			return p.Value, true
		}
	}

	return 0, false
}

func TestJobSucceeds(t *testing.T) {
	tel := obstest.New(t)

	// The job is a root span even inside another trace.
	parentCtx := trace.ContextWithSpanContext(t.Context(), trace.NewSpanContext(trace.SpanContextConfig{
		TraceID: trace.TraceID{1}, SpanID: trace.SpanID{1}, TraceFlags: trace.FlagsSampled,
	}))

	before := time.Now().Unix()

	outcome, err := obs.Job(parentCtx, tel.Logger, "sync-bills", func(ctx context.Context) error {
		obs.Items(ctx, names.ItemOutcomeOK, 3)
		obs.Items(ctx, names.ItemOutcomeFailed, 1)
		obs.Items(ctx, names.ItemOutcomeSkipped, 2)
		obs.Items(ctx, names.ItemOutcomeOK, 0)

		return nil
	})
	if err != nil || outcome != names.JobOutcomeOK {
		t.Fatalf("Job = %q, %v; want ok, nil", outcome, err)
	}

	span := onlySpan(t, tel)
	if span.Name() != "pipeline.job sync-bills" || span.SpanKind() != trace.SpanKindInternal {
		t.Errorf("span = %q kind %v, want pipeline.job sync-bills INTERNAL", span.Name(), span.SpanKind())
	}

	if span.Parent().IsValid() || span.SpanContext().TraceID() == (trace.TraceID{1}) {
		t.Errorf("span parent = %v, want a root span", span.Parent())
	}

	if span.Status().Code != codes.Unset {
		t.Errorf("span status = %v, want unset", span.Status())
	}

	wantAttrs(t, "span", span.Attributes(), map[string]string{
		"justabill.job.name": "sync-bills", "justabill.job.outcome": "ok",
	})

	p := onlyPoint(t, tel, names.PipelineJobDurationName)
	wantAttrs(t, "duration", p.Attributes.ToSlice(), map[string]string{
		"justabill.job.name": "sync-bills", "justabill.job.outcome": "ok",
	})

	if got, ok := lastSuccess(t, tel, "sync-bills"); !ok || got < before {
		t.Errorf("last_success = %d (%v), want at least %d", got, ok, before)
	}

	checkItems(t, tel, map[string]int64{"ok": 3, "failed": 1, "skipped": 2})

	severity, attrs := finishedRecord(t, tel)
	if severity != otellog.SeverityInfo {
		t.Errorf("finished severity = %v, want INFO", severity)
	}

	for k, want := range map[string]string{
		"justabill.job.name": "sync-bills", "justabill.job.outcome": "ok",
		"justabill.job.items.ok": "3", "justabill.job.items.failed": "1",
	} {
		if attrs[k] != want {
			t.Errorf("finished %s = %q, want %q (%v)", k, attrs[k], want, attrs)
		}
	}

	if _, ok := attrs["justabill.job.duration"]; !ok {
		t.Errorf("finished has no justabill.job.duration: %v", attrs)
	}
}

// checkItems checks justabill.pipeline.items by outcome for job sync-bills.
func checkItems(t *testing.T, tel *obstest.Telemetry, want map[string]int64) {
	t.Helper()

	m, ok := tel.Metric(t, names.PipelineItemsName)
	if !ok {
		t.Fatalf("no %s metric", names.PipelineItemsName)
	}

	sum, ok := m.Data.(metricdata.Sum[int64])
	if !ok || !sum.IsMonotonic {
		t.Fatalf("%s = %#v, want a counter", names.PipelineItemsName, m.Data)
	}

	got := map[string]int64{}

	for _, p := range sum.DataPoints {
		job, _ := p.Attributes.Value(names.JobNameKey)
		outcome, _ := p.Attributes.Value(names.ItemOutcomeKey)

		if job.AsString() != "sync-bills" || p.Attributes.Len() != 2 {
			t.Errorf("items point attributes = %v", p.Attributes.ToSlice())
		}

		got[outcome.AsString()] = p.Value
	}

	if len(got) != len(want) {
		t.Errorf("items = %v, want %v", got, want)
	}

	for k, v := range want {
		if got[k] != v {
			t.Errorf("items[%s] = %d, want %d", k, got[k], v)
		}
	}
}

func TestJobFails(t *testing.T) {
	tel := obstest.New(t)

	boom := errors.New("list bills: GET https://api.congress.gov/v3/bill?api_key=SECRET&format=json: 500")

	outcome, err := obs.Job(t.Context(), tel.Logger, "sync-bills", func(context.Context) error { return boom })
	if !errors.Is(err, boom) || outcome != names.JobOutcomeFailed {
		t.Fatalf("Job = %q, %v; want failed, boom", outcome, err)
	}

	span := onlySpan(t, tel)
	if span.Status().Code != codes.Error || span.Status().Description == boom.Error() {
		t.Errorf("span status = %v, want a redacted error", span.Status())
	}

	wantAttrs(t, "span", span.Attributes(), map[string]string{
		"justabill.job.name": "sync-bills", "justabill.job.outcome": "failed", "error.type": "*errors.errorString",
	})

	p := onlyPoint(t, tel, names.PipelineJobDurationName)
	if v, _ := p.Attributes.Value(names.JobOutcomeKey); v.AsString() != "failed" {
		t.Errorf("duration outcome = %q, want failed", v.AsString())
	}

	if v, ok := lastSuccess(t, tel, "sync-bills"); ok {
		t.Errorf("last_success = %d after a failed run, want none", v)
	}

	severity, attrs := finishedRecord(t, tel)
	if severity != otellog.SeverityError {
		t.Errorf("finished severity = %v, want ERROR", severity)
	}

	if attrs["justabill.job.outcome"] != "failed" || attrs["error"] == "" {
		t.Errorf("finished = %v, want outcome failed and the error", attrs)
	}

	if got := attrs["error"]; got != "list bills: GET https://api.congress.gov/v3/bill?api_key=[redacted]&format=json: 500" {
		t.Errorf("finished error = %q, want the key redacted", got)
	}
}

func TestJobKeepsLastSuccessAfterAFailure(t *testing.T) {
	tel := obstest.New(t)

	if _, err := obs.Job(
		t.Context(),
		tel.Logger,
		"sync-votes",
		func(context.Context) error { return nil },
	); err != nil {
		t.Fatal(err)
	}

	first, ok := lastSuccess(t, tel, "sync-votes")
	if !ok {
		t.Fatal("no last_success after a successful run")
	}

	_, _ = obs.Job(t.Context(), tel.Logger, "sync-votes", func(context.Context) error { return errors.New("x") })

	if got, _ := lastSuccess(t, tel, "sync-votes"); got != first {
		t.Errorf("last_success = %d after a failed run, want %d unchanged", got, first)
	}
}

func TestJobUnavailable(t *testing.T) {
	tel := obstest.New(t)

	down := fmt.Errorf("%w: %w", obs.ErrUnavailable, errors.New("uscode: download page is a maintenance page"))

	outcome, err := obs.Job(t.Context(), tel.Logger, "load-uscode", func(context.Context) error { return down })
	if !errors.Is(err, down) || outcome != names.JobOutcomeUnavailable {
		t.Fatalf("Job = %q, %v; want unavailable, the error", outcome, err)
	}

	span := onlySpan(t, tel)
	if span.Status().Code == codes.Error {
		t.Errorf("span status = %v, want no error: the upstream is down, not the job", span.Status())
	}

	wantAttrs(t, "span", span.Attributes(), map[string]string{
		"justabill.job.name": "load-uscode", "justabill.job.outcome": "unavailable",
	})

	p := onlyPoint(t, tel, names.PipelineJobDurationName)
	if v, _ := p.Attributes.Value(names.JobOutcomeKey); v.AsString() != names.JobOutcomeUnavailable {
		t.Errorf("duration outcome = %q, want unavailable", v.AsString())
	}

	if v, ok := lastSuccess(t, tel, "load-uscode"); ok {
		t.Errorf("last_success = %d after an unavailable run, want none", v)
	}

	severity, attrs := finishedRecord(t, tel)
	if severity != otellog.SeverityWarn {
		t.Errorf("finished severity = %v, want WARN", severity)
	}

	if attrs["justabill.job.outcome"] != "unavailable" || attrs["error"] != down.Error() {
		t.Errorf("finished = %v, want outcome unavailable and the error", attrs)
	}
}

func TestJobTimeoutAndCancel(t *testing.T) {
	tests := []struct {
		name         string
		ctx          func(t *testing.T) context.Context
		want         string
		wantStatus   codes.Code
		wantSeverity otellog.Severity
	}{
		{
			name: "past its deadline",
			ctx: func(t *testing.T) context.Context {
				ctx, cancel := context.WithDeadline(t.Context(), time.Now().Add(-time.Second))
				t.Cleanup(cancel)

				return ctx
			},
			want: names.JobOutcomeTimeout, wantStatus: codes.Error, wantSeverity: otellog.SeverityError,
		},
		{
			name: "cancelled",
			ctx: func(t *testing.T) context.Context {
				ctx, cancel := context.WithCancel(t.Context())
				cancel()

				return ctx
			},
			want: names.JobOutcomeCanceled, wantStatus: codes.Unset, wantSeverity: otellog.SeverityWarn,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tel := obstest.New(t)

			// A job that skips its work when cancelled can return nil: it still didn't succeed.
			outcome, _ := obs.Job(tt.ctx(t), tel.Logger, "sync-texts", func(context.Context) error { return nil })
			if outcome != tt.want {
				t.Errorf("outcome = %q, want %q", outcome, tt.want)
			}

			if got := onlySpan(t, tel).Status().Code; got != tt.wantStatus {
				t.Errorf("span status = %v, want %v", got, tt.wantStatus)
			}

			if _, ok := lastSuccess(t, tel, "sync-texts"); ok {
				t.Error("last_success recorded for a run that didn't succeed")
			}

			if severity, _ := finishedRecord(t, tel); severity != tt.wantSeverity {
				t.Errorf("finished severity = %v, want %v", severity, tt.wantSeverity)
			}
		})
	}
}

func TestJobEndsSpanOnPanic(t *testing.T) {
	tel := obstest.New(t)

	func() {
		defer func() {
			if recover() == nil {
				t.Error("the panic didn't go on up")
			}
		}()

		_, _ = obs.Job(t.Context(), tel.Logger, "sync-bills", func(context.Context) error { panic("boom") })
	}()

	if got := onlySpan(t, tel).Status(); got.Code != codes.Error || got.Description != "panic" {
		t.Errorf("span status = %v, want error panic", got)
	}
}

func TestItemsOutsideAJob(t *testing.T) {
	tel := obstest.New(t)

	obs.Items(t.Context(), names.ItemOutcomeOK, 1)

	if m, ok := tel.Metric(t, names.PipelineItemsName); ok {
		t.Errorf("items recorded outside a job: %#v", m)
	}
}
