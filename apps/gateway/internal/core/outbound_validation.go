package core

import (
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
)

const (
	minRecipientDigits = 7
	maxRecipientDigits = 15
)

func validateOutbound(message OutboundMessage) error {
	if !isValidRecipient(message.To) {
		return invalidRequest("to must be digits only, 7 to 15 characters, without a plus sign")
	}
	if problem := contentProblem(message.Type, message.Content); problem != "" {
		return invalidRequest(problem)
	}
	return nil
}

func isValidRecipient(recipient string) bool {
	if len(recipient) < minRecipientDigits || len(recipient) > maxRecipientDigits {
		return false
	}
	for _, character := range recipient {
		if !unicode.IsDigit(character) || character > unicode.MaxASCII {
			return false
		}
	}
	return true
}

func contentProblem(messageType MessageType, content json.RawMessage) string {
	switch messageType {
	case MessageText:
		return requireStrings(messageType, content, "body")
	case MessageImage, MessageVideo, MessageAudio, MessageDocument, MessageSticker:
		return requireMediaSource(messageType, content)
	case MessageLocation:
		return requireNumbers(messageType, content, "latitude", "longitude")
	case MessageContacts:
		return requireContacts(content)
	case MessageInteractive:
		return requireInteractive(content)
	case MessageTemplate:
		return requireTemplate(content)
	case MessageReaction:
		return requireStrings(messageType, content, "message_id")
	default:
		return fmt.Sprintf("type %q is not supported", messageType)
	}
}

func decodeObject(messageType MessageType, content json.RawMessage) (map[string]json.RawMessage, string) {
	var fields map[string]json.RawMessage
	if len(content) == 0 || json.Unmarshal(content, &fields) != nil || fields == nil {
		return nil, fmt.Sprintf("%s must be a JSON object", messageType)
	}
	return fields, ""
}

func requireStrings(messageType MessageType, content json.RawMessage, keys ...string) string {
	fields, problem := decodeObject(messageType, content)
	if problem != "" {
		return problem
	}
	for _, key := range keys {
		if !hasText(fields, key) {
			return fmt.Sprintf("%s.%s is required", messageType, key)
		}
	}
	return ""
}

func requireNumbers(messageType MessageType, content json.RawMessage, keys ...string) string {
	fields, problem := decodeObject(messageType, content)
	if problem != "" {
		return problem
	}
	for _, key := range keys {
		var number float64
		if raw, found := fields[key]; !found || json.Unmarshal(raw, &number) != nil {
			return fmt.Sprintf("%s.%s must be a number", messageType, key)
		}
	}
	return ""
}

func requireMediaSource(messageType MessageType, content json.RawMessage) string {
	fields, problem := decodeObject(messageType, content)
	if problem != "" {
		return problem
	}
	if hasText(fields, "id") == hasText(fields, "link") {
		return fmt.Sprintf("%s needs exactly one of id or link", messageType)
	}
	return ""
}

func requireContacts(content json.RawMessage) string {
	var contacts []map[string]json.RawMessage
	if json.Unmarshal(content, &contacts) != nil || len(contacts) == 0 {
		return "contacts must be a non-empty array"
	}
	for _, contact := range contacts {
		if _, found := contact["name"]; !found {
			return "every contact needs a name"
		}
	}
	return ""
}

func requireInteractive(content json.RawMessage) string {
	fields, problem := decodeObject(MessageInteractive, content)
	if problem != "" {
		return problem
	}
	if !hasText(fields, "type") {
		return "interactive.type is required"
	}
	if _, found := fields["action"]; !found {
		return "interactive.action is required"
	}
	return ""
}

func requireTemplate(content json.RawMessage) string {
	fields, problem := decodeObject(MessageTemplate, content)
	if problem != "" {
		return problem
	}
	if !hasText(fields, "name") {
		return "template.name is required"
	}
	var language map[string]json.RawMessage
	if raw, found := fields["language"]; !found || json.Unmarshal(raw, &language) != nil || !hasText(language, "code") {
		return "template.language.code is required"
	}
	return ""
}

func hasText(fields map[string]json.RawMessage, key string) bool {
	var value string
	raw, found := fields[key]
	if !found || json.Unmarshal(raw, &value) != nil {
		return false
	}
	return strings.TrimSpace(value) != ""
}
