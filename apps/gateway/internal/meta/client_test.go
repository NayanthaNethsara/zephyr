package meta

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/core"
)

type capturedSend struct {
	path          string
	authorization string
	contentType   string
	body          map[string]any
}

func textMessage() core.OutboundMessage {
	return core.OutboundMessage{
		To:      "94770000000",
		Type:    core.MessageText,
		Content: json.RawMessage(`{"body":"hello","preview_url":true}`),
	}
}

func TestClientSendRequestShape(t *testing.T) {
	tests := []struct {
		name     string
		message  core.OutboundMessage
		wantType string
		wantBody map[string]any
	}{
		{
			name:     "text message",
			message:  textMessage(),
			wantType: "text",
			wantBody: map[string]any{"body": "hello", "preview_url": true},
		},
		{
			name: "template message keeps its components untouched",
			message: core.OutboundMessage{
				To:      "94770000000",
				Type:    core.MessageTemplate,
				Content: json.RawMessage(`{"name":"order_confirm","language":{"code":"en_US"},"components":[{"type":"body","parameters":[{"type":"text","text":"1234"}]}]}`),
			},
			wantType: "template",
			wantBody: map[string]any{
				"name":     "order_confirm",
				"language": map[string]any{"code": "en_US"},
				"components": []any{
					map[string]any{"type": "body", "parameters": []any{map[string]any{"type": "text", "text": "1234"}}},
				},
			},
		},
		{
			name:     "contacts message is an array",
			message:  core.OutboundMessage{To: "94770000000", Type: core.MessageContacts, Content: json.RawMessage(`[{"name":{"formatted_name":"Sam"}}]`)},
			wantType: "contacts",
			wantBody: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var captured capturedSend
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				captured.path = r.URL.Path
				captured.authorization = r.Header.Get("Authorization")
				captured.contentType = r.Header.Get("Content-Type")
				raw, _ := io.ReadAll(r.Body)
				_ = json.Unmarshal(raw, &captured.body)
				_, _ = w.Write([]byte(`{"messaging_product":"whatsapp","contacts":[{"input":"94770000000","wa_id":"94770000000"}],"messages":[{"id":"wamid.SENT","message_status":"accepted"}]}`))
			}))
			defer server.Close()

			result, err := NewClient(server.URL, "v24.0", "secret-token").Send(context.Background(), "111", tt.message)
			if err != nil {
				t.Fatalf("Send() error = %v", err)
			}
			if result.MessageID != "wamid.SENT" {
				t.Errorf("message id = %q", result.MessageID)
			}
			if captured.path != "/v24.0/111/messages" {
				t.Errorf("path = %s", captured.path)
			}
			if captured.authorization != "Bearer secret-token" || captured.contentType != "application/json" {
				t.Errorf("authorization/content type = %q/%q", captured.authorization, captured.contentType)
			}
			if captured.body["messaging_product"] != "whatsapp" || captured.body["recipient_type"] != "individual" ||
				captured.body["to"] != "94770000000" || captured.body["type"] != tt.wantType {
				t.Errorf("body envelope = %v", captured.body)
			}
			if tt.wantBody != nil && !jsonEqual(captured.body[tt.wantType], tt.wantBody) {
				t.Errorf("body[%s] = %v, want %v", tt.wantType, captured.body[tt.wantType], tt.wantBody)
			}
			if tt.wantBody == nil {
				if _, isArray := captured.body[tt.wantType].([]any); !isArray {
					t.Errorf("body[%s] = %v, want an array", tt.wantType, captured.body[tt.wantType])
				}
			}
		})
	}
}

func jsonEqual(a, b any) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return string(left) == string(right)
}

func TestClientSendResponses(t *testing.T) {
	tests := []struct {
		name          string
		status        int
		body          string
		wantMessageID string
		wantCode      string
		wantRetryable bool
		wantMetaCode  int
		wantTraceID   string
		wantMessage   string
	}{
		{
			name:          "accepted message",
			status:        200,
			body:          `{"messages":[{"id":"wamid.OK"}]}`,
			wantMessageID: "wamid.OK",
		},
		{
			name:         "window closed uses the error details",
			status:       400,
			body:         `{"error":{"message":"(#131047) Re-engagement message","type":"OAuthException","code":131047,"error_data":{"messaging_product":"whatsapp","details":"More than 24 hours have passed"},"fbtrace_id":"trace1"}}`,
			wantCode:     core.CodeWindowClosed,
			wantMetaCode: 131047,
			wantTraceID:  "trace1",
			wantMessage:  "More than 24 hours have passed",
		},
		{
			name:         "falls back to the error message without details",
			status:       400,
			body:         `{"error":{"message":"Invalid parameter","code":100,"fbtrace_id":"trace2"}}`,
			wantCode:     core.CodeInvalidRequest,
			wantMetaCode: 100,
			wantTraceID:  "trace2",
			wantMessage:  "Invalid parameter",
		},
		{
			name:          "throughput limit is retryable",
			status:        429,
			body:          `{"error":{"message":"Throughput reached","code":130429}}`,
			wantCode:      core.CodeRateLimited,
			wantRetryable: true,
			wantMetaCode:  130429,
			wantMessage:   "Throughput reached",
		},
		{
			name:          "error with an empty json object from a proxy",
			status:        503,
			body:          `{}`,
			wantCode:      core.CodeUpstreamUnavailable,
			wantRetryable: true,
			wantMessage:   "Meta responded with status 503",
		},
		{
			name:          "html error page from a gateway",
			status:        502,
			body:          `<html>bad gateway</html>`,
			wantCode:      core.CodeUpstreamUnavailable,
			wantRetryable: true,
			wantMessage:   "Meta responded with status 502",
		},
		{
			name:          "success without a message id",
			status:        200,
			body:          `{"messages":[]}`,
			wantCode:      core.CodeUpstreamUnavailable,
			wantRetryable: true,
			wantMessage:   "Meta returned an unexpected response",
		},
		{
			name:          "success with an unreadable body",
			status:        200,
			body:          `not json`,
			wantCode:      core.CodeUpstreamUnavailable,
			wantRetryable: true,
			wantMessage:   "Meta returned an unexpected response",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = w.Write([]byte(tt.body))
			}))
			defer server.Close()

			result, err := NewClient(server.URL, "v24.0", "token").Send(context.Background(), "111", textMessage())

			if tt.wantCode == "" {
				if err != nil || result.MessageID != tt.wantMessageID {
					t.Fatalf("Send() = %+v, %v, want %s", result, err, tt.wantMessageID)
				}
				return
			}
			var sendErr *core.SendError
			if !errors.As(err, &sendErr) {
				t.Fatalf("error = %#v, want a SendError", err)
			}
			if sendErr.Code != tt.wantCode || sendErr.IsRetryable != tt.wantRetryable || sendErr.MetaCode != tt.wantMetaCode ||
				sendErr.TraceID != tt.wantTraceID || sendErr.Message != tt.wantMessage {
				t.Fatalf("SendError = %+v", sendErr)
			}
		})
	}
}

func TestClientSendTransportFailures(t *testing.T) {
	tests := []struct {
		name     string
		setup    func(t *testing.T) (*Client, context.Context)
		wantCode string
	}{
		{
			name: "slow response times out",
			setup: func(t *testing.T) (*Client, context.Context) {
				server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					select {
					case <-r.Context().Done():
					case <-time.After(2 * time.Second):
					}
				}))
				t.Cleanup(server.Close)
				client := NewClient(server.URL, "v24.0", "token")
				client.httpClient.Timeout = 50 * time.Millisecond
				return client, context.Background()
			},
			wantCode: core.CodeUpstreamTimeout,
		},
		{
			name: "expired caller deadline times out",
			setup: func(t *testing.T) (*Client, context.Context) {
				server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
					_, _ = io.Copy(io.Discard, r.Body)
					<-r.Context().Done()
				}))
				t.Cleanup(server.Close)
				ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
				t.Cleanup(cancel)
				return NewClient(server.URL, "v24.0", "token"), ctx
			},
			wantCode: core.CodeUpstreamTimeout,
		},
		{
			name: "unreachable server",
			setup: func(t *testing.T) (*Client, context.Context) {
				server := httptest.NewServer(http.NotFoundHandler())
				url := server.URL
				server.Close()
				return NewClient(url, "v24.0", "token"), context.Background()
			},
			wantCode: core.CodeUpstreamUnavailable,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, ctx := tt.setup(t)

			_, err := client.Send(ctx, "111", textMessage())

			var sendErr *core.SendError
			if !errors.As(err, &sendErr) || sendErr.Code != tt.wantCode || !sendErr.IsRetryable {
				t.Fatalf("error = %#v, want retryable %s", err, tt.wantCode)
			}
			if strings.Contains(sendErr.Message, "127.0.0.1") || strings.Contains(sendErr.Message, "http") {
				t.Errorf("message leaks the endpoint: %q", sendErr.Message)
			}
		})
	}
}
