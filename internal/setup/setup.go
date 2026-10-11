// Package setup serves a small first-run page for containers started without a valid
// configuration (typical in Portainer or other UIs where there is no shell to run
// `mailroutine hash-password`).
//
// The page is deliberately stateless: it generates fresh random secrets and hashes the password
// you type, shows the result for you to copy into your stack's environment, and applies
// nothing. It cannot be used to take over an installation, and the values it prints are not
// accepted by the server until you set them yourself and restart.
package setup

import (
	_ "embed"
	"encoding/base64"
	"html/template"
	"net/http"
	"sync"
	"time"

	"github.com/geshas/mailroutine/internal/security"
)

//go:embed setup.html
var page string

var tmpl = template.Must(template.New("setup").Parse(page))

const (
	minPasswordLength = 8
	maxPasswordLength = 256
	maxBodyBytes      = 4096
)

// Handler returns the setup handler. problems are the configuration errors that triggered setup
// mode, shown at the top of the page; version is displayed in the footer.
func Handler(problems, version string) http.Handler {
	h := &handler{problems: problems, version: version, limiter: newLimiter(10, time.Minute), hashSlot: make(chan struct{}, 1)}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", h.health)
	mux.HandleFunc("GET /ready", h.health)
	mux.HandleFunc("GET /{$}", h.show)
	mux.HandleFunc("POST /{$}", h.generate)
	return secure(mux)
}

type handler struct {
	problems, version string
	limiter           *limiter
	hashSlot          chan struct{} // one Argon2 at a time keeps memory bounded
}

type view struct {
	Problems, Version string
	Error             string
	SessionSecret     string
	EncryptionKey     string
	Username          string
	HashB64           string // set once a password has been hashed
	Hash              string
}

func newSecrets() (session, encryption string) {
	s, err1 := security.GenerateSecret()
	e, err2 := security.GenerateSecret()
	if err1 != nil || err2 != nil {
		panic("crypto/rand unavailable")
	}
	return s, e
}

func secure(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hd := w.Header()
		// The page prints secrets: never cache it, never embed it, load nothing from elsewhere.
		hd.Set("Cache-Control", "no-store")
		hd.Set("Content-Security-Policy", "default-src 'none'; style-src 'unsafe-inline'; form-action 'self'; frame-ancestors 'none'; base-uri 'none'")
		hd.Set("X-Content-Type-Options", "nosniff")
		hd.Set("X-Frame-Options", "DENY")
		hd.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}

func (h *handler) health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusServiceUnavailable) // not configured: report unhealthy honestly
	_, _ = w.Write([]byte(`{"status":"setup_required"}` + "\n"))
}

func (h *handler) render(w http.ResponseWriter, status int, v view) {
	v.Problems, v.Version = h.problems, h.version
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_ = tmpl.Execute(w, v)
}

func (h *handler) show(w http.ResponseWriter, _ *http.Request) {
	s, e := newSecrets()
	h.render(w, http.StatusOK, view{SessionSecret: s, EncryptionKey: e, Username: "admin"})
}

func (h *handler) generate(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
	if err := r.ParseForm(); err != nil {
		h.render(w, http.StatusBadRequest, view{Error: "The form was not understood.", Username: "admin"})
		return
	}
	// Echo back the secrets that were on the page so what you copy matches what you saw.
	v := view{SessionSecret: r.PostForm.Get("session_secret"), EncryptionKey: r.PostForm.Get("encryption_key"), Username: "admin"}
	if _, err := security.ParseKey(v.EncryptionKey); err != nil || len(v.SessionSecret) < 32 || len(v.SessionSecret) > 128 {
		v.SessionSecret, v.EncryptionKey = newSecrets() // stale or tampered form: start from fresh values
	}
	if !h.limiter.allow() {
		v.Error = "Too many attempts. Wait a minute and try again."
		h.render(w, http.StatusTooManyRequests, v)
		return
	}
	pw, confirm := r.PostForm.Get("password"), r.PostForm.Get("confirm")
	switch {
	case len(pw) < minPasswordLength:
		v.Error = "The password must be at least 8 characters."
	case len(pw) > maxPasswordLength:
		v.Error = "The password is too long."
	case pw != confirm:
		v.Error = "The two passwords do not match."
	}
	if v.Error != "" {
		h.render(w, http.StatusBadRequest, v)
		return
	}
	h.hashSlot <- struct{}{}
	hash, err := security.HashPassword(pw)
	<-h.hashSlot
	if err != nil {
		v.Error = "Could not hash the password."
		h.render(w, http.StatusInternalServerError, v)
		return
	}
	v.Hash = hash
	v.HashB64 = base64.StdEncoding.EncodeToString([]byte(hash))
	h.render(w, http.StatusOK, v)
}

// limiter is a tiny global fixed-window limiter: this page is only a convenience and must not
// become a way to burn CPU and memory with Argon2.
type limiter struct {
	mu     sync.Mutex
	max    int
	window time.Duration
	hits   []time.Time
	now    func() time.Time
}

func newLimiter(max int, window time.Duration) *limiter {
	return &limiter{max: max, window: window, now: time.Now}
}

func (l *limiter) allow() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	now := l.now()
	kept := l.hits[:0]
	for _, t := range l.hits {
		if now.Sub(t) < l.window {
			kept = append(kept, t)
		}
	}
	l.hits = kept
	if len(l.hits) >= l.max {
		return false
	}
	l.hits = append(l.hits, now)
	return true
}
