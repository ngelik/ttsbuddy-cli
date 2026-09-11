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

// RetryResult is the complete outcome of a retry sequence. EffectiveKey is
// the key used on the last request that was actually sent. In particular, if
// cancellation happens during a backoff after a provider requested key
// rotation, the last-sent key remains the safe identity to report for
// ambiguous recovery.
type RetryResult struct {
	Response     *TTSResponse
	Status       int
	Err          error
	EffectiveKey string
}

// WithRetry executes fn with retry logic.
// - On 429: waits Retry-After seconds, retries with same key.
// - On retryable 5xx: backoff with jitter, retries with same or new key.
// - If NeedsNewIdempotencyKey: generates a fresh key before retry.
// - On non-retryable errors: returns immediately.
func WithRetry(ctx context.Context, cfg RetryConfig, fn SpeakFunc, initialKey string) (*TTSResponse, int, error) {
	result := WithRetryResult(ctx, cfg, fn, initialKey)
	return result.Response, result.Status, result.Err
}

// WithRetryResult executes fn with retry logic and also returns the effective
// idempotency identity for recovery guidance. Existing callers can continue to
// use WithRetry without changing their signatures.
func WithRetryResult(ctx context.Context, cfg RetryConfig, fn SpeakFunc, initialKey string) RetryResult {
	key := initialKey
	lastSentKey := initialKey

	for attempt := 0; attempt <= cfg.MaxRetries; attempt++ {
		lastSentKey = key
		resp, status, err := fn(key)

		// No error or non-retryable error — return as-is
		if err == nil {
			return RetryResult{Response: resp, Status: status, EffectiveKey: lastSentKey}
		}

		var apiErr *APIResponseError
		if !errors.As(err, &apiErr) {
			// Network/transport error — retryable with same key
			if attempt >= cfg.MaxRetries {
				return RetryResult{Response: resp, Status: status, Err: err, EffectiveKey: lastSentKey}
			}
			if waitErr := sleepWithContext(ctx, backoffDelay(attempt, cfg)); waitErr != nil {
				return RetryResult{Response: resp, Status: status, Err: waitErr, EffectiveKey: lastSentKey}
			}
			continue
		}

		// API responded with an error
		if !IsRetryable(apiErr.ErrorCode(), apiErr.StatusCode) {
			return RetryResult{Response: resp, Status: status, Err: err, EffectiveKey: lastSentKey}
		}

		if attempt >= cfg.MaxRetries {
			return RetryResult{Response: resp, Status: status, Err: err, EffectiveKey: lastSentKey}
		}

		// Determine if we need a new idempotency key
		if NeedsNewIdempotencyKey(apiErr.Response.Error) {
			key = GenerateNew()
		}

		// Calculate wait time
		var delay time.Duration
		if resp != nil && resp.RetryAfterSeconds != nil {
			seconds := *resp.RetryAfterSeconds
			if cfg.MaxDelay > 0 && int64(seconds) > int64(cfg.MaxDelay/time.Second) {
				return RetryResult{Response: resp, Status: status, Err: err, EffectiveKey: lastSentKey}
			}
			delay = time.Duration(seconds) * time.Second
		} else {
			delay = backoffDelay(attempt, cfg)
		}
		// Never retry earlier than a valid server hint. If that hint exceeds
		// the bounded automatic policy, return the original response so the
		// caller can present its retry timing and an explicit next action.
		if cfg.MaxDelay > 0 && delay > cfg.MaxDelay {
			return RetryResult{Response: resp, Status: status, Err: err, EffectiveKey: lastSentKey}
		}

		if waitErr := sleepWithContext(ctx, delay); waitErr != nil {
			return RetryResult{Response: resp, Status: status, Err: waitErr, EffectiveKey: lastSentKey}
		}
	}

	return RetryResult{Err: errors.New("max retries exceeded"), EffectiveKey: lastSentKey}
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
