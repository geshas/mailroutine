package engine

import (
	"context"

	"github.com/geshas/mailroutine/pkg/models"
)

// MessagePage is one page of search results.
type MessagePage struct {
	IDs           []string
	NextPageToken string // empty when there are no further pages
	// EstimatedTotal is the provider's estimate of all matches; 0 if unknown.
	EstimatedTotal int64
}

// MailProvider abstracts a mailbox. Implementations must wrap failures with the
// sentinel/retryable errors in this package. Every mutating method must be
// idempotent so that retries are safe. Implementations must never fetch or
// return message bodies.
type MailProvider interface {
	// ListMessages searches using the provider's native query syntax.
	ListMessages(ctx context.Context, query, pageToken string, pageSize int) (MessagePage, error)
	// GetSummaries returns sender/subject/date metadata for the given ids.
	GetSummaries(ctx context.Context, ids []string) ([]models.MessageSummary, error)

	Archive(ctx context.Context, ids []string) error
	Trash(ctx context.Context, ids []string) error
	MarkRead(ctx context.Context, ids []string) error
	MarkUnread(ctx context.Context, ids []string) error
	AddLabel(ctx context.Context, ids []string, label string) error
	RemoveLabel(ctx context.Context, ids []string, label string) error
}

// Progress is a snapshot of a running execution.
type Progress struct {
	Matched   int
	Processed int
	Failed    int
}

// Recorder receives per-message results and progress. Both methods are
// optional; a nil Recorder discards everything.
type Recorder interface {
	RecordActions(ctx context.Context, results []models.MessageActionResult) error
	// Progress is called after every batch. Returning ErrStopped ends the run.
	Progress(ctx context.Context, p Progress) error
}
