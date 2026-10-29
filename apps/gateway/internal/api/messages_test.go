package api

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/core"
)

const (
	testAPIKey       = "first-api-key-0123456789abcdef0123"
	secondTestAPIKey = "second-api-key-0123456789abcdef012"
)

type fakeMessageSender struct {
	sent   []core.OutboundMessage
	result core.SendResult
	err    error
}

func (f *fakeMessageSender) Send(_ context.Context, message core.OutboundMessage) (core.SendResult, error) {
	f.sent = append(f.sent, message)
	return f.result, f.err
}

func newSendRouter(sender MessageSender, recorder Recorder) http.Handler {
	return NewRouter(Deps{
		Ingestor: &fakeIngestor{},
		Recorder: recorder,
		Sender:   sender,
		APIKeys:  []string{testAPIKey, secondTestAPIKey},
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func postMessage(router http.Handler, authorization, body string, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/v1/messages", strings.NewReader(body))
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, request)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var decoded map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("response body %q is not json: %v", rec.Body.String(), err)
	}
	return decoded
}

func TestMessagesRequireAPIKey(t *testing.T) {
	const body = `{"to":"94770000000","type":"text","text":{"body":"hi"}}`

	tests := []struct {
		name          string
		authorization string
		wantStatus    int
		wantSent      int
	}{
		{name: "first key", authorization: "Bearer " + testAPIKey, wantStatus: http.StatusOK, wantSent: 1},
		{name: "second key", authorization: "Bearer " + secondTestAPIKey, wantStatus: http.StatusOK, wantSent: 1},
		{name: "scheme is case insensitive", authorization: "bearer " + testAPIKey, wantStatus: http.StatusOK, wantSent: 1},
		{name: "no header", authorization: "", wantStatus: http.StatusUnauthorized},
		{name: "wrong key", authorization: "Bearer not-a-real-key", wantStatus: http.StatusUnauthorized},
		{name: "key without scheme", authorization: testAPIKey, wantStatus: http.StatusUnauthorized},
		{name: "basic scheme", authorization: "Basic " + testAPIKey, wantStatus: http.StatusUnauthorized},
		{name: "empty token", authorization: "Bearer ", wantStatus: http.StatusUnauthorized},
		{name: "key with trailing characters", authorization: "Bearer " + testAPIKey + "x", wantStatus: http.StatusUnauthorized},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sender := &fakeMessageSender{result: core.SendResult{MessageID: "wamid.OK"}}
			rec := postMessage(newSendRouter(sender, &fakeRecorder{}), tt.authorization, body, nil)

			if rec.Code != tt.wantStatus || len(sender.sent) != tt.wantSent {
				t.Fatalf("status = %d, sent = %d, want %d and %d", rec.Code, len(sender.sent), tt.wantStatus, tt.wantSent)
			}
			if tt.wantStatus == http.StatusUnauthorized {
				if decoded := decodeBody(t, rec); decoded["code"] != "unauthorized" || decoded["retryable"] != false {
					t.Errorf("body = %v", decoded)
				}
				if rec.Header().Get("WWW-Authenticate") != "Bearer" {
					t.Errorf("WWW-Authenticate = %q", rec.Header().Get("WWW-Authenticate"))
				}
			}
		})
	}
}

func TestSendRoutesAreAbsentWithoutASender(t *testing.T) {
	tests := []struct {
		name          string
		authorization string
	}{
		{name: "request with a key", authorization: "Bearer " + testAPIKey},
		{name: "request without a key", authorization: ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := postMessage(newTestRouter(&fakeIngestor{}), tt.authorization, `{}`, nil)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", rec.Code)
			}
		})
	}
}

func TestSendMessageParsing(t *testing.T) {
	tests := []struct {
		name            string
		body            string
		headers         map[string]string
		wantStatus      int
		wantProblem     string
		wantMessage     core.OutboundMessage
		wantCorrelation string
	}{
		{
			name:       "text message",
			body:       `{"to":"94770000000","type":"text","text":{"body":"hi"}}`,
			wantStatus: http.StatusOK,
			wantMessage: core.OutboundMessage{
				To: "94770000000", Type: core.MessageText, Content: json.RawMessage(`{"body":"hi"}`),
			},
		},
		{
			name:       "template with the number and a correlation id in the body",
			body:       `{"to":"94770000000","type":"template","phone_number_id":"111","correlation_id":"order-1","template":{"name":"x","language":{"code":"en"}}}`,
			wantStatus: http.StatusOK,
			wantMessage: core.OutboundMessage{
				To: "94770000000", Type: core.MessageTemplate, PhoneNumberID: "111", CorrelationID: "order-1",
				Content: json.RawMessage(`{"name":"x","language":{"code":"en"}}`),
			},
			wantCorrelation: "order-1",
		},
		{
			name:       "correlation id from the header",
			body:       `{"to":"94770000000","type":"text","text":{"body":"hi"}}`,
			headers:    map[string]string{"X-Correlation-Id": "from-header"},
			wantStatus: http.StatusOK,
			wantMessage: core.OutboundMessage{
				To: "94770000000", Type: core.MessageText, CorrelationID: "from-header", Content: json.RawMessage(`{"body":"hi"}`),
			},
			wantCorrelation: "from-header",
		},
		{
			name:       "body correlation id wins over the header",
			body:       `{"to":"94770000000","type":"text","correlation_id":"from-body","text":{"body":"hi"}}`,
			headers:    map[string]string{"X-Correlation-Id": "from-header"},
			wantStatus: http.StatusOK,
			wantMessage: core.OutboundMessage{
				To: "94770000000", Type: core.MessageText, CorrelationID: "from-body", Content: json.RawMessage(`{"body":"hi"}`),
			},
			wantCorrelation: "from-body",
		},
		{name: "invalid json", body: `{`, wantStatus: http.StatusBadRequest, wantProblem: "must be a JSON object"},
		{name: "json array instead of object", body: `[]`, wantStatus: http.StatusBadRequest, wantProblem: "must be a JSON object"},
		{name: "json null", body: `null`, wantStatus: http.StatusBadRequest, wantProblem: "must be a JSON object"},
		{name: "missing type", body: `{"to":"94770000000"}`, wantStatus: http.StatusBadRequest, wantProblem: "type is required"},
		{name: "type is not a string", body: `{"to":"94770000000","type":5}`, wantStatus: http.StatusBadRequest, wantProblem: "type must be a string"},
		{name: "to is a number", body: `{"to":94770000000,"type":"text","text":{"body":"hi"}}`, wantStatus: http.StatusBadRequest, wantProblem: "to must be a string"},
		{name: "unknown top level field", body: `{"to":"94770000000","type":"text","text":{"body":"hi"},"idempotency_key":"k"}`, wantStatus: http.StatusBadRequest, wantProblem: `unknown field "idempotency_key"`},
		{name: "content for a different type", body: `{"to":"94770000000","type":"text","text":{"body":"hi"},"image":{"id":"1"}}`, wantStatus: http.StatusBadRequest, wantProblem: `unknown field "image"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sender := &fakeMessageSender{result: core.SendResult{MessageID: "wamid.OK"}}
			rec := postMessage(newSendRouter(sender, &fakeRecorder{}), "Bearer "+testAPIKey, tt.body, tt.headers)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			decoded := decodeBody(t, rec)

			if tt.wantProblem != "" {
				if len(sender.sent) != 0 {
					t.Errorf("sender was called for a rejected request")
				}
				if decoded["code"] != "invalid_request" || !strings.Contains(decoded["message"].(string), tt.wantProblem) {
					t.Errorf("body = %v, want invalid_request containing %q", decoded, tt.wantProblem)
				}
				return
			}

			if len(sender.sent) != 1 {
				t.Fatalf("sent = %d messages, want 1", len(sender.sent))
			}
			got := sender.sent[0]
			if got.To != tt.wantMessage.To || got.Type != tt.wantMessage.Type || got.PhoneNumberID != tt.wantMessage.PhoneNumberID ||
				got.CorrelationID != tt.wantMessage.CorrelationID || string(got.Content) != string(tt.wantMessage.Content) {
				t.Errorf("message = %+v, want %+v", got, tt.wantMessage)
			}
			if decoded["wamid"] != "wamid.OK" {
				t.Errorf("wamid = %v", decoded["wamid"])
			}
			if tt.wantCorrelation != "" {
				if decoded["correlation_id"] != tt.wantCorrelation || rec.Header().Get("X-Correlation-Id") != tt.wantCorrelation {
					t.Errorf("correlation id body/header = %v/%q", decoded["correlation_id"], rec.Header().Get("X-Correlation-Id"))
				}
			}
		})
	}
}

func TestSendMessageErrors(t *testing.T) {
	tests := []struct {
		name          string
		err           error
		wantStatus    int
		wantCode      string
		wantRetryable bool
		wantMetaCode  float64
		wantResult    string
	}{
		{name: "invalid request from core", err: &core.SendError{Code: core.CodeInvalidRequest, Message: "text.body is required"}, wantStatus: 400, wantCode: "invalid_request", wantResult: "text:invalid_request"},
		{name: "window closed", err: &core.SendError{Code: core.CodeWindowClosed, Message: "closed", MetaCode: 131047}, wantStatus: 422, wantCode: "window_closed", wantMetaCode: 131047, wantResult: "text:window_closed"},
		{name: "recipient undeliverable", err: &core.SendError{Code: core.CodeRecipientUndeliverable, Message: "x", MetaCode: 131026}, wantStatus: 422, wantCode: "recipient_undeliverable", wantMetaCode: 131026, wantResult: "text:recipient_undeliverable"},
		{name: "recipient not allowed", err: &core.SendError{Code: core.CodeRecipientNotAllowed, Message: "x", MetaCode: 131030}, wantStatus: 422, wantCode: "recipient_not_allowed", wantMetaCode: 131030, wantResult: "text:recipient_not_allowed"},
		{name: "template error", err: &core.SendError{Code: core.CodeTemplateError, Message: "x", MetaCode: 132001}, wantStatus: 422, wantCode: "template_error", wantMetaCode: 132001, wantResult: "text:template_error"},
		{name: "sending restricted", err: &core.SendError{Code: core.CodeSendingRestricted, Message: "x"}, wantStatus: 422, wantCode: "sending_restricted", wantResult: "text:sending_restricted"},
		{name: "generic rejection", err: &core.SendError{Code: core.CodeRejected, Message: "x"}, wantStatus: 422, wantCode: "rejected", wantResult: "text:rejected"},
		{name: "rate limited", err: &core.SendError{Code: core.CodeRateLimited, Message: "slow down", IsRetryable: true, MetaCode: 130429}, wantStatus: 429, wantCode: "rate_limited", wantRetryable: true, wantMetaCode: 130429, wantResult: "text:rate_limited"},
		{name: "upstream unavailable", err: &core.SendError{Code: core.CodeUpstreamUnavailable, Message: "down", IsRetryable: true}, wantStatus: 502, wantCode: "upstream_unavailable", wantRetryable: true, wantResult: "text:upstream_unavailable"},
		{name: "upstream auth failed", err: &core.SendError{Code: core.CodeUpstreamAuthFailed, Message: "token", MetaCode: 190}, wantStatus: 502, wantCode: "upstream_auth_failed", wantMetaCode: 190, wantResult: "text:upstream_auth_failed"},
		{name: "sender misconfigured", err: &core.SendError{Code: core.CodeSenderMisconfigured, Message: "x"}, wantStatus: 502, wantCode: "sender_misconfigured", wantResult: "text:sender_misconfigured"},
		{name: "upstream timeout", err: &core.SendError{Code: core.CodeUpstreamTimeout, Message: "slow", IsRetryable: true}, wantStatus: 504, wantCode: "upstream_timeout", wantRetryable: true, wantResult: "text:upstream_timeout"},
		{name: "request in progress", err: &core.SendError{Code: core.CodeRequestInProgress, Message: "busy", IsRetryable: true}, wantStatus: 409, wantCode: "request_in_progress", wantRetryable: true, wantResult: "text:request_in_progress"},
		{name: "idempotency key reused", err: &core.SendError{Code: core.CodeIdempotencyKeyReused, Message: "reused"}, wantStatus: 422, wantCode: "idempotency_key_reused", wantResult: "text:idempotency_key_reused"},
		{name: "unexpected error", err: errors.New("boom"), wantStatus: 500, wantCode: "internal_error", wantResult: "text:internal_error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := &fakeRecorder{}
			sender := &fakeMessageSender{err: tt.err}

			rec := postMessage(newSendRouter(sender, recorder), "Bearer "+testAPIKey, `{"to":"94770000000","type":"text","text":{"body":"hi"}}`, nil)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			decoded := decodeBody(t, rec)
			if decoded["code"] != tt.wantCode || decoded["retryable"] != tt.wantRetryable {
				t.Errorf("body = %v", decoded)
			}
			if tt.wantMetaCode != 0 && decoded["meta_code"] != tt.wantMetaCode {
				t.Errorf("meta_code = %v, want %v", decoded["meta_code"], tt.wantMetaCode)
			}
			if tt.wantMetaCode == 0 {
				if _, present := decoded["meta_code"]; present {
					t.Errorf("meta_code should be omitted, body = %v", decoded)
				}
			}
			if strings.Contains(rec.Body.String(), "boom") {
				t.Errorf("internal error text leaked to the caller: %s", rec.Body.String())
			}
			if len(recorder.sends) != 1 || recorder.sends[0] != tt.wantResult {
				t.Errorf("recorded sends = %v, want [%s]", recorder.sends, tt.wantResult)
			}
		})
	}
}

func TestSendMessageRecordsMetrics(t *testing.T) {
	tests := []struct {
		name       string
		body       string
		sendErr    error
		wantRecord string
	}{
		{name: "successful template", body: `{"to":"94770000000","type":"template","template":{"name":"x","language":{"code":"en"}}}`, wantRecord: "template:sent"},
		{name: "unknown type is not used as a label", body: `{"to":"94770000000","type":"made-up-type","made-up-type":{}}`, wantRecord: "unknown:invalid_request"},
		{name: "malformed body has no type", body: `{`, wantRecord: "unknown:invalid_request"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := &fakeRecorder{}
			sender := &fakeMessageSender{result: core.SendResult{MessageID: "wamid.OK"}, err: tt.sendErr}
			if strings.Contains(tt.body, "made-up-type") {
				sender.err = &core.SendError{Code: core.CodeInvalidRequest, Message: "type is not supported"}
			}

			postMessage(newSendRouter(sender, recorder), "Bearer "+testAPIKey, tt.body, nil)

			if len(recorder.sends) != 1 || recorder.sends[0] != tt.wantRecord {
				t.Fatalf("recorded sends = %v, want [%s]", recorder.sends, tt.wantRecord)
			}
		})
	}
}

func TestSendMessageBodyLimit(t *testing.T) {
	tests := []struct {
		name       string
		textSize   int
		wantStatus int
		wantSent   int
		wantCode   string
	}{
		{name: "large body within the limit", textSize: 1 << 19, wantStatus: http.StatusOK, wantSent: 1},
		{name: "body over the limit", textSize: 1 << 20, wantStatus: http.StatusRequestEntityTooLarge, wantSent: 0, wantCode: "payload_too_large"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sender := &fakeMessageSender{result: core.SendResult{MessageID: "wamid.OK"}}
			body := `{"to":"94770000000","type":"text","text":{"body":"` + strings.Repeat("a", tt.textSize) + `"}}`

			rec := postMessage(newSendRouter(sender, &fakeRecorder{}), "Bearer "+testAPIKey, body, nil)

			if rec.Code != tt.wantStatus || len(sender.sent) != tt.wantSent {
				t.Fatalf("status = %d, sent = %d, want %d and %d", rec.Code, len(sender.sent), tt.wantStatus, tt.wantSent)
			}
			if tt.wantCode != "" {
				if decoded := decodeBody(t, rec); decoded["code"] != tt.wantCode {
					t.Errorf("body = %v", decoded)
				}
			}
		})
	}
}

func TestIdempotencyKeyHeader(t *testing.T) {
	const body = `{"to":"94770000000","type":"text","text":{"body":"hi"}}`

	tests := []struct {
		name         string
		header       string
		wantStatus   int
		wantKey      string
		wantReplayed bool
		replay       bool
	}{
		{name: "no header means no key", header: "", wantStatus: http.StatusOK, wantKey: ""},
		{name: "plain key", header: "order-1234", wantStatus: http.StatusOK, wantKey: "order-1234"},
		{name: "uuid style key", header: "3f2b8c1e-9a4d-4e7b-8f10-6c5d2a1b9e07", wantStatus: http.StatusOK, wantKey: "3f2b8c1e-9a4d-4e7b-8f10-6c5d2a1b9e07"},
		{name: "longest allowed key", header: strings.Repeat("k", 255), wantStatus: http.StatusOK, wantKey: strings.Repeat("k", 255)},
		{name: "key that is too long", header: strings.Repeat("k", 256), wantStatus: http.StatusBadRequest},
		{name: "key with a space", header: "order 1", wantStatus: http.StatusBadRequest},
		{name: "key with a control character", header: "order\t1", wantStatus: http.StatusBadRequest},
		{name: "replayed send is flagged", header: "order-1", wantStatus: http.StatusOK, wantKey: "order-1", replay: true, wantReplayed: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder := &fakeRecorder{}
			sender := &fakeMessageSender{result: core.SendResult{MessageID: "wamid.OK", IsReplay: tt.replay}}
			headers := map[string]string{}
			if tt.header != "" {
				headers["Idempotency-Key"] = tt.header
			}

			rec := postMessage(newSendRouter(sender, recorder), "Bearer "+testAPIKey, body, headers)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if tt.wantStatus != http.StatusOK {
				if len(sender.sent) != 0 {
					t.Error("sender was called for a rejected key")
				}
				return
			}
			if len(sender.sent) != 1 || sender.sent[0].IdempotencyKey != tt.wantKey {
				t.Fatalf("sent = %+v, want key %q", sender.sent, tt.wantKey)
			}
			if gotReplayed := rec.Header().Get("Idempotent-Replayed") == "true"; gotReplayed != tt.wantReplayed {
				t.Errorf("replayed header = %v, want %v", gotReplayed, tt.wantReplayed)
			}
			wantRecord := "text:sent"
			if tt.wantReplayed {
				wantRecord = "text:replayed"
			}
			if len(recorder.sends) != 1 || recorder.sends[0] != wantRecord {
				t.Errorf("recorded sends = %v, want [%s]", recorder.sends, wantRecord)
			}
		})
	}
}
