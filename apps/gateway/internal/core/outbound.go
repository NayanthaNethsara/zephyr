package core

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/store"
)

const (
	inFlightLease = time.Minute
	settleTimeout = 5 * time.Second
)

type MessageType string

const (
	MessageText        MessageType = "text"
	MessageImage       MessageType = "image"
	MessageVideo       MessageType = "video"
	MessageAudio       MessageType = "audio"
	MessageDocument    MessageType = "document"
	MessageSticker     MessageType = "sticker"
	MessageLocation    MessageType = "location"
	MessageContacts    MessageType = "contacts"
	MessageInteractive MessageType = "interactive"
	MessageTemplate    MessageType = "template"
	MessageReaction    MessageType = "reaction"
)

func (t MessageType) IsSupported() bool {
	switch t {
	case MessageText, MessageImage, MessageVideo, MessageAudio, MessageDocument, MessageSticker,
		MessageLocation, MessageContacts, MessageInteractive, MessageTemplate, MessageReaction:
		return true
	}
	return false
}

type OutboundMessage struct {
	PhoneNumberID  string
	To             string
	Type           MessageType
	Content        json.RawMessage
	CorrelationID  string
	IdempotencyKey string
}

type SendResult struct {
	MessageID       string
	IsReplay        bool
	IsKeyUnrecorded bool
}

type Messenger interface {
	Send(ctx context.Context, phoneNumberID string, message OutboundMessage) (SendResult, error)
}

type Sender struct {
	messenger      Messenger
	idempotency    store.IdempotencyStore
	phoneNumberID  string
	idempotencyTTL time.Duration
	now            func() time.Time
}

func NewSender(messenger Messenger, idempotency store.IdempotencyStore, phoneNumberID string, idempotencyTTL time.Duration) *Sender {
	return &Sender{
		messenger:      messenger,
		idempotency:    idempotency,
		phoneNumberID:  phoneNumberID,
		idempotencyTTL: idempotencyTTL,
		now:            time.Now,
	}
}

func (s *Sender) Send(ctx context.Context, message OutboundMessage) (SendResult, error) {
	if message.PhoneNumberID != "" && message.PhoneNumberID != s.phoneNumberID {
		return SendResult{}, invalidRequest("phone_number_id does not match the configured number")
	}
	if err := validateOutbound(message); err != nil {
		return SendResult{}, err
	}
	if message.IdempotencyKey == "" {
		return s.messenger.Send(ctx, s.phoneNumberID, message)
	}
	return s.sendOnce(ctx, message)
}

func (s *Sender) sendOnce(ctx context.Context, message OutboundMessage) (SendResult, error) {
	now := s.now()
	claimed, err := s.idempotency.ClaimIdempotencyKey(ctx, store.IdempotencyClaim{
		Key:         message.IdempotencyKey,
		Fingerprint: fingerprint(message),
		Now:         now,
		LeaseUntil:  now.Add(inFlightLease),
		ExpiresAt:   now.Add(s.idempotencyTTL),
	})
	if err != nil {
		return SendResult{}, fmt.Errorf("claim idempotency key: %w", err)
	}

	switch claimed.Outcome {
	case store.IdempotencyCompleted:
		return SendResult{MessageID: claimed.MessageID, IsReplay: true}, nil
	case store.IdempotencyInProgress:
		return SendResult{}, &SendError{
			Code:        CodeRequestInProgress,
			Message:     "a request with this idempotency key is still being processed",
			IsRetryable: true,
		}
	case store.IdempotencyMismatch:
		return SendResult{}, &SendError{
			Code:    CodeIdempotencyKeyReused,
			Message: "this idempotency key was already used with a different message",
		}
	}

	return s.deliverAndSettle(ctx, message)
}

func (s *Sender) deliverAndSettle(ctx context.Context, message OutboundMessage) (SendResult, error) {
	result, sendErr := s.messenger.Send(ctx, s.phoneNumberID, message)

	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), settleTimeout)
	defer cancel()

	if sendErr != nil {
		_ = s.idempotency.ReleaseIdempotencyKey(settleCtx, message.IdempotencyKey)
		return SendResult{}, sendErr
	}
	if err := s.idempotency.CompleteIdempotencyKey(settleCtx, message.IdempotencyKey, result.MessageID); err != nil {
		result.IsKeyUnrecorded = true
	}
	return result, nil
}

func invalidRequest(message string) *SendError {
	return &SendError{Code: CodeInvalidRequest, Message: message}
}
