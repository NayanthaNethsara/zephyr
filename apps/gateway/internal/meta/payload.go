package meta

import (
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/core"
)

const businessAccountObject = "whatsapp_business_account"

type webhookPayload struct {
	Object string         `json:"object"`
	Entry  []webhookEntry `json:"entry"`
}

type webhookEntry struct {
	ID      string          `json:"id"`
	Changes []webhookChange `json:"changes"`
}

type webhookChange struct {
	Field string      `json:"field"`
	Value changeValue `json:"value"`
}

type changeValue struct {
	Metadata struct {
		PhoneNumberID string `json:"phone_number_id"`
	} `json:"metadata"`
	Messages []json.RawMessage `json:"messages"`
	Statuses []json.RawMessage `json:"statuses"`
}

type messageFields struct {
	ID        string `json:"id"`
	From      string `json:"from"`
	Timestamp string `json:"timestamp"`
	Type      string `json:"type"`
}

type statusFields struct {
	ID          string          `json:"id"`
	Status      string          `json:"status"`
	RecipientID string          `json:"recipient_id"`
	Timestamp   string          `json:"timestamp"`
	Errors      json.RawMessage `json:"errors,omitempty"`
}

type inboundMessageData struct {
	WAMID     string          `json:"wamid"`
	From      string          `json:"from"`
	Timestamp int64           `json:"timestamp"`
	Type      string          `json:"type"`
	Raw       json.RawMessage `json:"raw"`
}

type messageStatusData struct {
	WAMID     string          `json:"wamid"`
	Status    string          `json:"status"`
	Recipient string          `json:"recipient"`
	Timestamp int64           `json:"timestamp"`
	Errors    json.RawMessage `json:"errors,omitempty"`
}

func ParseWebhook(body []byte) ([]core.Inbound, error) {
	var payload webhookPayload
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("decode webhook payload: %w", err)
	}
	if payload.Object != businessAccountObject {
		return nil, nil
	}

	var items []core.Inbound
	for _, entry := range payload.Entry {
		for _, change := range entry.Changes {
			if change.Field != "messages" {
				continue
			}
			parsed, err := parseChange(entry.ID, change.Value)
			if err != nil {
				return nil, err
			}
			items = append(items, parsed...)
		}
	}
	return items, nil
}

func parseChange(wabaID string, value changeValue) ([]core.Inbound, error) {
	phoneNumberID := value.Metadata.PhoneNumberID
	items := make([]core.Inbound, 0, len(value.Messages)+len(value.Statuses))

	for _, raw := range value.Messages {
		item, err := parseMessage(wabaID, phoneNumberID, raw)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	for _, raw := range value.Statuses {
		item, err := parseStatus(wabaID, phoneNumberID, raw)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, nil
}

func parseMessage(wabaID, phoneNumberID string, raw json.RawMessage) (core.Inbound, error) {
	var fields messageFields
	if err := json.Unmarshal(raw, &fields); err != nil {
		return core.Inbound{}, fmt.Errorf("decode message: %w", err)
	}
	if fields.ID == "" {
		return core.Inbound{}, fmt.Errorf("decode message: missing id")
	}

	seconds := parseUnixSeconds(fields.Timestamp)
	data, err := json.Marshal(inboundMessageData{
		WAMID:     fields.ID,
		From:      fields.From,
		Timestamp: seconds,
		Type:      fields.Type,
		Raw:       raw,
	})
	if err != nil {
		return core.Inbound{}, fmt.Errorf("encode message data: %w", err)
	}

	return core.Inbound{
		Type:          core.EventMessageInbound,
		MessageID:     fields.ID,
		Subtype:       fields.Type,
		DedupKey:      "msg:" + fields.ID,
		PartitionKey:  fields.From,
		PhoneNumberID: phoneNumberID,
		WABAID:        wabaID,
		OccurredAt:    unixToTime(seconds),
		Data:          data,
	}, nil
}

func parseStatus(wabaID, phoneNumberID string, raw json.RawMessage) (core.Inbound, error) {
	var fields statusFields
	if err := json.Unmarshal(raw, &fields); err != nil {
		return core.Inbound{}, fmt.Errorf("decode status: %w", err)
	}
	if fields.ID == "" || fields.Status == "" {
		return core.Inbound{}, fmt.Errorf("decode status: missing id or status")
	}

	seconds := parseUnixSeconds(fields.Timestamp)
	data, err := json.Marshal(messageStatusData{
		WAMID:     fields.ID,
		Status:    fields.Status,
		Recipient: fields.RecipientID,
		Timestamp: seconds,
		Errors:    fields.Errors,
	})
	if err != nil {
		return core.Inbound{}, fmt.Errorf("encode status data: %w", err)
	}

	return core.Inbound{
		Type:          core.EventMessageStatus,
		MessageID:     fields.ID,
		Subtype:       fields.Status,
		DedupKey:      "status:" + fields.ID + ":" + fields.Status,
		PartitionKey:  fields.RecipientID,
		PhoneNumberID: phoneNumberID,
		WABAID:        wabaID,
		OccurredAt:    unixToTime(seconds),
		Data:          data,
	}, nil
}

func parseUnixSeconds(value string) int64 {
	seconds, err := strconv.ParseInt(value, 10, 64)
	if err != nil || seconds < 0 {
		return 0
	}
	return seconds
}

func unixToTime(seconds int64) time.Time {
	if seconds == 0 {
		return time.Time{}
	}
	return time.Unix(seconds, 0).UTC()
}
