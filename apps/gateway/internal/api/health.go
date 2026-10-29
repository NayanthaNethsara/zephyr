package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

func healthz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

func readyz(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ready"})
}
