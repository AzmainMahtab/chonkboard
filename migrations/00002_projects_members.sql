-- +goose Up

CREATE TABLE projects (
    uuid        TEXT        NOT NULL PRIMARY KEY,
    slug        TEXT        NOT NULL,
    name        TEXT        NOT NULL,
    description TEXT        NOT NULL DEFAULT '',
    created_by  TEXT        NOT NULL REFERENCES users (uuid) ON DELETE RESTRICT,
    created_at  TIMESTAMPTZ NOT NULL,
    updated_at  TIMESTAMPTZ NOT NULL,
    archived_at TIMESTAMPTZ,
    CONSTRAINT projects_slug_check CHECK (slug = lower(slug) AND length(slug) BETWEEN 1 AND 40),
    CONSTRAINT projects_name_check CHECK (length(name) > 0)
);

CREATE UNIQUE INDEX projects_slug_key ON projects (slug);
CREATE INDEX projects_archived_at_idx ON projects (archived_at);

-- The grant table. A row here is the whole of "this person can see this
-- project"; absence is denial, so there is no need for a negative grant.
CREATE TABLE project_members (
    project_uuid TEXT        NOT NULL REFERENCES projects (uuid) ON DELETE CASCADE,
    user_uuid    TEXT        NOT NULL REFERENCES users (uuid) ON DELETE CASCADE,
    role         TEXT        NOT NULL,
    granted_by   TEXT        NOT NULL REFERENCES users (uuid) ON DELETE RESTRICT,
    granted_at   TIMESTAMPTZ NOT NULL,
    PRIMARY KEY (project_uuid, user_uuid),
    CONSTRAINT project_members_role_check CHECK (role IN ('manager', 'member'))
);

CREATE INDEX project_members_user_uuid_idx ON project_members (user_uuid);

-- +goose Down
DROP INDEX project_members_user_uuid_idx;
DROP TABLE project_members;
DROP INDEX projects_archived_at_idx;
DROP INDEX projects_slug_key;
DROP TABLE projects;
