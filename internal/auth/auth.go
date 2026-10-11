// Package auth implements single-administrator authentication, persistent
// sliding server-side sessions and login throttling.
package auth

import (
	"context"
	"crypto/subtle"
	"errors"
	"time"

	"github.com/geshas/mailroutine/internal/security"
	"github.com/geshas/mailroutine/internal/sqlite"
)

// Errors returned by session validation.
var (
	ErrUnauthenticated = errors.New("not authenticated")
	ErrExpired         = errors.New("session expired")
)

// SessionStore is the persistence the auth service needs.
type SessionStore interface {
	CreateSession(ctx context.Context, s sqlite.Session) error
	SessionByHash(ctx context.Context, hash string) (sqlite.Session, error)
	ExtendSession(ctx context.Context, id string, now, newExpiry, lastSeen time.Time) (bool, error)
	DeleteSession(ctx context.Context, id string) error
	DeleteSessionsExcept(ctx context.Context, keepFingerprint string) (int64, error)
	PruneSessions(ctx context.Context, now time.Time) (int64, error)
}

// Settings configures the service. Username and PasswordHash come from the
// environment; they are never written to the database.
type Settings struct {
	Username        string
	PasswordHash    string // Argon2id PHC
	Secret          []byte // SESSION_SECRET: keys token hashing, fingerprints and CSRF tokens
	IdleTimeout     time.Duration
	RenewThreshold  time.Duration
	AbsoluteTimeout time.Duration // 0 disables
}

// Service performs authentication and session management.
type Service struct {
	st          SessionStore
	cfg         Settings
	fingerprint string
	Now         func() time.Time
	// hashSlots bounds concurrent Argon2 verifications: each needs 64 MiB, so an
	// unbounded flood of login attempts could otherwise exhaust memory.
	hashSlots chan struct{}
}

const maxConcurrentHashes = 4

// New creates the service.
func New(st SessionStore, cfg Settings) *Service {
	return &Service{
		st: st, cfg: cfg, Now: time.Now, hashSlots: make(chan struct{}, maxConcurrentHashes),
		// A keyed digest of the credentials: changes whenever the username,
		// password hash or secret changes, without storing the hash itself.
		fingerprint: security.HMACHex(cfg.Secret, "credentials", cfg.Username, cfg.PasswordHash),
	}
}

// Config exposes timing settings (read-only copy) to the HTTP layer.
func (s *Service) Config() Settings { return s.cfg }

// InvalidateStaleSessions removes sessions created under different credentials.
// Call once at startup so a changed ADMIN_PASSWORD_HASH logs everyone out.
func (s *Service) InvalidateStaleSessions(ctx context.Context) (int64, error) {
	return s.st.DeleteSessionsExcept(ctx, s.fingerprint)
}

// CheckCredentials verifies the login in constant time with respect to which
// part (username or password) was wrong. The Argon2 work is always performed.
func (s *Service) CheckCredentials(username, password string) bool {
	s.hashSlots <- struct{}{}
	defer func() { <-s.hashSlots }()
	userOK := subtle.ConstantTimeCompare([]byte(username), []byte(s.cfg.Username)) == 1
	passOK, err := security.VerifyPassword(password, s.cfg.PasswordHash)
	return userOK && passOK && err == nil
}

func (s *Service) tokenHash(token string) string {
	return security.HMACHex(s.cfg.Secret, "session-token", token)
}

// CSRFToken derives the per-session CSRF token. It is not secret from the
// logged-in user but cannot be guessed by a cross-site attacker.
func (s *Service) CSRFToken(sess sqlite.Session) string {
	return security.HMACHex(s.cfg.Secret, "csrf", sess.ID)
}

// CreateSession starts a new session (always a fresh token, so login rotates
// any previous one) and returns the raw token for the cookie.
func (s *Service) CreateSession(ctx context.Context) (token string, sess sqlite.Session, err error) {
	token, err = security.RandomToken(32)
	if err != nil {
		return "", sess, err
	}
	now := s.Now().UTC()
	sess = sqlite.Session{
		ID: security.RandomID(), TokenHash: s.tokenHash(token),
		CreatedAt: now, LastActivityAt: now, ExpiresAt: now.Add(s.cfg.IdleTimeout),
		CredentialFingerprint: s.fingerprint,
	}
	if s.cfg.AbsoluteTimeout > 0 {
		abs := now.Add(s.cfg.AbsoluteTimeout)
		sess.AbsoluteExpiresAt = &abs
		if abs.Before(sess.ExpiresAt) {
			sess.ExpiresAt = abs
		}
	}
	return token, sess, s.st.CreateSession(ctx, sess)
}

// Validate resolves a cookie token to a live session without extending it.
func (s *Service) Validate(ctx context.Context, token string) (sqlite.Session, error) {
	if token == "" {
		return sqlite.Session{}, ErrUnauthenticated
	}
	sess, err := s.st.SessionByHash(ctx, s.tokenHash(token))
	if errors.Is(err, sqlite.ErrNotFound) {
		return sess, ErrUnauthenticated
	}
	if err != nil {
		return sess, err
	}
	if !security.ConstantTimeEqual(sess.CredentialFingerprint, s.fingerprint) {
		_ = s.st.DeleteSession(ctx, sess.ID)
		return sqlite.Session{}, ErrUnauthenticated
	}
	now := s.Now().UTC()
	if !now.Before(sess.ExpiresAt) || (sess.AbsoluteExpiresAt != nil && !now.Before(*sess.AbsoluteExpiresAt)) {
		_ = s.st.DeleteSession(ctx, sess.ID)
		return sqlite.Session{}, ErrExpired
	}
	return sess, nil
}

// Renew slides the inactivity window forward in response to genuine user
// activity. To limit writes it only acts when RenewThreshold has elapsed since
// the last extension; renewed reports whether the expiry actually moved.
func (s *Service) Renew(ctx context.Context, sess sqlite.Session) (out sqlite.Session, renewed bool, err error) {
	now := s.Now().UTC()
	if now.Sub(sess.LastActivityAt) < s.cfg.RenewThreshold {
		return sess, false, nil
	}
	newExp := now.Add(s.cfg.IdleTimeout)
	if sess.AbsoluteExpiresAt != nil && newExp.After(*sess.AbsoluteExpiresAt) {
		newExp = *sess.AbsoluteExpiresAt
	}
	ok, err := s.st.ExtendSession(ctx, sess.ID, now, newExp, sess.LastActivityAt)
	if err != nil {
		return sess, false, err
	}
	if ok {
		sess.LastActivityAt, sess.ExpiresAt = now, newExp
		return sess, true, nil
	}
	// Lost a race with a concurrent renewal (or the session just expired): re-read.
	cur, err := s.st.SessionByHash(ctx, sess.TokenHash)
	if errors.Is(err, sqlite.ErrNotFound) || (err == nil && !now.Before(cur.ExpiresAt)) {
		return sess, false, ErrExpired
	}
	return cur, false, err
}

// Destroy deletes a session (logout).
func (s *Service) Destroy(ctx context.Context, sess sqlite.Session) error {
	return s.st.DeleteSession(ctx, sess.ID)
}

// Prune removes expired sessions.
func (s *Service) Prune(ctx context.Context) (int64, error) {
	return s.st.PruneSessions(ctx, s.Now().UTC())
}
