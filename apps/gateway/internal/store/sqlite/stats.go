package sqlite

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/store"
)

func (s *Store) OutboxStats(ctx context.Context) (store.OutboxStats, error) {
	var (
		stats  store.OutboxStats
		oldest sql.NullInt64
	)
	err := s.db.QueryRowContext(ctx, `
		SELECT
			COALESCE(SUM(CASE WHEN state = ? THEN 1 ELSE 0 END), 0),
			COALESCE(SUM(CASE WHEN state = ? THEN 1 ELSE 0 END), 0),
			MIN(CASE WHEN state = ? THEN created_at END)
		FROM outbox_entries`,
		store.StatePending, store.StateFailed, store.StatePending,
	).Scan(&stats.Pending, &stats.Failed, &oldest)
	if err != nil {
		return store.OutboxStats{}, fmt.Errorf("read outbox stats: %w", err)
	}
	if oldest.Valid {
		stats.OldestPendingCreatedAt = fromMillis(oldest.Int64)
	}
	return stats, nil
}
