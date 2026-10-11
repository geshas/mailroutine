package httpapi_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"github.com/geshas/mailroutine/internal/sqlite"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"
	"time"

	"github.com/geshas/mailroutine/internal/auth"
	httpapi "github.com/geshas/mailroutine/internal/http"
	"github.com/geshas/mailroutine/pkg/models"

	_ "modernc.org/sqlite"
)

// breakTable drops a table through a second connection so one subsystem fails
// while sessions keep working, exercising the handlers' 500 paths.
func (h *harness) breakTable(names ...string) {
	h.t.Helper()
	db, err := sql.Open("sqlite", "file:"+h.dbPath+"?_pragma=busy_timeout(5000)")
	if err != nil {
		h.t.Fatal(err)
	}
	defer db.Close()
	for _, n := range names {
		if _, err := db.Exec("DROP TABLE " + n); err != nil {
			h.t.Fatal(err)
		}
	}
}

func (h *harness) mustStatus(method, path string, body any, want int) map[string]any {
	h.t.Helper()
	r, m := h.call(method, path, body)
	if r.StatusCode != want {
		h.t.Fatalf("%s %s: got %d want %d (%v)", method, path, r.StatusCode, want, m)
	}
	return m
}

func TestDatabaseFailuresReturn500NotPanics(t *testing.T) {
	t.Run("executions", func(t *testing.T) {
		h := newHarness(t, nil)
		h.login()
		h.breakTable("message_actions", "executions")
		h.mustStatus("GET", "/api/dashboard", nil, 500)
		h.mustStatus("GET", "/api/runs", nil, 500)
		h.mustStatus("GET", "/api/runs/abc", nil, 500)
	})
	t.Run("message_actions", func(t *testing.T) {
		h := newHarness(t, nil)
		h.login()
		_, rt := h.call("POST", "/api/routines", routineBody(h.acc.ID))
		ex, _ := h.store.CreateExecution(context.Background(), rt["id"].(string), models.TriggerManual, false, time.Now())
		h.breakTable("message_actions")
		h.mustStatus("GET", "/api/runs/"+ex.ID, nil, 500)
	})
	t.Run("settings", func(t *testing.T) {
		h := newHarness(t, nil)
		h.login()
		h.breakTable("settings")
		h.mustStatus("PUT", "/api/settings", map[string]any{"timezone": "UTC"}, 500)
		// reads fall back to the configured timezone
		if m := h.mustStatus("GET", "/api/settings", nil, 200); m["timezone"] != "UTC" {
			t.Fatal(m)
		}
	})
	t.Run("accounts", func(t *testing.T) {
		h := newHarness(t, nil)
		h.login()
		h.breakTable("routines", "accounts")
		h.mustStatus("GET", "/api/accounts", nil, 500)
		h.mustStatus("DELETE", "/api/accounts/"+h.acc.ID, nil, 500)
	})
	t.Run("routines", func(t *testing.T) {
		h := newHarness(t, nil)
		h.login()
		_, rt := h.call("POST", "/api/routines", routineBody(h.acc.ID))
		id := rt["id"].(string)
		h.breakTable("message_actions", "executions", "routines")
		h.mustStatus("GET", "/api/routines", nil, 500)
		h.mustStatus("GET", "/api/routines/"+id, nil, 500)
		h.mustStatus("POST", "/api/routines", routineBody(h.acc.ID), 500)
		h.mustStatus("PUT", "/api/routines/"+id, routineBody(h.acc.ID), 500)
		h.mustStatus("POST", "/api/routines/"+id+"/enable", nil, 500)
		h.mustStatus("POST", "/api/routines/"+id+"/disable", nil, 500)
		h.mustStatus("POST", "/api/routines/"+id+"/run", nil, 500)
		h.mustStatus("POST", "/api/routines/"+id+"/preview", nil, 500)
		h.mustStatus("DELETE", "/api/routines/"+id, nil, 500)
		if r, _ := h.req("GET", "/ready", nil); r.StatusCode != 200 {
			t.Fatal("ready only checks connectivity")
		}
	})
	t.Run("sessions", func(t *testing.T) {
		h := newHarness(t, nil)
		h.login()
		h.breakTable("sessions")
		h.mustStatus("GET", "/api/dashboard", nil, 500)
		if r, _ := h.req("POST", "/api/auth/login", map[string]string{"username": "admin", "password": "hunter2-hunter2"}); r.StatusCode != 500 {
			t.Fatalf("login with broken session store: %d", r.StatusCode)
		}
	})
}

func TestReadyReportsDatabaseDown(t *testing.T) {
	h := newHarness(t, nil)
	h.store.Close()
	if r, _ := h.req("GET", "/ready", nil); r.StatusCode != 503 {
		t.Fatal(r.StatusCode)
	}
}

func TestUnknownIDsAre404(t *testing.T) {
	h := newHarness(t, nil)
	h.login()
	for _, ep := range [][2]string{
		{"PUT", "/api/routines/nope"}, {"DELETE", "/api/routines/nope"}, {"POST", "/api/routines/nope/enable"},
		{"POST", "/api/routines/nope/disable"}, {"POST", "/api/routines/nope/run"}, {"POST", "/api/routines/nope/preview"},
		{"DELETE", "/api/accounts/nope"}, {"GET", "/api/accounts/nope/labels"},
	} {
		body := any(nil)
		if ep[0] == "PUT" {
			body = routineBody(h.acc.ID)
		}
		if r, _ := h.call(ep[0], ep[1], body); r.StatusCode != 404 {
			t.Errorf("%s %s: %d", ep[0], ep[1], r.StatusCode)
		}
	}
}

func TestMalformedBodies(t *testing.T) {
	h := newHarness(t, nil)
	h.login()
	_, rt := h.call("POST", "/api/routines", routineBody(h.acc.ID))
	id := rt["id"].(string)
	for _, ep := range [][2]string{
		{"POST", "/api/routines"}, {"PUT", "/api/routines/" + id}, {"POST", "/api/routines/" + id + "/enable"},
		{"POST", "/api/routines/" + id + "/run"}, {"POST", "/api/routines/" + id + "/preview"},
		{"POST", "/api/routines/preview"}, {"PUT", "/api/settings"},
	} {
		r, _ := h.req(ep[0], ep[1], map[string]any{"definitely_unknown": true}, "X-CSRF-Token", h.csrf)
		if r.StatusCode != 400 {
			t.Errorf("%s %s: %d", ep[0], ep[1], r.StatusCode)
		}
	}
	// Invalid JSON on login.
	req, _ := http.NewRequest("POST", h.srv.URL+"/api/auth/login", strings.NewReader("{oops"))
	req.Header.Set("Content-Type", "application/json")
	if r, _ := h.client.Do(req); r.StatusCode != 400 {
		t.Fatal(r.StatusCode)
	}
	// Empty settings update is a no-op that returns current settings.
	h.mustStatus("PUT", "/api/settings", map[string]any{}, 200)
}

func TestRoutineDefaultsAndUpdateValidation(t *testing.T) {
	h := newHarness(t, map[string]string{"DEFAULT_MAX_MESSAGES_PER_RUN": "321"})
	h.login()
	body := routineBody(h.acc.ID)
	delete(body, "max_messages_per_run")
	m := h.mustStatus("POST", "/api/routines", body, 201)
	if m["max_messages_per_run"] != float64(321) {
		t.Fatalf("default cap not applied: %v", m["max_messages_per_run"])
	}
	// Stray label config is dropped for actions that take none.
	body["action_config"] = map[string]any{"label": "ignored"}
	m = h.mustStatus("POST", "/api/routines", body, 201)
	if cfg := m["action_config"].(map[string]any); cfg["label"] != nil && cfg["label"] != "" {
		t.Fatalf("%v", cfg)
	}
	// Update validation errors do not modify the routine.
	id := m["id"].(string)
	bad := routineBody(h.acc.ID)
	bad["name"] = ""
	h.mustStatus("PUT", "/api/routines/"+id, bad, 400)
	// Enabling for a needs-reauth account is refused on create too.
	h.store.SetAccountStatus(context.Background(), h.acc.ID, models.StatusNeedsReauth)
	en := routineBody(h.acc.ID)
	en["enabled"] = true
	if m := h.mustStatus("POST", "/api/routines", en, 409); errCode(m) != "account_needs_reauth" {
		t.Fatal(m)
	}
	// Preview with an over-long query.
	h.store.SetAccountStatus(context.Background(), h.acc.ID, models.StatusConnected)
	h.mustStatus("POST", "/api/routines/preview", map[string]any{"account_id": h.acc.ID, "gmail_query": strings.Repeat("a", 2001)}, 400)
}

func TestRunsAtCapacityReturn429(t *testing.T) {
	h := newHarness(t, nil)
	h.fake.gate = make(chan struct{})
	h.fake.started = make(chan struct{}, 4)
	h.login()
	var ids []string
	for i := 0; i < 3; i++ {
		_, rt := h.call("POST", "/api/routines", routineBody(h.acc.ID))
		ids = append(ids, rt["id"].(string))
	}
	var runIDs []string
	for _, id := range ids[:2] {
		m := h.mustStatus("POST", "/api/routines/"+id+"/run", nil, 202)
		runIDs = append(runIDs, m["id"].(string))
	}
	<-h.fake.started
	<-h.fake.started
	if m := h.mustStatus("POST", "/api/routines/"+ids[2]+"/run", nil, 429); errCode(m) != "at_capacity" {
		t.Fatal(m)
	}
	close(h.fake.gate)
	for _, id := range runIDs {
		h.waitRun(id)
	}
}

func TestGmailFailuresMapToAPIErrors(t *testing.T) {
	h := newHarness(t, nil)
	h.login()
	preview := map[string]any{"account_id": h.acc.ID, "gmail_query": "label:x"}
	cases := []struct {
		status int
		want   int
		code   string
	}{{401, 409, "account_needs_reauth"}, {429, 503, "gmail_unavailable"}, {500, 503, "gmail_unavailable"}, {404, 502, "gmail_error"}}
	for _, c := range cases {
		h.store.SetAccountStatus(context.Background(), h.acc.ID, models.StatusConnected)
		h.fake.set(func() { h.fake.listStatus = c.status })
		// Retries make failures slow; the engine inside the test harness uses one attempt.
		m := h.mustStatus("POST", "/api/routines/preview", preview, c.want)
		if errCode(m) != c.code {
			t.Errorf("gmail %d: %v", c.status, m)
		}
	}
	// Revoked authorization during preview flags the account.
	h.store.SetAccountStatus(context.Background(), h.acc.ID, models.StatusConnected)
	h.fake.set(func() { h.fake.listStatus = 401 })
	h.call("POST", "/api/routines/preview", preview)
	if a, _ := h.store.GetAccount(context.Background(), h.acc.ID); a.ConnectionStatus != models.StatusNeedsReauth {
		t.Fatal("account not flagged after revoked authorization")
	}
	// Labels: unknown account, reauth, and upstream failures.
	h.fake.set(func() { h.fake.listStatus = 0 })
	h.mustStatus("GET", "/api/accounts/"+h.acc.ID+"/labels", nil, 409)
	h.store.SetAccountStatus(context.Background(), h.acc.ID, models.StatusConnected)
	h.fake.set(func() { h.fake.labelsStatus = 401 })
	h.mustStatus("GET", "/api/accounts/"+h.acc.ID+"/labels", nil, 409)
	h.fake.set(func() { h.fake.labelsStatus = 503 })
	h.mustStatus("GET", "/api/accounts/"+h.acc.ID+"/labels", nil, 503)
}

func TestOAuthFailureRedirects(t *testing.T) {
	cases := []struct {
		name   string
		inject func(*fakeGoogle)
		want   string
	}{
		{"exchange rejected", func(f *fakeGoogle) { f.tokenFail = true }, "exchange_failed"},
		{"no refresh token", func(f *fakeGoogle) { f.noRefresh = true }, "no_refresh_token"},
		{"profile unreadable", func(f *fakeGoogle) { f.profileFail = true }, "profile_failed"},
		{"gmail permission unticked", func(f *fakeGoogle) { s := "openid email"; f.scopeOverride = &s }, "insufficient_scope"},
		{"scope missing from response", func(f *fakeGoogle) { s := ""; f.scopeOverride = &s }, "insufficient_scope"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			h := newHarness(t, nil)
			h.login()
			h.fake.set(func() { c.inject(h.fake) })
			resp, _ := h.req("GET", "/api/accounts/google/connect", nil)
			state := strings.Split(strings.Split(resp.Header.Get("Location"), "state=")[1], "&")[0]
			resp, _ = h.req("GET", "/api/accounts/google/callback?code=c&state="+state, nil)
			if !strings.Contains(resp.Header.Get("Location"), "oauth_error="+c.want) {
				t.Fatalf("%s", resp.Header.Get("Location"))
			}
			if accs, _ := h.store.ListAccounts(context.Background()); len(accs) != 1 {
				t.Fatalf("account created on failure: %d", len(accs))
			}
		})
	}
	// Hostile error values are reduced to a safe code.
	h := newHarness(t, nil)
	h.login()
	h.req("GET", "/api/accounts/google/connect", nil)
	resp, _ := h.req("GET", "/api/accounts/google/callback?error=%3Cscript%3E", nil)
	if loc := resp.Header.Get("Location"); strings.Contains(loc, "<") || !strings.Contains(loc, "oauth_error=script") {
		t.Fatal(loc)
	}
	resp, _ = h.req("GET", "/api/accounts/google/callback?error=%21%21%21", nil)
	if !strings.Contains(resp.Header.Get("Location"), "oauth_error=unknown") {
		t.Fatal(resp.Header.Get("Location"))
	}
}

func TestDeletingAccountBlockedWhileRoutineRuns(t *testing.T) {
	h := newHarness(t, nil)
	h.fake.gate = make(chan struct{})
	h.fake.started = make(chan struct{}, 1)
	h.login()
	_, rt := h.call("POST", "/api/routines", routineBody(h.acc.ID))
	m := h.mustStatus("POST", "/api/routines/"+rt["id"].(string)+"/run", nil, 202)
	<-h.fake.started
	if r := h.mustStatus("DELETE", "/api/accounts/"+h.acc.ID, nil, 409); errCode(r) != "routine_running" {
		t.Fatal(r)
	}
	close(h.fake.gate)
	h.waitRun(m["id"].(string))
}

func TestLoginRotatesExistingSession(t *testing.T) {
	h := newHarness(t, nil)
	h.login()
	before, _ := h.store.CountSessions(context.Background())
	h.login() // presents the old cookie while signing in again
	after, _ := h.store.CountSessions(context.Background())
	if before != 1 || after != 1 {
		t.Fatalf("old session not replaced: %d -> %d", before, after)
	}
}

func TestRenewRaceWithExpiry(t *testing.T) {
	h := newHarness(t, nil)
	h.login()
	var calls atomic.Int32
	t0 := time.Now()
	h.auth.Now = func() time.Time {
		if calls.Add(1) == 1 {
			return t0
		}
		return t0.Add(31 * time.Minute) // session expires between validation and renewal
	}
	r, m := h.call("POST", "/api/auth/session/renew", nil)
	if r.StatusCode != 401 || errCode(m) != "session_expired" {
		t.Fatalf("%d %v", r.StatusCode, m)
	}
}

func TestOriginParsing(t *testing.T) {
	h := newHarness(t, nil)
	h.login()
	for _, origin := range []string{"http://[::1", "null", "http://localhost:9999", "https://localhost:8080"} {
		r, _ := h.req("POST", "/api/routines", routineBody(h.acc.ID), "X-CSRF-Token", h.csrf, "Origin", origin)
		if r.StatusCode != 403 {
			t.Errorf("Origin %q accepted", origin)
		}
	}
	if r, _ := h.req("POST", "/api/routines", routineBody(h.acc.ID), "X-CSRF-Token", h.csrf, "Origin", "HTTP://LOCALHOST:8080"); r.StatusCode != 201 {
		t.Fatalf("origin comparison must be case-insensitive: %d", r.StatusCode)
	}
}

func TestStaticEdgeCases(t *testing.T) {
	h := newHarness(t, nil)
	// Methods other than GET/HEAD on non-API paths.
	if r, _ := h.req("POST", "/some/page", nil); r.StatusCode != 405 {
		t.Fatalf("%d", r.StatusCode)
	}
	// A directory path falls back to the SPA shell.
	if r, body := h.req("GET", "/assets", nil); r.StatusCode != 200 || !strings.Contains(string(body), "spa") {
		t.Fatalf("%d", r.StatusCode)
	}
	if r, body := h.req("GET", "/", nil); r.StatusCode != 200 || !strings.Contains(string(body), "spa") {
		t.Fatal("root")
	}
	// No UI compiled in.
	deps := newBareServer(t, nil)
	if r, _ := deps.req("GET", "/", nil); r.StatusCode != 404 {
		t.Fatal("nil Static must 404")
	}
	// UI directory without index.html.
	empty := newBareServer(t, fstest.MapFS{"readme.txt": {Data: []byte("x")}})
	if r, _ := empty.req("GET", "/dashboard", nil); r.StatusCode != 404 {
		t.Fatal("missing index.html must 404")
	}
}

// newBareServer builds a server with an arbitrary static FS, reusing the harness wiring.
func newBareServer(t *testing.T, static fstest.MapFS) *harness {
	t.Helper()
	h := newHarness(t, nil)
	deps := httpapi.Deps{Config: h.cfg, Store: h.store, Auth: h.auth, Limiter: auth.NewLoginLimiter(5, 50, time.Minute),
		Runner: h.runner, Logger: discardLogger(), Version: "test"}
	if static != nil {
		deps.Static = static
	}
	h.srv.Config.Handler = httpapi.New(deps).Handler()
	return h
}

func discardLogger() *slog.Logger { return slog.New(slog.DiscardHandler) }

func TestDashboardNextRunAndLastRunHandled(t *testing.T) {
	h := newHarness(t, nil)
	h.login()
	m := h.mustStatus("GET", "/api/dashboard", nil, 200)
	if m["next_run"] != nil || m["last_run"] != nil || m["total_runs"] != float64(0) || m["emails_handled"] != float64(0) {
		t.Fatalf("an empty installation must report zeros, not placeholders: %v", m)
	}
	body := routineBody(h.acc.ID)
	body["enabled"] = true
	_, rt := h.call("POST", "/api/routines", body)
	id := rt["id"].(string)
	if rt["last_run_handled"] != nil {
		t.Fatal("never-run routine must not claim a handled count")
	}
	m = h.mustStatus("GET", "/api/dashboard", nil, 200)
	nr, ok := m["next_run"].(map[string]any)
	if !ok || nr["routine_id"] != id || nr["routine_name"] != "Archive news" || nr["at"] == nil {
		t.Fatalf("%v", m["next_run"])
	}
	run := h.mustStatus("POST", "/api/routines/"+id+"/run", nil, 202)
	h.waitRun(run["id"].(string))
	_, got := h.call("GET", "/api/routines/"+id, nil)
	if got["last_run_handled"] != float64(3) {
		t.Fatalf("%v", got["last_run_handled"])
	}
	list := h.mustStatus("GET", "/api/routines", nil, 200)
	if list["items"].([]any)[0].(map[string]any)["last_run_handled"] != float64(3) {
		t.Fatal("list must carry the handled count too")
	}
}

func TestRunsDateRangeFilter(t *testing.T) {
	h := newHarness(t, nil)
	h.login()
	_, rt := h.call("POST", "/api/routines", routineBody(h.acc.ID))
	old, _ := h.store.CreateExecution(context.Background(), rt["id"].(string), models.TriggerSchedule, false, time.Now().Add(-72*time.Hour))
	h.store.FinishExecution(context.Background(), old.ID, models.ExecSuccess, 1, 1, 0, "", time.Now().Add(-72*time.Hour))
	recent, _ := h.store.CreateExecution(context.Background(), rt["id"].(string), models.TriggerSchedule, false, time.Now().Add(-time.Hour))
	h.store.FinishExecution(context.Background(), recent.ID, models.ExecSuccess, 1, 1, 0, "", time.Now())

	from := url.QueryEscape(time.Now().Add(-24 * time.Hour).UTC().Format(time.RFC3339))
	m := h.mustStatus("GET", "/api/runs?from="+from, nil, 200)
	if m["total"] != float64(1) || m["items"].([]any)[0].(map[string]any)["id"] != recent.ID {
		t.Fatalf("%v", m)
	}
	to := url.QueryEscape(time.Now().Add(-48 * time.Hour).UTC().Format(time.RFC3339))
	if m := h.mustStatus("GET", "/api/runs?to="+to, nil, 200); m["total"] != float64(1) {
		t.Fatalf("%v", m)
	}
	for _, bad := range []string{"from=yesterday", "to=2026-13-45"} {
		if m := h.mustStatus("GET", "/api/runs?"+bad, nil, 400); errCode(m) != "invalid_filter" {
			t.Fatal(m)
		}
	}
}

func TestChangingTimezoneRetargetsRoutines(t *testing.T) {
	h := newHarness(t, nil)
	h.login()
	body := routineBody(h.acc.ID)
	body["enabled"] = true
	_, rt := h.call("POST", "/api/routines", body) // timezone UTC
	id := rt["id"].(string)
	before := rt["next_run_at"].(string)
	h.mustStatus("PUT", "/api/settings", map[string]any{"timezone": "Asia/Tokyo"}, 200)
	_, got := h.call("GET", "/api/routines/"+id, nil)
	if got["schedule_config"].(map[string]any)["timezone"] != "Asia/Tokyo" {
		t.Fatalf("%v", got["schedule_config"])
	}
	if got["next_run_at"].(string) == before {
		t.Fatal("next run must be recomputed for the new timezone")
	}
	next, _ := time.Parse(time.RFC3339Nano, got["next_run_at"].(string))
	if tokyo, _ := time.LoadLocation("Asia/Tokyo"); next.In(tokyo).Hour() != 3 {
		t.Fatalf("03:00 must now mean Tokyo time: %v", next.In(tokyo))
	}
	// A disabled routine follows too, without gaining a next run.
	_, off := h.call("POST", "/api/routines", routineBody(h.acc.ID))
	h.mustStatus("PUT", "/api/settings", map[string]any{"timezone": "Europe/Paris"}, 200)
	_, off = h.call("GET", "/api/routines/"+off["id"].(string), nil)
	if off["schedule_config"].(map[string]any)["timezone"] != "Europe/Paris" || off["next_run_at"] != nil {
		t.Fatalf("%v", off)
	}
}

func TestConnectCanReturnJSONForSinglePageClients(t *testing.T) {
	h := newHarness(t, nil)
	h.login()
	req, _ := http.NewRequest("GET", h.srv.URL+"/api/accounts/google/connect", nil)
	req.Header.Set("Accept", "application/json")
	resp, err := h.client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var m map[string]string
	json.NewDecoder(resp.Body).Decode(&m)
	if resp.StatusCode != 200 || !strings.Contains(m["url"], "/auth?") && !strings.Contains(m["url"], "state=") {
		t.Fatalf("%d %v", resp.StatusCode, m)
	}
	var state, verifier bool
	for _, c := range resp.Cookies() {
		state = state || c.Name == "mailroutine_oauth_state" && c.HttpOnly
		verifier = verifier || c.Name == "mailroutine_oauth_verifier" && c.HttpOnly
	}
	if !state || !verifier {
		t.Fatal("OAuth state cookies must be set in JSON mode too")
	}
	// The returned URL completes a normal callback.
	u, _ := url.Parse(m["url"])
	resp2, _ := h.req("GET", "/api/accounts/google/callback?code=c&state="+u.Query().Get("state"), nil)
	if resp2.Header.Get("Location") != "/settings?connected=1" {
		t.Fatal(resp2.Header.Get("Location"))
	}
}

func sqliteFilter() sqlite.ExecutionFilter { return sqlite.ExecutionFilter{Limit: 5} }

func TestSecurityHeadersAndNoVersionLeak(t *testing.T) {
	h := newHarness(t, nil)
	resp, body := h.req("GET", "/health", nil)
	if strings.Contains(string(body), "version") || strings.Contains(string(body), "test") {
		t.Fatalf("unauthenticated /health must not reveal the version: %s", body)
	}
	for k, want := range map[string]string{
		"Cross-Origin-Resource-Policy": "same-origin", "Referrer-Policy": "no-referrer", "X-Content-Type-Options": "nosniff",
	} {
		if resp.Header.Get(k) != want {
			t.Errorf("%s = %q", k, resp.Header.Get(k))
		}
	}
	if !strings.Contains(resp.Header.Get("Permissions-Policy"), "camera=()") {
		t.Error("Permissions-Policy missing")
	}
}

func traceItems(m map[string]any) map[string]bool {
	out := map[string]bool{}
	for _, it := range m["items"].([]any) {
		x := it.(map[string]any)
		out[x["name"].(string)] = x["exists"].(bool)
	}
	return out
}

func TestMailRoutineLabelsStatusAndCreation(t *testing.T) {
	h := newHarness(t, nil)
	h.login()
	path := "/api/accounts/" + h.acc.ID + "/labels/mailroutine"

	// Nothing exists yet: seven labels (parent + six actions), all missing.
	m := h.mustStatus("GET", path, nil, 200)
	items := traceItems(m)
	if len(items) != 7 || m["created"] == nil || len(m["created"].([]any)) != 0 {
		t.Fatalf("%v", m)
	}
	for name, exists := range items {
		if exists {
			t.Fatalf("%s should not exist yet", name)
		}
	}
	for _, want := range []string{"MailRoutine", "MailRoutine/Archive", "MailRoutine/Trash", "MailRoutine/Read", "MailRoutine/Unread", "MailRoutine/AddLabel", "MailRoutine/RemoveLabel"} {
		if _, ok := items[want]; !ok {
			t.Errorf("missing %s in %v", want, items)
		}
	}

	// A user-made label with different case must be recognised, not duplicated.
	h.fake.set(func() { h.fake.userLabels = append(h.fake.userLabels, "mailroutine/archive") })
	if !traceItems(h.mustStatus("GET", path, nil, 200))["MailRoutine/Archive"] {
		t.Fatal("case-insensitive match failed")
	}

	m = h.mustStatus("POST", path, nil, 200)
	created := m["created"].([]any)
	if len(created) != 6 { // everything except the pre-existing Archive label
		t.Fatalf("created %v", created)
	}
	for _, e := range traceItems(m) {
		if !e {
			t.Fatalf("not all labels exist after creation: %v", m)
		}
	}
	// Idempotent: a second click creates nothing and calls Gmail's create endpoint zero times.
	before := h.fake.labelCreates
	m = h.mustStatus("POST", path, nil, 200)
	if len(m["created"].([]any)) != 0 || h.fake.labelCreates != before {
		t.Fatalf("second call must not create labels: %v", m)
	}
}

func TestMailRoutineLabelsErrors(t *testing.T) {
	h := newHarness(t, nil)
	h.login()
	h.mustStatus("GET", "/api/accounts/nope/labels/mailroutine", nil, 404)
	h.mustStatus("POST", "/api/accounts/nope/labels/mailroutine", nil, 404)
	h.fake.set(func() { h.fake.labelsStatus = 503 })
	h.mustStatus("GET", "/api/accounts/"+h.acc.ID+"/labels/mailroutine", nil, 503)
	h.mustStatus("POST", "/api/accounts/"+h.acc.ID+"/labels/mailroutine", nil, 503)
	h.fake.set(func() { h.fake.labelsStatus = 0 })
	h.store.SetAccountStatus(context.Background(), h.acc.ID, models.StatusNeedsReauth)
	h.mustStatus("POST", "/api/accounts/"+h.acc.ID+"/labels/mailroutine", nil, 409)
	// Mutations still need the CSRF token.
	if r, _ := h.req("POST", "/api/accounts/"+h.acc.ID+"/labels/mailroutine", nil); r.StatusCode != 403 {
		t.Fatalf("%d", r.StatusCode)
	}
}

func TestTraceabilityOnRoutines(t *testing.T) {
	h := newHarness(t, nil)
	h.login()
	// Omitted => on for new routines.
	m := h.mustStatus("POST", "/api/routines", routineBody(h.acc.ID), 201)
	if m["traceability"] != true {
		t.Fatalf("new routines must trace by default: %v", m["traceability"])
	}
	id := m["id"].(string)
	// Explicit off survives an update that does not mention it.
	off := routineBody(h.acc.ID)
	off["traceability"] = false
	m = h.mustStatus("PUT", "/api/routines/"+id, off, 200)
	if m["traceability"] != false {
		t.Fatal("explicit false ignored")
	}
	m = h.mustStatus("PUT", "/api/routines/"+id, routineBody(h.acc.ID), 200)
	if m["traceability"] != false {
		t.Fatal("update without the field must keep the stored value")
	}

	// A run with tracing adds MailRoutine/Archive to the handled messages (second batchModify).
	m = h.mustStatus("PUT", "/api/routines/"+id, map[string]any{
		"account_id": h.acc.ID, "name": "t", "gmail_query": "label:x", "action_type": "archive", "schedule_type": "manual", "traceability": true,
	}, 200)
	run := h.mustStatus("POST", "/api/routines/"+id+"/run", nil, 202)
	h.waitRun(run["id"].(string))
	h.fake.mu.Lock()
	bodies := h.fake.modifyBodies
	h.fake.mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("expected the archive and the trace call, got %v", bodies)
	}
	add, _ := bodies[1]["addLabelIds"].([]any)
	if len(add) != 1 || add[0] != "Label_MailRoutine/Archive" {
		t.Fatalf("trace call: %v", bodies[1])
	}
	h.fake.mu.Lock()
	created := h.fake.userLabels
	h.fake.mu.Unlock()
	hasParent := false
	for _, n := range created {
		hasParent = hasParent || n == "MailRoutine"
	}
	if !hasParent {
		t.Fatalf("parent label must be created so Gmail shows the hierarchy: %v", created)
	}

	// With tracing off only the archive happens.
	off["gmail_query"], off["action_type"], off["schedule_type"] = "label:x", "archive", "manual"
	h.mustStatus("PUT", "/api/routines/"+id, off, 200)
	h.fake.set(func() { h.fake.modifyBodies = nil })
	run = h.mustStatus("POST", "/api/routines/"+id+"/run", nil, 202)
	h.waitRun(run["id"].(string))
	h.fake.mu.Lock()
	n := len(h.fake.modifyBodies)
	h.fake.mu.Unlock()
	if n != 1 {
		t.Fatalf("tracing off must not add labels, got %d calls", n)
	}
}

func TestPartialGoogleGrantIsRejectedAndNothingIsStored(t *testing.T) {
	h := newHarness(t, nil)
	h.login()
	s := "openid email"
	h.fake.set(func() { h.fake.scopeOverride = &s })
	before, _ := h.store.ListAccounts(context.Background())
	resp, _ := h.req("GET", "/api/accounts/google/connect", nil)
	state := strings.Split(strings.Split(resp.Header.Get("Location"), "state=")[1], "&")[0]
	resp, _ = h.req("GET", "/api/accounts/google/callback?code=c&state="+state, nil)
	if resp.Header.Get("Location") != "/settings?oauth_error=insufficient_scope" {
		t.Fatalf("%s", resp.Header.Get("Location"))
	}
	after, _ := h.store.ListAccounts(context.Background())
	if len(after) != len(before) {
		t.Fatal("an account with too few permissions must not be stored")
	}
	// Reconnecting with the full scope works.
	h.fake.set(func() { h.fake.scopeOverride = nil })
	resp, _ = h.req("GET", "/api/accounts/google/connect", nil)
	state = strings.Split(strings.Split(resp.Header.Get("Location"), "state=")[1], "&")[0]
	resp, _ = h.req("GET", "/api/accounts/google/callback?code=c&state="+state, nil)
	if resp.Header.Get("Location") != "/settings?connected=1" {
		t.Fatalf("%s", resp.Header.Get("Location"))
	}
}
