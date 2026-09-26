-- +goose Up

CREATE TABLE labels (
    uuid         TEXT        NOT NULL PRIMARY KEY,
    project_uuid TEXT        NOT NULL REFERENCES projects (uuid) ON DELETE CASCADE,
    name         TEXT        NOT NULL,
    color        TEXT        NOT NULL DEFAULT 'slate',
    created_at   TIMESTAMPTZ NOT NULL,
    CONSTRAINT labels_name_check  CHECK (length(name) > 0),
    CONSTRAINT labels_color_check CHECK (color IN ('slate', 'blue', 'teal', 'green', 'amber', 'rose', 'violet'))
);

CREATE UNIQUE INDEX labels_project_name_key ON labels (project_uuid, name);

CREATE TABLE card_labels (
    card_uuid  TEXT NOT NULL REFERENCES cards (uuid) ON DELETE CASCADE,
    label_uuid TEXT NOT NULL REFERENCES labels (uuid) ON DELETE CASCADE,
    PRIMARY KEY (card_uuid, label_uuid)
);

CREATE INDEX card_labels_label_uuid_idx ON card_labels (label_uuid);

-- Soft-deleted: a removed comment leaves a tombstone so a thread does not
-- silently lose its shape.
CREATE TABLE comments (
    uuid        TEXT        NOT NULL PRIMARY KEY,
    card_uuid   TEXT        NOT NULL REFERENCES cards (uuid) ON DELETE CASCADE,
    author_uuid TEXT        NOT NULL REFERENCES users (uuid) ON DELETE RESTRICT,
    body        TEXT        NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL,
    deleted_at  TIMESTAMPTZ,
    CONSTRAINT comments_body_check CHECK (length(body) > 0)
);

CREATE INDEX comments_card_created_idx ON comments (card_uuid, created_at);

-- stored_name is generated; filename is what the uploader called it and is only
-- ever echoed back in a Content-Disposition header.
CREATE TABLE attachments (
    uuid        TEXT        NOT NULL PRIMARY KEY,
    card_uuid   TEXT        NOT NULL REFERENCES cards (uuid) ON DELETE CASCADE,
    uploaded_by TEXT        NOT NULL REFERENCES users (uuid) ON DELETE RESTRICT,
    filename    TEXT        NOT NULL,
    stored_name TEXT        NOT NULL,
    mime_type   TEXT        NOT NULL,
    size_bytes  INTEGER     NOT NULL,
    created_at  TIMESTAMPTZ NOT NULL,
    CONSTRAINT attachments_filename_check CHECK (length(filename) > 0),
    CONSTRAINT attachments_size_check     CHECK (size_bytes >= 0)
);

CREATE UNIQUE INDEX attachments_stored_name_key ON attachments (stored_name);
CREATE INDEX attachments_card_uuid_idx ON attachments (card_uuid);

-- +goose Down
DROP INDEX attachments_card_uuid_idx;
DROP INDEX attachments_stored_name_key;
DROP TABLE attachments;
DROP INDEX comments_card_created_idx;
DROP TABLE comments;
DROP INDEX card_labels_label_uuid_idx;
DROP TABLE card_labels;
DROP INDEX labels_project_name_key;
DROP TABLE labels;
