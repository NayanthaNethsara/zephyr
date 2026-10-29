package sqlite

import (
	"context"
	"os"
	"path/filepath"
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

func TestSaveInboundDeduplicates(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	saved, err := s.SaveInbound(ctx, []store.InboundRecord{
		record("msg:1", "evt_1", baseTime),
		record("msg:1", "evt_dup_in_batch", baseTime),
		record("msg:2", "evt_2", baseTime),
	})
	if err != nil || len(saved) != 2 || saved[0] != "evt_1" || saved[1] != "evt_2" {
		t.Fatalf("SaveInbound() = %v, %v, want [evt_1 evt_2], nil", saved, err)
	}

	saved, err = s.SaveInbound(ctx, []store.InboundRecord{record("msg:1", "evt_retry", baseTime.Add(time.Minute))})
	if err != nil || len(saved) != 0 {
		t.Fatalf("duplicate SaveInbound() = %v, %v, want none", saved, err)
	}

	entries, err := s.ListDue(ctx, baseTime.Add(time.Hour), 10)
	if err != nil || len(entries) != 2 {
		t.Fatalf("ListDue() = %d entries, %v, want 2", len(entries), err)
	}
}

func TestSaveInboundAcceptsExpiredDedupKey(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	if _, err := s.SaveInbound(ctx, []store.InboundRecord{record("msg:1", "evt_1", baseTime)}); err != nil {
		t.Fatal(err)
	}

	later := baseTime.Add(2 * time.Hour)
	saved, err := s.SaveInbound(ctx, []store.InboundRecord{record("msg:1", "evt_again", later)})
	if err != nil || len(saved) != 1 {
		t.Fatalf("SaveInbound() after expiry = %v, %v, want one", saved, err)
	}
}

func TestListDueRespectsScheduleAndOrder(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	early := record("msg:1", "evt_1", baseTime)
	late := record("msg:2", "evt_2", baseTime.Add(time.Second))
	late.Entry.NextAttemptAt = baseTime.Add(time.Hour)
	third := record("msg:3", "evt_3", baseTime.Add(2*time.Second))

	if _, err := s.SaveInbound(ctx, []store.InboundRecord{third, late, early}); err != nil {
		t.Fatal(err)
	}

	entries, err := s.ListDue(ctx, baseTime.Add(time.Minute), 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 2 || entries[0].ID != "evt_1" || entries[1].ID != "evt_3" {
		t.Fatalf("ListDue() = %+v, want evt_1 then evt_3", entries)
	}
	if string(entries[0].Payload) != `{"id":"evt_1"}` || entries[0].Topic != "wa.inbound" {
		t.Errorf("entry = %+v", entries[0])
	}

	limited, err := s.ListDue(ctx, baseTime.Add(time.Minute), 1)
	if err != nil || len(limited) != 1 {
		t.Fatalf("ListDue(limit 1) = %d, %v", len(limited), err)
	}
}

func TestEntryLifecycle(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	records := []store.InboundRecord{
		record("msg:1", "evt_delivered", baseTime),
		record("msg:2", "evt_retry", baseTime),
		record("msg:3", "evt_failed", baseTime),
	}
	if _, err := s.SaveInbound(ctx, records); err != nil {
		t.Fatal(err)
	}

	if err := s.Delete(ctx, "evt_delivered"); err != nil {
		t.Fatal(err)
	}
	next := baseTime.Add(time.Hour)
	if err := s.Reschedule(ctx, "evt_retry", 2, next, "boom"); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkFailed(ctx, "evt_failed", 8, "gave up", baseTime); err != nil {
		t.Fatal(err)
	}

	due, err := s.ListDue(ctx, baseTime.Add(time.Minute), 10)
	if err != nil || len(due) != 0 {
		t.Fatalf("ListDue() before retry time = %d, %v, want 0", len(due), err)
	}

	due, err = s.ListDue(ctx, next, 10)
	if err != nil || len(due) != 1 {
		t.Fatalf("ListDue() at retry time = %d, %v, want 1", len(due), err)
	}
	if due[0].ID != "evt_retry" || due[0].Attempts != 2 || due[0].LastError != "boom" {
		t.Errorf("entry = %+v", due[0])
	}
}

func TestDeleteExpired(t *testing.T) {
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

	now := baseTime.Add(10 * 24 * time.Hour)
	if err := s.DeleteExpired(ctx, now, now.Add(-7*24*time.Hour)); err != nil {
		t.Fatal(err)
	}

	var failed, pending, dedup int
	for query, target := range map[string]*int{
		`SELECT COUNT(*) FROM outbox WHERE state = 'failed'`:  &failed,
		`SELECT COUNT(*) FROM outbox WHERE state = 'pending'`: &pending,
		`SELECT COUNT(*) FROM dedup`:                          &dedup,
	} {
		if err := s.db.QueryRowContext(ctx, query).Scan(target); err != nil {
			t.Fatal(err)
		}
	}
	if failed != 1 || pending != 1 || dedup != 0 {
		t.Fatalf("failed=%d pending=%d dedup=%d, want 1, 1, 0", failed, pending, dedup)
	}
}

func TestPing(t *testing.T) {
	if err := openTestStore(t).Ping(context.Background()); err != nil {
		t.Fatalf("Ping() error = %v", err)
	}
}

func TestOpenCreatesPrivateDatabaseFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "gateway.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = s.Close() }()

	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if mode := info.Mode().Perm(); mode != 0o600 {
		t.Fatalf("database file mode = %o, want 600", mode)
	}
}

func TestOutboxStats(t *testing.T) {
	ctx := context.Background()
	s := openTestStore(t)

	empty, err := s.OutboxStats(ctx)
	if err != nil || empty.Pending != 0 || empty.Failed != 0 || !empty.OldestPendingCreatedAt.IsZero() {
		t.Fatalf("empty OutboxStats() = %+v, %v", empty, err)
	}

	if _, err := s.SaveInbound(ctx, []store.InboundRecord{
		record("msg:1", "evt_1", baseTime.Add(time.Minute)),
		record("msg:2", "evt_2", baseTime),
		record("msg:3", "evt_3", baseTime.Add(2*time.Minute)),
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.MarkFailed(ctx, "evt_3", 8, "x", baseTime); err != nil {
		t.Fatal(err)
	}

	stats, err := s.OutboxStats(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if stats.Pending != 2 || stats.Failed != 1 || !stats.OldestPendingCreatedAt.Equal(baseTime) {
		t.Fatalf("OutboxStats() = %+v, want 2 pending, 1 failed, oldest %s", stats, baseTime)
	}
}
