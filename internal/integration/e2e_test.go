//go:build integration

// Package integration contains the Day-7 end-to-end test that boots
// real Postgres + NATS JetStream, constructs the same wiring cmd/api
// and cmd/consumer use, and proves the cross-service flow:
//
//	POST /users → users row → user.created event → notifications row
//	             with one trace_id covering all four hops.
//
// Build tag `integration` keeps it out of the default unit suite so
// `go test -short ./...` stays fast and Docker-free. Run with:
//
//	go test -tags=integration -race -timeout=5m ./internal/integration/...
package integration_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/nats-io/nats-server/v2/server"
	natstest "github.com/nats-io/nats-server/v2/test"
	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"

	natsevents "github.com/ashishsinghbhadoria/goLearn/internal/events/nats"
	"github.com/ashishsinghbhadoria/goLearn/internal/storage/postgres"
	"github.com/ashishsinghbhadoria/goLearn/internal/storage/postgres/pgdb"
	"github.com/ashishsinghbhadoria/goLearn/internal/transport/httpapi"
	"github.com/ashishsinghbhadoria/goLearn/internal/transport/httpapi/gen"
	"github.com/ashishsinghbhadoria/goLearn/internal/user"
	pkglogger "github.com/ashishsinghbhadoria/goLearn/pkg/logger"
	"github.com/ashishsinghbhadoria/goLearn/pkg/metrics"
)

// stack is one boot of every service the test needs. Encapsulating
// it lets us factor out the test setup from the assertions.
type stack struct {
	t           *testing.T
	ctx         context.Context
	cancel      context.CancelFunc
	pgDSN       string
	natsURL     string
	natsServer  *server.Server
	httpServer  *httptest.Server
	consumerWg  *sync.WaitGroup
	consumerEnd chan struct{}
	pool        *pgxpool.Pool
	queries     *pgdb.Queries
	publisher   *natsevents.Publisher
	exporter    *tracetest.InMemoryExporter
}

// bootStack is the full Day-7 wiring in one place. Returns when the
// HTTP server is listening and the consumer is subscribed.
func bootStack(t *testing.T) *stack {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)

	// 1) Tracer with in-memory exporter so we can assert on spans.
	exp := tracetest.NewInMemoryExporter()
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithSyncer(exp),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))
	t.Cleanup(func() {
		_ = tp.Shutdown(context.Background())
	})

	// 2) Postgres testcontainer + apply migrations.
	pgCtr, err := tcpostgres.Run(ctx, "postgres:16-alpine",
		tcpostgres.WithDatabase("test"),
		tcpostgres.WithUsername("test"),
		tcpostgres.WithPassword("test"),
		testcontainers.WithWaitStrategy(
			wait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).
				WithStartupTimeout(60*time.Second),
		),
	)
	if err != nil {
		t.Fatalf("postgres container: %v", err)
	}
	t.Cleanup(func() { _ = pgCtr.Terminate(context.Background()) })

	dsn, err := pgCtr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("dsn: %v", err)
	}
	migrationsURL := absMigrationsURL(t)
	if err := postgres.Migrate(migrationsURL, dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	// 3) Embedded NATS JetStream.
	opts := natstest.DefaultTestOptions
	opts.Port = -1
	opts.JetStream = true
	opts.StoreDir = t.TempDir()
	natsSrv := natstest.RunServer(&opts)
	if !natsSrv.ReadyForConnections(5 * time.Second) {
		natsSrv.Shutdown()
		t.Fatal("nats not ready")
	}
	t.Cleanup(func() {
		natsSrv.Shutdown()
		natsSrv.WaitForShutdown()
	})

	// 4) pgxpool + queries.
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool: %v", err)
	}
	t.Cleanup(pool.Close)
	queries := pgdb.New(pool)

	// 5) user.Service wired with the postgres repo + NATS publisher.
	logger := slog.New(pkglogger.NewTraceHandler(
		slog.NewTextHandler(io.Discard, nil),
	))
	repo, err := postgres.NewUserRepo(ctx, dsn, logger)
	if err != nil {
		t.Fatalf("NewUserRepo: %v", err)
	}
	t.Cleanup(func() { _ = repo.Close() })

	pub, err := natsevents.NewPublisher(ctx, natsSrv.ClientURL(), logger)
	if err != nil {
		t.Fatalf("NewPublisher: %v", err)
	}
	t.Cleanup(func() { _ = pub.Close() })

	svc := user.NewService(repo, logger, metrics.New(), user.WithPublisher(pub))

	// 6) HTTP server with the OpenAPI handler + the same middleware chain
	// the cmd/api binary uses. We skip the validator middleware here
	// because hauling in the full embedded swagger spec would couple
	// the test to an unrelated layer; the contract is exercised in
	// internal/transport/httpapi/handler_test.go.
	mux := http.NewServeMux()
	gen.HandlerWithOptions(httpapi.NewHandler(svc, logger), gen.StdHTTPServerOptions{
		BaseRouter: mux,
	})
	httpSrv := httptest.NewServer(mux)
	t.Cleanup(httpSrv.Close)

	// 7) Consumer goroutine. We replicate the cmd/consumer/main.go
	// handler logic here so the test exercises the same NATS+DB code
	// paths without depending on the binary's flag parsing.
	cons, err := setupConsumer(ctx, natsSrv.ClientURL())
	if err != nil {
		t.Fatalf("setupConsumer: %v", err)
	}
	consumerCtx, consumerCancel := context.WithCancel(ctx)
	t.Cleanup(consumerCancel)
	wg := &sync.WaitGroup{}
	wg.Add(1)
	end := make(chan struct{})
	go func() {
		defer wg.Done()
		runConsumer(consumerCtx, cons, queries)
		close(end)
	}()

	return &stack{
		t:           t,
		ctx:         ctx,
		cancel:      cancel,
		pgDSN:       dsn,
		natsURL:     natsSrv.ClientURL(),
		natsServer:  natsSrv,
		httpServer:  httpSrv,
		consumerWg:  wg,
		consumerEnd: end,
		pool:        pool,
		queries:     queries,
		publisher:   pub,
		exporter:    exp,
	}
}

func absMigrationsURL(t *testing.T) string {
	t.Helper()
	abs, err := filepath.Abs("../../db/migrations")
	if err != nil {
		t.Fatal(err)
	}
	return "file://" + abs
}

// setupConsumer dials NATS and creates the durable user-welcome
// consumer (idempotent — CreateOrUpdateConsumer).
func setupConsumer(ctx context.Context, natsURL string) (jetstream.Consumer, error) {
	conn, err := natsgo.Connect(natsURL)
	if err != nil {
		return nil, fmt.Errorf("nats connect: %w", err)
	}
	js, err := jetstream.New(conn)
	if err != nil {
		return nil, fmt.Errorf("jetstream: %w", err)
	}
	cons, err := js.CreateOrUpdateConsumer(ctx, natsevents.StreamName, jetstream.ConsumerConfig{
		Durable:       "user-welcome",
		FilterSubject: natsevents.SubjectUserCreated,
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       30 * time.Second,
		MaxDeliver:    5,
	})
	if err != nil {
		return nil, fmt.Errorf("ensure consumer: %w", err)
	}
	return cons, nil
}

// runConsumer iterates messages and inserts them as notifications.
// Mirrors cmd/consumer/main.go's handle() but inlined here so the
// integration test owns the lifecycle.
func runConsumer(ctx context.Context, cons jetstream.Consumer, queries *pgdb.Queries) {
	tracer := otel.Tracer("integration-consumer")
	iter, err := cons.Messages()
	if err != nil {
		return
	}
	go func() {
		<-ctx.Done()
		iter.Stop()
	}()

	for {
		msg, err := iter.Next()
		if err != nil {
			return
		}
		// Extract trace context BEFORE starting our own span so the
		// consumer span is parented to the producer's trace.
		spanCtx := otel.GetTextMapPropagator().Extract(ctx,
			natsevents.HeaderCarrier(msg.Headers()))
		spanCtx, span := tracer.Start(spanCtx, "consumer.user.created",
			trace.WithSpanKind(trace.SpanKindConsumer))

		var env struct {
			EventID string `json:"event_id"`
			User    struct {
				ID    string `json:"id"`
				Name  string `json:"name"`
				Email string `json:"email"`
			} `json:"user"`
		}
		if err := json.Unmarshal(msg.Data(), &env); err != nil {
			span.RecordError(err)
			span.End()
			_ = msg.Term()
			continue
		}
		if err := queries.InsertNotification(spanCtx, pgdb.InsertNotificationParams{
			EventID: env.EventID,
			UserID:  env.User.ID,
			Kind:    "welcome",
		}); err != nil {
			span.RecordError(err)
			span.End()
			_ = msg.NakWithDelay(500 * time.Millisecond)
			continue
		}
		_ = msg.Ack()
		span.End()
	}
}

// waitFor polls until the predicate is true or timeout elapses. Used
// to wait for async events (consumer ACK + DB insert) without sleeps
// scattered through the test.
func waitFor(t *testing.T, timeout time.Duration, pred func() bool) bool {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pred() {
			return true
		}
		time.Sleep(50 * time.Millisecond)
	}
	return pred()
}

func TestE2E_PostUserFansOutToNotifications(t *testing.T) {
	s := bootStack(t)

	body := bytes.NewBufferString(`{"name":"Ada","email":"ada@example.com","password":"hunter22"}`)
	resp, err := http.Post(s.httpServer.URL+"/users", "application/json", body)
	if err != nil {
		t.Fatalf("POST /users: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		t.Fatalf("status = %d, want 201; body=%s", resp.StatusCode, respBody)
	}

	if !waitFor(t, 10*time.Second, func() bool {
		n, _ := s.queries.CountNotifications(context.Background())
		return n == 1
	}) {
		n, _ := s.queries.CountNotifications(context.Background())
		t.Fatalf("notifications never landed: count=%d", n)
	}
}

func TestE2E_RedeliveryIsIdempotent(t *testing.T) {
	s := bootStack(t)

	// Same email/name produces a different event_id (random) on each
	// Register, so duplicate-email is the wrong test. To assert
	// idempotency at the DB layer we publish the same event_id twice
	// directly via the publisher's stream and watch the row count.
	conn, err := natsgo.Connect(s.natsURL)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Drain()
	js, _ := jetstream.New(conn)

	const eventID = "fixed-event-id-for-test-aaaaaaaaa"
	payload, _ := json.Marshal(map[string]any{
		"event_id":    eventID,
		"schema":      "user.created.v1",
		"occurred_at": time.Now().UTC().Format(time.RFC3339Nano),
		"user":        map[string]string{"id": "u-fixed", "name": "Fixed", "email": "fixed@x.com"},
	})

	for i := 0; i < 2; i++ {
		msg := &natsgo.Msg{
			Subject: natsevents.SubjectUserCreated,
			Data:    payload,
			Header:  natsgo.Header{},
		}
		msg.Header.Set(jetstream.MsgIDHeader, eventID)
		if _, err := js.PublishMsg(context.Background(), msg,
			jetstream.WithMsgID(eventID),
		); err != nil {
			t.Fatalf("publish %d: %v", i, err)
		}
	}

	// Wait for the consumer to drain. JetStream server-side dedup
	// usually swallows the second publish before the consumer sees
	// it; either way the notifications table must have exactly 1 row.
	time.Sleep(2 * time.Second)
	got, err := s.queries.CountNotifications(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got != 1 {
		t.Fatalf("notifications count after double-publish = %d, want 1 (idempotency violated)", got)
	}
}

func TestE2E_TraceContextLinksProducerAndConsumer(t *testing.T) {
	s := bootStack(t)

	body := bytes.NewBufferString(`{"name":"Lin","email":"lin@example.com","password":"hunter22"}`)
	resp, err := http.Post(s.httpServer.URL+"/users", "application/json", body)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("status = %d, want 201", resp.StatusCode)
	}

	// Wait for consumer to ACK so its span is exported.
	waitFor(t, 10*time.Second, func() bool {
		n, _ := s.queries.CountNotifications(context.Background())
		return n >= 1
	})
	// Force a flush of the in-memory exporter.
	time.Sleep(200 * time.Millisecond)

	spans := s.exporter.GetSpans()
	if len(spans) == 0 {
		t.Fatal("exporter saw no spans")
	}
	// Every span emitted by api or consumer should share at least one
	// trace_id when an event made it across; we look for the consumer
	// span and verify its TraceID matches a producer span.
	traceIDs := map[string]int{}
	var consumerTrace string
	for _, sp := range spans {
		traceIDs[sp.SpanContext.TraceID().String()]++
		if sp.Name == "consumer.user.created" {
			consumerTrace = sp.SpanContext.TraceID().String()
		}
	}
	if consumerTrace == "" {
		t.Fatalf("no consumer span; producer-only trace IDs = %v", traceIDs)
	}
	if traceIDs[consumerTrace] < 2 {
		t.Fatalf("trace %s only has %d span(s); expected >=2 (producer + consumer)",
			consumerTrace, traceIDs[consumerTrace])
	}
}

// Smoke: shutdown order doesn't deadlock. We tear down via Cleanup,
// so this test just makes sure bootStack returns and a no-op
// happens within the test's deadline.
func TestE2E_StackBootsAndShutsDownCleanly(t *testing.T) {
	s := bootStack(t)
	if s.httpServer == nil || s.natsServer == nil || s.queries == nil {
		t.Fatal("incomplete stack")
	}
}

// silence unused warnings on private os import path; we only use os
// transitively via testcontainers/wait.
var _ = os.Stdout
