package core

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/store"
)

const (
	resultSent   = "sent"
	resultRetry  = "retry"
	resultFailed = "failed"

	codeInternalError = "internal_error"
)

type SendFailure struct {
	Code        string
	Message     string
	IsRetryable bool
	MetaCode    int
}

type SendStatus struct {
	ID        string
	State     string
	MessageID string
	Failure   *SendFailure
	Attempts  int
	CreatedAt time.Time
	UpdatedAt time.Time
}

type AsyncRecorder interface {
	AsyncSendResult(result string)
}

type MessageSender interface {
	Validate(message OutboundMessage) error
	Send(ctx context.Context, message OutboundMessage) (SendResult, error)
}

type AsyncSettings struct {
	PhoneNumberID string
	TopicPrefix   string
	MaxAttempts   int
	RetryDelay    func(attempts int) time.Duration
}

type AsyncSender struct {
	sender   MessageSender
	jobs     store.SendJobStore
	settings AsyncSettings
	recorder AsyncRecorder
	logger   *slog.Logger
	now      func() time.Time
}

func NewAsyncSender(sender MessageSender, jobs store.SendJobStore, settings AsyncSettings, recorder AsyncRecorder, logger *slog.Logger) *AsyncSender {
	return &AsyncSender{
		sender:   sender,
		jobs:     jobs,
		settings: settings,
		recorder: recorder,
		logger:   logger,
		now:      time.Now,
	}
}

func (a *AsyncSender) Enqueue(ctx context.Context, message OutboundMessage) (string, error) {
	if err := a.sender.Validate(message); err != nil {
		return "", err
	}
	id, err := newID("msg_")
	if err != nil {
		return "", err
	}
	if message.IdempotencyKey == "" {
		message.IdempotencyKey = id
	}
	payload, err := json.Marshal(message)
	if err != nil {
		return "", fmt.Errorf("encode queued message: %w", err)
	}

	now := a.now()
	if err := a.jobs.EnqueueSend(ctx, store.SendJob{ID: id, Payload: payload, NextAttemptAt: now, CreatedAt: now}); err != nil {
		return "", err
	}
	return id, nil
}

func (a *AsyncSender) Reject(ctx context.Context, message OutboundMessage, cause error) error {
	id, err := newID("msg_")
	if err != nil {
		return err
	}
	now := a.now()
	if err := a.jobs.EnqueueSend(ctx, store.SendJob{ID: id, NextAttemptAt: now, CreatedAt: now}); err != nil {
		return err
	}

	failure := describeFailure(cause)
	outcome := store.SendOutcome{
		State:        store.SendFailed,
		ErrorCode:    failure.Code,
		ErrorMessage: failure.Message,
		IsRetryable:  failure.IsRetryable,
		MetaCode:     failure.MetaCode,
		At:           now,
	}
	data := resultData{ID: id, Status: store.SendFailed, CorrelationID: message.CorrelationID, Error: &resultError{
		Code: failure.Code, Message: failure.Message, Retryable: failure.IsRetryable, MetaCode: failure.MetaCode,
	}}
	if !a.finish(ctx, store.SendJob{ID: id}, message, 0, outcome, data) {
		return errors.New("record rejected message")
	}
	a.recorder.AsyncSendResult(resultFailed)
	return nil
}

func (a *AsyncSender) Status(ctx context.Context, id string) (SendStatus, error) {
	if !isSafeIdentifier(id) {
		return SendStatus{}, invalidRequest("message id must be 1 to 128 letters, digits, hyphens, or underscores")
	}
	status, isKnown, err := a.jobs.SendStatus(ctx, id)
	if err != nil {
		return SendStatus{}, err
	}
	if !isKnown {
		return SendStatus{}, &RequestError{Code: CodeMessageNotFound, Message: "there is no queued message with this id"}
	}

	result := SendStatus{
		ID:        status.ID,
		State:     status.State,
		MessageID: status.MessageID,
		Attempts:  status.Attempts,
		CreatedAt: status.CreatedAt,
		UpdatedAt: status.UpdatedAt,
	}
	if status.State == store.SendFailed {
		result.Failure = &SendFailure{
			Code:        status.ErrorCode,
			Message:     status.ErrorMessage,
			IsRetryable: status.IsRetryable,
			MetaCode:    status.MetaCode,
		}
	}
	return result, nil
}

func (a *AsyncSender) Process(ctx context.Context, job store.SendJob) {
	attempts := job.Attempts + 1

	var message OutboundMessage
	if err := json.Unmarshal(job.Payload, &message); err != nil {
		a.fail(ctx, job, message, attempts, invalidRequest("the queued message could not be read"))
		return
	}

	result, err := a.sender.Send(ctx, message)
	switch {
	case err == nil:
		a.succeed(ctx, job, message, attempts, result)
	case ctx.Err() != nil:
		return
	case isRetryable(err) && attempts < a.settings.MaxAttempts:
		a.retry(ctx, job, attempts, err)
	default:
		a.fail(ctx, job, message, attempts, err)
	}
}

func (a *AsyncSender) succeed(ctx context.Context, job store.SendJob, message OutboundMessage, attempts int, result SendResult) {
	outcome := store.SendOutcome{State: store.SendSent, MessageID: result.MessageID, At: a.now()}
	data := resultData{ID: job.ID, Status: store.SendSent, WAMID: result.MessageID, CorrelationID: message.CorrelationID}
	if a.finish(ctx, job, message, attempts, outcome, data) {
		a.recorder.AsyncSendResult(resultSent)
		a.logger.Info("queued message sent", "message_id", job.ID, "wamid", result.MessageID, "attempts", attempts)
	}
}

func (a *AsyncSender) fail(ctx context.Context, job store.SendJob, message OutboundMessage, attempts int, cause error) {
	failure := describeFailure(cause)
	outcome := store.SendOutcome{
		State:        store.SendFailed,
		ErrorCode:    failure.Code,
		ErrorMessage: failure.Message,
		IsRetryable:  failure.IsRetryable,
		MetaCode:     failure.MetaCode,
		At:           a.now(),
	}
	data := resultData{ID: job.ID, Status: store.SendFailed, CorrelationID: message.CorrelationID, Error: &resultError{
		Code: failure.Code, Message: failure.Message, Retryable: failure.IsRetryable, MetaCode: failure.MetaCode,
	}}
	if a.finish(ctx, job, message, attempts, outcome, data) {
		a.recorder.AsyncSendResult(resultFailed)
		a.logger.Warn("queued message failed", "message_id", job.ID, "code", failure.Code, "attempts", attempts)
	}
}

func (a *AsyncSender) retry(ctx context.Context, job store.SendJob, attempts int, cause error) {
	delay := a.settings.RetryDelay(attempts)
	if err := a.jobs.RescheduleSend(ctx, job.ID, attempts, a.now().Add(delay)); err != nil {
		a.logger.Error("reschedule queued message", "message_id", job.ID, "error", err)
		return
	}
	a.recorder.AsyncSendResult(resultRetry)
	a.logger.Warn("queued message will be retried",
		"message_id", job.ID,
		"code", describeFailure(cause).Code,
		"attempts", attempts,
		"retry_in_ms", delay.Milliseconds(),
	)
}

func (a *AsyncSender) finish(ctx context.Context, job store.SendJob, message OutboundMessage, attempts int, outcome store.SendOutcome, data resultData) bool {
	event, err := a.resultEvent(message, data)
	if err != nil {
		a.logger.Error("build result event", "message_id", job.ID, "error", err)
		return false
	}
	settleCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), settleTimeout)
	defer cancel()
	if err := a.jobs.FinishSend(settleCtx, job.ID, attempts, outcome, event); err != nil {
		a.logger.Error("record queued message result", "message_id", job.ID, "error", err)
		return false
	}
	return true
}

type resultError struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
	MetaCode  int    `json:"meta_code,omitempty"`
}

type resultData struct {
	ID            string       `json:"id"`
	Status        string       `json:"status"`
	WAMID         string       `json:"wamid,omitempty"`
	CorrelationID string       `json:"correlation_id,omitempty"`
	Error         *resultError `json:"error,omitempty"`
}

func (a *AsyncSender) resultEvent(message OutboundMessage, data resultData) (store.OutboxEntry, error) {
	id, err := newID("evt_")
	if err != nil {
		return store.OutboxEntry{}, err
	}
	now := a.now()
	encoded, err := json.Marshal(data)
	if err != nil {
		return store.OutboxEntry{}, fmt.Errorf("encode result data: %w", err)
	}
	payload, err := json.Marshal(Envelope{
		ID:            id,
		Type:          EventMessageResult,
		OccurredAt:    now.UTC(),
		PhoneNumberID: a.settings.PhoneNumberID,
		Data:          encoded,
	})
	if err != nil {
		return store.OutboxEntry{}, fmt.Errorf("encode result envelope: %w", err)
	}
	return store.OutboxEntry{
		ID:            id,
		Topic:         a.settings.TopicPrefix + "." + EventMessageResult.topicSuffix(),
		Key:           message.To,
		Payload:       payload,
		NextAttemptAt: now,
		CreatedAt:     now,
	}, nil
}

func isRetryable(err error) bool {
	var requestErr *RequestError
	if errors.As(err, &requestErr) {
		return requestErr.IsRetryable
	}
	return true
}

func describeFailure(err error) SendFailure {
	var requestErr *RequestError
	if errors.As(err, &requestErr) {
		return SendFailure{Code: requestErr.Code, Message: requestErr.Message, IsRetryable: requestErr.IsRetryable, MetaCode: requestErr.MetaCode}
	}
	return SendFailure{Code: codeInternalError, Message: "the message could not be sent", IsRetryable: true}
}

func newID(prefix string) (string, error) {
	raw := make([]byte, 16)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate id: %w", err)
	}
	return prefix + hex.EncodeToString(raw), nil
}
