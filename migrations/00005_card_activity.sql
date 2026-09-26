-- +goose Up

-- meta is a JSON document held as TEXT. PostgreSQL would rather have JSONB, but
-- nothing queries into this column — it is read back whole and rendered — so the
-- portable type wins. Switching it to JSONB later is one ALTER ... USING.
CREATE TABLE card_activity (
    uuid           TEXT        NOT NULL PRIMARY KEY,
    card_uuid      TEXT        NOT NULL REFERENCES cards (uuid) ON DELETE CASCADE,
    project_uuid   TEXT        NOT NULL REFERENCES projects (uuid) ON DELETE CASCADE,
    actor_uuid     TEXT        NOT NULL REFERENCES users (uuid) ON DELETE RESTRICT,
    kind           TEXT        NOT NULL,
    from_lane_uuid TEXT        REFERENCES lanes (uuid) ON DELETE SET NULL,
    to_lane_uuid   TEXT        REFERENCES lanes (uuid) ON DELETE SET NULL,
    meta           TEXT        NOT NULL DEFAULT '{}',
    created_at     TIMESTAMPTZ NOT NULL,
    CONSTRAINT card_activity_kind_check CHECK (kind IN (
        'created', 'moved', 'updated', 'archived', 'restored',
        'commented', 'attached', 'assigned', 'labelled'
    ))
);

CREATE INDEX card_activity_card_created_idx    ON card_activity (card_uuid, created_at);
CREATE INDEX card_activity_project_created_idx ON card_activity (project_uuid, created_at);

-- +goose Down
DROP INDEX card_activity_project_created_idx;
DROP INDEX card_activity_card_created_idx;
DROP TABLE card_activity;
