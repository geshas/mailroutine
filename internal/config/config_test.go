package config

import (
	"strings"
	"testing"
	"time"

	"github.com/geshas/mailroutine/internal/security"
)

func baseEnv(t *testing.T) map[string]string {
	h, _ := security.HashPassword("pw")
	k, _ := security.GenerateSecret()
	return map[string]string{
		"ADMIN_PASSWORD_HASH":  h,
		"SESSION_SECRET":       strings.Repeat("s", 40),
		"GOOGLE_CLIENT_ID":     "id",
		"GOOGLE_CLIENT_SECRET": "gcs-value-123",
		"ENCRYPTION_KEY":       k,
	}
}

func load(env map[string]string) (*Config, error) {
	return LoadFrom(func(k string) string { return env[k] })
}

func TestDefaults(t *testing.T) {
	c, err := load(baseEnv(t))
	if err != nil {
		t.Fatal(err)
	}
	if c.Addr() != "0.0.0.0:8080" || c.SessionIdleTimeout != 30*time.Minute || c.SessionAbsoluteTimeout != 0 ||
		c.DatabasePath != "/data/mailroutine.db" || c.SchedulerInterval != time.Minute ||
		c.GoogleRedirectURL != "http://localhost:8080/api/accounts/google/callback" || c.SecureCookies {
		t.Fatalf("%+v", c)
	}
}

func TestMissingRequiredReportsEverything(t *testing.T) {
	_, err := load(map[string]string{})
	if err == nil {
		t.Fatal("expected error")
	}
	for _, key := range []string{"ADMIN_PASSWORD_HASH", "SESSION_SECRET", "GOOGLE_CLIENT_ID", "GOOGLE_CLIENT_SECRET", "ENCRYPTION_KEY"} {
		if !strings.Contains(err.Error(), key) {
			t.Errorf("error should mention %s: %v", key, err)
		}
	}
}

func TestInvalidValues(t *testing.T) {
	cases := map[string]string{
		"ADMIN_PASSWORD_HASH":       "plaintext-password",
		"SESSION_SECRET":            "short",
		"ENCRYPTION_KEY":            "dG9vLXNob3J0",
		"HTTP_PORT":                 "99999",
		"APP_URL":                   "not a url",
		"TZ":                        "Mars/Base",
		"LOG_LEVEL":                 "chatty",
		"LOG_FORMAT":                "xml",
		"SESSION_IDLE_TIMEOUT":      "soon",
		"SESSION_RENEWAL_THRESHOLD": "1h",
		"SESSION_ABSOLUTE_TIMEOUT":  "10m",
		"MAX_CONCURRENT_ROUTINES":   "0",
		"TRUSTED_PROXIES":           "nope",
		"LOG_COMPRESS":              "maybe",
	}
	for k, v := range cases {
		env := baseEnv(t)
		env[k] = v
		if _, err := load(env); err == nil || !strings.Contains(err.Error(), k) {
			t.Errorf("%s=%q: got %v", k, v, err)
		}
	}
}

func TestHTTPSAndProxies(t *testing.T) {
	env := baseEnv(t)
	env["APP_URL"] = "https://mail.example.com/"
	env["TRUSTED_PROXIES"] = "10.0.0.0/8, 192.168.1.5"
	c, err := load(env)
	if err != nil {
		t.Fatal(err)
	}
	if !c.SecureCookies || c.AppURL.String() != "https://mail.example.com" || len(c.TrustedProxies) != 2 {
		t.Fatalf("%+v", c)
	}
	if c.GoogleRedirectURL != "https://mail.example.com/api/accounts/google/callback" {
		t.Fatal(c.GoogleRedirectURL)
	}
}

func TestStringRedactsSecrets(t *testing.T) {
	env := baseEnv(t)
	c, _ := load(env)
	s := c.String()
	for _, secret := range []string{env["SESSION_SECRET"], env["GOOGLE_CLIENT_SECRET"], env["ENCRYPTION_KEY"], env["ADMIN_PASSWORD_HASH"]} {
		if strings.Contains(s, secret) {
			t.Fatalf("secret leaked in %s", s)
		}
	}
}
