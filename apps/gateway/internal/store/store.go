package store

import (
	"context"
	"time"
)

const (
	StatePending = "pending"
	StateFailed  = "failed"
)

type OutboxEntry struct {
	ID            string
	Topic         string
	Key           string
	Payload       []byte
	Attempts      int
	NextAttemptAt time.Time
	CreatedAt     time.Time
	LastError     string
}

type InboundRecord struct {
	DedupKey       string
	DedupExpiresAt time.Time
	Entry          OutboxEntry
}

type OutboxStats struct {
	Pending                int
	Failed                 int
	OldestPendingCreatedAt time.Time
}

type Store interface {
	SaveInbound(ctx context.Context, records []InboundRecord) ([]string, error)
	OutboxStats(ctx context.Context) (OutboxStats, error)
	ListDue(ctx context.Context, now time.Time, limit int) ([]OutboxEntry, error)
	Delete(ctx context.Context, id string) error
	Reschedule(ctx context.Context, id string, attempts int, nextAttemptAt time.Time, lastError string) error
	MarkFailed(ctx context.Context, id string, attempts int, lastError string, at time.Time) error
	DeleteExpired(ctx context.Context, now time.Time, failedBefore time.Time) error
	Ping(ctx context.Context) error
	Close() error
}
