package main

// retry.go — withRetry: exponential backoff + jitter around any Provider.
//
// Real LLM endpoints return 429 (rate limit) and 5xx (overloaded / transient)
// constantly. Upstream aider handles this in simple_send_with_retries
// (aider/models.py L1039-1079): a `while True` loop that catches litellm's
// exceptions, asks `ex_info.retry` whether the error is transient, doubles
// `retry_delay` each time, and gives up once the delay exceeds a cap.
//
// We do the same with a decorator. RetryProvider wraps another Provider and
// implements Provider itself, so it composes: the registry builds a concrete
// provider, RetryProvider wraps it, and the loop above sees just a Provider.

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"time"
)

// RetryConfig tunes the backoff. Defaults mirror upstream: a 0.125s base that
// doubles, capped so we don't sleep forever.
type RetryConfig struct {
	MaxRetries int           // max ATTEMPTS beyond the first try (0 = no retries)
	BaseDelay  time.Duration // first backoff (upstream: 125ms)
	MaxDelay   time.Duration // cap on a single sleep (upstream: RETRY_TIMEOUT)
	// Jitter randomizes each delay in [delay/2, delay] to avoid thundering herds.
	// Disable for deterministic tests.
	Jitter bool
	// rng / sleep are injectable so tests are fast and deterministic. nil means
	// use the package defaults.
	rng   *rand.Rand
	sleep func(time.Duration)
}

// DefaultRetryConfig matches aider's numbers: start at 125ms, double, give up
// once the next delay would exceed ~32s, with jitter on.
func DefaultRetryConfig() RetryConfig {
	return RetryConfig{
		MaxRetries: 6,
		BaseDelay:  125 * time.Millisecond,
		MaxDelay:   32 * time.Second,
		Jitter:     true,
	}
}

// RetryProvider decorates a Provider with backoff retries. It satisfies Provider
// (and forwards streaming if the inner provider supports it via WrapStreaming).
type RetryProvider struct {
	inner Provider
	cfg   RetryConfig
}

// NewRetryProvider wraps inner with the given config.
func NewRetryProvider(inner Provider, cfg RetryConfig) *RetryProvider {
	if cfg.BaseDelay <= 0 {
		cfg.BaseDelay = 125 * time.Millisecond
	}
	if cfg.MaxDelay <= 0 {
		cfg.MaxDelay = 32 * time.Second
	}
	if cfg.sleep == nil {
		cfg.sleep = time.Sleep
	}
	return &RetryProvider{inner: inner, cfg: cfg}
}

// CreateMessage calls the inner provider, retrying transient failures with
// exponential backoff. This is the unary path; streaming has its own wrapper.
func (r *RetryProvider) CreateMessage(ctx context.Context, req CreateMessageRequest) (*CreateMessageResponse, error) {
	return withRetry(ctx, r.cfg, func() (*CreateMessageResponse, error) {
		return r.inner.CreateMessage(ctx, req)
	})
}

// withRetry runs `call` up to cfg.MaxRetries+1 times. After each transient
// failure it sleeps `delay`, then doubles `delay` (capped at MaxDelay), exactly
// as upstream multiplies retry_delay by 2 and stops once it passes RETRY_TIMEOUT.
//
// Three exit conditions:
//   - success            -> return the response
//   - non-retryable err  -> return immediately (fail fast, like a 400)
//   - retries exhausted  -> return the last error wrapped with attempt count
func withRetry[T any](ctx context.Context, cfg RetryConfig, call func() (T, error)) (T, error) {
	var zero T
	delay := cfg.BaseDelay

	for attempt := 0; ; attempt++ {
		out, err := call()
		if err == nil {
			return out, nil
		}

		// Fatal errors (e.g. 400 bad request, an auth failure) must not be
		// retried — burning the rate limit on a request that can never succeed
		// is worse than failing fast. This is the `should_retry` gate upstream.
		if !isRetryable(err) {
			return zero, err
		}

		// Out of attempts: surface the last error so the caller sees the cause.
		if attempt >= cfg.MaxRetries {
			return zero, fmt.Errorf("giving up after %d attempts: %w", attempt+1, err)
		}

		// Respect cancellation while we wait. A cancelled context shouldn't sleep.
		sleepFor := applyJitter(cfg, delay)
		select {
		case <-ctx.Done():
			return zero, ctx.Err()
		default:
		}
		cfg.sleep(sleepFor)

		// Exponential growth, capped. Upstream: retry_delay *= 2.
		delay *= 2
		if delay > cfg.MaxDelay {
			delay = cfg.MaxDelay
		}
	}
}

// applyJitter returns delay randomized into [delay/2, delay] when jitter is on,
// so a fleet of clients doesn't retry in lockstep. Deterministic (returns delay
// unchanged) when Jitter is false — which the tests rely on.
func applyJitter(cfg RetryConfig, delay time.Duration) time.Duration {
	if !cfg.Jitter || delay <= 0 {
		return delay
	}
	half := delay / 2
	var n int64
	if cfg.rng != nil {
		n = cfg.rng.Int63n(int64(half) + 1)
	} else {
		n = rand.Int63n(int64(half) + 1)
	}
	return half + time.Duration(n)
}

// isRetryable decides whether an error is transient. We retry:
//   - HTTP 429 (rate limited) and any 5xx (server/overloaded), via statusError
//   - errors explicitly tagged transient (transientError, used by fakes/tests)
//
// Everything else (4xx other than 429, decode errors, bad input) fails fast.
// Upstream gets this verdict from litellm's exception taxonomy (ex_info.retry);
// we read the status code directly.
func isRetryable(err error) bool {
	var se *statusError
	if errors.As(err, &se) {
		return se.Code == 429 || se.Code >= 500
	}
	var te *transientError
	if errors.As(err, &te) {
		return true
	}
	return false
}

// transientError marks an error as retryable without an HTTP status — handy for
// network blips and for the flaky fake Provider in the tests.
type transientError struct{ err error }

func (e *transientError) Error() string { return e.err.Error() }
func (e *transientError) Unwrap() error { return e.err }

// Transient wraps err so withRetry treats it as retryable.
func Transient(err error) error { return &transientError{err: err} }
