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

func TestPublishSignsAndDelivers(t *testing.T) {
	now := time.Unix(1790000000, 0)
	var gotBody []byte
	var gotHeader http.Header

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		gotHeader = r.Header.Clone()
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()

	transport := New(server.URL, "signing-secret")
	transport.now = func() time.Time { return now }

	if err := transport.Publish(context.Background(), "wa.inbound", "key", []byte(testPayload)); err != nil {
		t.Fatalf("Publish() error = %v", err)
	}

	if string(gotBody) != testPayload {
		t.Errorf("body = %s", gotBody)
	}
	if gotHeader.Get(eventHeader) != "message.inbound" || gotHeader.Get(deliveryHeader) != "evt_1" {
		t.Errorf("headers = %v", gotHeader)
	}
	timestamp := gotHeader.Get(timestampHeader)
	if timestamp != strconv.FormatInt(now.Unix(), 10) {
		t.Errorf("timestamp = %s", timestamp)
	}
	signature := gotHeader.Get(signatureHeader)[len(signaturePrefix):]
	if !crypto.VerifyHMACSHA256([]byte("signing-secret"), signature, []byte(timestamp+"."+testPayload)) {
		t.Error("signature does not verify against timestamp.body")
	}
}

func TestPublishFailures(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{name: "server error", handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusInternalServerError) }},
		{name: "client error", handler: func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadRequest) }},
		{name: "redirect is not followed", handler: func(w http.ResponseWriter, r *http.Request) {
			http.Redirect(w, r, "/elsewhere", http.StatusFound)
		}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := httptest.NewServer(tt.handler)
			defer server.Close()

			if err := New(server.URL, "s").Publish(context.Background(), "t", "k", []byte(testPayload)); err == nil {
				t.Fatal("Publish() error = nil, want error")
			}
		})
	}
}

func TestPublishRejectsInvalidEnvelope(t *testing.T) {
	if err := New("http://127.0.0.1:1", "s").Publish(context.Background(), "t", "k", []byte("not json")); err == nil {
		t.Fatal("Publish() error = nil, want error")
	}
}

func TestPublishErrorDoesNotLeakURL(t *testing.T) {
	server := httptest.NewServer(http.NotFoundHandler())
	target := server.URL + "/hook?token=super-secret"
	server.Close()

	err := New(target, "s").Publish(context.Background(), "t", "k", []byte(testPayload))
	if err == nil {
		t.Fatal("Publish() error = nil, want error")
	}
	if strings.Contains(err.Error(), "super-secret") || strings.Contains(err.Error(), "token=") {
		t.Fatalf("error leaks the webhook url: %v", err)
	}
}
