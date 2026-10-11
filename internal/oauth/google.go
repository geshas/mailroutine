// Package oauth implements the Google OAuth 2.0 authorization-code flow (with
// PKCE) used to connect Gmail accounts.
package oauth

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/oauth2"
	"golang.org/x/oauth2/google"

	"github.com/geshas/mailroutine/pkg/gmail"
)

// Google wraps the OAuth client configuration.
type Google struct {
	cfg *oauth2.Config
	// RevokeURL is overridable for tests.
	RevokeURL  string
	HTTPClient *http.Client
}

// New builds the OAuth client. Only the Gmail modify scope is requested: it
// allows reading metadata, changing labels and moving mail to Trash, but not
// permanent deletion or sending mail.
func New(clientID, clientSecret, redirectURL string) *Google {
	return &Google{
		cfg: &oauth2.Config{
			ClientID: clientID, ClientSecret: clientSecret, RedirectURL: redirectURL,
			Endpoint: google.Endpoint, Scopes: []string{gmail.Scope},
		},
		RevokeURL:  "https://oauth2.googleapis.com/revoke",
		HTTPClient: &http.Client{Timeout: 15 * time.Second},
	}
}

// SetEndpoint overrides the OAuth endpoints (tests).
func (g *Google) SetEndpoint(e oauth2.Endpoint) { g.cfg.Endpoint = e }

// RedirectURL returns the configured callback URL.
func (g *Google) RedirectURL() string { return g.cfg.RedirectURL }

// NewVerifier returns a PKCE code verifier.
func NewVerifier() string { return oauth2.GenerateVerifier() }

// AuthURL returns the Google consent URL. access_type=offline with
// prompt=consent makes Google issue a refresh token on every (re)connect.
func (g *Google) AuthURL(state, verifier string) string {
	return g.cfg.AuthCodeURL(state,
		oauth2.AccessTypeOffline,
		oauth2.SetAuthURLParam("prompt", "consent"),
		oauth2.S256ChallengeOption(verifier))
}

// ErrNoRefreshToken means Google did not return a refresh token.
var ErrNoRefreshToken = errors.New("google did not return a refresh token; remove MailRoutine at https://myaccount.google.com/permissions and connect again")

// ErrInsufficientScope means the person did not grant the Gmail permission MailRoutine needs
// (Google lets users untick individual permissions on the consent screen).
var ErrInsufficientScope = errors.New("google did not grant the Gmail permission MailRoutine needs; connect again and keep every requested permission ticked")

// HasRequiredScope reports whether a token response's space-separated scope list contains gmail.modify.
func HasRequiredScope(granted string) bool {
	for _, s := range strings.Fields(granted) {
		if s == gmail.Scope {
			return true
		}
	}
	return false
}

// Exchange trades the authorization code for tokens. It verifies that the granted scopes
// include everything MailRoutine needs, so a partial grant is rejected at connection time
// instead of failing later during a run.
func (g *Google) Exchange(ctx context.Context, code, verifier string) (*oauth2.Token, error) {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, g.HTTPClient)
	tok, err := g.cfg.Exchange(ctx, code, oauth2.VerifierOption(verifier))
	if err != nil {
		var re *oauth2.RetrieveError
		if errors.As(err, &re) {
			// Never include the response body: it can echo request parameters.
			return nil, fmt.Errorf("token exchange rejected by google: %s", re.ErrorCode)
		}
		return nil, fmt.Errorf("token exchange failed: %w", err)
	}
	granted, _ := tok.Extra("scope").(string)
	if !HasRequiredScope(granted) {
		return nil, ErrInsufficientScope
	}
	if tok.RefreshToken == "" {
		return nil, ErrNoRefreshToken
	}
	return tok, nil
}

// TokenSource returns an auto-refreshing source for a stored refresh token.
func (g *Google) TokenSource(ctx context.Context, refreshToken string) oauth2.TokenSource {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, g.HTTPClient)
	return g.cfg.TokenSource(ctx, &oauth2.Token{RefreshToken: refreshToken})
}

// StaticTokenSource wraps a freshly exchanged token (used to look up the address).
func (g *Google) StaticTokenSource(ctx context.Context, tok *oauth2.Token) oauth2.TokenSource {
	ctx = context.WithValue(ctx, oauth2.HTTPClient, g.HTTPClient)
	return g.cfg.TokenSource(ctx, tok)
}

// Revoke asks Google to invalidate a token. Failure is not fatal for callers.
func (g *Google) Revoke(ctx context.Context, token string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.RevokeURL, strings.NewReader(url.Values{"token": {token}}.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := g.HTTPClient.Do(req)
	if err != nil {
		return fmt.Errorf("revoke request failed: %w", errors.Unwrap(err))
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("google revoke returned HTTP %d", resp.StatusCode)
	}
	return nil
}
