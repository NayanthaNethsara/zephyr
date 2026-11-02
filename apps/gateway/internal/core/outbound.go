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
	PhoneNumberID  string          `json:"phone_number_id,omitempty"`
	To             string          `json:"to"`
	Type           MessageType     `json:"type"`
	Content        json.RawMessage `json:"content"`
	CorrelationID  string          `json:"correlation_id,omitempty"`
	IdempotencyKey string          `json:"idempotency_key,omitempty"`
}

type SendResult struct {
	MessageID       string
	IsReplay        bool
	IsKeyUnrecorded bool
}

type Messenger interface {
	Send(ctx context.Context, phoneNumberID string, message OutboundMessage) (SendResult, error)
}

type SessionChecker interface {
	Check(ctx context.Context, phone string) (Session, error)
}

type SenderDeps struct {
	Messenger   Messenger
	Idempotency store.IdempotencyStore
	Sessions    SessionChecker
	Limiter     RateLimiter
}

type Sender struct {
	messenger      Messenger
	idempotency    store.IdempotencyStore
	sessions       SessionChecker
	limiter        RateLimiter
	phoneNumberID  string
	idempotencyTTL time.Duration
	now            func() time.Time
}

func NewSender(deps SenderDeps, phoneNumberID string, idempotencyTTL time.Duration) *Sender {
	return &Sender{
		messenger:      deps.Messenger,
		idempotency:    deps.Idempotency,
		sessions:       deps.Sessions,
		limiter:        deps.Limiter,
		phoneNumberID:  phoneNumberID,
		idempotencyTTL: idempotencyTTL,
		now:            time.Now,
	}
}

func (s *Sender) Validate(message OutboundMessage) error {
	if message.PhoneNumberID != "" && message.PhoneNumberID != s.phoneNumberID {
		return invalidRequest("phone_number_id does not match the configured number")
	}
	return validateOutbound(message)
}

func (s *Sender) Send(ctx context.Context, message OutboundMessage) (SendResult, error) {
	if err := s.Validate(message); err != nil {
		return SendResult{}, err
	}
	if message.IdempotencyKey == "" {
		return s.deliver(ctx, message)
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
		return SendResult{}, &RequestError{
			Code:        CodeRequestInProgress,
			Message:     "a request with this idempotency key is still being processed",
			IsRetryable: true,
		}
	case store.IdempotencyMismatch:
		return SendResult{}, &RequestError{
			Code:    CodeIdempotencyKeyReused,
			Message: "this idempotency key was already used with a different message",
		}
	}

	return s.deliverAndSettle(ctx, message)
}

func (s *Sender) deliverAndSettle(ctx context.Context, message OutboundMessage) (SendResult, error) {
	result, sendErr := s.deliver(ctx, message)

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

func (s *Sender) deliver(ctx context.Context, message OutboundMessage) (SendResult, error) {
	if err := s.requireOpenWindow(ctx, message); err != nil {
		return SendResult{}, err
	}
	if err := s.limiter.Wait(ctx, s.phoneNumberID); err != nil {
		return SendResult{}, err
	}
	return s.messenger.Send(ctx, s.phoneNumberID, message)
}

func (s *Sender) requireOpenWindow(ctx context.Context, message OutboundMessage) error {
	if message.Type == MessageTemplate {
		return nil
	}
	session, err := s.sessions.Check(ctx, message.To)
	if err != nil {
		return err
	}
	if session.State == SessionClosed {
		return &RequestError{
			Code:    CodeWindowClosed,
			Message: "the 24-hour customer service window is closed, send an approved template message instead",
		}
	}
	return nil
}

func invalidRequest(message string) *RequestError {
	return &RequestError{Code: CodeInvalidRequest, Message: message}
}
