package sqlite

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/store"
)

func sendJob(id string, at time.Time) store.SendJob {
	return store.SendJob{ID: id, Payload: []byte(`{"to":"94770000000"}`), NextAttemptAt: at, CreatedAt: at}
}

func resultEvent(id string, at time.Time) store.OutboxEntry {
	return store.OutboxEntry{ID: id, Topic: "wa.results", Key: "94770000000", Payload: []byte(`{"id":"` + id + `"}`), NextAttemptAt: at, CreatedAt: at}
}

func sendIDs(jobs []store.SendJob) []string {
	ids := make([]string, 0, len(jobs))
	for _, job := range jobs {
		ids = append(ids, job.ID)
	}
	return ids
}

func seedSendJobs(t *testing.T, s *Store) {
	t.Helper()
	for i, id := range []string{"msg_a", "msg_b", "msg_c"} {
		if err := s.EnqueueSend(context.Background(), sendJob(id, baseTime.Add(time.Duration(i)*time.Second))); err != nil {
			t.Fatal(err)
		}
	}
}

func TestSendJobScheduling(t *testing.T) {
	retryAt := baseTime.Add(time.Hour)

	tests := []struct {
		name         string
		apply        func(ctx context.Context, s *Store) error
		checkAt      time.Time
		limit        int
		wantIDs      []string
		wantAttempts map[string]int
	}{
		{name: "oldest job first", checkAt: baseTime.Add(time.Minute), limit: 10, wantIDs: []string{"msg_a", "msg_b", "msg_c"}},
		{name: "limit cuts the batch", checkAt: baseTime.Add(time.Minute), limit: 2, wantIDs: []string{"msg_a", "msg_b"}},
		{name: "job scheduled for later is not due yet", checkAt: baseTime, limit: 10, wantIDs: []string{"msg_a"}},
		{
			name:    "rescheduled job waits for its retry time",
			apply:   func(ctx context.Context, s *Store) error { return s.RescheduleSend(ctx, "msg_b", 2, retryAt) },
			checkAt: baseTime.Add(time.Minute),
			limit:   10,
			wantIDs: []string{"msg_a", "msg_c"},
		},
		{
			name:         "rescheduled job is due again with its attempt count",
			apply:        func(ctx context.Context, s *Store) error { return s.RescheduleSend(ctx, "msg_b", 2, retryAt) },
			checkAt:      retryAt,
			limit:        10,
			wantIDs:      []string{"msg_a", "msg_b", "msg_c"},
			wantAttempts: map[string]int{"msg_b": 2},
		},
		{
			name: "finished job is never due again",
			apply: func(ctx context.Context, s *Store) error {
				outcome := store.SendOutcome{State: store.SendSent, MessageID: "wamid.1", At: baseTime}
				return s.FinishSend(ctx, "msg_a", 1, outcome, resultEvent("evt_1", baseTime))
			},
			checkAt: baseTime.Add(time.Minute),
			limit:   10,
			wantIDs: []string{"msg_b", "msg_c"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s := openTestStore(t)
			seedSendJobs(t, s)
			if tt.apply != nil {
				if err := tt.apply(ctx, s); err != nil {
					t.Fatal(err)
				}
			}

			jobs, err := s.ListDueSends(ctx, tt.checkAt, tt.limit)
			if err != nil {
				t.Fatal(err)
			}
			if got := sendIDs(jobs); !slices.Equal(got, tt.wantIDs) {
				t.Fatalf("ListDueSends() = %v, want %v", got, tt.wantIDs)
			}
			for _, job := range jobs {
				if want, ok := tt.wantAttempts[job.ID]; ok && job.Attempts != want {
					t.Errorf("job %s attempts = %d, want %d", job.ID, job.Attempts, want)
				}
			}
		})
	}
}

func TestFinishSend(t *testing.T) {
	failure := store.SendOutcome{State: store.SendFailed, ErrorCode: "window_closed", ErrorMessage: "closed", MetaCode: 131047, At: baseTime.Add(time.Minute)}
	success := store.SendOutcome{State: store.SendSent, MessageID: "wamid.1", At: baseTime.Add(time.Minute)}
	sentStatus := store.SendStatus{ID: "msg_a", State: store.SendSent, MessageID: "wamid.1", Attempts: 2, CreatedAt: baseTime, UpdatedAt: baseTime.Add(time.Minute)}

	tests := []struct {
		name       string
		outcomes   []store.SendOutcome
		wantStatus store.SendStatus
		wantEvents int
	}{
		{name: "sent job records the message id", outcomes: []store.SendOutcome{success}, wantStatus: sentStatus, wantEvents: 1},
		{
			name:       "failed job records the error",
			outcomes:   []store.SendOutcome{failure},
			wantStatus: store.SendStatus{ID: "msg_a", State: store.SendFailed, ErrorCode: "window_closed", ErrorMessage: "closed", MetaCode: 131047, Attempts: 2, CreatedAt: baseTime, UpdatedAt: baseTime.Add(time.Minute)},
			wantEvents: 1,
		},
		{name: "finishing twice keeps the first outcome and queues one event", outcomes: []store.SendOutcome{success, failure}, wantStatus: sentStatus, wantEvents: 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s := openTestStore(t)
			if err := s.EnqueueSend(ctx, sendJob("msg_a", baseTime)); err != nil {
				t.Fatal(err)
			}
			for i, outcome := range tt.outcomes {
				eventID := []string{"evt_first", "evt_second"}[i]
				if err := s.FinishSend(ctx, "msg_a", 2, outcome, resultEvent(eventID, baseTime)); err != nil {
					t.Fatal(err)
				}
			}

			status, isKnown, err := s.SendStatus(ctx, "msg_a")
			if err != nil || !isKnown {
				t.Fatalf("SendStatus() = %+v, %v, %v", status, isKnown, err)
			}
			if status != tt.wantStatus {
				t.Errorf("status = %+v, want %+v", status, tt.wantStatus)
			}
			if got := countRows(t, s, `SELECT COUNT(*) FROM outbox_entries WHERE topic = 'wa.results'`); got != tt.wantEvents {
				t.Errorf("result events = %d, want %d", got, tt.wantEvents)
			}
			if got := countRows(t, s, `SELECT COUNT(*) FROM send_jobs WHERE length(payload) > 0`); got != 0 {
				t.Error("a finished job still holds its message body")
			}
		})
	}
}

func TestSendStatus(t *testing.T) {
	tests := []struct {
		name      string
		enqueue   bool
		id        string
		wantKnown bool
		want      store.SendStatus
	}{
		{name: "queued job", enqueue: true, id: "msg_a", wantKnown: true, want: store.SendStatus{ID: "msg_a", State: store.SendQueued, CreatedAt: baseTime, UpdatedAt: baseTime}},
		{name: "unknown job", enqueue: true, id: "msg_missing"},
		{name: "empty table", id: "msg_a"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s := openTestStore(t)
			if tt.enqueue {
				if err := s.EnqueueSend(ctx, sendJob("msg_a", baseTime)); err != nil {
					t.Fatal(err)
				}
			}

			status, isKnown, err := s.SendStatus(ctx, tt.id)

			if err != nil || isKnown != tt.wantKnown || status != tt.want {
				t.Fatalf("SendStatus() = %+v, %v, %v, want %+v, %v", status, isKnown, err, tt.want, tt.wantKnown)
			}
		})
	}
}

func TestDeleteExpiredSends(t *testing.T) {
	tests := []struct {
		name     string
		before   time.Time
		wantKept []string
	}{
		{name: "cutoff before everything keeps all", before: baseTime.Add(-time.Hour), wantKept: []string{"msg_queued", "msg_old", "msg_new"}},
		{name: "old finished job is removed, queued job stays", before: baseTime.Add(90 * time.Minute), wantKept: []string{"msg_queued", "msg_new"}},
		{name: "every finished job is removed, queued job stays", before: baseTime.Add(24 * time.Hour), wantKept: []string{"msg_queued"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s := openTestStore(t)
			for _, id := range []string{"msg_queued", "msg_old", "msg_new"} {
				if err := s.EnqueueSend(ctx, sendJob(id, baseTime)); err != nil {
					t.Fatal(err)
				}
			}
			finishedAt := map[string]time.Time{"msg_old": baseTime.Add(time.Hour), "msg_new": baseTime.Add(2 * time.Hour)}
			for id, at := range finishedAt {
				outcome := store.SendOutcome{State: store.SendSent, MessageID: "w", At: at}
				if err := s.FinishSend(ctx, id, 1, outcome, resultEvent("evt_"+id, at)); err != nil {
					t.Fatal(err)
				}
			}

			if err := s.DeleteExpiredSends(ctx, tt.before); err != nil {
				t.Fatal(err)
			}

			kept := []string{}
			for _, id := range []string{"msg_queued", "msg_old", "msg_new"} {
				if _, isKnown, _ := s.SendStatus(ctx, id); isKnown {
					kept = append(kept, id)
				}
			}
			if !slices.Equal(kept, tt.wantKept) {
				t.Fatalf("kept %v, want %v", kept, tt.wantKept)
			}
		})
	}
}

func TestEnqueueSendPayloads(t *testing.T) {
	tests := []struct {
		name    string
		payload []byte
		wantLen int
	}{
		{name: "regular payload is stored", payload: []byte(`{"to":"94770000000"}`), wantLen: 20},
		{name: "empty payload is stored", payload: []byte{}, wantLen: 0},
		{name: "missing payload is stored as empty", payload: nil, wantLen: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s := openTestStore(t)

			err := s.EnqueueSend(ctx, store.SendJob{ID: "msg_a", Payload: tt.payload, NextAttemptAt: baseTime, CreatedAt: baseTime})

			if err != nil {
				t.Fatalf("EnqueueSend() error = %v", err)
			}
			if got := countRows(t, s, `SELECT length(payload) FROM send_jobs WHERE id = 'msg_a'`); got != tt.wantLen {
				t.Errorf("stored payload length = %d, want %d", got, tt.wantLen)
			}
			jobs, err := s.ListDueSends(ctx, baseTime, 10)
			if err != nil || len(jobs) != 1 || len(jobs[0].Payload) != tt.wantLen {
				t.Errorf("ListDueSends() = %+v, %v", jobs, err)
			}
		})
	}
}
