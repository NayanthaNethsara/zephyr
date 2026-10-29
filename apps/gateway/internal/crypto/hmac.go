package crypto

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
)

func SHA256Digest(value string) [sha256.Size]byte {
	return sha256.Sum256([]byte(value))
}

func SignHMACSHA256(secret []byte, parts ...[]byte) string {
	return hex.EncodeToString(computeHMACSHA256(secret, parts))
}

func VerifyHMACSHA256(secret []byte, signatureHex string, parts ...[]byte) bool {
	signature, err := hex.DecodeString(signatureHex)
	if err != nil {
		return false
	}
	return hmac.Equal(signature, computeHMACSHA256(secret, parts))
}

func computeHMACSHA256(secret []byte, parts [][]byte) []byte {
	mac := hmac.New(sha256.New, secret)
	for _, part := range parts {
		_, _ = mac.Write(part)
	}
	return mac.Sum(nil)
}
