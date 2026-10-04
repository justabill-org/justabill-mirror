package obs

import (
	"context"
	"errors"
	"log/slog"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	names "github.com/justabill-org/justabill/obs/semconv"
)

// jobSpanPrefix starts every job span's name: "pipeline.job sync-bills".
const jobSpanPrefix = "pipeline.job "

// jobDurationBuckets are the histogram boundaries for job runs, in seconds: from a second to the
// three-hour timeout of the longest job.
func jobDurationBuckets() []float64 {
	return []float64{1, 5, 15, 30, 60, 120, 300, 600, 1200, 1800, 3600, 7200, 10800}
}

// ErrUnavailable marks a job's error as an upstream source that is down for now, as for
// maintenance: wrap it (fmt.Errorf("%w: %w", obs.ErrUnavailable, err)) and [Job] reports the run
// as "unavailable" rather than "failed", so the failed-job alert doesn't fire for an outage the
// job will retry. A job that wants an outage to alert after a while stops wrapping it.
var ErrUnavailable = errors.New("upstream source unavailable")

// jobKey is the context key of the running job.
type jobKey struct{}

// jobRun is one run of a job: its name, its items counter and what Items has counted so far.
type jobRun struct {
	name   attribute.KeyValue
	items  metric.Int64Counter
	ok     atomic.Int64
	failed atomic.Int64
}

// Job runs fn as one run of the pipeline job name and reports how it ended: "ok", "failed",
// "timeout", "canceled" or "unavailable" (the justabill.job.outcome values), and fn's error.
//
// The run is an INTERNAL root span "pipeline.job {name}": a job is its own trace, never part of
// whatever ctx carries. fn's upstream, Spanner and Gemini calls become its children. When fn
// returns, Job records a justabill.pipeline.job.duration point with the outcome, sets
// justabill.pipeline.job.last_success to the current Unix time if the run succeeded, and logs one
// justabill.pipeline.job.finished record on log with the outcome, the duration, the items that
// [Items] counted and the error (through [Redact]): at INFO when it succeeded, WARN when it was
// canceled or unavailable and ERROR otherwise.
//
// The outcome comes from ctx once fn has returned, so the caller sets the run's deadline on ctx:
// past its deadline the run timed out, otherwise cancelled means canceled (a shutdown, or a lost
// lease), whatever fn returned. A live ctx and an error mean failed, or unavailable when the
// error wraps [ErrUnavailable].
//
// Call it after obs.Start (or obstest.New): it takes its tracer and meter when it's called.
func Job(ctx context.Context, log *slog.Logger, name string, fn func(context.Context) error) (string, error) {
	meter := otel.Meter(scope)
	run := &jobRun{name: names.JobNameKey.String(name), items: newCounter(meter,
		names.PipelineItemsName, names.PipelineItemsUnit, names.PipelineItemsDescription)}

	runCtx, span := otel.Tracer(scope).Start(context.WithValue(ctx, jobKey{}, run), jobSpanPrefix+name,
		trace.WithNewRoot(), trace.WithSpanKind(trace.SpanKindInternal), trace.WithAttributes(run.name))
	// Deferred so that a panicking job still ends its span.
	defer span.End()

	start := time.Now()

	err := runJob(runCtx, fn)
	elapsed := time.Since(start)
	outcome := jobOutcome(ctx, err)

	endJobSpan(span, outcome, err)
	recordJob(runCtx, meter, run, outcome, elapsed)
	logJobFinished(runCtx, log, run, outcome, elapsed, err)

	return outcome, err
}

// Items counts n items of the running job with outcome (a justabill.item.outcome value: "ok",
// "failed" or "skipped") on justabill.pipeline.items. The job's finished record adds up the ok and
// failed ones. Outside a [Job] it does nothing. It is safe for concurrent use.
func Items(ctx context.Context, outcome string, n int) {
	run, ok := ctx.Value(jobKey{}).(*jobRun)
	if !ok || n <= 0 {
		return
	}

	switch outcome {
	case names.ItemOutcomeOK:
		run.ok.Add(int64(n))
	case names.ItemOutcomeFailed:
		run.failed.Add(int64(n))
	}

	run.items.Add(ctx, int64(n), metric.WithAttributes(run.name, names.ItemOutcomeKey.String(outcome)))
}

// runJob calls fn, marking the job's span as an error if fn panics, before the panic goes on up.
func runJob(ctx context.Context, fn func(context.Context) error) error {
	defer func() {
		if p := recover(); p != nil {
			trace.SpanFromContext(ctx).SetStatus(codes.Error, "panic")
			panic(p)
		}
	}()

	return fn(ctx)
}

// jobOutcome classifies a finished run by its context first: see [Job].
func jobOutcome(ctx context.Context, err error) string {
	switch {
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		return names.JobOutcomeTimeout
	case ctx.Err() != nil:
		return names.JobOutcomeCanceled
	case errors.Is(err, ErrUnavailable):
		return names.JobOutcomeUnavailable
	case err != nil:
		return names.JobOutcomeFailed
	default:
		return names.JobOutcomeOK
	}
}

// endJobSpan sets the span's outcome. A failed or timed-out run is an error; a canceled one isn't,
// since shutdown cancels runs on purpose, and nor is an unavailable one, whose upstream is down.
func endJobSpan(span trace.Span, outcome string, err error) {
	span.SetAttributes(names.JobOutcomeKey.String(outcome))

	switch outcome {
	case names.JobOutcomeFailed:
		span.SetAttributes(semconv.ErrorType(err))
		span.SetStatus(codes.Error, Redact(err.Error()))
	case names.JobOutcomeTimeout:
		span.SetAttributes(semconv.ErrorTypeKey.String(names.JobOutcomeTimeout))
		span.SetStatus(codes.Error, names.JobOutcomeTimeout)
	}
}

// recordJob records the run's duration and, when it succeeded, its last_success time.
func recordJob(ctx context.Context, meter metric.Meter, run *jobRun, outcome string, elapsed time.Duration) {
	duration, err := meter.Float64Histogram(names.PipelineJobDurationName,
		metric.WithUnit(names.PipelineJobDurationUnit), metric.WithDescription(names.PipelineJobDurationDescription),
		metric.WithExplicitBucketBoundaries(jobDurationBuckets()...))
	if err != nil {
		otel.Handle(err)
	}

	duration.Record(ctx, elapsed.Seconds(), metric.WithAttributes(run.name, names.JobOutcomeKey.String(outcome)))

	if outcome != names.JobOutcomeOK {
		return
	}

	lastSuccess, err := meter.Int64Gauge(names.PipelineJobLastSuccessName,
		metric.WithUnit(names.PipelineJobLastSuccessUnit),
		metric.WithDescription(names.PipelineJobLastSuccessDescription))
	if err != nil {
		otel.Handle(err)
	}

	lastSuccess.Record(ctx, time.Now().Unix(), metric.WithAttributes(run.name))
}

// logJobFinished logs the justabill.pipeline.job.finished record. The run's context may be done,
// and the record only needs the trace IDs in it.
func logJobFinished(
	ctx context.Context, log *slog.Logger, run *jobRun, outcome string, elapsed time.Duration, err error,
) {
	attrs := []slog.Attr{
		slog.String(string(run.name.Key), run.name.Value.AsString()),
		slog.String(string(names.JobOutcomeKey), outcome),
		slog.Float64(string(names.JobDurationKey), elapsed.Seconds()),
		slog.Int64(string(names.JobItemsOKKey), run.ok.Load()),
		slog.Int64(string(names.JobItemsFailedKey), run.failed.Load()),
	}
	if err != nil {
		attrs = append(attrs, slog.String("error", Redact(err.Error())))
	}

	level := slog.LevelError

	switch outcome {
	case names.JobOutcomeOK:
		level = slog.LevelInfo
	case names.JobOutcomeCanceled, names.JobOutcomeUnavailable:
		level = slog.LevelWarn
	}

	log.LogAttrs(context.WithoutCancel(ctx), level, names.PipelineJobFinishedEvent, attrs...)
}

// newCounter creates an Int64Counter, handing any error to the OpenTelemetry error handler (the
// counter it returns then is a no-op).
func newCounter(meter metric.Meter, name, unit, description string) metric.Int64Counter {
	c, err := meter.Int64Counter(name, metric.WithUnit(unit), metric.WithDescription(description))
	if err != nil {
		otel.Handle(err)
	}

	return c
}
