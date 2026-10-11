package httpapi

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/geshas/mailroutine/internal/accounts"
	"github.com/geshas/mailroutine/internal/oauth"
	"github.com/geshas/mailroutine/internal/runner"
	"github.com/geshas/mailroutine/internal/security"
	"github.com/geshas/mailroutine/internal/sqlite"
	"github.com/geshas/mailroutine/pkg/engine"
	"github.com/geshas/mailroutine/pkg/gmail"
	"github.com/geshas/mailroutine/pkg/models"
)

const (
	oauthStateCookie    = "mailroutine_oauth_state"
	oauthVerifierCookie = "mailroutine_oauth_verifier"
	oauthCookieTTL      = 10 * time.Minute
)

func (s *Server) handleListAccounts(w http.ResponseWriter, r *http.Request, _ sqlite.Session) {
	list, err := s.Store.ListAccounts(r.Context())
	if err != nil {
		s.internalError(w, err, "listing accounts failed")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": list})
}

func (s *Server) oauthCookie(w http.ResponseWriter, name, value string, ttl time.Duration) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: value, Path: "/api/accounts/google", MaxAge: int(ttl.Seconds()),
		HttpOnly: true, Secure: s.Config.SecureCookies, SameSite: http.SameSiteLaxMode,
	})
}

// handleGoogleConnect starts the OAuth flow. It is a browser navigation (GET),
// so the state and PKCE verifier travel in short-lived HttpOnly cookies.
func (s *Server) handleGoogleConnect(w http.ResponseWriter, r *http.Request, _ sqlite.Session) {
	state, err := security.RandomToken(32)
	if err != nil {
		s.internalError(w, err, "generating oauth state failed")
		return
	}
	verifier := oauth.NewVerifier()
	s.oauthCookie(w, oauthStateCookie, state, oauthCookieTTL)
	s.oauthCookie(w, oauthVerifierCookie, verifier, oauthCookieTTL)
	authURL := s.Google.AuthURL(state, verifier)
	// Single-page clients ask for JSON so they can navigate themselves; the
	// state/PKCE cookies above are set either way.
	if strings.Contains(r.Header.Get("Accept"), "application/json") {
		writeJSON(w, http.StatusOK, map[string]string{"url": authURL})
		return
	}
	http.Redirect(w, r, authURL, http.StatusFound)
}

func (s *Server) redirectSettings(w http.ResponseWriter, r *http.Request, key, val string) {
	http.Redirect(w, r, "/settings?"+url.Values{key: {val}}.Encode(), http.StatusFound)
}

func (s *Server) handleGoogleCallback(w http.ResponseWriter, r *http.Request) {
	if _, ok := s.sessionFromCookie(r); !ok {
		http.Redirect(w, r, "/", http.StatusFound)
		return
	}
	stateC, _ := r.Cookie(oauthStateCookie)
	verC, _ := r.Cookie(oauthVerifierCookie)
	s.oauthCookie(w, oauthStateCookie, "", -time.Second)
	s.oauthCookie(w, oauthVerifierCookie, "", -time.Second)

	q := r.URL.Query()
	if e := q.Get("error"); e != "" {
		s.log.Info("google authorization was not granted", "reason", e)
		s.redirectSettings(w, r, "oauth_error", sanitizeCode(e))
		return
	}
	if stateC == nil || verC == nil || q.Get("state") == "" || !security.ConstantTimeEqual(stateC.Value, q.Get("state")) {
		s.log.Warn("oauth callback rejected: state mismatch")
		s.redirectSettings(w, r, "oauth_error", "state_mismatch")
		return
	}
	tok, err := s.Google.Exchange(r.Context(), q.Get("code"), verC.Value)
	if err != nil {
		s.log.Warn("oauth token exchange failed", "error", err)
		code := "exchange_failed"
		switch {
		case errors.Is(err, oauth.ErrNoRefreshToken):
			code = "no_refresh_token"
		case errors.Is(err, oauth.ErrInsufficientScope):
			code = "insufficient_scope"
		}
		s.redirectSettings(w, r, "oauth_error", code)
		return
	}
	acc, created, err := s.Accounts.Connect(r.Context(), tok)
	if err != nil {
		s.log.Warn("connecting account failed", "error", err)
		s.redirectSettings(w, r, "oauth_error", "profile_failed")
		return
	}
	s.log.Info("gmail account connected", "account_id", acc.ID, "new", created)
	s.redirectSettings(w, r, "connected", "1")
}

// sessionFromCookie validates the session cookie without writing a response.
func (s *Server) sessionFromCookie(r *http.Request) (sqlite.Session, bool) {
	sess, err := s.Auth.Validate(r.Context(), rawCookie(r))
	return sess, err == nil
}

// sanitizeCode keeps only a short alphanumeric/underscore error code from Google.
func sanitizeCode(s string) string {
	out := make([]byte, 0, len(s))
	for i := 0; i < len(s) && i < 40; i++ {
		c := s[i]
		if c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '_' {
			out = append(out, c)
		}
	}
	if len(out) == 0 {
		return "unknown"
	}
	return string(out)
}

func (s *Server) handleDeleteAccount(w http.ResponseWriter, r *http.Request, _ sqlite.Session) {
	id := r.PathValue("id")
	routines, err := s.Store.ListRoutines(r.Context(), id)
	if err != nil {
		s.internalError(w, err, "listing routines failed")
		return
	}
	for _, rt := range routines {
		if busy, _ := s.Store.IsClaimed(r.Context(), rt.ID, time.Now().Add(-runner.LeaseStaleAfter)); busy {
			writeError(w, http.StatusConflict, "routine_running", "a routine for this account is running; try again when it finishes", nil)
			return
		}
	}
	if err := s.Accounts.Disconnect(r.Context(), id); err != nil {
		s.storeError(w, err, "account")
		return
	}
	s.log.Info("gmail account disconnected", "account_id", id)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleAccountLabels(w http.ResponseWriter, r *http.Request, _ sqlite.Session) {
	client, _, err := s.Accounts.ProviderByID(r.Context(), r.PathValue("id"))
	if err != nil {
		s.providerError(w, err)
		return
	}
	labels, err := client.Labels(r.Context())
	if err != nil {
		s.providerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"items": labels})
}

type traceLabel struct {
	Name   string            `json:"name"`
	Action models.ActionType `json:"action,omitempty"`
	Exists bool              `json:"exists"`
}

type traceLabelReport struct {
	Items []traceLabel `json:"items"`
	// Created lists the labels this request created (empty for a status check).
	Created []string `json:"created"`
}

// traceLabelReport compares the account's Gmail labels with the MailRoutine ones.
func traceReport(labels []gmail.Label, created []string) traceLabelReport {
	have := make(map[string]bool, len(labels))
	for _, l := range labels {
		have[strings.ToLower(l.Name)] = true
	}
	rep := traceLabelReport{Items: []traceLabel{{Name: models.TraceParentLabel, Exists: have[strings.ToLower(models.TraceParentLabel)]}}, Created: []string{}}
	for _, a := range models.ActionTypes {
		n := models.TraceLabel(a)
		rep.Items = append(rep.Items, traceLabel{Name: n, Action: a, Exists: have[strings.ToLower(n)]})
	}
	if created != nil {
		rep.Created = created
	}
	return rep
}

func (s *Server) handleTraceLabels(w http.ResponseWriter, r *http.Request, _ sqlite.Session) {
	client, _, err := s.Accounts.ProviderByID(r.Context(), r.PathValue("id"))
	if err != nil {
		s.providerError(w, err)
		return
	}
	labels, err := client.Labels(r.Context())
	if err != nil {
		s.providerError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, traceReport(labels, nil))
}

// handleCreateTraceLabels creates whichever MailRoutine labels are missing (never duplicating
// or touching existing ones), so it is safe to call repeatedly.
func (s *Server) handleCreateTraceLabels(w http.ResponseWriter, r *http.Request, _ sqlite.Session) {
	client, acc, err := s.Accounts.ProviderByID(r.Context(), r.PathValue("id"))
	if err != nil {
		s.providerError(w, err)
		return
	}
	names := append([]string{models.TraceParentLabel}, models.TraceLabels()...)
	created, err := client.EnsureLabels(r.Context(), names)
	if err != nil {
		s.providerError(w, err)
		return
	}
	labels, err := client.Labels(r.Context())
	if err != nil {
		s.providerError(w, err)
		return
	}
	s.log.Info("mailroutine labels ensured", "account_id", acc.ID, "created", len(created))
	writeJSON(w, http.StatusOK, traceReport(labels, created))
}

// providerError maps mail-provider failures to API responses.
func (s *Server) providerError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, sqlite.ErrNotFound):
		writeError(w, http.StatusNotFound, "not_found", "account not found", nil)
	case errors.Is(err, accounts.ErrNeedsReauth), errors.Is(err, runner.ErrAccountUnavailable):
		writeError(w, http.StatusConflict, "account_needs_reauth", "this Gmail account must be reconnected in Settings", nil)
	case errors.Is(err, engine.ErrAuthRevoked):
		writeError(w, http.StatusConflict, "account_needs_reauth", "Google no longer accepts this account's authorization; reconnect it in Settings", nil)
	case errors.Is(err, engine.ErrInvalidQuery):
		writeError(w, http.StatusBadRequest, "invalid_query", "Gmail rejected this search query", nil)
	case engine.IsRetryable(err):
		writeError(w, http.StatusServiceUnavailable, "gmail_unavailable", "Gmail is rate limiting or temporarily unavailable; try again shortly", nil)
	default:
		s.log.Warn("gmail request failed", "error", err)
		writeError(w, http.StatusBadGateway, "gmail_error", "Gmail request failed; see server logs", nil)
	}
}
