package api

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/crypto"
)

const validWebhookBody = `{"object":"whatsapp_business_account","entry":[{"id":"W1","changes":[{"field":"messages","value":{"metadata":{"phone_number_id":"P1"},"messages":[{"from":"94770000000","id":"wamid.1","timestamp":"1790000000","type":"text"}]}}]}]}`

func signed(body string) string {
	return "sha256=" + crypto.SignHMACSHA256([]byte("app-secret"), []byte(body))
}

func TestVerifyWebhook(t *testing.T) {
	tests := []struct {
		name       string
		query      string
		wantStatus int
		wantBody   string
	}{
		{name: "valid handshake", query: "hub.mode=subscribe&hub.verify_token=verify-token&hub.challenge=12345", wantStatus: http.StatusOK, wantBody: "12345"},
		{name: "wrong token", query: "hub.mode=subscribe&hub.verify_token=nope&hub.challenge=12345", wantStatus: http.StatusForbidden},
		{name: "wrong mode", query: "hub.mode=other&hub.verify_token=verify-token&hub.challenge=12345", wantStatus: http.StatusForbidden},
		{name: "no parameters", query: "", wantStatus: http.StatusForbidden},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := newTestRouter(&fakeIngestor{})
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/webhook?"+tt.query, nil))

			if rec.Code != tt.wantStatus || rec.Body.String() != tt.wantBody {
				t.Fatalf("response = %d %q, want %d %q", rec.Code, rec.Body.String(), tt.wantStatus, tt.wantBody)
			}
		})
	}
}

func TestReceiveWebhook(t *testing.T) {
	tests := []struct {
		name         string
		body         string
		signature    string
		ingestErr    error
		wantStatus   int
		wantIngested int
		wantResult   string
	}{
		{name: "valid message", body: validWebhookBody, signature: signed(validWebhookBody), wantStatus: http.StatusOK, wantIngested: 1, wantResult: "accepted"},
		{name: "missing signature", body: validWebhookBody, signature: "", wantStatus: http.StatusUnauthorized, wantResult: "invalid_signature"},
		{name: "wrong signature", body: validWebhookBody, signature: "sha256=00", wantStatus: http.StatusUnauthorized, wantResult: "invalid_signature"},
		{name: "malformed json", body: "{", signature: signed("{"), wantStatus: http.StatusBadRequest, wantResult: "malformed"},
		{name: "unrelated object", body: `{"object":"page"}`, signature: signed(`{"object":"page"}`), wantStatus: http.StatusOK, wantResult: "ignored"},
		{name: "persistence failure", body: validWebhookBody, signature: signed(validWebhookBody), ingestErr: errors.New("disk full"), wantStatus: http.StatusInternalServerError, wantResult: "persist_error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ingestor := &fakeIngestor{ingestErr: tt.ingestErr}
			recorder := &fakeRecorder{}
			router := newRecordingRouter(ingestor, recorder)

			request := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(tt.body))
			if tt.signature != "" {
				request.Header.Set("X-Hub-Signature-256", tt.signature)
			}
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, request)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
			ingested := 0
			for _, batch := range ingestor.ingested {
				ingested += len(batch)
			}
			if ingested != tt.wantIngested {
				t.Fatalf("ingested = %d, want %d", ingested, tt.wantIngested)
			}
			if len(recorder.webhookResults) != 1 || recorder.webhookResults[0] != tt.wantResult {
				t.Fatalf("webhook results = %v, want [%s]", recorder.webhookResults, tt.wantResult)
			}
		})
	}
}

func TestReceiveWebhookRecordsQueuedAndDuplicateEvents(t *testing.T) {
	ingestor := &fakeIngestor{duplicates: map[string]bool{"msg:wamid.1": true}}
	recorder := &fakeRecorder{}
	router := newRecordingRouter(ingestor, recorder)

	request := httptest.NewRequest(http.MethodPost, "/webhook", strings.NewReader(validWebhookBody))
	request.Header.Set("X-Hub-Signature-256", signed(validWebhookBody))
	router.ServeHTTP(httptest.NewRecorder(), request)

	if len(recorder.events) != 1 || recorder.events[0] != "message.inbound:duplicate" {
		t.Fatalf("recorded events = %v, want [message.inbound:duplicate]", recorder.events)
	}
}
