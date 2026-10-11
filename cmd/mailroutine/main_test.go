package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/geshas/mailroutine/internal/security"
	"github.com/geshas/mailroutine/internal/sqlite"
	"github.com/geshas/mailroutine/pkg/models"
)

func pipeWith(t *testing.T, content string) *os.File {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	w.WriteString(content)
	w.Close()
	t.Cleanup(func() { r.Close() })
	return r
}

func exec(t *testing.T, stdin string, args ...string) (code int, out, errOut string) {
	t.Helper()
	var o, e bytes.Buffer
	code = run(args, pipeWith(t, stdin), &o, &e)
	return code, o.String(), e.String()
}

func TestSimpleCommands(t *testing.T) {
	if code, out, _ := exec(t, "", "version"); code != 0 || strings.TrimSpace(out) != version {
		t.Fatal(code, out)
	}
	for _, flag := range []string{"--version", "-v"} {
		if code, out, _ := exec(t, "", flag); code != 0 || strings.TrimSpace(out) != version {
			t.Fatal(flag)
		}
	}
	for _, h := range []string{"help", "--help", "-h"} {
		if code, out, _ := exec(t, "", h); code != 0 || !strings.Contains(out, "hash-password") {
			t.Fatal(h)
		}
	}
	code, out, _ := exec(t, "", "generate-secret")
	if code != 0 {
		t.Fatal(code)
	}
	if _, err := security.ParseKey(strings.TrimSpace(out)); err != nil {
		t.Fatalf("generated secret is not a valid key: %v", err)
	}
	if code, _, errOut := exec(t, "", "frobnicate"); code != 2 || !strings.Contains(errOut, "unknown command") {
		t.Fatal(code, errOut)
	}
	if code, _, errOut := exec(t, "", "backup"); code != 2 || !strings.Contains(errOut, "usage") {
		t.Fatal(code, errOut)
	}
}

func TestHashPasswordFromPipe(t *testing.T) {
	code, out, errOut := exec(t, "correct horse battery\ncorrect horse battery\n", "hash-password")
	if code != 0 {
		t.Fatalf("%d %s", code, errOut)
	}
	line := strings.TrimSpace(out)
	if !strings.HasPrefix(line, "ADMIN_PASSWORD_HASH='$argon2id$") || !strings.HasSuffix(line, "'") {
		t.Fatalf("not a quoted env line: %q", line)
	}
	hash := strings.TrimSuffix(strings.TrimPrefix(line, "ADMIN_PASSWORD_HASH='"), "'")
	if ok, err := security.VerifyPassword("correct horse battery", hash); !ok || err != nil {
		t.Fatal("hash does not verify")
	}
	if strings.Contains(out+errOut, "correct horse") {
		t.Fatal("password echoed")
	}
	// Default (no args) is serve; hash-password must not need any configuration.
	for name, in := range map[string]string{
		"mismatch":        "password-one\npassword-two\n",
		"short":           "short\nshort\n",
		"empty":           "",
		"no confirmation": "only-one-line-pw\n",
	} {
		if code, _, errOut := exec(t, in, "hash-password"); code != 1 || errOut == "" {
			t.Errorf("%s: code=%d err=%q", name, code, errOut)
		}
	}
}

func TestBackupCommand(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "m.db")
	st, err := sqlite.Open(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	st.SetSetting(context.Background(), "k", "v")
	st.Close()
	t.Setenv("DATABASE_PATH", db)
	dest := filepath.Join(dir, "out", "copy.db")
	code, out, errOut := exec(t, "", "backup", dest)
	if code != 0 || !strings.Contains(out, "ENCRYPTION_KEY") {
		t.Fatalf("%d %s %s", code, out, errOut)
	}
	cp, err := sqlite.Open(context.Background(), dest)
	if err != nil {
		t.Fatal(err)
	}
	defer cp.Close()
	if v, _ := cp.GetSetting(context.Background(), "k"); v != "v" {
		t.Fatal("backup is missing data")
	}
	if code, _, errOut := exec(t, "", "backup", dest); code != 1 || !strings.Contains(errOut, "already exists") {
		t.Fatalf("overwrite must be refused: %d %s", code, errOut)
	}
	t.Setenv("DATABASE_PATH", filepath.Join(dir, "missing.db"))
	if code, _, errOut := exec(t, "", "backup", filepath.Join(dir, "x.db")); code != 1 || !strings.Contains(errOut, "not found") {
		t.Fatal(code, errOut)
	}
	t.Setenv("DATABASE_PATH", "")
	if code, _, _ := exec(t, "", "backup", filepath.Join(dir, "y.db")); code != 1 {
		t.Fatal("default /data path should not exist in tests")
	}
	// Existing but unusable database file.
	junk := filepath.Join(dir, "junk.db")
	os.WriteFile(junk, []byte(strings.Repeat("not sqlite ", 50)), 0o600)
	t.Setenv("DATABASE_PATH", junk)
	if code, _, _ := exec(t, "", "backup", filepath.Join(dir, "z.db")); code != 1 {
		t.Fatal("junk database accepted")
	}
}

func TestHealthcheckCommand(t *testing.T) {
	status := http.StatusOK
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/health" {
			t.Errorf("path %s", r.URL.Path)
		}
		w.WriteHeader(status)
	}))
	defer srv.Close()
	t.Setenv("HTTP_PORT", strings.Split(srv.URL, ":")[2])
	if code, _, errOut := exec(t, "", "healthcheck"); code != 0 {
		t.Fatal(errOut)
	}
	status = http.StatusServiceUnavailable
	if code, _, errOut := exec(t, "", "healthcheck"); code != 1 || !strings.Contains(errOut, "503") {
		t.Fatal(code, errOut)
	}
	srv.Close()
	if code, _, _ := exec(t, "", "healthcheck"); code != 1 {
		t.Fatal("unreachable server must fail")
	}
	t.Setenv("HTTP_PORT", "")
	exec(t, "", "healthcheck") // falls back to 8080; result irrelevant, must not panic
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func serveEnv(t *testing.T, port int) string {
	t.Helper()
	dir := t.TempDir()
	hash, _ := security.HashPassword("server-test-pw")
	key, _ := security.GenerateSecret()
	secret, _ := security.GenerateSecret()
	for k, v := range map[string]string{
		"ADMIN_PASSWORD_HASH": hash, "SESSION_SECRET": secret, "ENCRYPTION_KEY": key,
		"GOOGLE_CLIENT_ID": "cid", "GOOGLE_CLIENT_SECRET": "csec", "DATABASE_PATH": filepath.Join(dir, "db", "m.db"),
		"HTTP_ADDR": "127.0.0.1", "HTTP_PORT": fmt.Sprint(port), "APP_URL": fmt.Sprintf("http://localhost:%d", port),
		"LOG_FILE_PATH": filepath.Join(dir, "logs", "app.log"), "SCHEDULER_INTERVAL": "5s",
	} {
		t.Setenv(k, v)
	}
	return dir
}

func TestServeLifecycle(t *testing.T) {
	port := freePort(t)
	dir := serveEnv(t, port)
	done := make(chan error, 1)
	go func() { done <- serve() }()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	var healthy bool
	for i := 0; i < 100 && !healthy; i++ {
		if resp, err := http.Get(base + "/health"); err == nil {
			var m map[string]string
			json.NewDecoder(resp.Body).Decode(&m)
			resp.Body.Close()
			healthy = resp.StatusCode == 200 && m["status"] == "ok"
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !healthy {
		t.Fatal("server never became healthy")
	}
	// Real login against the live server proves wiring (auth, store, router).
	body := strings.NewReader(`{"username":"admin","password":"server-test-pw"}`)
	req, _ := http.NewRequest("POST", base+"/api/auth/login", body)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Origin", fmt.Sprintf("http://localhost:%d", port))
	resp, err := http.DefaultClient.Do(req)
	if err != nil || resp.StatusCode != 200 {
		t.Fatalf("login: %v %v", resp, err)
	}
	resp.Body.Close()
	if resp, err := http.Get(base + "/"); err != nil || resp.StatusCode != 200 && resp.StatusCode != 404 {
		t.Fatalf("static: %v", err)
	}

	// Graceful shutdown on SIGTERM.
	syscall.Kill(syscall.Getpid(), syscall.SIGTERM)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("serve returned %v", err)
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not shut down")
	}
	logs, err := os.ReadFile(filepath.Join(dir, "logs", "app.log"))
	if err != nil || !strings.Contains(string(logs), "MailRoutine stopped") || strings.Contains(string(logs), "server-test-pw") {
		t.Fatalf("log file: %v %s", err, logs)
	}
}

func TestServeStartupFailures(t *testing.T) {
	t.Setenv("MAILROUTINE_SETUP", "off")
	// Missing configuration.
	for _, k := range []string{"ADMIN_PASSWORD_HASH", "SESSION_SECRET", "ENCRYPTION_KEY", "GOOGLE_CLIENT_ID", "GOOGLE_CLIENT_SECRET"} {
		t.Setenv(k, "")
	}
	if err := serve(); err == nil || !strings.Contains(err.Error(), "ADMIN_PASSWORD_HASH") {
		t.Fatalf("%v", err)
	}
	// Unwritable log path.
	port := freePort(t)
	dir := serveEnv(t, port)
	blocker := filepath.Join(dir, "blocker")
	os.WriteFile(blocker, []byte("x"), 0o600)
	t.Setenv("LOG_FILE_PATH", filepath.Join(blocker, "sub", "app.log"))
	if err := serve(); err == nil || !strings.Contains(err.Error(), "LOG_FILE_PATH") {
		t.Fatalf("%v", err)
	}
	// Database cannot be opened.
	t.Setenv("LOG_FILE_PATH", "")
	t.Setenv("DATABASE_PATH", filepath.Join(blocker, "sub", "m.db"))
	if err := serve(); err == nil || !strings.Contains(err.Error(), "database") {
		t.Fatalf("%v", err)
	}
	// Bad .env file (a directory named .env).
	t.Chdir(t.TempDir())
	os.Mkdir(".env", 0o700)
	if err := serve(); err == nil || !strings.Contains(err.Error(), ".env") {
		t.Fatalf("%v", err)
	}
}

func TestServePortInUse(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	port := l.Addr().(*net.TCPAddr).Port
	serveEnv(t, port)
	done := make(chan error, 1)
	go func() { done <- serve() }()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected listen error")
		}
	case <-time.After(15 * time.Second):
		t.Fatal("serve did not fail")
	}
}

func TestRunDefaultsToServe(t *testing.T) {
	t.Setenv("MAILROUTINE_SETUP", "off")
	for _, k := range []string{"ADMIN_PASSWORD_HASH", "SESSION_SECRET", "ENCRYPTION_KEY", "GOOGLE_CLIENT_ID", "GOOGLE_CLIENT_SECRET"} {
		t.Setenv(k, "")
	}
	code, _, errOut := exec(t, "")
	if code != 1 || !strings.Contains(errOut, "invalid configuration") {
		t.Fatalf("%d %s", code, errOut)
	}
}

func TestHashPasswordInteractivePrompt(t *testing.T) {
	// A pty-less stand-in: force the terminal branch by faking both checks.
	old, oldTTY := readSecret, isTerminal
	defer func() { readSecret, isTerminal = old, oldTTY }()
	isTerminal = func(int) bool { return true }
	answers := []string{"interactive-pw", "interactive-pw"}
	readSecret = func(int) ([]byte, error) { a := answers[0]; answers = answers[1:]; return []byte(a), nil }
	var out, errOut bytes.Buffer
	if err := hashPassword(pipeWith(t, ""), &out, &errOut); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "ADMIN_PASSWORD_HASH='$argon2id$") || !strings.Contains(errOut.String(), "Password: ") || !strings.Contains(errOut.String(), "Confirm password: ") {
		t.Fatalf("%q %q", out.String(), errOut.String())
	}
	// Read failures on either prompt.
	readSecret = func(int) ([]byte, error) { return nil, fmt.Errorf("no tty") }
	if err := hashPassword(pipeWith(t, ""), &out, &errOut); err == nil {
		t.Fatal("first prompt error swallowed")
	}
	calls := 0
	readSecret = func(int) ([]byte, error) {
		calls++
		if calls == 2 {
			return nil, fmt.Errorf("eof")
		}
		return []byte("interactive-pw"), nil
	}
	if err := hashPassword(pipeWith(t, ""), &out, &errOut); err == nil {
		t.Fatal("confirmation prompt error swallowed")
	}
}

func TestServeMaintenanceAndRecoveryAtStartup(t *testing.T) {
	ctx := context.Background()
	port := freePort(t)
	serveEnv(t, port)
	dbPath := os.Getenv("DATABASE_PATH")
	st, err := sqlite.Open(ctx, dbPath)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	// A due routine whose account token cannot be decrypted: exercises the provider wiring and failure reporting.
	acc, _, _ := st.UpsertAccount(ctx, "seed@gmail.com", "v1:not-decryptable")
	due := now.Add(-time.Minute)
	rt, err := st.CreateRoutine(ctx, modelsRoutine(acc.ID, &due))
	if err != nil {
		t.Fatal(err)
	}
	old, _ := st.CreateExecution(ctx, rt.ID, "manual", false, now.Add(-300*24*time.Hour))
	st.FinishExecution(ctx, old.ID, "success", 0, 0, 0, "", now.Add(-300*24*time.Hour))
	st.CreateSession(ctx, sqlite.Session{ID: "expired", TokenHash: "h1", CreatedAt: now.Add(-48 * time.Hour), LastActivityAt: now.Add(-48 * time.Hour),
		ExpiresAt: now.Add(-24 * time.Hour), CredentialFingerprint: "stale-credentials"})
	st.Close()

	done := make(chan error, 1)
	go func() { done <- serve() }()
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	var run *sqlite.Store
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := http.Get(base + "/health"); err == nil {
			resp.Body.Close()
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	// Wait for the startup scheduler tick to process the due routine.
	var failed bool
	for time.Now().Before(deadline) && !failed {
		if run, err = sqlite.Open(ctx, dbPath); err == nil {
			items, _, _ := run.ListExecutions(ctx, sqlite.ExecutionFilter{RoutineID: rt.ID, Status: "failed"})
			failed = len(items) > 0
			if n, _ := run.CountSessions(ctx); failed && n != 0 {
				t.Errorf("stale sessions survived startup: %d", n)
			}
			run.Close()
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !failed {
		t.Fatal("due routine was never executed by the scheduler")
	}
	syscall.Kill(syscall.Getpid(), syscall.SIGTERM)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	chk, _ := sqlite.Open(ctx, dbPath)
	defer chk.Close()
	if _, err := chk.GetExecution(ctx, old.ID); err == nil {
		t.Fatal("execution older than the retention window was not pruned")
	}
}

func modelsRoutine(accountID string, next *time.Time) models.Routine {
	return models.Routine{AccountID: accountID, Name: "seeded", GmailQuery: "label:x", ActionType: models.ActionArchive,
		ScheduleType: models.ScheduleDaily, ScheduleConfig: models.ScheduleConfig{Time: "03:00"}, Enabled: true, MaxMessagesPerRun: 10, NextRunAt: next}
}

func TestUnconfiguredContainerServesSetupPageInsteadOfExiting(t *testing.T) {
	for _, k := range []string{"ADMIN_PASSWORD_HASH", "ADMIN_PASSWORD_HASH_B64", "SESSION_SECRET", "ENCRYPTION_KEY", "GOOGLE_CLIENT_ID", "GOOGLE_CLIENT_SECRET", "MAILROUTINE_SETUP"} {
		t.Setenv(k, "")
	}
	port := freePort(t)
	t.Setenv("HTTP_ADDR", "127.0.0.1")
	t.Setenv("HTTP_PORT", fmt.Sprint(port))
	done := make(chan error, 1)
	go func() { done <- serve() }()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	var body string
	for i := 0; i < 100 && body == ""; i++ {
		if resp, err := http.Get(base + "/"); err == nil {
			b, _ := io.ReadAll(resp.Body)
			resp.Body.Close()
			body = string(b)
		} else {
			time.Sleep(50 * time.Millisecond)
		}
	}
	if !strings.Contains(body, "MailRoutine setup") || !strings.Contains(body, "ADMIN_PASSWORD_HASH") {
		t.Fatalf("setup page not served: %q", body)
	}
	if resp, err := http.Get(base + "/health"); err != nil || resp.StatusCode != 503 {
		t.Fatalf("health must report setup_required: %v %v", resp, err)
	}
	if resp, err := http.Get(base + "/api/dashboard"); err != nil || resp.StatusCode != 404 {
		t.Fatal("the real API must not be exposed in setup mode")
	}
	syscall.Kill(syscall.Getpid(), syscall.SIGTERM)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("clean shutdown expected, got %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("setup server did not stop")
	}
}

func TestSetupModeCanBeDisabledAndRejectsBadPort(t *testing.T) {
	t.Setenv("ADMIN_PASSWORD_HASH", "")
	t.Setenv("MAILROUTINE_SETUP", "off")
	if err := runSetup(errors.New("boom")); err == nil || err.Error() != "boom" {
		t.Fatalf("%v", err)
	}
	t.Setenv("MAILROUTINE_SETUP", "")
	t.Setenv("HTTP_PORT", "99999")
	if err := runSetup(errors.New("boom")); err == nil || err.Error() != "boom" {
		t.Fatalf("an unusable port must surface the original configuration error: %v", err)
	}
}

func TestSetupModeReportsPortInUse(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	t.Setenv("MAILROUTINE_SETUP", "")
	t.Setenv("HTTP_ADDR", "127.0.0.1")
	t.Setenv("HTTP_PORT", fmt.Sprint(l.Addr().(*net.TCPAddr).Port))
	if err := runSetup(errors.New("boom")); err == nil {
		t.Fatal("expected listen error")
	}
}
