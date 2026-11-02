package api

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"time"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/core"
	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/transport"
)

const (
	consumeRetryDelay   = 5 * time.Second
	brokerKeyPrefix     = "broker:"
	fieldIdempotencyKey = "idempotency_key"
	consumedQueued      = "queued"
	consumedRejected    = "rejected"
	consumedRetry       = "retry"
)

type OutboundQueue interface {
	Enqueue(ctx context.Context, message core.OutboundMessage) (string, error)
	Reject(ctx context.Context, message core.OutboundMessage, cause error) error
}

type ConsumeRecorder interface {
	OutboundConsumed(result string)
}

type OutboundConsumer struct {
	queue    OutboundQueue
	recorder ConsumeRecorder
	logger   *slog.Logger
}

func NewOutboundConsumer(queue OutboundQueue, recorder ConsumeRecorder, logger *slog.Logger) *OutboundConsumer {
	return &OutboundConsumer{queue: queue, recorder: recorder, logger: logger}
}

func (c *OutboundConsumer) Handle(ctx context.Context, received transport.Message) {
	body, key, problem := splitIdempotencyKey(received.Data())
	var message core.OutboundMessage
	if problem == "" {
		message, problem = parseSendRequest(body, "")
	}
	if problem != "" {
		c.reject(ctx, received, message, &core.RequestError{Code: core.CodeInvalidRequest, Message: problem})
		return
	}

	if key == "" && received.ID() != "" {
		key = brokerKeyPrefix + received.ID()
	}
	message.IdempotencyKey = key

	id, err := c.queue.Enqueue(ctx, message)
	if err != nil {
		var requestErr *core.RequestError
		if errors.As(err, &requestErr) {
			c.reject(ctx, received, message, err)
			return
		}
		c.retryLater(received, err)
		return
	}

	c.settle(received, consumedQueued)
	c.logger.Info("broker message queued",
		"message_id", id,
		"type", message.Type,
		"to", core.MaskPhone(message.To),
		"correlation_id", message.CorrelationID,
	)
}

func (c *OutboundConsumer) reject(ctx context.Context, received transport.Message, message core.OutboundMessage, cause error) {
	if err := c.queue.Reject(ctx, message, cause); err != nil {
		c.retryLater(received, err)
		return
	}
	c.settle(received, consumedRejected)
	c.logger.Warn("broker message rejected", "type", message.Type, "correlation_id", message.CorrelationID, "reason", cause.Error())
}

func (c *OutboundConsumer) retryLater(received transport.Message, cause error) {
	c.recorder.OutboundConsumed(consumedRetry)
	c.logger.Error("broker message could not be queued, retrying", "retry_in_ms", consumeRetryDelay.Milliseconds(), "error", cause)
	if err := received.Nack(consumeRetryDelay); err != nil {
		c.logger.Error("broker message nack failed", "error", err)
	}
}

func (c *OutboundConsumer) settle(received transport.Message, result string) {
	c.recorder.OutboundConsumed(result)
	if err := received.Ack(); err != nil {
		c.logger.Error("broker message ack failed", "error", err)
	}
}

func splitIdempotencyKey(body []byte) ([]byte, string, string) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return body, "", ""
	}
	raw, found := fields[fieldIdempotencyKey]
	if !found {
		return body, "", ""
	}
	delete(fields, fieldIdempotencyKey)

	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, "", fieldIdempotencyKey + " must be a string"
	}
	key, problem := parseIdempotencyKey(value, fieldIdempotencyKey)
	if problem != "" {
		return nil, "", problem
	}
	rest, err := json.Marshal(fields)
	if err != nil {
		return nil, "", "request body could not be read"
	}
	return rest, key, ""
}
