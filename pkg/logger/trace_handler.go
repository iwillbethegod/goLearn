// Package logger provides a slog.Handler that decorates an inner
// handler with OpenTelemetry trace correlation. Every Record handled
// gets `trace_id` and `span_id` attrs derived from the SpanContext in
// ctx, so log lines link back to spans in Jaeger / any OTLP backend.
//
// The handler implements all four slog.Handler methods so chains
// involving WithAttrs / WithGroup keep working — a partial impl
// would silently drop logger.WithGroup("…") nesting.
package logger

import (
	"context"
	"log/slog"

	"go.opentelemetry.io/otel/trace"
)

// TraceHandler wraps an inner slog.Handler and injects trace_id /
// span_id attrs from ctx when the SpanContext is valid.
type TraceHandler struct {
	inner slog.Handler
}

// NewTraceHandler decorates h. Use it from cmd/api, cmd/ingest, and
// cmd/consumer at logger construction time:
//
//	base := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{...})
//	logger := slog.New(logger.NewTraceHandler(base))
//
// Reminder: callers must use logger.InfoContext(ctx, …) /
// LogAttrs(ctx, …) for ctx to reach Handle. Plain logger.Info(...)
// uses context.Background() and the handler will see no SpanContext.
func NewTraceHandler(h slog.Handler) *TraceHandler {
	return &TraceHandler{inner: h}
}

// Enabled forwards to the inner handler so HandlerOptions.Level still
// applies.
func (h *TraceHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle adds trace_id and span_id when ctx carries a valid
// SpanContext, then delegates to the inner handler.
func (h *TraceHandler) Handle(ctx context.Context, r slog.Record) error {
	if sc := trace.SpanContextFromContext(ctx); sc.IsValid() {
		r.AddAttrs(
			slog.String("trace_id", sc.TraceID().String()),
			slog.String("span_id", sc.SpanID().String()),
		)
	}
	return h.inner.Handle(ctx, r)
}

// WithAttrs returns a new TraceHandler whose inner handler has the
// extra attrs pre-bound. Forwarding this is what keeps
// logger.With(...) chains intact.
func (h *TraceHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &TraceHandler{inner: h.inner.WithAttrs(attrs)}
}

// WithGroup forwards group nesting so logger.WithGroup("http") still
// scopes subsequent attrs.
func (h *TraceHandler) WithGroup(name string) slog.Handler {
	return &TraceHandler{inner: h.inner.WithGroup(name)}
}
