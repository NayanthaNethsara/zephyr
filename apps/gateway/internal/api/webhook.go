package api

import (
	"io"
	"log/slog"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/core"
	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/meta"
)

const maxWebhookBodyBytes = 2 << 20

const (
	resultAccepted         = "accepted"
	resultIgnored          = "ignored"
	resultInvalidSignature = "invalid_signature"
	resultMalformed        = "malformed"
	resultTooLarge         = "too_large"
	resultPersistError     = "persist_error"
)

func (h *handler) verifyWebhook(c *gin.Context) {
	mode := c.Query("hub.mode")
	token := c.Query("hub.verify_token")

	if !meta.VerifyHandshake(mode, token, h.deps.MetaVerifyToken) {
		requestLogger(h.deps.Logger, c).Warn("webhook verification rejected")
		c.Status(http.StatusForbidden)
		return
	}
	requestLogger(h.deps.Logger, c).Info("webhook verified by meta")
	c.String(http.StatusOK, c.Query("hub.challenge"))
}

func (h *handler) receiveWebhook(c *gin.Context) {
	logger := requestLogger(h.deps.Logger, c)

	body, err := io.ReadAll(http.MaxBytesReader(c.Writer, c.Request.Body, maxWebhookBodyBytes))
	if err != nil {
		h.deps.Recorder.WebhookResult(resultTooLarge)
		logger.Warn("webhook body rejected", "error", err)
		c.Status(http.StatusRequestEntityTooLarge)
		return
	}

	signature := c.GetHeader(meta.SignatureHeader)
	if err := meta.VerifySignature(h.deps.MetaAppSecret, signature, body); err != nil {
		h.deps.Recorder.WebhookResult(resultInvalidSignature)
		logger.Warn("webhook signature rejected")
		c.Status(http.StatusUnauthorized)
		return
	}

	items, err := meta.ParseWebhook(body)
	if err != nil {
		h.deps.Recorder.WebhookResult(resultMalformed)
		logger.Warn("webhook payload malformed", "error", err)
		c.Status(http.StatusBadRequest)
		return
	}
	if len(items) == 0 {
		h.deps.Recorder.WebhookResult(resultIgnored)
		c.Status(http.StatusOK)
		return
	}

	results, err := h.deps.Ingestor.Ingest(c.Request.Context(), items)
	if err != nil {
		h.deps.Recorder.WebhookResult(resultPersistError)
		logger.Error("persist webhook events failed", "events", len(items), "error", err)
		c.Status(http.StatusInternalServerError)
		return
	}

	h.deps.Recorder.WebhookResult(resultAccepted)
	h.reportResults(logger, results)
	c.Status(http.StatusOK)
}

func (h *handler) reportResults(logger *slog.Logger, results []core.IngestResult) {
	for _, result := range results {
		h.deps.Recorder.EventIngested(string(result.Type), result.Queued)

		outcome := "duplicate"
		if result.Queued {
			outcome = "queued"
		}
		logger.Info("event received",
			"event_id", result.EventID,
			"wamid", result.MessageID,
			"type", result.Type,
			"subtype", result.Subtype,
			"phone", result.MaskedPhone,
			"outcome", outcome,
		)
	}
}
