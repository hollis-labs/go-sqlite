package txutil

import (
	"context"
	"database/sql"
	"math/rand"
	"time"
)

// RetryOptions controls [WithRetry] and [WithImmediateRetry].
//
// Zero values use defaults: MaxAttempts=5, BaseDelay=1ms, MaxDelay=100ms,
// Jitter=false (opt in via the field), IsRetryable=IsRetryableLock.
type RetryOptions struct {
	// MaxAttempts caps the total number of fn invocations. Must be at least 1.
	MaxAttempts int

	// BaseDelay is the initial sleep between attempts. Doubles on each retry,
	// capped by MaxDelay.
	BaseDelay time.Duration

	// MaxDelay caps the per-attempt sleep.
	MaxDelay time.Duration

	// Jitter, when true, multiplies the computed delay by a random factor in
	// [0.5, 1.5) to spread out concurrent retries. Defaults to false (off);
	// set to true to enable. Useful when many goroutines retry in lockstep.
	Jitter bool

	// IsRetryable classifies an error from fn. When nil, [IsRetryableLock] is
	// used. Errors for which the classifier returns false are returned
	// immediately without retry.
	IsRetryable func(error) bool
}

// WithRetry invokes fn until it succeeds, ctx is cancelled, MaxAttempts is
// reached, or fn returns a non-retryable error. The retry sleep is bounded
// exponential backoff with optional jitter; ctx.Done() preempts the sleep.
func WithRetry(ctx context.Context, opts RetryOptions, fn func() error) error {
	maxAttempts := opts.MaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 5
	}
	baseDelay := opts.BaseDelay
	if baseDelay <= 0 {
		baseDelay = time.Millisecond
	}
	maxDelay := opts.MaxDelay
	if maxDelay <= 0 {
		maxDelay = 100 * time.Millisecond
	}
	classify := opts.IsRetryable
	if classify == nil {
		classify = IsRetryableLock
	}

	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := fn(); err == nil {
			return nil
		} else if !classify(err) {
			return err
		} else {
			lastErr = err
		}

		if attempt+1 >= maxAttempts {
			break
		}

		delay := baseDelay << attempt
		if delay <= 0 || delay > maxDelay {
			delay = maxDelay
		}
		if opts.Jitter {
			factor := 0.5 + rand.Float64()
			delay = time.Duration(float64(delay) * factor)
		}

		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(delay):
		}
	}
	return lastErr
}

// WithImmediateRetry runs fn inside a BEGIN IMMEDIATE transaction with retry
// on retryable lock errors. The transaction is restarted from scratch on each
// retry; fn must be idempotent across retries (any side effects inside the
// transaction roll back when the txn rolls back).
func WithImmediateRetry(ctx context.Context, db *sql.DB, retry RetryOptions, fn func(*sql.Tx) error) error {
	return WithRetry(ctx, retry, func() error {
		return WithImmediate(ctx, db, fn)
	})
}
