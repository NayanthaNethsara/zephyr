-- +goose Up
CREATE TABLE idempotency_keys (
    idempotency_key  TEXT PRIMARY KEY,
    fingerprint      TEXT NOT NULL,
    state            TEXT NOT NULL,
    message_id       TEXT NOT NULL DEFAULT '',
    lease_expires_at INTEGER NOT NULL,
    expires_at       INTEGER NOT NULL,
    created_at       INTEGER NOT NULL
);

CREATE INDEX idx_idempotency_keys_expires_at ON idempotency_keys (expires_at);

-- +goose Down
DROP TABLE idempotency_keys;
