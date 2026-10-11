package logging

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestStdoutOnly(t *testing.T) {
	var out bytes.Buffer
	l, c, err := New(Options{Level: "info", Format: "json", Stdout: &out})
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	l.With("component", "test").Info("hello", "routine_id", "r1", "status", "ok")
	var m map[string]any
	if err := json.Unmarshal(out.Bytes(), &m); err != nil {
		t.Fatalf("not json: %q", out.String())
	}
	if m["msg"] != "hello" || m["component"] != "test" || m["routine_id"] != "r1" || m["level"] != "INFO" {
		t.Fatalf("%v", m)
	}
}

func TestLevels(t *testing.T) {
	var out bytes.Buffer
	l, _, _ := New(Options{Level: "warn", Format: "text", Stdout: &out})
	l.Info("hidden")
	l.Warn("shown")
	if strings.Contains(out.String(), "hidden") || !strings.Contains(out.String(), "shown") {
		t.Fatalf("%q", out.String())
	}
	if _, _, err := New(Options{Level: "loud"}); err == nil {
		t.Fatal("bad level accepted")
	}
	if _, _, err := New(Options{Format: "xml"}); err == nil {
		t.Fatal("bad format accepted")
	}
}

func TestStdoutAndFileReceiveSameEvents(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "nested", "logs", "app.log") // parent dirs must be created
	var out bytes.Buffer
	l, c, err := New(Options{Level: "info", Format: "json", FilePath: path, MaxSizeMB: 1, MaxBackups: 2, MaxAgeDays: 1, Stdout: &out})
	if err != nil {
		t.Fatal(err)
	}
	l.Info("both destinations", "n", 1)
	c.Close()
	file, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(file), "both destinations") || out.String() != string(file) {
		t.Fatalf("stdout=%q file=%q", out.String(), file)
	}
}

func TestRotation(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "app.log")
	var out bytes.Buffer
	l, c, err := New(Options{Level: "info", Format: "json", FilePath: path, MaxSizeMB: 1, MaxBackups: 3, Stdout: &out})
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Repeat("x", 1024)
	for i := 0; i < 2300; i++ { // ~2.3 MB with a 1 MB cap
		l.Info(line, "i", i)
	}
	c.Close()
	entries, _ := os.ReadDir(dir)
	if len(entries) < 3 {
		t.Fatalf("expected rotated backups, got %d files", len(entries))
	}
	st, _ := os.Stat(path)
	if st.Size() > 1<<20+4096 {
		t.Fatalf("active file exceeds cap: %d", st.Size())
	}
}

func TestInvalidPaths(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "afile")
	os.WriteFile(file, []byte("x"), 0o600)
	// Parent is a regular file -> cannot create directory.
	if _, _, err := New(Options{FilePath: filepath.Join(file, "sub", "app.log")}); err == nil || !strings.Contains(err.Error(), "LOG_FILE_PATH") {
		t.Fatalf("got %v", err)
	}
	// Path is a directory -> cannot open for writing.
	if _, _, err := New(Options{FilePath: dir}); err == nil || !strings.Contains(err.Error(), "LOG_FILE_PATH") {
		t.Fatalf("got %v", err)
	}
}

func TestRedaction(t *testing.T) {
	var out bytes.Buffer
	l, _, _ := New(Options{Level: "debug", Format: "json", Stdout: &out})
	l.Info("login", "password", "hunter2", "session_token", "abc", "Authorization", "Bearer xyz",
		"note", "token ya29.A0ARrdaM-secret and 1//0gAbCdEfGhIjKlMnOpQrStUv",
		"err", errors.New("callback ?code=4/0AX4XfWh&state=abc failed"),
		"hash", "$argon2id$v=19$m=65536,t=3,p=2$c2FsdHNhbHQ$aGFzaA", "encryption_key", "k")
	s := out.String()
	for _, leak := range []string{"hunter2", "abc", "xyz", "ya29", "1//0gAb", "4/0AX4XfWh", "argon2id", `"k"`} {
		if strings.Contains(s, leak) {
			t.Errorf("leaked %q in %s", leak, s)
		}
	}
	if !strings.Contains(s, "[REDACTED]") {
		t.Fatal("nothing redacted")
	}
}
