package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/store"
)

func (s *Store) EnqueueSend(ctx context.Context, job store.SendJob) error {
	created := toMillis(job.CreatedAt)
	payload := job.Payload
	if payload == nil {
		payload = []byte{}
	}
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO send_jobs (id, state, payload, attempts, next_attempt_at, created_at, updated_at)
		VALUES (?, ?, ?, 0, ?, ?, ?)`,
		job.ID, store.SendQueued, payload, toMillis(job.NextAttemptAt), created, created,
	)
	if err != nil {
		return fmt.Errorf("enqueue send job: %w", err)
	}
	return nil
}

func (s *Store) ListDueSends(ctx context.Context, now time.Time, limit int) ([]store.SendJob, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT id, payload, attempts, next_attempt_at, created_at
		FROM send_jobs
		WHERE state = ? AND next_attempt_at <= ?
		ORDER BY created_at, id
		LIMIT ?`,
		store.SendQueued, toMillis(now), limit,
	)
	if err != nil {
		return nil, fmt.Errorf("list due send jobs: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var jobs []store.SendJob
	for rows.Next() {
		var (
			job           store.SendJob
			nextAttemptAt int64
			createdAt     int64
		)
		if err := rows.Scan(&job.ID, &job.Payload, &job.Attempts, &nextAttemptAt, &createdAt); err != nil {
			return nil, fmt.Errorf("scan send job: %w", err)
		}
		job.NextAttemptAt = fromMillis(nextAttemptAt)
		job.CreatedAt = fromMillis(createdAt)
		jobs = append(jobs, job)
	}
	return jobs, rows.Err()
}

func (s *Store) RescheduleSend(ctx context.Context, id string, attempts int, nextAttemptAt time.Time) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE send_jobs SET attempts = ?, next_attempt_at = ?, updated_at = ?
		WHERE id = ? AND state = ?`,
		attempts, toMillis(nextAttemptAt), toMillis(time.Now()), id, store.SendQueued,
	)
	if err != nil {
		return fmt.Errorf("reschedule send job: %w", err)
	}
	return nil
}

func (s *Store) FinishSend(ctx context.Context, id string, attempts int, outcome store.SendOutcome, event store.OutboxEntry) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `
		UPDATE send_jobs
		SET state = ?, payload = x'', attempts = ?, message_id = ?, error_code = ?, error_message = ?,
		    is_retryable = ?, meta_code = ?, updated_at = ?
		WHERE id = ? AND state = ?`,
		outcome.State, attempts, outcome.MessageID, outcome.ErrorCode, outcome.ErrorMessage,
		outcome.IsRetryable, outcome.MetaCode, toMillis(outcome.At), id, store.SendQueued,
	)
	if err != nil {
		return fmt.Errorf("finish send job: %w", err)
	}
	if changed, err := result.RowsAffected(); err != nil || changed == 0 {
		return nil
	}

	_, err = tx.ExecContext(ctx, insertOutbox,
		event.ID, event.Topic, event.Key, event.Payload,
		toMillis(event.NextAttemptAt), store.StatePending, toMillis(event.CreatedAt), toMillis(event.CreatedAt),
	)
	if err != nil {
		return fmt.Errorf("insert result event: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit transaction: %w", err)
	}
	return nil
}

func (s *Store) SendStatus(ctx context.Context, id string) (store.SendStatus, bool, error) {
	var (
		status    store.SendStatus
		createdAt int64
		updatedAt int64
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT id, state, message_id, error_code, error_message, is_retryable, meta_code, attempts, created_at, updated_at
		FROM send_jobs WHERE id = ?`, id,
	).Scan(&status.ID, &status.State, &status.MessageID, &status.ErrorCode, &status.ErrorMessage,
		&status.IsRetryable, &status.MetaCode, &status.Attempts, &createdAt, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return store.SendStatus{}, false, nil
	}
	if err != nil {
		return store.SendStatus{}, false, fmt.Errorf("read send status: %w", err)
	}
	status.CreatedAt = fromMillis(createdAt)
	status.UpdatedAt = fromMillis(updatedAt)
	return status, true, nil
}

func (s *Store) DeleteExpiredSends(ctx context.Context, before time.Time) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM send_jobs WHERE state != ? AND updated_at <= ?`,
		store.SendQueued, toMillis(before),
	)
	if err != nil {
		return fmt.Errorf("delete expired send jobs: %w", err)
	}
	return nil
}
