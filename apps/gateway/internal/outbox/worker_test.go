package outbox

import (
	"context"
	"errors"
	"io"
	"log/slog"
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

func TestDrainDeliversAllDueEntriesAcrossBatches(t *testing.T) {
	s := &fakeStore{due: []store.OutboxEntry{
		{ID: "1", Payload: []byte("a")}, {ID: "2", Payload: []byte("b")}, {ID: "3", Payload: []byte("c")},
	}}
	tr := &fakeTransport{}

	newTestWorker(s, tr, 5).drain(context.Background())

	if len(tr.published) != 3 || len(s.deleted) != 3 {
		t.Fatalf("published = %v, deleted = %v, want 3 each", tr.published, s.deleted)
	}
}

func TestDeliverFailureReschedulesWithBackoff(t *testing.T) {
	s := &fakeStore{}
	tr := &fakeTransport{err: errors.New("downstream 500")}
	worker := newTestWorker(s, tr, 5)

	worker.deliver(context.Background(), store.OutboxEntry{ID: "1", Attempts: 1, Payload: []byte("a")})

	if len(s.rescheduled) != 1 || len(s.deleted) != 0 || len(s.failed) != 0 {
		t.Fatalf("rescheduled=%v deleted=%v failed=%v", s.rescheduled, s.deleted, s.failed)
	}
	call := s.rescheduled[0]
	delay := call.next.Sub(worker.now())
	if call.attempts != 2 || call.lastErr != "downstream 500" || delay < time.Second || delay > 2*time.Second {
		t.Fatalf("reschedule = %+v, delay = %s", call, delay)
	}
}

func TestDeliverMarksFailedWhenAttemptsExhausted(t *testing.T) {
	s := &fakeStore{}
	tr := &fakeTransport{err: errors.New("still down")}

	newTestWorker(s, tr, 3).deliver(context.Background(), store.OutboxEntry{ID: "1", Attempts: 2})

	if len(s.failed) != 1 || len(s.rescheduled) != 0 {
		t.Fatalf("failed=%v rescheduled=%v, want one failed", s.failed, s.rescheduled)
	}
}

func TestDeliveryOutcomesAreRecorded(t *testing.T) {
	created := time.Date(2026, 11, 1, 9, 59, 0, 0, time.UTC)

	tests := []struct {
		name          string
		publishErr    error
		attempts      int
		wantResult    string
		wantDelivered bool
	}{
		{name: "success", wantResult: "success", wantDelivered: true},
		{name: "retry", publishErr: errors.New("500"), attempts: 0, wantResult: "retry"},
		{name: "exhausted", publishErr: errors.New("500"), attempts: 4, wantResult: "failed"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := &fakeRecorder{}
			worker := newRecordingWorker(&fakeStore{}, &fakeTransport{err: tt.publishErr}, rec, 5)

			worker.deliver(context.Background(), store.OutboxEntry{ID: "1", Attempts: tt.attempts, CreatedAt: created})

			if len(rec.attempts) != 1 || rec.attempts[0] != tt.wantResult {
				t.Fatalf("attempts = %v, want [%s]", rec.attempts, tt.wantResult)
			}
			if tt.wantDelivered {
				if len(rec.delivered) != 1 || rec.delivered[0] != time.Minute {
					t.Fatalf("delivered lag = %v, want [1m]", rec.delivered)
				}
			} else if len(rec.delivered) != 0 {
				t.Fatalf("delivered = %v, want none", rec.delivered)
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
