// Package httpapi serves the REST API and the embedded web UI.
package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/geshas/mailroutine/internal/accounts"
	"github.com/geshas/mailroutine/internal/auth"
	"github.com/geshas/mailroutine/internal/config"
	"github.com/geshas/mailroutine/internal/oauth"
	"github.com/geshas/mailroutine/internal/runner"
	"github.com/geshas/mailroutine/internal/security"
	"github.com/geshas/mailroutine/internal/sqlite"
)

const (
	sessionCookie = "mailroutine_session"
	csrfHeader    = "X-CSRF-Token"
	maxBodyBytes  = 1 << 20
	settingTZ     = "timezone"
	repoURL       = "https://github.com/geshas/mailroutine"
	docsURL       = "https://github.com/geshas/mailroutine#readme"
	licenseName   = "Apache-2.0"
)

// Deps are the collaborators the HTTP layer needs.
type Deps struct {
	Config   *config.Config
	Store    *sqlite.Store
	Auth     *auth.Service
	Limiter  *auth.LoginLimiter
	Accounts *accounts.Manager
	Google   *oauth.Google
	Runner   *runner.Runner
	Logger   *slog.Logger
	Version  string
	// Static is the built web UI (index.html at its root). May be nil or empty.
	Static fs.FS
}

// Server is the HTTP handler.
type Server struct {
	Deps
	mux *http.ServeMux
	log *slog.Logger
}

// New builds the server and registers routes.
func New(d Deps) *Server {
	s := &Server{Deps: d, mux: http.NewServeMux(), log: d.Logger.With("component", "http")}
	s.routes()
	return s
}

// Handler returns the fully wrapped http.Handler.
func (s *Server) Handler() http.Handler {
	return s.recoverer(s.logRequests(s.securityHeaders(s.mux)))
}

func (s *Server) routes() {
	m := s.mux
	m.HandleFunc("GET /health", s.handleHealth)
	m.HandleFunc("GET /ready", s.handleReady)

	m.HandleFunc("POST /api/auth/login", s.handleLogin)
	m.HandleFunc("POST /api/auth/logout", s.authedPassive(s.handleLogout))
	m.HandleFunc("GET /api/auth/session", s.authed(s.handleSession))
	m.HandleFunc("POST /api/auth/session/renew", s.authedPassive(s.handleRenew))

	m.HandleFunc("GET /api/dashboard", s.authed(s.handleDashboard))

	m.HandleFunc("GET /api/accounts", s.authed(s.handleListAccounts))
	m.HandleFunc("GET /api/accounts/google/connect", s.authed(s.handleGoogleConnect))
	m.HandleFunc("GET /api/accounts/google/callback", s.handleGoogleCallback)
	m.HandleFunc("DELETE /api/accounts/{id}", s.authed(s.handleDeleteAccount))
	m.HandleFunc("GET /api/accounts/{id}/labels", s.authed(s.handleAccountLabels))
	m.HandleFunc("GET /api/accounts/{id}/labels/mailroutine", s.authed(s.handleTraceLabels))
	m.HandleFunc("POST /api/accounts/{id}/labels/mailroutine", s.authed(s.handleCreateTraceLabels))

	m.HandleFunc("GET /api/routines", s.authed(s.handleListRoutines))
	m.HandleFunc("POST /api/routines", s.authed(s.handleCreateRoutine))
	m.HandleFunc("POST /api/routines/preview", s.authed(s.handlePreviewUnsaved))
	m.HandleFunc("GET /api/routines/{id}", s.authed(s.handleGetRoutine))
	m.HandleFunc("PUT /api/routines/{id}", s.authed(s.handleUpdateRoutine))
	m.HandleFunc("DELETE /api/routines/{id}", s.authed(s.handleDeleteRoutine))
	m.HandleFunc("POST /api/routines/{id}/enable", s.authed(s.handleEnableRoutine))
	m.HandleFunc("POST /api/routines/{id}/disable", s.authed(s.handleDisableRoutine))
	m.HandleFunc("POST /api/routines/{id}/run", s.authed(s.handleRunRoutine))
	m.HandleFunc("POST /api/routines/{id}/preview", s.authed(s.handlePreviewSaved))

	m.HandleFunc("GET /api/runs", s.authed(s.handleListRuns))
	m.HandleFunc("GET /api/runs/{id}", s.authed(s.handleGetRun))

	m.HandleFunc("GET /api/settings", s.authed(s.handleGetSettings))
	m.HandleFunc("PUT /api/settings", s.authed(s.handlePutSettings))

	m.HandleFunc("/api/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, "not_found", "unknown API endpoint", nil)
	})
	m.HandleFunc("/", s.handleStatic)
}

// ---------- middleware ----------

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (w *statusWriter) WriteHeader(code int) {
	w.status = code
	w.ResponseWriter.WriteHeader(code)
}

func (s *Server) recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic in handler", "method", r.Method, "path", r.URL.Path, "panic", rec)
				writeError(w, http.StatusInternalServerError, "internal", "internal server error", nil)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

func (s *Server) logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(sw, r)
		if r.URL.Path == "/health" || !strings.HasPrefix(r.URL.Path, "/api/") && sw.status < 400 {
			return
		}
		level := slog.LevelDebug
		if sw.status >= 500 {
			level = slog.LevelError
		} else if sw.status >= 400 || r.Method != http.MethodGet {
			level = slog.LevelInfo
		}
		// Only the path is logged: query strings can carry OAuth codes.
		s.log.Log(r.Context(), level, "request", "method", r.Method, "path", r.URL.Path, "status", sw.status,
			"duration", time.Since(start).String(), "client_ip", s.clientIP(r))
	})
}

func (s *Server) securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline'; script-src 'self'; "+
			"frame-ancestors 'none'; base-uri 'self'; form-action 'self'; object-src 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("X-Frame-Options", "DENY")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Cross-Origin-Opener-Policy", "same-origin")
		h.Set("Cross-Origin-Resource-Policy", "same-origin")
		h.Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		if s.Config.SecureCookies {
			h.Set("Strict-Transport-Security", "max-age=31536000")
		}
		if strings.HasPrefix(r.URL.Path, "/api/") {
			h.Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

// clientIP returns the caller's address, honouring X-Forwarded-For only when
// the direct peer is a configured trusted proxy.
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	peer := net.ParseIP(host)
	if peer == nil || !s.trusted(peer) {
		return host
	}
	parts := strings.Split(r.Header.Get("X-Forwarded-For"), ",")
	for i := len(parts) - 1; i >= 0; i-- { // right-most untrusted hop is the real client
		ip := net.ParseIP(strings.TrimSpace(parts[i]))
		if ip == nil {
			break
		}
		if !s.trusted(ip) {
			return ip.String()
		}
	}
	return host
}

func (s *Server) trusted(ip net.IP) bool {
	for _, n := range s.Config.TrustedProxies {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

// ---------- auth wrappers ----------

type authedHandler func(w http.ResponseWriter, r *http.Request, sess sqlite.Session)

func safeMethod(m string) bool {
	return m == http.MethodGet || m == http.MethodHead || m == http.MethodOptions
}

// authed requires a valid session. State-changing requests additionally need a
// matching CSRF token and a same-origin Origin header; they also count as user
// activity and slide the session forward. GET requests (including background
// polling) never extend the session.
func (s *Server) authed(h authedHandler) http.HandlerFunc { return s.authWrap(h, true) }

// authedPassive is like authed but does not auto-renew (used by renew/logout).
func (s *Server) authedPassive(h authedHandler) http.HandlerFunc { return s.authWrap(h, false) }

func (s *Server) authWrap(h authedHandler, renewOnWrite bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		sess, ok := s.requireSession(w, r)
		if !ok {
			return
		}
		if !safeMethod(r.Method) {
			if !s.sameOrigin(r) {
				writeError(w, http.StatusForbidden, "csrf_origin", "cross-origin request rejected: use the address configured as APP_URL ("+s.Config.AppURL.String()+")", nil)
				return
			}
			if !security.ConstantTimeEqual(r.Header.Get(csrfHeader), s.Auth.CSRFToken(sess)) {
				writeError(w, http.StatusForbidden, "csrf_invalid", "missing or invalid CSRF token", nil)
				return
			}
			if renewOnWrite {
				if cur, renewed, err := s.Auth.Renew(r.Context(), sess); err == nil && renewed {
					sess = cur
					s.setSessionCookie(w, rawCookie(r), sess)
				}
			}
		}
		h(w, r, sess)
	}
}

func rawCookie(r *http.Request) string {
	if c, err := r.Cookie(sessionCookie); err == nil {
		return c.Value
	}
	return ""
}

func (s *Server) requireSession(w http.ResponseWriter, r *http.Request) (sqlite.Session, bool) {
	token := rawCookie(r)
	sess, err := s.Auth.Validate(r.Context(), token)
	switch {
	case err == nil:
		return sess, true
	case errors.Is(err, auth.ErrExpired):
		s.clearSessionCookie(w)
		writeError(w, http.StatusUnauthorized, "session_expired", "your session expired due to inactivity", nil)
	case errors.Is(err, auth.ErrUnauthenticated):
		if token != "" {
			s.clearSessionCookie(w)
		}
		writeError(w, http.StatusUnauthorized, "unauthenticated", "authentication required", nil)
	default:
		s.log.Error("session lookup failed", "error", err)
		writeError(w, http.StatusInternalServerError, "internal", "internal server error", nil)
	}
	return sqlite.Session{}, false
}

// sameOrigin rejects browser requests whose Origin differs from APP_URL.
// Requests without an Origin header (non-browser clients) rely on the CSRF token.
func (s *Server) sameOrigin(r *http.Request) bool {
	origin := r.Header.Get("Origin")
	if origin == "" {
		return true
	}
	u, err := url.Parse(origin)
	if err != nil {
		return false
	}
	return strings.EqualFold(u.Scheme, s.Config.AppURL.Scheme) && strings.EqualFold(u.Host, s.Config.AppURL.Host)
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string, sess sqlite.Session) {
	maxAge := int(time.Until(sess.ExpiresAt).Seconds())
	if maxAge < 1 {
		maxAge = 1
	}
	http.SetCookie(w, &http.Cookie{
		Name: sessionCookie, Value: token, Path: "/", MaxAge: maxAge, HttpOnly: true,
		Secure: s.Config.SecureCookies, SameSite: http.SameSiteLaxMode,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{Name: sessionCookie, Value: "", Path: "/", MaxAge: -1, HttpOnly: true,
		Secure: s.Config.SecureCookies, SameSite: http.SameSiteLaxMode})
}

// ---------- JSON helpers ----------

type apiError struct {
	Code    string            `json:"code"`
	Message string            `json:"message"`
	Fields  map[string]string `json:"fields,omitempty"`
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	if v != nil {
		_ = json.NewEncoder(w).Encode(v)
	}
}

func writeError(w http.ResponseWriter, status int, code, msg string, fields map[string]string) {
	writeJSON(w, status, map[string]apiError{"error": {Code: code, Message: msg, Fields: fields}})
}

// decode reads a JSON body, rejecting unknown fields and oversized payloads.
func decode(w http.ResponseWriter, r *http.Request, dst any) bool {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		if errors.Is(err, io.EOF) {
			return true // empty body: use defaults
		}
		writeError(w, http.StatusBadRequest, "invalid_json", "request body is not valid JSON for this endpoint: "+err.Error(), nil)
		return false
	}
	return true
}

func (s *Server) internalError(w http.ResponseWriter, err error, what string) {
	s.log.Error(what, "error", err)
	writeError(w, http.StatusInternalServerError, "internal", "internal server error", nil)
}

// ---------- static UI ----------

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed", nil)
		return
	}
	if s.Static == nil {
		http.Error(w, "web UI is not built into this binary", http.StatusNotFound)
		return
	}
	p := strings.TrimPrefix(path.Clean("/"+r.URL.Path), "/")
	if p == "" {
		p = "index.html"
	}
	f, err := s.Static.Open(p)
	if err == nil {
		st, serr := f.Stat()
		f.Close()
		if serr == nil && st.IsDir() {
			err = fs.ErrNotExist
		}
	}
	if err != nil {
		// Single-page-app fallback; missing asset files still 404.
		if strings.Contains(path.Base(p), ".") {
			http.NotFound(w, r)
			return
		}
		p = "index.html"
	}
	if strings.HasPrefix(p, "assets/") {
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	} else {
		w.Header().Set("Cache-Control", "no-cache")
	}
	data, err := fs.ReadFile(s.Static, p)
	if err != nil {
		http.Error(w, "web UI is not built into this binary", http.StatusNotFound)
		return
	}
	http.ServeContent(w, r, p, time.Time{}, strings.NewReader(string(data)))
}

// ---------- health ----------

func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	// The version is deliberately not exposed to unauthenticated callers.
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (s *Server) handleReady(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()
	if err := s.Store.Ping(ctx); err != nil {
		writeError(w, http.StatusServiceUnavailable, "not_ready", "database unavailable", nil)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}
