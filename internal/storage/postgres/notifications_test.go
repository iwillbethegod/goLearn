package postgres_test

// Day-6 integration test: notifications table + idempotent INSERT.
//
// The cmd/consumer binary issues `pgdb.InsertNotification` for every
// JetStream-acked user.created event. Under redelivery (Nak,
// AckWait expiry, consumer crash) the same event_id reappears.
// UNIQUE(event_id) + ON CONFLICT DO NOTHING is what makes the row
// insert a silent no-op the second time.
//
// This test boots a real Postgres via testcontainers, applies both
// migrations, and asserts:
//
//   1. First InsertNotification → 1 row.
//   2. Second InsertNotification with the SAME event_id → still 1 row.
//   3. Different event_id → 2 rows.
//
// Skipped under -short (Docker required).

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/testcontainers/testcontainers-go"
	tcpostgres "github.com/testcontainers/testcontainers-go/modules/postgres"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/ashishsinghbhadoria/goLearn/internal/storage/postgres"
	"github.com/ashishsinghbhadoria/goLearn/internal/storage/postgres/pgdb"
)

func newNotifPool(t *testing.T) *pgdb.Queries {
	t.Helper()
	if testing.Short() {
		t.Skip("postgres integration test (requires Docker) — skipped in -short mode")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	t.Cleanup(cancel)

	ctr, err := tcpostgres.Run(ctx, "postgres:16-alpine",
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
		t.Fatalf("start container: %v", err)
	}
	t.Cleanup(func() { _ = ctr.Terminate(context.Background()) })

	dsn, err := ctr.ConnectionString(ctx, "sslmode=disable")
	if err != nil {
		t.Fatalf("connection string: %v", err)
	}
	abs, err := filepath.Abs("../../../db/migrations")
	if err != nil {
		t.Fatalf("abs path: %v", err)
	}
	if err := postgres.Migrate("file://"+abs, dsn); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("pgxpool: %v", err)
	}
	t.Cleanup(pool.Close)
	return pgdb.New(pool)
}

func TestNotifications_RedeliveryIsIdempotent(t *testing.T) {
	q := newNotifPool(t)
	ctx := context.Background()

	const eventID = "11111111111111111111111111111111"
	const userID = "u-001"

	if err := q.InsertNotification(ctx, pgdb.InsertNotificationParams{
		EventID: eventID,
		UserID:  userID,
		Kind:    "welcome",
	}); err != nil {
		t.Fatalf("first InsertNotification: %v", err)
	}
	got, err := q.CountNotifications(ctx)
	if err != nil {
		t.Fatalf("CountNotifications: %v", err)
	}
	if got != 1 {
		t.Fatalf("after first insert: count=%d, want 1", got)
	}

	// Same event_id again — the consumer's redelivery path. ON
	// CONFLICT DO NOTHING must keep the count at 1.
	if err := q.InsertNotification(ctx, pgdb.InsertNotificationParams{
		EventID: eventID,
		UserID:  userID,
		Kind:    "welcome",
	}); err != nil {
		t.Fatalf("second InsertNotification: %v", err)
	}
	got, _ = q.CountNotifications(ctx)
	if got != 1 {
		t.Fatalf("after redelivery: count=%d, want 1 (idempotency violated)", got)
	}

	// New event_id → new row, total now 2.
	if err := q.InsertNotification(ctx, pgdb.InsertNotificationParams{
		EventID: "22222222222222222222222222222222",
		UserID:  userID,
		Kind:    "welcome",
	}); err != nil {
		t.Fatalf("third InsertNotification: %v", err)
	}
	got, _ = q.CountNotifications(ctx)
	if got != 2 {
		t.Fatalf("after distinct insert: count=%d, want 2", got)
	}
}

func TestNotifications_LookupByEventID(t *testing.T) {
	q := newNotifPool(t)
	ctx := context.Background()

	const eventID = "abcdef0123456789abcdef0123456789"
	if err := q.InsertNotification(ctx, pgdb.InsertNotificationParams{
		EventID: eventID,
		UserID:  "u-002",
		Kind:    "welcome",
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	row, err := q.GetNotificationByEventID(ctx, eventID)
	if err != nil {
		t.Fatalf("GetNotificationByEventID: %v", err)
	}
	if row.UserID != "u-002" || row.Kind != "welcome" {
		t.Fatalf("row mismatch: %+v", row)
	}
}
