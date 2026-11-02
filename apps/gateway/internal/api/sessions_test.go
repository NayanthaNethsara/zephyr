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

type fakeSessions struct {
	session core.Session
	err     error
	checked []string
}

func (f *fakeSessions) Check(_ context.Context, phone string) (core.Session, error) {
	f.checked = append(f.checked, phone)
	return f.session, f.err
}

func newSessionsRouter(sessions SessionProvider) http.Handler {
	return NewRouter(Deps{
		Ingestor: &fakeIngestor{},
		Recorder: &fakeRecorder{},
		Sessions: sessions,
		APIKeys:  []string{firstTestKey},
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func TestSessionRouteRequiresAPIKey(t *testing.T) {
	tests := []struct {
		name          string
		authorization string
		wantStatus    int
		wantChecks    int
	}{
		{name: "no header", wantStatus: http.StatusUnauthorized},
		{name: "wrong key", authorization: "Bearer nope", wantStatus: http.StatusUnauthorized},
		{name: "valid key", authorization: "Bearer " + firstTestKey, wantStatus: http.StatusOK, wantChecks: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessions := &fakeSessions{session: core.Session{State: core.SessionUnknown}}
			request := httptest.NewRequest(http.MethodGet, "/v1/sessions/94770000000", nil)
			if tt.authorization != "" {
				request.Header.Set("Authorization", tt.authorization)
			}
			rec := httptest.NewRecorder()

			newSessionsRouter(sessions).ServeHTTP(rec, request)

			if rec.Code != tt.wantStatus || len(sessions.checked) != tt.wantChecks {
				t.Fatalf("status = %d, checks = %d, want %d and %d", rec.Code, len(sessions.checked), tt.wantStatus, tt.wantChecks)
			}
		})
	}
}

func TestSessionRouteIsAbsentWithoutAProvider(t *testing.T) {
	tests := []struct {
		name   string
		target string
	}{
		{name: "valid phone", target: "/v1/sessions/94770000000"},
		{name: "invalid phone", target: "/v1/sessions/12"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := doRequest(newTestRouter(&fakeIngestor{}), http.MethodGet, tt.target, "", nil)

			if rec.Code != http.StatusNotFound {
				t.Fatalf("status = %d, want 404", rec.Code)
			}
		})
	}
}

func TestCheckSession(t *testing.T) {
	lastInboundAt := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	expiresAt := lastInboundAt.Add(24 * time.Hour)

	tests := []struct {
		name          string
		phone         string
		session       core.Session
		err           error
		wantStatus    int
		wantCode      string
		wantState     string
		wantLastAt    string
		wantExpiresAt string
	}{
		{name: "open window", phone: "94770000000", session: core.Session{State: core.SessionOpen, LastInboundAt: lastInboundAt, ExpiresAt: expiresAt}, wantStatus: http.StatusOK, wantState: "open", wantLastAt: "2026-11-01T10:00:00Z", wantExpiresAt: "2026-11-02T10:00:00Z"},
		{name: "closed window", phone: "94770000000", session: core.Session{State: core.SessionClosed, LastInboundAt: lastInboundAt, ExpiresAt: expiresAt}, wantStatus: http.StatusOK, wantState: "closed", wantLastAt: "2026-11-01T10:00:00Z", wantExpiresAt: "2026-11-02T10:00:00Z"},
		{name: "customer never seen has no timestamps", phone: "94770000000", session: core.Session{State: core.SessionUnknown}, wantStatus: http.StatusOK, wantState: "unknown"},
		{name: "invalid phone", phone: "12", err: &core.RequestError{Code: core.CodeInvalidRequest, Message: "bad phone"}, wantStatus: http.StatusBadRequest, wantCode: "invalid_request"},
		{name: "store failure is not leaked", phone: "94770000000", err: errors.New("secret internal detail"), wantStatus: http.StatusInternalServerError, wantCode: "internal_error"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			sessions := &fakeSessions{session: tt.session, err: tt.err}

			rec := doRequest(newSessionsRouter(sessions), http.MethodGet, "/v1/sessions/"+tt.phone, "", nil)

			if rec.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d (body %s)", rec.Code, tt.wantStatus, rec.Body.String())
			}
			if len(sessions.checked) != 1 || sessions.checked[0] != tt.phone {
				t.Errorf("checked = %v, want [%s]", sessions.checked, tt.phone)
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
			if decoded["status"] != tt.wantState {
				t.Errorf("status field = %v, want %s", decoded["status"], tt.wantState)
			}
			lastAt, _ := decoded["last_inbound_at"].(string)
			expires, _ := decoded["expires_at"].(string)
			if lastAt != tt.wantLastAt || expires != tt.wantExpiresAt {
				t.Errorf("timestamps = %q and %q, want %q and %q", lastAt, expires, tt.wantLastAt, tt.wantExpiresAt)
			}
		})
	}
}
