package api

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/core"
)

type fakeIngestor struct {
	ingested   [][]core.Inbound
	ingestErr  error
	readyErr   error
	duplicates map[string]bool
}

func (f *fakeIngestor) Ingest(_ context.Context, items []core.Inbound) ([]core.IngestResult, error) {
	if f.ingestErr != nil {
		return nil, f.ingestErr
	}
	f.ingested = append(f.ingested, items)

	results := make([]core.IngestResult, len(items))
	for i, item := range items {
		queued := !f.duplicates[item.DedupKey]
		results[i] = core.IngestResult{Type: item.Type, MessageID: item.MessageID, Subtype: item.Subtype, Queued: queued}
		if queued {
			results[i].EventID = "evt_test"
		}
	}
	return results, nil
}

type fakeRecorder struct {
	webhookResults []string
	events         []string
	requests       []string
}

func (f *fakeRecorder) WebhookResult(result string) {
	f.webhookResults = append(f.webhookResults, result)
}

func (f *fakeRecorder) EventIngested(eventType string, queued bool) {
	outcome := "duplicate"
	if queued {
		outcome = "queued"
	}
	f.events = append(f.events, eventType+":"+outcome)
}

func (f *fakeRecorder) HTTPRequest(method, route string, status int, _ time.Duration) {
	f.requests = append(f.requests, method+" "+route+" "+strconv.Itoa(status))
}

func (f *fakeIngestor) Ready(context.Context) error {
	return f.readyErr
}

func newTestRouter(ingestor Ingestor) http.Handler {
	return newRecordingRouter(ingestor, &fakeRecorder{})
}

func newRecordingRouter(ingestor Ingestor, recorder Recorder) http.Handler {
	return NewRouter(Deps{
		Ingestor:        ingestor,
		Recorder:        recorder,
		MetaAppSecret:   "app-secret",
		MetaVerifyToken: "verify-token",
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
}

func TestHealthEndpoints(t *testing.T) {
	tests := []struct {
		name       string
		path       string
		readyErr   error
		wantStatus int
		wantBody   string
	}{
		{name: "healthz", path: "/healthz", wantStatus: http.StatusOK, wantBody: `{"status":"ok"}`},
		{name: "readyz ready", path: "/readyz", wantStatus: http.StatusOK, wantBody: `{"status":"ready"}`},
		{name: "readyz unavailable", path: "/readyz", readyErr: errors.New("db down"), wantStatus: http.StatusServiceUnavailable, wantBody: `{"status":"unavailable"}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			router := newTestRouter(&fakeIngestor{readyErr: tt.readyErr})
			rec := httptest.NewRecorder()
			router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, tt.path, nil))

			if rec.Code != tt.wantStatus || rec.Body.String() != tt.wantBody {
				t.Fatalf("response = %d %s, want %d %s", rec.Code, rec.Body.String(), tt.wantStatus, tt.wantBody)
			}
		})
	}
}

func TestRequestsGetIDAndAreRecorded(t *testing.T) {
	recorder := &fakeRecorder{}
	router := newRecordingRouter(&fakeIngestor{}, recorder)

	for _, path := range []string{"/healthz", "/missing"} {
		rec := httptest.NewRecorder()
		router.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))

		if id := rec.Header().Get("X-Request-Id"); !strings.HasPrefix(id, "req_") {
			t.Errorf("%s X-Request-Id = %q", path, id)
		}
	}

	want := []string{"GET /healthz 200", "GET unmatched 404"}
	if len(recorder.requests) != 2 || recorder.requests[0] != want[0] || recorder.requests[1] != want[1] {
		t.Fatalf("recorded requests = %v, want %v", recorder.requests, want)
	}
}

func TestMetricsEndpointIsOptional(t *testing.T) {
	deps := Deps{
		Ingestor: &fakeIngestor{},
		Recorder: &fakeRecorder{},
		Logger:   slog.New(slog.NewTextHandler(io.Discard, nil)),
	}

	rec := httptest.NewRecorder()
	NewRouter(deps).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("without handler status = %d, want 404", rec.Code)
	}

	deps.MetricsHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("metrics")) })
	rec = httptest.NewRecorder()
	NewRouter(deps).ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if rec.Code != http.StatusOK || rec.Body.String() != "metrics" {
		t.Fatalf("with handler = %d %q", rec.Code, rec.Body.String())
	}
}
