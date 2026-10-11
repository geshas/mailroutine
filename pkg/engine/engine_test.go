package engine

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/geshas/mailroutine/pkg/models"
)

type fakeProvider struct {
	mu       sync.Mutex
	messages []string
	pageSize int // forced page size cap
	calls    map[string][][]string
	listCnt  int
	// failIDs makes any call that includes one of these ids fail permanently.
	failIDs map[string]bool
	// transient failures remaining per method before succeeding.
	transient int
	listErr   error
	applyErr  error
	estimate  int64
}

func newFake(n int) *fakeProvider {
	f := &fakeProvider{calls: map[string][][]string{}, failIDs: map[string]bool{}}
	for i := 0; i < n; i++ {
		f.messages = append(f.messages, fmt.Sprintf("m%04d", i))
	}
	return f
}

func (f *fakeProvider) ListMessages(ctx context.Context, q, token string, size int) (MessagePage, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.listCnt++
	if f.listErr != nil {
		return MessagePage{}, f.listErr
	}
	start := 0
	if token != "" {
		fmt.Sscanf(token, "%d", &start)
	}
	if f.pageSize > 0 && size > f.pageSize {
		size = f.pageSize
	}
	end := min(start+size, len(f.messages))
	page := MessagePage{IDs: append([]string(nil), f.messages[start:end]...), EstimatedTotal: f.estimate}
	if end < len(f.messages) {
		page.NextPageToken = fmt.Sprint(end)
	}
	return page, nil
}

func (f *fakeProvider) GetSummaries(ctx context.Context, ids []string) ([]models.MessageSummary, error) {
	var out []models.MessageSummary
	for _, id := range ids {
		out = append(out, models.MessageSummary{ID: id, From: "a@example.com", Subject: "s " + id})
	}
	return out, nil
}

func (f *fakeProvider) do(name string, ids []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.applyErr != nil {
		return f.applyErr
	}
	if f.transient > 0 {
		f.transient--
		return Retryable(errors.New("503"), 0)
	}
	for _, id := range ids {
		if f.failIDs[id] {
			return errors.New("bad message " + id)
		}
	}
	f.calls[name] = append(f.calls[name], append([]string(nil), ids...))
	return nil
}

func (f *fakeProvider) Archive(_ context.Context, ids []string) error    { return f.do("archive", ids) }
func (f *fakeProvider) Trash(_ context.Context, ids []string) error      { return f.do("trash", ids) }
func (f *fakeProvider) MarkRead(_ context.Context, ids []string) error   { return f.do("read", ids) }
func (f *fakeProvider) MarkUnread(_ context.Context, ids []string) error { return f.do("unread", ids) }
func (f *fakeProvider) AddLabel(_ context.Context, ids []string, l string) error {
	return f.do("add:"+l, ids)
}
func (f *fakeProvider) RemoveLabel(_ context.Context, ids []string, l string) error {
	return f.do("remove:"+l, ids)
}

type memRecorder struct {
	mu       sync.Mutex
	results  []models.MessageActionResult
	progress []Progress
	stopAt   int // stop after this many progress calls (0 = never)
}

func (r *memRecorder) RecordActions(_ context.Context, res []models.MessageActionResult) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.results = append(r.results, res...)
	return nil
}

func (r *memRecorder) Progress(_ context.Context, p Progress) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.progress = append(r.progress, p)
	if r.stopAt > 0 && len(r.progress) >= r.stopAt {
		return ErrStopped
	}
	return nil
}

func (r *memRecorder) count(s models.ActionStatus) int {
	n := 0
	for _, x := range r.results {
		if x.Status == s {
			n++
		}
	}
	return n
}

func fastRetry() *RetryPolicy {
	return &RetryPolicy{MaxAttempts: 4, BaseDelay: time.Millisecond, MaxDelay: 2 * time.Millisecond,
		Sleep: func(context.Context, time.Duration) error { return nil }}
}

func archiveReq(max int) Request {
	return Request{Query: "label:x", Action: models.Action{Type: models.ActionArchive}, MaxMessages: max}
}

func TestRunPaginatesAndBatches(t *testing.T) {
	p := newFake(250)
	p.pageSize = 60
	rec := &memRecorder{}
	e := &Engine{Provider: p, Recorder: rec, BatchSize: 100, Retry: fastRetry()}
	res, err := e.Run(context.Background(), archiveReq(1000))
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != models.ExecSuccess || res.Matched != 250 || res.Processed != 250 || res.Failed != 0 {
		t.Fatalf("%+v", res)
	}
	if got := len(p.calls["archive"]); got != 3 {
		t.Fatalf("want 3 batches, got %d", got)
	}
	if len(p.calls["archive"][0]) != 100 || len(p.calls["archive"][2]) != 50 {
		t.Fatalf("unexpected batch sizes")
	}
	if rec.count(models.ActionSuccess) != 250 || len(rec.progress) != 3 {
		t.Fatalf("recorder: %d results, %d progress", len(rec.results), len(rec.progress))
	}
}

func TestMaxMessagesLimit(t *testing.T) {
	p := newFake(500)
	e := &Engine{Provider: p, BatchSize: 100, Retry: fastRetry()}
	res, _ := e.Run(context.Background(), archiveReq(120))
	if res.Matched != 120 || res.Processed != 120 || !res.Truncated {
		t.Fatalf("%+v", res)
	}
	total := 0
	for _, b := range p.calls["archive"] {
		total += len(b)
	}
	if total != 120 {
		t.Fatalf("modified %d messages", total)
	}
}

func TestExactLimitNotTruncated(t *testing.T) {
	p := newFake(100)
	e := &Engine{Provider: p, Retry: fastRetry()}
	res, _ := e.Run(context.Background(), archiveReq(100))
	if res.Truncated || res.Matched != 100 {
		t.Fatalf("%+v", res)
	}
}

func TestDryRunNeverModifies(t *testing.T) {
	p := newFake(30)
	rec := &memRecorder{}
	e := &Engine{Provider: p, Recorder: rec, Retry: fastRetry()}
	req := archiveReq(100)
	req.DryRun = true
	res, _ := e.Run(context.Background(), req)
	if res.Status != models.ExecSuccess || res.Matched != 30 || res.Processed != 0 {
		t.Fatalf("%+v", res)
	}
	if len(p.calls) != 0 {
		t.Fatalf("dry run modified mail: %v", p.calls)
	}
	if rec.count(models.ActionDryRun) != 30 {
		t.Fatalf("dry run results: %d", rec.count(models.ActionDryRun))
	}
}

func TestAllActionsDispatch(t *testing.T) {
	cases := map[models.ActionType]string{
		models.ActionArchive: "archive", models.ActionTrash: "trash", models.ActionMarkRead: "read",
		models.ActionMarkUnread: "unread", models.ActionAddLabel: "add:L", models.ActionRemoveLabel: "remove:L",
	}
	for at, key := range cases {
		p := newFake(3)
		e := &Engine{Provider: p, Retry: fastRetry()}
		res, _ := e.Run(context.Background(), Request{Query: "q", MaxMessages: 10,
			Action: models.Action{Type: at, Config: models.ActionConfig{Label: "L"}}})
		if res.Processed != 3 || len(p.calls[key]) != 1 {
			t.Errorf("%s: %+v calls=%v", at, res, p.calls)
		}
	}
}

func TestPartialFailureIsolatesBadMessage(t *testing.T) {
	p := newFake(10)
	p.failIDs["m0004"] = true
	rec := &memRecorder{}
	e := &Engine{Provider: p, Recorder: rec, BatchSize: 10, Retry: fastRetry()}
	res, _ := e.Run(context.Background(), archiveReq(100))
	if res.Status != models.ExecPartial || res.Processed != 9 || res.Failed != 1 {
		t.Fatalf("%+v", res)
	}
	if rec.count(models.ActionFailed) != 1 || rec.count(models.ActionSuccess) != 9 {
		t.Fatalf("recorded wrongly")
	}
	for _, r := range rec.results {
		if r.Status == models.ActionFailed && (r.MessageID != "m0004" || r.Error == "") {
			t.Fatalf("bad failure record %+v", r)
		}
	}
}

func TestFailedNeverRecordedAsSuccess(t *testing.T) {
	p := newFake(5)
	p.applyErr = errors.New("boom")
	rec := &memRecorder{}
	e := &Engine{Provider: p, Recorder: rec, Retry: fastRetry()}
	res, _ := e.Run(context.Background(), archiveReq(10))
	if res.Status != models.ExecFailed || res.Processed != 0 || res.Failed != 5 {
		t.Fatalf("%+v", res)
	}
	if rec.count(models.ActionSuccess) != 0 {
		t.Fatal("failure recorded as success")
	}
}

func TestFatalAuthAbortsRun(t *testing.T) {
	p := newFake(300)
	p.applyErr = fmt.Errorf("x: %w", ErrAuthRevoked)
	e := &Engine{Provider: p, BatchSize: 100, Retry: fastRetry()}
	res, _ := e.Run(context.Background(), archiveReq(300))
	if res.Status != models.ExecFailed || !errors.Is(res.Err, ErrAuthRevoked) {
		t.Fatalf("%+v", res)
	}
	if res.Failed != 100 { // stopped after the first batch
		t.Fatalf("should stop after first fatal batch, failed=%d", res.Failed)
	}
}

func TestSearchErrorFailsRun(t *testing.T) {
	p := newFake(3)
	p.listErr = ErrInvalidQuery
	e := &Engine{Provider: p, Retry: fastRetry()}
	res, _ := e.Run(context.Background(), archiveReq(10))
	if res.Status != models.ExecFailed || !errors.Is(res.Err, ErrInvalidQuery) {
		t.Fatalf("%+v", res)
	}
	if p.listCnt != 1 {
		t.Fatalf("permanent error was retried %d times", p.listCnt)
	}
}

func TestRetryTransientThenSucceed(t *testing.T) {
	p := newFake(5)
	p.transient = 2
	sleeps := 0
	pol := fastRetry()
	pol.Sleep = func(context.Context, time.Duration) error { sleeps++; return nil }
	e := &Engine{Provider: p, Retry: pol}
	res, _ := e.Run(context.Background(), archiveReq(10))
	if res.Status != models.ExecSuccess || res.Processed != 5 || sleeps != 2 {
		t.Fatalf("%+v sleeps=%d", res, sleeps)
	}
}

func TestRetryBoundedAndPermanentNotRetried(t *testing.T) {
	attempts := 0
	pol := fastRetry()
	err := pol.Do(context.Background(), func(context.Context) error { attempts++; return Retryable(errors.New("429"), 0) })
	if err == nil || attempts != 4 {
		t.Fatalf("attempts=%d err=%v", attempts, err)
	}
	attempts = 0
	err = pol.Do(context.Background(), func(context.Context) error { attempts++; return errors.New("400") })
	if err == nil || attempts != 1 {
		t.Fatalf("permanent error retried: %d", attempts)
	}
}

func TestBackoffHonoursHintAndCap(t *testing.T) {
	p := RetryPolicy{BaseDelay: 100 * time.Millisecond, MaxDelay: time.Second}
	for attempt := 0; attempt < 10; attempt++ {
		d := p.delay(attempt, 0)
		if d <= 0 || d > time.Second {
			t.Fatalf("attempt %d delay %v", attempt, d)
		}
	}
	if d := p.delay(0, 700*time.Millisecond); d < 700*time.Millisecond {
		t.Fatalf("hint ignored: %v", d)
	}
}

func TestContextCancellation(t *testing.T) {
	p := newFake(500)
	ctx, cancel := context.WithCancel(context.Background())
	rec := &memRecorder{}
	e := &Engine{Provider: p, Recorder: rec, BatchSize: 100, Retry: fastRetry()}
	// Cancel after the first batch has been recorded.
	cr := &cancelAfter{memRecorder: rec, cancel: cancel, after: 1}
	e.Recorder = cr
	res, _ := e.Run(ctx, archiveReq(500))
	if res.Status != models.ExecAborted || res.Processed != 100 {
		t.Fatalf("%+v", res)
	}
}

type cancelAfter struct {
	*memRecorder
	cancel func()
	after  int
}

func (c *cancelAfter) Progress(ctx context.Context, p Progress) error {
	err := c.memRecorder.Progress(ctx, p)
	if len(c.progress) >= c.after {
		c.cancel()
	}
	return err
}

func TestStoppedByRecorder(t *testing.T) {
	p := newFake(300)
	rec := &memRecorder{stopAt: 1}
	e := &Engine{Provider: p, Recorder: rec, BatchSize: 100, Retry: fastRetry()}
	res, _ := e.Run(context.Background(), archiveReq(300))
	if res.Status != models.ExecAborted || res.Processed != 100 {
		t.Fatalf("%+v", res)
	}
}

func TestDuplicateIDsDeduplicated(t *testing.T) {
	p := newFake(0)
	p.messages = []string{"a", "b", "a", "c", "b"}
	e := &Engine{Provider: p, Retry: fastRetry()}
	res, _ := e.Run(context.Background(), archiveReq(10))
	if res.Matched != 3 || res.Processed != 3 {
		t.Fatalf("%+v", res)
	}
}

func TestRequestValidation(t *testing.T) {
	e := &Engine{Provider: newFake(1)}
	bad := []Request{
		{Query: "", Action: models.Action{Type: models.ActionArchive}, MaxMessages: 1},
		{Query: "q", Action: models.Action{Type: "nuke"}, MaxMessages: 1},
		{Query: "q", Action: models.Action{Type: models.ActionAddLabel}, MaxMessages: 1},
		{Query: "q", Action: models.Action{Type: models.ActionArchive}, MaxMessages: 0},
		{Query: "q", Action: models.Action{Type: models.ActionArchive}, MaxMessages: models.MaxMessagesPerRunLimit + 1},
	}
	for i, r := range bad {
		if _, err := e.Run(context.Background(), r); err == nil {
			t.Errorf("case %d accepted", i)
		}
	}
}

func TestPreviewNeverModifiesAndLabelsEstimates(t *testing.T) {
	p := newFake(300)
	p.estimate = 4321
	e := &Engine{Provider: p, Retry: fastRetry()}
	pv, err := e.Preview(context.Background(), "label:x", 25)
	if err != nil {
		t.Fatal(err)
	}
	if len(p.calls) != 0 {
		t.Fatal("preview modified mail")
	}
	if len(pv.Messages) != 25 || !pv.HasMore || !pv.CountIsEstimate || pv.Count != 4321 {
		t.Fatalf("%+v", pv)
	}

	small := newFake(7)
	pv, _ = (&Engine{Provider: small, Retry: fastRetry()}).Preview(context.Background(), "q", 25)
	if pv.CountIsEstimate || pv.Count != 7 || pv.HasMore || len(pv.Messages) != 7 {
		t.Fatalf("exact preview wrong: %+v", pv)
	}
	empty := newFake(0)
	pv, _ = (&Engine{Provider: empty, Retry: fastRetry()}).Preview(context.Background(), "q", 25)
	if pv.Count != 0 || pv.Messages == nil {
		t.Fatalf("empty preview wrong: %+v", pv)
	}
}

func TestIdempotentRerun(t *testing.T) {
	// Running the same request twice yields the same outcome and never errors.
	p := newFake(20)
	e := &Engine{Provider: p, Retry: fastRetry()}
	for i := 0; i < 2; i++ {
		res, _ := e.Run(context.Background(), archiveReq(100))
		if res.Status != models.ExecSuccess || res.Processed != 20 {
			t.Fatalf("run %d: %+v", i, res)
		}
	}
}
