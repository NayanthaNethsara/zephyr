package core

import (
	"context"
	"errors"
	"testing"
	"time"
)

type fakeSessionStore struct {
	lastInboundAt time.Time
	isKnown       bool
	err           error
	gotHash       string
	gotNumberID   string
}

func (f *fakeSessionStore) LastInbound(_ context.Context, phoneHash, phoneNumberID string) (time.Time, bool, error) {
	f.gotHash, f.gotNumberID = phoneHash, phoneNumberID
	return f.lastInboundAt, f.isKnown, f.err
}

func (f *fakeSessionStore) DeleteExpiredSessions(context.Context, time.Time) error { return nil }

func TestSessionServiceCheck(t *testing.T) {
	now := time.Date(2026, 11, 1, 12, 0, 0, 0, time.UTC)
	storeDown := errors.New("disk full")

	tests := []struct {
		name          string
		phone         string
		lastInboundAt time.Time
		isKnown       bool
		storeErr      error
		wantState     SessionState
		wantExpiresAt time.Time
		wantCode      string
		wantErr       error
		wantLookup    bool
	}{
		{name: "message a minute ago", phone: "94770000000", lastInboundAt: now.Add(-time.Minute), isKnown: true, wantState: SessionOpen, wantExpiresAt: now.Add(-time.Minute).Add(24 * time.Hour), wantLookup: true},
		{name: "message just inside the window", phone: "94770000000", lastInboundAt: now.Add(-24*time.Hour + time.Second), isKnown: true, wantState: SessionOpen, wantExpiresAt: now.Add(time.Second), wantLookup: true},
		{name: "message exactly 24 hours ago is closed", phone: "94770000000", lastInboundAt: now.Add(-24 * time.Hour), isKnown: true, wantState: SessionClosed, wantExpiresAt: now, wantLookup: true},
		{name: "message two days ago", phone: "94770000000", lastInboundAt: now.Add(-48 * time.Hour), isKnown: true, wantState: SessionClosed, wantExpiresAt: now.Add(-24 * time.Hour), wantLookup: true},
		{name: "customer never seen", phone: "94770000000", isKnown: false, wantState: SessionUnknown, wantLookup: true},
		{name: "phone too short", phone: "123", wantCode: CodeInvalidRequest},
		{name: "phone with a plus sign", phone: "+94770000000", wantCode: CodeInvalidRequest},
		{name: "store failure is returned", phone: "94770000000", storeErr: storeDown, wantErr: storeDown, wantLookup: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			backend := &fakeSessionStore{lastInboundAt: tt.lastInboundAt, isKnown: tt.isKnown, err: tt.storeErr}
			service := NewSessionService(backend, "phone-hash-key-0123456789abcdef", "111")
			service.now = func() time.Time { return now }

			session, err := service.Check(context.Background(), tt.phone)

			if (backend.gotNumberID != "") != tt.wantLookup {
				t.Fatalf("store lookup = %v, want %v", backend.gotNumberID != "", tt.wantLookup)
			}
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Fatalf("error = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if tt.wantCode != "" {
				var requestErr *RequestError
				if !errors.As(err, &requestErr) || requestErr.Code != tt.wantCode {
					t.Fatalf("error = %#v, want code %s", err, tt.wantCode)
				}
				return
			}
			if err != nil || session.State != tt.wantState || !session.ExpiresAt.Equal(tt.wantExpiresAt) || !session.LastInboundAt.Equal(tt.lastInboundAt) {
				t.Fatalf("Check() = %+v, %v, want state %s expiring %v", session, err, tt.wantState, tt.wantExpiresAt)
			}
			if backend.gotNumberID != "111" {
				t.Errorf("looked up number %q, want 111", backend.gotNumberID)
			}
		})
	}
}

func TestSessionsAreKeyedByAHashOfThePhone(t *testing.T) {
	tests := []struct {
		name      string
		phoneA    string
		keyA      string
		phoneB    string
		keyB      string
		wantEqual bool
	}{
		{name: "same phone and key", phoneA: "94770000000", keyA: "key-one-0123456789abcdef", phoneB: "94770000000", keyB: "key-one-0123456789abcdef", wantEqual: true},
		{name: "different phones", phoneA: "94770000000", keyA: "key-one-0123456789abcdef", phoneB: "94770000001", keyB: "key-one-0123456789abcdef"},
		{name: "different keys", phoneA: "94770000000", keyA: "key-one-0123456789abcdef", phoneB: "94770000000", keyB: "key-two-0123456789abcdef"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			first := hashPhone([]byte(tt.keyA), tt.phoneA)
			second := hashPhone([]byte(tt.keyB), tt.phoneB)

			if (first == second) != tt.wantEqual {
				t.Fatalf("hashes equal = %v, want %v", first == second, tt.wantEqual)
			}
			if first == tt.phoneA || len(first) != 64 {
				t.Errorf("hash %q must not contain the phone and must be 64 hex characters", first)
			}
		})
	}
}
