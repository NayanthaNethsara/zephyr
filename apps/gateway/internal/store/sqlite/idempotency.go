package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/store"
)

const (
	stateInProgress = "in_progress"
	stateCompleted  = "completed"
)

const upsertClaim = `
INSERT INTO idempotency_keys (idempotency_key, fingerprint, state, message_id, lease_expires_at, expires_at, created_at)
VALUES (?, ?, ?, '', ?, ?, ?)
ON CONFLICT(idempotency_key) DO UPDATE SET
    fingerprint = excluded.fingerprint,
    state = excluded.state,
    message_id = '',
    lease_expires_at = excluded.lease_expires_at,
    expires_at = excluded.expires_at,
    created_at = excluded.created_at`

type idempotencyRow struct {
	fingerprint    string
	state          string
	messageID      string
	leaseExpiresAt int64
	expiresAt      int64
}

func (s *Store) ClaimIdempotencyKey(ctx context.Context, claim store.IdempotencyClaim) (store.IdempotencyResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return store.IdempotencyResult{}, fmt.Errorf("begin transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()

	now := toMillis(claim.Now)
	row, found, err := readIdempotencyRow(ctx, tx, claim.Key)
	if err != nil {
		return store.IdempotencyResult{}, err
	}

	if !found || row.expiresAt <= now {
		return commitClaim(ctx, tx, claim)
	}
	if row.fingerprint != claim.Fingerprint {
		return store.IdempotencyResult{Outcome: store.IdempotencyMismatch}, nil
	}
	if row.state == stateCompleted {
		return store.IdempotencyResult{Outcome: store.IdempotencyCompleted, MessageID: row.messageID}, nil
	}
	if row.leaseExpiresAt <= now {
		return commitClaim(ctx, tx, claim)
	}
	return store.IdempotencyResult{Outcome: store.IdempotencyInProgress}, nil
}

func readIdempotencyRow(ctx context.Context, tx *sql.Tx, key string) (idempotencyRow, bool, error) {
	var row idempotencyRow
	err := tx.QueryRowContext(ctx, `
		SELECT fingerprint, state, message_id, lease_expires_at, expires_at
		FROM idempotency_keys WHERE idempotency_key = ?`, key,
	).Scan(&row.fingerprint, &row.state, &row.messageID, &row.leaseExpiresAt, &row.expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		return idempotencyRow{}, false, nil
	}
	if err != nil {
		return idempotencyRow{}, false, fmt.Errorf("read idempotency key: %w", err)
	}
	return row, true, nil
}

func commitClaim(ctx context.Context, tx *sql.Tx, claim store.IdempotencyClaim) (store.IdempotencyResult, error) {
	_, err := tx.ExecContext(ctx, upsertClaim,
		claim.Key, claim.Fingerprint, stateInProgress,
		toMillis(claim.LeaseUntil), toMillis(claim.ExpiresAt), toMillis(claim.Now),
	)
	if err != nil {
		return store.IdempotencyResult{}, fmt.Errorf("claim idempotency key: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return store.IdempotencyResult{}, fmt.Errorf("commit transaction: %w", err)
	}
	return store.IdempotencyResult{Outcome: store.IdempotencyAcquired}, nil
}

func (s *Store) CompleteIdempotencyKey(ctx context.Context, key string, messageID string) error {
	_, err := s.db.ExecContext(ctx, `
		UPDATE idempotency_keys SET state = ?, message_id = ?, lease_expires_at = 0
		WHERE idempotency_key = ?`, stateCompleted, messageID, key,
	)
	if err != nil {
		return fmt.Errorf("complete idempotency key: %w", err)
	}
	return nil
}

func (s *Store) ReleaseIdempotencyKey(ctx context.Context, key string) error {
	_, err := s.db.ExecContext(ctx,
		`DELETE FROM idempotency_keys WHERE idempotency_key = ? AND state = ?`, key, stateInProgress,
	)
	if err != nil {
		return fmt.Errorf("release idempotency key: %w", err)
	}
	return nil
}
