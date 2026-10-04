package verceldrain

import (
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"iter"
	"net/http"
	"slices"
	"strings"
	"time"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/log"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"

	"github.com/justabill-org/justabill/obs"
	jsemconv "github.com/justabill-org/justabill/obs/semconv"
)

// maxMessageBytes caps a forwarded message. Vercel sends up to 256 KB; a platform error needs far
// less, and Cloud Logging bills by the byte.
const maxMessageBytes = 16 << 10

// The range of real HTTP status codes.
const (
	minStatus = 100
	maxStatus = 599
)

// drainLine is one Vercel log drain entry (https://vercel.com/docs/drains/reference/logs). It
// holds only the fields we forward, so the client IP, user agent, JA3/JA4 fingerprints, referer
// and the rest are never decoded.
type drainLine struct {
	DeploymentID string     `json:"deploymentId"`
	Source       string     `json:"source"`
	Host         string     `json:"host"`
	Timestamp    int64      `json:"timestamp"`
	Level        string     `json:"level"`
	Message      string     `json:"message"`
	Type         string     `json:"type"`
	Path         string     `json:"path"`
	StatusCode   int        `json:"statusCode"`
	RequestID    string     `json:"requestId"`
	Environment  string     `json:"environment"`
	TraceID      string     `json:"traceId"`
	SpanID       string     `json:"spanId"`
	TraceIDDot   string     `json:"trace.id"`
	SpanIDDot    string     `json:"span.id"`
	Proxy        *drainHTTP `json:"proxy"`
}

// drainHTTP is the allowlisted part of a line's proxy object.
type drainHTTP struct {
	Method     string `json:"method"`
	Path       string `json:"path"`
	StatusCode int    `json:"statusCode"`
}

// drain forwards a batch, in either of Vercel's formats (a JSON array or NDJSON). Lines that
// don't parse are counted and skipped; a batch with no line that parses gets 400.
func (rc *Receiver) drain(w http.ResponseWriter, r *http.Request, body []byte) {
	ctx := r.Context()
	lines, bad := parseBatch(body)

	if len(lines) == 0 && bad > 0 {
		rc.logger.WarnContext(ctx, "vercel-drain: unparseable drain batch", "lines", bad)
		http.Error(w, "unparseable batch", http.StatusBadRequest)

		return
	}

	var forwarded, duplicates int

	now := rc.now()
	for i := range lines {
		if rc.count(ctx, &lines[i], now) {
			duplicates++

			continue
		}

		rc.emit(ctx, &lines[i])
		forwarded++
	}

	if bad > 0 {
		rc.logger.WarnContext(ctx, "vercel-drain: skipped unparseable drain lines", "lines", bad)
	}

	rc.logger.DebugContext(ctx, "vercel-drain: drain batch",
		"forwarded", forwarded, "duplicates", duplicates, "skipped", bad)
	w.WriteHeader(http.StatusOK)
}

// parseBatch decodes a JSON array or NDJSON body, returning the lines that parsed and how many
// didn't. Each line is decoded on its own, so one bad element of an array doesn't drop the rest.
func parseBatch(body []byte) ([]drainLine, int) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var raws []json.RawMessage
		if err := json.Unmarshal(trimmed, &raws); err != nil {
			return nil, 1
		}

		return decodeLines(slices.Values(raws))
	}

	return decodeLines(bytes.SplitSeq(trimmed, []byte("\n")))
}

// decodeLines decodes each non-blank raw line, returning the lines that parsed and how many didn't.
func decodeLines[R ~[]byte](raws iter.Seq[R]) ([]drainLine, int) {
	var (
		lines []drainLine
		bad   int
	)

	for raw := range raws {
		trimmed := bytes.TrimSpace(raw)
		if len(trimmed) == 0 {
			continue
		}

		var line drainLine
		if err := json.Unmarshal(trimmed, &line); err != nil {
			bad++

			continue
		}

		lines = append(lines, line)
	}

	return lines, bad
}

// ours reports whether a message is a JSON line our own code wrote and already exported through
// obs: an object with a trace ID in obs's Cloud Logging field or as trace_id.
func ours(message string) bool {
	message = strings.TrimSpace(message)
	if !strings.HasPrefix(message, "{") {
		return false
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(message), &fields); err != nil {
		return false
	}

	_, cloudTrace := fields["logging.googleapis.com/trace"]
	_, traceID := fields["trace_id"]

	return cloudTrace || traceID
}

// emit forwards one line as a log record, in the line's trace when it has one.
func (rc *Receiver) emit(ctx context.Context, line *drainLine) {
	var rec log.Record

	rec.SetTimestamp(time.UnixMilli(line.Timestamp))
	rec.SetObservedTimestamp(rc.now())
	rec.SetSeverity(severity(line.Level))
	rec.SetSeverityText(line.Level)
	rec.SetBody(attribute.StringValue(obs.Redact(truncate(line.Message, maxMessageBytes))))
	rec.AddAttributes(lineAttributes(line)...)

	rc.records.Emit(withLineTrace(ctx, line), rec)
}

// lineAttributes is the allowlist: nothing else from a line reaches telemetry.
func lineAttributes(line *drainLine) []attribute.KeyValue {
	attrs := []attribute.KeyValue{jsemconv.VercelSourceKey.String(line.Source)}
	if line.Type != "" {
		attrs = append(attrs, jsemconv.VercelLogTypeKey.String(logType(line.Type)))
	}

	add := func(set bool, kv attribute.KeyValue) {
		if set {
			attrs = append(attrs, kv)
		}
	}

	add(line.DeploymentID != "", semconv.DeploymentID(line.DeploymentID))
	add(line.Environment != "", semconv.DeploymentEnvironmentNameKey.String(line.Environment))
	add(line.RequestID != "", jsemconv.VercelRequestIDKey.String(line.RequestID))
	add(line.Host != "", semconv.ServerAddress(line.Host))

	path, status := line.Path, line.StatusCode
	if line.Proxy != nil {
		add(line.Proxy.Method != "", semconv.HTTPRequestMethodKey.String(line.Proxy.Method))
		path = cmp.Or(line.Proxy.Path, path)
		if !validStatus(status) {
			status = line.Proxy.StatusCode
		}
	}

	path = obs.Redact(stripQuery(path))
	add(path != "", semconv.URLPath(path))
	add(validStatus(status), semconv.HTTPResponseStatusCode(status))

	return attrs
}

// withLineTrace puts the line's trace and span IDs, when both parse, into ctx as a remote span
// context, so the record carries them and links to the trace.
func withLineTrace(ctx context.Context, line *drainLine) context.Context {
	traceID, err := trace.TraceIDFromHex(cmp.Or(line.TraceID, line.TraceIDDot))
	if err != nil {
		return ctx
	}

	spanID, err := trace.SpanIDFromHex(cmp.Or(line.SpanID, line.SpanIDDot))
	if err != nil {
		return ctx
	}

	return trace.ContextWithRemoteSpanContext(ctx,
		trace.NewSpanContext(trace.SpanContextConfig{TraceID: traceID, SpanID: spanID, Remote: true}))
}

// validStatus reports whether code is an HTTP status. Vercel sends -1 when a function crashed
// without responding (the proxy's code then says what the client got) or revalidated in the
// background.
func validStatus(code int) bool {
	return code >= minStatus && code <= maxStatus
}

// severity maps Vercel's levels (info, warning, error, fatal) to OpenTelemetry's.
func severity(level string) log.Severity {
	switch strings.ToLower(level) {
	case "warning", "warn":
		return log.SeverityWarn
	case "error":
		return log.SeverityError
	case "fatal":
		return log.SeverityFatal
	default:
		return log.SeverityInfo
	}
}

// stripQuery drops a path's query string and fragment.
func stripQuery(path string) string {
	if i := strings.IndexAny(path, "?#"); i >= 0 {
		return path[:i]
	}

	return path
}

// truncate cuts s to at most n bytes without splitting a UTF-8 sequence.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}

	return strings.ToValidUTF8(s[:n], "")
}
