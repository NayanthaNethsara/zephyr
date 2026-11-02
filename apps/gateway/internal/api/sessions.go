package api

import (
	"context"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/core"
)

type SessionProvider interface {
	Check(ctx context.Context, phone string) (core.Session, error)
}

type sessionResponse struct {
	Status        string `json:"status"`
	LastInboundAt string `json:"last_inbound_at,omitempty"`
	ExpiresAt     string `json:"expires_at,omitempty"`
}

func (h *handler) checkSession(c *gin.Context) {
	logger := requestLogger(h.deps.Logger, c)

	session, err := h.deps.Sessions.Check(c.Request.Context(), c.Param("phone"))
	if err != nil {
		status, body, requestErr := describeError(err)
		if requestErr == nil {
			logger.Error("session check failed unexpectedly", "error", err)
		} else {
			logger.Warn("session check rejected", "code", requestErr.Code)
		}
		c.JSON(status, body)
		return
	}

	body := sessionResponse{Status: string(session.State)}
	if session.State != core.SessionUnknown {
		body.LastInboundAt = session.LastInboundAt.UTC().Format(time.RFC3339)
		body.ExpiresAt = session.ExpiresAt.UTC().Format(time.RFC3339)
	}
	c.JSON(http.StatusOK, body)
}
