-- +goose Up
CREATE TABLE send_jobs (
    id               TEXT PRIMARY KEY,
    state            TEXT NOT NULL,
    payload          BLOB NOT NULL,
    attempts         INTEGER NOT NULL DEFAULT 0,
    next_attempt_at  INTEGER NOT NULL,
    message_id       TEXT NOT NULL DEFAULT '',
    error_code       TEXT NOT NULL DEFAULT '',
    error_message    TEXT NOT NULL DEFAULT '',
    is_retryable     INTEGER NOT NULL DEFAULT 0,
    meta_code        INTEGER NOT NULL DEFAULT 0,
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL
);

CREATE INDEX idx_send_jobs_due ON send_jobs (state, next_attempt_at);
CREATE INDEX idx_send_jobs_updated_at ON send_jobs (updated_at);

-- +goose Down
DROP TABLE send_jobs;
