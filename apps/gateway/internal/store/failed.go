package store

import (
	"context"
	"time"
)

type FailedEntry struct {
	ID        string
	Topic     string
	Attempts  int
	LastError string
	CreatedAt time.Time
	FailedAt  time.Time
}

type FailedEventStore interface {
	ListFailed(ctx context.Context, limit, offset int) ([]FailedEntry, error)
	Requeue(ctx context.Context, id string, at time.Time) (bool, error)
}
