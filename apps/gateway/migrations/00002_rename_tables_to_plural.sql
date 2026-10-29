-- +goose Up
ALTER TABLE dedup RENAME TO dedup_keys;
ALTER TABLE outbox RENAME TO outbox_entries;

-- +goose Down
ALTER TABLE outbox_entries RENAME TO outbox;
ALTER TABLE dedup_keys RENAME TO dedup;
