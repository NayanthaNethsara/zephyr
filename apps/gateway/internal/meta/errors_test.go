package meta

import (
	"testing"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/core"
)

func TestClassifyError(t *testing.T) {
	tests := []struct {
		name          string
		metaCode      int
		httpStatus    int
		wantCode      string
		wantRetryable bool
	}{
		{name: "window closed", metaCode: 131047, httpStatus: 400, wantCode: core.CodeWindowClosed},
		{name: "recipient not on allowed list", metaCode: 131030, httpStatus: 400, wantCode: core.CodeRecipientNotAllowed},
		{name: "undeliverable", metaCode: 131026, httpStatus: 400, wantCode: core.CodeRecipientUndeliverable},
		{name: "recipient blocked the business", metaCode: 130403, httpStatus: 400, wantCode: core.CodeRecipientUndeliverable},
		{name: "recipient opted out", metaCode: 131050, httpStatus: 400, wantCode: core.CodeRecipientUndeliverable},
		{name: "ecosystem health", metaCode: 131049, httpStatus: 400, wantCode: core.CodeRecipientUndeliverable},
		{name: "required parameter missing", metaCode: 131008, httpStatus: 400, wantCode: core.CodeInvalidRequest},
		{name: "invalid parameter value", metaCode: 131009, httpStatus: 400, wantCode: core.CodeInvalidRequest},
		{name: "misspelled parameter", metaCode: 100, httpStatus: 400, wantCode: core.CodeInvalidRequest},
		{name: "unsupported message type", metaCode: 131051, httpStatus: 400, wantCode: core.CodeInvalidRequest},
		{name: "media upload failure", metaCode: 131053, httpStatus: 400, wantCode: core.CodeInvalidMedia},
		{name: "template parameter mismatch", metaCode: 132000, httpStatus: 400, wantCode: core.CodeTemplateError},
		{name: "template not found", metaCode: 132001, httpStatus: 404, wantCode: core.CodeTemplateError},
		{name: "template disabled for quality", metaCode: 132016, httpStatus: 400, wantCode: core.CodeTemplateError},
		{name: "throughput limit", metaCode: 130429, httpStatus: 429, wantCode: core.CodeRateLimited, wantRetryable: true},
		{name: "pair rate limit", metaCode: 131056, httpStatus: 400, wantCode: core.CodeRateLimited, wantRetryable: true},
		{name: "app call limit", metaCode: 4, httpStatus: 400, wantCode: core.CodeRateLimited, wantRetryable: true},
		{name: "sending restricted", metaCode: 131048, httpStatus: 400, wantCode: core.CodeSendingRestricted},
		{name: "policy violation", metaCode: 368, httpStatus: 400, wantCode: core.CodeSendingRestricted},
		{name: "account restricted", metaCode: 131031, httpStatus: 400, wantCode: core.CodeSendingRestricted},
		{name: "number not registered", metaCode: 133010, httpStatus: 400, wantCode: core.CodeSenderMisconfigured},
		{name: "payment problem", metaCode: 131042, httpStatus: 400, wantCode: core.CodeSenderMisconfigured},
		{name: "expired access token", metaCode: 190, httpStatus: 401, wantCode: core.CodeUpstreamAuthFailed},
		{name: "permission range", metaCode: 217, httpStatus: 403, wantCode: core.CodeUpstreamAuthFailed},
		{name: "user not authenticated", metaCode: 0, httpStatus: 401, wantCode: core.CodeUpstreamAuthFailed},
		{name: "temporary downtime", metaCode: 2, httpStatus: 503, wantCode: core.CodeUpstreamUnavailable, wantRetryable: true},
		{name: "unknown error", metaCode: 131000, httpStatus: 500, wantCode: core.CodeUpstreamUnavailable, wantRetryable: true},
		{name: "service unavailable", metaCode: 131016, httpStatus: 503, wantCode: core.CodeUpstreamUnavailable, wantRetryable: true},
		{name: "unknown code with server error status", metaCode: 999999, httpStatus: 502, wantCode: core.CodeUpstreamUnavailable, wantRetryable: true},
		{name: "unknown code with throttling status", metaCode: 999999, httpStatus: 429, wantCode: core.CodeRateLimited, wantRetryable: true},
		{name: "unknown code with auth status", metaCode: 999999, httpStatus: 403, wantCode: core.CodeUpstreamAuthFailed},
		{name: "unknown code with client error status", metaCode: 999999, httpStatus: 400, wantCode: core.CodeRejected},
		{name: "code zero from meta is an authentication failure", metaCode: 0, httpStatus: 400, wantCode: core.CodeUpstreamAuthFailed},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifyError(tt.metaCode, tt.httpStatus)
			if got.code != tt.wantCode || got.isRetryable != tt.wantRetryable {
				t.Fatalf("classifyError(%d, %d) = %+v, want %s retryable=%v", tt.metaCode, tt.httpStatus, got, tt.wantCode, tt.wantRetryable)
			}
		})
	}
}
