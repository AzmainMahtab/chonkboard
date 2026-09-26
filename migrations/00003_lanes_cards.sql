-- +goose Up

CREATE TABLE lanes (
    uuid         TEXT        NOT NULL PRIMARY KEY,
    project_uuid TEXT        NOT NULL REFERENCES projects (uuid) ON DELETE CASCADE,
    name         TEXT        NOT NULL,
    position     INTEGER     NOT NULL,
    color        TEXT        NOT NULL DEFAULT 'slate',
    wip_limit    INTEGER,
    is_done      BOOLEAN     NOT NULL DEFAULT FALSE,
    created_at   TIMESTAMPTZ NOT NULL,
    updated_at   TIMESTAMPTZ NOT NULL,
    CONSTRAINT lanes_name_check      CHECK (length(name) > 0),
    CONSTRAINT lanes_position_check  CHECK (position >= 0),
    CONSTRAINT lanes_wip_limit_check CHECK (wip_limit IS NULL OR wip_limit > 0),
    CONSTRAINT lanes_color_check     CHECK (color IN ('slate', 'blue', 'teal', 'green', 'amber', 'rose', 'violet'))
);

-- Deliberately NOT unique. A reorder rewrites positions in a loop, and a unique
-- constraint would fire on an intermediate state partway through the
-- transaction.
CREATE INDEX lanes_project_position_idx ON lanes (project_uuid, position);

-- project_uuid is denormalised from the lane so that every authorisation check
-- ("does this card belong to a project this person was granted?") is one lookup
-- instead of a join through lanes.
CREATE TABLE cards (
    uuid          TEXT        NOT NULL PRIMARY KEY,
    project_uuid  TEXT        NOT NULL REFERENCES projects (uuid) ON DELETE CASCADE,
    lane_uuid     TEXT        NOT NULL REFERENCES lanes (uuid) ON DELETE RESTRICT,
    title         TEXT        NOT NULL,
    description   TEXT        NOT NULL DEFAULT '',
    position      INTEGER     NOT NULL,
    priority      TEXT        NOT NULL DEFAULT 'none',
    assignee_uuid TEXT        REFERENCES users (uuid) ON DELETE SET NULL,
    due_at        TIMESTAMPTZ,
    created_by    TEXT        NOT NULL REFERENCES users (uuid) ON DELETE RESTRICT,
    created_at    TIMESTAMPTZ NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL,
    archived_at   TIMESTAMPTZ,
    CONSTRAINT cards_title_check    CHECK (length(title) > 0),
    CONSTRAINT cards_position_check CHECK (position >= 0),
    CONSTRAINT cards_priority_check CHECK (priority IN ('none', 'low', 'normal', 'high', 'urgent'))
);

CREATE INDEX cards_lane_position_idx ON cards (lane_uuid, position);
CREATE INDEX cards_assignee_uuid_idx ON cards (assignee_uuid);
CREATE INDEX cards_project_live_idx  ON cards (project_uuid) WHERE archived_at IS NULL;

-- +goose Down
DROP INDEX cards_project_live_idx;
DROP INDEX cards_assignee_uuid_idx;
DROP INDEX cards_lane_position_idx;
DROP TABLE cards;
DROP INDEX lanes_project_position_idx;
DROP TABLE lanes;
