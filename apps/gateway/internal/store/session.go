package store

import (
	"context"
	"time"
)

type SessionTouch struct {
	PhoneHash     string
	PhoneNumberID string
	At            time.Time
}

type SessionStore interface {
	LastInbound(ctx context.Context, phoneHash, phoneNumberID string) (time.Time, bool, error)
	DeleteExpiredSessions(ctx context.Context, before time.Time) error
}
