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

func TestIngestBuildsOutboxRecords(t *testing.T) {
	now := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	occurred := now.Add(-time.Minute)
	fake := &fakeStore{}
	service := NewService(fake, "wa", 48*time.Hour)
	service.now = func() time.Time { return now }

	items := []Inbound{
		{
			Type:          EventMessageInbound,
			MessageID:     "wamid.1",
			Subtype:       "text",
			DedupKey:      "msg:wamid.1",
			PartitionKey:  "94770000000",
			PhoneNumberID: "111",
			WABAID:        "222",
			OccurredAt:    occurred,
			Data:          json.RawMessage(`{"wamid":"wamid.1"}`),
		},
		{
			Type:         EventMessageStatus,
			DedupKey:     "status:wamid.2:read",
			PartitionKey: "94771111111",
			Data:         json.RawMessage(`{"wamid":"wamid.2"}`),
		},
	}

	results, err := service.Ingest(context.Background(), items)
	if err != nil {
		t.Fatalf("Ingest() error = %v", err)
	}
	if len(results) != 2 || len(fake.saved) != 2 {
		t.Fatalf("results = %d, records = %d, want 2", len(results), len(fake.saved))
	}

	tests := []struct {
		index     int
		wantTopic string
		wantKey   string
		wantType  EventType
		wantTime  time.Time
	}{
		{index: 0, wantTopic: "wa.inbound", wantKey: "94770000000", wantType: EventMessageInbound, wantTime: occurred},
		{index: 1, wantTopic: "wa.status", wantKey: "94771111111", wantType: EventMessageStatus, wantTime: now},
	}
	for _, tt := range tests {
		record := fake.saved[tt.index]
		if record.Entry.Topic != tt.wantTopic || record.Entry.Key != tt.wantKey {
			t.Errorf("record %d topic/key = %s/%s, want %s/%s", tt.index, record.Entry.Topic, record.Entry.Key, tt.wantTopic, tt.wantKey)
		}
		if !record.DedupExpiresAt.Equal(now.Add(48 * time.Hour)) {
			t.Errorf("record %d dedup expiry = %s", tt.index, record.DedupExpiresAt)
		}

		var envelope Envelope
		if err := json.Unmarshal(record.Entry.Payload, &envelope); err != nil {
			t.Fatalf("record %d payload: %v", tt.index, err)
		}
		if envelope.ID != record.Entry.ID || !strings.HasPrefix(envelope.ID, "evt_") {
			t.Errorf("record %d envelope id = %q, entry id = %q", tt.index, envelope.ID, record.Entry.ID)
		}
		if envelope.Type != tt.wantType || !envelope.OccurredAt.Equal(tt.wantTime) {
			t.Errorf("record %d envelope = %+v", tt.index, envelope)
		}
	}
	if fake.saved[0].Entry.ID == fake.saved[1].Entry.ID {
		t.Error("event ids must be unique")
	}

	first := results[0]
	if !first.Queued || first.EventID != fake.saved[0].Entry.ID || first.MessageID != "wamid.1" || first.Subtype != "text" || first.MaskedPhone != "9477*****00" {
		t.Errorf("first result = %+v", first)
	}
}

func TestIngestReportsDuplicates(t *testing.T) {
	fake := &fakeStore{duplicates: map[string]bool{"msg:wamid.dup": true}}
	service := NewService(fake, "wa", time.Hour)

	results, err := service.Ingest(context.Background(), []Inbound{
		{Type: EventMessageInbound, MessageID: "wamid.new", DedupKey: "msg:wamid.new", PartitionKey: "94770000000"},
		{Type: EventMessageInbound, MessageID: "wamid.dup", DedupKey: "msg:wamid.dup", PartitionKey: "94770000000"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !results[0].Queued || results[0].EventID == "" {
		t.Errorf("new result = %+v", results[0])
	}
	if results[1].Queued || results[1].EventID != "" || results[1].MessageID != "wamid.dup" {
		t.Errorf("duplicate result = %+v", results[1])
	}
}

func TestIngestReturnsStoreError(t *testing.T) {
	want := errors.New("disk full")
	service := NewService(&fakeStore{saveErr: want}, "wa", time.Hour)

	_, err := service.Ingest(context.Background(), []Inbound{{Type: EventMessageInbound, DedupKey: "k"}})
	if !errors.Is(err, want) {
		t.Fatalf("Ingest() error = %v, want %v", err, want)
	}
}

func TestReady(t *testing.T) {
	want := errors.New("db down")
	if err := NewService(&fakeStore{pingErr: want}, "wa", time.Hour).Ready(context.Background()); !errors.Is(err, want) {
		t.Fatalf("Ready() error = %v, want %v", err, want)
	}
	if err := NewService(&fakeStore{}, "wa", time.Hour).Ready(context.Background()); err != nil {
		t.Fatalf("Ready() error = %v", err)
	}
}
