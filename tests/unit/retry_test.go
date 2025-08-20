package unit

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/SeaSBee/go-patternx"
)

func TestRetrySuccess(t *testing.T) {
	policy := patternx.DefaultPolicy()
	attempts := 0

	err := patternx.Retry(policy, func() error {
		attempts++
		if attempts < 3 {
			return errors.New("temporary error")
		}
		return nil
	})

	if err != nil {
		t.Errorf("Expected no error after successful retry, got %v", err)
	}

	if attempts != 3 {
		t.Errorf("Expected 3 attempts, got %d", attempts)
	}
}

func TestRetryMaxAttemptsExceeded(t *testing.T) {
	policy := patternx.DefaultPolicy()
	expectedErr := errors.New("persistent error")

	err := patternx.Retry(policy, func() error {
		return expectedErr
	})

	if err == nil {
		t.Error("Expected error after max attempts exceeded, got nil")
	}

	if !strings.Contains(err.Error(), "retry max attempts exceeded") {
		t.Errorf("Expected error message to contain 'retry max attempts exceeded', got %v", err)
	}
}

func TestRetryWithContext(t *testing.T) {
	policy := patternx.DefaultPolicy()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := patternx.RetryWithContext(ctx, policy, func() error {
		time.Sleep(200 * time.Millisecond) // Longer than context timeout
		return errors.New("should timeout")
	})

	if err == nil {
		t.Error("Expected timeout error, got nil")
	}

	if !strings.Contains(err.Error(), "operation cancelled by context") {
		t.Errorf("Expected error message to contain 'operation cancelled by context', got %v", err)
	}
}

func TestRetryContextCancellation(t *testing.T) {
	policy := patternx.DefaultPolicy()
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	err := patternx.RetryWithContext(ctx, policy, func() error {
		return errors.New("should not execute")
	})

	if err == nil {
		t.Error("Expected context.Canceled error, got nil")
	}

	if !strings.Contains(err.Error(), "operation cancelled by context") {
		t.Errorf("Expected error message to contain 'operation cancelled by context', got %v", err)
	}
}

func TestRetryWithResult(t *testing.T) {
	policy := patternx.DefaultPolicy()
	attempts := 0

	result, err := patternx.RetryWithResult(policy, func() (string, error) {
		attempts++
		if attempts < 3 {
			return "", errors.New("temporary error")
		}
		return "success", nil
	})

	if err != nil {
		t.Errorf("Expected no error after successful retry, got %v", err)
	}

	if result != "success" {
		t.Errorf("Expected result 'success', got %s", result)
	}

	if attempts != 3 {
		t.Errorf("Expected 3 attempts, got %d", attempts)
	}
}

func TestRetryWithResultAndContext(t *testing.T) {
	policy := patternx.DefaultPolicy()
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	result, err := patternx.RetryWithResultAndContext(ctx, policy, func() (string, error) {
		time.Sleep(200 * time.Millisecond) // Longer than context timeout
		return "", errors.New("should timeout")
	})

	if err == nil {
		t.Error("Expected timeout error, got nil")
	}

	if result != "" {
		t.Errorf("Expected empty result on timeout, got %s", result)
	}
}

func TestRetryPolicyPresets(t *testing.T) {
	// Test default policy
	defaultPolicy := patternx.DefaultPolicy()
	if defaultPolicy.MaxAttempts != 3 {
		t.Errorf("Expected default MaxAttempts 3, got %d", defaultPolicy.MaxAttempts)
	}
	if defaultPolicy.InitialDelay != 100*time.Millisecond {
		t.Errorf("Expected default InitialDelay 100ms, got %v", defaultPolicy.InitialDelay)
	}
	if defaultPolicy.MaxDelay != 5*time.Second {
		t.Errorf("Expected default MaxDelay 5s, got %v", defaultPolicy.MaxDelay)
	}
	if defaultPolicy.Multiplier != 2.0 {
		t.Errorf("Expected default Multiplier 2.0, got %f", defaultPolicy.Multiplier)
	}
	if !defaultPolicy.Jitter {
		t.Error("Expected default Jitter true, got false")
	}

	// Test aggressive policy
	aggressivePolicy := patternx.AggressivePolicy()
	if aggressivePolicy.MaxAttempts != 5 {
		t.Errorf("Expected aggressive MaxAttempts 5, got %d", aggressivePolicy.MaxAttempts)
	}
	if aggressivePolicy.InitialDelay != 50*time.Millisecond {
		t.Errorf("Expected aggressive InitialDelay 50ms, got %v", aggressivePolicy.InitialDelay)
	}
	if aggressivePolicy.MaxDelay != 10*time.Second {
		t.Errorf("Expected aggressive MaxDelay 10s, got %v", aggressivePolicy.MaxDelay)
	}
	if aggressivePolicy.Multiplier != 1.5 {
		t.Errorf("Expected aggressive Multiplier 1.5, got %f", aggressivePolicy.Multiplier)
	}

	// Test conservative policy
	conservativePolicy := patternx.ConservativePolicy()
	if conservativePolicy.MaxAttempts != 2 {
		t.Errorf("Expected conservative MaxAttempts 2, got %d", conservativePolicy.MaxAttempts)
	}
	if conservativePolicy.InitialDelay != 500*time.Millisecond {
		t.Errorf("Expected conservative InitialDelay 500ms, got %v", conservativePolicy.InitialDelay)
	}
	if conservativePolicy.MaxDelay != 2*time.Second {
		t.Errorf("Expected conservative MaxDelay 2s, got %v", conservativePolicy.MaxDelay)
	}
}

func TestRetryNonRetryableError(t *testing.T) {
	policy := patternx.Policy{
		MaxAttempts:     3,
		InitialDelay:    10 * time.Millisecond,
		MaxDelay:        100 * time.Millisecond,
		Multiplier:      2.0,
		Jitter:          false,
		RetryableErrors: []error{errors.New("retryable")},
		RateLimit:       10,
		RateLimitWindow: time.Second,
	}

	nonRetryableErr := errors.New("non-retryable")
	attempts := 0

	err := patternx.Retry(policy, func() error {
		attempts++
		return nonRetryableErr
	})

	if err != nonRetryableErr {
		t.Errorf("Expected non-retryable error, got %v", err)
	}

	if attempts != 1 {
		t.Errorf("Expected 1 attempt for non-retryable error, got %d", attempts)
	}
}

func TestRetryRetryableError(t *testing.T) {
	retryableErr := errors.New("retryable")
	policy := patternx.Policy{
		MaxAttempts:     3,
		InitialDelay:    10 * time.Millisecond,
		MaxDelay:        100 * time.Millisecond,
		Multiplier:      2.0,
		Jitter:          false,
		RetryableErrors: []error{retryableErr},
		RateLimit:       10,
		RateLimitWindow: time.Second,
	}

	attempts := 0

	err := patternx.Retry(policy, func() error {
		attempts++
		return retryableErr
	})

	if err == nil {
		t.Error("Expected error after max attempts, got nil")
	}

	if attempts != 3 {
		t.Errorf("Expected 3 attempts for retryable error, got %d", attempts)
	}
}

func TestRetryDelayCalculation(t *testing.T) {
	policy := patternx.Policy{
		MaxAttempts:     3,
		InitialDelay:    10 * time.Millisecond,
		MaxDelay:        100 * time.Millisecond,
		Multiplier:      2.0,
		Jitter:          false,
		RateLimit:       10,
		RateLimitWindow: time.Second,
	}

	start := time.Now()
	attempts := 0

	err := patternx.Retry(policy, func() error {
		attempts++
		return errors.New("retry")
	})

	duration := time.Since(start)

	if err == nil {
		t.Error("Expected error after max attempts, got nil")
	}

	// Should have delays of 10ms and 20ms (total ~30ms)
	// Allow some tolerance for test execution
	if duration < 20*time.Millisecond || duration > 100*time.Millisecond {
		t.Errorf("Expected delay duration between 20ms and 100ms, got %v", duration)
	}
}

func TestRetryWithJitter(t *testing.T) {
	policy := patternx.Policy{
		MaxAttempts:     3,
		InitialDelay:    10 * time.Millisecond,
		MaxDelay:        100 * time.Millisecond,
		Multiplier:      2.0,
		Jitter:          true,
		RateLimit:       10,
		RateLimitWindow: time.Second,
	}

	start := time.Now()
	attempts := 0

	err := patternx.Retry(policy, func() error {
		attempts++
		return errors.New("retry")
	})

	duration := time.Since(start)

	if err == nil {
		t.Error("Expected error after max attempts, got nil")
	}

	// With jitter, duration should be somewhat variable but still reasonable
	if duration < 10*time.Millisecond || duration > 200*time.Millisecond {
		t.Errorf("Expected delay duration between 10ms and 200ms with jitter, got %v", duration)
	}
}

func TestRetryZeroMultiplier(t *testing.T) {
	policy := patternx.Policy{
		MaxAttempts:     3,
		InitialDelay:    10 * time.Millisecond,
		MaxDelay:        100 * time.Millisecond,
		Multiplier:      1.0, // Valid multiplier
		Jitter:          false,
		RateLimit:       10,
		RateLimitWindow: time.Second,
	}

	attempts := 0

	err := patternx.Retry(policy, func() error {
		attempts++
		return errors.New("retry")
	})

	if err == nil {
		t.Error("Expected error after max attempts, got nil")
	}

	if attempts != 3 {
		t.Errorf("Expected 3 attempts, got %d", attempts)
	}
}

func TestRetryImmediateSuccess(t *testing.T) {
	policy := patternx.DefaultPolicy()
	attempts := 0

	err := patternx.Retry(policy, func() error {
		attempts++
		return nil // Immediate success
	})

	if err != nil {
		t.Errorf("Expected no error on immediate success, got %v", err)
	}

	if attempts != 1 {
		t.Errorf("Expected 1 attempt for immediate success, got %d", attempts)
	}
}

func TestRetryWithResultImmediateSuccess(t *testing.T) {
	policy := patternx.DefaultPolicy()
	attempts := 0

	result, err := patternx.RetryWithResult(policy, func() (int, error) {
		attempts++
		return 42, nil // Immediate success
	})

	if err != nil {
		t.Errorf("Expected no error on immediate success, got %v", err)
	}

	if result != 42 {
		t.Errorf("Expected result 42, got %d", result)
	}

	if attempts != 1 {
		t.Errorf("Expected 1 attempt for immediate success, got %d", attempts)
	}
}
