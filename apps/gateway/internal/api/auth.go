package api

import (
	"crypto/sha256"
	"crypto/subtle"
	"log/slog"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/crypto"
)

const codeUnauthorized = "unauthorized"

type keyRing struct {
	digests [][sha256.Size]byte
}

func newKeyRing(keys []string) keyRing {
	ring := keyRing{digests: make([][sha256.Size]byte, 0, len(keys))}
	for _, key := range keys {
		ring.digests = append(ring.digests, crypto.SHA256Digest(key))
	}
	return ring
}

func (k keyRing) accepts(key string) bool {
	candidate := crypto.SHA256Digest(key)
	matches := 0
	for _, digest := range k.digests {
		matches |= subtle.ConstantTimeCompare(digest[:], candidate[:])
	}
	return matches == 1
}

func requireAPIKey(ring keyRing, logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		if key, found := bearerToken(c.GetHeader("Authorization")); found && ring.accepts(key) {
			c.Next()
			return
		}

		requestLogger(logger, c).Warn("api key rejected")
		c.Header("WWW-Authenticate", "Bearer")
		c.AbortWithStatusJSON(http.StatusUnauthorized, errorBody{
			Code:    codeUnauthorized,
			Message: "a valid API key is required in the Authorization header",
		})
	}
}

func bearerToken(header string) (string, bool) {
	scheme, token, found := strings.Cut(header, " ")
	if !found || !strings.EqualFold(scheme, "Bearer") || token == "" {
		return "", false
	}
	return token, true
}
