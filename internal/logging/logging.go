// Package logging builds the application's slog logger: always stdout, optionally
// also a size-rotated file, with sensitive values redacted.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"gopkg.in/natefinch/lumberjack.v2"
)

// Options configures New.
type Options struct {
	Level      string // debug, info, warn, error
	Format     string // json or text
	FilePath   string // empty => stdout only
	MaxSizeMB  int
	MaxBackups int
	MaxAgeDays int
	Compress   bool
	// Stdout overrides os.Stdout (tests).
	Stdout io.Writer
}

// New returns the logger and a closer that flushes/closes the log file.
func New(o Options) (*slog.Logger, io.Closer, error) {
	var level slog.Level
	switch strings.ToLower(o.Level) {
	case "debug":
		level = slog.LevelDebug
	case "", "info":
		level = slog.LevelInfo
	case "warn", "warning":
		level = slog.LevelWarn
	case "error":
		level = slog.LevelError
	default:
		return nil, nil, fmt.Errorf("unknown log level %q", o.Level)
	}
	stdout := o.Stdout
	if stdout == nil {
		stdout = os.Stdout
	}
	var w io.Writer = stdout
	var closer io.Closer = nopCloser{}

	if o.FilePath != "" {
		dir := filepath.Dir(o.FilePath)
		if err := os.MkdirAll(dir, 0o750); err != nil {
			return nil, nil, fmt.Errorf("LOG_FILE_PATH: cannot create directory %q: %w", dir, err)
		}
		// Open once so permission problems surface at startup, not at first write.
		f, err := os.OpenFile(o.FilePath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			return nil, nil, fmt.Errorf("LOG_FILE_PATH: cannot write %q: %w", o.FilePath, err)
		}
		f.Close()
		lj := &lumberjack.Logger{
			Filename: o.FilePath, MaxSize: max(o.MaxSizeMB, 1), MaxBackups: o.MaxBackups,
			MaxAge: o.MaxAgeDays, Compress: o.Compress,
		}
		w = io.MultiWriter(stdout, lj)
		closer = lj
	}

	opts := &slog.HandlerOptions{Level: level, ReplaceAttr: redactAttr}
	var h slog.Handler
	switch strings.ToLower(o.Format) {
	case "", "json":
		h = slog.NewJSONHandler(w, opts)
	case "text":
		h = slog.NewTextHandler(w, opts)
	default:
		return nil, nil, fmt.Errorf("unknown log format %q", o.Format)
	}
	return slog.New(h), closer, nil
}

type nopCloser struct{}

func (nopCloser) Close() error { return nil }

const redacted = "[REDACTED]"

var sensitiveKeys = []string{
	"password", "passwd", "secret", "token", "authorization", "cookie", "api_key", "apikey",
	"encryption_key", "hash", "credential", "private_key", "auth_code", "authorization_code",
}

var sensitiveValues = []*regexp.Regexp{
	regexp.MustCompile(`ya29\.[A-Za-z0-9._\-]+`),
	regexp.MustCompile(`1//[A-Za-z0-9._\-]{20,}`),
	regexp.MustCompile(`(?i)bearer\s+[A-Za-z0-9._~+/=\-]+`),
	regexp.MustCompile(`\$argon2id\$[^\s"]+`),
	regexp.MustCompile(`(?i)([?&](?:code|access_token|refresh_token|client_secret|state)=)[^&\s"]+`),
}

// Redact scrubs known secret shapes from free text.
func Redact(s string) string {
	for i, re := range sensitiveValues {
		if i == len(sensitiveValues)-1 {
			s = re.ReplaceAllString(s, "${1}"+redacted)
			continue
		}
		s = re.ReplaceAllString(s, redacted)
	}
	return s
}

func redactAttr(_ []string, a slog.Attr) slog.Attr {
	if a.Key == slog.TimeKey || a.Key == slog.LevelKey {
		return a
	}
	key := strings.ToLower(a.Key)
	for _, k := range sensitiveKeys {
		if strings.Contains(key, k) {
			return slog.String(a.Key, redacted)
		}
	}
	switch a.Value.Kind() {
	case slog.KindString:
		return slog.String(a.Key, Redact(a.Value.String()))
	case slog.KindAny:
		if err, ok := a.Value.Any().(error); ok {
			return slog.String(a.Key, Redact(err.Error()))
		}
	}
	return a
}
