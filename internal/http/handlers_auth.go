package httpapi

import (
	"fmt"
	"math"
	"net/http"
	"strings"
	"time"

	"github.com/geshas/mailroutine/internal/sqlite"
)

type sessionInfo struct {
	Authenticated           bool      `json:"authenticated"`
	Username                string    `json:"username"`
	ExpiresAt               time.Time `json:"expires_at"`
	IdleTimeoutSeconds      int       `json:"idle_timeout_seconds"`
	RenewalThresholdSeconds int       `json:"renewal_threshold_seconds"`
	CSRFToken               string    `json:"csrf_token"`
}

func (s *Server) sessionInfo(sess sqlite.Session) sessionInfo {
	cfg := s.Auth.Config()
	return sessionInfo{
		Authenticated: true, Username: cfg.Username, ExpiresAt: sess.ExpiresAt,
		IdleTimeoutSeconds:      int(cfg.IdleTimeout.Seconds()),
		RenewalThresholdSeconds: int(cfg.RenewThreshold.Seconds()),
		CSRFToken:               s.Auth.CSRFToken(sess),
	}
}

func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if !s.sameOrigin(r) || !strings.HasPrefix(r.Header.Get("Content-Type"), "application/json") {
		writeError(w, http.StatusForbidden, "csrf_origin",
			"request rejected: open MailRoutine at its configured address ("+s.Config.AppURL.String()+") and sign in from there", nil)
		return
	}
	ip := s.clientIP(r)
	if ok, wait := s.Limiter.Allow(ip); !ok {
		secs := int(math.Ceil(wait.Seconds()))
		w.Header().Set("Retry-After", fmt.Sprint(secs))
		s.log.Warn("login blocked by rate limit", "client_ip", ip)
		writeError(w, http.StatusTooManyRequests, "too_many_attempts",
			fmt.Sprintf("too many failed sign-in attempts; try again in %d seconds", secs), nil)
		return
	}
	var body struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if !decode(w, r, &body) {
		return
	}
	if !s.Auth.CheckCredentials(body.Username, body.Password) {
		s.Limiter.Fail(ip)
		s.log.Warn("login failed", "client_ip", ip)
		writeError(w, http.StatusUnauthorized, "invalid_credentials", "invalid username or password", nil)
		return
	}
	s.Limiter.Succeed(ip)
	// Always mint a fresh token (session rotation); drop any session presented with the request.
	if old := rawCookie(r); old != "" {
		if prev, err := s.Auth.Validate(r.Context(), old); err == nil {
			_ = s.Auth.Destroy(r.Context(), prev)
		}
	}
	token, sess, err := s.Auth.CreateSession(r.Context())
	if err != nil {
		s.internalError(w, err, "creating session failed")
		return
	}
	s.setSessionCookie(w, token, sess)
	s.log.Info("login succeeded", "client_ip", ip)
	writeJSON(w, http.StatusOK, s.sessionInfo(sess))
}

func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request, sess sqlite.Session) {
	if err := s.Auth.Destroy(r.Context(), sess); err != nil {
		s.internalError(w, err, "destroying session failed")
		return
	}
	s.clearSessionCookie(w)
	w.WriteHeader(http.StatusNoContent)
}

// handleSession validates the session without extending it.
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request, sess sqlite.Session) {
	writeJSON(w, http.StatusOK, s.sessionInfo(sess))
}

// handleRenew extends the session in response to genuine user activity. The
// server only moves the expiry once RenewalThreshold has elapsed since the last
// extension, so frequent calls are cheap and harmless.
func (s *Server) handleRenew(w http.ResponseWriter, r *http.Request, sess sqlite.Session) {
	cur, renewed, err := s.Auth.Renew(r.Context(), sess)
	if err != nil {
		s.requireExpired(w)
		return
	}
	if renewed {
		s.setSessionCookie(w, rawCookie(r), cur)
	}
	info := s.sessionInfo(cur)
	writeJSON(w, http.StatusOK, map[string]any{"renewed": renewed, "session": info})
}

func (s *Server) requireExpired(w http.ResponseWriter) {
	s.clearSessionCookie(w)
	writeError(w, http.StatusUnauthorized, "session_expired", "your session expired due to inactivity", nil)
}
