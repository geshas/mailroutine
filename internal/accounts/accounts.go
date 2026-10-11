// Package accounts connects Gmail accounts and builds authorized mail providers
// from encrypted refresh tokens.
package accounts

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/option"

	"github.com/geshas/mailroutine/internal/oauth"
	"github.com/geshas/mailroutine/internal/security"
	"github.com/geshas/mailroutine/internal/sqlite"
	"github.com/geshas/mailroutine/pkg/gmail"
	"github.com/geshas/mailroutine/pkg/models"
)

// ErrNeedsReauth means the account must be reconnected before it can be used.
var ErrNeedsReauth = errors.New("gmail account needs to be reconnected")

// Manager owns account lifecycle operations.
type Manager struct {
	Store   *sqlite.Store
	Google  *oauth.Google
	Key     []byte
	Timeout time.Duration
	Logger  *slog.Logger
	// GmailOptions are extra Gmail client options (tests point these at a fake server).
	GmailOptions []option.ClientOption
}

// Provider returns an authorized Gmail client for the account.
func (m *Manager) Provider(ctx context.Context, acc models.Account) (*gmail.Client, error) {
	if acc.ConnectionStatus != models.StatusConnected {
		return nil, ErrNeedsReauth
	}
	refresh, err := security.Decrypt(m.Key, acc.EncryptedRefreshToken)
	if err != nil {
		return nil, fmt.Errorf("account %s: %w", acc.Email, err)
	}
	return gmail.NewFromTokenSource(ctx, m.Google.TokenSource(ctx, refresh), m.Timeout, m.GmailOptions...)
}

// ProviderByID loads the account and returns its provider.
func (m *Manager) ProviderByID(ctx context.Context, id string) (*gmail.Client, models.Account, error) {
	acc, err := m.Store.GetAccount(ctx, id)
	if err != nil {
		return nil, acc, err
	}
	p, err := m.Provider(ctx, acc)
	return p, acc, err
}

// Connect completes an OAuth flow: it looks up the mailbox address with the
// new token and stores the encrypted refresh token (creating or reconnecting).
func (m *Manager) Connect(ctx context.Context, tok *oauth2.Token) (models.Account, bool, error) {
	client, err := gmail.NewFromTokenSource(ctx, m.Google.StaticTokenSource(ctx, tok), m.Timeout, m.GmailOptions...)
	if err != nil {
		return models.Account{}, false, err
	}
	email, err := client.EmailAddress(ctx)
	if err != nil {
		return models.Account{}, false, fmt.Errorf("could not read mailbox address: %w", err)
	}
	enc, err := security.Encrypt(m.Key, tok.RefreshToken)
	if err != nil {
		return models.Account{}, false, err
	}
	return m.Store.UpsertAccount(ctx, email, enc)
}

// Disconnect revokes the token at Google (best effort) and deletes the account
// together with its routines and history.
func (m *Manager) Disconnect(ctx context.Context, id string) error {
	acc, err := m.Store.GetAccount(ctx, id)
	if err != nil {
		return err
	}
	if refresh, derr := security.Decrypt(m.Key, acc.EncryptedRefreshToken); derr == nil {
		if rerr := m.Google.Revoke(ctx, refresh); rerr != nil && m.Logger != nil {
			m.Logger.Warn("token revocation failed; remove access manually at myaccount.google.com/permissions",
				"component", "accounts", "account_id", acc.ID, "error", rerr)
		}
	}
	return m.Store.DeleteAccount(ctx, id)
}

// MarkNeedsReauth flags an account whose authorization was revoked or expired.
func (m *Manager) MarkNeedsReauth(ctx context.Context, id string) error {
	return m.Store.SetAccountStatus(ctx, id, models.StatusNeedsReauth)
}
