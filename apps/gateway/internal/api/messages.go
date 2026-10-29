package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/core"
)

const (
	maxSendBodyBytes        = 1 << 20
	correlationHeader       = "X-Correlation-Id"
	idempotencyHeader       = "Idempotency-Key"
	replayedHeader          = "Idempotent-Replayed"
	resultReplayed          = "replayed"
	maxIdempotencyKeyLength = 255
	codeInternalError       = "internal_error"
	codePayloadTooLarge     = "payload_too_large"
	resultSent              = "sent"
	unknownMessageType      = "unknown"
	fieldTo                 = "to"
	fieldType               = "type"
	fieldPhoneNumberID      = "phone_number_id"
	fieldCorrelationID      = "correlation_id"
)

type MessageSender interface {
	Send(ctx context.Context, message core.OutboundMessage) (core.SendResult, error)
}

type errorBody struct {
	Code        string `json:"code"`
	Message     string `json:"message"`
	IsRetryable bool   `json:"retryable"`
	MetaCode    int    `json:"meta_code,omitempty"`
}

type sendResponse struct {
	WAMID         string `json:"wamid"`
	CorrelationID string `json:"correlation_id,omitempty"`
}

func (h *handler) sendMessage(c *gin.Context) {
	started := time.Now()
	logger := requestLogger(h.deps.Logger, c)

	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, maxSendBodyBytes))
	if err != nil {
		h.rejectSend(c, logger, core.OutboundMessage{}, started, http.StatusRequestEntityTooLarge, errorBody{
			Code:    codePayloadTooLarge,
			Message: "request body is too large",
		})
		return
	}

	message, problem := parseSendRequest(body, c.GetHeader(correlationHeader))
	if problem == "" {
		message.IdempotencyKey, problem = parseIdempotencyKey(c.GetHeader(idempotencyHeader))
	}
	if problem != "" {
		h.rejectSend(c, logger, message, started, http.StatusBadRequest, errorBody{Code: core.CodeInvalidRequest, Message: problem})
		return
	}
	if message.CorrelationID != "" {
		c.Header(correlationHeader, message.CorrelationID)
	}

	result, err := h.deps.Sender.Send(c.Request.Context(), message)
	if err != nil {
		h.failSend(c, logger, message, started, err)
		return
	}

	h.respondSent(c, logger, message, result, started)
}

func (h *handler) respondSent(c *gin.Context, logger *slog.Logger, message core.OutboundMessage, result core.SendResult, started time.Time) {
	took := time.Since(started)
	outcome, logMessage := resultSent, "message sent"
	if result.IsReplay {
		outcome, logMessage = resultReplayed, "message send replayed"
		c.Header(replayedHeader, "true")
	}

	h.deps.Recorder.SendResult(metricType(message.Type), outcome, took)
	logger.Info(logMessage,
		"wamid", result.MessageID,
		"type", message.Type,
		"to", core.MaskPhone(message.To),
		"correlation_id", message.CorrelationID,
		"idempotent", message.IdempotencyKey != "",
		"duration_ms", took.Milliseconds(),
	)
	if result.IsKeyUnrecorded {
		logger.Error("idempotency key could not be recorded, a retry may send the message again", "wamid", result.MessageID)
	}
	c.JSON(http.StatusOK, sendResponse{WAMID: result.MessageID, CorrelationID: message.CorrelationID})
}

func (h *handler) failSend(c *gin.Context, logger *slog.Logger, message core.OutboundMessage, started time.Time, err error) {
	var sendErr *core.SendError
	if !errors.As(err, &sendErr) {
		h.deps.Recorder.SendResult(metricType(message.Type), codeInternalError, time.Since(started))
		logger.Error("message send failed unexpectedly", "type", message.Type, "error", err)
		c.JSON(http.StatusInternalServerError, errorBody{Code: codeInternalError, Message: "the message could not be sent"})
		return
	}

	status := statusForCode(sendErr.Code)
	took := time.Since(started)
	h.deps.Recorder.SendResult(metricType(message.Type), sendErr.Code, took)

	log := logger.Warn
	if status >= http.StatusInternalServerError {
		log = logger.Error
	}
	log("message send failed",
		"code", sendErr.Code,
		"retryable", sendErr.IsRetryable,
		"meta_code", sendErr.MetaCode,
		"trace_id", sendErr.TraceID,
		"type", message.Type,
		"to", core.MaskPhone(message.To),
		"correlation_id", message.CorrelationID,
		"duration_ms", took.Milliseconds(),
	)
	c.JSON(status, errorBody{
		Code:        sendErr.Code,
		Message:     sendErr.Message,
		IsRetryable: sendErr.IsRetryable,
		MetaCode:    sendErr.MetaCode,
	})
}

func (h *handler) rejectSend(c *gin.Context, logger *slog.Logger, message core.OutboundMessage, started time.Time, status int, body errorBody) {
	h.deps.Recorder.SendResult(metricType(message.Type), body.Code, time.Since(started))
	logger.Warn("message request rejected", "code", body.Code, "reason", body.Message)
	c.JSON(status, body)
}

func parseSendRequest(body []byte, headerCorrelationID string) (core.OutboundMessage, string) {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil || fields == nil {
		return core.OutboundMessage{}, "request body must be a JSON object"
	}

	var message core.OutboundMessage
	stringFields := []struct {
		key    string
		target *string
	}{
		{fieldTo, &message.To},
		{fieldPhoneNumberID, &message.PhoneNumberID},
		{fieldCorrelationID, &message.CorrelationID},
	}
	for _, field := range stringFields {
		if problem := decodeStringField(fields, field.key, field.target); problem != "" {
			return core.OutboundMessage{}, problem
		}
	}

	var messageType string
	if problem := decodeStringField(fields, fieldType, &messageType); problem != "" {
		return core.OutboundMessage{}, problem
	}
	if messageType == "" {
		return core.OutboundMessage{}, "type is required"
	}
	message.Type = core.MessageType(messageType)
	message.Content = fields[messageType]

	if problem := unknownFieldProblem(fields, messageType); problem != "" {
		return message, problem
	}
	if message.CorrelationID == "" {
		message.CorrelationID = headerCorrelationID
	}
	return message, ""
}

func parseIdempotencyKey(header string) (string, string) {
	if header == "" {
		return "", ""
	}
	if len(header) > maxIdempotencyKeyLength {
		return "", fmt.Sprintf("%s must be at most %d characters", idempotencyHeader, maxIdempotencyKeyLength)
	}
	for _, character := range header {
		if character < '!' || character > '~' {
			return "", fmt.Sprintf("%s must use printable ASCII without spaces", idempotencyHeader)
		}
	}
	return header, ""
}

func decodeStringField(fields map[string]json.RawMessage, key string, target *string) string {
	raw, found := fields[key]
	if !found {
		return ""
	}
	if err := json.Unmarshal(raw, target); err != nil {
		return fmt.Sprintf("%s must be a string", key)
	}
	return ""
}

func unknownFieldProblem(fields map[string]json.RawMessage, contentKey string) string {
	for key := range fields {
		switch key {
		case fieldTo, fieldType, fieldPhoneNumberID, fieldCorrelationID, contentKey:
		default:
			return fmt.Sprintf("unknown field %q", key)
		}
	}
	return ""
}

func statusForCode(code string) int {
	switch code {
	case core.CodeInvalidRequest:
		return http.StatusBadRequest
	case core.CodeRateLimited:
		return http.StatusTooManyRequests
	case core.CodeRequestInProgress:
		return http.StatusConflict
	case core.CodeUpstreamUnavailable, core.CodeUpstreamAuthFailed, core.CodeSenderMisconfigured:
		return http.StatusBadGateway
	case core.CodeUpstreamTimeout:
		return http.StatusGatewayTimeout
	default:
		return http.StatusUnprocessableEntity
	}
}

func metricType(messageType core.MessageType) string {
	if messageType.IsSupported() {
		return string(messageType)
	}
	return unknownMessageType
}
