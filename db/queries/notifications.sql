-- name: InsertNotification :exec
-- ON CONFLICT DO NOTHING gives idempotency under JetStream
-- redelivery. The UNIQUE(event_id) index on notifications is the
-- enforcement point — a re-ACKed message becomes a silent no-op.
INSERT INTO notifications (event_id, user_id, kind)
VALUES ($1, $2, $3)
ON CONFLICT (event_id) DO NOTHING;

-- name: CountNotifications :one
SELECT COUNT(*) FROM notifications;

-- name: GetNotificationByEventID :one
SELECT id, event_id, user_id, kind, created_at
FROM   notifications
WHERE  event_id = $1;
