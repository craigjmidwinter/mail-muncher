package provider

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand/v2"
	"time"
)

// Retry defaults. Providers that need different numbers build their own
// RetryConfig; zero fields fall back to these.
const (
	DefaultMaxAttempts = 5
	DefaultBaseDelay   = 500 * time.Millisecond
	DefaultMaxDelay    = 30 * time.Second
	DefaultJitter      = 0.5
)

// RetryConfig tunes exponential backoff with jitter. The zero value is usable
// and behaves like DefaultRetryConfig.
type RetryConfig struct {
	// MaxAttempts is the total number of calls, not the number of retries.
	MaxAttempts int
	// BaseDelay is the delay before the second attempt; it doubles thereafter.
	BaseDelay time.Duration
	// MaxDelay caps the pre-jitter delay.
	MaxDelay time.Duration
	// Jitter is the fraction of each delay that is randomised, in [0,1]. With
	// the default 0.5 the actual delay is uniform in [0.5d, d]. Zero means the
	// default; use a negative value to disable jitter entirely.
	Jitter float64

	// Test hooks (in-package only).
	sleep     func(context.Context, time.Duration) error
	randFloat func() float64
}

// DefaultRetryConfig is the backoff every provider should use unless it has a
// reason not to: 5 attempts, 500ms base, doubling, capped at 30s, 50% jitter.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxAttempts: DefaultMaxAttempts,
		BaseDelay:   DefaultBaseDelay,
		MaxDelay:    DefaultMaxDelay,
		Jitter:      DefaultJitter,
	}
}

// Retry runs op until it succeeds, until an error is judged non-retryable, or
// until cfg.MaxAttempts is exhausted.
//
// retryable decides whether an error is worth another attempt; a nil retryable
// means every error is retried. Errors that are (or wrap) ctx's error are never
// retried. On give-up the last error is returned wrapped, so errors.Is/As still
// see through to the provider's error.
func Retry(ctx context.Context, cfg RetryConfig, retryable func(error) bool, op func(context.Context) error) error {
	_, err := RetryValue(ctx, cfg, retryable, func(ctx context.Context) (struct{}, error) {
		return struct{}{}, op(ctx)
	})
	return err
}

// RetryValue is Retry for an operation that produces a value.
func RetryValue[T any](ctx context.Context, cfg RetryConfig, retryable func(error) bool, op func(context.Context) (T, error)) (T, error) {
	cfg = cfg.withDefaults()

	var zero T
	if err := ctx.Err(); err != nil {
		return zero, err
	}

	var lastErr error
	for attempt := 1; attempt <= cfg.MaxAttempts; attempt++ {
		v, err := op(ctx)
		if err == nil {
			return v, nil
		}
		lastErr = err

		// A cancelled context is never a transient failure.
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return zero, err
		}
		if ctxErr := ctx.Err(); ctxErr != nil {
			return zero, errors.Join(err, ctxErr)
		}
		if retryable != nil && !retryable(err) {
			return zero, err
		}
		if attempt == cfg.MaxAttempts {
			break
		}
		if err := cfg.sleep(ctx, cfg.delay(attempt)); err != nil {
			return zero, fmt.Errorf("retry aborted after %d attempt(s): %w", attempt, errors.Join(lastErr, err))
		}
	}
	return zero, fmt.Errorf("giving up after %d attempt(s): %w", cfg.MaxAttempts, lastErr)
}

// RetryableStatus reports whether an HTTP status code is worth retrying:
// 408 (timeout), 429 (rate limited) and any 5xx.
//
// Note for Google APIs: a rate-limited call can also come back as 403 with
// reason "rateLimitExceeded"/"userRateLimitExceeded". That needs the error body,
// which this helper does not see, so callers must add that case themselves.
func RetryableStatus(code int) bool {
	switch {
	case code == 408, code == 429:
		return true
	case code >= 500 && code <= 599:
		return true
	default:
		return false
	}
}

func (c RetryConfig) withDefaults() RetryConfig {
	if c.MaxAttempts <= 0 {
		c.MaxAttempts = DefaultMaxAttempts
	}
	if c.BaseDelay <= 0 {
		c.BaseDelay = DefaultBaseDelay
	}
	if c.MaxDelay <= 0 {
		c.MaxDelay = DefaultMaxDelay
	}
	switch {
	case c.Jitter == 0:
		c.Jitter = DefaultJitter
	case c.Jitter < 0:
		c.Jitter = 0
	case c.Jitter > 1:
		c.Jitter = 1
	}
	if c.randFloat == nil {
		c.randFloat = rand.Float64
	}
	if c.sleep == nil {
		c.sleep = sleepCtx
	}
	return c
}

// delay returns the wait before attempt+1, given a 1-based attempt number.
func (c RetryConfig) delay(attempt int) time.Duration {
	if attempt < 1 {
		attempt = 1
	}
	backoff := float64(c.BaseDelay) * math.Pow(2, float64(attempt-1))
	if ceiling := float64(c.MaxDelay); backoff > ceiling {
		backoff = ceiling
	}
	// Uniform in [(1-jitter)*backoff, backoff].
	d := backoff * (1 - c.Jitter*c.randFloat())
	if d < 0 {
		d = 0
	}
	return time.Duration(d)
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
