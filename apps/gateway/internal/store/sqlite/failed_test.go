package sqlite

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/store"
)

func seedFailed(t *testing.T, s *Store) {
	t.Helper()
	ctx := context.Background()
	if _, err := s.SaveInbound(ctx, []store.InboundRecord{
		record("msg:a", "evt_a", baseTime),
		record("msg:b", "evt_b", baseTime.Add(time.Second)),
		record("msg:c", "evt_c", baseTime.Add(2*time.Second)),
		record("msg:d", "evt_d", baseTime.Add(3*time.Second)),
		record("msg:e", "evt_e", baseTime.Add(4*time.Second)),
	}); err != nil {
		t.Fatal(err)
	}
	failures := []struct {
		id string
		at time.Time
	}{
		{"evt_a", baseTime.Add(10 * time.Minute)},
		{"evt_b", baseTime.Add(20 * time.Minute)},
		{"evt_c", baseTime.Add(5 * time.Minute)},
		{"evt_e", baseTime.Add(5 * time.Minute)},
	}
	for _, failure := range failures {
		if err := s.MarkFailed(ctx, failure.id, 8, "webhook responded with status 500", failure.at); err != nil {
			t.Fatal(err)
		}
	}
}

func failedIDs(entries []store.FailedEntry) []string {
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.ID)
	}
	return ids
}

func TestListFailed(t *testing.T) {
	tests := []struct {
		name    string
		limit   int
		offset  int
		wantIDs []string
	}{
		{name: "newest failure first, ties broken by id, pending entries excluded", limit: 10, wantIDs: []string{"evt_b", "evt_a", "evt_c", "evt_e"}},
		{name: "limit cuts the page", limit: 2, wantIDs: []string{"evt_b", "evt_a"}},
		{name: "offset skips earlier entries", limit: 2, offset: 2, wantIDs: []string{"evt_c", "evt_e"}},
		{name: "last partial page", limit: 3, offset: 3, wantIDs: []string{"evt_e"}},
		{name: "offset past the end is empty", limit: 10, offset: 4, wantIDs: []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := openTestStore(t)
			seedFailed(t, s)

			entries, err := s.ListFailed(context.Background(), tt.limit, tt.offset)
			if err != nil {
				t.Fatal(err)
			}
			if entries == nil {
				t.Fatal("ListFailed() returned nil, want an empty slice")
			}
			if got := failedIDs(entries); !slices.Equal(got, tt.wantIDs) {
				t.Fatalf("ListFailed() ids = %v, want %v", got, tt.wantIDs)
			}
		})
	}
}

func TestListFailedReportsTheEntryDetails(t *testing.T) {
	tests := []struct {
		name   string
		offset int
		want   store.FailedEntry
	}{
		{
			name: "newest failure",
			want: store.FailedEntry{
				ID: "evt_b", Topic: "wa.inbound", Attempts: 8, LastError: "webhook responded with status 500",
				CreatedAt: baseTime.Add(time.Second), FailedAt: baseTime.Add(20 * time.Minute),
			},
		},
		{
			name:   "older failure",
			offset: 1,
			want: store.FailedEntry{
				ID: "evt_a", Topic: "wa.inbound", Attempts: 8, LastError: "webhook responded with status 500",
				CreatedAt: baseTime, FailedAt: baseTime.Add(10 * time.Minute),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := openTestStore(t)
			seedFailed(t, s)

			entries, err := s.ListFailed(context.Background(), 1, tt.offset)
			if err != nil {
				t.Fatal(err)
			}
			if len(entries) != 1 || entries[0] != tt.want {
				t.Fatalf("ListFailed() = %+v, want [%+v]", entries, tt.want)
			}
		})
	}
}

func TestRequeue(t *testing.T) {
	requeueAt := baseTime.Add(time.Hour)

	tests := []struct {
		name         string
		id           string
		isRepeated   bool
		wantQueued   bool
		wantDue      bool
		wantAttempts int
	}{
		{name: "failed entry goes back to pending with fresh attempts", id: "evt_a", wantQueued: true, wantDue: true, wantAttempts: 0},
		{name: "pending entry is left alone", id: "evt_d", wantQueued: false, wantDue: true},
		{name: "unknown entry", id: "evt_missing", wantQueued: false},
		{name: "second requeue of the same entry does nothing", id: "evt_a", isRepeated: true, wantQueued: false, wantDue: true, wantAttempts: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s := openTestStore(t)
			seedFailed(t, s)

			isQueued, err := s.Requeue(ctx, tt.id, requeueAt)
			if err != nil {
				t.Fatal(err)
			}
			if tt.isRepeated {
				if isQueued, err = s.Requeue(ctx, tt.id, requeueAt); err != nil {
					t.Fatal(err)
				}
			}
			if isQueued != tt.wantQueued {
				t.Fatalf("Requeue() = %v, want %v", isQueued, tt.wantQueued)
			}

			due, err := s.ListDue(ctx, requeueAt, 10)
			if err != nil {
				t.Fatal(err)
			}
			var found *store.OutboxEntry
			for i := range due {
				if due[i].ID == tt.id {
					found = &due[i]
				}
			}
			if (found != nil) != tt.wantDue {
				t.Fatalf("entry %s due = %v, want %v", tt.id, found != nil, tt.wantDue)
			}
			if found != nil && tt.wantQueued && found.Attempts != tt.wantAttempts {
				t.Errorf("attempts = %d, want %d", found.Attempts, tt.wantAttempts)
			}
			if tt.wantQueued {
				failed, err := s.ListFailed(ctx, 10, 0)
				if err != nil {
					t.Fatal(err)
				}
				if slices.Contains(failedIDs(failed), tt.id) {
					t.Errorf("requeued entry %s is still listed as failed", tt.id)
				}
			}
		})
	}
}

func TestRequeueAndRetention(t *testing.T) {
	requeueAt := baseTime.Add(48 * time.Hour)

	tests := []struct {
		name         string
		isRequeued   bool
		wantSurvivor []string
	}{
		{name: "entry that failed again after the cutoff survives cleanup", isRequeued: true, wantSurvivor: []string{"evt_a"}},
		{name: "entry that was never requeued is cleaned up", isRequeued: false, wantSurvivor: []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s := openTestStore(t)
			seedFailed(t, s)

			failedAgainAt := baseTime.Add(10 * time.Minute)
			if tt.isRequeued {
				if _, err := s.Requeue(ctx, "evt_a", requeueAt); err != nil {
					t.Fatal(err)
				}
				failedAgainAt = requeueAt.Add(time.Minute)
			}
			if err := s.MarkFailed(ctx, "evt_a", 8, "again", failedAgainAt); err != nil {
				t.Fatal(err)
			}
			for _, id := range []string{"evt_b", "evt_c", "evt_e"} {
				if _, err := s.Requeue(ctx, id, baseTime); err != nil {
					t.Fatal(err)
				}
			}
			if err := s.DeleteExpired(ctx, requeueAt, requeueAt); err != nil {
				t.Fatal(err)
			}

			entries, err := s.ListFailed(ctx, 10, 0)
			if err != nil {
				t.Fatal(err)
			}
			if got := failedIDs(entries); !slices.Equal(got, tt.wantSurvivor) {
				t.Fatalf("after cleanup failed ids = %v, want %v", got, tt.wantSurvivor)
			}
		})
	}
}

func TestRequeueIsExclusiveUnderConcurrency(t *testing.T) {
	tests := []struct {
		name    string
		callers int
	}{
		{name: "two callers", callers: 2},
		{name: "many callers", callers: 16},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s := openTestStore(t)
			seedFailed(t, s)

			results := make(chan bool, tt.callers)
			for range tt.callers {
				go func() {
					isQueued, err := s.Requeue(ctx, "evt_a", baseTime.Add(time.Hour))
					if err != nil {
						t.Error(err)
					}
					results <- isQueued
				}()
			}

			winners := 0
			for range tt.callers {
				if <-results {
					winners++
				}
			}
			if winners != 1 {
				t.Fatalf("%d concurrent requeues succeeded, want exactly 1", winners)
			}
		})
	}
}
