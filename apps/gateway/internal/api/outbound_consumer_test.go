package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/core"
)

type fakeBrokerMessage struct {
	id     string
	data   string
	acks   int
	nacks  []time.Duration
	ackErr error
}

func (f *fakeBrokerMessage) ID() string   { return f.id }
func (f *fakeBrokerMessage) Data() []byte { return []byte(f.data) }

func (f *fakeBrokerMessage) Ack() error {
	f.acks++
	return f.ackErr
}

func (f *fakeBrokerMessage) Nack(delay time.Duration) error {
	f.nacks = append(f.nacks, delay)
	return nil
}

type rejection struct {
	message core.OutboundMessage
	cause   error
}

type fakeQueue struct {
	enqueued   []core.OutboundMessage
	enqueueErr error
	rejected   []rejection
	rejectErr  error
}

func (f *fakeQueue) Enqueue(_ context.Context, message core.OutboundMessage) (string, error) {
	f.enqueued = append(f.enqueued, message)
	return "msg_1", f.enqueueErr
}

func (f *fakeQueue) Reject(_ context.Context, message core.OutboundMessage, cause error) error {
	f.rejected = append(f.rejected, rejection{message, cause})
	return f.rejectErr
}

type fakeConsumeRecorder struct {
	results []string
}

func (f *fakeConsumeRecorder) OutboundConsumed(result string) {
	f.results = append(f.results, result)
}

func TestOutboundConsumerHandle(t *testing.T) {
	const text = `{"to":"94770000000","type":"text","text":{"body":"hi"},"correlation_id":"order-1"}`
	storeDown := errors.New("disk full")
	invalid := &core.RequestError{Code: core.CodeInvalidRequest, Message: "to is wrong"}

	tests := []struct {
		name         string
		id           string
		data         string
		ackErr       error
		enqueueErr   error
		rejectErr    error
		wantEnqueued int
		wantKey      string
		wantRejected int
		wantRejCode  string
		wantAcks     int
		wantNacks    int
		wantRecorded string
	}{
		{name: "valid request is queued under a key derived from the broker id", id: "nats:7", data: text, wantEnqueued: 1, wantKey: "broker:nats:7", wantAcks: 1, wantRecorded: "queued"},
		{
			name: "idempotency key in the body is honored and is not an unknown field", id: "nats:8",
			data:         `{"to":"94770000000","type":"text","text":{"body":"hi"},"idempotency_key":"order-1-confirm"}`,
			wantEnqueued: 1, wantKey: "order-1-confirm", wantAcks: 1, wantRecorded: "queued",
		},
		{name: "without a broker id no key is invented", id: "", data: text, wantEnqueued: 1, wantKey: "", wantAcks: 1, wantRecorded: "queued"},
		{name: "ack failure does not change the outcome", id: "nats:9", data: text, ackErr: errors.New("connection lost"), wantEnqueued: 1, wantKey: "broker:nats:9", wantAcks: 1, wantRecorded: "queued"},
		{name: "invalid json is rejected and acknowledged", id: "nats:1", data: `{not json`, wantRejected: 1, wantRejCode: core.CodeInvalidRequest, wantAcks: 1, wantRecorded: "rejected"},
		{name: "unknown field is rejected", id: "nats:1", data: `{"to":"94770000000","type":"text","text":{"body":"hi"},"bogus":1}`, wantRejected: 1, wantRejCode: core.CodeInvalidRequest, wantAcks: 1, wantRecorded: "rejected"},
		{name: "missing type is rejected", id: "nats:1", data: `{"to":"94770000000"}`, wantRejected: 1, wantRejCode: core.CodeInvalidRequest, wantAcks: 1, wantRecorded: "rejected"},
		{name: "idempotency key of the wrong type is rejected", id: "nats:1", data: `{"to":"94770000000","type":"text","text":{"body":"hi"},"idempotency_key":5}`, wantRejected: 1, wantRejCode: core.CodeInvalidRequest, wantAcks: 1, wantRecorded: "rejected"},
		{name: "idempotency key with a space is rejected", id: "nats:1", data: `{"to":"94770000000","type":"text","text":{"body":"hi"},"idempotency_key":"a b"}`, wantRejected: 1, wantRejCode: core.CodeInvalidRequest, wantAcks: 1, wantRecorded: "rejected"},
		{name: "idempotency key that is too long is rejected", id: "nats:1", data: `{"to":"94770000000","type":"text","text":{"body":"hi"},"idempotency_key":"` + strings.Repeat("k", 256) + `"}`, wantRejected: 1, wantRejCode: core.CodeInvalidRequest, wantAcks: 1, wantRecorded: "rejected"},
		{name: "message the sender refuses is rejected", id: "nats:2", data: text, enqueueErr: invalid, wantEnqueued: 1, wantKey: "broker:nats:2", wantRejected: 1, wantRejCode: core.CodeInvalidRequest, wantAcks: 1, wantRecorded: "rejected"},
		{name: "store failure is retried later", id: "nats:3", data: text, enqueueErr: storeDown, wantEnqueued: 1, wantKey: "broker:nats:3", wantNacks: 1, wantRecorded: "retry"},
		{name: "failure to record a rejection is retried later", id: "nats:4", data: `{not json`, rejectErr: storeDown, wantRejected: 1, wantRejCode: core.CodeInvalidRequest, wantNacks: 1, wantRecorded: "retry"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			queue := &fakeQueue{enqueueErr: tt.enqueueErr, rejectErr: tt.rejectErr}
			recorder := &fakeConsumeRecorder{}
			received := &fakeBrokerMessage{id: tt.id, data: tt.data, ackErr: tt.ackErr}
			consumer := NewOutboundConsumer(queue, recorder, slog.New(slog.NewTextHandler(io.Discard, nil)))

			consumer.Handle(context.Background(), received)

			if len(queue.enqueued) != tt.wantEnqueued || len(queue.rejected) != tt.wantRejected {
				t.Fatalf("enqueued = %d, rejected = %d, want %d and %d", len(queue.enqueued), len(queue.rejected), tt.wantEnqueued, tt.wantRejected)
			}
			if received.acks != tt.wantAcks || len(received.nacks) != tt.wantNacks {
				t.Errorf("acks = %d, nacks = %v, want %d acks and %d nacks", received.acks, received.nacks, tt.wantAcks, tt.wantNacks)
			}
			for _, delay := range received.nacks {
				if delay != consumeRetryDelay {
					t.Errorf("nack delay = %v, want %v", delay, consumeRetryDelay)
				}
			}
			if len(recorder.results) != 1 || recorder.results[0] != tt.wantRecorded {
				t.Errorf("recorded = %v, want [%s]", recorder.results, tt.wantRecorded)
			}
			if tt.wantEnqueued == 1 {
				queued := queue.enqueued[0]
				if queued.IdempotencyKey != tt.wantKey || queued.To != "94770000000" {
					t.Errorf("queued message = %+v, want key %q", queued, tt.wantKey)
				}
			}
			if tt.wantRejected == 1 {
				var requestErr *core.RequestError
				if !errors.As(queue.rejected[0].cause, &requestErr) || requestErr.Code != tt.wantRejCode {
					t.Errorf("rejection cause = %#v, want code %s", queue.rejected[0].cause, tt.wantRejCode)
				}
			}
		})
	}
}

func TestOutboundConsumerKeepsTheCorrelationIDOnRejection(t *testing.T) {
	tests := []struct {
		name            string
		data            string
		wantCorrelation string
	}{
		{name: "bad request that still names its correlation id", data: `{"to":"94770000000","type":"text","text":{"body":"hi"},"bogus":1,"correlation_id":"order-9"}`, wantCorrelation: "order-9"},
		{name: "unreadable request has none", data: `{nope`, wantCorrelation: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			queue := &fakeQueue{}
			consumer := NewOutboundConsumer(queue, &fakeConsumeRecorder{}, slog.New(slog.NewTextHandler(io.Discard, nil)))

			consumer.Handle(context.Background(), &fakeBrokerMessage{id: "nats:1", data: tt.data})

			if len(queue.rejected) != 1 || queue.rejected[0].message.CorrelationID != tt.wantCorrelation {
				t.Fatalf("rejected = %+v, want correlation %q", queue.rejected, tt.wantCorrelation)
			}
		})
	}
}

func TestSplitIdempotencyKey(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantKey     string
		wantProblem string
		wantRest    bool
	}{
		{name: "no key leaves the body alone", body: `{"to":"1"}`, wantRest: true},
		{name: "key is removed from the body", body: `{"to":"1","idempotency_key":"abc"}`, wantKey: "abc", wantRest: true},
		{name: "empty key means none", body: `{"to":"1","idempotency_key":""}`, wantRest: true},
		{name: "not an object is left for the parser to report", body: `[1]`, wantRest: true},
		{name: "not json is left for the parser to report", body: `nope`, wantRest: true},
		{name: "key of the wrong type", body: `{"idempotency_key":[]}`, wantProblem: "idempotency_key must be a string"},
		{name: "key with a control character", body: `{"idempotency_key":"a\tb"}`, wantProblem: "idempotency_key must use printable ASCII"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rest, key, problem := splitIdempotencyKey([]byte(tt.body))

			if key != tt.wantKey || !strings.HasPrefix(problem, tt.wantProblem) || (problem == "") != (tt.wantProblem == "") {
				t.Fatalf("key = %q, problem = %q, want %q and %q", key, problem, tt.wantKey, tt.wantProblem)
			}
			if (rest != nil) != tt.wantRest {
				t.Errorf("rest = %q, want present=%v", rest, tt.wantRest)
			}
			if tt.wantRest && strings.Contains(string(rest), "idempotency_key") && tt.wantKey != "" {
				t.Errorf("the key was not removed from %s", rest)
			}
		})
	}
}
