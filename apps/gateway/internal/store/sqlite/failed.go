package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/store"
)

func (s *Store) ListFailed(ctx context.Context, limit, offset int) ([]store.FailedEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, topic, attempts, last_error, created_at, updated_at
		FROM outbox_entries
		WHERE state = ?
		ORDER BY updated_at DESC, id
		LIMIT ? OFFSET ?`,
		store.StateFailed, limit, offset,
	)
	if err != nil {
		return nil, fmt.Errorf("list failed outbox entries: %w", err)
	}
	defer func() { _ = rows.Close() }()

	entries := []store.FailedEntry{}
	for rows.Next() {
		var (
			entry     store.FailedEntry
			createdAt int64
			failedAt  int64
		)
		if err := rows.Scan(&entry.ID, &entry.Topic, &entry.Attempts, &entry.LastError, &createdAt, &failedAt); err != nil {
			return nil, fmt.Errorf("scan failed outbox entry: %w", err)
		}
		entry.CreatedAt = fromMillis(createdAt)
		entry.FailedAt = fromMillis(failedAt)
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (s *Store) Requeue(ctx context.Context, id string, at time.Time) (bool, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE outbox_entries
		SET state = ?, attempts = 0, next_attempt_at = ?, updated_at = ?
		WHERE id = ? AND state = ?`,
		store.StatePending, toMillis(at), toMillis(at), id, store.StateFailed,
	)
	if err != nil {
		return false, fmt.Errorf("requeue failed outbox entry: %w", err)
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return false, fmt.Errorf("read requeue result: %w", err)
	}
	return changed > 0, nil
}
