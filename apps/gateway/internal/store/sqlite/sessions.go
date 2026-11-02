package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"
)

const upsertSession = `
INSERT INTO sessions (phone_hash, phone_number_id, last_inbound_at) VALUES (?, ?, ?)
ON CONFLICT(phone_hash, phone_number_id) DO UPDATE SET last_inbound_at = MAX(last_inbound_at, excluded.last_inbound_at)`

func (s *Store) LastInbound(ctx context.Context, phoneHash, phoneNumberID string) (time.Time, bool, error) {
	var lastInboundAt int64
	err := s.db.QueryRowContext(ctx,
		`SELECT last_inbound_at FROM sessions WHERE phone_hash = ? AND phone_number_id = ?`,
		phoneHash, phoneNumberID,
	).Scan(&lastInboundAt)
	if errors.Is(err, sql.ErrNoRows) {
		return time.Time{}, false, nil
	}
	if err != nil {
		return time.Time{}, false, fmt.Errorf("read session: %w", err)
	}
	return fromMillis(lastInboundAt), true, nil
}

func (s *Store) DeleteExpiredSessions(ctx context.Context, before time.Time) error {
	if _, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE last_inbound_at <= ?`, toMillis(before)); err != nil {
		return fmt.Errorf("delete expired sessions: %w", err)
	}
	return nil
}
