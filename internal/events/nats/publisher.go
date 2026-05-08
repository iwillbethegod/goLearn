// Package nats implements user.Publisher on top of NATS JetStream.
// The producer:
//
//   - Connects to a NATS cluster, ensures stream USERS exists with
//     subjects user.> (idempotent — CreateOrUpdateStream).
//   - Marshals events to a versioned JSON envelope and publishes
//     synchronously via JetStream so we get a server-side ack
//     (durable persistence) before returning.
//   - Injects W3C trace context into NATS message headers so the
//     consumer can stitch its spans back to the producer's trace.
//   - Sets Nats-Msg-Id = event_id, which JetStream uses for a
//     server-side dedup window AND consumers use as an idempotency
//     key when they fan out to side effects (e.g. INSERT notifications).
//
// "Detached publish ctx": Service.Register passes the HTTP request
// ctx, which can be cancelled by a client disconnect right after the
// DB row commits. Using that ctx for js.PublishMsg would lose the
// event with an opaque "context canceled" error. The publisher
// derives a fresh ctx via context.WithoutCancel + a 2 s timeout, but
// CARRIES the SpanContext forward via trace.ContextWithSpanContext so
// the published span stays attached to the same trace.
package nats

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"time"

	natsgo "github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"

	"github.com/ashishsinghbhadoria/goLearn/internal/model"
)

// Subject names. Reserve user.> so future events (user.deleted,
// user.updated) ride the same stream without a new config.
const (
	StreamName              = "USERS"
	StreamSubjects          = "user.>"
	SubjectUserCreated      = "user.created"
	SchemaUserCreatedV1     = "user.created.v1"
	publishTimeout          = 2 * time.Second
	streamMaxAge            = 24 * time.Hour
	streamMaxBytes    int64 = 1 << 30 // 1 GiB
)

// Publisher implements user.Publisher.
type Publisher struct {
	conn   *natsgo.Conn
	js     jetstream.JetStream
	logger *slog.Logger
}

// NewPublisher dials the NATS cluster, ensures the USERS stream
// exists, and returns a connected publisher. Caller MUST defer Close
// to drain in-flight publishes.
func NewPublisher(ctx context.Context, url string, logger *slog.Logger) (*Publisher, error) {
	if url == "" {
		return nil, fmt.Errorf("nats: empty url")
	}
	conn, err := natsgo.Connect(url,
		natsgo.Name("goLearn-publisher"),
		natsgo.Timeout(5*time.Second),
		natsgo.MaxReconnects(-1),
		natsgo.ReconnectWait(2*time.Second),
	)
	if err != nil {
		return nil, fmt.Errorf("nats: dial %s: %w", url, err)
	}
	js, err := jetstream.New(conn)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("nats: jetstream init: %w", err)
	}
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      StreamName,
		Subjects:  []string{StreamSubjects},
		Retention: jetstream.LimitsPolicy,
		Storage:   jetstream.FileStorage,
		MaxAge:    streamMaxAge,
		MaxBytes:  streamMaxBytes,
	}); err != nil {
		conn.Close()
		return nil, fmt.Errorf("nats: ensure stream %s: %w", StreamName, err)
	}
	return &Publisher{conn: conn, js: js, logger: logger}, nil
}

// Close drains the connection so in-flight publishes get acked
// before the underlying TCP closes. Drain implies close — calling
// Close after Drain is a no-op that emits a confusing log line, so
// we don't.
func (p *Publisher) Close() error {
	if p == nil || p.conn == nil {
		return nil
	}
	return p.conn.Drain()
}

// PublishUserCreated emits a user.created event. The DB row has
// already been committed by the time this is called; if the publish
// fails the caller logs but does NOT roll back. The detached ctx
// keeps the publish from being cancelled by a client disconnect.
func (p *Publisher) PublishUserCreated(ctx context.Context, u model.User) error {
	eventID, err := newEventID()
	if err != nil {
		return fmt.Errorf("nats: gen event id: %w", err)
	}

	payload, err := json.Marshal(eventEnvelope{
		EventID:    eventID,
		Schema:     SchemaUserCreatedV1,
		OccurredAt: time.Now().UTC().Format(time.RFC3339Nano),
		User: userPayload{
			ID:    u.ID,
			Name:  u.Name,
			Email: u.Email,
			// PasswordHash deliberately omitted — events must not
			// carry credentials.
		},
	})
	if err != nil {
		return fmt.Errorf("nats: marshal event: %w", err)
	}

	msg := &natsgo.Msg{
		Subject: SubjectUserCreated,
		Data:    payload,
		Header:  natsgo.Header{},
	}
	msg.Header.Set(jetstream.MsgIDHeader, eventID)
	otel.GetTextMapPropagator().Inject(ctx, HeaderCarrier(msg.Header))

	pubCtx, cancel := detachedPublishCtx(ctx, publishTimeout)
	defer cancel()

	ack, err := p.js.PublishMsg(pubCtx, msg)
	if err != nil {
		return fmt.Errorf("nats: publish %s: %w", SubjectUserCreated, err)
	}
	p.logger.InfoContext(ctx, "user.created published",
		"event_id", eventID,
		"user_id", u.ID,
		"stream", ack.Stream,
		"sequence", ack.Sequence,
	)
	return nil
}

// detachedPublishCtx returns a ctx that:
//   - drops parent cancellation (so a client disconnect post-DB-commit
//     doesn't lose the event), and
//   - keeps the SpanContext from parent (so the publish span stays
//     attached to the original trace), and
//   - bounds total publish time to timeout.
func detachedPublishCtx(parent context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	detached := trace.ContextWithSpanContext(
		context.WithoutCancel(parent),
		trace.SpanContextFromContext(parent),
	)
	return context.WithTimeout(detached, timeout)
}

// newEventID returns 16 random bytes hex-encoded (32 chars). Used as
// both the application-level event_id field and the Nats-Msg-Id
// header. Random ⇒ collision-resistant, hex ⇒ safe in headers.
func newEventID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(b[:]), nil
}

// eventEnvelope is the wire format for user.* events. Pin the schema
// field so v2 can ship on a new subject (user.created.v2) without
// breaking existing subscribers.
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
