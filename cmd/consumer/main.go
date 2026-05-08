// Package main is the user.* event consumer. It owns one durable
// pull consumer on the USERS JetStream stream and writes a row to
// notifications for every user.created event it ACKs.
//
// Trace flow:
//
//	cmd/api → POST /users span
//	   └─ user.Service.Register span
//	         └─ pgx INSERT users span (otelpgx)
//	               └─ nats publish user.created span
//	                  ↓ W3C traceparent header
//	cmd/consumer → consumer.user.created span
//	   └─ pgx INSERT notifications span (otelpgx)
//
// All spans share one trace_id; slog lines on both sides emit it via
// the TraceHandler so logs and Jaeger UI line up.
//
// Idempotency: JetStream redelivers on Nak / AckWait expiry. The
// notifications table has UNIQUE(event_id) and the INSERT uses
// ON CONFLICT DO NOTHING, so a re-delivered message becomes a no-op
// row insert and the span records `notification.duplicate=true`.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/exaring/otelpgx"
	"github.com/jackc/pgx/v5/pgxpool"
	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/trace"

	natsevents "github.com/ashishsinghbhadoria/goLearn/internal/events/nats"
	"github.com/ashishsinghbhadoria/goLearn/internal/observability"
	"github.com/ashishsinghbhadoria/goLearn/internal/storage/postgres/pgdb"
	pkglogger "github.com/ashishsinghbhadoria/goLearn/pkg/logger"
)

const (
	durableName  = "user-welcome"
	notifyKind   = "welcome"
	tracerName   = "github.com/ashishsinghbhadoria/goLearn/cmd/consumer"
	ackWait      = 30 * time.Second
	maxDeliver   = 5
	nakDelay     = 2 * time.Second
	pendingLimit = 256
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "fatal:", err)
		os.Exit(1)
	}
}

type consumerConfig struct {
	natsURL      string
	dbDSN        string
	otelService  string
	otelEndpoint string
	otelExporter string
}

func parseFlags() consumerConfig {
	cfg := consumerConfig{}
	flag.StringVar(&cfg.natsURL, "nats-url", firstNonEmpty(os.Getenv("NATS_URL"), "nats://localhost:4222"), "NATS JetStream URL")
	flag.StringVar(&cfg.dbDSN, "db-dsn", os.Getenv("DATABASE_URL"), "Postgres DSN; defaults to $DATABASE_URL")
	flag.StringVar(&cfg.otelService, "otel-service-name", firstNonEmpty(os.Getenv("OTEL_SERVICE_NAME"), "goLearn-consumer"), "OTel service.name resource attr")
	flag.StringVar(&cfg.otelEndpoint, "otel-endpoint", os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT"), "OTLP gRPC endpoint")
	flag.StringVar(&cfg.otelExporter, "otel-exporter", os.Getenv("OTEL_TRACES_EXPORTER"), "trace exporter: otlp | stdout | none")
	flag.Parse()
	return cfg
}

func run() error {
	cfg := parseFlags()
	if cfg.dbDSN == "" {
		return errors.New("missing -db-dsn (or set DATABASE_URL)")
	}

	logger := slog.New(pkglogger.NewTraceHandler(
		slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}),
	))

	rootCtx, rootCancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer rootCancel()

	otelShutdown, err := observability.Init(rootCtx, observability.Config{
		ServiceName: cfg.otelService,
		Endpoint:    cfg.otelEndpoint,
		Exporter:    cfg.otelExporter,
	})
	if err != nil {
		return fmt.Errorf("observability init: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := otelShutdown(shutdownCtx); err != nil {
			logger.Error("otel shutdown", "err", err)
		}
	}()

	pool, err := newPool(rootCtx, cfg.dbDSN)
	if err != nil {
		return fmt.Errorf("pgxpool: %w", err)
	}
	defer pool.Close()
	queries := pgdb.New(pool)

	conn, err := natsgo.Connect(cfg.natsURL,
		natsgo.Name("goLearn-consumer"),
		natsgo.Timeout(5*time.Second),
		natsgo.MaxReconnects(-1),
		natsgo.ReconnectWait(2*time.Second),
	)
	if err != nil {
		return fmt.Errorf("nats dial %s: %w", cfg.natsURL, err)
	}
	defer func() { _ = conn.Drain() }()

	js, err := jetstream.New(conn)
	if err != nil {
		return fmt.Errorf("jetstream: %w", err)
	}
	cons, err := js.CreateOrUpdateConsumer(rootCtx, natsevents.StreamName, jetstream.ConsumerConfig{
		Durable:       durableName,
		FilterSubject: natsevents.SubjectUserCreated,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       ackWait,
		MaxDeliver:    maxDeliver,
	})
	if err != nil {
		return fmt.Errorf("ensure consumer %s: %w", durableName, err)
	}
	logger.Info("consumer ready",
		"stream", natsevents.StreamName,
		"durable", durableName,
		"subject", natsevents.SubjectUserCreated,
	)

	iter, err := cons.Messages(jetstream.PullMaxMessages(pendingLimit))
	if err != nil {
		return fmt.Errorf("messages iterator: %w", err)
	}
	go func() {
		<-rootCtx.Done()
		iter.Stop()
	}()

	tracer := otel.Tracer(tracerName)
	for {
		msg, err := iter.Next()
		if err != nil {
			if errors.Is(err, jetstream.ErrMsgIteratorClosed) {
				logger.Info("iterator closed; shutting down")
				return nil
			}
			logger.Error("iter next", "err", err)
			return fmt.Errorf("iter next: %w", err)
		}
		handle(rootCtx, logger, tracer, queries, msg)
	}
}

// handle is one message → one span. Trace context is extracted from
// the message headers BEFORE the span starts so the consumer span
// chains under the producer's trace.
func handle(parent context.Context, logger *slog.Logger, tracer trace.Tracer, queries *pgdb.Queries, msg jetstream.Msg) {
	ctx := otel.GetTextMapPropagator().Extract(parent, natsevents.HeaderCarrier(msg.Headers()))
	ctx, span := tracer.Start(ctx, "consumer.user.created",
		trace.WithSpanKind(trace.SpanKindConsumer),
		trace.WithAttributes(
			attribute.String("messaging.system", "nats"),
			attribute.String("messaging.destination.name", msg.Subject()),
		),
	)
	defer span.End()

	eventID := msg.Headers().Get(jetstream.MsgIDHeader)
	span.SetAttributes(attribute.String("messaging.message.id", eventID))

	var env eventEnvelope
	if err := json.Unmarshal(msg.Data(), &env); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "unmarshal")
		logger.ErrorContext(ctx, "consumer parse failed", "err", err, "event_id", eventID)
		// Bad payload: terminate so JetStream stops redelivering.
		_ = msg.Term()
		return
	}

	if err := queries.InsertNotification(ctx, pgdb.InsertNotificationParams{
		EventID: env.EventID,
		UserID:  env.User.ID,
		Kind:    notifyKind,
	}); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "insert")
		logger.ErrorContext(ctx, "consumer insert failed", "err", err, "event_id", env.EventID)
		_ = msg.NakWithDelay(nakDelay)
		return
	}

	if err := msg.Ack(); err != nil {
		span.RecordError(err)
		span.SetStatus(codes.Error, "ack")
		logger.ErrorContext(ctx, "consumer ack failed", "err", err, "event_id", env.EventID)
		return
	}

	logger.InfoContext(ctx, "user.created processed",
		"event_id", env.EventID,
		"user_id", env.User.ID,
		"schema", env.Schema,
	)
}

// newPool builds a pgxpool with the otelpgx tracer wired in so every
// query becomes a span under whatever ctx the caller passes.
func newPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	pcfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, fmt.Errorf("parse dsn: %w", err)
	}
	pcfg.ConnConfig.Tracer = otelpgx.NewTracer(
		otelpgx.WithTracerProvider(otel.GetTracerProvider()),
	)
	pool, err := pgxpool.NewWithConfig(ctx, pcfg)
	if err != nil {
		return nil, fmt.Errorf("pgxpool new: %w", err)
	}
	pingCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := pool.Ping(pingCtx); err != nil {
		pool.Close()
		return nil, fmt.Errorf("pgxpool ping: %w", err)
	}
	return pool, nil
}

// eventEnvelope mirrors the shape produced by the NATS publisher
// (internal/events/nats). Kept private because the wire schema is
// owned by the producer; consumer just shadows it.
type eventEnvelope struct {
	EventID    string      `json:"event_id"`
	Schema     string      `json:"schema"`
	OccurredAt string      `json:"occurred_at"`
	User       userPayload `json:"user"`
}

type userPayload struct {
	ID    string `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}
