-- +goose Up
CREATE TABLE dedup (
    dedup_key  TEXT PRIMARY KEY,
    expires_at INTEGER NOT NULL
);

CREATE INDEX idx_dedup_expires_at ON dedup (expires_at);

CREATE TABLE outbox (
    id              TEXT PRIMARY KEY,
    topic           TEXT NOT NULL,
    msg_key         TEXT NOT NULL,
    payload         BLOB NOT NULL,
    attempts        INTEGER NOT NULL DEFAULT 0,
    next_attempt_at INTEGER NOT NULL,
    state           TEXT NOT NULL DEFAULT 'pending',
    last_error      TEXT NOT NULL DEFAULT '',
    created_at      INTEGER NOT NULL,
    updated_at      INTEGER NOT NULL
);

CREATE INDEX idx_outbox_due ON outbox (state, next_attempt_at);

-- +goose Down
DROP TABLE outbox;
DROP TABLE dedup;
