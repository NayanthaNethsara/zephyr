package store

import (
	"context"
	"time"
)

type IdempotencyOutcome string

const (
	IdempotencyAcquired   IdempotencyOutcome = "acquired"
	IdempotencyCompleted  IdempotencyOutcome = "completed"
	IdempotencyInProgress IdempotencyOutcome = "in_progress"
	IdempotencyMismatch   IdempotencyOutcome = "mismatch"
)

type IdempotencyClaim struct {
	Key         string
	Fingerprint string
	Now         time.Time
	LeaseUntil  time.Time
	ExpiresAt   time.Time
}

type IdempotencyResult struct {
	Outcome   IdempotencyOutcome
	MessageID string
}

type IdempotencyStore interface {
	ClaimIdempotencyKey(ctx context.Context, claim IdempotencyClaim) (IdempotencyResult, error)
	CompleteIdempotencyKey(ctx context.Context, key string, messageID string) error
	ReleaseIdempotencyKey(ctx context.Context, key string) error
}
