// Package observability bootstraps the process-wide OpenTelemetry
// TracerProvider and propagator. Call Init once at program start and
// defer the returned shutdown func so spans get flushed on exit.
package observability

import (
	"context"
	"errors"
	"fmt"
	"os"
	"runtime/debug"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracegrpc"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	tracenoop "go.opentelemetry.io/otel/trace/noop"
)

// Config drives Init. Empty fields fall back to environment variables
// (OTEL_SERVICE_NAME, OTEL_EXPORTER_OTLP_ENDPOINT, OTEL_TRACES_EXPORTER).
type Config struct {
	ServiceName string
	Endpoint    string
	Exporter    string
	Environment string
}

// Init configures the global TracerProvider + TextMapPropagator and
// returns a shutdown func that flushes spans (5 s timeout). The
// exporter is chosen as follows:
//
//   - cfg.Exporter == "otlp" or OTEL_EXPORTER_OTLP_ENDPOINT set
//     → OTLP/gRPC exporter to the endpoint.
//   - cfg.Exporter == "stdout" → stdout JSON exporter.
//   - "none" / "" with no OTLP endpoint → no-op TracerProvider so
//     the process pays zero per-span overhead and tests stay quiet.
//
// Init is idempotent in the sense that it can be called once per
// process; calling twice replaces the previous provider but does NOT
// flush its spans — call the previous shutdown first.
func Init(ctx context.Context, cfg Config) (func(context.Context) error, error) {
	cfg = cfg.withDefaults()

	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	exporter, err := buildExporter(ctx, cfg)
	if err != nil {
		return nil, err
	}
	if exporter == nil {
		// No-op TracerProvider: zero per-span work, fine for tests
		// and for prod-without-tracing.
		otel.SetTracerProvider(tracenoop.NewTracerProvider())
		return func(context.Context) error { return nil }, nil
	}

	// Use resource.New with WithAttributes (no explicit schema URL) so
	// the merge with resource.Default() — which carries whatever schema
	// the installed SDK ships with — never conflicts. The attribute
	// keys match OTel semantic conventions even though we don't import
	// a specific semconv version.
	res, err := resource.New(ctx,
		resource.WithFromEnv(),
		resource.WithProcess(),
		resource.WithTelemetrySDK(),
		resource.WithAttributes(
			attribute.String("service.name", cfg.ServiceName),
			attribute.String("service.version", serviceVersion()),
			attribute.String("deployment.environment.name", cfg.Environment),
		),
	)
	if err != nil {
		return nil, fmt.Errorf("observability: build resource: %w", err)
	}

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.AlwaysSample())),
	)
	otel.SetTracerProvider(tp)

	return func(ctx context.Context) error {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		return tp.Shutdown(ctx)
	}, nil
}

// buildExporter returns nil (no exporter) when neither an OTLP
// endpoint nor a stdout opt-in is configured.
func buildExporter(ctx context.Context, cfg Config) (sdktrace.SpanExporter, error) {
	switch {
	case cfg.Exporter == "stdout":
		exp, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
		if err != nil {
			return nil, fmt.Errorf("observability: stdout exporter: %w", err)
		}
		return exp, nil
	case cfg.Exporter == "otlp" || cfg.Endpoint != "":
		opts := []otlptracegrpc.Option{otlptracegrpc.WithInsecure()}
		if cfg.Endpoint != "" {
			opts = append(opts, otlptracegrpc.WithEndpoint(cfg.Endpoint))
		}
		exp, err := otlptracegrpc.New(ctx, opts...)
		if err != nil {
			return nil, fmt.Errorf("observability: otlp exporter: %w", err)
		}
		return exp, nil
	case cfg.Exporter == "" || cfg.Exporter == "none":
		return nil, nil
	default:
		return nil, fmt.Errorf("observability: unknown exporter %q (want otlp|stdout|none)", cfg.Exporter)
	}
}

func (c Config) withDefaults() Config {
	if c.ServiceName == "" {
		c.ServiceName = firstNonEmpty(os.Getenv("OTEL_SERVICE_NAME"), "goLearn")
	}
	if c.Endpoint == "" {
		c.Endpoint = os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT")
	}
	if c.Exporter == "" {
		c.Exporter = strings.ToLower(os.Getenv("OTEL_TRACES_EXPORTER"))
	}
	if c.Environment == "" {
		c.Environment = firstNonEmpty(os.Getenv("DEPLOYMENT_ENV"), "dev")
	}
	return c
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

// serviceVersion reads the build's main module version (via
// runtime/debug). Falls back to "dev" for `go run` invocations.
func serviceVersion() string {
	bi, ok := debug.ReadBuildInfo()
	if !ok {
		return "dev"
	}
	if bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		return bi.Main.Version
	}
	for _, s := range bi.Settings {
		if s.Key == "vcs.revision" && s.Value != "" {
			return s.Value
		}
	}
	return "dev"
}

// ErrShutdownTimeout is returned by the shutdown func when the
// TracerProvider doesn't drain within the 5 s window.
var ErrShutdownTimeout = errors.New("observability: tracer shutdown timed out")
