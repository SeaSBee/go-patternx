package unit

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/SeaSBee/go-patternx/patternx/cb"
)

func TestNewCircuitBreaker(t *testing.T) {
	config := cb.Config{
		Threshold:   5,
		Timeout:     30 * time.Second,
		HalfOpenMax: 3,
	}
	breaker, err := cb.New(config)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	if breaker == nil {
		t.Fatal("Expected circuit breaker to be created")
	}

	// Test that the circuit breaker works
	execErr := breaker.Execute(func() error {
		return nil
	})

	if execErr != nil {
		t.Errorf("Expected no error, got %v", execErr)
	}
}

func TestCircuitBreakerExecuteSuccess(t *testing.T) {
	config := cb.Config{
		Threshold:   3,
		Timeout:     1 * time.Second,
		HalfOpenMax: 2,
	}
	breaker, err := cb.New(config)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	// Execute successful operation
	execErr := breaker.Execute(func() error {
		return nil
	})

	if execErr != nil {
		t.Errorf("Expected no error, got %v", execErr)
	}

	stats := breaker.GetStats()
	if stats.State != cb.StateClosed {
		t.Errorf("Expected state CLOSED, got %s", stats.State)
	}
	if stats.TotalRequests != 1 {
		t.Errorf("Expected 1 total request, got %d", stats.TotalRequests)
	}
	if stats.TotalSuccesses != 1 {
		t.Errorf("Expected 1 total success, got %d", stats.TotalSuccesses)
	}
}

func TestCircuitBreakerExecuteFailure(t *testing.T) {
	config := cb.Config{
		Threshold:   3,
		Timeout:     1 * time.Second,
		HalfOpenMax: 2,
	}
	breaker, err := cb.New(config)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	expectedErr := errors.New("test error")

	// Execute failing operation
	execErr := breaker.Execute(func() error {
		return expectedErr
	})

	if execErr != expectedErr {
		t.Errorf("Expected error %v, got %v", expectedErr, execErr)
	}

	stats := breaker.GetStats()
	if stats.State != cb.StateClosed {
		t.Errorf("Expected state CLOSED, got %s", stats.State)
	}
	if stats.TotalRequests != 1 {
		t.Errorf("Expected 1 total request, got %d", stats.TotalRequests)
	}
	if stats.TotalFailures != 1 {
		t.Errorf("Expected 1 total failure, got %d", stats.TotalFailures)
	}
}

func TestCircuitBreakerStateTransitions(t *testing.T) {
	config := cb.Config{
		Threshold:   2,
		Timeout:     100 * time.Millisecond,
		HalfOpenMax: 2,
	}
	breaker, err := cb.New(config)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	// Start in CLOSED state
	if breaker.GetState() != cb.StateClosed {
		t.Errorf("Expected initial state CLOSED, got %s", breaker.GetState())
	}

	// Fail twice to open the circuit
	for i := 0; i < 2; i++ {
		breaker.Execute(func() error {
			return errors.New("failure")
		})
	}

	// Circuit should be OPEN
	if breaker.GetState() != cb.StateOpen {
		t.Errorf("Expected state OPEN after failures, got %s", breaker.GetState())
	}

	// Try to execute - should be rejected
	execErr := breaker.Execute(func() error {
		return nil
	})

	if execErr == nil || execErr.Error() != "circuit breaker is open" {
		t.Errorf("Expected 'circuit breaker is open' error, got %v", execErr)
	}

	// Wait for timeout
	time.Sleep(150 * time.Millisecond)

	// Circuit should transition to HALF_OPEN (timing dependent)
	state := breaker.GetState()
	if state != cb.StateHalfOpen && state != cb.StateOpen {
		t.Errorf("Expected state HALF_OPEN or OPEN after timeout, got %s", state)
	}

	// Try a successful operation
	execErr = breaker.Execute(func() error {
		return nil
	})

	if execErr != nil {
		t.Errorf("Expected no error in half-open state, got %v", execErr)
	}

	// Circuit should be CLOSED again (timing dependent)
	state = breaker.GetState()
	if state != cb.StateClosed && state != cb.StateHalfOpen {
		t.Errorf("Expected state CLOSED or HALF_OPEN after success, got %s", state)
	}
}

func TestCircuitBreakerExecuteWithContext(t *testing.T) {
	config := cb.Config{
		Threshold:   3,
		Timeout:     1 * time.Second,
		HalfOpenMax: 2,
	}
	breaker, err := cb.New(config)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Execute with context
	execErr := breaker.ExecuteWithContext(ctx, func() error {
		time.Sleep(200 * time.Millisecond) // Longer than context timeout
		return nil
	})

	if execErr == nil {
		t.Error("Expected timeout error, got nil")
	}

	stats := breaker.GetStats()
	if stats.TotalTimeouts != 1 {
		t.Errorf("Expected 1 timeout, got %d", stats.TotalTimeouts)
	}
}

func TestCircuitBreakerExecuteWithTimeout(t *testing.T) {
	config := cb.Config{
		Threshold:   3,
		Timeout:     1 * time.Second,
		HalfOpenMax: 2,
	}
	breaker, err := cb.New(config)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	// Execute with timeout
	execErr := breaker.ExecuteWithTimeout(50*time.Millisecond, func() error {
		time.Sleep(100 * time.Millisecond) // Longer than timeout
		return nil
	})

	if execErr == nil {
		t.Error("Expected timeout error, got nil")
	}

	stats := breaker.GetStats()
	if stats.TotalTimeouts != 1 {
		t.Errorf("Expected 1 timeout, got %d", stats.TotalTimeouts)
	}
}

func TestCircuitBreakerContextCancellation(t *testing.T) {
	config := cb.Config{
		Threshold:   3,
		Timeout:     1 * time.Second,
		HalfOpenMax: 2,
	}
	breaker, err := cb.New(config)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	// Execute with cancelled context
	execErr := breaker.ExecuteWithContext(ctx, func() error {
		return nil
	})

	if execErr == nil {
		t.Error("Expected context.Canceled error, got nil")
	}
}

func TestCircuitBreakerHalfOpenMaxLimit(t *testing.T) {
	config := cb.Config{
		Threshold:   1,
		Timeout:     50 * time.Millisecond,
		HalfOpenMax: 2,
	}
	breaker, err := cb.New(config)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	// Fail once to open circuit
	breaker.Execute(func() error {
		return errors.New("failure")
	})

	// Wait for timeout to enter half-open state
	time.Sleep(100 * time.Millisecond)

	// Try 3 operations in half-open state (max is 2)
	for i := 0; i < 3; i++ {
		err := breaker.Execute(func() error {
			return errors.New("failure")
		})

		if i < 2 {
			// First two should be allowed
			if err == nil {
				t.Errorf("Expected error for attempt %d, got nil", i+1)
			}
		} else {
			// Third should be rejected
			if err == nil || err.Error() != "circuit breaker is open" {
				t.Errorf("Expected 'circuit breaker is open' error for attempt %d, got %v", i+1, err)
			}
		}
	}
}

func TestCircuitBreakerConfigValidation(t *testing.T) {
	// Test with valid default values
	config := cb.DefaultConfig()
	breaker, err := cb.New(config)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	// Should work with default values
	execErr := breaker.Execute(func() error {
		return nil
	})

	if execErr != nil {
		t.Errorf("Expected no error with default config, got %v", execErr)
	}

	stats := breaker.GetStats()
	if stats.Config.Threshold != 5 {
		t.Errorf("Expected default threshold 5, got %d", stats.Config.Threshold)
	}
	if stats.Config.Timeout != 30*time.Second {
		t.Errorf("Expected default timeout 30s, got %v", stats.Config.Timeout)
	}
	if stats.Config.HalfOpenMax != 3 {
		t.Errorf("Expected default half-open max 3, got %d", stats.Config.HalfOpenMax)
	}
}

func TestCircuitBreakerGetStats(t *testing.T) {
	config := cb.Config{
		Threshold:   2,
		Timeout:     1 * time.Second,
		HalfOpenMax: 2,
	}
	breaker, err := cb.New(config)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	// Execute some operations
	breaker.Execute(func() error {
		return nil
	})

	breaker.Execute(func() error {
		return errors.New("failure")
	})

	stats := breaker.GetStats()

	if stats.TotalRequests != 2 {
		t.Errorf("Expected 2 total requests, got %d", stats.TotalRequests)
	}
	if stats.TotalSuccesses != 1 {
		t.Errorf("Expected 1 total success, got %d", stats.TotalSuccesses)
	}
	if stats.TotalFailures != 1 {
		t.Errorf("Expected 1 total failure, got %d", stats.TotalFailures)
	}
	if stats.State != cb.StateClosed {
		t.Errorf("Expected state CLOSED, got %s", stats.State)
	}
}

func TestCircuitBreakerConcurrentAccess(t *testing.T) {
	config := cb.Config{
		Threshold:   10,
		Timeout:     1 * time.Second,
		HalfOpenMax: 5,
	}
	breaker, err := cb.New(config)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	// Test concurrent access
	var wg sync.WaitGroup
	numGoroutines := 50

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// Execute operation
			breaker.Execute(func() error {
				return nil
			})

			// Check state
			_ = breaker.GetState()

			// Get stats
			_ = breaker.GetStats()
		}()
	}

	wg.Wait()

	// Verify final state
	stats := breaker.GetStats()
	if stats.TotalRequests != int64(numGoroutines) {
		t.Errorf("Expected %d total requests, got %d", numGoroutines, stats.TotalRequests)
	}
}
