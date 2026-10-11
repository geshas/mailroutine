package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// Session is a stored login session. TokenHash is a keyed hash; the raw token
// exists only in the user's cookie.
type Session struct {
	ID                    string
	TokenHash             string
	CreatedAt             time.Time
	LastActivityAt        time.Time
	ExpiresAt             time.Time
	AbsoluteExpiresAt     *time.Time
	CredentialFingerprint string
}

// CreateSession inserts a session.
func (s *Store) CreateSession(ctx context.Context, x Session) error {
	_, err := s.db.ExecContext(ctx, `INSERT INTO sessions (id, token_hash, created_at, last_activity_at, expires_at, absolute_expires_at, credential_fingerprint)
		VALUES (?,?,?,?,?,?,?)`, x.ID, x.TokenHash, ms(x.CreatedAt), ms(x.LastActivityAt), ms(x.ExpiresAt), msPtr(x.AbsoluteExpiresAt), x.CredentialFingerprint)
	return err
}

// SessionByHash looks up a session by token hash.
func (s *Store) SessionByHash(ctx context.Context, hash string) (Session, error) {
	var x Session
	var created, last, exp int64
	var abs sql.NullInt64
	err := s.db.QueryRowContext(ctx, `SELECT id, token_hash, created_at, last_activity_at, expires_at, absolute_expires_at, credential_fingerprint
		FROM sessions WHERE token_hash = ?`, hash).Scan(&x.ID, &x.TokenHash, &created, &last, &exp, &abs, &x.CredentialFingerprint)
	if errors.Is(err, sql.ErrNoRows) {
		return x, ErrNotFound
	}
	x.CreatedAt, x.LastActivityAt, x.ExpiresAt, x.AbsoluteExpiresAt = fromMs(created), fromMs(last), fromMs(exp), ptrFromNull(abs)
	return x, err
}

// ExtendSession slides the expiry forward. The UPDATE only applies if the
// session is still live and has not been extended since lastSeen, so concurrent
// renewals cannot double-write or resurrect an expired session.
func (s *Store) ExtendSession(ctx context.Context, id string, now, newExpiry, lastSeen time.Time) (bool, error) {
	res, err := s.db.ExecContext(ctx, `UPDATE sessions SET last_activity_at = ?, expires_at = ?
		WHERE id = ? AND expires_at > ? AND last_activity_at = ?`, ms(now), ms(newExpiry), id, ms(now), ms(lastSeen))
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}

// DeleteSession removes a session by id.
func (s *Store) DeleteSession(ctx context.Context, id string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE id = ?`, id)
	return err
}

// DeleteSessionsExcept removes every session whose credential fingerprint
// differs from keep.
func (s *Store) DeleteSessionsExcept(ctx context.Context, keepFingerprint string) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE credential_fingerprint <> ?`, keepFingerprint)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// PruneSessions deletes expired sessions.
func (s *Store) PruneSessions(ctx context.Context, now time.Time) (int64, error) {
	res, err := s.db.ExecContext(ctx, `DELETE FROM sessions WHERE expires_at <= ? OR (absolute_expires_at IS NOT NULL AND absolute_expires_at <= ?)`,
		ms(now), ms(now))
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// CountSessions returns the number of stored sessions.
func (s *Store) CountSessions(ctx context.Context) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM sessions`).Scan(&n)
	return n, err
}
