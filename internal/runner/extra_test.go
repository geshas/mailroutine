package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/geshas/mailroutine/pkg/engine"
	"github.com/geshas/mailroutine/pkg/models"
)

func TestNewDefaults(t *testing.T) {
	r := New(Options{})
	if cap(r.sem) != 1 || r.o.Now == nil || r.o.Logger == nil {
		t.Fatal("defaults not applied")
	}
	r.Shutdown(context.Background())
}

func TestStartUnknownRoutineReleasesSlot(t *testing.T) {
	e := setup(t, 1)
	if _, err := e.r.Start(context.Background(), "missing", models.TriggerManual, false); err == nil {
		t.Fatal("expected error")
	}
	// The concurrency slot must have been returned.
	if _, err := e.r.RunSync(context.Background(), e.rt.ID, models.TriggerManual, false); err != nil {
		t.Fatal(err)
	}
}

func TestStoreFailuresInStartAndRecover(t *testing.T) {
	e := setup(t, 1)
	e.st.Close()
	ctx := context.Background()
	if _, err := e.r.Start(ctx, e.rt.ID, models.TriggerManual, false); err == nil {
		t.Fatal("Start")
	}
	if _, err := e.r.RunSync(ctx, e.rt.ID, models.TriggerManual, false); err == nil {
		t.Fatal("RunSync")
	}
	if err := e.r.Recover(ctx); err == nil {
		t.Fatal("Recover")
	}
	if err := e.r.RecoverStale(ctx); err == nil {
		t.Fatal("RecoverStale")
	}
}

func TestRecoverFailsWhenExecutionsCannotBeUpdated(t *testing.T) {
	e := setup(t, 1)
	// Break only the executions table so ReleaseAllClaims succeeds and the next step fails.
	e.st.Close()
	if err := e.r.Recover(context.Background()); err == nil {
		t.Fatal("expected error")
	}
}

func TestProviderAndAccountFailuresFailTheRun(t *testing.T) {
	ctx := context.Background()
	e := setup(t, 1)
	e.r.o.Provider = func(context.Context, models.Account) (engine.MailProvider, error) { return nil, errors.New("no token") }
	x, _ := e.r.RunSync(ctx, e.rt.ID, models.TriggerManual, false)
	if x.Status != models.ExecFailed || !strings.Contains(x.ErrorMessage, "not connected") {
		t.Fatalf("%+v", x)
	}
	if _, err := e.r.Preview(ctx, e.acc, "q", 5); !errors.Is(err, ErrAccountUnavailable) {
		t.Fatal(err)
	}
	// Account row vanished (FK cascade removes routines, so simulate by pointing the routine elsewhere).
	e.st.SetAccountStatus(ctx, e.acc.ID, models.StatusConnected)
	e.r.o.Store.DeleteAccount(ctx, e.acc.ID)
	if _, err := e.r.RunSync(ctx, e.rt.ID, models.TriggerManual, false); err == nil {
		t.Fatal("routine should be gone with its account")
	}
}

func TestInvalidStoredRoutineFails(t *testing.T) {
	ctx := context.Background()
	e := setup(t, 1)
	// Bypass validation (e.g. rows written by an older version): action label missing.
	e.st.UpdateRoutine(ctx, models.Routine{ID: e.rt.ID, AccountID: e.acc.ID, Name: "bad", GmailQuery: "q",
		ActionType: models.ActionAddLabel, ScheduleType: models.ScheduleManual, MaxMessagesPerRun: 5})
	x, _ := e.r.RunSync(ctx, e.rt.ID, models.TriggerManual, false)
	if x.Status != models.ExecFailed || !strings.Contains(x.ErrorMessage, "label") {
		t.Fatalf("%+v", x)
	}
}

func TestTruncatedRunExplainsItself(t *testing.T) {
	ctx := context.Background()
	e := setup(t, 1)
	e.mail.ids = []string{"a", "b", "c"}
	e.mail.more = true
	e.st.UpdateRoutine(ctx, models.Routine{ID: e.rt.ID, AccountID: e.acc.ID, Name: "r", GmailQuery: "q", ActionType: models.ActionArchive,
		ScheduleType: models.ScheduleManual, MaxMessagesPerRun: 3})
	x, _ := e.r.RunSync(ctx, e.rt.ID, models.TriggerManual, false)
	if x.Status != models.ExecSuccess || !strings.Contains(x.ErrorMessage, "limit of 3") {
		t.Fatalf("%+v", x)
	}
}

func TestLongErrorsAreTruncatedAndRedacted(t *testing.T) {
	ctx := context.Background()
	e := setup(t, 1)
	e.mail.err = errors.New("token ya29.SECRETSECRET " + strings.Repeat("x", 800))
	x, _ := e.r.RunSync(ctx, e.rt.ID, models.TriggerManual, false)
	if len(x.ErrorMessage) > maxErrorLength+5 || strings.Contains(x.ErrorMessage, "SECRETSECRET") || !strings.HasSuffix(x.ErrorMessage, "…") {
		t.Fatalf("%q", x.ErrorMessage)
	}
}

func TestHeartbeatKeepsLeaseFresh(t *testing.T) {
	old := heartbeatEvery
	heartbeatEvery = 10 * time.Millisecond
	t.Cleanup(func() { heartbeatEvery = old })
	ctx := context.Background()
	e := setup(t, 1)
	e.mail.gate = make(chan struct{})
	e.mail.started = make(chan struct{}, 1)
	if _, err := e.r.Start(ctx, e.rt.ID, models.TriggerManual, false); err != nil {
		t.Fatal(err)
	}
	<-e.mail.started
	time.Sleep(60 * time.Millisecond)
	close(e.mail.gate)
	e.r.Wait()
}

func TestLostLeaseStopsRun(t *testing.T) {
	ctx := context.Background()
	e := setup(t, 1)
	e.mail.ids = []string{"a", "b", "c", "d"}
	e.mail.gate = make(chan struct{}, 1)
	e.mail.started = make(chan struct{}, 1)
	x, _ := e.r.Start(ctx, e.rt.ID, models.TriggerManual, false)
	<-e.mail.started
	e.st.ReleaseAllClaims(ctx) // someone took the lease away
	e.mail.gate <- struct{}{}
	close(e.mail.gate)
	e.r.Wait()
	got, _ := e.st.GetExecution(ctx, x.ID)
	if got.Status != models.ExecAborted || got.ProcessedCount >= 4 {
		t.Fatalf("%+v", got)
	}
}

func TestShutdownTimeout(t *testing.T) {
	ctx := context.Background()
	e := setup(t, 1)
	stuck := make(chan struct{})
	e.r.o.Provider = func(context.Context, models.Account) (engine.MailProvider, error) {
		<-stuck // ignores cancellation
		return e.mail, nil
	}
	if _, err := e.r.Start(ctx, e.rt.ID, models.TriggerManual, false); err != nil {
		t.Fatal(err)
	}
	short, cancel := context.WithTimeout(ctx, 30*time.Millisecond)
	defer cancel()
	e.r.Shutdown(short) // returns despite the stuck run
	close(stuck)
	e.r.Wait()
}

func TestErrorClass(t *testing.T) {
	cases := map[string]error{
		"auth_revoked":        fmt.Errorf("x: %w", engine.ErrAuthRevoked),
		"invalid_query":       engine.ErrInvalidQuery,
		"account_unavailable": ErrAccountUnavailable,
		"cancelled":           context.Canceled,
		"transient":           engine.Retryable(errors.New("x"), 0),
		"other":               errors.New("x"),
	}
	for want, err := range cases {
		if got := errorClass(err); got != want {
			t.Errorf("%v: got %s want %s", err, got, want)
		}
	}
	if errorClass(engine.ErrStopped) != "cancelled" {
		t.Fatal("stopped")
	}
}

func TestPreviewAuthRevokedNotifies(t *testing.T) {
	e := setup(t, 1)
	e.r.o.Provider = func(context.Context, models.Account) (engine.MailProvider, error) {
		return &authFailMail{fakeMail: e.mail}, nil
	}
	if _, err := e.r.Preview(context.Background(), e.acc, "q", 5); !errors.Is(err, engine.ErrAuthRevoked) {
		t.Fatal(err)
	}
	select {
	case <-e.revoked:
	case <-time.After(time.Second):
		t.Fatal("OnAuthRevoked not called")
	}
}

type authFailMail struct{ *fakeMail }

func (a *authFailMail) ListMessages(context.Context, string, string, int) (engine.MessagePage, error) {
	return engine.MessagePage{}, engine.ErrAuthRevoked
}

type traceMail struct {
	*fakeMail
	added []string
	fail  bool
}

func (t *traceMail) AddLabel(_ context.Context, ids []string, label string) error {
	if t.fail {
		return errors.New("labels unavailable")
	}
	t.added = append(t.added, label)
	return nil
}

func TestRunnerPassesTraceLabelOnlyWhenEnabled(t *testing.T) {
	ctx := context.Background()
	e := setup(t, 1)
	tm := &traceMail{fakeMail: e.mail}
	e.r.o.Provider = func(context.Context, models.Account) (engine.MailProvider, error) { return tm, nil }
	upd := func(trace bool) {
		e.st.UpdateRoutine(ctx, models.Routine{ID: e.rt.ID, AccountID: e.acc.ID, Name: "r", GmailQuery: "q", ActionType: models.ActionArchive,
			ScheduleType: models.ScheduleManual, MaxMessagesPerRun: 10, Traceability: trace})
	}
	upd(true)
	if _, err := e.r.RunSync(ctx, e.rt.ID, models.TriggerManual, false); err != nil {
		t.Fatal(err)
	}
	if len(tm.added) == 0 || tm.added[0] != "MailRoutine/Archive" {
		t.Fatalf("%v", tm.added)
	}
	tm.added = nil
	upd(false)
	e.r.RunSync(ctx, e.rt.ID, models.TriggerManual, false)
	if len(tm.added) != 0 {
		t.Fatalf("tracing off but labels added: %v", tm.added)
	}
}

func TestTraceFailureIsExplainedButRunSucceeds(t *testing.T) {
	ctx := context.Background()
	e := setup(t, 1)
	tm := &traceMail{fakeMail: e.mail, fail: true}
	e.r.o.Provider = func(context.Context, models.Account) (engine.MailProvider, error) { return tm, nil }
	e.st.UpdateRoutine(ctx, models.Routine{ID: e.rt.ID, AccountID: e.acc.ID, Name: "r", GmailQuery: "q", ActionType: models.ActionArchive,
		ScheduleType: models.ScheduleManual, MaxMessagesPerRun: 10, Traceability: true})
	x, _ := e.r.RunSync(ctx, e.rt.ID, models.TriggerManual, false)
	if x.Status != models.ExecSuccess || x.ProcessedCount != 3 || !strings.Contains(x.ErrorMessage, "traceability label") {
		t.Fatalf("%+v", x)
	}
}
