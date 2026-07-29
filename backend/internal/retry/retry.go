// Package retry provides a tiny, dependency-free helper for retrying a fallible
// operation with exponential backoff, bounded by a context. It is used to retry
// the single genuinely transient step in PCB generation — the Filer network
// upload — now that tasks no longer auto-retry at the asynq level.
package retry

import (
	"context"
	"time"
)

// Do runs fn up to attempts times. After a failed attempt it waits
// baseDelay<<attempt (exponential backoff) before the next try, aborting early
// if ctx is cancelled. It returns nil on the first success, or the last error
// once attempts are exhausted, ctx is cancelled, or fn returns a non-retryable
// error.
//
// retryable classifies an error: return false to stop immediately (a permanent
// failure). A nil retryable treats every error as retryable. attempts <= 0 is
// treated as a single attempt.
func Do(ctx context.Context, attempts int, baseDelay time.Duration, retryable func(error) bool, fn func() error) error {
	if attempts < 1 {
		attempts = 1
	}

	var err error
	for attempt := 0; attempt < attempts; attempt++ {
		if err = fn(); err == nil {
			return nil
		}
		if retryable != nil && !retryable(err) {
			return err
		}
		// No point sleeping after the final attempt.
		if attempt == attempts-1 {
			break
		}

		delay := baseDelay << uint(attempt)
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return err
		case <-timer.C:
		}
	}
	return err
}
