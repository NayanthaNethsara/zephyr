package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func (h *handler) healthz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func (h *handler) readyz(c *gin.Context) {
	if err := h.deps.Ingestor.Ready(c.Request.Context()); err != nil {
		h.deps.Logger.Error("readiness check failed", "error", err)
		c.JSON(http.StatusServiceUnavailable, gin.H{"status": "unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"status": "ready"})
}
