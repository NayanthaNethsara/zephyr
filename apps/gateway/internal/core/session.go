package core

import (
	"context"
	"time"

	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/crypto"
	"github.com/NayanthaNethsara/zephyr/apps/gateway/internal/store"
)

const sessionWindow = 24 * time.Hour

type SessionState string

const (
	SessionOpen    SessionState = "open"
	SessionClosed  SessionState = "closed"
	SessionUnknown SessionState = "unknown"
)

type Session struct {
	State         SessionState
	LastInboundAt time.Time
	ExpiresAt     time.Time
}

type SessionService struct {
	store         store.SessionStore
	hashKey       []byte
	phoneNumberID string
	now           func() time.Time
}

func NewSessionService(sessions store.SessionStore, hashKey, phoneNumberID string) *SessionService {
	return &SessionService{
		store:         sessions,
		hashKey:       []byte(hashKey),
		phoneNumberID: phoneNumberID,
		now:           time.Now,
	}
}

func (s *SessionService) Check(ctx context.Context, phone string) (Session, error) {
	if !isValidRecipient(phone) {
		return Session{}, invalidRequest("phone must be digits only, 7 to 15 characters, without a plus sign")
	}
	lastInboundAt, isKnown, err := s.store.LastInbound(ctx, hashPhone(s.hashKey, phone), s.phoneNumberID)
	if err != nil {
		return Session{}, err
	}
	if !isKnown {
		return Session{State: SessionUnknown}, nil
	}

	session := Session{LastInboundAt: lastInboundAt, ExpiresAt: lastInboundAt.Add(sessionWindow)}
	if s.now().Before(session.ExpiresAt) {
		session.State = SessionOpen
	} else {
		session.State = SessionClosed
	}
	return session, nil
}

func hashPhone(key []byte, phone string) string {
	return crypto.SignHMACSHA256(key, []byte(phone))
}
