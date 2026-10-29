package core

import "fmt"

const (
	CodeInvalidRequest         = "invalid_request"
	CodeWindowClosed           = "window_closed"
	CodeRecipientUndeliverable = "recipient_undeliverable"
	CodeRecipientNotAllowed    = "recipient_not_allowed"
	CodeTemplateError          = "template_error"
	CodeInvalidMedia           = "invalid_media"
	CodeRateLimited            = "rate_limited"
	CodeSendingRestricted      = "sending_restricted"
	CodeSenderMisconfigured    = "sender_misconfigured"
	CodeUpstreamAuthFailed     = "upstream_auth_failed"
	CodeUpstreamUnavailable    = "upstream_unavailable"
	CodeUpstreamTimeout        = "upstream_timeout"
	CodeRejected               = "rejected"
	CodeRequestInProgress      = "request_in_progress"
	CodeIdempotencyKeyReused   = "idempotency_key_reused"
)

type SendError struct {
	Code        string
	Message     string
	IsRetryable bool
	MetaCode    int
	TraceID     string
}

func (e *SendError) Error() string {
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}
