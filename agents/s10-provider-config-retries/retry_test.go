package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

// flakyProvider fails its first `failTimes` calls, then succeeds. Each failure
// uses the supplied error (transient or fatal) so tests can steer the retry gate.
type flakyProvider struct {
	failTimes int
	failErr   error
	calls     int
}

func (f *flakyProvider) CreateMessage(ctx context.Context, req CreateMessageRequest) (*CreateMessageResponse, error) {
	f.calls++
	if f.calls <= f.failTimes {
		return nil, f.failErr
	}
	return &CreateMessageResponse{
		Role:       "assistant",
		Content:    []ContentBlock{{Type: "text", Text: "ok"}},
		StopReason: "end_turn",
	}, nil
}

// testConfig is a fast, deterministic RetryConfig: no real sleeping, no jitter,
// and it records the delays withRetry asked for so we can assert backoff growth.
func testConfig(maxRetries int, recorded *[]time.Duration) RetryConfig {
	return RetryConfig{
		MaxRetries: maxRetries,
		BaseDelay:  100 * time.Millisecond,
		MaxDelay:   800 * time.Millisecond,
		Jitter:     false,
		sleep: func(d time.Duration) {
			if recorded != nil {
				*recorded = append(*recorded, d)
			}
		},
	}
}

// TestRetry_SucceedsAfterTransientFailures: two 429s then success. The wrapper
// must retry through both and return the eventual response.
func TestRetry_SucceedsAfterTransientFailures(t *testing.T) {
	f := &flakyProvider{failTimes: 2, failErr: &statusError{Code: 429, Body: "rate limited"}}
	rp := NewRetryProvider(f, testConfig(5, nil))

	resp, err := rp.CreateMessage(context.Background(), CreateMessageRequest{})
	if err != nil {
		t.Fatalf("should have recovered after retries, got %v", err)
	}
	if firstText(resp.Content) != "ok" {
		t.Fatalf("unexpected response: %+v", resp)
	}
	if f.calls != 3 {
		t.Fatalf("expected 3 calls (2 fail + 1 ok), got %d", f.calls)
	}
}

// TestRetry_BackoffDoublesAndCaps: with 4 forced failures we should see the
// recorded delays double (100, 200, 400) and then clamp at MaxDelay (800).
func TestRetry_BackoffDoublesAndCaps(t *testing.T) {
	var delays []time.Duration
	f := &flakyProvider{failTimes: 99, failErr: Transient(errors.New("blip"))}
	rp := NewRetryProvider(f, testConfig(4, &delays))

	_, err := rp.CreateMessage(context.Background(), CreateMessageRequest{})
	if err == nil {
		t.Fatal("expected failure after exhausting retries")
	}
	// 4 retries => 4 sleeps between the 5 attempts.
	want := []time.Duration{
		100 * time.Millisecond,
		200 * time.Millisecond,
		400 * time.Millisecond,
		800 * time.Millisecond, // next would be 1600ms but MaxDelay caps it at 800
	}
	if len(delays) != len(want) {
		t.Fatalf("recorded %d delays, want %d: %v", len(delays), len(want), delays)
	}
	for i := range want {
		if delays[i] != want[i] {
			t.Fatalf("delay[%d] = %v, want %v (full: %v)", i, delays[i], want[i], delays)
		}
	}
}

// TestRetry_GivesUpAfterCap: the wrapper stops after MaxRetries+1 attempts and
// returns the last error wrapped.
func TestRetry_GivesUpAfterCap(t *testing.T) {
	f := &flakyProvider{failTimes: 99, failErr: &statusError{Code: 503, Body: "overloaded"}}
	rp := NewRetryProvider(f, testConfig(3, nil))

	_, err := rp.CreateMessage(context.Background(), CreateMessageRequest{})
	if err == nil {
		t.Fatal("expected an error once retries are exhausted")
	}
	if f.calls != 4 { // 1 initial + 3 retries
		t.Fatalf("expected 4 attempts, got %d", f.calls)
	}
	// The underlying 503 must still be reachable through the wrap.
	var se *statusError
	if !errors.As(err, &se) || se.Code != 503 {
		t.Fatalf("wrapped error lost the 503: %v", err)
	}
}

// TestRetry_FailsFastOnNonRetryable: a 400 is a client error; retrying it would
// waste the rate limit. The wrapper must return on the very first attempt.
func TestRetry_FailsFastOnNonRetryable(t *testing.T) {
	var delays []time.Duration
	f := &flakyProvider{failTimes: 99, failErr: &statusError{Code: 400, Body: "bad request"}}
	rp := NewRetryProvider(f, testConfig(5, &delays))

	_, err := rp.CreateMessage(context.Background(), CreateMessageRequest{})
	if err == nil {
		t.Fatal("a 400 should error")
	}
	if f.calls != 1 {
		t.Fatalf("400 must not be retried; got %d calls", f.calls)
	}
	if len(delays) != 0 {
		t.Fatalf("400 must not sleep; recorded delays: %v", delays)
	}
}

// TestRetry_JitterStaysInBounds: with jitter on, each delay must land in
// [base/2, base]. We don't assert an exact value (it's random) — only the range.
func TestRetry_JitterStaysInBounds(t *testing.T) {
	cfg := RetryConfig{Jitter: true, BaseDelay: 200 * time.Millisecond}
	for i := 0; i < 200; i++ {
		d := applyJitter(cfg, 200*time.Millisecond)
		if d < 100*time.Millisecond || d > 200*time.Millisecond {
			t.Fatalf("jittered delay %v out of [100ms,200ms]", d)
		}
	}
}
