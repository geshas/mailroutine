package engine

import (
	"context"
	"errors"
	"math/rand/v2"
	"time"
)

// RetryPolicy configures bounded exponential backoff with jitter.
type RetryPolicy struct {
	MaxAttempts int           // total attempts including the first; <=1 disables retries
	BaseDelay   time.Duration // delay before the first retry
	MaxDelay    time.Duration // upper bound for any single wait
	// Sleep waits for d or until ctx ends. Nil uses a real timer; tests override it.
	Sleep func(ctx context.Context, d time.Duration) error
}

// DefaultRetryPolicy is used when an Engine has no explicit policy.
var DefaultRetryPolicy = RetryPolicy{MaxAttempts: 5, BaseDelay: 500 * time.Millisecond, MaxDelay: 30 * time.Second}

func (p RetryPolicy) delay(attempt int, hint time.Duration) time.Duration {
	d := p.BaseDelay << attempt
	if d <= 0 || d > p.MaxDelay {
		d = p.MaxDelay
	}
	// Full jitter in [d/2, d] keeps retries spread out without collapsing to zero.
	d = d/2 + time.Duration(rand.Int64N(int64(d/2)+1)) // #nosec G404 -- backoff jitter, not security sensitive
	if hint > d {
		d = hint
	}
	if d > p.MaxDelay {
		d = p.MaxDelay
	}
	return d
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

// Do runs fn, retrying only transient errors. Permanent errors return at once.
func (p RetryPolicy) Do(ctx context.Context, fn func(context.Context) error) error {
	sleep := p.Sleep
	if sleep == nil {
		sleep = sleepCtx
	}
	var err error
	for attempt := 0; ; attempt++ {
		if cerr := ctx.Err(); cerr != nil {
			return cerr
		}
		err = fn(ctx)
		if err == nil || !IsRetryable(err) || attempt+1 >= p.MaxAttempts {
			return err
		}
		var re *RetryableError
		errors.As(err, &re)
		if serr := sleep(ctx, p.delay(attempt, re.After)); serr != nil {
			return serr
		}
	}
}
