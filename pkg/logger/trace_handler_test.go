package logger_test

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"go.opentelemetry.io/otel/trace"

	pkglogger "github.com/ashishsinghbhadoria/goLearn/pkg/logger"
)

// newJSONLogger gives us a JSON-handler-backed logger we can decode
// in tests, plus the buffer it writes to.
func newJSONLogger(t *testing.T, level slog.Level) (*slog.Logger, *bytes.Buffer) {
	t.Helper()
	buf := &bytes.Buffer{}
	base := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: level})
	return slog.New(pkglogger.NewTraceHandler(base)), buf
}

func decodeOne(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	line := strings.TrimSpace(buf.String())
	if line == "" {
		t.Fatal("no log line emitted")
	}
	out := map[string]any{}
	if err := json.Unmarshal([]byte(line), &out); err != nil {
		t.Fatalf("decode log line %q: %v", line, err)
	}
	return out
}

// validSpanContext returns a SpanContext with deterministic non-zero
// IDs so tests can assert on the exact values that should appear in
// the log line.
func validSpanContext(t *testing.T) trace.SpanContext {
	t.Helper()
	tid, err := trace.TraceIDFromHex("0102030405060708090a0b0c0d0e0f10")
	if err != nil {
		t.Fatal(err)
	}
	sid, err := trace.SpanIDFromHex("1112131415161718")
	if err != nil {
		t.Fatal(err)
	}
	return trace.NewSpanContext(trace.SpanContextConfig{
		TraceID:    tid,
		SpanID:     sid,
		TraceFlags: trace.FlagsSampled,
	})
}

func TestTraceHandler_AddsTraceAndSpanIDFromCtx(t *testing.T) {
	logger, buf := newJSONLogger(t, slog.LevelInfo)
	sc := validSpanContext(t)
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	logger.InfoContext(ctx, "hello", "key", "value")

	got := decodeOne(t, buf)
	if got["trace_id"] != "0102030405060708090a0b0c0d0e0f10" {
		t.Fatalf("trace_id = %v, want 0102…0f10", got["trace_id"])
	}
	if got["span_id"] != "1112131415161718" {
		t.Fatalf("span_id = %v, want 1112…1718", got["span_id"])
	}
	if got["msg"] != "hello" || got["key"] != "value" {
		t.Fatalf("inner attrs lost: %+v", got)
	}
}

func TestTraceHandler_OmitsAttrsWhenNoSpanContext(t *testing.T) {
	logger, buf := newJSONLogger(t, slog.LevelInfo)
	logger.InfoContext(context.Background(), "no-span-ctx")

	got := decodeOne(t, buf)
	if _, ok := got["trace_id"]; ok {
		t.Fatalf("trace_id leaked when ctx had no span: %+v", got)
	}
	if _, ok := got["span_id"]; ok {
		t.Fatalf("span_id leaked when ctx had no span: %+v", got)
	}
}

// Plain logger.Info uses ctx=Background internally — even if a span
// is in the *caller's* ctx, the handler can't see it. This is the
// documented footgun the package's docstring calls out; we lock it
// in as a regression test.
func TestTraceHandler_PlainInfoSeesNoCtx(t *testing.T) {
	logger, buf := newJSONLogger(t, slog.LevelInfo)
	sc := validSpanContext(t)
	_ = trace.ContextWithSpanContext(context.Background(), sc)

	logger.Info("this-uses-Background")

	got := decodeOne(t, buf)
	if _, ok := got["trace_id"]; ok {
		t.Fatalf("plain Info() leaked trace_id; reader should know to use InfoContext")
	}
}

func TestTraceHandler_EnabledForwarded(t *testing.T) {
	// Inner handler set to Warn — Info MUST be filtered out before our
	// trace decoration even runs (correctness AND perf).
	buf := &bytes.Buffer{}
	base := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelWarn})
	h := pkglogger.NewTraceHandler(base)

	if h.Enabled(context.Background(), slog.LevelInfo) {
		t.Fatal("Enabled(Info) should be false when inner is at Warn")
	}
	if !h.Enabled(context.Background(), slog.LevelWarn) {
		t.Fatal("Enabled(Warn) should be true when inner is at Warn")
	}
}

// WithAttrs/WithGroup forwarding: a partial impl would break
// logger.With(...) chains and silently drop group nesting.
func TestTraceHandler_WithAttrsForwarded(t *testing.T) {
	logger, buf := newJSONLogger(t, slog.LevelInfo)
	sub := logger.With("svc", "users")

	sub.InfoContext(context.Background(), "msg")
	got := decodeOne(t, buf)
	if got["svc"] != "users" {
		t.Fatalf("WithAttrs not forwarded: %+v", got)
	}
}

func TestTraceHandler_WithGroupForwarded(t *testing.T) {
	logger, buf := newJSONLogger(t, slog.LevelInfo)
	sub := logger.WithGroup("http").With("status", 200)

	sub.InfoContext(context.Background(), "request")
	got := decodeOne(t, buf)
	// JSON encoder nests the group as an object.
	httpAttrs, ok := got["http"].(map[string]any)
	if !ok {
		t.Fatalf("WithGroup not forwarded; got %+v", got)
	}
	if httpAttrs["status"] != float64(200) {
		t.Fatalf("status not under group: %+v", httpAttrs)
	}
}

func TestTraceHandler_WithAttrsPreservesTraceInjection(t *testing.T) {
	// Critical: chaining With() must not strip the TraceHandler from
	// the chain, otherwise trace_id stops appearing after the first
	// .With().
	logger, buf := newJSONLogger(t, slog.LevelInfo)
	sc := validSpanContext(t)
	ctx := trace.ContextWithSpanContext(context.Background(), sc)

	logger.With("svc", "users").InfoContext(ctx, "sub-logger msg")

	got := decodeOne(t, buf)
	if got["trace_id"] != "0102030405060708090a0b0c0d0e0f10" {
		t.Fatalf("WithAttrs decorator lost trace injection: %+v", got)
	}
	if got["svc"] != "users" {
		t.Fatalf("WithAttrs payload also missing: %+v", got)
	}
}
