package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/core"
)

const (
	defaultFailedLimit = 50
	maxFailedLimit     = 200
	resultQueued       = "queued"
)

type FailedEventProvider interface {
	ListFailed(ctx context.Context, limit, offset int) ([]core.FailedEvent, error)
	Replay(ctx context.Context, eventID string) error
}

type failedEventBody struct {
	ID        string `json:"id"`
	Topic     string `json:"topic"`
	Attempts  int    `json:"attempts"`
	LastError string `json:"last_error"`
	CreatedAt string `json:"created_at"`
	FailedAt  string `json:"failed_at"`
}

type failedEventsResponse struct {
	Events []failedEventBody `json:"events"`
	Limit  int               `json:"limit"`
	Offset int               `json:"offset"`
}

type replayResponse struct {
	ID     string `json:"id"`
	Status string `json:"status"`
}

func (h *handler) listFailedEvents(c *gin.Context) {
	logger := requestLogger(h.deps.Logger, c)

	limit, offset, problem := parsePage(c.Query("limit"), c.Query("offset"))
	if problem != "" {
		c.JSON(http.StatusBadRequest, errorBody{Code: core.CodeInvalidRequest, Message: problem})
		return
	}

	events, err := h.deps.Events.ListFailed(c.Request.Context(), limit, offset)
	if err != nil {
		h.failEvent(c, logger, "list", err)
		return
	}

	body := failedEventsResponse{Events: make([]failedEventBody, 0, len(events)), Limit: limit, Offset: offset}
	for _, event := range events {
		body.Events = append(body.Events, failedEventBody{
			ID:        event.ID,
			Topic:     event.Topic,
			Attempts:  event.Attempts,
			LastError: event.LastError,
			CreatedAt: event.CreatedAt.UTC().Format(time.RFC3339),
			FailedAt:  event.FailedAt.UTC().Format(time.RFC3339),
		})
	}
	c.JSON(http.StatusOK, body)
}

func (h *handler) replayEvent(c *gin.Context) {
	logger := requestLogger(h.deps.Logger, c)
	eventID := c.Param("id")

	if err := h.deps.Events.Replay(c.Request.Context(), eventID); err != nil {
		h.failEvent(c, logger, "replay", err)
		return
	}

	h.deps.Recorder.ReplayResult(resultQueued)
	logger.Info("failed event queued for replay", "event_id", eventID)
	c.JSON(http.StatusAccepted, replayResponse{ID: eventID, Status: resultQueued})
}

func (h *handler) failEvent(c *gin.Context, logger *slog.Logger, operation string, err error) {
	status, body, requestErr := describeError(err)
	if operation == "replay" {
		h.deps.Recorder.ReplayResult(body.Code)
	}

	if requestErr == nil {
		logger.Error("failed event request failed unexpectedly", "operation", operation, "error", err)
		c.JSON(status, body)
		return
	}
	logger.Warn("failed event request rejected", "operation", operation, "code", requestErr.Code)
	c.JSON(status, body)
}

func parsePage(rawLimit, rawOffset string) (int, int, string) {
	limit, offset := defaultFailedLimit, 0
	if rawLimit != "" {
		parsed, err := strconv.Atoi(rawLimit)
		if err != nil || parsed < 1 || parsed > maxFailedLimit {
			return 0, 0, fmt.Sprintf("limit must be a whole number from 1 to %d", maxFailedLimit)
		}
		limit = parsed
	}
	if rawOffset != "" {
		parsed, err := strconv.Atoi(rawOffset)
		if err != nil || parsed < 0 {
			return 0, 0, "offset must be a whole number of 0 or more"
		}
		offset = parsed
	}
	return limit, offset, ""
}
