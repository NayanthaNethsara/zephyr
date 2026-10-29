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

type fakeMessenger struct {
	sentTo        string
	sentNumber    string
	result        SendResult
	err           error
	calls         int
	sentContentJS string
}

func (f *fakeMessenger) Send(_ context.Context, phoneNumberID string, message OutboundMessage) (SendResult, error) {
	f.calls++
	f.sentNumber = phoneNumberID
	f.sentTo = message.To
	f.sentContentJS = string(message.Content)
	return f.result, f.err
}

func TestValidateOutbound(t *testing.T) {
	tests := []struct {
		name        string
		to          string
		messageType MessageType
		content     string
		wantProblem string
	}{
		{name: "text", to: "94770000000", messageType: MessageText, content: `{"body":"hello"}`},
		{name: "text with preview flag", to: "94770000000", messageType: MessageText, content: `{"body":"see https://example.com","preview_url":true}`},
		{name: "image by id", to: "94770000000", messageType: MessageImage, content: `{"id":"123","caption":"hi"}`},
		{name: "video by link", to: "94770000000", messageType: MessageVideo, content: `{"link":"https://example.com/v.mp4"}`},
		{name: "audio by id", to: "94770000000", messageType: MessageAudio, content: `{"id":"123"}`},
		{name: "document with filename", to: "94770000000", messageType: MessageDocument, content: `{"link":"https://example.com/a.pdf","filename":"a.pdf"}`},
		{name: "sticker by id", to: "94770000000", messageType: MessageSticker, content: `{"id":"123"}`},
		{name: "location", to: "94770000000", messageType: MessageLocation, content: `{"latitude":6.9271,"longitude":79.8612,"name":"Colombo"}`},
		{name: "contacts", to: "94770000000", messageType: MessageContacts, content: `[{"name":{"formatted_name":"Sam","first_name":"Sam"}}]`},
		{name: "interactive buttons", to: "94770000000", messageType: MessageInteractive, content: `{"type":"button","body":{"text":"Pick"},"action":{"buttons":[]}}`},
		{name: "template", to: "94770000000", messageType: MessageTemplate, content: `{"name":"order_confirm","language":{"code":"en_US"},"components":[]}`},
		{name: "reaction", to: "94770000000", messageType: MessageReaction, content: `{"message_id":"wamid.1","emoji":"x"}`},

		{name: "recipient too short", to: "12345", messageType: MessageText, content: `{"body":"hi"}`, wantProblem: "to must be"},
		{name: "recipient too long", to: "1234567890123456", messageType: MessageText, content: `{"body":"hi"}`, wantProblem: "to must be"},
		{name: "recipient with plus sign", to: "+94770000000", messageType: MessageText, content: `{"body":"hi"}`, wantProblem: "to must be"},
		{name: "recipient with non ascii digits", to: "٩٤٧٧٠٠٠٠٠٠٠", messageType: MessageText, content: `{"body":"hi"}`, wantProblem: "to must be"},
		{name: "unsupported type", to: "94770000000", messageType: "poll", content: `{}`, wantProblem: "not supported"},
		{name: "text without body", to: "94770000000", messageType: MessageText, content: `{}`, wantProblem: "text.body is required"},
		{name: "text with blank body", to: "94770000000", messageType: MessageText, content: `{"body":"   "}`, wantProblem: "text.body is required"},
		{name: "text content missing", to: "94770000000", messageType: MessageText, content: ``, wantProblem: "must be a JSON object"},
		{name: "text content not an object", to: "94770000000", messageType: MessageText, content: `"hello"`, wantProblem: "must be a JSON object"},
		{name: "image with neither id nor link", to: "94770000000", messageType: MessageImage, content: `{"caption":"x"}`, wantProblem: "exactly one of id or link"},
		{name: "image with both id and link", to: "94770000000", messageType: MessageImage, content: `{"id":"1","link":"https://example.com/a.png"}`, wantProblem: "exactly one of id or link"},
		{name: "location without longitude", to: "94770000000", messageType: MessageLocation, content: `{"latitude":1}`, wantProblem: "location.longitude must be a number"},
		{name: "location with string coordinates", to: "94770000000", messageType: MessageLocation, content: `{"latitude":"1","longitude":"2"}`, wantProblem: "must be a number"},
		{name: "contacts as object", to: "94770000000", messageType: MessageContacts, content: `{"name":{}}`, wantProblem: "non-empty array"},
		{name: "contacts empty", to: "94770000000", messageType: MessageContacts, content: `[]`, wantProblem: "non-empty array"},
		{name: "contact without name", to: "94770000000", messageType: MessageContacts, content: `[{"phones":[]}]`, wantProblem: "needs a name"},
		{name: "interactive without type", to: "94770000000", messageType: MessageInteractive, content: `{"action":{}}`, wantProblem: "interactive.type is required"},
		{name: "interactive without action", to: "94770000000", messageType: MessageInteractive, content: `{"type":"list"}`, wantProblem: "interactive.action is required"},
		{name: "template without name", to: "94770000000", messageType: MessageTemplate, content: `{"language":{"code":"en"}}`, wantProblem: "template.name is required"},
		{name: "template with string language", to: "94770000000", messageType: MessageTemplate, content: `{"name":"x","language":"en"}`, wantProblem: "template.language.code is required"},
		{name: "template without language", to: "94770000000", messageType: MessageTemplate, content: `{"name":"x"}`, wantProblem: "template.language.code is required"},
		{name: "reaction without message id", to: "94770000000", messageType: MessageReaction, content: `{"emoji":"x"}`, wantProblem: "reaction.message_id is required"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateOutbound(OutboundMessage{To: tt.to, Type: tt.messageType, Content: json.RawMessage(tt.content)})

			if tt.wantProblem == "" {
				if err != nil {
					t.Fatalf("validateOutbound() error = %v, want none", err)
				}
				return
			}
			var sendErr *SendError
			if !errors.As(err, &sendErr) || sendErr.Code != CodeInvalidRequest || sendErr.IsRetryable {
				t.Fatalf("error = %#v, want non-retryable invalid_request", err)
			}
			if !strings.Contains(sendErr.Message, tt.wantProblem) {
				t.Fatalf("message = %q, want it to contain %q", sendErr.Message, tt.wantProblem)
			}
		})
	}
}

func TestSenderSend(t *testing.T) {
	upstream := &SendError{Code: CodeWindowClosed, Message: "window closed", MetaCode: 131047}

	tests := []struct {
		name          string
		message       OutboundMessage
		messengerErr  error
		wantCalls     int
		wantCode      string
		wantMessageID string
	}{
		{
			name:          "valid message is sent through the configured number",
			message:       OutboundMessage{To: "94770000000", Type: MessageText, Content: json.RawMessage(`{"body":"hi"}`)},
			wantCalls:     1,
			wantMessageID: "wamid.sent",
		},
		{
			name:          "matching phone number id is accepted",
			message:       OutboundMessage{PhoneNumberID: "111", To: "94770000000", Type: MessageText, Content: json.RawMessage(`{"body":"hi"}`)},
			wantCalls:     1,
			wantMessageID: "wamid.sent",
		},
		{
			name:      "different phone number id is rejected before sending",
			message:   OutboundMessage{PhoneNumberID: "999", To: "94770000000", Type: MessageText, Content: json.RawMessage(`{"body":"hi"}`)},
			wantCalls: 0,
			wantCode:  CodeInvalidRequest,
		},
		{
			name:      "invalid content is rejected before sending",
			message:   OutboundMessage{To: "94770000000", Type: MessageText, Content: json.RawMessage(`{}`)},
			wantCalls: 0,
			wantCode:  CodeInvalidRequest,
		},
		{
			name:         "upstream error is returned unchanged",
			message:      OutboundMessage{To: "94770000000", Type: MessageText, Content: json.RawMessage(`{"body":"hi"}`)},
			messengerErr: upstream,
			wantCalls:    1,
			wantCode:     CodeWindowClosed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			messenger := &fakeMessenger{result: SendResult{MessageID: "wamid.sent"}, err: tt.messengerErr}
			sender := NewSender(messenger, &fakeIdempotencyStore{}, "111", 24*time.Hour)

			result, err := sender.Send(context.Background(), tt.message)

			if messenger.calls != tt.wantCalls {
				t.Fatalf("messenger calls = %d, want %d", messenger.calls, tt.wantCalls)
			}
			if tt.wantCode == "" {
				if err != nil || result.MessageID != tt.wantMessageID {
					t.Fatalf("Send() = %+v, %v, want %s", result, err, tt.wantMessageID)
				}
				if messenger.sentNumber != "111" || messenger.sentTo != "94770000000" {
					t.Errorf("sent via %q to %q", messenger.sentNumber, messenger.sentTo)
				}
				if messenger.sentContentJS != string(tt.message.Content) {
					t.Errorf("content = %s, want it passed through unchanged", messenger.sentContentJS)
				}
				return
			}
			var sendErr *SendError
			if !errors.As(err, &sendErr) || sendErr.Code != tt.wantCode {
				t.Fatalf("error = %#v, want code %s", err, tt.wantCode)
			}
		})
	}
}

type fakeIdempotencyStore struct {
	outcome      store.IdempotencyOutcome
	storedID     string
	claimErr     error
	completeErr  error
	claims       []store.IdempotencyClaim
	completed    []string
	released     []string
	settleErrors []error
}

func (f *fakeIdempotencyStore) ClaimIdempotencyKey(_ context.Context, claim store.IdempotencyClaim) (store.IdempotencyResult, error) {
	f.claims = append(f.claims, claim)
	if f.claimErr != nil {
		return store.IdempotencyResult{}, f.claimErr
	}
	outcome := f.outcome
	if outcome == "" {
		outcome = store.IdempotencyAcquired
	}
	return store.IdempotencyResult{Outcome: outcome, MessageID: f.storedID}, nil
}

func (f *fakeIdempotencyStore) CompleteIdempotencyKey(ctx context.Context, key, messageID string) error {
	f.settleErrors = append(f.settleErrors, ctx.Err())
	f.completed = append(f.completed, key+"="+messageID)
	return f.completeErr
}

func (f *fakeIdempotencyStore) ReleaseIdempotencyKey(ctx context.Context, key string) error {
	f.settleErrors = append(f.settleErrors, ctx.Err())
	f.released = append(f.released, key)
	return nil
}

func TestSenderIdempotency(t *testing.T) {
	textMessage := func(key string) OutboundMessage {
		return OutboundMessage{To: "94770000000", Type: MessageText, Content: json.RawMessage(`{"body":"hi"}`), IdempotencyKey: key}
	}
	upstream := &SendError{Code: CodeUpstreamTimeout, Message: "slow", IsRetryable: true}

	tests := []struct {
		name              string
		message           OutboundMessage
		outcome           store.IdempotencyOutcome
		storedID          string
		messengerErr      error
		claimErr          error
		completeErr       error
		cancelBefore      bool
		wantClaims        int
		wantMessengerHits int
		wantCompleted     []string
		wantReleased      []string
		wantCode          string
		wantMessageID     string
		wantReplay        bool
		wantUnrecorded    bool
		wantPlainError    bool
	}{
		{
			name:       "no key skips the store entirely",
			message:    textMessage(""),
			wantClaims: 0, wantMessengerHits: 1, wantMessageID: "wamid.sent",
		},
		{
			name:       "acquired key sends and records the message id",
			message:    textMessage("k1"),
			wantClaims: 1, wantMessengerHits: 1, wantCompleted: []string{"k1=wamid.sent"}, wantMessageID: "wamid.sent",
		},
		{
			name:         "failed send releases the key so a retry can resend",
			message:      textMessage("k1"),
			messengerErr: upstream,
			wantClaims:   1, wantMessengerHits: 1, wantReleased: []string{"k1"}, wantCode: CodeUpstreamTimeout,
		},
		{
			name:    "completed key replays without calling meta",
			message: textMessage("k1"),
			outcome: store.IdempotencyCompleted, storedID: "wamid.earlier",
			wantClaims: 1, wantMessengerHits: 0, wantMessageID: "wamid.earlier", wantReplay: true,
		},
		{
			name:       "in flight key is a retryable conflict",
			message:    textMessage("k1"),
			outcome:    store.IdempotencyInProgress,
			wantClaims: 1, wantMessengerHits: 0, wantCode: CodeRequestInProgress,
		},
		{
			name:       "reused key with a different message is rejected",
			message:    textMessage("k1"),
			outcome:    store.IdempotencyMismatch,
			wantClaims: 1, wantMessengerHits: 0, wantCode: CodeIdempotencyKeyReused,
		},
		{
			name:       "store failure stops the send so it cannot duplicate",
			message:    textMessage("k1"),
			claimErr:   errors.New("database is locked"),
			wantClaims: 1, wantMessengerHits: 0, wantPlainError: true,
		},
		{
			name:        "failure to record the key still returns the sent message",
			message:     textMessage("k1"),
			completeErr: errors.New("disk full"),
			wantClaims:  1, wantMessengerHits: 1, wantCompleted: []string{"k1=wamid.sent"}, wantMessageID: "wamid.sent", wantUnrecorded: true,
		},
		{
			name:         "key is settled even when the caller has gone away",
			message:      textMessage("k1"),
			cancelBefore: true,
			wantClaims:   1, wantMessengerHits: 1, wantCompleted: []string{"k1=wamid.sent"}, wantMessageID: "wamid.sent",
		},
		{
			name:       "invalid message is rejected before the store is touched",
			message:    OutboundMessage{To: "12", Type: MessageText, Content: json.RawMessage(`{"body":"hi"}`), IdempotencyKey: "k1"},
			wantClaims: 0, wantMessengerHits: 0, wantCode: CodeInvalidRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			messenger := &fakeMessenger{result: SendResult{MessageID: "wamid.sent"}, err: tt.messengerErr}
			idempotency := &fakeIdempotencyStore{outcome: tt.outcome, storedID: tt.storedID, claimErr: tt.claimErr, completeErr: tt.completeErr}
			sender := NewSender(messenger, idempotency, "111", 24*time.Hour)

			ctx := context.Background()
			if tt.cancelBefore {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}

			result, err := sender.Send(ctx, tt.message)

			if len(idempotency.claims) != tt.wantClaims || messenger.calls != tt.wantMessengerHits {
				t.Fatalf("claims = %d, messenger calls = %d, want %d and %d", len(idempotency.claims), messenger.calls, tt.wantClaims, tt.wantMessengerHits)
			}
			if strings.Join(idempotency.completed, ",") != strings.Join(tt.wantCompleted, ",") || strings.Join(idempotency.released, ",") != strings.Join(tt.wantReleased, ",") {
				t.Errorf("completed = %v, released = %v, want %v and %v", idempotency.completed, idempotency.released, tt.wantCompleted, tt.wantReleased)
			}
			for _, settleErr := range idempotency.settleErrors {
				if settleErr != nil {
					t.Errorf("settling used a canceled context: %v", settleErr)
				}
			}

			switch {
			case tt.wantPlainError:
				var sendErr *SendError
				if err == nil || errors.As(err, &sendErr) {
					t.Fatalf("error = %#v, want a plain internal error", err)
				}
			case tt.wantCode != "":
				var sendErr *SendError
				if !errors.As(err, &sendErr) || sendErr.Code != tt.wantCode {
					t.Fatalf("error = %#v, want code %s", err, tt.wantCode)
				}
				if tt.wantCode == CodeRequestInProgress && !sendErr.IsRetryable {
					t.Error("request_in_progress must be retryable")
				}
			default:
				if err != nil || result.MessageID != tt.wantMessageID || result.IsReplay != tt.wantReplay || result.IsKeyUnrecorded != tt.wantUnrecorded {
					t.Fatalf("Send() = %+v, %v", result, err)
				}
			}
		})
	}
}

func TestFingerprint(t *testing.T) {
	base := OutboundMessage{To: "94770000000", Type: MessageText, Content: json.RawMessage(`{"body":"hi"}`)}
	spaced := base
	spaced.Content = json.RawMessage("{ \"body\" :  \"hi\" }\n")
	withNoise := base
	withNoise.CorrelationID = "other"
	withNoise.PhoneNumberID = "111"
	withNoise.IdempotencyKey = "k"
	otherText := base
	otherText.Content = json.RawMessage(`{"body":"bye"}`)
	otherRecipient := base
	otherRecipient.To = "94771111111"
	otherType := base
	otherType.Type = MessageTemplate

	tests := []struct {
		name      string
		other     OutboundMessage
		wantEqual bool
	}{
		{name: "whitespace differences are ignored", other: spaced, wantEqual: true},
		{name: "correlation id, number id and key are not part of the identity", other: withNoise, wantEqual: true},
		{name: "different content", other: otherText, wantEqual: false},
		{name: "different recipient", other: otherRecipient, wantEqual: false},
		{name: "different type", other: otherType, wantEqual: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := fingerprint(base) == fingerprint(tt.other); got != tt.wantEqual {
				t.Fatalf("fingerprints equal = %v, want %v", got, tt.wantEqual)
			}
		})
	}
}
