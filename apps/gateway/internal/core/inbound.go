package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"time"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/store"
)

type Service struct {
	store       store.Store
	topicPrefix string
	dedupTTL    time.Duration
	now         func() time.Time
}

func NewService(s store.Store, topicPrefix string, dedupTTL time.Duration) *Service {
	return &Service{
		store:       s,
		topicPrefix: topicPrefix,
		dedupTTL:    dedupTTL,
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
			Queued:      isQueued,
		}
		if isQueued {
			results[i].EventID = id
		}
	}
	return results
}

func (s *Service) buildRecord(item Inbound, now time.Time) (store.InboundRecord, error) {
	id, err := newEventID()
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

func newEventID() (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate event id: %w", err)
	}
	return "evt_" + hex.EncodeToString(raw), nil
}
