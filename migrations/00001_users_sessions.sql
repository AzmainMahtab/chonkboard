-- +goose Up

-- Email is stored already lower-cased and a CHECK enforces it. SQLite's
-- COLLATE NOCASE and PostgreSQL's CITEXT are both dialect-specific, so
-- normalising on the way in is the only portable way to get case-insensitive
-- uniqueness out of a plain UNIQUE index.
CREATE TABLE users (
    uuid                 TEXT        NOT NULL PRIMARY KEY,
    email                TEXT        NOT NULL,
    display_name         TEXT        NOT NULL,
    password_hash        TEXT        NOT NULL,
    role                 TEXT        NOT NULL,
    status               TEXT        NOT NULL DEFAULT 'active',
    must_change_password BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at           TIMESTAMPTZ NOT NULL,
    updated_at           TIMESTAMPTZ NOT NULL,
    CONSTRAINT users_role_check   CHECK (role IN ('super_admin', 'member')),
    CONSTRAINT users_status_check CHECK (status IN ('active', 'suspended')),
    CONSTRAINT users_email_check  CHECK (email = lower(email) AND length(email) > 0),
    CONSTRAINT users_name_check   CHECK (length(display_name) > 0)
);

CREATE UNIQUE INDEX users_email_key ON users (email);

-- Only the SHA-256 of the cookie value is stored, so a dump of this table does
-- not hand over live sessions.
CREATE TABLE sessions (
    uuid         TEXT        NOT NULL PRIMARY KEY,
    user_uuid    TEXT        NOT NULL REFERENCES users (uuid) ON DELETE CASCADE,
    token_hash   TEXT        NOT NULL,
    csrf_token   TEXT        NOT NULL,
    ip           TEXT        NOT NULL DEFAULT '',
    user_agent   TEXT        NOT NULL DEFAULT '',
    created_at   TIMESTAMPTZ NOT NULL,
    last_used_at TIMESTAMPTZ NOT NULL,
    expires_at   TIMESTAMPTZ NOT NULL,
    revoked_at   TIMESTAMPTZ
);

CREATE UNIQUE INDEX sessions_token_hash_key ON sessions (token_hash);
CREATE INDEX sessions_user_uuid_idx  ON sessions (user_uuid);
CREATE INDEX sessions_expires_at_idx ON sessions (expires_at);

-- +goose Down
DROP INDEX sessions_expires_at_idx;
DROP INDEX sessions_user_uuid_idx;
DROP INDEX sessions_token_hash_key;
DROP TABLE sessions;
DROP INDEX users_email_key;
DROP TABLE users;
