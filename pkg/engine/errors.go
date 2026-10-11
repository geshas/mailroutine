package engine

import (
	"errors"
	"time"
)

// Sentinel errors a MailProvider should wrap so the engine can decide how to react.
var (
	// ErrAuthRevoked means the account's authorization is no longer valid.
	// Retrying cannot help; the user must reconnect the account.
	ErrAuthRevoked = errors.New("mail account authorization revoked or expired")
	// ErrInvalidQuery means the provider rejected the search query.
	ErrInvalidQuery = errors.New("invalid search query")
	// ErrStopped may be returned by a Recorder's Progress to end a run early
	// (for example because the routine was disabled).
	ErrStopped = errors.New("run stopped")
)

// RetryableError marks a failure as transient (rate limit, 5xx, network).
type RetryableError struct {
	Err error
	// After is a provider supplied minimum wait (for example Retry-After).
	After time.Duration
}

func (e *RetryableError) Error() string { return e.Err.Error() }
func (e *RetryableError) Unwrap() error { return e.Err }

// Retryable wraps err as transient.
func Retryable(err error, after time.Duration) error {
	if err == nil {
		return nil
	}
	return &RetryableError{Err: err, After: after}
}

// IsRetryable reports whether err (or anything it wraps) is transient.
func IsRetryable(err error) bool {
	var r *RetryableError
	return errors.As(err, &r)
}

// IsFatal reports errors that make continuing the run pointless.
func IsFatal(err error) bool {
	return errors.Is(err, ErrAuthRevoked) || errors.Is(err, ErrInvalidQuery) || errors.Is(err, ErrStopped)
}
