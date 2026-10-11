package runner

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/geshas/mailroutine/internal/sqlite"
	"github.com/geshas/mailroutine/pkg/engine"
	"github.com/geshas/mailroutine/pkg/models"
)

type fakeMail struct {
	mu       sync.Mutex
	ids      []string
	archived []string
	gate     chan struct{} // if set, Archive blocks until closed
	started  chan struct{}
	err      error
	more     bool // pretend another page exists
}

func (f *fakeMail) ListMessages(ctx context.Context, q, tok string, n int) (engine.MessagePage, error) {
	page := engine.MessagePage{IDs: f.ids}
	if f.more {
		page.NextPageToken = "next"
	}
	return page, nil
}
func (f *fakeMail) GetSummaries(ctx context.Context, ids []string) ([]models.MessageSummary, error) {
	return nil, nil
}
func (f *fakeMail) Archive(ctx context.Context, ids []string) error {
	if f.started != nil {
		select {
		case f.started <- struct{}{}:
		default:
		}
	}
	if f.gate != nil {
		select {
		case <-f.gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if f.err != nil {
		return f.err
	}
	f.mu.Lock()
	f.archived = append(f.archived, ids...)
	f.mu.Unlock()
	return nil
}
func (f *fakeMail) Trash(context.Context, []string) error               { return nil }
func (f *fakeMail) MarkRead(context.Context, []string) error            { return nil }
func (f *fakeMail) MarkUnread(context.Context, []string) error          { return nil }
func (f *fakeMail) AddLabel(context.Context, []string, string) error    { return nil }
func (f *fakeMail) RemoveLabel(context.Context, []string, string) error { return nil }

type env struct {
	st      *sqlite.Store
	mail    *fakeMail
	r       *Runner
	acc     models.Account
	rt      models.Routine
	revoked chan string
}

func setup(t *testing.T, max int) *env {
	t.Helper()
	ctx := context.Background()
	st, err := sqlite.Open(ctx, filepath.Join(t.TempDir(), "r.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { st.Close() })
	e := &env{st: st, mail: &fakeMail{ids: []string{"a", "b", "c"}}, revoked: make(chan string, 1)}
	e.acc, _, _ = st.UpsertAccount(ctx, "me@gmail.com", "enc")
	next := time.Now().Add(-time.Minute)
	e.rt, err = st.CreateRoutine(ctx, models.Routine{AccountID: e.acc.ID, Name: "r", GmailQuery: "q", ActionType: models.ActionArchive,
		ScheduleType: models.ScheduleDaily, ScheduleConfig: models.ScheduleConfig{Time: "03:00"}, Enabled: true, MaxMessagesPerRun: 100, NextRunAt: &next})
	if err != nil {
		t.Fatal(err)
	}
	e.r = New(Options{Store: st, MaxConcurrent: max, BatchSize: 2,
		Provider:      func(context.Context, models.Account) (engine.MailProvider, error) { return e.mail, nil },
		Retry:         &engine.RetryPolicy{MaxAttempts: 1},
		OnAuthRevoked: func(_ context.Context, id string) { e.revoked <- id },
	})
	t.Cleanup(func() { e.r.Shutdown(ctx) })
	return e
}

func TestScheduledRunRecordsAndAdvancesSchedule(t *testing.T) {
	ctx := context.Background()
	e := setup(t, 2)
	x, err := e.r.RunSync(ctx, e.rt.ID, models.TriggerSchedule, false)
	if err != nil {
		t.Fatal(err)
	}
	if x.Status != models.ExecSuccess || x.MatchedCount != 3 || x.ProcessedCount != 3 || x.FinishedAt == nil {
		t.Fatalf("%+v", x)
	}
	if len(e.mail.archived) != 3 {
		t.Fatal(e.mail.archived)
	}
	acts, _ := e.st.ListMessageActions(ctx, x.ID, 10, 0)
	if len(acts) != 3 || acts[0].Status != models.ActionSuccess {
		t.Fatalf("%+v", acts)
	}
	got, _ := e.st.GetRoutine(ctx, e.rt.ID)
	if got.LastRunAt == nil || got.NextRunAt == nil || !got.NextRunAt.After(time.Now()) {
		t.Fatalf("schedule not advanced: %+v", got)
	}
	// Lease released: a second run can start.
	if _, err := e.r.RunSync(ctx, e.rt.ID, models.TriggerManual, false); err != nil {
		t.Fatalf("lease not released: %v", err)
	}
}

func TestManualRunDoesNotMoveSchedule(t *testing.T) {
	ctx := context.Background()
	e := setup(t, 2)
	before, _ := e.st.GetRoutine(ctx, e.rt.ID)
	if _, err := e.r.RunSync(ctx, e.rt.ID, models.TriggerManual, false); err != nil {
		t.Fatal(err)
	}
	after, _ := e.st.GetRoutine(ctx, e.rt.ID)
	if !after.NextRunAt.Equal(*before.NextRunAt) || after.LastRunAt == nil {
		t.Fatalf("%+v", after)
	}
}

func TestDryRunModifiesNothing(t *testing.T) {
	ctx := context.Background()
	e := setup(t, 2)
	x, err := e.r.RunSync(ctx, e.rt.ID, models.TriggerManual, true)
	if err != nil {
		t.Fatal(err)
	}
	if !x.DryRun || x.MatchedCount != 3 || x.ProcessedCount != 0 || len(e.mail.archived) != 0 {
		t.Fatalf("%+v %v", x, e.mail.archived)
	}
	got, _ := e.st.GetRoutine(ctx, e.rt.ID)
	if got.LastRunAt != nil {
		t.Fatal("dry run must not record last_run_at")
	}
}

func TestNoOverlappingRuns(t *testing.T) {
	ctx := context.Background()
	e := setup(t, 4)
	e.mail.gate = make(chan struct{})
	e.mail.started = make(chan struct{}, 1)
	first, err := e.r.Start(ctx, e.rt.ID, models.TriggerManual, false)
	if err != nil {
		t.Fatal(err)
	}
	<-e.mail.started
	if _, err := e.r.Start(ctx, e.rt.ID, models.TriggerSchedule, false); !errors.Is(err, ErrBusy) {
		t.Fatalf("overlap allowed: %v", err)
	}
	close(e.mail.gate)
	e.r.Wait()
	x, _ := e.st.GetExecution(ctx, first.ID)
	if x.Status != models.ExecSuccess {
		t.Fatalf("%+v", x)
	}
}

func TestConcurrencyLimit(t *testing.T) {
	ctx := context.Background()
	e := setup(t, 1)
	other, _ := e.st.CreateRoutine(ctx, models.Routine{AccountID: e.acc.ID, Name: "r2", GmailQuery: "q", ActionType: models.ActionArchive,
		ScheduleType: models.ScheduleManual, MaxMessagesPerRun: 10})
	e.mail.gate = make(chan struct{})
	e.mail.started = make(chan struct{}, 1)
	if _, err := e.r.Start(ctx, e.rt.ID, models.TriggerManual, false); err != nil {
		t.Fatal(err)
	}
	<-e.mail.started
	if _, err := e.r.Start(ctx, other.ID, models.TriggerManual, false); !errors.Is(err, ErrAtCapacity) {
		t.Fatalf("got %v", err)
	}
	close(e.mail.gate)
	e.r.Wait()
	if _, err := e.r.RunSync(ctx, other.ID, models.TriggerManual, false); err != nil {
		t.Fatal(err)
	}
}

func TestFailureIsReportedNotSuccess(t *testing.T) {
	ctx := context.Background()
	e := setup(t, 2)
	e.mail.err = errors.New("gmail exploded")
	x, _ := e.r.RunSync(ctx, e.rt.ID, models.TriggerSchedule, false)
	if x.Status != models.ExecFailed || x.ProcessedCount != 0 || x.FailedCount != 3 || x.ErrorMessage == "" {
		t.Fatalf("%+v", x)
	}
	d, _ := e.st.Dashboard(ctx)
	if d.EmailsHandled != 0 {
		t.Fatal("failed actions counted as handled")
	}
	// A failed scheduled run still advances next_run_at (no hammering).
	got, _ := e.st.GetRoutine(ctx, e.rt.ID)
	if !got.NextRunAt.After(time.Now()) {
		t.Fatal("schedule stuck after failure")
	}
}

func TestAuthRevokedMarksAccount(t *testing.T) {
	e := setup(t, 2)
	e.mail.err = fmt.Errorf("x: %w", engine.ErrAuthRevoked)
	x, _ := e.r.RunSync(context.Background(), e.rt.ID, models.TriggerSchedule, false)
	if x.Status != models.ExecFailed {
		t.Fatalf("%+v", x)
	}
	select {
	case id := <-e.revoked:
		if id != e.acc.ID {
			t.Fatal(id)
		}
	case <-time.After(time.Second):
		t.Fatal("OnAuthRevoked not called")
	}
}

func TestDisablingStopsScheduledRun(t *testing.T) {
	ctx := context.Background()
	e := setup(t, 2)
	e.mail.ids = []string{"a", "b", "c", "d", "e", "f"} // 3 batches of 2
	e.mail.started = make(chan struct{}, 1)
	e.mail.gate = make(chan struct{}, 1)
	x, _ := e.r.Start(ctx, e.rt.ID, models.TriggerSchedule, false)
	<-e.mail.started
	e.st.SetRoutineEnabled(ctx, e.rt.ID, false, nil) // user disables mid-run
	e.mail.gate <- struct{}{}                        // let batch 1 finish
	close(e.mail.gate)
	e.r.Wait()
	got, _ := e.st.GetExecution(ctx, x.ID)
	if got.Status != models.ExecAborted || got.ProcessedCount >= 6 {
		t.Fatalf("disable did not stop run: %+v", got)
	}
	rt, _ := e.st.GetRoutine(ctx, e.rt.ID)
	if rt.Enabled || rt.NextRunAt != nil {
		t.Fatalf("run must not re-enable or reschedule: %+v", rt)
	}
}

func TestRecoverAfterCrash(t *testing.T) {
	ctx := context.Background()
	e := setup(t, 2)
	// Simulate a crashed process: lease held and an execution left running.
	e.st.ClaimRoutine(ctx, e.rt.ID, "dead", time.Now(), time.Now().Add(-time.Hour))
	x, _ := e.st.CreateExecution(ctx, e.rt.ID, models.TriggerSchedule, false, time.Now())
	if _, err := e.r.Start(ctx, e.rt.ID, models.TriggerManual, false); !errors.Is(err, ErrBusy) {
		t.Fatalf("got %v", err)
	}
	if err := e.r.Recover(ctx); err != nil {
		t.Fatal(err)
	}
	got, _ := e.st.GetExecution(ctx, x.ID)
	if got.Status != models.ExecFailed {
		t.Fatalf("%+v", got)
	}
	if _, err := e.r.RunSync(ctx, e.rt.ID, models.TriggerManual, false); err != nil {
		t.Fatalf("still blocked after recovery: %v", err)
	}
}

func TestStaleLeaseRecovered(t *testing.T) {
	ctx := context.Background()
	e := setup(t, 2)
	old := time.Now().Add(-LeaseStaleAfter - time.Minute)
	e.st.ClaimRoutine(ctx, e.rt.ID, "hung", old, old.Add(-time.Hour))
	hung, _ := e.st.CreateExecution(ctx, e.rt.ID, models.TriggerSchedule, false, old)
	if err := e.r.RecoverStale(ctx); err != nil {
		t.Fatal(err)
	}
	if x, _ := e.st.GetExecution(ctx, hung.ID); x.Status != models.ExecFailed {
		t.Fatalf("%+v", x)
	}
	if _, err := e.r.RunSync(ctx, e.rt.ID, models.TriggerManual, false); err != nil {
		t.Fatalf("stale lease should be taken over: %v", err)
	}
}

func TestShutdownCancelsRunAndKeepsSchedule(t *testing.T) {
	ctx := context.Background()
	e := setup(t, 2)
	e.mail.gate = make(chan struct{})
	e.mail.started = make(chan struct{}, 1)
	before, _ := e.st.GetRoutine(ctx, e.rt.ID)
	x, _ := e.r.Start(ctx, e.rt.ID, models.TriggerSchedule, false)
	<-e.mail.started
	sctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	e.r.Shutdown(sctx)
	got, _ := e.st.GetExecution(ctx, x.ID)
	if got.Status != models.ExecAborted {
		t.Fatalf("%+v", got)
	}
	after, _ := e.st.GetRoutine(ctx, e.rt.ID)
	if after.NextRunAt == nil || !after.NextRunAt.Equal(*before.NextRunAt) {
		t.Fatal("interrupted run must stay due after restart")
	}
}

func TestPreview(t *testing.T) {
	e := setup(t, 1)
	pv, err := e.r.Preview(context.Background(), e.acc, "q", 10)
	if err != nil || pv.Count != 3 {
		t.Fatalf("%+v %v", pv, err)
	}
	if len(e.mail.archived) != 0 {
		t.Fatal("preview modified mail")
	}
}
