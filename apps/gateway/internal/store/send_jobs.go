package store

import (
	"context"
	"time"
)

const (
	SendQueued = "queued"
	SendSent   = "sent"
	SendFailed = "failed"
)

type SendJob struct {
	ID            string
	Payload       []byte
	Attempts      int
	NextAttemptAt time.Time
	CreatedAt     time.Time
}

type SendOutcome struct {
	State        string
	MessageID    string
	ErrorCode    string
	ErrorMessage string
	IsRetryable  bool
	MetaCode     int
	At           time.Time
}

type SendStatus struct {
	ID           string
	State        string
	MessageID    string
	ErrorCode    string
	ErrorMessage string
	IsRetryable  bool
	MetaCode     int
	Attempts     int
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

type SendJobStore interface {
	EnqueueSend(ctx context.Context, job SendJob) error
	ListDueSends(ctx context.Context, now time.Time, limit int) ([]SendJob, error)
	RescheduleSend(ctx context.Context, id string, attempts int, nextAttemptAt time.Time) error
	FinishSend(ctx context.Context, id string, attempts int, outcome SendOutcome, event OutboxEntry) error
	SendStatus(ctx context.Context, id string) (SendStatus, bool, error)
	DeleteExpiredSends(ctx context.Context, before time.Time) error
}
