-- 000001_init.up.sql
--
-- Initial Day-5 schema. Modelled on internal/model.User plus a tiny
-- registration_log audit table that exists primarily so Repository.Add
-- can demonstrate a real two-statement transaction.

CREATE TABLE users (
    id            text        PRIMARY KEY,
    name          text        NOT NULL,
    email         text        NOT NULL,
    password_hash text        NOT NULL DEFAULT '',
    created_at    timestamptz NOT NULL DEFAULT now()
);

-- Case-insensitive unique email. Closes the dedup gap noted in the
-- README's edge-case list: "Email keys not normalised by Service.AddUser".
CREATE UNIQUE INDEX users_email_lower_idx ON users ((lower(email)));

CREATE TABLE registration_log (
    id         bigserial   PRIMARY KEY,
    user_id    text        NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    event      text        NOT NULL,            -- 'register' | 'add' | …
    created_at timestamptz NOT NULL DEFAULT now()
);

CREATE INDEX registration_log_user_id_idx ON registration_log (user_id);
