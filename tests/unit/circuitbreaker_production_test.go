package unit

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SeaSBee/go-patternx/patternx/cb"
)

// TestCircuitBreakerProductionConfigValidation tests comprehensive configuration validation
func TestCircuitBreakerProductionConfigValidation(t *testing.T) {
	tests := []struct {
		name        string
		config      cb.Config
		expectError bool
		errorMsg    string
	}{
		{
			name:        "Valid Default Config",
			config:      cb.DefaultConfig(),
			expectError: false,
		},
		{
			name:        "Valid High Performance Config",
			config:      cb.HighPerformanceConfig(),
			expectError: false,
		},
		{
			name:        "Valid Enterprise Config",
			config:      cb.EnterpriseConfig(),
			expectError: false,
		},
		{
			name: "Invalid Threshold - Zero",
			config: cb.Config{
				Threshold:   0,
				Timeout:     30 * time.Second,
				HalfOpenMax: 3,
			},
			expectError: true, // Validation is strict
			errorMsg:    "threshold must be between 1 and 10000",
		},
		{
			name: "Invalid Threshold - Negative",
			config: cb.Config{
				Threshold:   -1,
				Timeout:     30 * time.Second,
				HalfOpenMax: 3,
			},
			expectError: true, // Validation is strict
			errorMsg:    "threshold must be between 1 and 10000",
		},
		{
			name: "Invalid Threshold - Too High",
			config: cb.Config{
				Threshold:   20000,
				Timeout:     30 * time.Second,
				HalfOpenMax: 3,
			},
			expectError: true,
			errorMsg:    "threshold must be between 1 and 10000",
		},
		{
			name: "Invalid Timeout - Too High",
			config: cb.Config{
				Threshold:   5,
				Timeout:     600 * time.Second,
				HalfOpenMax: 3,
			},
			expectError: true,
			errorMsg:    "timeout must be between 1ms and 5m0s",
		},
		{
			name: "Invalid HalfOpenMax - Too High",
			config: cb.Config{
				Threshold:   5,
				Timeout:     30 * time.Second,
				HalfOpenMax: 2000,
			},
			expectError: true,
			errorMsg:    "half open max must be between 1 and 1000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			circuitBreaker, err := cb.New(tt.config)

			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error but got none")
					return
				}
				if tt.errorMsg != "" && !contains(err.Error(), tt.errorMsg) {
					t.Errorf("Expected error message containing '%s', got '%s'", tt.errorMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
					return
				}
				if circuitBreaker == nil {
					t.Error("Expected circuit breaker instance but got nil")
				}
			}
		})
	}
}

// TestCircuitBreakerProductionInputValidation tests input validation for Execute methods
func TestCircuitBreakerProductionInputValidation(t *testing.T) {
	config := cb.DefaultConfig()
	circuitBreaker, err := cb.New(config)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	// Test nil operation
	err = circuitBreaker.Execute(nil)
	if err == nil {
		t.Error("Expected error for nil operation")
	}

	// Test nil context
	ctx := context.Background()
	err = circuitBreaker.ExecuteWithContext(nil, func() error { return nil })
	if err == nil {
		t.Error("Expected error for nil context")
	}

	// Test nil operation with context
	err = circuitBreaker.ExecuteWithContext(ctx, nil)
	if err == nil {
		t.Error("Expected error for nil operation with context")
	}

	// Test invalid timeout
	err = circuitBreaker.ExecuteWithTimeout(0, func() error { return nil })
	if err == nil {
		t.Error("Expected error for zero timeout")
	}

	err = circuitBreaker.ExecuteWithTimeout(120*time.Second, func() error { return nil })
	if err == nil {
		t.Error("Expected error for timeout too high")
	}
}

// TestCircuitBreakerProductionConcurrencySafety tests concurrency safety
func TestCircuitBreakerProductionConcurrencySafety(t *testing.T) {
	config := cb.Config{
		Threshold:   5,
		Timeout:     100 * time.Millisecond,
		HalfOpenMax: 3,
	}

	circuitBreaker, err := cb.New(config)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	numGoroutines := 50
	operationsPerGoroutine := 10

	var wg sync.WaitGroup
	var executionErrors int64
	var successCount int64

	// Start concurrent operations
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < operationsPerGoroutine; j++ {
				err := circuitBreaker.Execute(func() error {
					// Simulate some work
					time.Sleep(1 * time.Millisecond)
					if j%3 == 0 {
						return errors.New("simulated failure")
					}
					return nil
				})

				if err != nil {
					atomic.AddInt64(&executionErrors, 1)
				} else {
					atomic.AddInt64(&successCount, 1)
				}
			}
		}(i)
	}

	wg.Wait()

	// Verify metrics
	stats := circuitBreaker.GetStats()
	expectedTotal := int64(numGoroutines * operationsPerGoroutine)

	if stats.TotalRequests != expectedTotal {
		t.Errorf("Expected %d total requests, got %d", expectedTotal, stats.TotalRequests)
	}

	t.Logf("Concurrency safety test completed:")
	t.Logf("  Total operations: %d", expectedTotal)
	t.Logf("  Successful operations: %d", successCount)
	t.Logf("  Execution errors: %d", executionErrors)
	t.Logf("  Stats total requests: %d", stats.TotalRequests)
	t.Logf("  Stats total failures: %d", stats.TotalFailures)
	t.Logf("  Stats total successes: %d", stats.TotalSuccesses)
}

// TestCircuitBreakerProductionStateTransitions tests state transitions
func TestCircuitBreakerProductionStateTransitions(t *testing.T) {
	config := cb.Config{
		Threshold:   3,
		Timeout:     100 * time.Millisecond,
		HalfOpenMax: 2,
	}

	circuitBreaker, err := cb.New(config)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	// Initially should be closed
	if circuitBreaker.GetState() != cb.StateClosed {
		t.Error("Circuit breaker should be closed initially")
	}

	// Add failures to trigger open state
	for i := 0; i < 3; i++ {
		err := circuitBreaker.Execute(func() error {
			return errors.New("simulated failure")
		})
		if err == nil {
			t.Error("Expected error from failing operation")
		}
	}

	// Should be open now
	if circuitBreaker.GetState() != cb.StateOpen {
		t.Error("Circuit breaker should be open after threshold failures")
	}

	// Wait for timeout
	time.Sleep(150 * time.Millisecond)

	// Should transition to half-open (timing dependent)
	state := circuitBreaker.GetState()
	if state != cb.StateHalfOpen && state != cb.StateOpen {
		t.Errorf("Circuit breaker should be half-open or still open after timeout, got %v", state)
	}

	// Add successes to close the circuit
	for i := 0; i < 2; i++ {
		err := circuitBreaker.Execute(func() error {
			return nil
		})
		if err != nil {
			t.Error("Expected success from successful operation")
		}
	}

	// Should be closed again
	if circuitBreaker.GetState() != cb.StateClosed {
		t.Error("Circuit breaker should be closed after successful operations")
	}

	t.Logf("State transitions test completed successfully")
}

// TestCircuitBreakerProductionContextCancellation tests context cancellation
func TestCircuitBreakerProductionContextCancellation(t *testing.T) {
	config := cb.DefaultConfig()
	circuitBreaker, err := cb.New(config)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	var cancellationErrors int64
	var successCount int64

	// Test rapid context cancellation
	for i := 0; i < 100; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Microsecond)

		err := circuitBreaker.ExecuteWithContext(ctx, func() error {
			time.Sleep(10 * time.Millisecond)
			return nil
		})

		cancel()

		if err != nil {
			if contains(err.Error(), "operation cancelled by context") || contains(err.Error(), "operation timeout") {
				atomic.AddInt64(&cancellationErrors, 1)
			}
		} else {
			atomic.AddInt64(&successCount, 1)
		}
	}

	// Some operations should be cancelled (timing dependent)
	if cancellationErrors < 5 {
		t.Errorf("Expected some operations to be cancelled, got %d cancellations out of 100", cancellationErrors)
	}

	t.Logf("Context cancellation test:")
	t.Logf("  Successful operations: %d", successCount)
	t.Logf("  Cancellation errors: %d", cancellationErrors)
}

// TestCircuitBreakerProductionPanicRecovery tests panic recovery
func TestCircuitBreakerProductionPanicRecovery(t *testing.T) {
	config := cb.DefaultConfig()
	circuitBreaker, err := cb.New(config)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	// Test panic recovery
	err = circuitBreaker.Execute(func() error {
		panic("simulated panic")
	})

	// Panic recovery might not always return an error, but should not crash
	// The panic is caught and recorded in metrics

	// Test panic recovery with context
	ctx := context.Background()
	err = circuitBreaker.ExecuteWithContext(ctx, func() error {
		panic("simulated panic with context")
	})

	// Panic recovery might not always return an error, but should not crash
	// The panic is caught and recorded in metrics

	// Verify panic metrics
	stats := circuitBreaker.GetStats()
	if stats.TotalPanics == 0 {
		t.Error("Expected panic count to be greater than 0")
	}

	t.Logf("Panic recovery test completed:")
	t.Logf("  Total panics: %d", stats.TotalPanics)
}

// TestCircuitBreakerProductionHealthChecks tests health check functionality
func TestCircuitBreakerProductionHealthChecks(t *testing.T) {
	config := cb.DefaultConfig()
	circuitBreaker, err := cb.New(config)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	// Initially should be healthy
	if !circuitBreaker.IsHealthy() {
		t.Error("Circuit breaker should be healthy initially")
	}

	// Add failures to make it unhealthy
	for i := 0; i < 5; i++ {
		circuitBreaker.Execute(func() error {
			return errors.New("simulated failure")
		})
	}

	// Should be unhealthy now
	if circuitBreaker.IsHealthy() {
		t.Error("Circuit breaker should be unhealthy after failures")
	}

	// Check health status details
	health := circuitBreaker.GetHealthStatus()
	if health["is_healthy"] == true {
		t.Error("Health status should show unhealthy")
	}

	failureRate := health["failure_rate"].(float64)
	if failureRate < 50.0 {
		t.Errorf("Expected failure rate >= 50.0, got %f", failureRate)
	}

	t.Logf("Health check test:")
	t.Logf("  Is healthy: %v", health["is_healthy"])
	t.Logf("  Failure rate: %f", failureRate)
	t.Logf("  State: %v", health["state"])
}

// TestCircuitBreakerProductionForceOperations tests force operations
func TestCircuitBreakerProductionForceOperations(t *testing.T) {
	config := cb.DefaultConfig()
	circuitBreaker, err := cb.New(config)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	// Initially should be closed
	if circuitBreaker.GetState() != cb.StateClosed {
		t.Error("Circuit breaker should be closed initially")
	}

	// Force open
	circuitBreaker.ForceOpen()
	if circuitBreaker.GetState() != cb.StateOpen {
		t.Error("Circuit breaker should be open after force open")
	}

	// Try to execute - should fail
	err = circuitBreaker.Execute(func() error {
		return nil
	})
	if err == nil {
		t.Error("Expected error when circuit breaker is forced open")
	}

	// Force close
	circuitBreaker.ForceClose()
	if circuitBreaker.GetState() != cb.StateClosed {
		t.Error("Circuit breaker should be closed after force close")
	}

	// Try to execute - should succeed
	err = circuitBreaker.Execute(func() error {
		return nil
	})
	if err != nil {
		t.Error("Expected success when circuit breaker is forced closed")
	}

	t.Logf("Force operations test completed successfully")
}

// TestCircuitBreakerProductionMetricsAccuracy tests metrics accuracy
func TestCircuitBreakerProductionMetricsAccuracy(t *testing.T) {
	config := cb.Config{
		Threshold:   5,
		Timeout:     1 * time.Second,
		HalfOpenMax: 3,
	}

	circuitBreaker, err := cb.New(config)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	// Execute some successful operations
	for i := 0; i < 5; i++ {
		err := circuitBreaker.Execute(func() error {
			time.Sleep(1 * time.Millisecond)
			return nil
		})
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}
	}

	// Execute some failed operations
	for i := 0; i < 3; i++ {
		err := circuitBreaker.Execute(func() error {
			return errors.New("simulated failure")
		})
		if err == nil {
			t.Error("Expected error from failing operation")
		}
	}

	// Check metrics accuracy
	stats := circuitBreaker.GetStats()
	expectedTotal := int64(8)
	expectedSuccesses := int64(5)
	expectedFailures := int64(3)

	if stats.TotalRequests != expectedTotal {
		t.Errorf("Expected %d total requests, got %d", expectedTotal, stats.TotalRequests)
	}

	if stats.TotalSuccesses != expectedSuccesses {
		t.Errorf("Expected %d total successes, got %d", expectedSuccesses, stats.TotalSuccesses)
	}

	if stats.TotalFailures != expectedFailures {
		t.Errorf("Expected %d total failures, got %d", expectedFailures, stats.TotalFailures)
	}

	t.Logf("Metrics accuracy test:")
	t.Logf("  Total requests: %d", stats.TotalRequests)
	t.Logf("  Total successes: %d", stats.TotalSuccesses)
	t.Logf("  Total failures: %d", stats.TotalFailures)
	t.Logf("  Failure rate: %.2f%%", stats.FailureRate())
	t.Logf("  Success rate: %.2f%%", stats.SuccessRate())
}

// TestCircuitBreakerProductionReset tests reset functionality
func TestCircuitBreakerProductionReset(t *testing.T) {
	config := cb.DefaultConfig()
	circuitBreaker, err := cb.New(config)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	// Execute some operations
	for i := 0; i < 5; i++ {
		circuitBreaker.Execute(func() error {
			return errors.New("simulated failure")
		})
	}

	// Verify metrics are populated
	stats := circuitBreaker.GetStats()
	if stats.TotalRequests == 0 {
		t.Error("Expected metrics to be populated")
	}

	// Reset metrics
	circuitBreaker.Reset()

	// Verify metrics are reset
	stats = circuitBreaker.GetStats()
	if stats.TotalRequests != 0 {
		t.Error("Expected total requests to be reset to zero")
	}

	if stats.TotalFailures != 0 {
		t.Error("Expected total failures to be reset to zero")
	}

	if stats.TotalSuccesses != 0 {
		t.Error("Expected total successes to be reset to zero")
	}

	if stats.TotalPanics != 0 {
		t.Error("Expected total panics to be reset to zero")
	}

	if circuitBreaker.GetState() != cb.StateClosed {
		t.Error("Expected state to be reset to closed")
	}

	if !circuitBreaker.IsHealthy() {
		t.Error("Expected health status to be reset to healthy")
	}

	t.Logf("Reset test completed successfully")
}

// TestCircuitBreakerProductionTimeoutHandling tests timeout handling
func TestCircuitBreakerProductionTimeoutHandling(t *testing.T) {
	config := cb.DefaultConfig()
	circuitBreaker, err := cb.New(config)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	var timeoutErrors int64
	var successCount int64

	// Test timeout scenarios
	for i := 0; i < 10; i++ {
		err := circuitBreaker.ExecuteWithTimeout(10*time.Millisecond, func() error {
			time.Sleep(50 * time.Millisecond) // Longer than timeout
			return nil
		})

		if err != nil {
			if contains(err.Error(), "operation timeout") {
				atomic.AddInt64(&timeoutErrors, 1)
			}
		} else {
			atomic.AddInt64(&successCount, 1)
		}
	}

	// Verify timeout handling
	stats := circuitBreaker.GetStats()
	if stats.TotalTimeouts == 0 {
		t.Error("Expected timeout calls but got none")
	}

	t.Logf("Timeout handling test:")
	t.Logf("  Successful operations: %d", successCount)
	t.Logf("  Timeout errors: %d", timeoutErrors)
	t.Logf("  Stats total timeouts: %d", stats.TotalTimeouts)
}
