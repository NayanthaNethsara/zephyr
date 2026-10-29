package sqlite

import (
	"context"
	"fmt"
	"time"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/store"
)

func (s *Store) ListDue(ctx context.Context, now time.Time, limit int) ([]store.OutboxEntry, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, topic, msg_key, payload, attempts, next_attempt_at, created_at, last_error
		FROM outbox_entries
		WHERE state = ? AND next_attempt_at <= ?
		ORDER BY created_at, id
		LIMIT ?`,
		store.StatePending, toMillis(now), limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list due outbox entries: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var entries []store.OutboxEntry
	for rows.Next() {
		var (
			entry         store.OutboxEntry
			nextAttemptAt int64
			createdAt     int64
		)
		if err := rows.Scan(
			&entry.ID, &entry.Topic, &entry.Key, &entry.Payload,
			&entry.Attempts, &nextAttemptAt, &createdAt, &entry.LastError,
		); err != nil {
			return nil, fmt.Errorf("scan outbox entry: %w", err)
		}
		entry.NextAttemptAt = fromMillis(nextAttemptAt)
		entry.CreatedAt = fromMillis(createdAt)
		entries = append(entries, entry)
	}
	return entries, rows.Err()
}

func (s *Store) Delete(ctx context.Context, id string) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM outbox_entries WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete outbox entry: %w", err)
	}
	return nil
}

func (s *Store) Reschedule(ctx context.Context, id string, attempts int, nextAttemptAt time.Time, lastError string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE outbox_entries
		SET attempts = ?, next_attempt_at = ?, last_error = ?, updated_at = ?
		WHERE id = ?`,
		attempts, toMillis(nextAttemptAt), lastError, toMillis(time.Now()), id,
	)
	if err != nil {
		return fmt.Errorf("reschedule outbox entry: %w", err)
	}
	return nil
}

func (s *Store) MarkFailed(ctx context.Context, id string, attempts int, lastError string, at time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE outbox_entries
		SET state = ?, attempts = ?, last_error = ?, updated_at = ?
		WHERE id = ?`,
		store.StateFailed, attempts, lastError, toMillis(at), id,
	)
	if err != nil {
		return fmt.Errorf("mark outbox entry failed: %w", err)
	}
	return nil
}

func (s *Store) DeleteExpired(ctx context.Context, now time.Time, failedBefore time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `DELETE FROM dedup_keys WHERE expires_at <= ?`, toMillis(now)); err != nil {
		return fmt.Errorf("delete expired dedup keys: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM idempotency_keys WHERE expires_at <= ?`, toMillis(now)); err != nil {
		return fmt.Errorf("delete expired idempotency keys: %w", err)
	}
	_, err = tx.ExecContext(ctx,
		`DELETE FROM outbox_entries WHERE state = ? AND updated_at <= ?`,
		store.StateFailed, toMillis(failedBefore),
	)
	if err != nil {
		return fmt.Errorf("delete expired failed entries: %w", err)
	}

	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}
