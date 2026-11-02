package core

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestBucketLimiterWait(t *testing.T) {
	tests := []struct {
		name         string
		perSecond    int
		maxWait      time.Duration
		calls        []string
		cancelAfter  time.Duration
		wantErrCalls []int
		wantMinDelay time.Duration
	}{
		{name: "calls within the burst pass at once", perSecond: 5, maxWait: time.Second, calls: []string{"111", "111", "111", "111", "111"}},
		{name: "a call past the burst is delayed but allowed", perSecond: 20, maxWait: time.Second, calls: repeatCalls("111", 21), wantMinDelay: 30 * time.Millisecond},
		{name: "a call that would wait too long is refused", perSecond: 1, maxWait: 50 * time.Millisecond, calls: []string{"111", "111"}, wantErrCalls: []int{1}},
		{name: "numbers have separate budgets", perSecond: 1, maxWait: 50 * time.Millisecond, calls: []string{"111", "222", "333"}},
		{name: "a refused call does not use up a token", perSecond: 1, maxWait: 50 * time.Millisecond, calls: []string{"111", "111", "111"}, wantErrCalls: []int{1, 2}},
		{name: "a caller that goes away during the wait is released", perSecond: 2, maxWait: time.Second, calls: []string{"111", "111", "111"}, cancelAfter: 20 * time.Millisecond, wantErrCalls: []int{2}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			limiter := NewBucketLimiter(tt.perSecond)
			limiter.maxWait = tt.maxWait

			wantFailure := map[int]bool{}
			for _, index := range tt.wantErrCalls {
				wantFailure[index] = true
			}

			started := time.Now()
			for i, phoneNumberID := range tt.calls {
				ctx := context.Background()
				cancel := func() {}
				if tt.cancelAfter > 0 && wantFailure[i] {
					ctx, cancel = context.WithTimeout(ctx, tt.cancelAfter)
				}
				err := limiter.Wait(ctx, phoneNumberID)
				cancel()

				var requestErr *RequestError
				isFailure := errors.As(err, &requestErr)
				if isFailure != wantFailure[i] {
					t.Fatalf("call %d error = %v, want failure=%v", i, err, wantFailure[i])
				}
				if isFailure && (requestErr.Code != CodeRateLimited || !requestErr.IsRetryable) {
					t.Errorf("call %d error = %#v, want a retryable rate_limited", i, requestErr)
				}
			}
			if elapsed := time.Since(started); elapsed < tt.wantMinDelay {
				t.Errorf("elapsed = %v, want at least %v", elapsed, tt.wantMinDelay)
			}
		})
	}
}

func repeatCalls(phoneNumberID string, count int) []string {
	calls := make([]string, count)
	for i := range calls {
		calls[i] = phoneNumberID
	}
	return calls
}

func TestSenderRateLimiting(t *testing.T) {
	text := OutboundMessage{To: "94770000000", Type: MessageText, Content: []byte(`{"body":"hi"}`)}
	keyed := text
	keyed.IdempotencyKey = "k1"
	limited := &RequestError{Code: CodeRateLimited, Message: "slow down", IsRetryable: true}

	tests := []struct {
		name         string
		message      OutboundMessage
		state        SessionState
		limiterErr   error
		wantWaits    []string
		wantSends    int
		wantCode     string
		wantReleased []string
	}{
		{name: "send waits on the configured number", message: text, state: SessionOpen, wantWaits: []string{"111"}, wantSends: 1},
		{name: "limiter refusal stops the send", message: text, state: SessionOpen, limiterErr: limited, wantWaits: []string{"111"}, wantCode: CodeRateLimited},
		{name: "closed window is refused before the limiter is consulted", message: text, state: SessionClosed, wantCode: CodeWindowClosed},
		{name: "refused keyed send releases its key", message: keyed, state: SessionOpen, limiterErr: limited, wantWaits: []string{"111"}, wantCode: CodeRateLimited, wantReleased: []string{"k1"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			messenger := &fakeMessenger{result: SendResult{MessageID: "wamid.sent"}}
			idempotency := &fakeIdempotencyStore{}
			limiter := &fakeLimiter{err: tt.limiterErr}
			sender := NewSender(SenderDeps{Messenger: messenger, Idempotency: idempotency, Sessions: &fakeSessions{state: tt.state}, Limiter: limiter}, "111", time.Hour)

			_, err := sender.Send(context.Background(), tt.message)

			if len(limiter.waited) != len(tt.wantWaits) || messenger.calls != tt.wantSends {
				t.Fatalf("waits = %v, sends = %d, want %v and %d", limiter.waited, messenger.calls, tt.wantWaits, tt.wantSends)
			}
			if len(idempotency.released) != len(tt.wantReleased) {
				t.Errorf("released = %v, want %v", idempotency.released, tt.wantReleased)
			}
			var requestErr *RequestError
			if tt.wantCode == "" {
				if err != nil {
					t.Fatalf("Send() error = %v", err)
				}
				return
			}
			if !errors.As(err, &requestErr) || requestErr.Code != tt.wantCode {
				t.Fatalf("error = %#v, want code %s", err, tt.wantCode)
			}
		})
	}
}
