// Package runner executes routines: it takes the execution lease, records the
// run in the database, drives the engine and schedules the next run.
package runner

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/geshas/mailroutine/internal/logging"
	"github.com/geshas/mailroutine/internal/security"
	"github.com/geshas/mailroutine/internal/sqlite"
	"github.com/geshas/mailroutine/pkg/engine"
	"github.com/geshas/mailroutine/pkg/models"
	"github.com/geshas/mailroutine/pkg/schedule"
)

// Errors returned by Start.
var (
	// ErrBusy means the routine is already running.
	ErrBusy = errors.New("routine is already running")
	// ErrAtCapacity means MAX_CONCURRENT_ROUTINES runs are in progress.
	ErrAtCapacity = errors.New("too many routines are running; try again shortly")
	// ErrAccountUnavailable means the routine's account is missing or disconnected.
	ErrAccountUnavailable = errors.New("gmail account is not connected")
)

const (
	// LeaseStaleAfter is how long a lease may go without a heartbeat before
	// another worker may take it over.
	LeaseStaleAfter = 10 * time.Minute
	maxErrorLength  = 500
)

// heartbeatEvery is how often a running routine refreshes its lease (variable for tests).
var heartbeatEvery = time.Minute

// ProviderFunc builds a mail provider for an account.
type ProviderFunc func(ctx context.Context, acc models.Account) (engine.MailProvider, error)

// Options configures a Runner.
type Options struct {
	Store         *sqlite.Store
	Provider      ProviderFunc
	Logger        *slog.Logger
	MaxConcurrent int
	BatchSize     int
	Retry         *engine.RetryPolicy
	// OnAuthRevoked is called when Gmail rejects the account's credentials.
	OnAuthRevoked func(ctx context.Context, accountID string)
	Now           func() time.Time
}

// Runner starts and supervises routine executions.
type Runner struct {
	o   Options
	sem chan struct{}
	wg  sync.WaitGroup

	ctx    context.Context
	cancel context.CancelFunc
}

// New creates a Runner. Call Shutdown to stop in-flight runs.
func New(o Options) *Runner {
	if o.MaxConcurrent < 1 {
		o.MaxConcurrent = 1
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.New(slog.DiscardHandler)
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Runner{o: o, sem: make(chan struct{}, o.MaxConcurrent), ctx: ctx, cancel: cancel}
}

func (r *Runner) log() *slog.Logger { return r.o.Logger.With("component", "runner") }

// Recover cleans up after a previous process: this process is the only worker,
// so leftover leases and "running" executions are dead.
func (r *Runner) Recover(ctx context.Context) error {
	now := r.o.Now()
	n, err := r.o.Store.ReleaseAllClaims(ctx)
	if err != nil {
		return err
	}
	f, err := r.o.Store.FailRunningExecutions(ctx, "interrupted: MailRoutine restarted while this run was in progress", now)
	if err != nil {
		return err
	}
	if n > 0 || f > 0 {
		r.log().Warn("recovered from unclean shutdown", "released_claims", n, "failed_executions", f)
	}
	return nil
}

// RecoverStale fails executions whose lease stopped heartbeating and frees the lease.
func (r *Runner) RecoverStale(ctx context.Context) error {
	now := r.o.Now()
	stale := now.Add(-LeaseStaleAfter)
	n, err := r.o.Store.FailStaleExecutions(ctx, stale, "interrupted: run stopped responding", now)
	if err != nil {
		return err
	}
	if n > 0 {
		r.log().Warn("failed stale executions", "count", n)
	}
	return nil
}

// Start claims the routine, records an execution and runs it in the background.
// It returns as soon as the execution exists.
func (r *Runner) Start(ctx context.Context, routineID string, trigger models.Trigger, dryRun bool) (models.Execution, error) {
	select {
	case r.sem <- struct{}{}:
	default:
		return models.Execution{}, ErrAtCapacity
	}
	release := func() { <-r.sem }

	routine, err := r.o.Store.GetRoutine(ctx, routineID)
	if err != nil {
		release()
		return models.Execution{}, err
	}
	now := r.o.Now()
	token := security.RandomID()
	ok, err := r.o.Store.ClaimRoutine(ctx, routineID, token, now, now.Add(-LeaseStaleAfter))
	if err != nil {
		release()
		return models.Execution{}, err
	}
	if !ok {
		release()
		return models.Execution{}, ErrBusy
	}
	exec, err := r.o.Store.CreateExecution(ctx, routineID, trigger, dryRun, now)
	if err != nil {
		_ = r.o.Store.ReleaseClaim(ctx, routineID, token)
		release()
		return models.Execution{}, err
	}
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer release()
		r.execute(routine, exec, token)
	}()
	return exec, nil
}

// RunSync starts a routine and waits for it to finish (used by tests and CLI-style callers).
func (r *Runner) RunSync(ctx context.Context, routineID string, trigger models.Trigger, dryRun bool) (models.Execution, error) {
	exec, err := r.Start(ctx, routineID, trigger, dryRun)
	if err != nil {
		return exec, err
	}
	r.wg.Wait()
	return r.o.Store.GetExecution(ctx, exec.ID)
}

// Wait blocks until all in-flight runs have finished.
func (r *Runner) Wait() { r.wg.Wait() }

// Shutdown cancels in-flight runs and waits for them (bounded by ctx).
func (r *Runner) Shutdown(ctx context.Context) {
	r.cancel()
	done := make(chan struct{})
	go func() { r.wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-ctx.Done():
		r.log().Warn("shutdown timed out waiting for running routines")
	}
}

type recorder struct {
	r         *Runner
	execID    string
	routineID string
	token     string
	scheduled bool
}

func (rec *recorder) RecordActions(ctx context.Context, res []models.MessageActionResult) error {
	return rec.r.o.Store.InsertMessageActions(ctx, rec.execID, res)
}

func (rec *recorder) Progress(ctx context.Context, p engine.Progress) error {
	st := rec.r.o.Store
	if err := st.UpdateExecutionProgress(ctx, rec.execID, p.Matched, p.Processed, p.Failed); err != nil {
		return err
	}
	if ok, err := st.Heartbeat(ctx, rec.routineID, rec.token, rec.r.o.Now()); err != nil || !ok {
		return engine.ErrStopped // lease lost: another worker may own the routine
	}
	// A disabled scheduled routine must stop immediately.
	if rec.scheduled {
		if cur, err := st.GetRoutine(ctx, rec.routineID); err != nil || !cur.Enabled {
			return engine.ErrStopped
		}
	}
	return nil
}

func (r *Runner) execute(routine models.Routine, exec models.Execution, token string) {
	start := r.o.Now()
	log := r.log().With("routine_id", routine.ID, "execution_id", exec.ID, "trigger", string(exec.Trigger), "dry_run", exec.DryRun)
	log.Info("run started")

	// Keep the lease alive during long searches and API calls.
	hbCtx, stopHB := context.WithCancel(context.WithoutCancel(r.ctx))
	go func() {
		t := time.NewTicker(heartbeatEvery)
		defer t.Stop()
		for {
			select {
			case <-hbCtx.Done():
				return
			case <-t.C:
				_, _ = r.o.Store.Heartbeat(hbCtx, routine.ID, token, r.o.Now())
			}
		}
	}()
	defer stopHB()

	res := r.runEngine(routine, exec, token)

	final := context.WithoutCancel(r.ctx)
	end := r.o.Now()
	errMsg := ""
	if res.Err != nil {
		errMsg = logging.Redact(res.Err.Error())
		if len(errMsg) > maxErrorLength {
			errMsg = errMsg[:maxErrorLength] + "…"
		}
	}
	if res.TraceFailed > 0 && errMsg == "" {
		errMsg = fmt.Sprintf("%d handled emails could not be given the traceability label %q", res.TraceFailed, models.TraceLabel(routine.ActionType))
	}
	if res.Truncated && errMsg == "" {
		errMsg = fmt.Sprintf("more messages matched than the limit of %d per run; the rest will be handled by later runs", routine.MaxMessagesPerRun)
	}
	if err := r.o.Store.FinishExecution(final, exec.ID, res.Status, res.Matched, res.Processed, res.Failed, errMsg, end); err != nil {
		log.Error("recording run result failed", "error", err)
	}
	r.finishRoutine(final, routine, exec, token, res.Status, end, log)

	attrs := []any{"status", string(res.Status), "matched", res.Matched, "processed", res.Processed,
		"failed", res.Failed, "duration", end.Sub(start).String()}
	if res.Err != nil {
		attrs = append(attrs, "error_class", errorClass(res.Err), "error", res.Err)
	}
	if res.Status == models.ExecFailed || res.Status == models.ExecPartial {
		log.Warn("run finished", attrs...)
	} else {
		log.Info("run finished", attrs...)
	}
}

func (r *Runner) runEngine(routine models.Routine, exec models.Execution, token string) engine.Result {
	ctx := r.ctx
	acc, err := r.o.Store.GetAccount(ctx, routine.AccountID)
	if err != nil {
		return engine.Result{Status: models.ExecFailed, Err: fmt.Errorf("%w: %v", ErrAccountUnavailable, err)}
	}
	provider, err := r.o.Provider(ctx, acc)
	if err != nil {
		return engine.Result{Status: models.ExecFailed, Err: fmt.Errorf("%w: %v", ErrAccountUnavailable, err)}
	}
	eng := &engine.Engine{
		Provider: provider, BatchSize: r.o.BatchSize, Retry: r.o.Retry, Logger: r.log().With("routine_id", routine.ID, "execution_id", exec.ID),
		Recorder: &recorder{r: r, execID: exec.ID, routineID: routine.ID, token: token, scheduled: exec.Trigger == models.TriggerSchedule},
		Now:      r.o.Now,
	}
	res, err := eng.Run(ctx, engine.Request{
		Query: routine.GmailQuery, Action: routine.Action(), MaxMessages: routine.MaxMessagesPerRun, DryRun: exec.DryRun,
		TraceLabel: traceLabelFor(routine),
	})
	if err != nil { // invalid request: the stored routine is no longer valid
		return engine.Result{Status: models.ExecFailed, Err: err}
	}
	if errors.Is(res.Err, engine.ErrAuthRevoked) && r.o.OnAuthRevoked != nil {
		r.o.OnAuthRevoked(context.WithoutCancel(ctx), routine.AccountID)
	}
	return res
}

// finishRoutine releases the lease and updates the schedule.
func (r *Runner) finishRoutine(ctx context.Context, routine models.Routine, exec models.Execution, token string,
	status models.ExecutionStatus, end time.Time, log *slog.Logger) {
	st := r.o.Store
	var err error
	switch {
	case exec.DryRun:
		err = st.ReleaseClaim(ctx, routine.ID, token)
	case exec.Trigger == models.TriggerManual:
		// Manual runs update last_run_at but never disturb the schedule.
		err = st.CompleteRun(ctx, routine.ID, token, end, nil, false)
	case status == models.ExecAborted:
		// Interrupted (shutdown or disabled): keep next_run_at so a restart picks it up.
		err = st.CompleteRun(ctx, routine.ID, token, end, nil, false)
	default:
		var next *time.Time
		if cur, gerr := st.GetRoutine(ctx, routine.ID); gerr == nil && cur.Enabled {
			// Computed from "now", so missed slots are skipped rather than replayed.
			if n, nerr := schedule.Next(cur.ScheduleType, cur.ScheduleConfig, end); nerr == nil {
				next = &n
			}
		}
		err = st.CompleteRun(ctx, routine.ID, token, end, next, true)
	}
	if err != nil && !errors.Is(err, sqlite.ErrNotFound) {
		log.Error("releasing routine lease failed", "error", err)
	}
}

func errorClass(err error) string {
	switch {
	case errors.Is(err, engine.ErrAuthRevoked):
		return "auth_revoked"
	case errors.Is(err, engine.ErrInvalidQuery):
		return "invalid_query"
	case errors.Is(err, ErrAccountUnavailable):
		return "account_unavailable"
	case errors.Is(err, context.Canceled), errors.Is(err, engine.ErrStopped):
		return "cancelled"
	case engine.IsRetryable(err):
		return "transient"
	}
	return "other"
}

// Preview searches without modifying anything.
func (r *Runner) Preview(ctx context.Context, acc models.Account, query string, sample int) (engine.Preview, error) {
	provider, err := r.o.Provider(ctx, acc)
	if err != nil {
		return engine.Preview{}, fmt.Errorf("%w: %v", ErrAccountUnavailable, err)
	}
	eng := &engine.Engine{Provider: provider, Retry: r.o.Retry}
	pv, err := eng.Preview(ctx, query, sample)
	if errors.Is(err, engine.ErrAuthRevoked) && r.o.OnAuthRevoked != nil {
		r.o.OnAuthRevoked(ctx, acc.ID)
	}
	return pv, err
}

// traceLabelFor returns the traceability label for a routine, or "" when it is switched off.
func traceLabelFor(r models.Routine) string {
	if !r.Traceability {
		return ""
	}
	return models.TraceLabel(r.ActionType)
}
