package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/store"
)

type fakeStore struct {
	store.Store
	saved      []store.InboundRecord
	saveErr    error
	pingErr    error
	duplicates map[string]bool
}

func (f *fakeStore) SaveInbound(_ context.Context, records []store.InboundRecord) ([]string, error) {
	if f.saveErr != nil {
		return nil, f.saveErr
	}
	f.saved = append(f.saved, records...)
	ids := make([]string, 0, len(records))
	for _, record := range records {
		if f.duplicates[record.DedupKey] {
			continue
		}
		ids = append(ids, record.Entry.ID)
	}
	return ids, nil
}

func (f *fakeStore) Ping(context.Context) error {
	return f.pingErr
}

func TestIngest(t *testing.T) {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	occurred := now.Add(-time.Minute)
	storeDown := errors.New("disk full")

	type wantItem struct {
		topic       string
		key         string
		eventType   EventType
		occurredAt  time.Time
		isQueued    bool
		maskedPhone string
	}

	tests := []struct {
		name       string
		items      []Inbound
		duplicates map[string]bool
		saveErr    error
		want       []wantItem
		wantErr    error
	}{
		{
			name: "inbound message uses the inbound topic and its own timestamp",
			items: []Inbound{{
				Type: EventMessageInbound, MessageID: "wamid.1", Subtype: "text", DedupKey: "msg:wamid.1",
				PartitionKey: "94770000000", PhoneNumberID: "111", WABAID: "222", OccurredAt: occurred,
				Data: json.RawMessage(`{"wamid":"wamid.1"}`),
			}},
			want: []wantItem{{topic: "wa.inbound", key: "94770000000", eventType: EventMessageInbound, occurredAt: occurred, isQueued: true, maskedPhone: "9477*****00"}},
		},
		{
			name: "status without a timestamp falls back to now and uses the status topic",
			items: []Inbound{{
				Type: EventMessageStatus, MessageID: "wamid.2", Subtype: "read", DedupKey: "status:wamid.2:read",
				PartitionKey: "94771111111", Data: json.RawMessage(`{"wamid":"wamid.2"}`),
			}},
			want: []wantItem{{topic: "wa.status", key: "94771111111", eventType: EventMessageStatus, occurredAt: now, isQueued: true, maskedPhone: "9477*****11"}},
		},
		{
			name: "duplicate is reported without an event id",
			items: []Inbound{
				{Type: EventMessageInbound, MessageID: "wamid.new", DedupKey: "msg:wamid.new", PartitionKey: "94770000000"},
				{Type: EventMessageInbound, MessageID: "wamid.dup", DedupKey: "msg:wamid.dup", PartitionKey: "94770000000"},
			},
			duplicates: map[string]bool{"msg:wamid.dup": true},
			want: []wantItem{
				{topic: "wa.inbound", key: "94770000000", eventType: EventMessageInbound, occurredAt: now, isQueued: true, maskedPhone: "9477*****00"},
				{topic: "wa.inbound", key: "94770000000", eventType: EventMessageInbound, occurredAt: now, isQueued: false, maskedPhone: "9477*****00"},
			},
		},
		{
			name:    "store failure is returned",
			items:   []Inbound{{Type: EventMessageInbound, DedupKey: "k"}},
			saveErr: storeDown,
			wantErr: storeDown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeStore{duplicates: tt.duplicates, saveErr: tt.saveErr}
			service := NewService(fake, "wa", 48*time.Hour)
			service.now = func() time.Time { return now }

			results, err := service.Ingest(context.Background(), tt.items)
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Ingest() error = %v, want %v", err, tt.wantErr)
			}
			if tt.wantErr != nil {
				return
			}
			if len(results) != len(tt.want) || len(fake.saved) != len(tt.want) {
				t.Fatalf("results = %d, records = %d, want %d", len(results), len(fake.saved), len(tt.want))
			}

			seenIDs := map[string]bool{}
			for i, want := range tt.want {
				record := fake.saved[i]
				result := results[i]

				if record.Entry.Topic != want.topic || record.Entry.Key != want.key {
					t.Errorf("item %d topic/key = %s/%s, want %s/%s", i, record.Entry.Topic, record.Entry.Key, want.topic, want.key)
				}
				if !record.DedupExpiresAt.Equal(now.Add(48 * time.Hour)) {
					t.Errorf("item %d dedup expiry = %s", i, record.DedupExpiresAt)
				}

				var envelope Envelope
				if err := json.Unmarshal(record.Entry.Payload, &envelope); err != nil {
					t.Fatalf("item %d payload: %v", i, err)
				}
				if envelope.ID != record.Entry.ID || !strings.HasPrefix(envelope.ID, "evt_") || seenIDs[envelope.ID] {
					t.Errorf("item %d envelope id = %q (entry %q, duplicate %v)", i, envelope.ID, record.Entry.ID, seenIDs[envelope.ID])
				}
				seenIDs[envelope.ID] = true
				if envelope.Type != want.eventType || !envelope.OccurredAt.Equal(want.occurredAt) {
					t.Errorf("item %d envelope = %+v", i, envelope)
				}

				if result.IsQueued != want.isQueued || result.MaskedPhone != want.maskedPhone || result.MessageID != tt.items[i].MessageID {
					t.Errorf("item %d result = %+v", i, result)
				}
				if wantEventID := want.isQueued; (result.EventID != "") != wantEventID || (want.isQueued && result.EventID != record.Entry.ID) {
					t.Errorf("item %d event id = %q, queued %v", i, result.EventID, want.isQueued)
				}
			}
		})
	}
}

func TestReady(t *testing.T) {
	storeDown := errors.New("db down")

	tests := []struct {
		name    string
		pingErr error
		wantErr error
	}{
		{name: "store reachable", pingErr: nil, wantErr: nil},
		{name: "store down", pingErr: storeDown, wantErr: storeDown},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := NewService(&fakeStore{pingErr: tt.pingErr}, "wa", time.Hour).Ready(context.Background())
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("Ready() error = %v, want %v", err, tt.wantErr)
			}
		})
	}
}
