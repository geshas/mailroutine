package setup

import (
	"encoding/base64"
	"html"
	"net/http"
	"net/http/httptest"
	"net/url"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/geshas/mailroutine/internal/config"
	"github.com/geshas/mailroutine/internal/security"
)

func get(t *testing.T, h http.Handler, path string) (*httptest.ResponseRecorder, string) {
	t.Helper()
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
	return rec, rec.Body.String()
}

func post(t *testing.T, h http.Handler, form url.Values) (*httptest.ResponseRecorder, string) {
	t.Helper()
	req := httptest.NewRequest("POST", "/", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec, rec.Body.String()
}

var hiddenRe = regexp.MustCompile(`name="(session_secret|encryption_key)" value="([^"]+)"`)

func secretsOf(t *testing.T, page string) url.Values {
	t.Helper()
	v := url.Values{}
	for _, m := range hiddenRe.FindAllStringSubmatch(page, -1) {
		v.Set(m[1], html.UnescapeString(m[2])) // the browser decodes attribute entities
	}
	if v.Get("session_secret") == "" || v.Get("encryption_key") == "" {
		t.Fatalf("secrets missing from page")
	}
	return v
}

func TestPageShowsProblemsAndFreshValidSecretsEveryTime(t *testing.T) {
	h := Handler("ADMIN_PASSWORD_HASH is required", "1.2.3")
	rec, body := get(t, h, "/")
	if rec.Code != 200 || !strings.Contains(body, "ADMIN_PASSWORD_HASH is required") || !strings.Contains(body, "version 1.2.3") {
		t.Fatalf("%d %s", rec.Code, body)
	}
	a := secretsOf(t, body)
	if _, err := security.ParseKey(a.Get("encryption_key")); err != nil {
		t.Fatalf("generated ENCRYPTION_KEY must be valid: %v", err)
	}
	if len(a.Get("session_secret")) < 32 {
		t.Fatal("generated SESSION_SECRET too short")
	}
	_, body2 := get(t, h, "/")
	b := secretsOf(t, body2)
	if a.Get("encryption_key") == b.Get("encryption_key") || a.Get("session_secret") == b.Get("session_secret") {
		t.Fatal("secrets must be freshly random on every load")
	}
}

func TestGeneratedValuesAreAcceptedByTheRealConfigLoader(t *testing.T) {
	h := Handler("", "")
	_, body := get(t, h, "/")
	form := secretsOf(t, body)
	form.Set("password", "correct horse battery")
	form.Set("confirm", "correct horse battery")
	rec, out := post(t, h, form)
	if rec.Code != 200 {
		t.Fatalf("%d %s", rec.Code, out)
	}
	env := map[string]string{}
	block := regexp.MustCompile(`(?s)<textarea[^>]*>(.*?)</textarea>`).FindStringSubmatch(out)
	if block == nil {
		t.Fatalf("no env block in %s", out)
	}
	for _, line := range strings.Split(strings.TrimSpace(html.UnescapeString(block[1])), "\n") {
		k, v, _ := strings.Cut(strings.TrimSpace(line), "=")
		env[k] = v
	}
	if env["ADMIN_USERNAME"] != "admin" || strings.Contains(out, "$argon2id$v=19$m=65536,t=3,p=2$"+"x") {
		t.Fatal(env)
	}
	if env["SESSION_SECRET"] != form.Get("session_secret") || env["ENCRYPTION_KEY"] != form.Get("encryption_key") {
		t.Fatal("the secrets copied must be the ones that were on the page")
	}
	if strings.Contains(env["ADMIN_PASSWORD_HASH_B64"], "$") {
		t.Fatal("the encoded hash must not contain '$'")
	}
	// Feed exactly what a person would paste into Portainer to the real loader.
	env["GOOGLE_CLIENT_ID"], env["GOOGLE_CLIENT_SECRET"] = "id", "secret-value"
	cfg, err := config.LoadFrom(func(k string) string { return env[k] })
	if err != nil {
		t.Fatalf("generated configuration rejected: %v", err)
	}
	if ok, _ := security.VerifyPassword("correct horse battery", cfg.AdminPasswordHash); !ok {
		t.Fatal("the hash must verify against the chosen password")
	}
	if ok, _ := security.VerifyPassword("wrong", cfg.AdminPasswordHash); ok {
		t.Fatal("wrong password accepted")
	}
	raw, _ := base64.StdEncoding.DecodeString(env["ADMIN_PASSWORD_HASH_B64"])
	if !strings.HasPrefix(string(raw), "$argon2id$") {
		t.Fatal("not a PHC hash")
	}
	if strings.Contains(out, "correct horse") {
		t.Fatal("the password must never be echoed back")
	}
}

func TestPasswordValidation(t *testing.T) {
	h := Handler("", "")
	_, body := get(t, h, "/")
	base := secretsOf(t, body)
	cases := map[string][2]string{
		"too short": {"short", "short"},
		"mismatch":  {"password-one", "password-two"},
		"empty":     {"", ""},
		"huge":      {strings.Repeat("a", 300), strings.Repeat("a", 300)},
	}
	for name, c := range cases {
		f := url.Values{}
		for k, v := range base {
			f[k] = v
		}
		f.Set("password", c[0])
		f.Set("confirm", c[1])
		rec, out := post(t, h, f)
		if rec.Code != 400 || !strings.Contains(out, `role="alert"`) || strings.Contains(out, "<textarea") {
			t.Errorf("%s: %d", name, rec.Code)
		}
		if got := secretsOf(t, out); got.Get("encryption_key") != base.Get("encryption_key") {
			t.Errorf("%s: secrets must stay stable across validation errors", name)
		}
	}
}

func TestTamperedOrMissingSecretsAreReplacedNotTrusted(t *testing.T) {
	h := Handler("", "")
	f := url.Values{"password": {"long enough pw"}, "confirm": {"long enough pw"}, "session_secret": {"short"}, "encryption_key": {"not-a-key"}}
	rec, out := post(t, h, f)
	if rec.Code != 200 {
		t.Fatalf("%d", rec.Code)
	}
	m := regexp.MustCompile(`ENCRYPTION_KEY=([A-Za-z0-9+/=]+)`).FindStringSubmatch(html.UnescapeString(out))
	if m == nil {
		t.Fatal("no key in output")
	}
	if _, err := security.ParseKey(m[1]); err != nil {
		t.Fatalf("a bad submitted key must be replaced with a valid one: %v", err)
	}
	if strings.Contains(out, "not-a-key") {
		t.Fatal("tampered value was echoed")
	}
}

func TestHealthReportsSetupRequiredAndSecurityHeaders(t *testing.T) {
	h := Handler("x", "")
	for _, p := range []string{"/health", "/ready"} {
		rec, body := get(t, h, p)
		if rec.Code != http.StatusServiceUnavailable || !strings.Contains(body, "setup_required") {
			t.Errorf("%s: %d %s", p, rec.Code, body)
		}
	}
	rec, _ := get(t, h, "/")
	csp := rec.Header().Get("Content-Security-Policy")
	if rec.Header().Get("Cache-Control") != "no-store" || !strings.Contains(csp, "default-src 'none'") || strings.Contains(csp, "script-src") ||
		rec.Header().Get("X-Frame-Options") != "DENY" || rec.Header().Get("Referrer-Policy") != "no-referrer" {
		t.Fatalf("%v", rec.Header())
	}
	if rec, _ := get(t, h, "/other"); rec.Code != 404 {
		t.Fatalf("unknown paths must 404, got %d", rec.Code)
	}
}

func TestMalformedBodyAndRateLimit(t *testing.T) {
	h := Handler("", "")
	req := httptest.NewRequest("POST", "/", strings.NewReader("%zz"))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != 400 {
		t.Fatalf("%d", rec.Code)
	}
	_, body := get(t, h, "/")
	f := secretsOf(t, body)
	f.Set("password", "x") // fails validation quickly, but still counts against the limiter
	f.Set("confirm", "x")
	var limited bool
	for i := 0; i < 15; i++ {
		rec, _ := post(t, h, f)
		limited = limited || rec.Code == http.StatusTooManyRequests
	}
	if !limited {
		t.Fatal("repeated submissions must be rate limited")
	}
}

func TestLimiterWindow(t *testing.T) {
	now := time.Now()
	l := newLimiter(2, time.Minute)
	l.now = func() time.Time { return now }
	if !l.allow() || !l.allow() || l.allow() {
		t.Fatal("limit not enforced")
	}
	now = now.Add(61 * time.Second)
	if !l.allow() {
		t.Fatal("window did not expire")
	}
}
