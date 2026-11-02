package core

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/store"
)

type Service struct {
	store       store.Store
	topicPrefix string
	dedupTTL    time.Duration
	phoneHash   []byte
	now         func() time.Time
}

func NewService(s store.Store, topicPrefix string, dedupTTL time.Duration, phoneHashKey string) *Service {
	return &Service{
		store:       s,
		topicPrefix: topicPrefix,
		dedupTTL:    dedupTTL,
		phoneHash:   []byte(phoneHashKey),
		now:         time.Now,
	}
}

func (s *Service) Ready(ctx context.Context) error {
	return s.store.Ping(ctx)
}

func (s *Service) Ingest(ctx context.Context, items []Inbound) ([]IngestResult, error) {
	now := s.now()
	records := make([]store.InboundRecord, 0, len(items))

	for _, item := range items {
		record, err := s.buildRecord(item, now)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}

	saved, err := s.store.SaveInbound(ctx, records)
	if err != nil {
		return nil, err
	}
	return buildResults(items, records, saved), nil
}

func buildResults(items []Inbound, records []store.InboundRecord, saved []string) []IngestResult {
	queued := make(map[string]struct{}, len(saved))
	for _, id := range saved {
		queued[id] = struct{}{}
	}

	results := make([]IngestResult, len(items))
	for i, item := range items {
		id := records[i].Entry.ID
		_, isQueued := queued[id]

		results[i] = IngestResult{
			MessageID:   item.MessageID,
			Type:        item.Type,
			Subtype:     item.Subtype,
			MaskedPhone: MaskPhone(item.PartitionKey),
			IsQueued:    isQueued,
		}
		if isQueued {
			results[i].EventID = id
		}
	}
	return results
}

func (s *Service) buildRecord(item Inbound, now time.Time) (store.InboundRecord, error) {
	id, err := newID("evt_")
	if err != nil {
		return store.InboundRecord{}, err
	}

	occurredAt := item.OccurredAt
	if occurredAt.IsZero() {
		occurredAt = now
	}

	payload, err := json.Marshal(Envelope{
		ID:            id,
		Type:          item.Type,
		OccurredAt:    occurredAt.UTC(),
		PhoneNumberID: item.PhoneNumberID,
		WABAID:        item.WABAID,
		Data:          item.Data,
	})
	if err != nil {
		return store.InboundRecord{}, fmt.Errorf("encode envelope: %w", err)
	}

	return store.InboundRecord{
		DedupKey:       item.DedupKey,
		DedupExpiresAt: now.Add(s.dedupTTL),
		Session:        s.sessionTouch(item, occurredAt),
		Entry: store.OutboxEntry{
			ID:            id,
			Topic:         s.topicPrefix + "." + item.Type.topicSuffix(),
			Key:           item.PartitionKey,
			Payload:       payload,
			NextAttemptAt: now,
			CreatedAt:     now,
		},
	}, nil
}

func (s *Service) sessionTouch(item Inbound, at time.Time) *store.SessionTouch {
	if item.Type != EventMessageInbound || item.PartitionKey == "" {
		return nil
	}
	return &store.SessionTouch{
		PhoneHash:     hashPhone(s.phoneHash, item.PartitionKey),
		PhoneNumberID: item.PhoneNumberID,
		At:            at,
	}
}
