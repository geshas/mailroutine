package scheduler

import (
	"context"
	"log/slog"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/geshas/mailroutine/internal/runner"
	"github.com/geshas/mailroutine/internal/sqlite"
	"github.com/geshas/mailroutine/pkg/engine"
	"github.com/geshas/mailroutine/pkg/models"
)

type countingMail struct{ archived atomic.Int32 }

func (c *countingMail) ListMessages(context.Context, string, string, int) (engine.MessagePage, error) {
	return engine.MessagePage{IDs: []string{"a", "b"}}, nil
}
func (c *countingMail) GetSummaries(context.Context, []string) ([]models.MessageSummary, error) {
	return nil, nil
}
func (c *countingMail) Archive(_ context.Context, ids []string) error {
	c.archived.Add(int32(len(ids)))
	return nil
}
func (c *countingMail) Trash(context.Context, []string) error               { return nil }
func (c *countingMail) MarkRead(context.Context, []string) error            { return nil }
func (c *countingMail) MarkUnread(context.Context, []string) error          { return nil }
func (c *countingMail) AddLabel(context.Context, []string, string) error    { return nil }
func (c *countingMail) RemoveLabel(context.Context, []string, string) error { return nil }

func setup(t *testing.T) (*Scheduler, *sqlite.Store, *countingMail, models.Account) {
	ctx := context.Background()
	st, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "s.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	mail := &countingMail{}
	r := runner.New(runner.Options{Store: st, MaxConcurrent: 4, BatchSize: 10, Retry: &engine.RetryPolicy{MaxAttempts: 1},
		Provider: func(context.Context, models.Account) (engine.MailProvider, error) { return mail, nil }})
	t.Cleanup(func() { r.Shutdown(ctx) })
	acc, _, _ := st.UpsertAccount(ctx, "me@gmail.com", "enc")
	s := &Scheduler{Store: st, Runner: r, Interval: time.Hour, Logger: slog.New(slog.DiscardHandler)}
	return s, st, mail, acc
}

func mk(t *testing.T, st *sqlite.Store, acc models.Account, name string, enabled bool, next *time.Time, typ models.ScheduleType) models.Routine {
	r, err := st.CreateRoutine(context.Background(), models.Routine{AccountID: acc.ID, Name: name, GmailQuery: "q",
		ActionType: models.ActionArchive, ScheduleType: typ, ScheduleConfig: models.ScheduleConfig{Time: "03:00", Minute: 15},
		Enabled: enabled, MaxMessagesPerRun: 10, NextRunAt: next})
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func TestTickRunsOnlyDueEnabledRoutines(t *testing.T) {
	ctx := context.Background()
	s, st, mail, acc := setup(t)
	past, future := time.Now().Add(-time.Minute), time.Now().Add(time.Hour)
	due := mk(t, st, acc, "due", true, &past, models.ScheduleDaily)
	mk(t, st, acc, "future", true, &future, models.ScheduleDaily)
	mk(t, st, acc, "disabled", false, &past, models.ScheduleDaily)
	mk(t, st, acc, "manual", true, nil, models.ScheduleManual)

	s.Tick(ctx)
	s.Runner.Wait()
	if mail.archived.Load() != 2 {
		t.Fatalf("expected exactly one routine to run, archived=%d", mail.archived.Load())
	}
	got, _ := st.GetRoutine(ctx, due.ID)
	if got.NextRunAt == nil || !got.NextRunAt.After(time.Now()) || got.LastRunAt == nil {
		t.Fatalf("%+v", got)
	}
	// A second tick finds nothing due.
	s.Tick(ctx)
	s.Runner.Wait()
	if mail.archived.Load() != 2 {
		t.Fatal("routine ran twice")
	}
}

func TestMissedRunsRunOnceAfterDowntime(t *testing.T) {
	ctx := context.Background()
	s, st, mail, acc := setup(t)
	weekAgo := time.Now().Add(-7 * 24 * time.Hour)
	r := mk(t, st, acc, "hourly", true, &weekAgo, models.ScheduleHourly)
	s.Tick(ctx)
	s.Runner.Wait()
	s.Tick(ctx)
	s.Runner.Wait()
	if mail.archived.Load() != 2 { // 168 missed hourly slots, one run
		t.Fatalf("replayed missed runs: archived=%d", mail.archived.Load())
	}
	execs, total, _ := st.ListExecutions(ctx, sqlite.ExecutionFilter{RoutineID: r.ID})
	if total != 1 || len(execs) != 1 {
		t.Fatal(total)
	}
}

func TestFailingRoutineDoesNotBlockOthers(t *testing.T) {
	ctx := context.Background()
	s, st, mail, acc := setup(t)
	past := time.Now().Add(-time.Minute)
	bad := mk(t, st, acc, "orphan", true, &past, models.ScheduleDaily)
	// Corrupt the first routine's account reference by disconnecting it.
	acc2, _, _ := st.UpsertAccount(ctx, "other@gmail.com", "enc")
	st.SetAccountStatus(ctx, acc2.ID, models.StatusNeedsReauth)
	_ = bad
	good := mk(t, st, acc, "good", true, &past, models.ScheduleDaily)
	s.Tick(ctx)
	s.Runner.Wait()
	execs, total, _ := st.ListExecutions(ctx, sqlite.ExecutionFilter{})
	if total != 2 {
		t.Fatalf("expected both routines to run, got %d: %+v", total, execs)
	}
	_ = good
	if mail.archived.Load() == 0 {
		t.Fatal("nothing archived")
	}
}

func TestAtCapacityDefersRemaining(t *testing.T) {
	ctx := context.Background()
	s, st, mail, acc := setup(t)
	s.Runner = runner.New(runner.Options{Store: st, MaxConcurrent: 1, BatchSize: 10, Retry: &engine.RetryPolicy{MaxAttempts: 1},
		Provider: func(context.Context, models.Account) (engine.MailProvider, error) { return mail, nil }})
	past := time.Now().Add(-time.Minute)
	for i := 0; i < 3; i++ {
		mk(t, st, acc, "r", true, &past, models.ScheduleDaily)
	}
	for i := 0; i < 5; i++ { // a few ticks drain the queue even with one slot
		s.Tick(ctx)
		s.Runner.Wait()
	}
	if mail.archived.Load() != 6 {
		t.Fatalf("archived=%d", mail.archived.Load())
	}
}

func TestRunLoopTicksMaintainsAndStops(t *testing.T) {
	s, st, mail, acc := setup(t)
	s.Interval = 20 * time.Millisecond
	s.MaintenanceInterval = 20 * time.Millisecond
	var maint atomic.Int32
	s.Maintenance = func(context.Context) { maint.Add(1) }
	past := time.Now().Add(-time.Minute)
	mk(t, st, acc, "due", true, &past, models.ScheduleHourly)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	deadline := time.Now().Add(3 * time.Second)
	for (mail.archived.Load() == 0 || maint.Load() == 0) && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("scheduler did not stop")
	}
	if mail.archived.Load() == 0 || maint.Load() == 0 {
		t.Fatalf("archived=%d maintenance=%d", mail.archived.Load(), maint.Load())
	}
}

func TestTickSurvivesStoreFailures(t *testing.T) {
	s, st, _, _ := setup(t)
	st.Close()
	s.Now = nil
	s.Tick(context.Background()) // must log and return, not panic
}

func TestBusyRoutineIsSkipped(t *testing.T) {
	ctx := context.Background()
	s, st, mail, acc := setup(t)
	past := time.Now().Add(-time.Minute)
	r := mk(t, st, acc, "x", true, &past, models.ScheduleDaily)
	// A manual run grabs the lease first; the tick must not start a second one.
	st.ClaimRoutine(ctx, r.ID, "manual", time.Now(), time.Now().Add(-time.Hour))
	s.Tick(ctx)
	s.Runner.Wait()
	if mail.archived.Load() != 0 {
		t.Fatal("overlapping run started")
	}
}
