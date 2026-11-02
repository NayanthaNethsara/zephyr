package core

import (
	"context"
	"time"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/store"
)

type FailedEvent struct {
	ID        string
	Topic     string
	Attempts  int
	LastError string
	CreatedAt time.Time
	FailedAt  time.Time
}

type FailedEventService struct {
	store store.FailedEventStore
	now   func() time.Time
}

func NewFailedEventService(failed store.FailedEventStore) *FailedEventService {
	return &FailedEventService{store: failed, now: time.Now}
}

func (s *FailedEventService) ListFailed(ctx context.Context, limit, offset int) ([]FailedEvent, error) {
	entries, err := s.store.ListFailed(ctx, limit, offset)
	if err != nil {
		return nil, err
	}
	events := make([]FailedEvent, 0, len(entries))
	for _, entry := range entries {
		events = append(events, FailedEvent(entry))
	}
	return events, nil
}

func (s *FailedEventService) Replay(ctx context.Context, eventID string) error {
	if !isSafeIdentifier(eventID) {
		return invalidRequest("event id must be 1 to 128 letters, digits, hyphens, or underscores")
	}
	isQueued, err := s.store.Requeue(ctx, eventID, s.now())
	if err != nil {
		return err
	}
	if !isQueued {
		return &RequestError{Code: CodeEventNotFound, Message: "there is no failed event with this id"}
	}
	return nil
}
