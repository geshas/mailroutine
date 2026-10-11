package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"time"

	"github.com/geshas/mailroutine/internal/security"
	"github.com/geshas/mailroutine/pkg/models"
)

const accountCols = `id, provider, email, encrypted_refresh_token, connection_status, created_at, updated_at`

func scanAccount(r interface{ Scan(...any) error }) (models.Account, error) {
	var a models.Account
	var created, updated int64
	err := r.Scan(&a.ID, &a.Provider, &a.Email, &a.EncryptedRefreshToken, &a.ConnectionStatus, &created, &updated)
	a.CreatedAt, a.UpdatedAt = fromMs(created), fromMs(updated)
	return a, err
}

// UpsertAccount stores a connected mailbox. Reconnecting an existing address
// replaces its token and clears needs_reauth. created reports a new row.
func (s *Store) UpsertAccount(ctx context.Context, email, encryptedToken string) (acc models.Account, created bool, err error) {
	now := time.Now()
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		row := tx.QueryRowContext(ctx, `SELECT `+accountCols+` FROM accounts WHERE provider = ? AND email = ?`, models.ProviderGmail, email)
		existing, serr := scanAccount(row)
		switch {
		case serr == nil:
			_, uerr := tx.ExecContext(ctx, `UPDATE accounts SET encrypted_refresh_token = ?, connection_status = 'connected', updated_at = ? WHERE id = ?`,
				encryptedToken, ms(now), existing.ID)
			existing.EncryptedRefreshToken, existing.ConnectionStatus, existing.UpdatedAt = encryptedToken, models.StatusConnected, now.UTC()
			acc = existing
			return uerr
		case errors.Is(serr, sql.ErrNoRows):
			acc = models.Account{ID: security.RandomID(), Provider: models.ProviderGmail, Email: email,
				EncryptedRefreshToken: encryptedToken, ConnectionStatus: models.StatusConnected, CreatedAt: now.UTC(), UpdatedAt: now.UTC()}
			created = true
			_, ierr := tx.ExecContext(ctx, `INSERT INTO accounts (`+accountCols+`) VALUES (?, ?, ?, ?, ?, ?, ?)`,
				acc.ID, acc.Provider, acc.Email, acc.EncryptedRefreshToken, acc.ConnectionStatus, ms(now), ms(now))
			return ierr
		default:
			return serr
		}
	})
	return acc, created, err
}

// GetAccount returns one account.
func (s *Store) GetAccount(ctx context.Context, id string) (models.Account, error) {
	a, err := scanAccount(s.db.QueryRowContext(ctx, `SELECT `+accountCols+` FROM accounts WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return a, ErrNotFound
	}
	return a, err
}

// ListAccounts returns all accounts ordered by email.
func (s *Store) ListAccounts(ctx context.Context) ([]models.Account, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+accountCols+` FROM accounts ORDER BY email`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []models.Account{}
	for rows.Next() {
		a, err := scanAccount(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// SetAccountStatus updates an account's connection status.
func (s *Store) SetAccountStatus(ctx context.Context, id string, st models.ConnectionStatus) error {
	res, err := s.db.ExecContext(ctx, `UPDATE accounts SET connection_status = ?, updated_at = ? WHERE id = ?`, st, ms(time.Now()), id)
	return affected(res, err)
}

// DeleteAccount removes an account and, by cascade, its routines and history.
func (s *Store) DeleteAccount(ctx context.Context, id string) error {
	res, err := s.db.ExecContext(ctx, `DELETE FROM accounts WHERE id = ?`, id)
	return affected(res, err)
}

func affected(res sql.Result, err error) error {
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}
