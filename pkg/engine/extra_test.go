package engine

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"testing"
	"time"

	"github.com/geshas/mailroutine/pkg/models"
)

func TestErrorHelpers(t *testing.T) {
	if Retryable(nil, 0) != nil {
		t.Fatal("nil must stay nil")
	}
	base := errors.New("boom")
	r := Retryable(base, time.Second)
	if r.Error() != "boom" || !errors.Is(r, base) || !IsRetryable(fmt.Errorf("wrap: %w", r)) {
		t.Fatal("retryable wrapping broken")
	}
	if IsRetryable(base) {
		t.Fatal("plain error is not retryable")
	}
	for _, e := range []error{ErrAuthRevoked, ErrInvalidQuery, ErrStopped} {
		if !IsFatal(fmt.Errorf("x: %w", e)) {
			t.Fatalf("%v should be fatal", e)
		}
	}
	if IsFatal(base) {
		t.Fatal("plain error is not fatal")
	}
}

func TestRetryContextHandling(t *testing.T) {
	// Already-cancelled context: fn never runs.
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	called := false
	err := DefaultRetryPolicy.Do(ctx, func(context.Context) error { called = true; return nil })
	if !errors.Is(err, context.Canceled) || called {
		t.Fatalf("called=%v err=%v", called, err)
	}
	// Sleep failure aborts the retry loop.
	pol := RetryPolicy{MaxAttempts: 3, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond,
		Sleep: func(context.Context, time.Duration) error { return context.DeadlineExceeded }}
	err = pol.Do(context.Background(), func(context.Context) error { return Retryable(errors.New("x"), 0) })
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	// Real timer sleep: completes, and is interruptible.
	if err := sleepCtx(context.Background(), time.Millisecond); err != nil {
		t.Fatal(err)
	}
	c2, cancel2 := context.WithCancel(context.Background())
	cancel2()
	if err := sleepCtx(c2, time.Hour); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	// Default Sleep path with a tiny real delay.
	fast := RetryPolicy{MaxAttempts: 2, BaseDelay: time.Millisecond, MaxDelay: time.Millisecond}
	n := 0
	err = fast.Do(context.Background(), func(context.Context) error {
		n++
		if n == 1 {
			return Retryable(errors.New("x"), 0)
		}
		return nil
	})
	if err != nil || n != 2 {
		t.Fatal(n, err)
	}
	// Overflowing shifts are capped at MaxDelay.
	if d := (RetryPolicy{BaseDelay: time.Hour, MaxDelay: time.Second}).delay(70, 0); d > time.Second {
		t.Fatal(d)
	}
}

func TestEngineDefaults(t *testing.T) {
	e := &Engine{}
	if e.logger() == nil || e.batchSize() != defaultBatchSize || e.retry().MaxAttempts != DefaultRetryPolicy.MaxAttempts {
		t.Fatal("defaults wrong")
	}
	fixed := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	e = &Engine{Now: func() time.Time { return fixed }, Logger: slog.Default(), BatchSize: 7}
	if !e.now().Equal(fixed) || e.batchSize() != 7 || e.logger() != slog.Default() {
		t.Fatal("overrides ignored")
	}
	if e0 := (&Engine{}); e0.now().IsZero() {
		t.Fatal("now")
	}
}

func TestNilRecorderAndUnsupportedAction(t *testing.T) {
	e := &Engine{Provider: newFake(2), Retry: fastRetry()}
	e.record(context.Background(), []string{"a"}, models.ActionArchive, models.ActionSuccess, "")
	if err := e.progress(context.Background(), Result{}); err != nil {
		t.Fatal(err)
	}
	if err := e.apply(context.Background(), models.Action{Type: "bogus"}, []string{"a"}); err == nil {
		t.Fatal("unsupported action accepted")
	}
}

type failingRecorder struct{ memRecorder }

func (f *failingRecorder) RecordActions(context.Context, []models.MessageActionResult) error {
	return errors.New("db down")
}

func TestRecorderFailureIsLoggedNotFatal(t *testing.T) {
	var buf bytes.Buffer
	e := &Engine{Provider: newFake(3), Recorder: &failingRecorder{}, Retry: fastRetry(),
		Logger: slog.New(slog.NewTextHandler(&buf, nil))}
	res, _ := e.Run(context.Background(), archiveReq(10))
	if res.Status != models.ExecSuccess || res.Processed != 3 {
		t.Fatalf("%+v", res)
	}
	if !bytes.Contains(buf.Bytes(), []byte("recording message actions failed")) {
		t.Fatal("recorder error not logged")
	}
}

func TestFallbackStopsAfterConsecutiveFailures(t *testing.T) {
	p := newFake(12)
	for _, id := range p.messages {
		p.failIDs[id] = true
	}
	rec := &memRecorder{}
	e := &Engine{Provider: p, Recorder: rec, BatchSize: 12, Retry: fastRetry()}
	res, _ := e.Run(context.Background(), archiveReq(12))
	if res.Status != models.ExecFailed || res.Failed != 12 || res.Processed != 0 {
		t.Fatalf("%+v", res)
	}
	if rec.count(models.ActionFailed) != 12 {
		t.Fatalf("every message must be recorded as failed, got %d", rec.count(models.ActionFailed))
	}
}

// First call (the batch) fails permanently, then a per-message call hits a fatal error.
type flakyProvider struct {
	*fakeProvider
	calls int
}

func (f *flakyProvider) Archive(ctx context.Context, ids []string) error {
	f.calls++
	switch {
	case f.calls == 1:
		return errors.New("batch rejected")
	case f.calls == 3:
		return fmt.Errorf("revoked: %w", ErrAuthRevoked)
	}
	return f.fakeProvider.Archive(ctx, ids)
}

func TestFatalErrorDuringFallbackAborts(t *testing.T) {
	p := &flakyProvider{fakeProvider: newFake(5)}
	e := &Engine{Provider: p, BatchSize: 5, Retry: fastRetry()}
	res, _ := e.Run(context.Background(), archiveReq(5))
	if !errors.Is(res.Err, ErrAuthRevoked) || res.Processed != 1 || res.Failed != 1 {
		t.Fatalf("%+v", res)
	}
}

func TestCancelDuringFallback(t *testing.T) {
	p := newFake(6)
	p.failIDs["m0000"] = true // forces the batch to fail
	ctx, cancel := context.WithCancel(context.Background())
	rec := &cancelOnRecord{memRecorder: &memRecorder{}, cancel: cancel}
	e := &Engine{Provider: p, Recorder: rec, BatchSize: 6, Retry: fastRetry()}
	res, _ := e.Run(ctx, archiveReq(6))
	if res.Status != models.ExecAborted {
		t.Fatalf("%+v", res)
	}
	if rec.count(models.ActionSuccess)+rec.count(models.ActionFailed) != 6 {
		t.Fatal("every message must be accounted for after cancellation")
	}
}

type cancelOnRecord struct {
	*memRecorder
	cancel func()
}

func (c *cancelOnRecord) RecordActions(ctx context.Context, r []models.MessageActionResult) error {
	err := c.memRecorder.RecordActions(ctx, r)
	if len(c.results) >= 2 {
		c.cancel()
	}
	return err
}

func TestSearchCancelled(t *testing.T) {
	p := newFake(3)
	p.listErr = context.Canceled
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	res, _ := (&Engine{Provider: p, Retry: fastRetry()}).Run(ctx, archiveReq(3))
	if res.Status != models.ExecAborted {
		t.Fatalf("%+v", res)
	}
}

type previewProvider struct {
	*fakeProvider
	summaryErr error
}

func (p *previewProvider) GetSummaries(ctx context.Context, ids []string) ([]models.MessageSummary, error) {
	if p.summaryErr != nil {
		return nil, p.summaryErr
	}
	return p.fakeProvider.GetSummaries(ctx, ids)
}

func TestPreviewErrors(t *testing.T) {
	e := &Engine{Provider: newFake(2), Retry: fastRetry()}
	if _, err := e.Preview(context.Background(), "", 5); err == nil {
		t.Fatal("empty query accepted")
	}
	p := newFake(2)
	p.listErr = errors.New("nope")
	if _, err := (&Engine{Provider: p, Retry: fastRetry()}).Preview(context.Background(), "q", 5); err == nil {
		t.Fatal("search error swallowed")
	}
	pp := &previewProvider{fakeProvider: newFake(2), summaryErr: errors.New("meta fail")}
	if _, err := (&Engine{Provider: pp, Retry: fastRetry()}).Preview(context.Background(), "q", 5); err == nil {
		t.Fatal("metadata error swallowed")
	}
	// Sample size is clamped.
	pv, err := (&Engine{Provider: newFake(500), Retry: fastRetry()}).Preview(context.Background(), "q", 100000)
	if err != nil || len(pv.Messages) != 100 {
		t.Fatalf("%d %v", len(pv.Messages), err)
	}
}

func TestBackoffHintCappedAtMax(t *testing.T) {
	p := RetryPolicy{BaseDelay: time.Millisecond, MaxDelay: 10 * time.Millisecond}
	if d := p.delay(0, time.Hour); d != 10*time.Millisecond {
		t.Fatal(d)
	}
}

func TestTraceLabelAddedToHandledMessagesOnly(t *testing.T) {
	p := newFake(6)
	p.failIDs["m0002"] = true // fails in the batch and again on its own
	rec := &memRecorder{}
	e := &Engine{Provider: p, Recorder: rec, BatchSize: 6, Retry: fastRetry()}
	res, _ := e.Run(context.Background(), Request{Query: "q", MaxMessages: 10, Action: models.Action{Type: models.ActionArchive}, TraceLabel: "MailRoutine/Archive"})
	if res.Processed != 5 || res.Failed != 1 || res.TraceFailed != 0 {
		t.Fatalf("%+v", res)
	}
	traced := p.calls["add:MailRoutine/Archive"]
	var ids []string
	for _, b := range traced {
		ids = append(ids, b...)
	}
	if len(ids) != 5 {
		t.Fatalf("expected the 5 handled messages to be traced, got %v", ids)
	}
	for _, id := range ids {
		if id == "m0002" {
			t.Fatal("a message the action failed on must not be labelled as handled")
		}
	}
}

func TestTraceLabelWholeBatchAndDisabled(t *testing.T) {
	p := newFake(5)
	e := &Engine{Provider: p, Retry: fastRetry()}
	e.Run(context.Background(), Request{Query: "q", MaxMessages: 10, Action: models.Action{Type: models.ActionTrash}, TraceLabel: "MailRoutine/Trash"})
	if len(p.calls["add:MailRoutine/Trash"]) != 1 || len(p.calls["add:MailRoutine/Trash"][0]) != 5 {
		t.Fatalf("%v", p.calls)
	}
	p2 := newFake(5)
	(&Engine{Provider: p2, Retry: fastRetry()}).Run(context.Background(), archiveReq(10))
	for k := range p2.calls {
		if k != "archive" {
			t.Fatalf("no TraceLabel means no extra calls, saw %s", k)
		}
	}
}

func TestTraceLabelNotAppliedOnDryRun(t *testing.T) {
	p := newFake(3)
	req := archiveReq(10)
	req.DryRun, req.TraceLabel = true, "MailRoutine/Archive"
	(&Engine{Provider: p, Retry: fastRetry()}).Run(context.Background(), req)
	if len(p.calls) != 0 {
		t.Fatalf("dry run modified mail: %v", p.calls)
	}
}

type traceFailProvider struct{ *fakeProvider }

func (t *traceFailProvider) AddLabel(ctx context.Context, ids []string, l string) error {
	return errors.New("label service down")
}

func TestTraceFailureDoesNotFailTheRun(t *testing.T) {
	var buf bytes.Buffer
	p := &traceFailProvider{newFake(4)}
	e := &Engine{Provider: p, Retry: fastRetry(), Logger: slog.New(slog.NewTextHandler(&buf, nil))}
	req := archiveReq(10)
	req.TraceLabel = "MailRoutine/Archive"
	res, _ := e.Run(context.Background(), req)
	if res.Status != models.ExecSuccess || res.Processed != 4 || res.TraceFailed != 4 {
		t.Fatalf("%+v", res)
	}
	if !bytes.Contains(buf.Bytes(), []byte("traceability label failed")) {
		t.Fatal("trace failure not logged")
	}
}

func TestTraceAfterFatalErrorInFallbackStillLabelsEarlierSuccesses(t *testing.T) {
	p := &flakyProvider{fakeProvider: newFake(5)}
	e := &Engine{Provider: p, BatchSize: 5, Retry: fastRetry()}
	req := archiveReq(5)
	req.TraceLabel = "MailRoutine/Archive"
	res, _ := e.Run(context.Background(), req)
	if !errors.Is(res.Err, ErrAuthRevoked) || res.Processed != 1 {
		t.Fatalf("%+v", res)
	}
	if got := p.fakeProvider.calls["add:MailRoutine/Archive"]; len(got) != 1 || len(got[0]) != 1 {
		t.Fatalf("the one handled message must still be traced: %v", p.fakeProvider.calls)
	}
}
