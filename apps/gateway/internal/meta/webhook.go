package meta

import (
	"crypto/subtle"
	"errors"
	"strings"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/crypto"
)

const (
	SignatureHeader = "X-Hub-Signature-256"
	signaturePrefix = "sha256="
	subscribeMode   = "subscribe"
)

var ErrInvalidSignature = errors.New("invalid webhook signature")

func VerifySignature(appSecret, header string, body []byte) error {
	signature, ok := strings.CutPrefix(header, signaturePrefix)
	if !ok || !crypto.VerifyHMACSHA256([]byte(appSecret), signature, body) {
		return ErrInvalidSignature
	}
	return nil
}

func VerifyHandshake(mode, token, expectedToken string) bool {
	return mode == subscribeMode &&
		subtle.ConstantTimeCompare([]byte(token), []byte(expectedToken)) == 1
}
