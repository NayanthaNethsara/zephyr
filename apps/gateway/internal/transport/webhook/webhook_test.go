package webhook

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/crypto"
)

const testPayload = `{"id":"evt_1","type":"message.inbound","data":{}}`

type capturedRequest struct {
	body   []byte
	header http.Header
}

func TestPublish(t *testing.T) {
	now := time.Unix(1790000000, 0)

	tests := []struct {
		name          string
		handler       http.HandlerFunc
		closeServer   bool
		payload       string
		wantErr       bool
		wantDelivered bool
	}{
		{
			name:          "success with 2xx",
			handler:       func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) },
			payload:       testPayload,
			wantDelivered: true,
		},
		{
			name:    "server error is a failure",
			handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) },
			payload: testPayload,
			wantErr: true,
		},
		{
			name:    "client error is a failure",
			handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadRequest) },
			payload: testPayload,
			wantErr: true,
		},
		{
			name: "redirect is not followed",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Redirect(w, r, "/elsewhere", http.StatusFound)
			},
			payload: testPayload,
			wantErr: true,
		},
		{
			name:    "invalid envelope is rejected before sending",
			handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) },
			payload: "not json",
			wantErr: true,
		},
		{
			name:        "unreachable endpoint is a failure",
			handler:     func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) },
			closeServer: true,
			payload:     testPayload,
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var captured capturedRequest
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				captured.body, _ = io.ReadAll(r.Body)
				captured.header = r.Header.Clone()
				tt.handler(w, r)
			}))
			defer server.Close()

			target := server.URL + "/hook?token=super-secret"
			if tt.closeServer {
				server.Close()
			}

			transport := New(target, "signing-secret")
			transport.now = func() time.Time { return now }

			err := transport.Publish(context.Background(), "wa.inbound", "key", []byte(tt.payload))
			if (err != nil) != tt.wantErr {
				t.Fatalf("Publish() error = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && strings.Contains(err.Error(), "super-secret") {
				t.Errorf("error leaks the webhook url: %v", err)
			}
			if tt.wantDelivered {
				assertSignedDelivery(t, captured, now)
			}
		})
	}
}

func assertSignedDelivery(t *testing.T, captured capturedRequest, now time.Time) {
	t.Helper()

	if string(captured.body) != testPayload {
		t.Errorf("body = %s", captured.body)
	}
	if captured.header.Get(eventHeader) != "message.inbound" || captured.header.Get(deliveryHeader) != "evt_1" {
		t.Errorf("headers = %v", captured.header)
	}
	timestamp := captured.header.Get(timestampHeader)
	if timestamp != strconv.FormatInt(now.Unix(), 10) {
		t.Errorf("timestamp = %s", timestamp)
	}
	signature := strings.TrimPrefix(captured.header.Get(signatureHeader), signaturePrefix)
	if !crypto.VerifyHMACSHA256([]byte("signing-secret"), signature, []byte(timestamp+"."+testPayload)) {
		t.Error("signature does not verify against timestamp.body")
	}
}
