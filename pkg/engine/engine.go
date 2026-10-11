// Package engine executes routines against a MailProvider. It depends on no
// storage, HTTP or configuration code, so it can be reused by other hosts.
//
// Delivery semantics: Gmail confirms each batch call, and the engine only counts
// messages as processed after that confirmation. A process crash between the
// provider call and recording the result can leave a message modified but not
// recorded (at-least-once). All supported actions are idempotent, so re-running
// is harmless.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/geshas/mailroutine/pkg/models"
)

const (
	defaultBatchSize = 100
	maxPageSize      = 500
	// maxConsecutiveItemFailures stops per-message fallback once a batch looks
	// systematically broken.
	maxConsecutiveItemFailures = 5
)

// Engine runs routines. Zero values for optional fields use safe defaults.
type Engine struct {
	Provider  MailProvider
	Recorder  Recorder
	Logger    *slog.Logger
	BatchSize int          // messages per modify call
	Retry     *RetryPolicy // nil => DefaultRetryPolicy
	Now       func() time.Time
}

// Request describes one run.
type Request struct {
	Query       string
	Action      models.Action
	MaxMessages int
	// DryRun searches and records what would happen without modifying anything.
	DryRun bool
	// TraceLabel, when set, is added to every message the action succeeded on so the
	// handled mail can be found later. A failure to add it never fails the action.
	TraceLabel string
}

// Result summarises a run.
type Result struct {
	Status    models.ExecutionStatus
	Matched   int
	Processed int
	Failed    int
	// Truncated is true when more messages matched than MaxMessages allowed.
	Truncated bool
	// TraceFailed counts messages that were handled but could not get the TraceLabel.
	TraceFailed int
	// Err is the error that ended or degraded the run, if any.
	Err error
}

func (e *Engine) logger() *slog.Logger {
	if e.Logger != nil {
		return e.Logger
	}
	return slog.New(slog.DiscardHandler)
}

func (e *Engine) now() time.Time {
	if e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}

func (e *Engine) retry() RetryPolicy {
	if e.Retry != nil {
		return *e.Retry
	}
	return DefaultRetryPolicy
}

func (e *Engine) batchSize() int {
	if e.BatchSize > 0 {
		return e.BatchSize
	}
	return defaultBatchSize
}

// Validate checks a request.
func (r Request) Validate() error {
	errs := models.ValidationErrors{}
	if r.Query == "" {
		errs["query"] = "is required"
	}
	if !r.Action.Type.Valid() {
		errs["action"] = "unsupported action"
	}
	if (r.Action.Type == models.ActionAddLabel || r.Action.Type == models.ActionRemoveLabel) && r.Action.Config.Label == "" {
		errs["action.label"] = "is required"
	}
	if r.MaxMessages < 1 || r.MaxMessages > models.MaxMessagesPerRunLimit {
		errs["max_messages"] = fmt.Sprintf("must be between 1 and %d", models.MaxMessagesPerRunLimit)
	}
	return errs.OrNil()
}

// Run executes the request. A returned error means the request was invalid;
// execution problems are reported in Result (Status/Err).
func (e *Engine) Run(ctx context.Context, req Request) (Result, error) {
	if err := req.Validate(); err != nil {
		return Result{}, err
	}
	log := e.logger()
	res := Result{}

	ids, truncated, err := e.collect(ctx, req.Query, req.MaxMessages)
	res.Matched = len(ids)
	res.Truncated = truncated
	if err != nil {
		res.Err = err
		res.Status = failureStatus(ctx, err, res)
		return res, nil
	}
	log.Debug("search complete", "matched", res.Matched, "truncated", truncated)

	if req.DryRun {
		for start := 0; start < len(ids); start += e.batchSize() {
			batch := ids[start:min(start+e.batchSize(), len(ids))]
			e.record(ctx, batch, req.Action.Type, models.ActionDryRun, "")
		}
		res.Status = models.ExecSuccess
		return res, nil
	}

	for start := 0; start < len(ids); start += e.batchSize() {
		if err := ctx.Err(); err != nil {
			res.Err = err
			break
		}
		batch := ids[start:min(start+e.batchSize(), len(ids))]
		ok, failed, traceFailed, ferr := e.applyBatch(ctx, req.Action, batch, req.TraceLabel)
		res.Processed += ok
		res.Failed += failed
		res.TraceFailed += traceFailed
		if ferr != nil && res.Err == nil {
			res.Err = ferr
		}
		if perr := e.progress(ctx, res); perr != nil {
			res.Err = perr
			break
		}
		if ferr != nil && IsFatal(ferr) {
			break
		}
	}
	res.Status = finalStatus(ctx, res)
	return res, nil
}

func finalStatus(ctx context.Context, res Result) models.ExecutionStatus {
	switch {
	case errors.Is(res.Err, ErrStopped) || ctx.Err() != nil:
		return models.ExecAborted
	case res.Failed == 0 && res.Err == nil:
		return models.ExecSuccess
	case res.Processed > 0:
		return models.ExecPartial
	default:
		return models.ExecFailed
	}
}

func failureStatus(ctx context.Context, err error, res Result) models.ExecutionStatus {
	if ctx.Err() != nil || errors.Is(err, context.Canceled) {
		return models.ExecAborted
	}
	return models.ExecFailed
}

// collect pages through search results until max IDs are gathered. Memory use
// is bounded by max (itself capped at models.MaxMessagesPerRunLimit).
func (e *Engine) collect(ctx context.Context, query string, max int) (ids []string, truncated bool, err error) {
	seen := make(map[string]struct{}, min(max, 1024))
	token := ""
	for {
		size := min(maxPageSize, max-len(ids))
		var page MessagePage
		err = e.retry().Do(ctx, func(ctx context.Context) error {
			var lerr error
			page, lerr = e.Provider.ListMessages(ctx, query, token, size)
			return lerr
		})
		if err != nil {
			return ids, false, fmt.Errorf("search: %w", err)
		}
		for _, id := range page.IDs {
			if _, dup := seen[id]; dup {
				continue
			}
			seen[id] = struct{}{}
			ids = append(ids, id)
			if len(ids) == max {
				break
			}
		}
		if page.NextPageToken == "" {
			return ids, false, nil
		}
		if len(ids) >= max {
			return ids, true, nil
		}
		token = page.NextPageToken
	}
}

// applyBatch applies the action to a batch, falling back to one-by-one calls
// when the batch call fails so a single bad message cannot block the others.
// Messages the action succeeded on are then tagged with traceLabel (if set).
func (e *Engine) applyBatch(ctx context.Context, action models.Action, batch []string, traceLabel string) (ok, failed, traceFailed int, err error) {
	berr := e.apply(ctx, action, batch)
	if berr == nil {
		e.record(ctx, batch, action.Type, models.ActionSuccess, "")
		return len(batch), 0, e.trace(ctx, batch, traceLabel), nil
	}
	if IsFatal(berr) || ctx.Err() != nil || len(batch) == 1 {
		e.record(ctx, batch, action.Type, models.ActionFailed, berr.Error())
		return 0, len(batch), 0, berr
	}
	e.logger().Warn("batch failed, retrying messages individually", "size", len(batch), "error", berr)
	consecutive := 0
	var okIDs []string
	finish := func() { traceFailed = e.trace(ctx, okIDs, traceLabel) }
	for i, id := range batch {
		if consecutive >= maxConsecutiveItemFailures || ctx.Err() != nil {
			rest := batch[i:]
			e.record(ctx, rest, action.Type, models.ActionFailed, berr.Error())
			failed += len(rest)
			break
		}
		ierr := e.apply(ctx, action, []string{id})
		if ierr == nil {
			consecutive = 0
			ok++
			okIDs = append(okIDs, id)
			e.record(ctx, []string{id}, action.Type, models.ActionSuccess, "")
			continue
		}
		consecutive++
		failed++
		e.record(ctx, []string{id}, action.Type, models.ActionFailed, ierr.Error())
		if IsFatal(ierr) {
			finish()
			return ok, failed, traceFailed, ierr
		}
	}
	finish()
	if failed > 0 {
		err = berr
	}
	return ok, failed, traceFailed, err
}

// trace adds the traceability label to messages that were handled successfully and
// returns how many could not be labelled. The action itself already succeeded, so a
// failure here is logged and reported but never turns the run into a failure.
func (e *Engine) trace(ctx context.Context, ids []string, label string) int {
	if label == "" || len(ids) == 0 {
		return 0
	}
	err := e.retry().Do(context.WithoutCancel(ctx), func(ctx context.Context) error { return e.Provider.AddLabel(ctx, ids, label) })
	if err != nil {
		e.logger().Warn("adding traceability label failed", "label", label, "messages", len(ids), "error", err)
		return len(ids)
	}
	return 0
}

func (e *Engine) apply(ctx context.Context, action models.Action, ids []string) error {
	return e.retry().Do(ctx, func(ctx context.Context) error {
		p := e.Provider
		switch action.Type {
		case models.ActionArchive:
			return p.Archive(ctx, ids)
		case models.ActionTrash:
			return p.Trash(ctx, ids)
		case models.ActionMarkRead:
			return p.MarkRead(ctx, ids)
		case models.ActionMarkUnread:
			return p.MarkUnread(ctx, ids)
		case models.ActionAddLabel:
			return p.AddLabel(ctx, ids, action.Config.Label)
		case models.ActionRemoveLabel:
			return p.RemoveLabel(ctx, ids, action.Config.Label)
		}
		return fmt.Errorf("unsupported action %q", action.Type)
	})
}

func (e *Engine) record(ctx context.Context, ids []string, action models.ActionType, status models.ActionStatus, msg string) {
	if e.Recorder == nil || len(ids) == 0 {
		return
	}
	now := e.now()
	out := make([]models.MessageActionResult, len(ids))
	for i, id := range ids {
		out[i] = models.MessageActionResult{MessageID: id, Action: action, Status: status, Error: msg, CreatedAt: now}
	}
	// Recording must still happen when the run's context was cancelled.
	rctx := context.WithoutCancel(ctx)
	if err := e.Recorder.RecordActions(rctx, out); err != nil {
		e.logger().Error("recording message actions failed", "error", err)
	}
}

func (e *Engine) progress(ctx context.Context, r Result) error {
	if e.Recorder == nil {
		return nil
	}
	return e.Recorder.Progress(context.WithoutCancel(ctx), Progress{Matched: r.Matched, Processed: r.Processed, Failed: r.Failed})
}

// Preview describes what a query currently matches. It never modifies mail.
type Preview struct {
	Query    string                  `json:"query"`
	Messages []models.MessageSummary `json:"messages"`
	// Count is exact when CountIsEstimate is false; otherwise it is the
	// provider's approximation of the total number of matches.
	Count           int64 `json:"count"`
	CountIsEstimate bool  `json:"count_is_estimate"`
	// HasMore is true when matches exist beyond the returned sample.
	HasMore bool `json:"has_more"`
}

// Preview returns up to sample matching messages with metadata.
func (e *Engine) Preview(ctx context.Context, query string, sample int) (Preview, error) {
	if query == "" {
		return Preview{}, models.ValidationErrors{"query": "is required"}
	}
	sample = max(1, min(sample, 100))
	var page MessagePage
	err := e.retry().Do(ctx, func(ctx context.Context) error {
		var lerr error
		page, lerr = e.Provider.ListMessages(ctx, query, "", sample)
		return lerr
	})
	if err != nil {
		return Preview{}, fmt.Errorf("search: %w", err)
	}
	out := Preview{Query: query, Messages: []models.MessageSummary{}}
	if len(page.IDs) > 0 {
		var msgs []models.MessageSummary
		err = e.retry().Do(ctx, func(ctx context.Context) error {
			var gerr error
			msgs, gerr = e.Provider.GetSummaries(ctx, page.IDs)
			return gerr
		})
		if err != nil {
			return Preview{}, fmt.Errorf("fetch metadata: %w", err)
		}
		out.Messages = msgs
	}
	out.HasMore = page.NextPageToken != ""
	if out.HasMore {
		out.Count = max(page.EstimatedTotal, int64(len(page.IDs)))
		out.CountIsEstimate = true
	} else {
		out.Count = int64(len(page.IDs))
	}
	return out, nil
}
