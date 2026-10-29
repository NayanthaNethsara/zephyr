package sqlite

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/store"
)

func claimAt(key, fingerprint string, now time.Time) store.IdempotencyClaim {
	return store.IdempotencyClaim{
		Key:         key,
		Fingerprint: fingerprint,
		Now:         now,
		LeaseUntil:  now.Add(time.Minute),
		ExpiresAt:   now.Add(24 * time.Hour),
	}
}

func TestIdempotencyLifecycle(t *testing.T) {
	type step struct {
		action        string
		claim         store.IdempotencyClaim
		messageID     string
		wantOutcome   store.IdempotencyOutcome
		wantMessageID string
	}

	tests := []struct {
		name  string
		steps []step
	}{
		{
			name: "first claim is acquired",
			steps: []step{
				{action: "claim", claim: claimAt("k1", "fp", baseTime), wantOutcome: store.IdempotencyAcquired},
			},
		},
		{
			name: "second claim while the first is in flight is in progress",
			steps: []step{
				{action: "claim", claim: claimAt("k1", "fp", baseTime), wantOutcome: store.IdempotencyAcquired},
				{action: "claim", claim: claimAt("k1", "fp", baseTime.Add(time.Second)), wantOutcome: store.IdempotencyInProgress},
			},
		},
		{
			name: "completed key replays the stored message id",
			steps: []step{
				{action: "claim", claim: claimAt("k1", "fp", baseTime), wantOutcome: store.IdempotencyAcquired},
				{action: "complete", claim: claimAt("k1", "fp", baseTime), messageID: "wamid.1"},
				{action: "claim", claim: claimAt("k1", "fp", baseTime.Add(time.Hour)), wantOutcome: store.IdempotencyCompleted, wantMessageID: "wamid.1"},
			},
		},
		{
			name: "different payload under the same key is a mismatch",
			steps: []step{
				{action: "claim", claim: claimAt("k1", "fp-a", baseTime), wantOutcome: store.IdempotencyAcquired},
				{action: "claim", claim: claimAt("k1", "fp-b", baseTime.Add(time.Second)), wantOutcome: store.IdempotencyMismatch},
			},
		},
		{
			name: "mismatch is reported for a completed key as well",
			steps: []step{
				{action: "claim", claim: claimAt("k1", "fp-a", baseTime), wantOutcome: store.IdempotencyAcquired},
				{action: "complete", claim: claimAt("k1", "fp-a", baseTime), messageID: "wamid.1"},
				{action: "claim", claim: claimAt("k1", "fp-b", baseTime.Add(time.Second)), wantOutcome: store.IdempotencyMismatch},
			},
		},
		{
			name: "released key can be claimed again",
			steps: []step{
				{action: "claim", claim: claimAt("k1", "fp", baseTime), wantOutcome: store.IdempotencyAcquired},
				{action: "release", claim: claimAt("k1", "fp", baseTime)},
				{action: "claim", claim: claimAt("k1", "fp", baseTime.Add(time.Second)), wantOutcome: store.IdempotencyAcquired},
			},
		},
		{
			name: "release never removes a completed key",
			steps: []step{
				{action: "claim", claim: claimAt("k1", "fp", baseTime), wantOutcome: store.IdempotencyAcquired},
				{action: "complete", claim: claimAt("k1", "fp", baseTime), messageID: "wamid.1"},
				{action: "release", claim: claimAt("k1", "fp", baseTime)},
				{action: "claim", claim: claimAt("k1", "fp", baseTime.Add(time.Second)), wantOutcome: store.IdempotencyCompleted, wantMessageID: "wamid.1"},
			},
		},
		{
			name: "abandoned claim can be taken over after its lease",
			steps: []step{
				{action: "claim", claim: claimAt("k1", "fp", baseTime), wantOutcome: store.IdempotencyAcquired},
				{action: "claim", claim: claimAt("k1", "fp", baseTime.Add(2*time.Minute)), wantOutcome: store.IdempotencyAcquired},
			},
		},
		{
			name: "expired key starts over, even with a different payload",
			steps: []step{
				{action: "claim", claim: claimAt("k1", "fp-a", baseTime), wantOutcome: store.IdempotencyAcquired},
				{action: "complete", claim: claimAt("k1", "fp-a", baseTime), messageID: "wamid.1"},
				{action: "claim", claim: claimAt("k1", "fp-b", baseTime.Add(25*time.Hour)), wantOutcome: store.IdempotencyAcquired},
			},
		},
		{
			name: "keys are independent",
			steps: []step{
				{action: "claim", claim: claimAt("k1", "fp", baseTime), wantOutcome: store.IdempotencyAcquired},
				{action: "claim", claim: claimAt("k2", "fp", baseTime), wantOutcome: store.IdempotencyAcquired},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s := openTestStore(t)

			for i, st := range tt.steps {
				switch st.action {
				case "claim":
					result, err := s.ClaimIdempotencyKey(ctx, st.claim)
					if err != nil {
						t.Fatalf("step %d: claim error = %v", i, err)
					}
					if result.Outcome != st.wantOutcome || result.MessageID != st.wantMessageID {
						t.Fatalf("step %d: result = %+v, want %s with message %q", i, result, st.wantOutcome, st.wantMessageID)
					}
				case "complete":
					if err := s.CompleteIdempotencyKey(ctx, st.claim.Key, st.messageID); err != nil {
						t.Fatalf("step %d: complete error = %v", i, err)
					}
				case "release":
					if err := s.ReleaseIdempotencyKey(ctx, st.claim.Key); err != nil {
						t.Fatalf("step %d: release error = %v", i, err)
					}
				}
			}
		})
	}
}

func TestDeleteExpiredRemovesOldIdempotencyKeys(t *testing.T) {
	tests := []struct {
		name     string
		now      time.Time
		wantKeys int
	}{
		{name: "nothing expired yet", now: baseTime.Add(time.Hour), wantKeys: 2},
		{name: "short lived key expires first", now: baseTime.Add(30 * time.Hour), wantKeys: 1},
		{name: "everything expired", now: baseTime.Add(72 * time.Hour), wantKeys: 0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s := openTestStore(t)

			short := claimAt("short", "fp", baseTime)
			long := claimAt("long", "fp", baseTime)
			long.ExpiresAt = baseTime.Add(48 * time.Hour)
			for _, claim := range []store.IdempotencyClaim{short, long} {
				if _, err := s.ClaimIdempotencyKey(ctx, claim); err != nil {
					t.Fatal(err)
				}
			}

			if err := s.DeleteExpired(ctx, tt.now, tt.now.Add(-time.Hour)); err != nil {
				t.Fatal(err)
			}

			if got := countRows(t, s, `SELECT COUNT(*) FROM idempotency_keys`); got != tt.wantKeys {
				t.Fatalf("idempotency_keys rows = %d, want %d", got, tt.wantKeys)
			}
		})
	}
}

func TestConcurrentClaimsHaveExactlyOneWinner(t *testing.T) {
	tests := []struct {
		name    string
		workers int
	}{
		{name: "two simultaneous requests", workers: 2},
		{name: "twenty simultaneous requests", workers: 20},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := openTestStore(t)

			outcomes := make(chan store.IdempotencyOutcome, tt.workers)
			var group sync.WaitGroup
			for range tt.workers {
				group.Add(1)
				go func() {
					defer group.Done()
					result, err := s.ClaimIdempotencyKey(context.Background(), claimAt("same-key", "fp", baseTime))
					if err != nil {
						t.Errorf("claim error = %v", err)
						return
					}
					outcomes <- result.Outcome
				}()
			}
			group.Wait()
			close(outcomes)

			counts := map[store.IdempotencyOutcome]int{}
			for outcome := range outcomes {
				counts[outcome]++
			}
			if counts[store.IdempotencyAcquired] != 1 || counts[store.IdempotencyInProgress] != tt.workers-1 {
				t.Fatalf("outcomes = %v, want 1 acquired and %d in progress", counts, tt.workers-1)
			}
		})
	}
}
