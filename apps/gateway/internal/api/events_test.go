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
	"time"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/core"
)

type fakeEvents struct {
	events    []core.FailedEvent
	listErr   error
	replayErr error
	limit     int
	offset    int
	listCalls int
	replayed  []string
}

func (f *fakeEvents) ListFailed(_ context.Context, limit, offset int) ([]core.FailedEvent, error) {
	f.listCalls++
	f.limit, f.offset = limit, offset
	return f.events, f.listErr
}

func (f *fakeEvents) Replay(_ context.Context, eventID string) error {
	f.replayed = append(f.replayed, eventID)
	return f.replayErr
}

func newEventsRouter(events *fakeEvents, recorder Recorder) http.Handler {
	return NewRouter(Deps{
		Ingestor: &fakeIngestor{},
		Recorder: recorder,
		Events:   events,
		APIKeys:  []string{testAPIKey},
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func TestEventRoutesRequireAPIKey(t *testing.T) {
	routes := []struct {
		name   string
		method string
		target string
	}{
		{name: "list", method: http.MethodGet, target: "/v1/events/failed"},
		{name: "replay", method: http.MethodPost, target: "/v1/events/evt_1/replay"},
	}
	credentials := []struct {
		name          string
		authorization string
		wantStatus    int
	}{
		{name: "no header", authorization: "", wantStatus: http.StatusUnauthorized},
		{name: "wrong key", authorization: "Bearer nope", wantStatus: http.StatusUnauthorized},
		{name: "valid key", authorization: "Bearer " + testAPIKey},
	}

	for _, route := range routes {
		for _, credential := range credentials {
			t.Run(route.name+" with "+credential.name, func(t *testing.T) {
				events := &fakeEvents{}
				request := httptest.NewRequest(route.method, route.target, nil)
				if credential.authorization != "" {
					request.Header.Set("Authorization", credential.authorization)
				}
				rec := httptest.NewRecorder()

				newEventsRouter(events, &fakeRecorder{}).ServeHTTP(rec, request)

				reached := credential.wantStatus == 0
				if reached && rec.Code >= http.StatusBadRequest {
					t.Fatalf("status = %d, want the handler to run", rec.Code)
				}
				if !reached && (rec.Code != credential.wantStatus || events.listCalls+len(events.replayed) != 0) {
					t.Fatalf("status = %d with %d calls, want %d and none", rec.Code, events.listCalls+len(events.replayed), credential.wantStatus)
				}
			})
		}
	}
}

func TestEventRoutesAreAbsentWithoutAProvider(t *testing.T) {
	tests := []struct {
		name   string
		method string
		target string
	}{
		{name: "list", method: http.MethodGet, target: "/v1/events/failed"},
		{name: "replay", method: http.MethodPost, target: "/v1/events/evt_1/replay"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := doRequest(newTestRouter(&fakeIngestor{}), tt.method, tt.target, "", nil)
			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", rec.Code)
			}
		})
	}
}

func TestListFailedEvents(t *testing.T) {
	createdAt := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	failedAt := time.Date(2026, 11, 1, 10, 30, 0, 0, time.FixedZone("plus530", 5*3600+1800))

	tests := []struct {
		name       string
		query      string
		events     []core.FailedEvent
		listErr    error
		wantStatus int
		wantCode   string
		wantCalls  int
		wantLimit  int
		wantOffset int
		wantEvents int
	}{
		{name: "defaults", query: "", wantStatus: http.StatusOK, wantCalls: 1, wantLimit: 50, wantOffset: 0},
		{name: "explicit page", query: "?limit=10&offset=20", wantStatus: http.StatusOK, wantCalls: 1, wantLimit: 10, wantOffset: 20},
		{name: "largest allowed limit", query: "?limit=200", wantStatus: http.StatusOK, wantCalls: 1, wantLimit: 200, wantOffset: 0},
		{name: "events are returned", events: []core.FailedEvent{{ID: "evt_a", Topic: "wa.inbound", Attempts: 8, LastError: "status 500", CreatedAt: createdAt, FailedAt: failedAt}}, wantStatus: http.StatusOK, wantCalls: 1, wantLimit: 50, wantEvents: 1},
		{name: "limit of zero", query: "?limit=0", wantStatus: http.StatusBadRequest, wantCode: "invalid_request"},
		{name: "limit over the maximum", query: "?limit=201", wantStatus: http.StatusBadRequest, wantCode: "invalid_request"},
		{name: "limit that is not a number", query: "?limit=ten", wantStatus: http.StatusBadRequest, wantCode: "invalid_request"},
		{name: "negative offset", query: "?offset=-1", wantStatus: http.StatusBadRequest, wantCode: "invalid_request"},
		{name: "offset that is not a number", query: "?offset=1.5", wantStatus: http.StatusBadRequest, wantCode: "invalid_request"},
		{name: "store failure is not leaked", listErr: errors.New("secret internal detail"), wantStatus: http.StatusInternalServerError, wantCode: "internal_error", wantCalls: 1, wantLimit: 50},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := &fakeEvents{events: tt.events, listErr: tt.listErr}

			rec := doRequest(newEventsRouter(events, &fakeRecorder{}), http.MethodGet, "/v1/events/failed"+tt.query, "", nil)

			if rec.Code != tt.wantStatus || events.listCalls != tt.wantCalls {
				t.Fatalf("status = %d, calls = %d, want %d and %d (body %s)", rec.Code, events.listCalls, tt.wantStatus, tt.wantCalls, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "secret internal detail") {
				t.Errorf("internal error leaked: %s", rec.Body.String())
			}
			if tt.wantCode != "" {
				if decoded := decodeBody(t, rec); decoded["code"] != tt.wantCode {
					t.Errorf("body = %v, want code %s", decoded, tt.wantCode)
				}
				return
			}
			if events.limit != tt.wantLimit || events.offset != tt.wantOffset {
				t.Errorf("provider saw limit %d offset %d, want %d and %d", events.limit, events.offset, tt.wantLimit, tt.wantOffset)
			}

			var body failedEventsResponse
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if len(body.Events) != tt.wantEvents || body.Limit != tt.wantLimit || body.Offset != tt.wantOffset {
				t.Fatalf("body = %+v", body)
			}
			if !strings.Contains(rec.Body.String(), `"events":[`) {
				t.Errorf("an empty list must encode as [] and not null: %s", rec.Body.String())
			}
			if tt.wantEvents == 1 {
				want := failedEventBody{ID: "evt_a", Topic: "wa.inbound", Attempts: 8, LastError: "status 500", CreatedAt: "2026-11-01T10:00:00Z", FailedAt: "2026-11-01T05:00:00Z"}
				if body.Events[0] != want {
					t.Errorf("event = %+v, want %+v", body.Events[0], want)
				}
			}
		})
	}
}

func TestReplayEvent(t *testing.T) {
	tests := []struct {
		name        string
		eventID     string
		replayErr   error
		wantStatus  int
		wantCode    string
		wantReplays int
		wantRecord  string
	}{
		{name: "queued", eventID: "evt_0123", wantStatus: http.StatusAccepted, wantReplays: 1, wantRecord: "queued"},
		{name: "event is not failed", eventID: "evt_9", replayErr: &core.RequestError{Code: core.CodeEventNotFound, Message: "none"}, wantStatus: http.StatusNotFound, wantCode: "event_not_found", wantReplays: 1, wantRecord: "event_not_found"},
		{name: "invalid id", eventID: "bad.id", replayErr: &core.RequestError{Code: core.CodeInvalidRequest, Message: "bad id"}, wantStatus: http.StatusBadRequest, wantCode: "invalid_request", wantReplays: 1, wantRecord: "invalid_request"},
		{name: "store failure is not leaked", eventID: "evt_1", replayErr: errors.New("secret internal detail"), wantStatus: http.StatusInternalServerError, wantCode: "internal_error", wantReplays: 1, wantRecord: "internal_error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			events := &fakeEvents{replayErr: tt.replayErr}
			recorder := &fakeRecorder{}

			rec := doRequest(newEventsRouter(events, recorder), http.MethodPost, "/v1/events/"+tt.eventID+"/replay", "", nil)

			if rec.Code != tt.wantStatus || len(events.replayed) != tt.wantReplays {
				t.Fatalf("status = %d, replays = %d, want %d and %d (body %s)", rec.Code, len(events.replayed), tt.wantStatus, tt.wantReplays, rec.Body.String())
			}
			if strings.Contains(rec.Body.String(), "secret internal detail") {
				t.Errorf("internal error leaked: %s", rec.Body.String())
			}
			decoded := decodeBody(t, rec)
			if tt.wantCode != "" && decoded["code"] != tt.wantCode {
				t.Errorf("body = %v, want code %s", decoded, tt.wantCode)
			}
			if tt.wantStatus == http.StatusAccepted && (decoded["id"] != tt.eventID || decoded["status"] != "queued") {
				t.Errorf("body = %v", decoded)
			}
			if len(recorder.replays) != 1 || recorder.replays[0] != tt.wantRecord {
				t.Errorf("recorded replays = %v, want [%s]", recorder.replays, tt.wantRecord)
			}
		})
	}
}
