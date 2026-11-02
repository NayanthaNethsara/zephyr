package core

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/store"
)

type fakeFailedStore struct {
	entries     []store.FailedEntry
	listErr     error
	requeueErr  error
	isRequeued  bool
	listedLimit int
	listedSkip  int
	requeuedID  string
	requeuedAt  time.Time
	requeues    int
}

func (f *fakeFailedStore) ListFailed(_ context.Context, limit, offset int) ([]store.FailedEntry, error) {
	f.listedLimit, f.listedSkip = limit, offset
	return f.entries, f.listErr
}

func (f *fakeFailedStore) Requeue(_ context.Context, id string, at time.Time) (bool, error) {
	f.requeues++
	f.requeuedID, f.requeuedAt = id, at
	return f.isRequeued, f.requeueErr
}

func TestFailedEventServiceListFailed(t *testing.T) {
	createdAt := time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)
	failedAt := createdAt.Add(time.Hour)

	tests := []struct {
		name    string
		entries []store.FailedEntry
		listErr error
		want    []FailedEvent
		wantErr bool
	}{
		{name: "no failures gives an empty list", entries: nil, want: []FailedEvent{}},
		{
			name:    "entries are mapped field by field",
			entries: []store.FailedEntry{{ID: "evt_a", Topic: "wa.inbound", Attempts: 8, LastError: "status 500", CreatedAt: createdAt, FailedAt: failedAt}},
			want:    []FailedEvent{{ID: "evt_a", Topic: "wa.inbound", Attempts: 8, LastError: "status 500", CreatedAt: createdAt, FailedAt: failedAt}},
		},
		{name: "store failure is returned", listErr: errors.New("db down"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := &fakeFailedStore{entries: tt.entries, listErr: tt.listErr}

			got, err := NewFailedEventService(backend).ListFailed(context.Background(), 25, 50)

			if (err != nil) != tt.wantErr {
				t.Fatalf("ListFailed() error = %v, wantErr %v", err, tt.wantErr)
			}
			if backend.listedLimit != 25 || backend.listedSkip != 50 {
				t.Errorf("store saw limit %d offset %d, want 25 and 50", backend.listedLimit, backend.listedSkip)
			}
			if !tt.wantErr && !slices.Equal(got, tt.want) {
				t.Fatalf("ListFailed() = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestFailedEventServiceReplay(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name         string
		eventID      string
		isRequeued   bool
		requeueErr   error
		wantRequeues int
		wantCode     string
		wantInternal bool
	}{
		{name: "failed event is queued", eventID: "evt_0123456789abcdef0123456789abcdef", isRequeued: true, wantRequeues: 1},
		{name: "event that is not failed", eventID: "evt_abc", isRequeued: false, wantRequeues: 1, wantCode: CodeEventNotFound},
		{name: "empty id", eventID: "", wantRequeues: 0, wantCode: CodeInvalidRequest},
		{name: "id that is too long", eventID: strings.Repeat("a", 129), wantRequeues: 0, wantCode: CodeInvalidRequest},
		{name: "id with a slash", eventID: "evt/../x", wantRequeues: 0, wantCode: CodeInvalidRequest},
		{name: "id with a space", eventID: "evt abc", wantRequeues: 0, wantCode: CodeInvalidRequest},
		{name: "store failure is not a request error", eventID: "evt_abc", requeueErr: errors.New("db down"), wantRequeues: 1, wantInternal: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := &fakeFailedStore{isRequeued: tt.isRequeued, requeueErr: tt.requeueErr}
			service := NewFailedEventService(backend)
			service.now = func() time.Time { return now }

			err := service.Replay(context.Background(), tt.eventID)

			if backend.requeues != tt.wantRequeues {
				t.Fatalf("requeues = %d, want %d", backend.requeues, tt.wantRequeues)
			}
			var requestErr *RequestError
			switch {
			case tt.wantInternal:
				if err == nil || errors.As(err, &requestErr) {
					t.Fatalf("error = %#v, want a plain store error", err)
				}
			case tt.wantCode != "":
				if !errors.As(err, &requestErr) || requestErr.Code != tt.wantCode {
					t.Fatalf("error = %#v, want code %s", err, tt.wantCode)
				}
			default:
				if err != nil {
					t.Fatalf("Replay() error = %v", err)
				}
				if backend.requeuedID != tt.eventID || !backend.requeuedAt.Equal(now) {
					t.Errorf("store saw %q at %v, want %q at %v", backend.requeuedID, backend.requeuedAt, tt.eventID, now)
				}
			}
		})
	}
}
