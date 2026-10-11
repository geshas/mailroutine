package httpapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/fstest"
	"time"

	"golang.org/x/oauth2"
	"google.golang.org/api/option"

	"github.com/geshas/mailroutine/internal/accounts"
	"github.com/geshas/mailroutine/internal/auth"
	"github.com/geshas/mailroutine/internal/config"
	httpapi "github.com/geshas/mailroutine/internal/http"
	"github.com/geshas/mailroutine/internal/oauth"
	"github.com/geshas/mailroutine/internal/runner"
	"github.com/geshas/mailroutine/internal/security"
	"github.com/geshas/mailroutine/internal/sqlite"
	"github.com/geshas/mailroutine/pkg/engine"
	"github.com/geshas/mailroutine/pkg/models"
)

// ---- fake Google (token endpoint + Gmail API) ----

type fakeGoogle struct {
	mu       sync.Mutex
	messages []string
	modified [][]string
	gate     chan struct{}
	started  chan struct{}

	// fault injection (all guarded by mu)
	listStatus   int  // forced status for messages.list
	labelsStatus int  // forced status for labels.list
	tokenFail    bool // token endpoint rejects the grant
	noRefresh    bool // token endpoint omits refresh_token
	profileFail  bool // profile endpoint fails

	scopeOverride *string          // scope string the token endpoint reports (nil = the full Gmail scope)
	userLabels    []string         // user labels that exist in the mailbox
	modifyBodies  []map[string]any // full batchModify requests, in order
	labelCreates  int
}

func (f *fakeGoogle) set(fn func()) { f.mu.Lock(); fn(); f.mu.Unlock() }

func (f *fakeGoogle) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("POST /token", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		fail, noRefresh := f.tokenFail, f.noRefresh
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if fail {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":"invalid_grant"}`)
			return
		}
		scope := "https://www.googleapis.com/auth/gmail.modify"
		f.mu.Lock()
		if f.scopeOverride != nil {
			scope = *f.scopeOverride
		}
		f.mu.Unlock()
		resp := map[string]any{"access_token": "ya29.fake", "token_type": "Bearer", "refresh_token": "1//new-refresh", "expires_in": 3600, "scope": scope}
		if noRefresh {
			delete(resp, "refresh_token")
		}
		json.NewEncoder(w).Encode(resp)
	})
	mux.HandleFunc("POST /revoke", func(w http.ResponseWriter, r *http.Request) {})
	mux.HandleFunc("GET /gmail/v1/users/me/profile", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		pf := f.profileFail
		f.mu.Unlock()
		if pf {
			w.WriteHeader(403)
			return
		}
		json.NewEncoder(w).Encode(map[string]string{"emailAddress": "connected@gmail.com"})
	})
	mux.HandleFunc("GET /gmail/v1/users/me/messages", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		if f.listStatus != 0 {
			w.WriteHeader(f.listStatus)
			io.WriteString(w, `{"error":{"code":`+fmt.Sprint(f.listStatus)+`,"message":"injected"}}`)
			return
		}
		if r.URL.Query().Get("q") == "bad:::query" {
			w.WriteHeader(400)
			io.WriteString(w, `{"error":{"code":400,"message":"Invalid"}}`)
			return
		}
		var ms []map[string]string
		for _, id := range f.messages {
			ms = append(ms, map[string]string{"id": id})
		}
		json.NewEncoder(w).Encode(map[string]any{"messages": ms, "resultSizeEstimate": len(ms)})
	})
	mux.HandleFunc("GET /gmail/v1/users/me/messages/{id}", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(map[string]any{"id": r.PathValue("id"), "payload": map[string]any{"headers": []map[string]string{
			{"name": "From", "value": "sender@example.com"}, {"name": "Subject", "value": "Subj " + r.PathValue("id")}, {"name": "Date", "value": "Mon, 02 Jan 2006 15:04:05 +0000"}}}})
	})
	mux.HandleFunc("POST /gmail/v1/users/me/messages/batchModify", func(w http.ResponseWriter, r *http.Request) {
		if f.started != nil {
			select {
			case f.started <- struct{}{}:
			default:
			}
		}
		if f.gate != nil {
			<-f.gate
		}
		var raw map[string]any
		json.NewDecoder(r.Body).Decode(&raw)
		var m struct{ Ids []string }
		if ids, ok := raw["ids"].([]any); ok {
			for _, id := range ids {
				m.Ids = append(m.Ids, id.(string))
			}
		}
		f.mu.Lock()
		f.modifyBodies = append(f.modifyBodies, raw)
		f.modified = append(f.modified, m.Ids)
		f.mu.Unlock()
		w.WriteHeader(204)
	})
	mux.HandleFunc("GET /gmail/v1/users/me/labels", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		ls := f.labelsStatus
		f.mu.Unlock()
		if ls != 0 {
			w.WriteHeader(ls)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		labels := []map[string]string{{"id": "INBOX", "name": "INBOX", "type": "system"}}
		for _, n := range f.userLabels {
			labels = append(labels, map[string]string{"id": "Label_" + n, "name": n, "type": "user"})
		}
		json.NewEncoder(w).Encode(map[string]any{"labels": labels})
	})
	mux.HandleFunc("POST /gmail/v1/users/me/labels", func(w http.ResponseWriter, r *http.Request) {
		var l struct{ Name string }
		json.NewDecoder(r.Body).Decode(&l)
		f.mu.Lock()
		defer f.mu.Unlock()
		for _, n := range f.userLabels {
			if strings.EqualFold(n, l.Name) {
				w.WriteHeader(409)
				io.WriteString(w, `{"error":{"code":409,"message":"Label name exists"}}`)
				return
			}
		}
		f.userLabels = append(f.userLabels, l.Name)
		f.labelCreates++
		json.NewEncoder(w).Encode(map[string]string{"id": "Label_" + l.Name, "name": l.Name})
	})
	return mux
}

// ---- harness ----

type harness struct {
	t      *testing.T
	srv    *httptest.Server
	client *http.Client
	store  *sqlite.Store
	auth   *auth.Service
	fake   *fakeGoogle
	acc    models.Account
	clock  *testClock
	csrf   string
	runner *runner.Runner
	dbPath string
	cfg    *config.Config
}

type testClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *testClock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *testClock) Advance(d time.Duration) {
	c.mu.Lock()
	c.t = c.t.Add(d)
	c.mu.Unlock()
}

var adminHash, _ = security.HashPassword("hunter2-hunter2")

func newHarness(t *testing.T, env map[string]string) *harness {
	t.Helper()
	ctx := context.Background()
	h := &harness{t: t, fake: &fakeGoogle{messages: []string{"m1", "m2", "m3"}, userLabels: []string{"Newsletters"}}, clock: &testClock{t: time.Now()}}
	gsrv := httptest.NewServer(h.fake.handler())
	t.Cleanup(gsrv.Close)

	key, _ := security.GenerateSecret()
	e := map[string]string{
		"ADMIN_PASSWORD_HASH": adminHash, "SESSION_SECRET": strings.Repeat("k", 40),
		"GOOGLE_CLIENT_ID": "cid", "GOOGLE_CLIENT_SECRET": "csecret-value", "ENCRYPTION_KEY": key,
	}
	for k, v := range env {
		e[k] = v
	}
	cfg, err := config.LoadFrom(func(k string) string { return e[k] })
	if err != nil {
		t.Fatal(err)
	}
	h.cfg, h.dbPath = cfg, filepath.Join(t.TempDir(), "api.db")
	h.store, err = sqlite.Open(ctx, h.dbPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { h.store.Close() })

	h.auth = auth.New(h.store, auth.Settings{Username: cfg.AdminUsername, PasswordHash: cfg.AdminPasswordHash, Secret: cfg.SessionSecret,
		IdleTimeout: cfg.SessionIdleTimeout, RenewThreshold: cfg.SessionRenewalThreshold, AbsoluteTimeout: cfg.SessionAbsoluteTimeout})
	h.auth.Now = h.clock.Now

	google := oauth.New(cfg.GoogleClientID, cfg.GoogleClientSecret, cfg.GoogleRedirectURL)
	google.SetEndpoint(oauth2.Endpoint{AuthURL: gsrv.URL + "/auth", TokenURL: gsrv.URL + "/token"})
	google.RevokeURL = gsrv.URL + "/revoke"
	mgr := &accounts.Manager{Store: h.store, Google: google, Key: cfg.EncryptionKey, Timeout: 5 * time.Second,
		Logger: slog.New(slog.DiscardHandler), GmailOptions: []option.ClientOption{option.WithEndpoint(gsrv.URL + "/")}}
	h.runner = runner.New(runner.Options{Store: h.store, MaxConcurrent: 2, BatchSize: 100, Retry: &engine.RetryPolicy{MaxAttempts: 1},
		Provider: func(ctx context.Context, acc models.Account) (engine.MailProvider, error) {
			return mgr.Provider(ctx, acc)
		},
		OnAuthRevoked: func(ctx context.Context, id string) { mgr.MarkNeedsReauth(ctx, id) }})
	t.Cleanup(func() { h.runner.Shutdown(ctx) })

	static := fstest.MapFS{"index.html": {Data: []byte("<html>spa</html>")}, "assets/app.js": {Data: []byte("console.log(1)")}}
	api := httpapi.New(httpapi.Deps{Config: cfg, Store: h.store, Auth: h.auth, Limiter: auth.NewLoginLimiter(5, 50, time.Minute),
		Accounts: mgr, Google: google, Runner: h.runner, Logger: slog.New(slog.DiscardHandler), Version: "test", Static: static})
	h.srv = httptest.NewServer(api.Handler())
	t.Cleanup(h.srv.Close)

	jar, _ := cookiejar.New(nil)
	h.client = &http.Client{Jar: jar, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}

	enc, _ := security.Encrypt(cfg.EncryptionKey, "1//stored-refresh")
	h.acc, _, _ = h.store.UpsertAccount(ctx, "me@gmail.com", enc)
	return h
}

func (h *harness) req(method, path string, body any, headers ...string) (*http.Response, []byte) {
	h.t.Helper()
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, h.srv.URL+path, rd)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	resp, err := h.client.Do(req)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return resp, data
}

// call sends an authenticated request including the CSRF header.
func (h *harness) call(method, path string, body any) (*http.Response, map[string]any) {
	h.t.Helper()
	resp, data := h.req(method, path, body, "X-CSRF-Token", h.csrf)
	var m map[string]any
	json.Unmarshal(data, &m)
	return resp, m
}

func (h *harness) login() {
	h.t.Helper()
	resp, data := h.req("POST", "/api/auth/login", map[string]string{"username": "admin", "password": "hunter2-hunter2"})
	if resp.StatusCode != 200 {
		h.t.Fatalf("login: %d %s", resp.StatusCode, data)
	}
	var m map[string]any
	json.Unmarshal(data, &m)
	h.csrf = m["csrf_token"].(string)
}

func errCode(m map[string]any) string {
	e, _ := m["error"].(map[string]any)
	c, _ := e["code"].(string)
	return c
}

func routineBody(accountID string) map[string]any {
	return map[string]any{
		"account_id": accountID, "name": "Archive news", "gmail_query": "label:newsletters older_than:7d",
		"action_type": "archive", "schedule_type": "daily", "schedule_config": map[string]any{"time": "03:00", "timezone": "UTC"},
		"max_messages_per_run": 50,
	}
}

func (h *harness) waitRun(id string) map[string]any {
	h.t.Helper()
	for i := 0; i < 200; i++ {
		_, m := h.call("GET", "/api/runs/"+id, nil)
		if run, _ := m["run"].(map[string]any); run != nil && run["status"] != "running" {
			return m
		}
		time.Sleep(25 * time.Millisecond)
	}
	h.t.Fatal("run did not finish")
	return nil
}

// ---- tests ----

func TestPublicAndProtectedEndpoints(t *testing.T) {
	h := newHarness(t, nil)
	if resp, _ := h.req("GET", "/health", nil); resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	if resp, _ := h.req("GET", "/ready", nil); resp.StatusCode != 200 {
		t.Fatal(resp.StatusCode)
	}
	for _, ep := range [][2]string{{"GET", "/api/dashboard"}, {"GET", "/api/accounts"}, {"GET", "/api/routines"}, {"POST", "/api/routines"},
		{"GET", "/api/runs"}, {"GET", "/api/settings"}, {"GET", "/api/auth/session"}, {"POST", "/api/auth/session/renew"},
		{"GET", "/api/accounts/google/connect"}, {"DELETE", "/api/accounts/x"}, {"POST", "/api/routines/x/run"}} {
		resp, data := h.req(ep[0], ep[1], nil)
		if resp.StatusCode != 401 {
			t.Errorf("%s %s: %d %s", ep[0], ep[1], resp.StatusCode, data)
		}
	}
	resp, _ := h.req("GET", "/api/nope", nil)
	if resp.StatusCode != 404 || resp.Header.Get("Content-Type") != "application/json; charset=utf-8" {
		t.Fatalf("%d %s", resp.StatusCode, resp.Header.Get("Content-Type"))
	}
}

func TestLoginLogoutAndCookieFlags(t *testing.T) {
	h := newHarness(t, nil)
	resp, data := h.req("POST", "/api/auth/login", map[string]string{"username": "admin", "password": "wrong"})
	if resp.StatusCode != 401 || strings.Contains(string(data), "hunter2") {
		t.Fatalf("%d %s", resp.StatusCode, data)
	}
	resp, _ = h.req("POST", "/api/auth/login", map[string]string{"username": "nobody", "password": "hunter2-hunter2"})
	if resp.StatusCode != 401 {
		t.Fatal("wrong username accepted")
	}
	resp, data = h.req("POST", "/api/auth/login", map[string]string{"username": "admin", "password": "hunter2-hunter2"})
	if resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, data)
	}
	var c *http.Cookie
	for _, ck := range resp.Cookies() {
		if ck.Name == "mailroutine_session" {
			c = ck
		}
	}
	if c == nil || !c.HttpOnly || c.SameSite != http.SameSiteLaxMode || c.Secure || c.MaxAge < 1700 || c.MaxAge > 1800 {
		t.Fatalf("cookie flags wrong: %+v", c)
	}
	if strings.Contains(string(data), c.Value) {
		t.Fatal("session token leaked in body")
	}
	var info map[string]any
	json.Unmarshal(data, &info)
	h.csrf = info["csrf_token"].(string)

	if r, m := h.call("GET", "/api/auth/session", nil); r.StatusCode != 200 || m["username"] != "admin" {
		t.Fatalf("%d %v", r.StatusCode, m)
	}
	// Logout needs CSRF and then kills the session server-side.
	if r, _ := h.req("POST", "/api/auth/logout", nil); r.StatusCode != 403 {
		t.Fatalf("logout without csrf: %d", r.StatusCode)
	}
	if r, _ := h.call("POST", "/api/auth/logout", nil); r.StatusCode != 204 {
		t.Fatal(r.StatusCode)
	}
	if r, _ := h.call("GET", "/api/auth/session", nil); r.StatusCode != 401 {
		t.Fatal("session usable after logout")
	}
	// Replaying the old cookie value directly must also fail.
	req, _ := http.NewRequest("GET", h.srv.URL+"/api/auth/session", nil)
	req.AddCookie(c)
	r2, _ := http.DefaultClient.Do(req)
	if r2.StatusCode != 401 {
		t.Fatal("old token still valid")
	}
	if n, _ := h.store.CountSessions(context.Background()); n != 0 {
		t.Fatalf("%d sessions remain", n)
	}
}

func TestLoginRequiresJSONAndSameOrigin(t *testing.T) {
	h := newHarness(t, nil)
	b := `{"username":"admin","password":"hunter2-hunter2"}`
	req, _ := http.NewRequest("POST", h.srv.URL+"/api/auth/login", strings.NewReader(b))
	req.Header.Set("Content-Type", "text/plain")
	if r, _ := h.client.Do(req); r.StatusCode != 403 {
		t.Fatalf("form-style login accepted: %d", r.StatusCode)
	}
	resp, _ := h.req("POST", "/api/auth/login", map[string]string{"username": "admin", "password": "hunter2-hunter2"}, "Origin", "https://evil.example")
	if resp.StatusCode != 403 {
		t.Fatalf("cross-origin login accepted: %d", resp.StatusCode)
	}
	resp, _ = h.req("POST", "/api/auth/login", map[string]string{"username": "admin", "password": "hunter2-hunter2"}, "Origin", "http://localhost:8080")
	if resp.StatusCode != 200 {
		t.Fatalf("same-origin login rejected: %d", resp.StatusCode)
	}
}

func TestLoginThrottling(t *testing.T) {
	h := newHarness(t, nil)
	for i := 0; i < 5; i++ {
		if r, _ := h.req("POST", "/api/auth/login", map[string]string{"username": "admin", "password": "bad"}); r.StatusCode != 401 {
			t.Fatalf("attempt %d: %d", i, r.StatusCode)
		}
	}
	resp, data := h.req("POST", "/api/auth/login", map[string]string{"username": "admin", "password": "hunter2-hunter2"})
	if resp.StatusCode != 429 || resp.Header.Get("Retry-After") == "" {
		t.Fatalf("correct password must also be blocked while throttled: %d %s", resp.StatusCode, data)
	}
}

func TestCSRFProtection(t *testing.T) {
	h := newHarness(t, nil)
	h.login()
	body := routineBody(h.acc.ID)
	if r, m := h.req("POST", "/api/routines", body); r.StatusCode != 403 {
		t.Fatalf("no token: %d %s", r.StatusCode, m)
	}
	if r, _ := h.req("POST", "/api/routines", body, "X-CSRF-Token", "forged"); r.StatusCode != 403 {
		t.Fatal("forged token accepted")
	}
	if r, _ := h.req("POST", "/api/routines", body, "X-CSRF-Token", h.csrf, "Origin", "https://evil.example"); r.StatusCode != 403 {
		t.Fatal("cross-origin request accepted")
	}
	if r, _ := h.call("POST", "/api/routines", body); r.StatusCode != 201 {
		t.Fatalf("valid request rejected: %d", r.StatusCode)
	}
	// GET needs no token.
	if r, _ := h.req("GET", "/api/routines", nil); r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	// A CSRF token from another session is useless.
	other := newHarness(t, nil)
	other.login()
	if r, _ := h.req("POST", "/api/routines", body, "X-CSRF-Token", other.csrf); r.StatusCode != 403 {
		t.Fatal("token from another session accepted")
	}
}

func TestSlidingSessionOverHTTP(t *testing.T) {
	h := newHarness(t, nil)
	h.login()
	// Background polling (GETs) never extends the session.
	for i := 0; i < 5; i++ {
		h.clock.Advance(5 * time.Minute)
		if r, _ := h.call("GET", "/api/dashboard", nil); r.StatusCode != 200 {
			t.Fatalf("poll %d: %d", i, r.StatusCode)
		}
		if r, m := h.call("GET", "/api/auth/session", nil); r.StatusCode != 200 {
			t.Fatalf("%v", m)
		}
	}
	h.clock.Advance(5*time.Minute + time.Second)
	r, m := h.call("GET", "/api/dashboard", nil)
	if r.StatusCode != 401 || errCode(m) != "session_expired" {
		t.Fatalf("polling kept the session alive: %d %v", r.StatusCode, m)
	}

	// Explicit renewals keep an active user logged in far beyond 30 minutes.
	h.login()
	for i := 0; i < 12; i++ {
		h.clock.Advance(10 * time.Minute)
		r, m := h.call("POST", "/api/auth/session/renew", nil)
		if r.StatusCode != 200 || m["renewed"] != true {
			t.Fatalf("renew %d: %d %v", i, r.StatusCode, m)
		}
		if r.Header.Get("Set-Cookie") == "" {
			t.Fatal("renewal must refresh the cookie")
		}
	}
	if r, _ := h.call("GET", "/api/dashboard", nil); r.StatusCode != 200 {
		t.Fatalf("active user logged out after 2h: %d", r.StatusCode)
	}
	// A renewal before the threshold is a no-op.
	_, m = h.call("POST", "/api/auth/session/renew", nil)
	if m["renewed"] != false {
		t.Fatalf("premature renewal: %v", m)
	}
	// Writes count as activity too.
	h.clock.Advance(20 * time.Minute)
	if r, _ := h.call("POST", "/api/routines/preview", map[string]any{"account_id": h.acc.ID, "gmail_query": "in:inbox"}); r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	h.clock.Advance(20 * time.Minute)
	if r, _ := h.call("GET", "/api/dashboard", nil); r.StatusCode != 200 {
		t.Fatal("write did not extend the session")
	}
	// Idle → logout.
	h.clock.Advance(31 * time.Minute)
	if r, _ := h.call("POST", "/api/auth/session/renew", nil); r.StatusCode != 401 {
		t.Fatalf("renewal must not resurrect an expired session: %d", r.StatusCode)
	}
}

func TestSessionSurvivesServerRestartAndCredentialChange(t *testing.T) {
	h := newHarness(t, nil)
	h.login()
	// "Restart": a fresh auth service over the same database accepts the cookie.
	svc2 := auth.New(h.store, auth.Settings{Username: "admin", PasswordHash: adminHash, Secret: []byte(strings.Repeat("k", 40)),
		IdleTimeout: 30 * time.Minute, RenewThreshold: 5 * time.Minute})
	if n, _ := svc2.InvalidateStaleSessions(context.Background()); n != 0 {
		t.Fatal("purged valid sessions on restart")
	}
	u, _ := url.Parse(h.srv.URL)
	var token string
	for _, c := range h.client.Jar.Cookies(u) {
		if c.Name == "mailroutine_session" {
			token = c.Value
		}
	}
	if _, err := svc2.Validate(context.Background(), token); err != nil {
		t.Fatalf("session lost across restart: %v", err)
	}
	newHash, _ := security.HashPassword("brand-new-password")
	svc3 := auth.New(h.store, auth.Settings{Username: "admin", PasswordHash: newHash, Secret: []byte(strings.Repeat("k", 40)),
		IdleTimeout: 30 * time.Minute, RenewThreshold: 5 * time.Minute})
	svc3.InvalidateStaleSessions(context.Background())
	if _, err := svc3.Validate(context.Background(), token); err == nil {
		t.Fatal("session survived password change")
	}
}

func TestSecureCookieOverHTTPS(t *testing.T) {
	h := newHarness(t, map[string]string{"APP_URL": "https://mail.example.com"})
	resp, _ := h.req("POST", "/api/auth/login", map[string]string{"username": "admin", "password": "hunter2-hunter2"}, "Origin", "https://mail.example.com")
	var secure bool
	for _, c := range resp.Cookies() {
		if c.Name == "mailroutine_session" {
			secure = c.Secure
		}
	}
	if resp.StatusCode != 200 || !secure || resp.Header.Get("Strict-Transport-Security") == "" {
		t.Fatalf("status %d secure=%v", resp.StatusCode, secure)
	}
}

func TestRoutineLifecycle(t *testing.T) {
	h := newHarness(t, nil)
	h.login()
	// Validation.
	bad := routineBody(h.acc.ID)
	bad["name"] = ""
	bad["gmail_query"] = ""
	bad["schedule_config"] = map[string]any{"time": "99:00"}
	r, m := h.call("POST", "/api/routines", bad)
	fields := m["error"].(map[string]any)["fields"].(map[string]any)
	if r.StatusCode != 400 || errCode(m) != "validation_failed" || fields["name"] == nil || fields["gmail_query"] == nil || fields["schedule_config.time"] == nil {
		t.Fatalf("%d %v", r.StatusCode, m)
	}
	unknownAcc := routineBody("nope")
	if r, m := h.call("POST", "/api/routines", unknownAcc); r.StatusCode != 400 || m["error"].(map[string]any)["fields"].(map[string]any)["account_id"] == nil {
		t.Fatalf("%d %v", r.StatusCode, m)
	}
	if r, _ := h.req("POST", "/api/routines", map[string]any{"bogus": 1}, "X-CSRF-Token", h.csrf); r.StatusCode != 400 {
		t.Fatal("unknown fields accepted")
	}
	lbl := routineBody(h.acc.ID)
	lbl["action_type"] = "add_label"
	if r, _ := h.call("POST", "/api/routines", lbl); r.StatusCode != 400 {
		t.Fatal("label action without label accepted")
	}
	lbl["action_config"] = map[string]any{"label": "TRASH"}
	if r, _ := h.call("POST", "/api/routines", lbl); r.StatusCode != 400 {
		t.Fatal("TRASH label bypass accepted")
	}

	// Create disabled: no next run yet.
	r, m = h.call("POST", "/api/routines", routineBody(h.acc.ID))
	if r.StatusCode != 201 || m["enabled"] != false || m["next_run_at"] != nil || m["account_email"] != "me@gmail.com" {
		t.Fatalf("%d %v", r.StatusCode, m)
	}
	id := m["id"].(string)

	// Enable computes the next run; disable clears it.
	r, m = h.call("POST", "/api/routines/"+id+"/enable", nil)
	if r.StatusCode != 200 || m["enabled"] != true || m["next_run_at"] == nil {
		t.Fatalf("%d %v", r.StatusCode, m)
	}
	next, _ := time.Parse(time.RFC3339Nano, m["next_run_at"].(string))
	if next.Hour() != 3 || next.Minute() != 0 || !next.After(time.Now()) {
		t.Fatalf("bad next run %v", next)
	}
	r, m = h.call("POST", "/api/routines/"+id+"/disable", nil)
	if m["enabled"] != false || m["next_run_at"] != nil {
		t.Fatalf("%v", m)
	}

	// Update.
	upd := routineBody(h.acc.ID)
	upd["name"] = "Renamed"
	upd["schedule_type"] = "manual"
	upd["enabled"] = true
	r, m = h.call("PUT", "/api/routines/"+id, upd)
	if r.StatusCode != 200 || m["name"] != "Renamed" || m["next_run_at"] != nil {
		t.Fatalf("manual routines have no next run: %d %v", r.StatusCode, m)
	}
	if r, m := h.call("GET", "/api/routines/"+id, nil); r.StatusCode != 200 || m["schedule_type"] != "manual" {
		t.Fatal("get")
	}
	if r, _ := h.call("GET", "/api/routines/missing", nil); r.StatusCode != 404 {
		t.Fatal("missing routine")
	}
	_, list := h.call("GET", "/api/routines", nil)
	if len(list["items"].([]any)) != 1 {
		t.Fatal("list")
	}
	if r, _ := h.call("DELETE", "/api/routines/"+id, nil); r.StatusCode != 204 {
		t.Fatal(r.StatusCode)
	}
	if r, _ := h.call("DELETE", "/api/routines/"+id, nil); r.StatusCode != 404 {
		t.Fatal(r.StatusCode)
	}
}

func TestDestructiveActionsNeedConfirmation(t *testing.T) {
	h := newHarness(t, nil)
	h.login()
	body := routineBody(h.acc.ID)
	body["action_type"] = "trash"
	// Saving disabled is fine; enabling is not without confirmation.
	r, m := h.call("POST", "/api/routines", body)
	if r.StatusCode != 201 {
		t.Fatalf("%d %v", r.StatusCode, m)
	}
	id := m["id"].(string)
	body["enabled"] = true
	if r, m := h.call("POST", "/api/routines", body); r.StatusCode != 400 || errCode(m) != "confirmation_required" {
		t.Fatalf("%d %v", r.StatusCode, m)
	}
	if r, m := h.call("POST", "/api/routines/"+id+"/enable", nil); r.StatusCode != 400 || errCode(m) != "confirmation_required" {
		t.Fatalf("%d %v", r.StatusCode, m)
	}
	if r, _ := h.call("POST", "/api/routines/"+id+"/enable", map[string]any{"confirm_destructive": true}); r.StatusCode != 200 {
		t.Fatal(r.StatusCode)
	}
	// A real run needs confirmation; a dry run does not.
	if r, m := h.call("POST", "/api/routines/"+id+"/run", nil); r.StatusCode != 400 || errCode(m) != "confirmation_required" {
		t.Fatalf("%d %v", r.StatusCode, m)
	}
	if len(h.fake.modified) != 0 {
		t.Fatal("mail modified without confirmation")
	}
	r, m = h.call("POST", "/api/routines/"+id+"/run", map[string]any{"dry_run": true})
	if r.StatusCode != 202 {
		t.Fatalf("%d %v", r.StatusCode, m)
	}
	h.waitRun(m["id"].(string))
	if len(h.fake.modified) != 0 {
		t.Fatal("dry run modified mail")
	}
}

func TestPreviewEndpoints(t *testing.T) {
	h := newHarness(t, nil)
	h.login()
	r, m := h.call("POST", "/api/routines/preview", map[string]any{"account_id": h.acc.ID, "gmail_query": "label:newsletters"})
	if r.StatusCode != 200 || m["query"] != "label:newsletters" || m["count"] != float64(3) || m["count_is_estimate"] != false {
		t.Fatalf("%d %v", r.StatusCode, m)
	}
	msgs := m["messages"].([]any)
	first := msgs[0].(map[string]any)
	if len(msgs) != 3 || first["from"] != "sender@example.com" || first["subject"] != "Subj m1" || first["id"] != "m1" {
		t.Fatalf("%v", msgs)
	}
	if len(h.fake.modified) != 0 {
		t.Fatal("preview modified mail")
	}
	// Saved routine preview.
	_, rt := h.call("POST", "/api/routines", routineBody(h.acc.ID))
	if r, m := h.call("POST", "/api/routines/"+rt["id"].(string)+"/preview", nil); r.StatusCode != 200 || m["count"] != float64(3) {
		t.Fatalf("%d %v", r.StatusCode, m)
	}
	// Errors.
	if r, m := h.call("POST", "/api/routines/preview", map[string]any{"account_id": h.acc.ID, "gmail_query": "bad:::query"}); r.StatusCode != 400 || errCode(m) != "invalid_query" {
		t.Fatalf("%d %v", r.StatusCode, m)
	}
	if r, _ := h.call("POST", "/api/routines/preview", map[string]any{"account_id": h.acc.ID}); r.StatusCode != 400 {
		t.Fatal("empty query accepted")
	}
	if r, _ := h.call("POST", "/api/routines/preview", map[string]any{"account_id": "nope", "gmail_query": "x"}); r.StatusCode != 400 {
		t.Fatal("unknown account accepted")
	}
}

func TestRunNowDryRunAndRealRunAndDashboard(t *testing.T) {
	h := newHarness(t, nil)
	h.login()
	_, rt := h.call("POST", "/api/routines", routineBody(h.acc.ID))
	id := rt["id"].(string)

	r, m := h.call("POST", "/api/routines/"+id+"/run", map[string]any{"dry_run": true})
	if r.StatusCode != 202 || m["dry_run"] != true {
		t.Fatalf("%d %v", r.StatusCode, m)
	}
	done := h.waitRun(m["id"].(string))
	run := done["run"].(map[string]any)
	if run["status"] != "success" || run["matched_count"] != float64(3) || run["processed_count"] != float64(0) || len(h.fake.modified) != 0 {
		t.Fatalf("%v", run)
	}

	r, m = h.call("POST", "/api/routines/"+id+"/run", nil)
	if r.StatusCode != 202 {
		t.Fatalf("%d %v", r.StatusCode, m)
	}
	done = h.waitRun(m["id"].(string))
	run = done["run"].(map[string]any)
	if run["status"] != "success" || run["processed_count"] != float64(3) || run["routine_name"] != "Archive news" {
		t.Fatalf("%v", run)
	}
	// Archive of the 3 messages, then the (default-on) traceability label on the same 3.
	if len(h.fake.modified) != 2 || len(h.fake.modified[0]) != 3 || len(h.fake.modified[1]) != 3 {
		t.Fatalf("gmail calls: %v", h.fake.modified)
	}
	if acts := done["actions"].([]any); len(acts) != 3 {
		t.Fatalf("%v", acts)
	}

	_, d := h.call("GET", "/api/dashboard", nil)
	if d["total_runs"] != float64(1) || d["emails_handled"] != float64(3) || d["connected_accounts"] != float64(1) || d["last_run"] == nil ||
		len(d["recent_runs"].([]any)) != 2 {
		t.Fatalf("%v", d)
	}

	// Runs listing with filters and pagination.
	_, l := h.call("GET", "/api/runs?limit=1", nil)
	if l["total"] != float64(2) || len(l["items"].([]any)) != 1 {
		t.Fatalf("%v", l)
	}
	_, l = h.call("GET", "/api/runs?status=success&routine_id="+id, nil)
	if l["total"] != float64(2) {
		t.Fatalf("%v", l)
	}
	if r, _ := h.call("GET", "/api/runs?status=bogus", nil); r.StatusCode != 400 {
		t.Fatal("bad filter accepted")
	}
	if r, _ := h.call("GET", "/api/runs/missing", nil); r.StatusCode != 404 {
		t.Fatal("missing run")
	}
}

func TestRunConflictsWhileRunning(t *testing.T) {
	h := newHarness(t, nil)
	h.fake.gate = make(chan struct{})
	h.fake.started = make(chan struct{}, 1)
	h.login()
	_, rt := h.call("POST", "/api/routines", routineBody(h.acc.ID))
	id := rt["id"].(string)
	r, m := h.call("POST", "/api/routines/"+id+"/run", nil)
	if r.StatusCode != 202 {
		t.Fatal(r.StatusCode)
	}
	<-h.fake.started
	if r, m := h.call("POST", "/api/routines/"+id+"/run", nil); r.StatusCode != 409 || errCode(m) != "routine_running" {
		t.Fatalf("%d %v", r.StatusCode, m)
	}
	if r, _ := h.call("DELETE", "/api/routines/"+id, nil); r.StatusCode != 409 {
		t.Fatalf("deleting a running routine: %d", r.StatusCode)
	}
	_, got := h.call("GET", "/api/routines/"+id, nil)
	if got["running"] != true {
		t.Fatal("running flag missing")
	}
	close(h.fake.gate)
	h.waitRun(m["id"].(string))
}

func TestGoogleOAuthFlow(t *testing.T) {
	h := newHarness(t, nil)
	h.login()
	// Connect redirects to Google with state + PKCE and sets short-lived cookies.
	resp, _ := h.req("GET", "/api/accounts/google/connect", nil)
	if resp.StatusCode != 302 {
		t.Fatalf("%d", resp.StatusCode)
	}
	loc, _ := url.Parse(resp.Header.Get("Location"))
	state := loc.Query().Get("state")
	if state == "" || loc.Query().Get("code_challenge") == "" || loc.Query().Get("access_type") != "offline" {
		t.Fatalf("%s", loc)
	}

	// Wrong state is rejected and no account is created.
	before, _ := h.store.ListAccounts(context.Background())
	resp, _ = h.req("GET", "/api/accounts/google/callback?code=abc&state=forged", nil)
	if resp.StatusCode != 302 || !strings.Contains(resp.Header.Get("Location"), "oauth_error=state_mismatch") {
		t.Fatalf("%d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	after, _ := h.store.ListAccounts(context.Background())
	if len(after) != len(before) {
		t.Fatal("account created despite bad state")
	}
	// State cookies are single-use: even the right state now fails.
	resp, _ = h.req("GET", "/api/accounts/google/callback?code=abc&state="+state, nil)
	if !strings.Contains(resp.Header.Get("Location"), "state_mismatch") {
		t.Fatalf("state reusable: %s", resp.Header.Get("Location"))
	}

	// Full happy path.
	resp, _ = h.req("GET", "/api/accounts/google/connect", nil)
	loc, _ = url.Parse(resp.Header.Get("Location"))
	state = loc.Query().Get("state")
	resp, _ = h.req("GET", "/api/accounts/google/callback?code=goodcode&state="+state, nil)
	if resp.StatusCode != 302 || resp.Header.Get("Location") != "/settings?connected=1" {
		t.Fatalf("%d %s", resp.StatusCode, resp.Header.Get("Location"))
	}
	accs, _ := h.store.ListAccounts(context.Background())
	var found *models.Account
	for i := range accs {
		if accs[i].Email == "connected@gmail.com" {
			found = &accs[i]
		}
	}
	if found == nil || found.ConnectionStatus != models.StatusConnected || strings.Contains(found.EncryptedRefreshToken, "new-refresh") {
		t.Fatalf("%+v", found)
	}
	// API never exposes tokens.
	_, data := h.req("GET", "/api/accounts", nil)
	if strings.Contains(string(data), "refresh") || strings.Contains(string(data), "v1:") {
		t.Fatalf("token material in API: %s", data)
	}
	// User denied consent.
	h.req("GET", "/api/accounts/google/connect", nil)
	resp, _ = h.req("GET", "/api/accounts/google/callback?error=access_denied", nil)
	if !strings.Contains(resp.Header.Get("Location"), "oauth_error=access_denied") {
		t.Fatal(resp.Header.Get("Location"))
	}
	// Callback without a session bounces to login.
	anon := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	r, _ := anon.Get(h.srv.URL + "/api/accounts/google/callback?code=x&state=y")
	if r.StatusCode != 302 || r.Header.Get("Location") != "/" {
		t.Fatal("unauthenticated callback not redirected")
	}

	// Labels and disconnect.
	if r, m := h.call("GET", "/api/accounts/"+found.ID+"/labels", nil); r.StatusCode != 200 || len(m["items"].([]any)) != 2 {
		t.Fatalf("%d %v", r.StatusCode, m)
	}
	if r, _ := h.call("DELETE", "/api/accounts/"+found.ID, nil); r.StatusCode != 204 {
		t.Fatal(r.StatusCode)
	}
	if r, _ := h.call("DELETE", "/api/accounts/"+found.ID, nil); r.StatusCode != 404 {
		t.Fatal(r.StatusCode)
	}
}

func TestNeedsReauthAccountBlocksEnable(t *testing.T) {
	h := newHarness(t, nil)
	h.login()
	_, rt := h.call("POST", "/api/routines", routineBody(h.acc.ID))
	h.store.SetAccountStatus(context.Background(), h.acc.ID, models.StatusNeedsReauth)
	if r, m := h.call("POST", "/api/routines/"+rt["id"].(string)+"/enable", nil); r.StatusCode != 409 || errCode(m) != "account_needs_reauth" {
		t.Fatalf("%d %v", r.StatusCode, m)
	}
	if r, m := h.call("POST", "/api/routines/preview", map[string]any{"account_id": h.acc.ID, "gmail_query": "x"}); r.StatusCode != 409 {
		t.Fatalf("%d %v", r.StatusCode, m)
	}
}

func TestSettings(t *testing.T) {
	h := newHarness(t, map[string]string{"TZ": "Europe/Berlin"})
	h.login()
	_, m := h.call("GET", "/api/settings", nil)
	if m["timezone"] != "Europe/Berlin" || m["license"] != "Apache-2.0" || m["version"] != "test" {
		t.Fatalf("%v", m)
	}
	if r, _ := h.call("PUT", "/api/settings", map[string]any{"timezone": "Mars/Base"}); r.StatusCode != 400 {
		t.Fatal("bad timezone accepted")
	}
	r, m := h.call("PUT", "/api/settings", map[string]any{"timezone": "America/New_York"})
	if r.StatusCode != 200 || m["timezone"] != "America/New_York" {
		t.Fatalf("%d %v", r.StatusCode, m)
	}
	// New routines inherit the timezone when none is given.
	body := routineBody(h.acc.ID)
	body["schedule_config"] = map[string]any{"time": "03:00"}
	_, rt := h.call("POST", "/api/routines", body)
	if rt["schedule_config"].(map[string]any)["timezone"] != "America/New_York" {
		t.Fatalf("%v", rt["schedule_config"])
	}
}

func TestStaticUIAndSecurityHeaders(t *testing.T) {
	h := newHarness(t, nil)
	resp, data := h.req("GET", "/routines/abc", nil)
	if resp.StatusCode != 200 || !strings.Contains(string(data), "spa") {
		t.Fatalf("SPA fallback: %d %s", resp.StatusCode, data)
	}
	if resp.Header.Get("Cache-Control") != "no-cache" || resp.Header.Get("X-Frame-Options") != "DENY" ||
		!strings.Contains(resp.Header.Get("Content-Security-Policy"), "default-src 'self'") || resp.Header.Get("X-Content-Type-Options") != "nosniff" {
		t.Fatalf("%v", resp.Header)
	}
	resp, _ = h.req("GET", "/assets/app.js", nil)
	if resp.StatusCode != 200 || !strings.Contains(resp.Header.Get("Cache-Control"), "immutable") {
		t.Fatal("asset caching")
	}
	if resp, _ = h.req("GET", "/assets/missing.js", nil); resp.StatusCode != 404 {
		t.Fatal("missing asset should 404, not fall back to index.html")
	}
}

func TestTrustedProxyClientIP(t *testing.T) {
	// Behind a trusted proxy, throttling keys on X-Forwarded-For, not on the proxy address.
	h := newHarness(t, map[string]string{"TRUSTED_PROXIES": "127.0.0.1/32"})
	bad := map[string]string{"username": "admin", "password": "bad"}
	for i := 0; i < 5; i++ {
		h.req("POST", "/api/auth/login", bad, "X-Forwarded-For", "203.0.113.9")
	}
	if r, _ := h.req("POST", "/api/auth/login", bad, "X-Forwarded-For", "203.0.113.9"); r.StatusCode != 429 {
		t.Fatalf("attacker not throttled: %d", r.StatusCode)
	}
	if r, _ := h.req("POST", "/api/auth/login", map[string]string{"username": "admin", "password": "hunter2-hunter2"}, "X-Forwarded-For", "198.51.100.7"); r.StatusCode != 200 {
		t.Fatalf("other client blocked: %d", r.StatusCode)
	}
	// Without trusted proxies the header is ignored (cannot be used to dodge the limiter).
	h2 := newHarness(t, nil)
	for i := 0; i < 5; i++ {
		h2.req("POST", "/api/auth/login", bad, "X-Forwarded-For", fmt.Sprintf("10.0.0.%d", i))
	}
	if r, _ := h2.req("POST", "/api/auth/login", bad, "X-Forwarded-For", "10.9.9.9"); r.StatusCode != 429 {
		t.Fatalf("spoofed XFF evaded throttle: %d", r.StatusCode)
	}
}
