package provider

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// testConfig returns a RetryConfig that never really sleeps: it records the
// delays it was asked for instead.
func testConfig(delays *[]time.Duration) RetryConfig {
	cfg := DefaultRetryConfig()
	cfg.randFloat = func() float64 { return 0 } // no jitter subtraction
	cfg.sleep = func(ctx context.Context, d time.Duration) error {
		*delays = append(*delays, d)
		return ctx.Err()
	}
	return cfg
}

func TestRetrySucceedsFirstTry(t *testing.T) {
	var delays []time.Duration
	calls := 0

	err := Retry(context.Background(), testConfig(&delays), nil, func(context.Context) error {
		calls++
		return nil
	})

	require.NoError(t, err)
	require.Equal(t, 1, calls)
	require.Empty(t, delays)
}

func TestRetrySucceedsAfterTransientFailures(t *testing.T) {
	var delays []time.Duration
	calls := 0
	transient := errors.New("429 too many requests")

	err := Retry(context.Background(), testConfig(&delays), func(error) bool { return true }, func(context.Context) error {
		calls++
		if calls < 3 {
			return transient
		}
		return nil
	})

	require.NoError(t, err)
	require.Equal(t, 3, calls)
	// Exponential: base, 2*base.
	require.Equal(t, []time.Duration{DefaultBaseDelay, 2 * DefaultBaseDelay}, delays)
}

func TestRetryGivesUpAfterMaxAttempts(t *testing.T) {
	var delays []time.Duration
	calls := 0
	transient := errors.New("503 unavailable")

	err := Retry(context.Background(), testConfig(&delays), func(error) bool { return true }, func(context.Context) error {
		calls++
		return transient
	})

	require.ErrorIs(t, err, transient)
	require.Equal(t, DefaultMaxAttempts, calls)
	require.Len(t, delays, DefaultMaxAttempts-1)
	require.Contains(t, err.Error(), "giving up after 5 attempt(s)")
}

func TestRetryStopsOnNonRetryableError(t *testing.T) {
	var delays []time.Duration
	calls := 0
	fatal := errors.New("401 unauthorized")

	err := Retry(context.Background(), testConfig(&delays), func(err error) bool {
		return !errors.Is(err, fatal)
	}, func(context.Context) error {
		calls++
		return fatal
	})

	require.ErrorIs(t, err, fatal)
	require.Equal(t, fatal, err, "a non-retryable error is returned unwrapped")
	require.Equal(t, 1, calls)
	require.Empty(t, delays)
}

func TestRetryRespectsContextCancellationBeforeStarting(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var delays []time.Duration
	calls := 0
	err := Retry(ctx, testConfig(&delays), nil, func(context.Context) error {
		calls++
		return nil
	})

	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, 0, calls)
}

func TestRetryStopsWhenContextIsCancelledDuringBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	transient := errors.New("500 boom")

	cfg := DefaultRetryConfig()
	cfg.randFloat = func() float64 { return 0 }
	cfg.sleep = func(ctx context.Context, d time.Duration) error {
		cancel() // the run is aborted while we are waiting to retry
		return ctx.Err()
	}

	calls := 0
	err := Retry(ctx, cfg, func(error) bool { return true }, func(context.Context) error {
		calls++
		return transient
	})

	require.Equal(t, 1, calls)
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, err, transient)
	require.Contains(t, err.Error(), "retry aborted after 1 attempt(s)")
}

func TestRetryDoesNotRetryContextErrors(t *testing.T) {
	var delays []time.Duration
	calls := 0

	err := Retry(context.Background(), testConfig(&delays), func(error) bool { return true }, func(context.Context) error {
		calls++
		return context.DeadlineExceeded
	})

	require.ErrorIs(t, err, context.DeadlineExceeded)
	require.Equal(t, 1, calls)
}

func TestRetryValueReturnsValue(t *testing.T) {
	var delays []time.Duration
	calls := 0

	got, err := RetryValue(context.Background(), testConfig(&delays), nil, func(context.Context) (string, error) {
		calls++
		if calls == 1 {
			return "", errors.New("transient")
		}
		return "payload", nil
	})

	require.NoError(t, err)
	require.Equal(t, "payload", got)
	require.Equal(t, 2, calls)
}

func TestRetryValueZeroOnFailure(t *testing.T) {
	var delays []time.Duration
	got, err := RetryValue(context.Background(), testConfig(&delays), nil, func(context.Context) (int, error) {
		return 7, errors.New("nope")
	})

	require.Error(t, err)
	require.Equal(t, 0, got)
}

func TestZeroRetryConfigUsesDefaults(t *testing.T) {
	cfg := RetryConfig{}.withDefaults()
	require.Equal(t, DefaultMaxAttempts, cfg.MaxAttempts)
	require.Equal(t, DefaultBaseDelay, cfg.BaseDelay)
	require.Equal(t, DefaultMaxDelay, cfg.MaxDelay)
	require.Equal(t, DefaultJitter, cfg.Jitter)
	require.NotNil(t, cfg.sleep)
	require.NotNil(t, cfg.randFloat)
}

func TestNegativeJitterDisablesJitter(t *testing.T) {
	cfg := RetryConfig{Jitter: -1}.withDefaults()
	require.Zero(t, cfg.Jitter)
	require.Equal(t, DefaultBaseDelay, cfg.delay(1))
	require.Equal(t, 2*DefaultBaseDelay, cfg.delay(2))
}

func TestDelayJitterBoundsAndCap(t *testing.T) {
	cfg := DefaultRetryConfig().withDefaults()

	for attempt := 1; attempt <= 12; attempt++ {
		expected := time.Duration(float64(DefaultBaseDelay) * pow2(attempt-1))
		if expected > DefaultMaxDelay || expected <= 0 {
			expected = DefaultMaxDelay
		}
		d := cfg.delay(attempt)
		require.LessOrEqual(t, d, expected, "attempt %d must not exceed the un-jittered delay", attempt)
		require.GreaterOrEqual(t, d, time.Duration(float64(expected)*(1-DefaultJitter)), "attempt %d jitter floor", attempt)
		require.LessOrEqual(t, d, DefaultMaxDelay)
	}
}

func TestRetryableStatus(t *testing.T) {
	for _, code := range []int{408, 429, 500, 502, 503, 504} {
		require.True(t, RetryableStatus(code), "%d should be retryable", code)
	}
	for _, code := range []int{200, 301, 400, 401, 403, 404} {
		require.False(t, RetryableStatus(code), "%d should not be retryable", code)
	}
}

func pow2(n int) float64 {
	out := 1.0
	for range n {
		out *= 2
	}
	return out
}
