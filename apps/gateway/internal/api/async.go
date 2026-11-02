package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/core"
)

const asyncQuery = "async"

type AsyncSender interface {
	Enqueue(ctx context.Context, message core.OutboundMessage) (string, error)
	Status(ctx context.Context, id string) (core.SendStatus, error)
}

type queuedResponse struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	CorrelationID string `json:"correlation_id,omitempty"`
}

type messageStatusResponse struct {
	ID        string     `json:"id"`
	Status    string     `json:"status"`
	WAMID     string     `json:"wamid,omitempty"`
	Attempts  int        `json:"attempts"`
	Error     *errorBody `json:"error,omitempty"`
	CreatedAt string     `json:"created_at"`
	UpdatedAt string     `json:"updated_at"`
}

func parseAsync(value string) (bool, string) {
	switch value {
	case "", "false":
		return false, ""
	case "true":
		return true, ""
	default:
		return false, "async must be true or false"
	}
}

func (h *handler) enqueueMessage(c *gin.Context, logger *slog.Logger, message core.OutboundMessage, started time.Time) {
	id, err := h.deps.Async.Enqueue(c.Request.Context(), message)
	if err != nil {
		h.failSend(c, logger, message, started, err)
		return
	}

	took := time.Since(started)
	h.deps.Recorder.SendResult(metricType(message.Type), resultQueued, took)
	logger.Info("message queued",
		"message_id", id,
		"type", message.Type,
		"to", core.MaskPhone(message.To),
		"correlation_id", message.CorrelationID,
		"duration_ms", took.Milliseconds(),
	)
	c.JSON(http.StatusAccepted, queuedResponse{ID: id, Status: resultQueued, CorrelationID: message.CorrelationID})
}

func (h *handler) messageStatus(c *gin.Context) {
	logger := requestLogger(h.deps.Logger, c)

	status, err := h.deps.Async.Status(c.Request.Context(), c.Param("id"))
	if err != nil {
		code, body, requestErr := describeError(err)
		if requestErr == nil {
			logger.Error("message status failed unexpectedly", "error", err)
		} else {
			logger.Warn("message status rejected", "code", requestErr.Code)
		}
		c.JSON(code, body)
		return
	}

	body := messageStatusResponse{
		ID:        status.ID,
		Status:    status.State,
		WAMID:     status.MessageID,
		Attempts:  status.Attempts,
		CreatedAt: status.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt: status.UpdatedAt.UTC().Format(time.RFC3339),
	}
	if status.Failure != nil {
		body.Error = &errorBody{
			Code:        status.Failure.Code,
			Message:     status.Failure.Message,
			IsRetryable: status.Failure.IsRetryable,
			MetaCode:    status.Failure.MetaCode,
		}
	}
	c.JSON(http.StatusOK, body)
}
