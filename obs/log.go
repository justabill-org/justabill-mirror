package obs

import (
	"context"
	"io"
	"log/slog"
	"math"
	"slices"

	"go.opentelemetry.io/otel/trace"
)

// Cloud Logging's structured-logging fields
// (https://cloud.google.com/logging/docs/structured-logging#special-payload-fields).
const (
	fieldSeverity     = "severity"
	fieldMessage      = "message"
	fieldTrace        = "logging.googleapis.com/trace"
	fieldSpanID       = "logging.googleapis.com/spanId"
	fieldTraceSampled = "logging.googleapis.com/trace_sampled"
)

// stderrHandler writes JSON lines that Cloud Logging (and GKE's log agent) parse into severity,
// message and trace correlation, and that stay readable in a terminal. Trace fields stay at the
// top level of the line even under a group opened with WithGroup, where Cloud Logging looks for
// them.
type stderrHandler struct {
	// root is the JSON handler before any WithAttrs or WithGroup; json is root with ops applied.
	root      slog.Handler
	json      slog.Handler
	ops       []func(slog.Handler) slog.Handler
	grouped   bool
	projectID string
}

func newStderrHandler(w io.Writer, projectID string) slog.Handler {
	json := slog.NewJSONHandler(w, &slog.HandlerOptions{
		// leveled filters levels, so the JSON handler takes everything.
		Level:       slog.Level(math.MinInt),
		ReplaceAttr: cloudLoggingAttr,
	})

	return &stderrHandler{root: json, json: json, projectID: projectID}
}

func (h *stderrHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.json.Enabled(ctx, level)
}

func (h *stderrHandler) Handle(ctx context.Context, r slog.Record) error {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return h.json.Handle(ctx, r)
	}

	traceID := sc.TraceID().String()
	if h.projectID != "" {
		traceID = "projects/" + h.projectID + "/traces/" + traceID
	}

	attrs := []slog.Attr{
		slog.String(fieldTrace, traceID),
		slog.String(fieldSpanID, sc.SpanID().String()),
		slog.Bool(fieldTraceSampled, sc.IsSampled()),
	}

	if !h.grouped {
		r = r.Clone()
		r.AddAttrs(attrs...)

		return h.json.Handle(ctx, r)
	}

	// Record attributes would land inside the open group, so add the trace fields to the root
	// handler and replay the WithAttrs and WithGroup calls on top of it.
	next := h.root.WithAttrs(attrs)
	for _, op := range h.ops {
		next = op(next)
	}

	return next.Handle(ctx, r)
}

func (h *stderrHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	if len(attrs) == 0 {
		return h
	}

	return h.with(func(next slog.Handler) slog.Handler { return next.WithAttrs(attrs) }, false)
}

func (h *stderrHandler) WithGroup(name string) slog.Handler {
	if name == "" {
		return h
	}

	return h.with(func(next slog.Handler) slog.Handler { return next.WithGroup(name) }, true)
}

func (h *stderrHandler) with(op func(slog.Handler) slog.Handler, group bool) *stderrHandler {
	return &stderrHandler{
		root:      h.root,
		json:      op(h.json),
		ops:       append(slices.Clip(h.ops), op),
		grouped:   h.grouped || group,
		projectID: h.projectID,
	}
}

// cloudLoggingAttr renames slog's level and msg keys to Cloud Logging's severity and message.
func cloudLoggingAttr(groups []string, a slog.Attr) slog.Attr {
	if len(groups) > 0 {
		return a
	}

	switch a.Key {
	case slog.LevelKey:
		level, _ := a.Value.Any().(slog.Level)
		return slog.String(fieldSeverity, severity(level))
	case slog.MessageKey:
		return slog.String(fieldMessage, a.Value.String())
	default:
		return a
	}
}

// severity maps a slog level to Cloud Logging's LogSeverity names.
func severity(level slog.Level) string {
	switch {
	case level < slog.LevelInfo:
		return "DEBUG"
	case level < slog.LevelWarn:
		return "INFO"
	case level < slog.LevelError:
		return "WARNING"
	default:
		return "ERROR"
	}
}

// levelHandler drops records below its level before they reach the wrapped handler.
type levelHandler struct {
	next  slog.Handler
	level slog.Leveler
}

func leveled(next slog.Handler, level slog.Leveler) slog.Handler {
	return &levelHandler{next: next, level: level}
}

func (h *levelHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return level >= h.level.Level() && h.next.Enabled(ctx, level)
}

func (h *levelHandler) Handle(ctx context.Context, r slog.Record) error {
	return h.next.Handle(ctx, r)
}

func (h *levelHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return leveled(h.next.WithAttrs(attrs), h.level)
}

func (h *levelHandler) WithGroup(name string) slog.Handler {
	return leveled(h.next.WithGroup(name), h.level)
}
