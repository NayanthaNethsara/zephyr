-- +goose Up
CREATE TABLE sessions (
    phone_hash       TEXT NOT NULL,
    phone_number_id  TEXT NOT NULL,
    last_inbound_at  INTEGER NOT NULL,
    PRIMARY KEY (phone_hash, phone_number_id)
);

CREATE INDEX idx_sessions_last_inbound_at ON sessions (last_inbound_at);

-- +goose Down
DROP TABLE sessions;
