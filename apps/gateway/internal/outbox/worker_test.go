package outbox

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strconv"
	"testing"
	"time"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/store"
)

type fakeStore struct {
	store.Store
	due         []store.OutboxEntry
	deleted     []string
	rescheduled []rescheduleCall
	failed      []string
}

type rescheduleCall struct {
	id       string
	attempts int
	next     time.Time
	lastErr  string
}

func (f *fakeStore) ListDue(_ context.Context, _ time.Time, limit int) ([]store.OutboxEntry, error) {
	entries := f.due
	if len(entries) > limit {
		entries = entries[:limit]
	}
	f.due = f.due[len(entries):]
	return entries, nil
}

func (f *fakeStore) Delete(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	return nil
}

func (f *fakeStore) Reschedule(_ context.Context, id string, attempts int, next time.Time, lastErr string) error {
	f.rescheduled = append(f.rescheduled, rescheduleCall{id, attempts, next, lastErr})
	return nil
}

func (f *fakeStore) MarkFailed(_ context.Context, id string, _ int, _ string, _ time.Time) error {
	f.failed = append(f.failed, id)
	return nil
}

type fakeTransport struct {
	err       error
	published []string
}

func (f *fakeTransport) Name() string { return "fake" }
func (f *fakeTransport) Close() error { return nil }
func (f *fakeTransport) Publish(_ context.Context, _ string, _ string, payload []byte) error {
	f.published = append(f.published, string(payload))
	return f.err
}

type fakeRecorder struct {
	attempts  []string
	delivered []time.Duration
}

func (f *fakeRecorder) DeliveryAttempt(_ string, result string, _ time.Duration) {
	f.attempts = append(f.attempts, result)
}

func (f *fakeRecorder) EventDelivered(_ string, lag time.Duration) {
	f.delivered = append(f.delivered, lag)
}

func newTestWorker(s *fakeStore, tr *fakeTransport, maxAttempts int) *Worker {
	return newRecordingWorker(s, tr, &fakeRecorder{}, maxAttempts)
}

func newRecordingWorker(s *fakeStore, tr *fakeTransport, rec *fakeRecorder, maxAttempts int) *Worker {
	worker := NewWorker(s, tr, Settings{
		MaxAttempts:    maxAttempts,
		BaseDelay:      time.Second,
		PollInterval:   time.Hour,
		BatchSize:      2,
		PublishTimeout: time.Second,
	}, rec, slog.New(slog.NewTextHandler(io.Discard, nil)))
	worker.now = func() time.Time { return time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC) }
	return worker
}

func TestDrain(t *testing.T) {
	tests := []struct {
		name          string
		entryCount    int
		wantPublished int
	}{
		{name: "nothing due", entryCount: 0, wantPublished: 0},
		{name: "fits in one batch", entryCount: 2, wantPublished: 2},
		{name: "spans several batches", entryCount: 5, wantPublished: 5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			due := make([]store.OutboxEntry, tt.entryCount)
			for i := range due {
				due[i] = store.OutboxEntry{ID: strconv.Itoa(i), Payload: []byte("p")}
			}
			s := &fakeStore{due: due}
			tr := &fakeTransport{}

			newTestWorker(s, tr, 5).drain(context.Background())

			if len(tr.published) != tt.wantPublished || len(s.deleted) != tt.wantPublished {
				t.Fatalf("published = %d, deleted = %d, want %d each", len(tr.published), len(s.deleted), tt.wantPublished)
			}
		})
	}
}

func TestDeliver(t *testing.T) {
	created := time.Date(2026, 11, 1, 9, 59, 0, 0, time.UTC)

	tests := []struct {
		name            string
		publishErr      error
		attempts        int
		wantResult      string
		wantDeleted     bool
		wantRescheduled bool
		wantFailed      bool
		wantAttempts    int
	}{
		{name: "success deletes the entry", wantResult: "success", wantDeleted: true},
		{
			name:            "first failure is rescheduled",
			publishErr:      errors.New("downstream 500"),
			attempts:        0,
			wantResult:      "retry",
			wantRescheduled: true,
			wantAttempts:    1,
		},
		{
			name:            "failure with attempts left is rescheduled",
			publishErr:      errors.New("downstream 500"),
			attempts:        3,
			wantResult:      "retry",
			wantRescheduled: true,
			wantAttempts:    4,
		},
		{
			name:       "last allowed attempt marks the entry failed",
			publishErr: errors.New("still down"),
			attempts:   4,
			wantResult: "failed",
			wantFailed: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := &fakeStore{}
			rec := &fakeRecorder{}
			worker := newRecordingWorker(s, &fakeTransport{err: tt.publishErr}, rec, 5)

			worker.deliver(context.Background(), store.OutboxEntry{ID: "1", Attempts: tt.attempts, CreatedAt: created})

			if len(rec.attempts) != 1 || rec.attempts[0] != tt.wantResult {
				t.Fatalf("recorded attempts = %v, want [%s]", rec.attempts, tt.wantResult)
			}
			if gotDeleted := len(s.deleted) == 1; gotDeleted != tt.wantDeleted {
				t.Errorf("deleted = %v, want %v", s.deleted, tt.wantDeleted)
			}
			if gotFailed := len(s.failed) == 1; gotFailed != tt.wantFailed {
				t.Errorf("failed = %v, want %v", s.failed, tt.wantFailed)
			}
			if tt.wantDeleted {
				if len(rec.delivered) != 1 || rec.delivered[0] != time.Minute {
					t.Errorf("delivered lag = %v, want [1m]", rec.delivered)
				}
			} else if len(rec.delivered) != 0 {
				t.Errorf("delivered = %v, want none", rec.delivered)
			}
			if !tt.wantRescheduled {
				if len(s.rescheduled) != 0 {
					t.Errorf("rescheduled = %v, want none", s.rescheduled)
				}
				return
			}
			if len(s.rescheduled) != 1 {
				t.Fatalf("rescheduled = %v, want one call", s.rescheduled)
			}
			call := s.rescheduled[0]
			delay := call.next.Sub(worker.now())
			maxDelay := time.Second << (call.attempts - 1)
			if call.attempts != tt.wantAttempts || call.lastErr != tt.publishErr.Error() {
				t.Errorf("reschedule = %+v, want attempts %d", call, tt.wantAttempts)
			}
			if delay < maxDelay/2 || delay > maxDelay {
				t.Errorf("delay = %s, want within [%s, %s]", delay, maxDelay/2, maxDelay)
			}
		})
	}
}

func TestBackoff(t *testing.T) {
	tests := []struct {
		name    string
		attempt int
		base    time.Duration
		min     time.Duration
		max     time.Duration
	}{
		{name: "first attempt", attempt: 1, base: 2 * time.Second, min: time.Second, max: 2 * time.Second},
		{name: "second attempt", attempt: 2, base: 2 * time.Second, min: 2 * time.Second, max: 4 * time.Second},
		{name: "fifth attempt", attempt: 5, base: 2 * time.Second, min: 16 * time.Second, max: 32 * time.Second},
		{name: "capped", attempt: 30, base: 2 * time.Second, min: maxBackoff / 2, max: maxBackoff},
		{name: "overflowing base is capped", attempt: 19, base: time.Hour, min: maxBackoff / 2, max: maxBackoff},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			for range 50 {
				if got := Backoff(tt.attempt, tt.base); got < tt.min || got > tt.max {
					t.Fatalf("Backoff() = %s, want within [%s, %s]", got, tt.min, tt.max)
				}
			}
		})
	}
}
