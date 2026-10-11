package sqlite

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/geshas/mailroutine/pkg/models"
)

func openTemp(t *testing.T) (*Store, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sub", "test.db")
	s, err := Open(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { s.Close() })
	return s, path
}

func sampleRoutine(accountID string) models.Routine {
	next := time.Now().Add(time.Hour).UTC()
	return models.Routine{AccountID: accountID, Name: "Archive news", GmailQuery: "label:news older_than:7d",
		ActionType: models.ActionAddLabel, ActionConfig: models.ActionConfig{Label: "Done"},
		ScheduleType: models.ScheduleDaily, ScheduleConfig: models.ScheduleConfig{Time: "03:00", Timezone: "Europe/Berlin"},
		Enabled: true, MaxMessagesPerRun: 100, NextRunAt: &next}
}

func TestMigrationsIdempotentAndForeignKeys(t *testing.T) {
	ctx := context.Background()
	s, path := openTemp(t)
	v, err := s.SchemaVersion(ctx)
	if err != nil || v != 2 {
		t.Fatalf("version %d %v", v, err)
	}
	s.Close()
	s2, err := Open(ctx, path) // reopen: migrations must not re-run
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	if v, _ := s2.SchemaVersion(ctx); v != 2 {
		t.Fatalf("version after reopen %d", v)
	}
	// Foreign keys enforced: routine for a missing account must fail.
	if _, err := s2.CreateRoutine(ctx, sampleRoutine("missing")); err == nil {
		t.Fatal("foreign key not enforced")
	}
}

func TestAccountsCRUDAndReconnect(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	a, created, err := s.UpsertAccount(ctx, "me@gmail.com", "v1:enc")
	if err != nil || !created {
		t.Fatal(err, created)
	}
	if err := s.SetAccountStatus(ctx, a.ID, models.StatusNeedsReauth); err != nil {
		t.Fatal(err)
	}
	a2, created, err := s.UpsertAccount(ctx, "me@gmail.com", "v1:enc2")
	if err != nil || created || a2.ID != a.ID || a2.ConnectionStatus != models.StatusConnected || a2.EncryptedRefreshToken != "v1:enc2" {
		t.Fatalf("%+v created=%v err=%v", a2, created, err)
	}
	list, _ := s.ListAccounts(ctx)
	if len(list) != 1 {
		t.Fatal(len(list))
	}
	if err := s.DeleteAccount(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	if _, err := s.GetAccount(ctx, "nope"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
}

func TestRoutineCRUDAndCascade(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	acc, _, _ := s.UpsertAccount(ctx, "me@gmail.com", "x")
	r, err := s.CreateRoutine(ctx, sampleRoutine(acc.ID))
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.GetRoutine(ctx, r.ID)
	if err != nil || got.ActionConfig.Label != "Done" || got.ScheduleConfig.Timezone != "Europe/Berlin" || !got.Enabled || got.NextRunAt == nil {
		t.Fatalf("%+v %v", got, err)
	}
	got.Name = "Renamed"
	got.Enabled = false
	got.NextRunAt = nil
	upd, err := s.UpdateRoutine(ctx, got)
	if err != nil || upd.Name != "Renamed" || upd.Enabled || upd.NextRunAt != nil {
		t.Fatalf("%+v %v", upd, err)
	}
	// Invalid enum rejected by CHECK constraint.
	bad := sampleRoutine(acc.ID)
	bad.ActionType = "nuke"
	if _, err := s.CreateRoutine(ctx, bad); err == nil {
		t.Fatal("CHECK constraint missing")
	}
	exec, _ := s.CreateExecution(ctx, r.ID, models.TriggerManual, false, time.Now())
	if err := s.DeleteAccount(ctx, acc.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.GetRoutine(ctx, r.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("routine should cascade")
	}
	if _, err := s.GetExecution(ctx, exec.ID); !errors.Is(err, ErrNotFound) {
		t.Fatal("execution should cascade")
	}
}

func TestClaimsAreExclusiveAndRecoverWhenStale(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	acc, _, _ := s.UpsertAccount(ctx, "me@gmail.com", "x")
	r, _ := s.CreateRoutine(ctx, sampleRoutine(acc.ID))
	now := time.Now()
	stale := now.Add(-10 * time.Minute)

	// Many concurrent claimers: exactly one wins.
	var wins atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ok, err := s.ClaimRoutine(ctx, r.ID, string(rune('a'+i)), now, stale)
			if err != nil {
				t.Error(err)
			}
			if ok {
				wins.Add(1)
			}
		}(i)
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d claimers won", wins.Load())
	}
	if claimed, _ := s.IsClaimed(ctx, r.ID, stale); !claimed {
		t.Fatal("should be claimed")
	}
	// Still held: a new claim fails.
	if ok, _ := s.ClaimRoutine(ctx, r.ID, "late", now.Add(time.Minute), stale); ok {
		t.Fatal("claim stolen while lease fresh")
	}
	// Stale lease can be taken over.
	if ok, _ := s.ClaimRoutine(ctx, r.ID, "recover", now.Add(20*time.Minute), now.Add(10*time.Minute)); !ok {
		t.Fatal("stale lease not recoverable")
	}
	if ok, _ := s.Heartbeat(ctx, r.ID, "recover", now.Add(21*time.Minute)); !ok {
		t.Fatal("heartbeat failed")
	}
	if ok, _ := s.Heartbeat(ctx, r.ID, "someone-else", now); ok {
		t.Fatal("foreign heartbeat accepted")
	}
	// Complete only works for the holder and releases the lease.
	next := now.Add(24 * time.Hour)
	if err := s.CompleteRun(ctx, r.ID, "wrong", now, &next, true); !errors.Is(err, ErrNotFound) {
		t.Fatal("completed with wrong token")
	}
	if err := s.CompleteRun(ctx, r.ID, "recover", now, &next, true); err != nil {
		t.Fatal(err)
	}
	got, _ := s.GetRoutine(ctx, r.ID)
	if got.LastRunAt == nil || got.NextRunAt == nil || got.NextRunAt.UnixMilli() != next.UnixMilli() {
		t.Fatalf("%+v", got)
	}
	if ok, _ := s.ClaimRoutine(ctx, r.ID, "again", now, stale); !ok {
		t.Fatal("not released")
	}
	if n, _ := s.ReleaseAllClaims(ctx); n != 1 {
		t.Fatalf("released %d", n)
	}
}

func TestDueRoutines(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	acc, _, _ := s.UpsertAccount(ctx, "me@gmail.com", "x")
	now := time.Now().UTC()
	past, future := now.Add(-time.Minute), now.Add(time.Hour)
	mk := func(name string, enabled bool, next *time.Time) models.Routine {
		r := sampleRoutine(acc.ID)
		r.Name, r.Enabled, r.NextRunAt = name, enabled, next
		r, err := s.CreateRoutine(ctx, r)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	due := mk("due", true, &past)
	mk("future", true, &future)
	mk("disabled", false, &past)
	mk("manual", true, nil)
	claimed := mk("claimed", true, &past)
	s.ClaimRoutine(ctx, claimed.ID, "t", now, now.Add(-time.Minute))
	got, err := s.DueRoutines(ctx, now, now.Add(-10*time.Minute), 10)
	if err != nil || len(got) != 1 || got[0].ID != due.ID {
		t.Fatalf("%+v %v", got, err)
	}
	// After the lease turns stale the claimed routine becomes due again.
	got, _ = s.DueRoutines(ctx, now.Add(time.Hour), now.Add(30*time.Minute), 10)
	if len(got) != 3 { // due, disabled excluded, claimed (stale), future (now due)
		t.Fatalf("got %d", len(got))
	}
}

func TestExecutionsAndDashboard(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	acc, _, _ := s.UpsertAccount(ctx, "me@gmail.com", "x")
	r, _ := s.CreateRoutine(ctx, sampleRoutine(acc.ID))
	now := time.Now()

	real, _ := s.CreateExecution(ctx, r.ID, models.TriggerSchedule, false, now)
	s.InsertMessageActions(ctx, real.ID, []models.MessageActionResult{
		{MessageID: "a", Action: models.ActionArchive, Status: models.ActionSuccess, CreatedAt: now},
		{MessageID: "b", Action: models.ActionArchive, Status: models.ActionSuccess, CreatedAt: now},
		{MessageID: "c", Action: models.ActionArchive, Status: models.ActionFailed, Error: "boom", CreatedAt: now},
	})
	s.FinishExecution(ctx, real.ID, models.ExecPartial, 3, 2, 1, "one failed", now.Add(time.Second))
	dry, _ := s.CreateExecution(ctx, r.ID, models.TriggerManual, true, now.Add(time.Minute))
	s.InsertMessageActions(ctx, dry.ID, []models.MessageActionResult{{MessageID: "z", Action: models.ActionArchive, Status: models.ActionDryRun, CreatedAt: now}})
	s.FinishExecution(ctx, dry.ID, models.ExecSuccess, 1, 0, 0, "", now.Add(time.Minute))

	d, err := s.Dashboard(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if d.TotalRuns != 1 || d.EmailsHandled != 2 || d.EnabledRoutines != 1 || d.ConnectedAccounts != 1 || len(d.RecentRuns) != 2 {
		t.Fatalf("%+v", d)
	}
	if d.LastRun == nil || d.LastRun.ID != real.ID || d.LastRun.RoutineName != "Archive news" || d.NextRun == nil {
		t.Fatalf("%+v", d.LastRun)
	}

	list, total, _ := s.ListExecutions(ctx, ExecutionFilter{RoutineID: r.ID, Status: models.ExecPartial})
	if total != 1 || len(list) != 1 || list[0].FailedCount != 1 || list[0].FinishedAt == nil {
		t.Fatalf("%+v %d", list, total)
	}
	page, total, _ := s.ListExecutions(ctx, ExecutionFilter{Limit: 1, Offset: 1})
	if total != 2 || len(page) != 1 || page[0].ID != real.ID {
		t.Fatalf("pagination: %+v %d", page, total)
	}
	acts, _ := s.ListMessageActions(ctx, real.ID, 10, 0)
	if len(acts) != 3 || acts[0].Status != models.ActionFailed {
		t.Fatalf("failures should sort first: %+v", acts)
	}

	// Crash recovery.
	crashed, _ := s.CreateExecution(ctx, r.ID, models.TriggerSchedule, false, now)
	if n, _ := s.FailRunningExecutions(ctx, "interrupted", now); n != 1 {
		t.Fatal(n)
	}
	x, _ := s.GetExecution(ctx, crashed.ID)
	if x.Status != models.ExecFailed || x.ErrorMessage != "interrupted" {
		t.Fatalf("%+v", x)
	}
	// Retention.
	if n, _ := s.PruneHistory(ctx, now.Add(time.Hour)); n == 0 {
		t.Fatal("nothing pruned")
	}
}

func TestSessionsPersistAndExtendSafely(t *testing.T) {
	ctx := context.Background()
	s, path := openTemp(t)
	now := time.Now().UTC().Truncate(time.Millisecond)
	sess := Session{ID: "s1", TokenHash: "h1", CreatedAt: now, LastActivityAt: now, ExpiresAt: now.Add(30 * time.Minute), CredentialFingerprint: "fp"}
	if err := s.CreateSession(ctx, sess); err != nil {
		t.Fatal(err)
	}
	// Survives restart.
	s.Close()
	s2, err := Open(ctx, path)
	if err != nil {
		t.Fatal(err)
	}
	defer s2.Close()
	got, err := s2.SessionByHash(ctx, "h1")
	if err != nil || got.ID != "s1" || !got.ExpiresAt.Equal(sess.ExpiresAt) || got.AbsoluteExpiresAt != nil {
		t.Fatalf("%+v %v", got, err)
	}
	// Concurrent renewals with the same observed state: exactly one applies.
	var wins atomic.Int32
	var wg sync.WaitGroup
	later := now.Add(10 * time.Minute)
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if ok, err := s2.ExtendSession(ctx, "s1", later, later.Add(30*time.Minute), now); err == nil && ok {
				wins.Add(1)
			}
		}()
	}
	wg.Wait()
	if wins.Load() != 1 {
		t.Fatalf("%d renewals applied", wins.Load())
	}
	// Expired sessions cannot be extended.
	if ok, _ := s2.ExtendSession(ctx, "s1", later.Add(2*time.Hour), later.Add(3*time.Hour), later); ok {
		t.Fatal("expired session extended")
	}
	if _, err := s2.SessionByHash(ctx, "missing"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	s2.CreateSession(ctx, Session{ID: "s2", TokenHash: "h2", CreatedAt: now, LastActivityAt: now, ExpiresAt: now.Add(time.Hour), CredentialFingerprint: "other"})
	if n, _ := s2.DeleteSessionsExcept(ctx, "fp"); n != 1 {
		t.Fatalf("fingerprint purge removed %d", n)
	}
	if n, _ := s2.PruneSessions(ctx, now.Add(24*time.Hour)); n != 1 {
		t.Fatalf("pruned %d", n)
	}
	if c, _ := s2.CountSessions(ctx); c != 0 {
		t.Fatal(c)
	}
}

func TestSettings(t *testing.T) {
	ctx := context.Background()
	s, _ := openTemp(t)
	if _, err := s.GetSetting(ctx, "timezone"); !errors.Is(err, ErrNotFound) {
		t.Fatal(err)
	}
	s.SetSetting(ctx, "timezone", "UTC")
	s.SetSetting(ctx, "timezone", "Europe/Paris")
	if v, _ := s.GetSetting(ctx, "timezone"); v != "Europe/Paris" {
		t.Fatal(v)
	}
}

func TestBackupIsConsistentSnapshot(t *testing.T) {
	ctx := context.Background()
	s, path := openTemp(t)
	s.SetSetting(ctx, "k", "v")
	dest := filepath.Join(filepath.Dir(path), "backups", "snap.db")
	if err := s.Backup(ctx, dest); err != nil {
		t.Fatal(err)
	}
	if err := s.Backup(ctx, dest); err == nil {
		t.Fatal("must refuse to overwrite")
	}
	b, err := Open(ctx, dest)
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	if v, _ := b.GetSetting(ctx, "k"); v != "v" {
		t.Fatalf("backup missing data: %q", v)
	}
	if st, _ := os.Stat(dest); st.Mode().Perm() != 0o600 {
		t.Fatalf("mode %v", st.Mode())
	}
}
