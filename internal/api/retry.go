package api

import (
	"context"
	"errors"
	"math"
	"math/rand"
	"time"
)

// RetryConfig controls retry behavior.
type RetryConfig struct {
	MaxRetries int
	BaseDelay  time.Duration
	MaxDelay   time.Duration
}

// DefaultRetryConfig returns the standard retry configuration.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries: 3,
		BaseDelay:  1 * time.Second,
		MaxDelay:   30 * time.Second,
	}
}

// SpeakFunc is the function signature for the retryable speak operation.
type SpeakFunc func(idempotencyKey string) (*TTSResponse, int, error)

// WithRetry executes fn with retry logic.
// - On 429: waits Retry-After seconds, retries with same key.
// - On retryable 5xx: backoff with jitter, retries with same or new key.
// - If NeedsNewIdempotencyKey: generates a fresh key before retry.
// - On non-retryable errors: returns immediately.
func WithRetry(ctx context.Context, cfg RetryConfig, fn SpeakFunc, initialKey string) (*TTSResponse, int, error) {
	key := initialKey

	for attempt := 0; attempt <= cfg.MaxRetries; attempt++ {
		resp, status, err := fn(key)

		// No error or non-retryable error — return as-is
		if err == nil {
			return resp, status, nil
		}

		var apiErr *APIResponseError
		if !errors.As(err, &apiErr) {
			// Network/transport error — retryable with same key
			if attempt >= cfg.MaxRetries {
				return resp, status, err
			}
			if waitErr := sleepWithContext(ctx, backoffDelay(attempt, cfg)); waitErr != nil {
				return nil, 0, waitErr
			}
			continue
		}

		// API responded with an error
		if !IsRetryable(apiErr.ErrorCode(), apiErr.StatusCode) {
			return resp, status, err
		}

		if attempt >= cfg.MaxRetries {
			return resp, status, err
		}

		// Determine if we need a new idempotency key
		if NeedsNewIdempotencyKey(apiErr.Response.Error) {
			key = GenerateNew()
		}

		// Calculate wait time
		var delay time.Duration
		if apiErr.StatusCode == 429 && resp != nil && resp.RetryAfterSeconds != nil {
			delay = time.Duration(*resp.RetryAfterSeconds) * time.Second
		} else {
			delay = backoffDelay(attempt, cfg)
		}

		if waitErr := sleepWithContext(ctx, delay); waitErr != nil {
			return nil, 0, waitErr
		}
	}

	return nil, 0, errors.New("max retries exceeded")
}

func backoffDelay(attempt int, cfg RetryConfig) time.Duration {
	base := float64(cfg.BaseDelay) * math.Pow(2, float64(attempt))
	jitter := float64(time.Second) * rand.Float64()
	delay := time.Duration(math.Min(base+jitter, float64(cfg.MaxDelay)))
	return delay
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
