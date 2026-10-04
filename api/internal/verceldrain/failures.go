package verceldrain

import (
	"context"
	"strconv"
	"sync"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"

	jsemconv "github.com/justabill-org/justabill/obs/semconv"
)

// The failure counter's dedup set (docs/design/473-vercel-function-failures.md, "Deduplication").
const (
	// failureTTL is how long a counted request ID suppresses its later failing lines.
	failureTTL = 15 * time.Minute
	// sweepEvery is the least time between two sweeps of expired IDs.
	sweepEvery = time.Minute
	// maxFailures caps the set; when it's full, the oldest IDs are dropped.
	maxFailures = 10_000
)

const (
	// sourceLambda is the drain source of function lines, the only ones counted as failures.
	sourceLambda = "lambda"
	// statusNone is Vercel's -1: no response (statusCode) or a background revalidation
	// (proxy.statusCode).
	statusNone = -1
	// statusTimeout is what Vercel returns when a function times out (FUNCTION_INVOCATION_TIMEOUT).
	statusTimeout = 504
	// minServerError starts the 5xx range.
	minServerError = 500
	// statusClassWidth turns a status into its class digit.
	statusClassWidth = 100
)

// lagBuckets are the justabill.vercel.drain.lag boundaries, in seconds.
func lagBuckets() []float64 {
	return []float64{1, 5, 15, 30, 60, 120, 300, 600, 1800}
}

// knownSource maps a line's source to Vercel's documented sources, else _OTHER (the same value as
// the log type's), so the census's series stay bounded.
func knownSource(source string) string {
	switch source {
	case "build", "edge", sourceLambda, "static", "external", "firewall", "redirect":
		return source
	default:
		return jsemconv.VercelLogTypeOther
	}
}

// logType maps a line's type to Vercel's documented values, else _OTHER.
func logType(t string) string {
	switch t {
	case jsemconv.VercelLogTypeCommand, jsemconv.VercelLogTypeStdout, jsemconv.VercelLogTypeStderr,
		jsemconv.VercelLogTypeExit, jsemconv.VercelLogTypeDeploymentState, jsemconv.VercelLogTypeDelimiter,
		jsemconv.VercelLogTypeMiddleware, jsemconv.VercelLogTypeMiddlewareInvocation,
		jsemconv.VercelLogTypeEdgeFunctionInvocation, jsemconv.VercelLogTypeMetric,
		jsemconv.VercelLogTypeReport, jsemconv.VercelLogTypeFatal:
		return t
	default:
		return jsemconv.VercelLogTypeOther
	}
}

// statusClass is the census class of a line's own statusCode: -1, 1xx…5xx or none.
func statusClass(code int) string {
	switch {
	case code == statusNone:
		return jsemconv.VercelStatusClassNoResponse
	case validStatus(code):
		return strconv.Itoa(code/statusClassWidth) + "xx"
	default:
		return jsemconv.VercelStatusClassNone
	}
}

// classify applies the counting rule to one lambda line. It returns the failure kind, the status
// to record (0 when there's no valid one), and false when the line doesn't mark a
// failure. L is the line's statusCode, P its proxy.statusCode (0 when absent), and S the client's
// status: P when valid, else L when valid.
func classify(line *drainLine) (string, int, bool) {
	l, p := line.StatusCode, 0
	if line.Proxy != nil {
		p = line.Proxy.StatusCode
	}

	s := 0
	switch {
	case validStatus(p):
		s = p
	case validStatus(l):
		s = l
	}

	switch {
	case p == statusNone:
		if l == statusNone || serverError(l) {
			return jsemconv.VercelFailureKindBackground, validOrZero(l), true
		}

		return "", 0, false
	case s == statusTimeout:
		return jsemconv.VercelFailureKindTimeout, statusTimeout, true
	case l == statusNone:
		return jsemconv.VercelFailureKindCrash, s, true
	case serverError(s):
		return jsemconv.VercelFailureKindError, s, true
	default:
		return "", 0, false
	}
}

// serverError reports whether code is a 5xx.
func serverError(code int) bool {
	return code >= minServerError && code <= maxStatus
}

// validOrZero returns code when it's a valid status, else 0.
func validOrZero(code int) int {
	if validStatus(code) {
		return code
	}

	return 0
}

// seenEntry is one counted request in the order it was counted.
type seenEntry struct {
	id string
	at time.Time
}

// failureSet remembers the request IDs of failures counted in the last failureTTL. Only failing
// requests' IDs go in it, so its size follows failures, not traffic.
type failureSet struct {
	mu        sync.Mutex
	seen      map[string]time.Time
	order     []seenEntry // oldest first; an ID re-counted after expiring has a stale earlier entry
	lastSweep time.Time
	max       int
}

func newFailureSet(limit int) *failureSet {
	return &failureSet{seen: map[string]time.Time{}, max: limit}
}

// first records id at now and reports whether it's the first failure for id in the last
// failureTTL.
func (fs *failureSet) first(id string, now time.Time) bool {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	if at, ok := fs.seen[id]; ok && now.Sub(at) < failureTTL {
		return false
	}

	if now.Sub(fs.lastSweep) >= sweepEvery {
		fs.dropWhile(func(e seenEntry) bool { return now.Sub(e.at) >= failureTTL })
		fs.lastSweep = now
	}

	fs.dropWhile(func(seenEntry) bool { return len(fs.seen) >= fs.max })

	fs.seen[id] = now
	fs.order = append(fs.order, seenEntry{id: id, at: now})

	return true
}

// dropWhile removes the oldest entries while drop says so.
func (fs *failureSet) dropWhile(drop func(seenEntry) bool) {
	n := 0
	for n < len(fs.order) && drop(fs.order[n]) {
		e := fs.order[n]
		if at, ok := fs.seen[e.id]; ok && at.Equal(e.at) {
			delete(fs.seen, e.id)
		}
		n++
	}

	if n > 0 {
		fs.order = append(fs.order[:0:0], fs.order[n:]...)
	}
}

// size is how many request IDs the set holds.
func (fs *failureSet) size() int {
	fs.mu.Lock()
	defer fs.mu.Unlock()

	return len(fs.seen)
}

// instruments are vercel-drain's metrics.
type instruments struct {
	failures metric.Int64Counter
	lines    metric.Int64Counter
	lag      metric.Float64Histogram
}

// newInstruments creates the metrics on the global meter provider, which obs.Start sets. An
// error goes to the OpenTelemetry error handler, and the instrument is then a no-op.
func newInstruments() instruments {
	meter := otel.Meter(scope)

	failures, err := meter.Int64Counter(jsemconv.VercelFunctionFailuresName,
		metric.WithUnit(jsemconv.VercelFunctionFailuresUnit),
		metric.WithDescription(jsemconv.VercelFunctionFailuresDescription))
	if err != nil {
		otel.Handle(err)
	}

	lines, err := meter.Int64Counter(jsemconv.VercelDrainLinesName,
		metric.WithUnit(jsemconv.VercelDrainLinesUnit),
		metric.WithDescription(jsemconv.VercelDrainLinesDescription))
	if err != nil {
		otel.Handle(err)
	}

	lag, err := meter.Float64Histogram(jsemconv.VercelDrainLagName,
		metric.WithUnit(jsemconv.VercelDrainLagUnit),
		metric.WithDescription(jsemconv.VercelDrainLagDescription),
		metric.WithExplicitBucketBoundaries(lagBuckets()...))
	if err != nil {
		otel.Handle(err)
	}

	return instruments{failures: failures, lines: lines, lag: lag}
}

// count runs one line through the failure counter and the census, and reports whether it's ours
// (so not to be forwarded). It runs before the ours filter, so a failed request whose only lines
// are ours still counts.
func (rc *Receiver) count(ctx context.Context, line *drainLine, now time.Time) bool {
	isOurs := ours(line.Message)
	outcome := jsemconv.VercelLineOutcomeForwarded

	switch {
	case line.Source == sourceLambda && line.RequestID != "":
		if kind, status, failed := classify(line); failed {
			outcome = jsemconv.VercelLineOutcomeRepeat
			if rc.failed.first(line.RequestID, now) {
				outcome = jsemconv.VercelLineOutcomeFailure
				rc.recordFailure(ctx, kind, status)
			}
		} else if isOurs {
			outcome = jsemconv.VercelLineOutcomeOurs
		}
	case isOurs:
		outcome = jsemconv.VercelLineOutcomeOurs
	case line.Source == sourceLambda:
		outcome = jsemconv.VercelLineOutcomeNoRequestId
	}

	source := jsemconv.VercelSourceKey.String(knownSource(line.Source))
	rc.metrics.lines.Add(ctx, 1, metric.WithAttributes(source,
		jsemconv.VercelLogTypeKey.String(logType(line.Type)),
		jsemconv.VercelStatusClassKey.String(statusClass(line.StatusCode)),
		jsemconv.VercelLineOutcomeKey.String(outcome)))

	if line.Timestamp > 0 {
		lag := max(now.Sub(time.UnixMilli(line.Timestamp)), 0)
		rc.metrics.lag.Record(ctx, lag.Seconds(), metric.WithAttributes(source))
	}

	return isOurs
}

// recordFailure adds one failed request to the counter.
func (rc *Receiver) recordFailure(ctx context.Context, kind string, status int) {
	attrs := []attribute.KeyValue{jsemconv.VercelFailureKindKey.String(kind)}
	if validStatus(status) {
		attrs = append(attrs, semconv.HTTPResponseStatusCode(status))
	}

	rc.metrics.failures.Add(ctx, 1, metric.WithAttributes(attrs...))
}
