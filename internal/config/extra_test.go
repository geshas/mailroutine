package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestLoadReadsProcessEnvironment(t *testing.T) {
	env := baseEnv(t)
	for k, v := range env {
		t.Setenv(k, v)
	}
	t.Setenv("HTTP_PORT", "9090")
	c, err := Load()
	if err != nil || c.HTTPPort != 9090 {
		t.Fatalf("%v %v", c, err)
	}
}

func TestLoadDotEnv(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "# comment\n\nFOO_A=plain\nFOO_B='quo$ted value'\nFOO_C=\"dq\"\nexport FOO_D=exported\nFOO_E=trailing # comment\nFOO_F=keep-existing\nnot-a-pair\r\nFOO_G=crlf\r\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("FOO_F", "from-process")
	for _, k := range []string{"FOO_A", "FOO_B", "FOO_C", "FOO_D", "FOO_E", "FOO_G"} {
		os.Unsetenv(k)
		t.Cleanup(func() { os.Unsetenv(k) })
	}
	if err := LoadDotEnv(path); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{"FOO_A": "plain", "FOO_B": "quo$ted value", "FOO_C": "dq", "FOO_D": "exported",
		"FOO_E": "trailing", "FOO_F": "from-process", "FOO_G": "crlf"}
	for k, v := range want {
		if os.Getenv(k) != v {
			t.Errorf("%s=%q want %q", k, os.Getenv(k), v)
		}
	}
	// Missing file is fine; an unreadable path (a directory) is an error.
	if err := LoadDotEnv(filepath.Join(dir, "missing")); err != nil {
		t.Fatal(err)
	}
	if err := LoadDotEnv(dir); err == nil {
		t.Fatal("directory accepted")
	}
}

func TestDurationAndBooleanParsing(t *testing.T) {
	env := baseEnv(t)
	env["SESSION_ABSOLUTE_TIMEOUT"] = "0"
	env["LOG_COMPRESS"] = "false"
	env["SESSION_IDLE_TIMEOUT"] = "2h"
	env["SESSION_ABSOLUTE_TIMEOUT"] = "24h"
	env["GOOGLE_REDIRECT_URL"] = "https://x.example/cb"
	env["TRUSTED_PROXIES"] = "::1, 10.0.0.0/8,"
	c, err := load(env)
	if err != nil {
		t.Fatal(err)
	}
	if c.LogCompress || c.SessionIdleTimeout != 2*time.Hour || c.SessionAbsoluteTimeout != 24*time.Hour ||
		c.GoogleRedirectURL != "https://x.example/cb" || len(c.TrustedProxies) != 2 {
		t.Fatalf("%+v", c)
	}
	for k, v := range map[string]string{
		"SESSION_IDLE_TIMEOUT": "10s", "SCHEDULER_INTERVAL": "1s", "GMAIL_REQUEST_TIMEOUT": "-5s",
		"GOOGLE_REDIRECT_URL": "/relative", "HTTP_PORT": "abc", "LOG_MAX_SIZE_MB": "0",
	} {
		e := baseEnv(t)
		e[k] = v
		if _, err := load(e); err == nil || !strings.Contains(err.Error(), k) {
			t.Errorf("%s=%s: %v", k, v, err)
		}
	}
	e := baseEnv(t)
	e["SESSION_RENEWAL_THRESHOLD"] = "0"
	if _, err := load(e); err != nil {
		t.Fatalf("zero threshold must be allowed: %v", err)
	}
}
