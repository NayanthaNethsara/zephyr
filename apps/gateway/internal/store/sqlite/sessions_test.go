package sqlite

import (
	"context"
	"testing"
	"time"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/store"
)

func withSession(r store.InboundRecord, hash, numberID string, at time.Time) store.InboundRecord {
	r.Session = &store.SessionTouch{PhoneHash: hash, PhoneNumberID: numberID, At: at}
	return r
}

func TestSaveInboundRecordsSessions(t *testing.T) {
	tests := []struct {
		name     string
		records  []store.InboundRecord
		lookups  []struct{ hash, numberID string }
		wantAt   []time.Time
		wantSeen []bool
	}{
		{
			name:     "first message opens a session",
			records:  []store.InboundRecord{withSession(record("m1", "evt_1", baseTime), "h1", "111", baseTime)},
			lookups:  []struct{ hash, numberID string }{{"h1", "111"}},
			wantAt:   []time.Time{baseTime},
			wantSeen: []bool{true},
		},
		{
			name: "later message moves the session forward",
			records: []store.InboundRecord{
				withSession(record("m1", "evt_1", baseTime), "h1", "111", baseTime),
				withSession(record("m2", "evt_2", baseTime.Add(time.Hour)), "h1", "111", baseTime.Add(time.Hour)),
			},
			lookups:  []struct{ hash, numberID string }{{"h1", "111"}},
			wantAt:   []time.Time{baseTime.Add(time.Hour)},
			wantSeen: []bool{true},
		},
		{
			name: "an older message arriving late never moves the session back",
			records: []store.InboundRecord{
				withSession(record("m2", "evt_2", baseTime.Add(time.Hour)), "h1", "111", baseTime.Add(time.Hour)),
				withSession(record("m1", "evt_1", baseTime), "h1", "111", baseTime),
			},
			lookups:  []struct{ hash, numberID string }{{"h1", "111"}},
			wantAt:   []time.Time{baseTime.Add(time.Hour)},
			wantSeen: []bool{true},
		},
		{
			name: "sessions are separate per customer and per business number",
			records: []store.InboundRecord{
				withSession(record("m1", "evt_1", baseTime), "h1", "111", baseTime),
				withSession(record("m2", "evt_2", baseTime), "h2", "111", baseTime.Add(time.Minute)),
				withSession(record("m3", "evt_3", baseTime), "h1", "222", baseTime.Add(2*time.Minute)),
			},
			lookups:  []struct{ hash, numberID string }{{"h1", "111"}, {"h2", "111"}, {"h1", "222"}, {"h3", "111"}},
			wantAt:   []time.Time{baseTime, baseTime.Add(time.Minute), baseTime.Add(2 * time.Minute), {}},
			wantSeen: []bool{true, true, true, false},
		},
		{
			name: "a duplicate delivery does not touch the session",
			records: []store.InboundRecord{
				withSession(record("same-key", "evt_1", baseTime), "h1", "111", baseTime),
				withSession(record("same-key", "evt_2", baseTime.Add(30*time.Minute)), "h1", "111", baseTime.Add(30*time.Minute)),
			},
			lookups:  []struct{ hash, numberID string }{{"h1", "111"}},
			wantAt:   []time.Time{baseTime},
			wantSeen: []bool{true},
		},
		{
			name:     "a record without a session leaves the table empty",
			records:  []store.InboundRecord{record("m1", "evt_1", baseTime)},
			lookups:  []struct{ hash, numberID string }{{"h1", "111"}},
			wantAt:   []time.Time{{}},
			wantSeen: []bool{false},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s := openTestStore(t)
			for _, r := range tt.records {
				if _, err := s.SaveInbound(ctx, []store.InboundRecord{r}); err != nil {
					t.Fatal(err)
				}
			}

			for i, lookup := range tt.lookups {
				at, isKnown, err := s.LastInbound(ctx, lookup.hash, lookup.numberID)
				if err != nil {
					t.Fatal(err)
				}
				if isKnown != tt.wantSeen[i] || !at.Equal(tt.wantAt[i]) {
					t.Errorf("LastInbound(%s, %s) = %v, %v, want %v, %v", lookup.hash, lookup.numberID, at, isKnown, tt.wantAt[i], tt.wantSeen[i])
				}
			}
		})
	}
}

func TestDeleteExpiredSessions(t *testing.T) {
	tests := []struct {
		name     string
		before   time.Time
		wantKept []string
	}{
		{name: "cutoff before every session keeps all", before: baseTime.Add(-time.Hour), wantKept: []string{"old", "mid", "new"}},
		{name: "cutoff removes older sessions only", before: baseTime.Add(90 * time.Minute), wantKept: []string{"new"}},
		{name: "a session exactly at the cutoff is removed", before: baseTime.Add(time.Hour), wantKept: []string{"new"}},
		{name: "cutoff after every session removes all", before: baseTime.Add(24 * time.Hour), wantKept: []string{}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			s := openTestStore(t)
			touches := map[string]time.Time{"old": baseTime, "mid": baseTime.Add(time.Hour), "new": baseTime.Add(2 * time.Hour)}
			for name, at := range touches {
				if _, err := s.SaveInbound(ctx, []store.InboundRecord{withSession(record("k-"+name, "evt_"+name, at), name, "111", at)}); err != nil {
					t.Fatal(err)
				}
			}

			if err := s.DeleteExpiredSessions(ctx, tt.before); err != nil {
				t.Fatal(err)
			}

			kept := []string{}
			for name := range touches {
				if _, isKnown, err := s.LastInbound(ctx, name, "111"); err != nil {
					t.Fatal(err)
				} else if isKnown {
					kept = append(kept, name)
				}
			}
			if len(kept) != len(tt.wantKept) {
				t.Fatalf("kept %v, want %v", kept, tt.wantKept)
			}
			for _, name := range tt.wantKept {
				if _, isKnown, _ := s.LastInbound(ctx, name, "111"); !isKnown {
					t.Errorf("session %s was removed, want it kept", name)
				}
			}
		})
	}
}
