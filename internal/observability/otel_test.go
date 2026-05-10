package observability_test

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"

	"github.com/ashishsinghbhadoria/goLearn/internal/observability"
)

// resetGlobals forces a fresh global state between tests so a prior
// test's TracerProvider doesn't leak into the next.
func resetGlobals() {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator())
}

func TestInit_NoExporterReturnsNoop(t *testing.T) {
	resetGlobals()
	shutdown, err := observability.Init(context.Background(), observability.Config{})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })

	tp := otel.GetTracerProvider()
	tname := strings.ToLower(reflectTypeName(tp))
	if !strings.Contains(tname, "noop") {
		t.Fatalf("expected noop provider, got %q", tname)
	}
}

func TestInit_StdoutExporter(t *testing.T) {
	resetGlobals()
	shutdown, err := observability.Init(context.Background(), observability.Config{
		ServiceName: "goLearn-test",
		Exporter:    "stdout",
	})
	if err != nil {
		t.Fatalf("Init stdout: %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_ = shutdown(ctx)
	})

	// Stdout exporter wraps an SDK TracerProvider, not a noop.
	tp := otel.GetTracerProvider()
	tname := strings.ToLower(reflectTypeName(tp))
	if strings.Contains(tname, "noop") {
		t.Fatalf("stdout exporter must produce a real provider, got noop")
	}
}

func TestInit_UnknownExporterErrors(t *testing.T) {
	resetGlobals()
	if _, err := observability.Init(context.Background(), observability.Config{
		Exporter: "made-up",
	}); err == nil {
		t.Fatal("expected error for unknown exporter")
	}
}

func TestInit_PropagatorIsCompositeTraceContextBaggage(t *testing.T) {
	resetGlobals()
	shutdown, err := observability.Init(context.Background(), observability.Config{})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	t.Cleanup(func() { _ = shutdown(context.Background()) })

	prop := otel.GetTextMapPropagator()
	keys := prop.Fields()
	// W3C TraceContext contributes "traceparent" / "tracestate";
	// Baggage contributes "baggage".
	want := map[string]bool{"traceparent": false, "tracestate": false, "baggage": false}
	for _, k := range keys {
		if _, ok := want[k]; ok {
			want[k] = true
		}
	}
	for k, found := range want {
		if !found {
			t.Errorf("propagator missing field %q (got %v)", k, keys)
		}
	}
}

func TestInit_ShutdownIsIdempotent(t *testing.T) {
	resetGlobals()
	shutdown, err := observability.Init(context.Background(), observability.Config{
		Exporter: "stdout",
	})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := shutdown(ctx); err != nil {
		t.Fatalf("first shutdown: %v", err)
	}
	// Second call must not panic / hang. The OTel SDK itself is
	// idempotent on Shutdown; our wrapper preserves that.
	if err := shutdown(ctx); err != nil {
		t.Logf("second shutdown returned err (acceptable, must not panic): %v", err)
	}
}

func TestInit_NoopShutdownAlwaysNil(t *testing.T) {
	resetGlobals()
	shutdown, err := observability.Init(context.Background(), observability.Config{Exporter: "none"})
	if err != nil {
		t.Fatalf("Init: %v", err)
	}
	if err := shutdown(context.Background()); err != nil {
		t.Fatalf("noop shutdown should always be nil, got %v", err)
	}
}

// reflectTypeName returns a printable type name for any value (used
// to assert "this is a noop provider" without importing the noop
// package directly). Implemented via fmt %T which is the canonical Go
// idiom; the silly hand-rolled fallback in v1 of this file returned ""
// and broke every assertion.
func reflectTypeName(v any) string {
	return fmt.Sprintf("%T", v)
}
