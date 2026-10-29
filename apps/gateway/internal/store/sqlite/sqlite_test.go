package sqlite

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
	"time"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/store"
)

var baseTime = time.Date(2026, 11, 1, 10, 0, 0, 0, time.UTC)

func openTestStore(t *testing.T) *Store {
	t.Helper()
	s, err := Open(context.Background(), filepath.Join(t.TempDir(), "nested", "gateway.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func record(key, id string, at time.Time) store.InboundRecord {
	return store.InboundRecord{
		DedupKey:       key,
		DedupExpiresAt: at.Add(time.Hour),
		Entry: store.OutboxEntry{
			ID:            id,
			Topic:         "wa.inbound",
			Key:           "94770000000",
			Payload:       []byte(`{"id":"` + id + `"}`),
			NextAttemptAt: at,
			CreatedAt:     at,
		},
	}
}

func entryIDs(entries []store.OutboxEntry) []string {
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		ids = append(ids, entry.ID)
	}
	return ids
}

func countRows(t *testing.T, s *Store, query string) int {
	t.Helper()
	var count int
	if err := s.db.QueryRowContext(context.Background(), query).Scan(&count); err != nil {
		t.Fatal(err)
	}
	return count
}

func TestOpen(t *testing.T) {
	tests := []struct {
		name         string
		relativePath string
	}{
		{name: "nested directories are created", relativePath: "a/b/gateway.db"},
		{name: "file in an existing directory", relativePath: "gateway.db"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), tt.relativePath)
			s, err := Open(context.Background(), path)
			if err != nil {
				t.Fatalf("Open() error = %v", err)
			}
			defer func() { _ = s.Close() }()

			info, err := os.Stat(path)
			if err != nil {
				t.Fatal(err)
			}
			if mode := info.Mode().Perm(); mode != 0o600 {
				t.Errorf("database file mode = %o, want 600", mode)
			}
			if err := s.Ping(context.Background()); err != nil {
				t.Errorf("Ping() error = %v", err)
			}
			if got := countRows(t, s, `SELECT COUNT(*) FROM sqlite_master WHERE name IN ('dedup_keys', 'outbox_entries')`); got != 2 {
				t.Errorf("migrated tables = %d, want 2", got)
			}
		})
	}
}

func TestSaveInbound(t *testing.T) {
	type batch struct {
		records   []store.InboundRecord
		wantSaved []string
	}

	tests := []struct {
		name    string
		batches []batch
	}{
		{
			name: "new keys are all saved",
			batches: []batch{{
				records:   []store.InboundRecord{record("msg:1", "evt_1", baseTime), record("msg:2", "evt_2", baseTime)},
				wantSaved: []string{"evt_1", "evt_2"},
			}},
		},
		{
			name: "same key twice in one batch is saved once",
			batches: []batch{{
				records:   []store.InboundRecord{record("msg:1", "evt_1", baseTime), record("msg:1", "evt_dup", baseTime)},
				wantSaved: []string{"evt_1"},
			}},
		},
		{
			name: "repeat of an earlier batch is dropped",
			batches: []batch{
				{records: []store.InboundRecord{record("msg:1", "evt_1", baseTime)}, wantSaved: []string{"evt_1"}},
				{records: []store.InboundRecord{record("msg:1", "evt_retry", baseTime.Add(time.Minute))}, wantSaved: []string{}},
			},
		},
		{
			name: "key is accepted again after its ttl expires",
			batches: []batch{
				{records: []store.InboundRecord{record("msg:1", "evt_1", baseTime)}, wantSaved: []string{"evt_1"}},
				{records: []store.InboundRecord{record("msg:1", "evt_again", baseTime.Add(2*time.Hour))}, wantSaved: []string{"evt_again"}},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := openTestStore(t)
			for i, b := range tt.batches {
				saved, err := s.SaveInbound(context.Background(), b.records)
				if err != nil {
					t.Fatalf("batch %d: SaveInbound() error = %v", i, err)
				}
				if !slices.Equal(saved, b.wantSaved) {
					t.Errorf("batch %d: saved = %v, want %v", i, saved, b.wantSaved)
				}
			}
		})
	}
}

func TestListDue(t *testing.T) {
	early := record("msg:1", "evt_1", baseTime)
	late := record("msg:2", "evt_2", baseTime.Add(time.Second))
	late.Entry.NextAttemptAt = baseTime.Add(time.Hour)
	third := record("msg:3", "evt_3", baseTime.Add(2*time.Second))

	tests := []struct {
		name    string
		now     time.Time
		limit   int
		wantIDs []string
	}{
		{name: "only due entries in creation order", now: baseTime.Add(time.Minute), limit: 10, wantIDs: []string{"evt_1", "evt_3"}},
		{name: "scheduled entry becomes due", now: baseTime.Add(time.Hour), limit: 10, wantIDs: []string{"evt_1", "evt_2", "evt_3"}},
		{name: "limit is applied", now: baseTime.Add(time.Hour), limit: 1, wantIDs: []string{"evt_1"}},
		{name: "nothing due yet", now: baseTime.Add(-time.Minute), limit: 10, wantIDs: []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := openTestStore(t)
			if _, err := s.SaveInbound(context.Background(), []store.InboundRecord{third, late, early}); err != nil {
				t.Fatal(err)
			}

			entries, err := s.ListDue(context.Background(), tt.now, tt.limit)
			if err != nil {
				t.Fatal(err)
			}
			if got := entryIDs(entries); !slices.Equal(got, tt.wantIDs) {
				t.Fatalf("ListDue() ids = %v, want %v", got, tt.wantIDs)
			}
			if len(entries) > 0 && (string(entries[0].Payload) != `{"id":"evt_1"}` || entries[0].Topic != "wa.inbound") {
				t.Errorf("entry fields = %+v", entries[0])
			}
		})
	}
}

func TestEntryUpdates(t *testing.T) {
	retryAt := baseTime.Add(time.Hour)

	tests := []struct {
		name         string
		apply        func(ctx context.Context, s *Store) error
		checkAt      time.Time
		wantIDs      []string
		wantAttempts map[string]int
	}{
		{
			name:    "delete removes the entry",
			apply:   func(ctx context.Context, s *Store) error { return s.Delete(ctx, "evt_a") },
			checkAt: baseTime.Add(time.Minute),
			wantIDs: []string{"evt_b", "evt_c"},
		},
		{
			name: "rescheduled entry is not due before its retry time",
			apply: func(ctx context.Context, s *Store) error {
				return s.Reschedule(ctx, "evt_b", 2, retryAt, "boom")
			},
			checkAt: baseTime.Add(time.Minute),
			wantIDs: []string{"evt_a", "evt_c"},
		},
		{
			name: "rescheduled entry is due again at its retry time with its history",
			apply: func(ctx context.Context, s *Store) error {
				return s.Reschedule(ctx, "evt_b", 2, retryAt, "boom")
			},
			checkAt:      retryAt,
			wantIDs:      []string{"evt_a", "evt_b", "evt_c"},
			wantAttempts: map[string]int{"evt_b": 2},
		},
		{
			name: "failed entry is never due again",
			apply: func(ctx context.Context, s *Store) error {
				return s.MarkFailed(ctx, "evt_c", 8, "gave up", baseTime)
			},
			checkAt: baseTime.Add(24 * time.Hour),
			wantIDs: []string{"evt_a", "evt_b"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s := openTestStore(t)
			if _, err := s.SaveInbound(ctx, []store.InboundRecord{
				record("msg:a", "evt_a", baseTime),
				record("msg:b", "evt_b", baseTime.Add(time.Second)),
				record("msg:c", "evt_c", baseTime.Add(2*time.Second)),
			}); err != nil {
				t.Fatal(err)
			}
			if err := tt.apply(ctx, s); err != nil {
				t.Fatal(err)
			}

			entries, err := s.ListDue(ctx, tt.checkAt, 10)
			if err != nil {
				t.Fatal(err)
			}
			if got := entryIDs(entries); !slices.Equal(got, tt.wantIDs) {
				t.Fatalf("ListDue() ids = %v, want %v", got, tt.wantIDs)
			}
			for _, entry := range entries {
				if want, ok := tt.wantAttempts[entry.ID]; ok && (entry.Attempts != want || entry.LastError != "boom") {
					t.Errorf("entry %s attempts/lastError = %d/%q, want %d/boom", entry.ID, entry.Attempts, entry.LastError, want)
				}
			}
		})
	}
}

func TestDeleteExpired(t *testing.T) {
	tests := []struct {
		name         string
		now          time.Time
		failedBefore time.Time
		wantFailed   int
		wantPending  int
		wantDedup    int
	}{
		{
			name:         "removes expired keys and old failed entries only",
			now:          baseTime.Add(10 * 24 * time.Hour),
			failedBefore: baseTime.Add(3 * 24 * time.Hour),
			wantFailed:   1,
			wantPending:  1,
			wantDedup:    0,
		},
		{
			name:         "keeps everything that is still within retention",
			now:          baseTime,
			failedBefore: baseTime.Add(-time.Hour),
			wantFailed:   2,
			wantPending:  1,
			wantDedup:    3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s := openTestStore(t)
			if _, err := s.SaveInbound(ctx, []store.InboundRecord{
				record("msg:old", "evt_old_failed", baseTime),
				record("msg:new", "evt_new_failed", baseTime),
				record("msg:pending", "evt_pending", baseTime),
			}); err != nil {
				t.Fatal(err)
			}
			if err := s.MarkFailed(ctx, "evt_old_failed", 8, "x", baseTime); err != nil {
				t.Fatal(err)
			}
			if err := s.MarkFailed(ctx, "evt_new_failed", 8, "x", baseTime.Add(10*24*time.Hour)); err != nil {
				t.Fatal(err)
			}

			if err := s.DeleteExpired(ctx, tt.now, tt.failedBefore); err != nil {
				t.Fatal(err)
			}

			got := map[string]int{
				"failed":  countRows(t, s, `SELECT COUNT(*) FROM outbox_entries WHERE state = 'failed'`),
				"pending": countRows(t, s, `SELECT COUNT(*) FROM outbox_entries WHERE state = 'pending'`),
				"dedup":   countRows(t, s, `SELECT COUNT(*) FROM dedup_keys`),
			}
			want := map[string]int{"failed": tt.wantFailed, "pending": tt.wantPending, "dedup": tt.wantDedup}
			for key, wantCount := range want {
				if got[key] != wantCount {
					t.Errorf("%s rows = %d, want %d", key, got[key], wantCount)
				}
			}
		})
	}
}

func TestOutboxStats(t *testing.T) {
	tests := []struct {
		name      string
		records   []store.InboundRecord
		failedIDs []string
		want      store.OutboxStats
	}{
		{
			name: "empty outbox",
			want: store.OutboxStats{},
		},
		{
			name: "pending backlog and one failure",
			records: []store.InboundRecord{
				record("msg:1", "evt_1", baseTime.Add(time.Minute)),
				record("msg:2", "evt_2", baseTime),
				record("msg:3", "evt_3", baseTime.Add(2*time.Minute)),
			},
			failedIDs: []string{"evt_3"},
			want:      store.OutboxStats{Pending: 2, Failed: 1, OldestPendingCreatedAt: baseTime},
		},
		{
			name:      "only failed entries leaves no oldest pending",
			records:   []store.InboundRecord{record("msg:1", "evt_1", baseTime)},
			failedIDs: []string{"evt_1"},
			want:      store.OutboxStats{Failed: 1},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s := openTestStore(t)
			if len(tt.records) > 0 {
				if _, err := s.SaveInbound(ctx, tt.records); err != nil {
					t.Fatal(err)
				}
			}
			for _, id := range tt.failedIDs {
				if err := s.MarkFailed(ctx, id, 8, "x", baseTime); err != nil {
					t.Fatal(err)
				}
			}

			got, err := s.OutboxStats(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if got.Pending != tt.want.Pending || got.Failed != tt.want.Failed || !got.OldestPendingCreatedAt.Equal(tt.want.OldestPendingCreatedAt) {
				t.Fatalf("OutboxStats() = %+v, want %+v", got, tt.want)
			}
		})
	}
}
