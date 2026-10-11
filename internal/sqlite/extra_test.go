package sqlite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/geshas/mailroutine/pkg/models"
)

func TestPingAndInMemory(t *testing.T) {
	ctx := context.Background()
	s, err := Open(ctx, ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err := s.Ping(ctx); err != nil {
		t.Fatal(err)
	}
}

func TestOpenFailures(t *testing.T) {
	ctx := context.Background()
	dir := t.TempDir()
	file := filepath.Join(dir, "plain")
	os.WriteFile(file, []byte("x"), 0o600)
	// Parent is a file: cannot create the directory.
	if _, err := Open(ctx, filepath.Join(file, "sub", "db")); err == nil {
		t.Fatal("expected mkdir failure")
	}
	// A non-database file is rejected on first use.
	junk := filepath.Join(dir, "junk.db")
	os.WriteFile(junk, []byte("this is definitely not sqlite, just text padding to exceed header size........................................................"), 0o600)
	if _, err := Open(ctx, junk); err == nil {
		t.Fatal("garbage database accepted")
	}
}

func TestEveryMethodSurfacesDatabaseErrors(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	acc, _, _ := s.UpsertAccount(ctx, "me@gmail.com", "x")
	rt, _ := s.CreateRoutine(ctx, sampleRoutine(acc.ID))
	ex, _ := s.CreateExecution(ctx, rt.ID, models.TriggerManual, false, time.Now())
	s.Close()

	now := time.Now()
	checks := map[string]func() error{
		"Ping":            func() error { return s.Ping(ctx) },
		"SchemaVersion":   func() error { _, err := s.SchemaVersion(ctx); return err },
		"Backup":          func() error { return s.Backup(ctx, filepath.Join(t.TempDir(), "b.db")) },
		"GetSetting":      func() error { _, err := s.GetSetting(ctx, "k"); return err },
		"SetSetting":      func() error { return s.SetSetting(ctx, "k", "v") },
		"UpsertAccount":   func() error { _, _, err := s.UpsertAccount(ctx, "a@b.c", "x"); return err },
		"GetAccount":      func() error { _, err := s.GetAccount(ctx, acc.ID); return err },
		"ListAccounts":    func() error { _, err := s.ListAccounts(ctx); return err },
		"SetStatus":       func() error { return s.SetAccountStatus(ctx, acc.ID, models.StatusConnected) },
		"DeleteAccount":   func() error { return s.DeleteAccount(ctx, acc.ID) },
		"CreateRoutine":   func() error { _, err := s.CreateRoutine(ctx, sampleRoutine(acc.ID)); return err },
		"UpdateRoutine":   func() error { _, err := s.UpdateRoutine(ctx, rt); return err },
		"GetRoutine":      func() error { _, err := s.GetRoutine(ctx, rt.ID); return err },
		"ListRoutines":    func() error { _, err := s.ListRoutines(ctx, ""); return err },
		"DeleteRoutine":   func() error { return s.DeleteRoutine(ctx, rt.ID) },
		"SetEnabled":      func() error { return s.SetRoutineEnabled(ctx, rt.ID, true, nil) },
		"DueRoutines":     func() error { _, err := s.DueRoutines(ctx, now, now, 5); return err },
		"Claim":           func() error { _, err := s.ClaimRoutine(ctx, rt.ID, "t", now, now); return err },
		"Heartbeat":       func() error { _, err := s.Heartbeat(ctx, rt.ID, "t", now); return err },
		"CompleteRun":     func() error { return s.CompleteRun(ctx, rt.ID, "t", now, nil, true) },
		"ReleaseClaim":    func() error { return s.ReleaseClaim(ctx, rt.ID, "t") },
		"ReleaseAll":      func() error { _, err := s.ReleaseAllClaims(ctx); return err },
		"IsClaimed":       func() error { _, err := s.IsClaimed(ctx, rt.ID, now); return err },
		"CreateExecution": func() error { _, err := s.CreateExecution(ctx, rt.ID, models.TriggerManual, false, now); return err },
		"UpdateProgress":  func() error { return s.UpdateExecutionProgress(ctx, ex.ID, 1, 1, 0) },
		"Finish":          func() error { return s.FinishExecution(ctx, ex.ID, models.ExecSuccess, 1, 1, 0, "", now) },
		"FailRunning":     func() error { _, err := s.FailRunningExecutions(ctx, "r", now); return err },
		"FailStale":       func() error { _, err := s.FailStaleExecutions(ctx, now, "r", now); return err },
		"GetExecution":    func() error { _, err := s.GetExecution(ctx, ex.ID); return err },
		"ListExecutions":  func() error { _, _, err := s.ListExecutions(ctx, ExecutionFilter{}); return err },
		"InsertActions": func() error {
			return s.InsertMessageActions(ctx, ex.ID, []models.MessageActionResult{{MessageID: "a", Action: models.ActionArchive, Status: models.ActionSuccess, CreatedAt: now}})
		},
		"ListActions": func() error { _, err := s.ListMessageActions(ctx, ex.ID, 5, 0); return err },
		"Prune":       func() error { _, err := s.PruneHistory(ctx, now); return err },
		"Dashboard":   func() error { _, err := s.Dashboard(ctx); return err },
		"CreateSession": func() error {
			return s.CreateSession(ctx, Session{ID: "i", TokenHash: "h", CreatedAt: now, LastActivityAt: now, ExpiresAt: now})
		},
		"SessionByHash": func() error { _, err := s.SessionByHash(ctx, "h"); return err },
		"Extend":        func() error { _, err := s.ExtendSession(ctx, "i", now, now, now); return err },
		"DeleteSession": func() error { return s.DeleteSession(ctx, "i") },
		"DeleteExcept":  func() error { _, err := s.DeleteSessionsExcept(ctx, "fp"); return err },
		"PruneSessions": func() error { _, err := s.PruneSessions(ctx, now); return err },
		"CountSessions": func() error { _, err := s.CountSessions(ctx); return err },
	}
	for name, fn := range checks {
		if err := fn(); err == nil {
			t.Errorf("%s: closed database did not produce an error", name)
		}
	}
}

func TestNotFoundPaths(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	for name, err := range map[string]error{
		"SetAccountStatus": s.SetAccountStatus(ctx, "x", models.StatusConnected),
		"SetEnabled":       s.SetRoutineEnabled(ctx, "x", true, nil),
		"DeleteRoutine":    s.DeleteRoutine(ctx, "x"),
		"CompleteRun":      s.CompleteRun(ctx, "x", "t", time.Now(), nil, false),
	} {
		if !errors.Is(err, ErrNotFound) {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := s.GetRoutine(ctx, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := s.UpdateRoutine(ctx, models.Routine{ID: "x", ActionType: models.ActionArchive}); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := s.GetExecution(ctx, "x"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if err := s.InsertMessageActions(ctx, "x", nil); err != nil {
		t.Fatal("empty insert must be a no-op")
	}
	// FK violation inside the transaction rolls everything back.
	err := s.InsertMessageActions(ctx, "no-such-execution", []models.MessageActionResult{{MessageID: "a", Action: models.ActionArchive, Status: models.ActionSuccess, CreatedAt: time.Now()}})
	if err == nil {
		t.Fatal("FK violation accepted")
	}
	if err := s.SetRoutineEnabled(ctx, "x", true, nil); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestStaleExecutionsAndListing(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	acc, _, _ := s.UpsertAccount(ctx, "me@gmail.com", "x")
	r1, _ := s.CreateRoutine(ctx, sampleRoutine(acc.ID))
	r2, _ := s.CreateRoutine(ctx, sampleRoutine(acc.ID))
	now := time.Now()
	hung, _ := s.CreateExecution(ctx, r1.ID, models.TriggerSchedule, false, now)
	live, _ := s.CreateExecution(ctx, r2.ID, models.TriggerSchedule, false, now)
	s.ClaimRoutine(ctx, r2.ID, "t", now, now.Add(-time.Hour)) // r2 has a fresh lease, r1 has none
	if n, err := s.FailStaleExecutions(ctx, now.Add(-time.Minute), "stale", now); err != nil || n != 1 {
		t.Fatalf("%d %v", n, err)
	}
	if x, _ := s.GetExecution(ctx, hung.ID); x.Status != models.ExecFailed {
		t.Fatal("hung run not failed")
	}
	if x, _ := s.GetExecution(ctx, live.ID); x.Status != models.ExecRunning {
		t.Fatal("live run must be untouched")
	}
	if err := s.UpdateExecutionProgress(ctx, live.ID, 10, 7, 3); err != nil {
		t.Fatal(err)
	}
	if x, _ := s.GetExecution(ctx, live.ID); x.MatchedCount != 10 || x.ProcessedCount != 7 || x.FailedCount != 3 {
		t.Fatalf("%+v", x)
	}
	if err := s.ReleaseClaim(ctx, r2.ID, "t"); err != nil {
		t.Fatal(err)
	}
	list, _ := s.ListRoutines(ctx, acc.ID)
	other, _ := s.ListRoutines(ctx, "other-account")
	if len(list) != 2 || len(other) != 0 {
		t.Fatalf("%d %d", len(list), len(other))
	}
	if err := s.DeleteRoutine(ctx, r1.ID); err != nil {
		t.Fatal(err)
	}
	if err := s.SetRoutineEnabled(ctx, r2.ID, false, nil); err != nil {
		t.Fatal(err)
	}
	// Default limit applies when none is given.
	if items, total, _ := s.ListExecutions(ctx, ExecutionFilter{}); total != 1 || len(items) != 1 {
		t.Fatalf("%d", total)
	}
	if !isUnique(errors.New("UNIQUE constraint failed: accounts.email")) || isUnique(nil) {
		t.Fatal("isUnique")
	}
}

func TestCorruptRowsAreReportedNotIgnored(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	acc, _, _ := s.UpsertAccount(ctx, "me@gmail.com", "x")
	r, _ := s.CreateRoutine(ctx, sampleRoutine(acc.ID))
	for _, col := range []string{"action_config", "schedule_config"} {
		if _, err := s.db.ExecContext(ctx, `UPDATE routines SET `+col+` = '{not json' WHERE id = ?`, r.ID); err != nil {
			t.Fatal(err)
		}
		if _, err := s.GetRoutine(ctx, r.ID); err == nil || errors.Is(err, ErrNotFound) {
			t.Errorf("%s: %v", col, err)
		}
		if _, err := s.ListRoutines(ctx, ""); err == nil {
			t.Errorf("%s: list swallowed corruption", col)
		}
		if _, err := s.DueRoutines(ctx, time.Now().Add(48*time.Hour), time.Now(), 5); err == nil {
			t.Errorf("%s: due swallowed corruption", col)
		}
		s.db.ExecContext(ctx, `UPDATE routines SET `+col+` = '{}' WHERE id = ?`, r.ID)
	}
}

func TestFailedMigrationIsRolledBackAndReported(t *testing.T) {
	ctx := context.Background()
	path := filepath.Join(t.TempDir(), "pre.db")
	s, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	// Forget that migration 1 ran: re-applying collides with existing tables.
	s.db.ExecContext(ctx, `DELETE FROM schema_migrations`)
	s.Close()
	if _, err := Open(ctx, path); err == nil {
		t.Fatal("expected migration failure")
	}
}

func TestNextRunLastHandledAndDateFilters(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	acc, _, _ := s.UpsertAccount(ctx, "me@gmail.com", "x")
	soon, later := time.Now().Add(time.Hour), time.Now().Add(5*time.Hour)
	a := sampleRoutine(acc.ID)
	a.Name, a.NextRunAt = "later", &later
	b := sampleRoutine(acc.ID)
	b.Name, b.NextRunAt = "sooner", &soon
	s.CreateRoutine(ctx, a)
	rb, _ := s.CreateRoutine(ctx, b)

	d, err := s.Dashboard(ctx)
	if err != nil || d.NextRun == nil || d.NextRun.RoutineID != rb.ID || d.NextRun.RoutineName != "sooner" || d.NextRun.At.UnixMilli() != soon.UnixMilli() {
		t.Fatalf("%+v %v", d.NextRun, err)
	}

	// Two real runs (latest handled 7), one dry run and one still running must not count.
	base := time.Now().Add(-10 * time.Hour)
	mk := func(off time.Duration, dry bool, st models.ExecutionStatus, handled int) models.Execution {
		x, _ := s.CreateExecution(ctx, rb.ID, models.TriggerSchedule, dry, base.Add(off))
		if st != models.ExecRunning {
			s.FinishExecution(ctx, x.ID, st, handled, handled, 0, "", base.Add(off+time.Second))
		}
		return x
	}
	mk(0, false, models.ExecSuccess, 3)
	mk(time.Hour, false, models.ExecSuccess, 7)
	mk(2*time.Hour, true, models.ExecSuccess, 99)
	mk(3*time.Hour, false, models.ExecRunning, 0)
	got, err := s.LastHandled(ctx)
	if err != nil || len(got) != 1 || got[rb.ID] != 7 {
		t.Fatalf("%v %v", got, err)
	}

	from, to := base.Add(30*time.Minute), base.Add(150*time.Minute)
	items, total, _ := s.ListExecutions(ctx, ExecutionFilter{From: &from, To: &to})
	if total != 2 || len(items) != 2 {
		t.Fatalf("range filter: %d", total)
	}
	items, total, _ = s.ListExecutions(ctx, ExecutionFilter{From: &to})
	if total != 1 {
		t.Fatalf("from only: %d %v", total, items)
	}
}

func TestDatabaseFilesAreOwnerOnly(t *testing.T) {
	s, path := openTemp(t)
	s.SetSetting(context.Background(), "k", "v") // force a write so the WAL exists
	for _, p := range []string{path, path + "-wal", path + "-shm"} {
		st, err := os.Stat(p)
		if err != nil {
			continue // -shm/-wal may not exist depending on checkpointing
		}
		if st.Mode().Perm()&0o077 != 0 {
			t.Errorf("%s is accessible to group/others: %v", p, st.Mode().Perm())
		}
	}
}

func TestTraceabilityColumn(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	acc, _, _ := s.UpsertAccount(ctx, "me@gmail.com", "x")
	on := sampleRoutine(acc.ID)
	on.Traceability = true
	r, _ := s.CreateRoutine(ctx, on)
	off := sampleRoutine(acc.ID)
	off.Name = "off"
	r2, _ := s.CreateRoutine(ctx, off)
	if g, _ := s.GetRoutine(ctx, r.ID); !g.Traceability {
		t.Fatal("true not stored")
	}
	if g, _ := s.GetRoutine(ctx, r2.ID); g.Traceability {
		t.Fatal("false not stored")
	}
	r2.Traceability = true
	if u, _ := s.UpdateRoutine(ctx, r2); !u.Traceability {
		t.Fatal("update lost the flag")
	}
	// Rows created without the column (older writers) default to tracing on.
	_, err := s.db.ExecContext(ctx, `INSERT INTO routines (id, account_id, name, gmail_query, action_type, schedule_type, max_messages_per_run, created_at, updated_at)
		VALUES ('legacy', ?, 'legacy', 'q', 'archive', 'manual', 5, 1, 1)`, acc.ID)
	if err != nil {
		t.Fatal(err)
	}
	if g, _ := s.GetRoutine(ctx, "legacy"); !g.Traceability {
		t.Fatal("default must be on")
	}
}
