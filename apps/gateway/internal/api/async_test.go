package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/core"
)

type fakeAsync struct {
	enqueued   []core.OutboundMessage
	id         string
	enqueueErr error
	status     core.SendStatus
	statusErr  error
	checked    []string
}

func (f *fakeAsync) Enqueue(_ context.Context, message core.OutboundMessage) (string, error) {
	f.enqueued = append(f.enqueued, message)
	return f.id, f.enqueueErr
}

func (f *fakeAsync) Status(_ context.Context, id string) (core.SendStatus, error) {
	f.checked = append(f.checked, id)
	return f.status, f.statusErr
}

func newAsyncRouter(sender MessageSender, async AsyncSender, recorder Recorder) http.Handler {
	return NewRouter(Deps{
		Ingestor: &fakeIngestor{},
		Recorder: recorder,
		Sender:   sender,
		Async:    async,
		APIKeys:  []string{firstTestKey},
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func postAsync(router http.Handler, query, body string, headers map[string]string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(http.MethodPost, "/v1/messages"+query, strings.NewReader(body))
	request.Header.Set("Authorization", "Bearer "+firstTestKey)
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, request)
	return rec
}

func TestSendMessageAsync(t *testing.T) {
	const body = `{"to":"94770000000","type":"text","text":{"body":"hi"},"correlation_id":"order-1"}`

	tests := []struct {
		name         string
		query        string
		body         string
		headers      map[string]string
		enqueueErr   error
		wantStatus   int
		wantCode     string
		wantEnqueued int
		wantSyncSent int
		wantRecord   string
	}{
		{name: "async true queues the message", query: "?async=true", body: body, wantStatus: http.StatusAccepted, wantEnqueued: 1, wantRecord: "text:queued"},
		{name: "idempotency key travels with the queued message", query: "?async=true", body: body, headers: map[string]string{"Idempotency-Key": "order-1-confirm"}, wantStatus: http.StatusAccepted, wantEnqueued: 1, wantRecord: "text:queued"},
		{name: "async false sends in the request", query: "?async=false", body: body, wantStatus: http.StatusOK, wantSyncSent: 1, wantRecord: "text:sent"},
		{name: "no async parameter sends in the request", body: body, wantStatus: http.StatusOK, wantSyncSent: 1, wantRecord: "text:sent"},
		{name: "unknown async value is refused", query: "?async=yes", body: body, wantStatus: http.StatusBadRequest, wantCode: "invalid_request", wantRecord: "text:invalid_request"},
		{name: "invalid message is refused at queue time", query: "?async=true", body: body, enqueueErr: &core.RequestError{Code: core.CodeInvalidRequest, Message: "bad"}, wantStatus: http.StatusBadRequest, wantCode: "invalid_request", wantEnqueued: 1, wantRecord: "text:invalid_request"},
		{name: "malformed body is refused before queueing", query: "?async=true", body: `[]`, wantStatus: http.StatusBadRequest, wantCode: "invalid_request", wantRecord: "unknown:invalid_request"},
		{name: "store failure is not leaked", query: "?async=true", body: body, enqueueErr: errors.New("secret internal detail"), wantStatus: http.StatusInternalServerError, wantCode: "internal_error", wantEnqueued: 1, wantRecord: "text:internal_error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sender := &fakeMessageSender{result: core.SendResult{MessageID: "wamid.SYNC"}}
			async := &fakeAsync{id: "msg_0123", enqueueErr: tt.enqueueErr}
			recorder := &fakeRecorder{}

			rec := postAsync(newAsyncRouter(sender, async, recorder), tt.query, tt.body, tt.headers)

			if rec.Code != tt.wantStatus || len(async.enqueued) != tt.wantEnqueued || len(sender.sent) != tt.wantSyncSent {
				t.Fatalf("status = %d, enqueued = %d, sync sends = %d, want %d, %d, %d (body %s)",
					rec.Code, len(async.enqueued), len(sender.sent), tt.wantStatus, tt.wantEnqueued, tt.wantSyncSent, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "secret internal detail") {
				t.Errorf("internal error leaked: %s", rec.Body.String())
			}
			if len(recorder.sends) != 1 || recorder.sends[0] != tt.wantRecord {
				t.Errorf("recorded sends = %v, want [%s]", recorder.sends, tt.wantRecord)
			}
			decoded := decodeBody(t, rec)
			if tt.wantCode != "" && decoded["code"] != tt.wantCode {
				t.Errorf("body = %v, want code %s", decoded, tt.wantCode)
			}
			if tt.wantStatus != http.StatusAccepted {
				return
			}
			if decoded["id"] != "msg_0123" || decoded["status"] != "queued" || decoded["correlation_id"] != "order-1" {
				t.Errorf("body = %v", decoded)
			}
			if rec.Header().Get("X-Correlation-Id") != "order-1" {
				t.Errorf("correlation header = %q", rec.Header().Get("X-Correlation-Id"))
			}
			if key := tt.headers["Idempotency-Key"]; async.enqueued[0].IdempotencyKey != key {
				t.Errorf("queued idempotency key = %q, want %q", async.enqueued[0].IdempotencyKey, key)
			}
		})
	}
}

func TestMessageStatusRoute(t *testing.T) {
	createdAt := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	updatedAt := createdAt.Add(3 * time.Second)

	tests := []struct {
		name         string
		id           string
		status       core.SendStatus
		statusErr    error
		wantStatus   int
		wantCode     string
		wantState    string
		wantWAMID    string
		wantErrCode  string
		wantAttempts float64
	}{
		{name: "queued", id: "msg_1", status: core.SendStatus{ID: "msg_1", State: "queued", CreatedAt: createdAt, UpdatedAt: createdAt}, wantStatus: http.StatusOK, wantState: "queued"},
		{name: "sent has the wamid", id: "msg_1", status: core.SendStatus{ID: "msg_1", State: "sent", MessageID: "wamid.1", Attempts: 2, CreatedAt: createdAt, UpdatedAt: updatedAt}, wantStatus: http.StatusOK, wantState: "sent", wantWAMID: "wamid.1", wantAttempts: 2},
		{
			name:       "failed has the error",
			id:         "msg_1",
			status:     core.SendStatus{ID: "msg_1", State: "failed", Failure: &core.SendFailure{Code: "window_closed", Message: "closed", MetaCode: 131047}, Attempts: 1, CreatedAt: createdAt, UpdatedAt: updatedAt},
			wantStatus: http.StatusOK, wantState: "failed", wantErrCode: "window_closed", wantAttempts: 1,
		},
		{name: "unknown id", id: "msg_missing", statusErr: &core.RequestError{Code: core.CodeMessageNotFound, Message: "none"}, wantStatus: http.StatusNotFound, wantCode: "message_not_found"},
		{name: "invalid id", id: "bad.id", statusErr: &core.RequestError{Code: core.CodeInvalidRequest, Message: "bad id"}, wantStatus: http.StatusBadRequest, wantCode: "invalid_request"},
		{name: "store failure is not leaked", id: "msg_1", statusErr: errors.New("secret internal detail"), wantStatus: http.StatusInternalServerError, wantCode: "internal_error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			async := &fakeAsync{status: tt.status, statusErr: tt.statusErr}

			rec := doRequest(newAsyncRouter(&fakeMessageSender{}, async, &fakeRecorder{}), http.MethodGet, "/v1/messages/"+tt.id, "", nil)

			if rec.Code != tt.wantStatus || len(async.checked) != 1 || async.checked[0] != tt.id {
				t.Fatalf("status = %d, checked = %v, want %d for %s", rec.Code, async.checked, tt.wantStatus, tt.id)
			}
			if strings.Contains(rec.Body.String(), "secret internal detail") {
				t.Errorf("internal error leaked: %s", rec.Body.String())
			}
			decoded := decodeBody(t, rec)
			if tt.wantCode != "" {
				if decoded["code"] != tt.wantCode {
					t.Errorf("body = %v, want code %s", decoded, tt.wantCode)
				}
				return
			}
			if decoded["status"] != tt.wantState || decoded["id"] != tt.id {
				t.Errorf("body = %v", decoded)
			}
			if wamid, _ := decoded["wamid"].(string); wamid != tt.wantWAMID {
				t.Errorf("wamid = %q, want %q", wamid, tt.wantWAMID)
			}
			if attempts, _ := decoded["attempts"].(float64); attempts != tt.wantAttempts {
				t.Errorf("attempts = %v, want %v", attempts, tt.wantAttempts)
			}
			if decoded["created_at"] != "2026-11-01T12:00:00Z" {
				t.Errorf("created_at = %v", decoded["created_at"])
			}
			failure, _ := decoded["error"].(map[string]any)
			if tt.wantErrCode != "" && failure["code"] != tt.wantErrCode {
				t.Errorf("error = %v, want code %s", decoded["error"], tt.wantErrCode)
			}
			if tt.wantErrCode == "" && decoded["error"] != nil {
				t.Errorf("unexpected error object %v", decoded["error"])
			}
		})
	}
}

func TestMessageStatusRouteRequiresAPIKeyAndAProvider(t *testing.T) {
	tests := []struct {
		name          string
		hasProvider   bool
		authorization string
		wantStatus    int
	}{
		{name: "no header", hasProvider: true, wantStatus: http.StatusUnauthorized},
		{name: "wrong key", hasProvider: true, authorization: "Bearer nope", wantStatus: http.StatusUnauthorized},
		{name: "valid key", hasProvider: true, authorization: "Bearer " + firstTestKey, wantStatus: http.StatusOK},
		{name: "route is absent without the async sender", hasProvider: false, authorization: "Bearer " + firstTestKey, wantStatus: http.StatusNotFound},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var async AsyncSender
			if tt.hasProvider {
				async = &fakeAsync{status: core.SendStatus{ID: "msg_1", State: "queued"}}
			}
			request := httptest.NewRequest(http.MethodGet, "/v1/messages/msg_1", nil)
			if tt.authorization != "" {
				request.Header.Set("Authorization", tt.authorization)
			}
			rec := httptest.NewRecorder()

			newAsyncRouter(&fakeMessageSender{}, async, &fakeRecorder{}).ServeHTTP(rec, request)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d", rec.Code, tt.wantStatus)
			}
		})
	}
}
