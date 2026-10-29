package sqlite

import (
	"context"
	"fmt"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/store"
)

const insertDedup = `
INSERT INTO dedup (dedup_key, expires_at) VALUES (?, ?)
ON CONFLICT(dedup_key) DO UPDATE SET expires_at = excluded.expires_at
WHERE dedup.expires_at <= ?`

const insertOutbox = `
INSERT INTO outbox (id, topic, msg_key, payload, attempts, next_attempt_at, state, last_error, created_at, updated_at)
VALUES (?, ?, ?, ?, 0, ?, ?, '', ?, ?)`

func (s *Store) SaveInbound(ctx context.Context, records []store.InboundRecord) ([]string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	saved := make([]string, 0, len(records))
	for _, record := range records {
		entry := record.Entry
		created := toMillis(entry.CreatedAt)

		result, err := tx.ExecContext(ctx, insertDedup, record.DedupKey, toMillis(record.DedupExpiresAt), created)
		if err != nil {
			return nil, fmt.Errorf("insert dedup key: %w", err)
		}
		inserted, err := result.RowsAffected()
		if err != nil {
			return nil, fmt.Errorf("read dedup result: %w", err)
		}
		if inserted == 0 {
			continue
		}

		_, err = tx.ExecContext(ctx, insertOutbox,
			entry.ID, entry.Topic, entry.Key, entry.Payload,
			toMillis(entry.NextAttemptAt), store.StatePending, created, created,
		)
		if err != nil {
			return nil, fmt.Errorf("insert outbox entry: %w", err)
		}
		saved = append(saved, entry.ID)
	}

	if err := tx.Commit(); err != nil {
		return nil, fmt.Errorf("commit transaction: %w", err)
	}
	return saved, nil
}
