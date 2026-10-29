package api

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/core"
	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/meta"
	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/store"
)

func TestSendThroughRealCoreAndMetaClient(t *testing.T) {
	tests := []struct {
		name          string
		graphStatus   int
		graphBody     string
		requestBody   string
		wantStatus    int
		wantCode      string
		wantRetryable bool
		wantWAMID     string
		wantGraphHits int
	}{
		{
			name:          "accepted by meta",
			graphStatus:   200,
			graphBody:     `{"messages":[{"id":"wamid.REAL","message_status":"accepted"}]}`,
			requestBody:   `{"to":"94770000000","type":"text","text":{"body":"hi"}}`,
			wantStatus:    http.StatusOK,
			wantWAMID:     "wamid.REAL",
			wantGraphHits: 1,
		},
		{
			name:          "meta says the window is closed",
			graphStatus:   400,
			graphBody:     `{"error":{"message":"(#131047) Re-engagement","code":131047,"error_data":{"details":"More than 24 hours have passed"}}}`,
			requestBody:   `{"to":"94770000000","type":"text","text":{"body":"hi"}}`,
			wantStatus:    http.StatusUnprocessableEntity,
			wantCode:      "window_closed",
			wantGraphHits: 1,
		},
		{
			name:          "meta throttles the sender",
			graphStatus:   429,
			graphBody:     `{"error":{"message":"Throughput limit","code":130429}}`,
			requestBody:   `{"to":"94770000000","type":"text","text":{"body":"hi"}}`,
			wantStatus:    http.StatusTooManyRequests,
			wantCode:      "rate_limited",
			wantRetryable: true,
			wantGraphHits: 1,
		},
		{
			name:          "meta is down",
			graphStatus:   503,
			graphBody:     `<html>service unavailable</html>`,
			requestBody:   `{"to":"94770000000","type":"text","text":{"body":"hi"}}`,
			wantStatus:    http.StatusBadGateway,
			wantCode:      "upstream_unavailable",
			wantRetryable: true,
			wantGraphHits: 1,
		},
		{
			name:          "invalid message never reaches meta",
			graphStatus:   200,
			graphBody:     `{"messages":[{"id":"wamid.UNEXPECTED"}]}`,
			requestBody:   `{"to":"94770000000","type":"text","text":{}}`,
			wantStatus:    http.StatusBadRequest,
			wantCode:      "invalid_request",
			wantGraphHits: 0,
		},
		{
			name:          "other phone number never reaches meta",
			graphStatus:   200,
			graphBody:     `{"messages":[{"id":"wamid.UNEXPECTED"}]}`,
			requestBody:   `{"to":"94770000000","type":"text","phone_number_id":"999","text":{"body":"hi"}}`,
			wantStatus:    http.StatusBadRequest,
			wantCode:      "invalid_request",
			wantGraphHits: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			graphHits := 0
			graph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				graphHits++
				if r.URL.Path != "/v24.0/111/messages" || r.Header.Get("Authorization") != "Bearer meta-token" {
					t.Errorf("unexpected graph request %s with %q", r.URL.Path, r.Header.Get("Authorization"))
				}
				w.WriteHeader(tt.graphStatus)
				_, _ = w.Write([]byte(tt.graphBody))
			}))
			defer graph.Close()

			sender := core.NewSender(meta.NewClient(graph.URL, "v24.0", "meta-token"), newMemoryIdempotencyStore(), "111", time.Hour)
			router := NewRouter(Deps{
				Ingestor: &fakeIngestor{},
				Recorder: &fakeRecorder{},
				Sender:   sender,
				APIKeys:  []string{testAPIKey},
				Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
			})

			rec := postMessage(router, "Bearer "+testAPIKey, tt.requestBody, nil)

			if rec.Code != tt.wantStatus || graphHits != tt.wantGraphHits {
				t.Fatalf("status = %d, graph hits = %d, want %d and %d (body %s)", rec.Code, graphHits, tt.wantStatus, tt.wantGraphHits, rec.Body.String())
			}
			decoded := decodeBody(t, rec)
			if tt.wantWAMID != "" && decoded["wamid"] != tt.wantWAMID {
				t.Errorf("wamid = %v", decoded["wamid"])
			}
			if tt.wantCode != "" && (decoded["code"] != tt.wantCode || decoded["retryable"] != tt.wantRetryable) {
				t.Errorf("body = %v", decoded)
			}
			if strings.Contains(rec.Body.String(), "meta-token") {
				t.Errorf("access token leaked to the caller: %s", rec.Body.String())
			}
		})
	}
}

type memoryIdempotencyStore struct {
	mu      sync.Mutex
	records map[string]store.IdempotencyResult
	prints  map[string]string
}

func newMemoryIdempotencyStore() *memoryIdempotencyStore {
	return &memoryIdempotencyStore{records: map[string]store.IdempotencyResult{}, prints: map[string]string{}}
}

func (m *memoryIdempotencyStore) ClaimIdempotencyKey(_ context.Context, claim store.IdempotencyClaim) (store.IdempotencyResult, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	existing, found := m.records[claim.Key]
	switch {
	case !found:
		m.records[claim.Key] = store.IdempotencyResult{Outcome: store.IdempotencyInProgress}
		m.prints[claim.Key] = claim.Fingerprint
		return store.IdempotencyResult{Outcome: store.IdempotencyAcquired}, nil
	case m.prints[claim.Key] != claim.Fingerprint:
		return store.IdempotencyResult{Outcome: store.IdempotencyMismatch}, nil
	default:
		return existing, nil
	}
}

func (m *memoryIdempotencyStore) CompleteIdempotencyKey(_ context.Context, key, messageID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.records[key] = store.IdempotencyResult{Outcome: store.IdempotencyCompleted, MessageID: messageID}
	return nil
}

func (m *memoryIdempotencyStore) ReleaseIdempotencyKey(_ context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.records[key].Outcome == store.IdempotencyInProgress {
		delete(m.records, key)
		delete(m.prints, key)
	}
	return nil
}

func TestIdempotentSendThroughRealCoreAndMetaClient(t *testing.T) {
	const (
		textA = `{"to":"94770000000","type":"text","text":{"body":"first"}}`
		textB = `{"to":"94770000000","type":"text","text":{"body":"second"}}`
	)

	type call struct {
		key           string
		body          string
		graphStatus   int
		wantStatus    int
		wantCode      string
		wantReplayed  bool
		wantWAMID     string
		wantGraphHits int
	}

	accepted := func(id string) string { return `{"messages":[{"id":"` + id + `"}]}` }

	tests := []struct {
		name  string
		calls []call
	}{
		{
			name: "same key and message sends once and replays the result",
			calls: []call{
				{key: "order-1", body: textA, graphStatus: 200, wantStatus: 200, wantWAMID: "wamid.ONE", wantGraphHits: 1},
				{key: "order-1", body: textA, graphStatus: 200, wantStatus: 200, wantWAMID: "wamid.ONE", wantReplayed: true, wantGraphHits: 1},
				{key: "order-1", body: textA, graphStatus: 200, wantStatus: 200, wantWAMID: "wamid.ONE", wantReplayed: true, wantGraphHits: 1},
			},
		},
		{
			name: "no key sends every time",
			calls: []call{
				{body: textA, graphStatus: 200, wantStatus: 200, wantWAMID: "wamid.ONE", wantGraphHits: 1},
				{body: textA, graphStatus: 200, wantStatus: 200, wantWAMID: "wamid.ONE", wantGraphHits: 2},
			},
		},
		{
			name: "same key with a different message is rejected without sending",
			calls: []call{
				{key: "order-1", body: textA, graphStatus: 200, wantStatus: 200, wantWAMID: "wamid.ONE", wantGraphHits: 1},
				{key: "order-1", body: textB, graphStatus: 200, wantStatus: 422, wantCode: "idempotency_key_reused", wantGraphHits: 1},
			},
		},
		{
			name: "failed send releases the key so the retry reaches meta",
			calls: []call{
				{key: "order-1", body: textA, graphStatus: 503, wantStatus: 502, wantCode: "upstream_unavailable", wantGraphHits: 1},
				{key: "order-1", body: textA, graphStatus: 200, wantStatus: 200, wantWAMID: "wamid.ONE", wantGraphHits: 2},
				{key: "order-1", body: textA, graphStatus: 200, wantStatus: 200, wantWAMID: "wamid.ONE", wantReplayed: true, wantGraphHits: 2},
			},
		},
		{
			name: "different keys are independent",
			calls: []call{
				{key: "order-1", body: textA, graphStatus: 200, wantStatus: 200, wantWAMID: "wamid.ONE", wantGraphHits: 1},
				{key: "order-2", body: textA, graphStatus: 200, wantStatus: 200, wantWAMID: "wamid.ONE", wantGraphHits: 2},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var graphStatus int
			graphHits := 0
			graph := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				graphHits++
				w.WriteHeader(graphStatus)
				if graphStatus == http.StatusOK {
					_, _ = w.Write([]byte(accepted("wamid.ONE")))
				}
			}))
			defer graph.Close()

			recorder := &fakeRecorder{}
			sender := core.NewSender(meta.NewClient(graph.URL, "v24.0", "meta-token"), newMemoryIdempotencyStore(), "111", time.Hour)
			router := NewRouter(Deps{
				Ingestor: &fakeIngestor{},
				Recorder: recorder,
				Sender:   sender,
				APIKeys:  []string{testAPIKey},
				Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
			})

			for i, c := range tt.calls {
				graphStatus = c.graphStatus
				headers := map[string]string{}
				if c.key != "" {
					headers["Idempotency-Key"] = c.key
				}

				rec := postMessage(router, "Bearer "+testAPIKey, c.body, headers)

				if rec.Code != c.wantStatus || graphHits != c.wantGraphHits {
					t.Fatalf("call %d: status = %d, graph hits = %d, want %d and %d (body %s)", i, rec.Code, graphHits, c.wantStatus, c.wantGraphHits, rec.Body.String())
				}
				decoded := decodeBody(t, rec)
				if c.wantCode != "" && decoded["code"] != c.wantCode {
					t.Errorf("call %d: body = %v, want code %s", i, decoded, c.wantCode)
				}
				if c.wantWAMID != "" && decoded["wamid"] != c.wantWAMID {
					t.Errorf("call %d: wamid = %v, want %s", i, decoded["wamid"], c.wantWAMID)
				}
				if gotReplayed := rec.Header().Get("Idempotent-Replayed") == "true"; gotReplayed != c.wantReplayed {
					t.Errorf("call %d: replayed header = %v, want %v", i, gotReplayed, c.wantReplayed)
				}
			}
		})
	}
}
