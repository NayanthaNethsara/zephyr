package core

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/store"
)

type fakeSendJobs struct {
	enqueued      []store.SendJob
	enqueueErr    error
	rescheduled   []string
	rescheduleAt  time.Time
	rescheduleErr error
	finishedID    string
	finishedTries int
	outcome       store.SendOutcome
	event         store.OutboxEntry
	finishes      int
	finishErr     error
	status        store.SendStatus
	isKnown       bool
	statusErr     error
}

func (f *fakeSendJobs) EnqueueSend(_ context.Context, job store.SendJob) error {
	f.enqueued = append(f.enqueued, job)
	return f.enqueueErr
}

func (f *fakeSendJobs) ListDueSends(context.Context, time.Time, int) ([]store.SendJob, error) {
	return nil, nil
}

func (f *fakeSendJobs) RescheduleSend(_ context.Context, id string, attempts int, nextAttemptAt time.Time) error {
	f.rescheduled = append(f.rescheduled, fmt.Sprintf("%s:%d", id, attempts))
	f.rescheduleAt = nextAttemptAt
	return f.rescheduleErr
}

func (f *fakeSendJobs) FinishSend(_ context.Context, id string, attempts int, outcome store.SendOutcome, event store.OutboxEntry) error {
	f.finishes++
	f.finishedID, f.finishedTries, f.outcome, f.event = id, attempts, outcome, event
	return f.finishErr
}

func (f *fakeSendJobs) SendStatus(context.Context, string) (store.SendStatus, bool, error) {
	return f.status, f.isKnown, f.statusErr
}

func (f *fakeSendJobs) DeleteExpiredSends(context.Context, time.Time) error { return nil }

type fakeAsyncSender struct {
	validateErr error
	result      SendResult
	sendErr     error
	sent        []OutboundMessage
	cancel      context.CancelFunc
}

func (f *fakeAsyncSender) Validate(OutboundMessage) error { return f.validateErr }

func (f *fakeAsyncSender) Send(_ context.Context, message OutboundMessage) (SendResult, error) {
	f.sent = append(f.sent, message)
	if f.cancel != nil {
		f.cancel()
	}
	return f.result, f.sendErr
}

type recordingAsyncRecorder struct {
	results []string
}

func (r *recordingAsyncRecorder) AsyncSendResult(result string) {
	r.results = append(r.results, result)
}

func newTestAsync(sender MessageSender, jobs *fakeSendJobs, recorder *recordingAsyncRecorder, now time.Time) *AsyncSender {
	async := NewAsyncSender(sender, jobs, AsyncSettings{
		PhoneNumberID: "111",
		TopicPrefix:   "wa",
		MaxAttempts:   3,
		RetryDelay:    func(attempts int) time.Duration { return time.Duration(attempts) * time.Minute },
	}, recorder, slog.New(slog.NewTextHandler(io.Discard, nil)))
	async.now = func() time.Time { return now }
	return async
}

func TestAsyncSenderEnqueue(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	text := OutboundMessage{To: "94770000000", Type: MessageText, Content: json.RawMessage(`{"body":"hi"}`), CorrelationID: "order-1"}
	keyed := text
	keyed.IdempotencyKey = "caller-key"
	invalid := &RequestError{Code: CodeInvalidRequest, Message: "bad"}

	tests := []struct {
		name         string
		message      OutboundMessage
		validateErr  error
		enqueueErr   error
		wantEnqueued int
		wantKey      string
		wantCode     string
		wantPlain    bool
	}{
		{name: "message without a key gets its job id as the key", message: text, wantEnqueued: 1},
		{name: "caller key is kept", message: keyed, wantEnqueued: 1, wantKey: "caller-key"},
		{name: "invalid message is never queued", message: text, validateErr: invalid, wantCode: CodeInvalidRequest},
		{name: "store failure is not a request error", message: text, enqueueErr: errors.New("disk full"), wantEnqueued: 1, wantPlain: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jobs := &fakeSendJobs{enqueueErr: tt.enqueueErr}
			async := newTestAsync(&fakeAsyncSender{validateErr: tt.validateErr}, jobs, &recordingAsyncRecorder{}, now)

			id, err := async.Enqueue(context.Background(), tt.message)

			if len(jobs.enqueued) != tt.wantEnqueued {
				t.Fatalf("enqueued = %d, want %d", len(jobs.enqueued), tt.wantEnqueued)
			}
			var requestErr *RequestError
			switch {
			case tt.wantPlain:
				if err == nil || errors.As(err, &requestErr) {
					t.Fatalf("error = %#v, want a plain error", err)
				}
				return
			case tt.wantCode != "":
				if !errors.As(err, &requestErr) || requestErr.Code != tt.wantCode {
					t.Fatalf("error = %#v, want code %s", err, tt.wantCode)
				}
				return
			}

			if err != nil || !strings.HasPrefix(id, "msg_") || len(id) != len("msg_")+32 {
				t.Fatalf("Enqueue() = %q, %v", id, err)
			}
			job := jobs.enqueued[0]
			if job.ID != id || !job.NextAttemptAt.Equal(now) || !job.CreatedAt.Equal(now) {
				t.Errorf("job = %+v", job)
			}
			var stored OutboundMessage
			if err := json.Unmarshal(job.Payload, &stored); err != nil {
				t.Fatal(err)
			}
			wantKey := tt.wantKey
			if wantKey == "" {
				wantKey = id
			}
			if stored.IdempotencyKey != wantKey || stored.To != "94770000000" || stored.Type != MessageText || string(stored.Content) != `{"body":"hi"}` || stored.CorrelationID != "order-1" {
				t.Errorf("stored message = %+v, want key %s", stored, wantKey)
			}
		})
	}
}

func TestAsyncSenderProcess(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	payload := `{"to":"94770000000","type":"text","content":{"body":"hi"},"correlation_id":"order-1","idempotency_key":"msg_1"}`
	windowClosed := &RequestError{Code: CodeWindowClosed, Message: "closed", MetaCode: 131047}
	throttled := &RequestError{Code: CodeRateLimited, Message: "slow", IsRetryable: true}

	tests := []struct {
		name             string
		payload          string
		attempts         int
		sendErr          error
		isCanceledInSend bool
		finishErr        error
		wantSent         int
		wantFinish       bool
		wantState        string
		wantCode         string
		wantRetry        string
		wantRetryAt      time.Time
		wantRecorded     []string
		wantEventData    map[string]any
	}{
		{
			name: "success finishes with the message id", payload: payload, attempts: 0, wantSent: 1, wantFinish: true, wantState: store.SendSent,
			wantRecorded:  []string{"sent"},
			wantEventData: map[string]any{"id": "msg_1", "status": "sent", "wamid": "wamid.OK", "correlation_id": "order-1"},
		},
		{
			name: "retryable failure is rescheduled with backoff", payload: payload, attempts: 0, sendErr: throttled, wantSent: 1,
			wantRetry: "msg_1:1", wantRetryAt: now.Add(time.Minute), wantRecorded: []string{"retry"},
		},
		{
			name: "second retryable failure backs off further", payload: payload, attempts: 1, sendErr: throttled, wantSent: 1,
			wantRetry: "msg_1:2", wantRetryAt: now.Add(2 * time.Minute), wantRecorded: []string{"retry"},
		},
		{
			name: "retryable failure on the last attempt becomes final", payload: payload, attempts: 2, sendErr: throttled, wantSent: 1, wantFinish: true, wantState: store.SendFailed, wantCode: CodeRateLimited,
			wantRecorded:  []string{"failed"},
			wantEventData: map[string]any{"id": "msg_1", "status": "failed", "correlation_id": "order-1"},
		},
		{
			name: "permanent failure is final at once", payload: payload, attempts: 0, sendErr: windowClosed, wantSent: 1, wantFinish: true, wantState: store.SendFailed, wantCode: CodeWindowClosed,
			wantRecorded:  []string{"failed"},
			wantEventData: map[string]any{"id": "msg_1", "status": "failed", "correlation_id": "order-1"},
		},
		{
			name: "unexpected error is retried", payload: payload, attempts: 0, sendErr: errors.New("boom"), wantSent: 1,
			wantRetry: "msg_1:1", wantRetryAt: now.Add(time.Minute), wantRecorded: []string{"retry"},
		},
		{
			name: "unexpected error on the last attempt is reported without detail", payload: payload, attempts: 2, sendErr: errors.New("secret internal detail"), wantSent: 1, wantFinish: true, wantState: store.SendFailed, wantCode: "internal_error",
			wantRecorded: []string{"failed"},
		},
		{
			name: "unreadable payload fails without sending", payload: `not json`, attempts: 0, wantSent: 0, wantFinish: true, wantState: store.SendFailed, wantCode: CodeInvalidRequest,
			wantRecorded: []string{"failed"},
		},
		{
			name: "shutdown during the send leaves the job untouched", payload: payload, attempts: 0, sendErr: context.Canceled, isCanceledInSend: true, wantSent: 1,
		},
		{
			name: "result that cannot be recorded is not counted", payload: payload, attempts: 0, finishErr: errors.New("disk full"), wantSent: 1, wantFinish: true, wantState: store.SendSent,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			sender := &fakeAsyncSender{result: SendResult{MessageID: "wamid.OK"}, sendErr: tt.sendErr}
			if tt.isCanceledInSend {
				sender.cancel = cancel
			}
			jobs := &fakeSendJobs{finishErr: tt.finishErr}
			recorder := &recordingAsyncRecorder{}
			async := newTestAsync(sender, jobs, recorder, now)

			async.Process(ctx, store.SendJob{ID: "msg_1", Payload: []byte(tt.payload), Attempts: tt.attempts})

			if len(sender.sent) != tt.wantSent {
				t.Fatalf("sends = %d, want %d", len(sender.sent), tt.wantSent)
			}
			if (jobs.finishes > 0) != tt.wantFinish {
				t.Fatalf("finishes = %d, want finish=%v", jobs.finishes, tt.wantFinish)
			}
			if tt.wantRetry != "" {
				if len(jobs.rescheduled) != 1 || jobs.rescheduled[0] != tt.wantRetry || !jobs.rescheduleAt.Equal(tt.wantRetryAt) {
					t.Errorf("rescheduled %v at %v, want %s at %v", jobs.rescheduled, jobs.rescheduleAt, tt.wantRetry, tt.wantRetryAt)
				}
			} else if len(jobs.rescheduled) != 0 {
				t.Errorf("unexpected reschedule %v", jobs.rescheduled)
			}
			if strings.Join(recorder.results, ",") != strings.Join(tt.wantRecorded, ",") {
				t.Errorf("recorded %v, want %v", recorder.results, tt.wantRecorded)
			}
			if !tt.wantFinish {
				return
			}

			if jobs.finishedID != "msg_1" || jobs.finishedTries != tt.attempts+1 || jobs.outcome.State != tt.wantState || jobs.outcome.ErrorCode != tt.wantCode {
				t.Errorf("finished %s after %d attempts with %+v, want state %s code %q", jobs.finishedID, jobs.finishedTries, jobs.outcome, tt.wantState, tt.wantCode)
			}
			if strings.Contains(jobs.outcome.ErrorMessage, "secret internal detail") {
				t.Errorf("internal detail leaked into the stored error: %q", jobs.outcome.ErrorMessage)
			}
			checkResultEvent(t, jobs.event, tt.wantEventData, tt.wantCode)
		})
	}
}

func checkResultEvent(t *testing.T, event store.OutboxEntry, wantData map[string]any, wantCode string) {
	t.Helper()
	if event.Topic != "wa.results" || !strings.HasPrefix(event.ID, "evt_") {
		t.Errorf("event topic/id = %s/%s", event.Topic, event.ID)
	}
	var envelope Envelope
	if err := json.Unmarshal(event.Payload, &envelope); err != nil {
		t.Fatalf("event payload: %v", err)
	}
	if envelope.Type != EventMessageResult || envelope.ID != event.ID || envelope.PhoneNumberID != "111" {
		t.Errorf("envelope = %+v", envelope)
	}
	var data map[string]any
	if err := json.Unmarshal(envelope.Data, &data); err != nil {
		t.Fatal(err)
	}
	for key, want := range wantData {
		if data[key] != want {
			t.Errorf("event data %s = %v, want %v", key, data[key], want)
		}
	}
	if wantCode != "" {
		failure, _ := data["error"].(map[string]any)
		if failure["code"] != wantCode {
			t.Errorf("event error = %v, want code %s", data["error"], wantCode)
		}
	}
	if strings.Contains(string(event.Payload), "hi") && strings.Contains(string(event.Payload), `"body"`) {
		t.Error("the result event must not carry the message content")
	}
}

func TestAsyncSenderStatus(t *testing.T) {
	createdAt := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	updatedAt := createdAt.Add(time.Minute)

	tests := []struct {
		name        string
		id          string
		stored      store.SendStatus
		isKnown     bool
		storeErr    error
		wantCode    string
		wantPlain   bool
		wantState   string
		wantWAMID   string
		wantFailure *SendFailure
	}{
		{name: "queued", id: "msg_1", stored: store.SendStatus{ID: "msg_1", State: store.SendQueued, Attempts: 1, CreatedAt: createdAt, UpdatedAt: createdAt}, isKnown: true, wantState: "queued"},
		{name: "sent", id: "msg_1", stored: store.SendStatus{ID: "msg_1", State: store.SendSent, MessageID: "wamid.1", Attempts: 1, CreatedAt: createdAt, UpdatedAt: updatedAt}, isKnown: true, wantState: "sent", wantWAMID: "wamid.1"},
		{
			name:   "failed carries the error",
			id:     "msg_1",
			stored: store.SendStatus{ID: "msg_1", State: store.SendFailed, ErrorCode: "window_closed", ErrorMessage: "closed", MetaCode: 131047, Attempts: 2, CreatedAt: createdAt, UpdatedAt: updatedAt}, isKnown: true,
			wantState: "failed", wantFailure: &SendFailure{Code: "window_closed", Message: "closed", MetaCode: 131047},
		},
		{name: "unknown id", id: "msg_missing", wantCode: CodeMessageNotFound},
		{name: "invalid id", id: "bad/id", wantCode: CodeInvalidRequest},
		{name: "empty id", id: "", wantCode: CodeInvalidRequest},
		{name: "store failure is not a request error", id: "msg_1", storeErr: errors.New("disk full"), wantPlain: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jobs := &fakeSendJobs{status: tt.stored, isKnown: tt.isKnown, statusErr: tt.storeErr}
			async := newTestAsync(&fakeAsyncSender{}, jobs, &recordingAsyncRecorder{}, createdAt)

			status, err := async.Status(context.Background(), tt.id)

			var requestErr *RequestError
			switch {
			case tt.wantPlain:
				if err == nil || errors.As(err, &requestErr) {
					t.Fatalf("error = %#v, want a plain error", err)
				}
			case tt.wantCode != "":
				if !errors.As(err, &requestErr) || requestErr.Code != tt.wantCode {
					t.Fatalf("error = %#v, want code %s", err, tt.wantCode)
				}
			default:
				if err != nil || status.State != tt.wantState || status.MessageID != tt.wantWAMID || status.Attempts != tt.stored.Attempts {
					t.Fatalf("Status() = %+v, %v", status, err)
				}
				if !status.CreatedAt.Equal(createdAt) || !status.UpdatedAt.Equal(tt.stored.UpdatedAt) {
					t.Errorf("timestamps = %v and %v", status.CreatedAt, status.UpdatedAt)
				}
				switch {
				case tt.wantFailure == nil && status.Failure != nil:
					t.Errorf("unexpected failure %+v", status.Failure)
				case tt.wantFailure != nil && (status.Failure == nil || *status.Failure != *tt.wantFailure):
					t.Errorf("failure = %+v, want %+v", status.Failure, tt.wantFailure)
				}
			}
		})
	}
}

func TestAsyncSenderReject(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	message := OutboundMessage{To: "94770000000", CorrelationID: "order-7"}
	invalid := &RequestError{Code: CodeInvalidRequest, Message: "to is wrong"}

	tests := []struct {
		name         string
		cause        error
		enqueueErr   error
		finishErr    error
		wantErr      bool
		wantFinishes int
		wantCode     string
		wantRecorded []string
	}{
		{name: "request error is reported with its own code", cause: invalid, wantFinishes: 1, wantCode: CodeInvalidRequest, wantRecorded: []string{"failed"}},
		{name: "unexpected cause is reported without detail", cause: errors.New("secret internal detail"), wantFinishes: 1, wantCode: "internal_error", wantRecorded: []string{"failed"}},
		{name: "queue failure is returned and nothing is finished", cause: invalid, enqueueErr: errors.New("disk full"), wantErr: true},
		{name: "failure to record the rejection is returned", cause: invalid, finishErr: errors.New("disk full"), wantErr: true, wantFinishes: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			jobs := &fakeSendJobs{enqueueErr: tt.enqueueErr, finishErr: tt.finishErr}
			recorder := &recordingAsyncRecorder{}
			async := newTestAsync(&fakeAsyncSender{}, jobs, recorder, now)

			err := async.Reject(context.Background(), message, tt.cause)

			if (err != nil) != tt.wantErr || jobs.finishes != tt.wantFinishes {
				t.Fatalf("error = %v, finishes = %d, want error=%v and %d finishes", err, jobs.finishes, tt.wantErr, tt.wantFinishes)
			}
			if strings.Join(recorder.results, ",") != strings.Join(tt.wantRecorded, ",") {
				t.Errorf("recorded %v, want %v", recorder.results, tt.wantRecorded)
			}
			if tt.wantCode == "" {
				return
			}
			if jobs.outcome.State != store.SendFailed || jobs.outcome.ErrorCode != tt.wantCode || strings.Contains(jobs.outcome.ErrorMessage, "secret internal detail") {
				t.Errorf("outcome = %+v, want a failed outcome with code %s", jobs.outcome, tt.wantCode)
			}
			checkResultEvent(t, jobs.event, map[string]any{"status": "failed", "correlation_id": "order-7"}, tt.wantCode)
			if jobs.event.Key != "94770000000" {
				t.Errorf("event key = %q, want the recipient", jobs.event.Key)
			}
		})
	}
}
