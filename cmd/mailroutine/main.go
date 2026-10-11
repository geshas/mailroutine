// Command mailroutine is the self-hosted MailRoutine server and its helper CLI.
package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	_ "time/tzdata" // embed the timezone database so TZ works in minimal images

	"golang.org/x/term"

	"github.com/geshas/mailroutine/internal/accounts"
	"github.com/geshas/mailroutine/internal/auth"
	"github.com/geshas/mailroutine/internal/config"
	httpapi "github.com/geshas/mailroutine/internal/http"
	"github.com/geshas/mailroutine/internal/logging"
	"github.com/geshas/mailroutine/internal/oauth"
	"github.com/geshas/mailroutine/internal/runner"
	"github.com/geshas/mailroutine/internal/scheduler"
	"github.com/geshas/mailroutine/internal/security"
	"github.com/geshas/mailroutine/internal/setup"
	"github.com/geshas/mailroutine/internal/sqlite"
	"github.com/geshas/mailroutine/pkg/engine"
	"github.com/geshas/mailroutine/pkg/models"
	"github.com/geshas/mailroutine/web"
)

// version is overridden at build time: -ldflags "-X main.version=v0.1.0".
var version = "dev"

// readSecret reads a line without echo (replaced in tests).
var (
	readSecret = term.ReadPassword
	isTerminal = term.IsTerminal
)

const historyRetention = 180 * 24 * time.Hour

const usage = `MailRoutine - scheduled automation for Gmail

Usage:
  mailroutine [serve]            run the server (default)
  mailroutine hash-password      create an Argon2id hash for ADMIN_PASSWORD_HASH
  mailroutine generate-secret    print a random 32-byte base64 secret
  mailroutine backup <file>      write a consistent copy of the database
  mailroutine healthcheck        exit 0 if the local server is healthy
  mailroutine version            print the version
`

func main() {
	os.Exit(run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr))
}

func run(args []string, stdin *os.File, stdout, stderr io.Writer) int {
	cmd := "serve"
	if len(args) > 0 {
		cmd = args[0]
	}
	var err error
	switch cmd {
	case "serve":
		err = serve()
	case "hash-password":
		err = hashPassword(stdin, stdout, stderr)
	case "generate-secret":
		var s string
		if s, err = security.GenerateSecret(); err == nil {
			fmt.Fprintln(stdout, s)
		}
	case "backup":
		if len(args) != 2 {
			fmt.Fprintln(stderr, "usage: mailroutine backup <destination-file>")
			return 2
		}
		err = backup(args[1], stdout)
	case "healthcheck":
		err = healthcheck()
	case "version", "--version", "-v":
		fmt.Fprintln(stdout, version)
	case "help", "--help", "-h":
		fmt.Fprint(stdout, usage)
	default:
		fmt.Fprintf(stderr, "unknown command %q\n\n%s", cmd, usage)
		return 2
	}
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	return 0
}

// hashPassword prompts twice without echo (or reads two lines when stdin is not
// a terminal, which allows scripted use) and prints an Argon2id PHC hash.
func hashPassword(stdin *os.File, stdout, stderr io.Writer) error {
	var pw, confirm string
	if isTerminal(int(stdin.Fd())) {
		fmt.Fprint(stderr, "Password: ")
		a, err := readSecret(int(stdin.Fd()))
		fmt.Fprintln(stderr)
		if err != nil {
			return err
		}
		fmt.Fprint(stderr, "Confirm password: ")
		b, err := readSecret(int(stdin.Fd()))
		fmt.Fprintln(stderr)
		if err != nil {
			return err
		}
		pw, confirm = string(a), string(b)
	} else {
		sc := bufio.NewScanner(stdin)
		sc.Buffer(make([]byte, 0, 4096), 1<<20)
		if !sc.Scan() {
			return errors.New("no password supplied on stdin (run in a terminal, or use docker compose run without -T)")
		}
		pw = sc.Text()
		if !sc.Scan() {
			return errors.New("password confirmation missing: pipe the password twice, one per line")
		}
		confirm = sc.Text()
	}
	if pw != confirm {
		return errors.New("passwords do not match")
	}
	if len(pw) < 8 {
		return errors.New("password must be at least 8 characters")
	}
	hash, err := security.HashPassword(pw)
	if err != nil {
		return err
	}
	fmt.Fprintln(stderr, "\nAdd this line to your .env file (keep the single quotes so '$' is preserved):")
	fmt.Fprintf(stdout, "ADMIN_PASSWORD_HASH='%s'\n", hash)
	return nil
}

func backup(dest string, stdout io.Writer) error {
	_ = config.LoadDotEnv(".env")
	dbPath := os.Getenv("DATABASE_PATH")
	if dbPath == "" {
		dbPath = "/data/mailroutine.db"
	}
	if _, err := os.Stat(dbPath); err != nil {
		return fmt.Errorf("database %q not found: %w", dbPath, err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	st, err := sqlite.Open(ctx, dbPath)
	if err != nil {
		return err
	}
	defer st.Close()
	if err := st.Backup(ctx, dest); err != nil {
		return err
	}
	fmt.Fprintf(stdout, "backup written to %s\nRemember: it can only be used together with the same ENCRYPTION_KEY.\n", dest)
	return nil
}

func healthcheck() error {
	_ = config.LoadDotEnv(".env")
	port := os.Getenv("HTTP_PORT")
	if port == "" {
		port = "8080"
	}
	c := &http.Client{Timeout: 4 * time.Second}
	resp, err := c.Get("http://127.0.0.1:" + port + "/health")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("health endpoint returned HTTP %d", resp.StatusCode)
	}
	return nil
}

func serve() error {
	if err := config.LoadDotEnv(".env"); err != nil {
		return fmt.Errorf("reading .env: %w", err)
	}
	cfg, err := config.Load()
	if err != nil {
		return runSetup(err)
	}
	logger, closeLog, err := logging.New(logging.Options{
		Level: cfg.LogLevel, Format: cfg.LogFormat, FilePath: cfg.LogFilePath,
		MaxSizeMB: cfg.LogMaxSizeMB, MaxBackups: cfg.LogMaxBackups, MaxAgeDays: cfg.LogMaxAgeDays, Compress: cfg.LogCompress,
	})
	if err != nil {
		return err
	}
	defer closeLog.Close()
	slog.SetDefault(logger)
	log := logger.With("component", "main")

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	st, err := sqlite.Open(ctx, cfg.DatabasePath)
	if err != nil {
		return fmt.Errorf("database: %w", err)
	}
	defer st.Close()

	authSvc := auth.New(st, auth.Settings{
		Username: cfg.AdminUsername, PasswordHash: cfg.AdminPasswordHash, Secret: cfg.SessionSecret,
		IdleTimeout: cfg.SessionIdleTimeout, RenewThreshold: cfg.SessionRenewalThreshold, AbsoluteTimeout: cfg.SessionAbsoluteTimeout,
	})
	if n, err := authSvc.InvalidateStaleSessions(ctx); err != nil {
		return err
	} else if n > 0 {
		log.Info("administrator credentials changed; ended existing sessions", "sessions", n)
	}

	google := oauth.New(cfg.GoogleClientID, cfg.GoogleClientSecret, cfg.GoogleRedirectURL)
	acctMgr := &accounts.Manager{Store: st, Google: google, Key: cfg.EncryptionKey, Timeout: cfg.GmailRequestTimeout,
		Logger: logger}

	run := runner.New(runner.Options{
		Store: st, Logger: logger, MaxConcurrent: cfg.MaxConcurrentRoutines, BatchSize: cfg.GmailBatchSize,
		Provider: func(ctx context.Context, acc models.Account) (engine.MailProvider, error) {
			return acctMgr.Provider(ctx, acc)
		},
		OnAuthRevoked: func(ctx context.Context, accountID string) {
			if err := acctMgr.MarkNeedsReauth(ctx, accountID); err == nil {
				log.Warn("gmail authorization was revoked or expired; reconnect the account", "account_id", accountID)
			}
		},
	})
	if err := run.Recover(ctx); err != nil {
		return fmt.Errorf("startup recovery: %w", err)
	}

	maintenance := func(ctx context.Context) {
		if n, err := authSvc.Prune(ctx); err != nil {
			log.Error("pruning sessions failed", "error", err)
		} else if n > 0 {
			log.Debug("pruned expired sessions", "count", n)
		}
		if n, err := st.PruneHistory(ctx, time.Now().Add(-historyRetention)); err != nil {
			log.Error("pruning history failed", "error", err)
		} else if n > 0 {
			log.Info("pruned old run history", "rows", n)
		}
	}
	maintenance(ctx)

	sched := &scheduler.Scheduler{Store: st, Runner: run, Interval: cfg.SchedulerInterval, Logger: logger, Maintenance: maintenance}
	schedDone := make(chan struct{})
	go func() { defer close(schedDone); sched.Run(ctx) }()

	srv := httpapi.New(httpapi.Deps{
		Config: cfg, Store: st, Auth: authSvc, Limiter: auth.NewLoginLimiter(5, 50, 15*time.Minute),
		Accounts: acctMgr, Google: google, Runner: run, Logger: logger, Version: version, Static: web.FS(),
	})
	httpSrv := &http.Server{
		Addr: cfg.Addr(), Handler: srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 30 * time.Second, WriteTimeout: 2 * time.Minute, IdleTimeout: 2 * time.Minute,
		ErrorLog: slog.NewLogLogger(logger.Handler().WithAttrs([]slog.Attr{slog.String("component", "http")}), slog.LevelWarn),
	}
	log.Info("MailRoutine starting", "version", version, "addr", cfg.Addr(), "app_url", cfg.AppURL.String(),
		"database", filepath.Clean(cfg.DatabasePath), "timezone", cfg.Timezone, "file_logging", cfg.LogFilePath != "")

	errCh := make(chan error, 1)
	go func() {
		if err := httpSrv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	var serveErr error
	select {
	case <-ctx.Done():
		log.Info("shutdown requested")
	case serveErr = <-errCh:
		stop()
		log.Error("http server failed", "error", serveErr)
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	_ = httpSrv.Shutdown(shutdownCtx)
	<-schedDone
	run.Shutdown(shutdownCtx)
	log.Info("MailRoutine stopped")
	return serveErr
}

// runSetup is what an unconfigured container does instead of exiting: it reports the problems
// and serves the stateless setup page, so people without a shell (Portainer, NAS UIs) can
// generate the password hash and secrets. Set MAILROUTINE_SETUP=off to fail fast instead.
func runSetup(problems error) error {
	if strings.EqualFold(os.Getenv("MAILROUTINE_SETUP"), "off") {
		return problems
	}
	host, port := os.Getenv("HTTP_ADDR"), os.Getenv("HTTP_PORT")
	if host == "" {
		host = "0.0.0.0"
	}
	if port == "" {
		port = "8080"
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return problems // cannot even listen; report the original error
	}
	fmt.Fprintln(os.Stderr, "error:", problems)
	fmt.Fprintf(os.Stderr, "\nMailRoutine is not configured yet. Serving the setup page on %s:%s (open it in a browser).\n"+
		"Set the missing variables and restart; set MAILROUTINE_SETUP=off to exit instead.\n", host, port)

	srv := &http.Server{
		Addr: net.JoinHostPort(host, port), Handler: setup.Handler(problems.Error(), version),
		ReadHeaderTimeout: 10 * time.Second, ReadTimeout: 15 * time.Second, WriteTimeout: 30 * time.Second, IdleTimeout: time.Minute,
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()
	select {
	case <-ctx.Done():
	case err := <-errCh:
		return err
	}
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return srv.Shutdown(shutdownCtx)
}
