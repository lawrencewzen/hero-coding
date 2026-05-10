package agent

import (
	"context"
	"errors"
	"math/rand"
	"time"

	"hero-coding/internal/logging"
)

// Rate-limit retry policy. Free-tier OpenAI-compatible endpoints (notably
// OpenRouter free models) frequently return 429 from upstream providers
// during bursty traffic. The retry budget below absorbs ~60s of cumulative
// throttling before giving up — long enough for typical brief stalls,
// short enough that a permanently-out-of-quota key fails fast.
const (
	rateLimitMaxAttempts = 5
	rateLimitBaseDelay   = 2 * time.Second
	rateLimitMaxDelay    = 32 * time.Second
)

// retryRateLimit invokes fn until it succeeds, returns a non-retryable error,
// or the rate-limit retry budget is exhausted. Between attempts it sleeps
// with exponential backoff (2s, 4s, 8s, 16s, capped at 32s) plus ±25% jitter
// to spread retries across concurrent workers. Honors ctx — returns ctx.Err()
// if cancelled during a sleep.
func retryRateLimit[T any](ctx context.Context, fn func() (T, error)) (T, error) {
	var zero T
	var lastErr error
	for attempt := 0; attempt < rateLimitMaxAttempts; attempt++ {
		out, err := fn()
		if err == nil {
			return out, nil
		}
		lastErr = err

		var llmErr *LLMError
		if !errors.As(err, &llmErr) || llmErr.Kind != ErrRateLimit {
			return zero, err // not retryable — bail immediately
		}
		if attempt+1 >= rateLimitMaxAttempts {
			break // budget exhausted, return last error
		}

		delay := backoffFor(attempt)

		logging.FromContext(ctx).Info("llm.rate_limited",
			"attempt", attempt+1,
			"max_attempts", rateLimitMaxAttempts,
			"backoff_ms", delay.Milliseconds(),
		)

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return zero, ctx.Err()
		case <-timer.C:
		}
	}
	return zero, lastErr
}

// backoffFor returns the sleep duration for the given attempt index (0-based)
// using exponential backoff (base × 2^attempt) capped at the max, with ±25% jitter.
func backoffFor(attempt int) time.Duration {
	delay := rateLimitBaseDelay << attempt
	if delay > rateLimitMaxDelay {
		delay = rateLimitMaxDelay
	}
	jitter := time.Duration(rand.Int63n(int64(delay)/2)) - delay/4
	return delay + jitter
}
