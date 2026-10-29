package meta

import (
	"encoding/json"
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

func TestParseWebhookMessage(t *testing.T) {
	items, err := ParseWebhook([]byte(messagePayload))
	if err != nil {
		t.Fatalf("ParseWebhook() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}

	item := items[0]
	if item.MessageID != "wamid.IN1" || item.Subtype != "text" {
		t.Errorf("message id/subtype = %s/%s", item.MessageID, item.Subtype)
	}
	if item.Type != core.EventMessageInbound || item.DedupKey != "msg:wamid.IN1" {
		t.Errorf("type/dedup = %s/%s", item.Type, item.DedupKey)
	}
	if item.PartitionKey != "94770000000" || item.PhoneNumberID != "PNID1" || item.WABAID != "WABA1" {
		t.Errorf("identifiers = %+v", item)
	}
	if want := time.Unix(1790000000, 0).UTC(); !item.OccurredAt.Equal(want) {
		t.Errorf("occurred_at = %s, want %s", item.OccurredAt, want)
	}

	var data struct {
		WAMID     string          `json:"wamid"`
		From      string          `json:"from"`
		Timestamp int64           `json:"timestamp"`
		Type      string          `json:"type"`
		Raw       json.RawMessage `json:"raw"`
	}
	if err := json.Unmarshal(item.Data, &data); err != nil {
		t.Fatalf("data: %v", err)
	}
	if data.WAMID != "wamid.IN1" || data.From != "94770000000" || data.Type != "text" || data.Timestamp != 1790000000 {
		t.Errorf("data = %+v", data)
	}
	var raw map[string]any
	if err := json.Unmarshal(data.Raw, &raw); err != nil || raw["id"] != "wamid.IN1" {
		t.Errorf("raw = %s, err = %v", data.Raw, err)
	}
}

func TestParseWebhookStatus(t *testing.T) {
	items, err := ParseWebhook([]byte(statusPayload))
	if err != nil {
		t.Fatalf("ParseWebhook() error = %v", err)
	}
	if len(items) != 1 {
		t.Fatalf("items = %d, want 1", len(items))
	}

	item := items[0]
	if item.MessageID != "wamid.OUT1" || item.Subtype != "failed" {
		t.Errorf("message id/subtype = %s/%s", item.MessageID, item.Subtype)
	}
	if item.Type != core.EventMessageStatus || item.DedupKey != "status:wamid.OUT1:failed" || item.PartitionKey != "94771111111" {
		t.Errorf("item = %+v", item)
	}

	var data struct {
		WAMID  string          `json:"wamid"`
		Status string          `json:"status"`
		Errors json.RawMessage `json:"errors"`
	}
	if err := json.Unmarshal(item.Data, &data); err != nil {
		t.Fatalf("data: %v", err)
	}
	if data.WAMID != "wamid.OUT1" || data.Status != "failed" || string(data.Errors) != `[{"code":131047}]` {
		t.Errorf("data = %+v, errors = %s", data, data.Errors)
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
