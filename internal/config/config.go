// Package config loads and validates MailRoutine's environment configuration.
package config

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/geshas/mailroutine/internal/security"
)

// Config is the validated runtime configuration. Secrets are never printed:
// String() redacts them.
type Config struct {
	AppURL        *url.URL
	HTTPAddr      string
	HTTPPort      int
	Location      *time.Location
	Timezone      string
	SecureCookies bool

	AdminUsername     string
	AdminPasswordHash string

	SessionIdleTimeout      time.Duration
	SessionRenewalThreshold time.Duration
	SessionAbsoluteTimeout  time.Duration
	SessionSecret           []byte

	GoogleClientID     string
	GoogleClientSecret string
	GoogleRedirectURL  string

	EncryptionKey []byte

	DatabasePath string

	SchedulerInterval     time.Duration
	DefaultMaxMessages    int
	MaxConcurrentRoutines int

	GmailBatchSize      int
	GmailRequestTimeout time.Duration

	LogLevel      string
	LogFormat     string
	LogFilePath   string
	LogMaxSizeMB  int
	LogMaxBackups int
	LogMaxAgeDays int
	LogCompress   bool

	TrustedProxies []*net.IPNet
}

// Addr returns the listen address.
func (c *Config) Addr() string { return net.JoinHostPort(c.HTTPAddr, strconv.Itoa(c.HTTPPort)) }

// String implements fmt.Stringer without exposing secrets.
func (c *Config) String() string {
	return fmt.Sprintf("Config{app_url=%s addr=%s tz=%s admin=%s db=%s google_client_id=%s secrets=[REDACTED]}",
		c.AppURL, c.Addr(), c.Timezone, c.AdminUsername, c.DatabasePath, c.GoogleClientID)
}

// Getenv abstracts os.Getenv for tests.
type Getenv func(string) string

// Load reads the process environment.
func Load() (*Config, error) { return LoadFrom(os.Getenv) }

// LoadFrom validates configuration from getenv, returning every problem found.
func LoadFrom(getenv Getenv) (*Config, error) {
	l := &loader{get: func(k string) string { return strings.TrimSpace(getenv(k)) }}
	c := &Config{}

	appURL := l.str("APP_URL", "http://localhost:8080")
	if u, err := url.Parse(appURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		l.fail("APP_URL", "must be an absolute http(s) URL such as https://mail.example.com")
	} else {
		u.Path = strings.TrimRight(u.Path, "/")
		c.AppURL = u
		c.SecureCookies = u.Scheme == "https"
	}
	c.HTTPAddr = l.str("HTTP_ADDR", "0.0.0.0")
	c.HTTPPort = l.intRange("HTTP_PORT", 8080, 1, 65535)

	c.Timezone = l.str("TZ", "UTC")
	loc, err := time.LoadLocation(c.Timezone)
	if err != nil {
		l.fail("TZ", "unknown timezone "+strconv.Quote(c.Timezone)+" (use an IANA name such as Europe/Berlin)")
		loc = time.UTC
	}
	c.Location = loc

	c.AdminUsername = l.str("ADMIN_USERNAME", "admin")
	c.AdminPasswordHash = l.str("ADMIN_PASSWORD_HASH", "")
	switch {
	case c.AdminPasswordHash == "":
		l.fail("ADMIN_PASSWORD_HASH", "is required; generate one with: mailroutine hash-password")
	case security.ValidateHash(c.AdminPasswordHash) != nil:
		l.fail("ADMIN_PASSWORD_HASH", "is not a valid Argon2id PHC hash (if it came from a .env file, wrap it in single quotes so '$' is preserved)")
	}

	c.SessionIdleTimeout = l.duration("SESSION_IDLE_TIMEOUT", 30*time.Minute, time.Minute)
	c.SessionRenewalThreshold = l.duration("SESSION_RENEWAL_THRESHOLD", 5*time.Minute, 0)
	c.SessionAbsoluteTimeout = l.duration("SESSION_ABSOLUTE_TIMEOUT", 0, 0)
	if c.SessionRenewalThreshold >= c.SessionIdleTimeout {
		l.fail("SESSION_RENEWAL_THRESHOLD", "must be shorter than SESSION_IDLE_TIMEOUT")
	}
	if c.SessionAbsoluteTimeout != 0 && c.SessionAbsoluteTimeout < c.SessionIdleTimeout {
		l.fail("SESSION_ABSOLUTE_TIMEOUT", "must be 0 (disabled) or at least SESSION_IDLE_TIMEOUT")
	}
	secret := l.str("SESSION_SECRET", "")
	if len(secret) < 32 {
		l.fail("SESSION_SECRET", "is required and must be at least 32 characters; generate with: mailroutine generate-secret")
	}
	c.SessionSecret = []byte(secret)

	c.GoogleClientID = l.str("GOOGLE_CLIENT_ID", "")
	c.GoogleClientSecret = l.str("GOOGLE_CLIENT_SECRET", "")
	if c.GoogleClientID == "" {
		l.fail("GOOGLE_CLIENT_ID", "is required (see docs/oauth-setup.md)")
	}
	if c.GoogleClientSecret == "" {
		l.fail("GOOGLE_CLIENT_SECRET", "is required (see docs/oauth-setup.md)")
	}
	c.GoogleRedirectURL = l.str("GOOGLE_REDIRECT_URL", "")
	if c.GoogleRedirectURL == "" && c.AppURL != nil {
		c.GoogleRedirectURL = c.AppURL.String() + "/api/accounts/google/callback"
	} else if u, err := url.Parse(c.GoogleRedirectURL); err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") {
		l.fail("GOOGLE_REDIRECT_URL", "must be an absolute http(s) URL")
	}

	if k := l.str("ENCRYPTION_KEY", ""); k == "" {
		l.fail("ENCRYPTION_KEY", "is required; generate with: mailroutine generate-secret")
	} else if key, err := security.ParseKey(k); err != nil {
		l.fail("ENCRYPTION_KEY", err.Error())
	} else {
		c.EncryptionKey = key
	}

	c.DatabasePath = l.str("DATABASE_PATH", "/data/mailroutine.db")

	c.SchedulerInterval = l.duration("SCHEDULER_INTERVAL", 60*time.Second, 5*time.Second)
	c.DefaultMaxMessages = l.intRange("DEFAULT_MAX_MESSAGES_PER_RUN", 500, 1, 10000)
	c.MaxConcurrentRoutines = l.intRange("MAX_CONCURRENT_ROUTINES", 2, 1, 16)

	c.GmailBatchSize = l.intRange("GMAIL_BATCH_SIZE", 100, 1, 1000)
	c.GmailRequestTimeout = l.duration("GMAIL_REQUEST_TIMEOUT", 30*time.Second, time.Second)

	c.LogLevel = strings.ToLower(l.str("LOG_LEVEL", "info"))
	if !contains([]string{"debug", "info", "warn", "error"}, c.LogLevel) {
		l.fail("LOG_LEVEL", "must be one of debug, info, warn, error")
	}
	c.LogFormat = strings.ToLower(l.str("LOG_FORMAT", "json"))
	if !contains([]string{"json", "text"}, c.LogFormat) {
		l.fail("LOG_FORMAT", "must be json or text")
	}
	c.LogFilePath = l.str("LOG_FILE_PATH", "")
	c.LogMaxSizeMB = l.intRange("LOG_MAX_SIZE_MB", 10, 1, 10240)
	c.LogMaxBackups = l.intRange("LOG_MAX_BACKUPS", 5, 0, 1000)
	c.LogMaxAgeDays = l.intRange("LOG_MAX_AGE_DAYS", 30, 0, 36500)
	c.LogCompress = l.boolean("LOG_COMPRESS", true)

	for _, p := range strings.Split(l.str("TRUSTED_PROXIES", ""), ",") {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		if !strings.Contains(p, "/") {
			if strings.Contains(p, ":") {
				p += "/128"
			} else {
				p += "/32"
			}
		}
		if _, n, err := net.ParseCIDR(p); err != nil {
			l.fail("TRUSTED_PROXIES", "invalid CIDR/IP "+strconv.Quote(p))
		} else {
			c.TrustedProxies = append(c.TrustedProxies, n)
		}
	}

	if len(l.errs) > 0 {
		return nil, fmt.Errorf("invalid configuration:\n  - %w", errors.New(strings.Join(l.errs, "\n  - ")))
	}
	return c, nil
}

type loader struct {
	get  func(string) string
	errs []string
}

func (l *loader) fail(key, msg string) { l.errs = append(l.errs, key+" "+msg) }

func (l *loader) str(key, def string) string {
	if v := l.get(key); v != "" {
		return v
	}
	return def
}

func (l *loader) intRange(key string, def, lo, hi int) int {
	v := l.get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < lo || n > hi {
		l.fail(key, fmt.Sprintf("must be an integer between %d and %d", lo, hi))
		return def
	}
	return n
}

func (l *loader) duration(key string, def, min time.Duration) time.Duration {
	v := l.get(key)
	if v == "" {
		return def
	}
	if v == "0" {
		v = "0s"
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 || (d != 0 || min > 0) && d < min {
		l.fail(key, fmt.Sprintf("must be a duration such as 30m (minimum %s)", min))
		return def
	}
	return d
}

func (l *loader) boolean(key string, def bool) bool {
	v := l.get(key)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		l.fail(key, "must be true or false")
		return def
	}
	return b
}

func contains(list []string, s string) bool {
	for _, x := range list {
		if x == s {
			return true
		}
	}
	return false
}

// LoadDotEnv reads KEY=VALUE lines from path into the process environment
// without overriding variables that are already set. A missing file is not an
// error. Values may be wrapped in single or double quotes.
func LoadDotEnv(path string) error {
	data, err := os.ReadFile(path) // #nosec G304 -- path is chosen by the operator/CLI, not request input
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '\'' && v[len(v)-1] == '\'' || v[0] == '"' && v[len(v)-1] == '"') {
			v = v[1 : len(v)-1]
		} else if i := strings.Index(v, " #"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		if _, set := os.LookupEnv(k); !set {
			os.Setenv(k, v)
		}
	}
	return nil
}
