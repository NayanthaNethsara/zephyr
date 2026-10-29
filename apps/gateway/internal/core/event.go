package core

import (
	"encoding/json"
	"time"
)

type EventType string

const (
	EventMessageInbound EventType = "message.inbound"
	EventMessageStatus  EventType = "message.status"
)

type Inbound struct {
	Type          EventType
	MessageID     string
	Subtype       string
	DedupKey      string
	PartitionKey  string
	PhoneNumberID string
	WABAID        string
	OccurredAt    time.Time
	Data          json.RawMessage
}

type IngestResult struct {
	EventID     string
	MessageID   string
	Type        EventType
	Subtype     string
	MaskedPhone string
	IsQueued    bool
}

type Envelope struct {
	ID            string          `json:"id"`
	Type          EventType       `json:"type"`
	OccurredAt    time.Time       `json:"occurred_at"`
	PhoneNumberID string          `json:"phone_number_id"`
	WABAID        string          `json:"waba_id"`
	Data          json.RawMessage `json:"data"`
}

func (t EventType) topicSuffix() string {
	switch t {
	case EventMessageStatus:
		return "status"
	default:
		return "inbound"
	}
}
