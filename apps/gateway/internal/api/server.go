package api

import (
	"context"
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/core"
)

type Ingestor interface {
	Ingest(ctx context.Context, items []core.Inbound) ([]core.IngestResult, error)
	Ready(ctx context.Context) error
}

type Recorder interface {
	WebhookResult(result string)
	EventIngested(eventType string, isQueued bool)
	SendResult(messageType, result string, took time.Duration)
	HTTPRequest(method, route string, status int, took time.Duration)
}

type Deps struct {
	Ingestor        Ingestor
	Recorder        Recorder
	MetricsHandler  http.Handler
	Sender          MessageSender
	APIKeys         []string
	MetaAppSecret   string
	MetaVerifyToken string
	Logger          *slog.Logger
}

type handler struct {
	deps Deps
}

func NewRouter(deps Deps) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)

	h := &handler{deps: deps}
	router := gin.New()
	router.Use(gin.Recovery(), observe(deps.Logger, deps.Recorder))

	router.GET("/healthz", h.healthz)
	router.GET("/readyz", h.readyz)
	router.GET("/webhook", h.verifyWebhook)
	router.POST("/webhook", h.receiveWebhook)
	if deps.Sender != nil {
		v1 := router.Group("/v1", requireAPIKey(newKeyRing(deps.APIKeys), deps.Logger))
		v1.POST("/messages", h.sendMessage)
	}
	if deps.MetricsHandler != nil {
		router.GET("/metrics", gin.WrapH(deps.MetricsHandler))
	}

	return router
}
