package meta

import (
	"encoding/json"
	"reflect"
	"testing"
	"time"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/core"
)

const messagePayload = `{
  "object": "whatsapp_business_account",
  "entry": [{
    "id": "WABA1",
    "changes": [{
      "field": "messages",
      "value": {
        "messaging_product": "whatsapp",
        "metadata": {"display_phone_number": "15550000000", "phone_number_id": "PNID1"},
        "contacts": [{"profile": {"name": "Sam"}, "wa_id": "94770000000"}],
        "messages": [{"from": "94770000000", "id": "wamid.IN1", "timestamp": "1790000000", "type": "text", "text": {"body": "hi"}}]
      }
    }]
  }]
}`

const statusPayload = `{
  "object": "whatsapp_business_account",
  "entry": [{
    "id": "WABA1",
    "changes": [{
      "field": "messages",
      "value": {
        "metadata": {"phone_number_id": "PNID1"},
        "statuses": [{"id": "wamid.OUT1", "status": "failed", "timestamp": "1790000100", "recipient_id": "94771111111", "errors": [{"code": 131047}]}]
      }
    }]
  }]
}`

func TestParseWebhookEvents(t *testing.T) {
	tests := []struct {
		name          string
		body          string
		wantType      core.EventType
		wantMessageID string
		wantSubtype   string
		wantDedupKey  string
		wantPartition string
		wantOccurred  time.Time
		wantData      map[string]any
		wantRawID     string
	}{
		{
			name:          "inbound text message",
			body:          messagePayload,
			wantType:      core.EventMessageInbound,
			wantMessageID: "wamid.IN1",
			wantSubtype:   "text",
			wantDedupKey:  "msg:wamid.IN1",
			wantPartition: "94770000000",
			wantOccurred:  time.Unix(1790000000, 0).UTC(),
			wantData: map[string]any{
				"wamid":     "wamid.IN1",
				"from":      "94770000000",
				"type":      "text",
				"timestamp": float64(1790000000),
			},
			wantRawID: "wamid.IN1",
		},
		{
			name:          "failed delivery status",
			body:          statusPayload,
			wantType:      core.EventMessageStatus,
			wantMessageID: "wamid.OUT1",
			wantSubtype:   "failed",
			wantDedupKey:  "status:wamid.OUT1:failed",
			wantPartition: "94771111111",
			wantOccurred:  time.Unix(1790000100, 0).UTC(),
			wantData: map[string]any{
				"wamid":     "wamid.OUT1",
				"status":    "failed",
				"recipient": "94771111111",
				"timestamp": float64(1790000100),
				"errors":    []any{map[string]any{"code": float64(131047)}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items, err := ParseWebhook([]byte(tt.body))
			if err != nil {
				t.Fatalf("ParseWebhook() error = %v", err)
			}
			if len(items) != 1 {
				t.Fatalf("items = %d, want 1", len(items))
			}

			item := items[0]
			if item.Type != tt.wantType || item.MessageID != tt.wantMessageID || item.Subtype != tt.wantSubtype {
				t.Errorf("type/message/subtype = %s/%s/%s", item.Type, item.MessageID, item.Subtype)
			}
			if item.DedupKey != tt.wantDedupKey || item.PartitionKey != tt.wantPartition {
				t.Errorf("dedup/partition = %s/%s", item.DedupKey, item.PartitionKey)
			}
			if item.PhoneNumberID != "PNID1" || item.WABAID != "WABA1" {
				t.Errorf("phone number id / waba id = %s/%s", item.PhoneNumberID, item.WABAID)
			}
			if !item.OccurredAt.Equal(tt.wantOccurred) {
				t.Errorf("occurred_at = %s, want %s", item.OccurredAt, tt.wantOccurred)
			}

			var data map[string]any
			if err := json.Unmarshal(item.Data, &data); err != nil {
				t.Fatalf("data: %v", err)
			}
			for key, want := range tt.wantData {
				if !reflect.DeepEqual(data[key], want) {
					t.Errorf("data[%s] = %v, want %v", key, data[key], want)
				}
			}
			if tt.wantRawID != "" {
				raw, _ := data["raw"].(map[string]any)
				if raw["id"] != tt.wantRawID {
					t.Errorf("raw message = %v, want id %s", data["raw"], tt.wantRawID)
				}
			}
		})
	}
}

func TestParseWebhookIgnoresUnrelatedPayloads(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "other object", body: `{"object":"page","entry":[]}`},
		{name: "other field", body: `{"object":"whatsapp_business_account","entry":[{"id":"1","changes":[{"field":"account_update","value":{}}]}]}`},
		{name: "no entries", body: `{"object":"whatsapp_business_account"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items, err := ParseWebhook([]byte(tt.body))
			if err != nil || len(items) != 0 {
				t.Fatalf("items = %d, err = %v, want none", len(items), err)
			}
		})
	}
}

func TestParseWebhookRejectsMalformedPayloads(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "invalid json", body: `{`},
		{name: "message without id", body: `{"object":"whatsapp_business_account","entry":[{"id":"1","changes":[{"field":"messages","value":{"messages":[{"from":"1"}]}}]}]}`},
		{name: "status without status", body: `{"object":"whatsapp_business_account","entry":[{"id":"1","changes":[{"field":"messages","value":{"statuses":[{"id":"w"}]}}]}]}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseWebhook([]byte(tt.body)); err == nil {
				t.Fatal("ParseWebhook() error = nil, want error")
			}
		})
	}
}
