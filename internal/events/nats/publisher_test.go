package nats_test

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats-server/v2/test"
	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"

	"github.com/ashishsinghbhadoria/goLearn/internal/model"
	natsevents "github.com/ashishsinghbhadoria/goLearn/internal/events/nats"
)

// TestMain wires a recording propagator + an in-memory tracer
// provider so trace-context-in-headers assertions are deterministic.
func TestMain(m *testing.M) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	otel.SetTracerProvider(sdktrace.NewTracerProvider(
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	))
	m.Run()
}

// startEmbeddedJetStream boots an in-process NATS server with
// JetStream enabled. StoreDir is per-test (t.TempDir()) so parallel
// `go test` runs don't collide on a shared file store. The server
// shuts down on test cleanup AND we wait for goroutines to drain.
func startEmbeddedJetStream(t *testing.T) (*server.Server, string) {
	t.Helper()
	opts := test.DefaultTestOptions
	opts.Port = -1 // ephemeral
	opts.JetStream = true
	opts.StoreDir = t.TempDir()

	s := test.RunServer(&opts)
	if !s.ReadyForConnections(5 * time.Second) {
		s.Shutdown()
		t.Fatalf("nats server not ready")
	}
	t.Cleanup(func() {
		s.Shutdown()
		s.WaitForShutdown()
	})
	return s, s.ClientURL()
}

// TestPublisherWritesToStream is the happy path: NewPublisher creates
// the USERS stream on first call (idempotent), and PublishUserCreated
// lands a message on user.created.
func TestPublisherWritesToStream(t *testing.T) {
	_, url := startEmbeddedJetStream(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pub, err := natsevents.NewPublisher(ctx, url, logger)
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	t.Cleanup(func() { _ = pub.Close() })

	u := model.User{ID: "u-1", Name: "Ada", Email: "ada@example.com", PasswordHash: "SHOULDNOTLEAK"}
	if err := pub.PublishUserCreated(ctx, u); err != nil {
		t.Fatalf("PublishUserCreated: %v", err)
	}

	// Subscribe with a separate connection, fetch the message.
	conn, err := natsgo.Connect(url)
	if err != nil {
		t.Fatalf("subscriber connect: %v", err)
	}
	defer conn.Drain()
	js, err := jetstream.New(conn)
	if err != nil {
		t.Fatalf("jetstream: %v", err)
	}
	cons, err := js.CreateOrUpdateConsumer(ctx, natsevents.StreamName, jetstream.ConsumerConfig{
		Durable:       "test-reader",
		FilterSubject: natsevents.SubjectUserCreated,
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	if err != nil {
		t.Fatalf("create consumer: %v", err)
	}
	msg, err := cons.Next(jetstream.FetchMaxWait(5 * time.Second))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	defer msg.Ack()

	// Subject + payload assertions.
	if msg.Subject() != natsevents.SubjectUserCreated {
		t.Fatalf("subject = %q, want %q", msg.Subject(), natsevents.SubjectUserCreated)
	}
	var env struct {
		EventID string `json:"event_id"`
		Schema  string `json:"schema"`
		User    struct {
			ID           string `json:"id"`
			Email        string `json:"email"`
			PasswordHash string `json:"password_hash"` // expect zero-value: payload must NOT carry the hash
		} `json:"user"`
	}
	if err := json.Unmarshal(msg.Data(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Schema != natsevents.SchemaUserCreatedV1 {
		t.Fatalf("schema = %q, want %q", env.Schema, natsevents.SchemaUserCreatedV1)
	}
	if env.User.ID != "u-1" || env.User.Email != "ada@example.com" {
		t.Fatalf("user payload mismatch: %+v", env.User)
	}
	if env.User.PasswordHash != "" {
		t.Fatalf("password_hash leaked into event payload: %q", env.User.PasswordHash)
	}

	// event_id == Nats-Msg-Id header (idempotency contract).
	if got := msg.Headers().Get(jetstream.MsgIDHeader); got != env.EventID {
		t.Fatalf("Nats-Msg-Id = %q, event_id = %q (must match)", got, env.EventID)
	}
}

// TestPublisherInjectsTraceContext: when called inside an active
// span, the message MUST carry a W3C `traceparent` header so the
// consumer can re-parent its span under the same trace.
func TestPublisherInjectsTraceContext(t *testing.T) {
	_, url := startEmbeddedJetStream(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	pub, err := natsevents.NewPublisher(ctx, url, logger)
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	t.Cleanup(func() { _ = pub.Close() })

	tracer := otel.Tracer("test")
	pubCtx, span := tracer.Start(ctx, "test-root")
	wantTraceID := span.SpanContext().TraceID().String()

	if err := pub.PublishUserCreated(pubCtx, model.User{ID: "u-2", Email: "x@y.com", Name: "X"}); err != nil {
		t.Fatalf("publish: %v", err)
	}
	span.End()

	conn, err := natsgo.Connect(url)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Drain()
	js, _ := jetstream.New(conn)
	cons, err := js.CreateOrUpdateConsumer(ctx, natsevents.StreamName, jetstream.ConsumerConfig{
		Durable:       "tp-reader",
		FilterSubject: natsevents.SubjectUserCreated,
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	if err != nil {
		t.Fatalf("create consumer: %v", err)
	}
	msg, err := cons.Next(jetstream.FetchMaxWait(5 * time.Second))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	defer msg.Ack()

	traceparent := msg.Headers().Get("traceparent")
	if traceparent == "" {
		t.Fatalf("missing traceparent header; consumer can't re-parent its span")
	}
	if !strings.Contains(traceparent, wantTraceID) {
		t.Fatalf("traceparent = %q, expected to contain trace_id %q", traceparent, wantTraceID)
	}

	// Round-trip: extract via the same propagator, verify span context.
	extracted := otel.GetTextMapPropagator().Extract(context.Background(),
		natsevents.HeaderCarrier(msg.Headers()))
	sc := trace.SpanContextFromContext(extracted)
	if !sc.IsValid() {
		t.Fatal("extracted span context not valid")
	}
	if sc.TraceID().String() != wantTraceID {
		t.Fatalf("extracted trace_id = %s, want %s", sc.TraceID(), wantTraceID)
	}
}

// TestPublisherDetachesContext: a cancelled parent ctx must NOT
// abort the publish (best-effort post-commit contract).
func TestPublisherDetachesContext(t *testing.T) {
	_, url := startEmbeddedJetStream(t)
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	ctx, cancel := context.WithCancel(context.Background())
	pub, err := natsevents.NewPublisher(ctx, url, logger)
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	t.Cleanup(func() { _ = pub.Close() })

	cancel() // simulate client disconnect AFTER repo.Add committed
	if err := pub.PublishUserCreated(ctx, model.User{ID: "u-3", Email: "x@y.com", Name: "X"}); err != nil {
		t.Fatalf("publish on cancelled ctx must succeed (detached publish): %v", err)
	}
}
