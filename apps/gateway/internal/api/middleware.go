package api

import (
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"
)

const (
	requestIDKey    = "request_id"
	requestIDHeader = "X-Request-Id"
	unmatchedRoute  = "unmatched"
)

func isQuietRoute(route string) bool {
	switch route {
	case "/healthz", "/readyz", "/metrics":
		return true
	}
	return false
}

func observe(logger *slog.Logger, recorder Recorder) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		requestID := newRequestID()
		c.Set(requestIDKey, requestID)
		c.Header(requestIDHeader, requestID)

		c.Next()

		route := c.FullPath()
		if route == "" {
			route = unmatchedRoute
		}
		took := time.Since(start)
		status := c.Writer.Status()

		recorder.HTTPRequest(c.Request.Method, route, status, took)

		level := slog.LevelInfo
		if isQuietRoute(route) {
			level = slog.LevelDebug
		}
		logger.Log(c.Request.Context(), level, "request",
			"request_id", requestID,
			"method", c.Request.Method,
			"route", route,
			"status", status,
			"duration_ms", took.Milliseconds(),
		)
	}
}

func requestLogger(logger *slog.Logger, c *gin.Context) *slog.Logger {
	return logger.With("request_id", c.GetString(requestIDKey))
}

func newRequestID() string {
	raw := make([]byte, 8)
	if _, err := rand.Read(raw); err != nil {
		return "req_unknown"
	}
	return "req_" + hex.EncodeToString(raw)
}
