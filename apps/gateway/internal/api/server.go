package api

import (
	"log/slog"

	"github.com/gin-gonic/gin"
)

func NewRouter(logger *slog.Logger) *gin.Engine {
	gin.SetMode(gin.ReleaseMode)

	router := gin.New()
	router.Use(gin.Recovery(), requestLogger(logger))

	router.GET("/healthz", healthz)
	router.GET("/readyz", readyz)

	return router
}
