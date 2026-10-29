package core

import (
	"bytes"
	"encoding/hex"
	"encoding/json"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/crypto"
)

func fingerprint(message OutboundMessage) string {
	var content bytes.Buffer
	if err := json.Compact(&content, message.Content); err != nil {
		content.Reset()
		content.Write(message.Content)
	}

	digest := crypto.SHA256Digest(message.To + "\x00" + string(message.Type) + "\x00" + content.String())
	return hex.EncodeToString(digest[:])
}
