-- 000002: notifications table for the user.* event consumer
-- (cmd/consumer). Each user.created event the consumer ACKs lands as
-- one row here.
--
-- The UNIQUE on event_id is what makes the consumer idempotent under
-- JetStream redelivery: paired with INSERT ... ON CONFLICT DO NOTHING
-- in db/queries/notifications.sql, a re-delivered message becomes a
-- no-op INSERT.

CREATE TABLE IF NOT EXISTS notifications (
    id          BIGSERIAL    PRIMARY KEY,
    event_id    text         NOT NULL,
    user_id     text         NOT NULL,
    kind        text         NOT NULL,
    created_at  timestamptz  NOT NULL DEFAULT now(),
    UNIQUE (event_id)
);

CREATE INDEX IF NOT EXISTS notifications_user_id_idx
    ON notifications (user_id);
