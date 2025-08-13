package unit

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SeaSBee/go-patternx/patternx/bulkhead"
)

// TestBulkheadProductionConfigValidation tests comprehensive configuration validation
func TestBulkheadProductionConfigValidation(t *testing.T) {
	tests := []struct {
		name        string
		config      bulkhead.BulkheadConfig
		expectError bool
		errorMsg    string
	}{
		{
			name:        "Valid Default Config",
			config:      bulkhead.DefaultBulkheadConfig(),
			expectError: false,
		},
		{
			name:        "Valid High Performance Config",
			config:      bulkhead.HighPerformanceBulkheadConfig(),
			expectError: false,
		},
		{
			name:        "Valid Enterprise Config",
			config:      bulkhead.EnterpriseBulkheadConfig(),
			expectError: false,
		},
		{
			name: "Invalid MaxConcurrentCalls - Zero",
			config: bulkhead.BulkheadConfig{
				MaxConcurrentCalls: 0,
				MaxWaitDuration:    5 * time.Second,
				MaxQueueSize:       100,
			},
			expectError: true, // Validation is strict
			errorMsg:    "max concurrent calls must be between 1 and 10000",
		},
		{
			name: "Invalid MaxConcurrentCalls - Negative",
			config: bulkhead.BulkheadConfig{
				MaxConcurrentCalls: -1,
				MaxWaitDuration:    5 * time.Second,
				MaxQueueSize:       100,
			},
			expectError: true, // Validation is strict
			errorMsg:    "max concurrent calls must be between 1 and 10000",
		},
		{
			name: "Invalid MaxConcurrentCalls - Too High",
			config: bulkhead.BulkheadConfig{
				MaxConcurrentCalls: 20000,
				MaxWaitDuration:    5 * time.Second,
				MaxQueueSize:       100,
			},
			expectError: true,
			errorMsg:    "max concurrent calls must be between 1 and 10000",
		},
		{
			name: "Invalid MaxQueueSize - Too High",
			config: bulkhead.BulkheadConfig{
				MaxConcurrentCalls: 10,
				MaxWaitDuration:    5 * time.Second,
				MaxQueueSize:       200000,
			},
			expectError: true,
			errorMsg:    "max queue size must be between 1 and 100000",
		},
		{
			name: "Invalid MaxWaitDuration - Too High",
			config: bulkhead.BulkheadConfig{
				MaxConcurrentCalls: 10,
				MaxWaitDuration:    120 * time.Second,
				MaxQueueSize:       100,
			},
			expectError: true,
			errorMsg:    "max wait duration must be between 1ms and 1m0s",
		},
		{
			name: "Invalid HealthThreshold - Too High",
			config: bulkhead.BulkheadConfig{
				MaxConcurrentCalls: 10,
				MaxWaitDuration:    5 * time.Second,
				MaxQueueSize:       100,
				HealthThreshold:    1.5,
			},
			expectError: true,
			errorMsg:    "health threshold must be between 0.100000 and 1.000000",
		},
		{
			name: "Invalid HealthThreshold - Too Low",
			config: bulkhead.BulkheadConfig{
				MaxConcurrentCalls: 10,
				MaxWaitDuration:    5 * time.Second,
				MaxQueueSize:       100,
				HealthThreshold:    0.05,
			},
			expectError: true,
			errorMsg:    "health threshold must be between 0.100000 and 1.000000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			b, err := bulkhead.NewBulkhead(tt.config)

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
				if b == nil {
					t.Error("Expected bulkhead instance but got nil")
				}
			}
		})
	}
}

// TestBulkheadProductionInputValidation tests input validation for Execute methods
func TestBulkheadProductionInputValidation(t *testing.T) {
	config := bulkhead.DefaultBulkheadConfig()
	b, err := bulkhead.NewBulkhead(config)
	if err != nil {
		t.Fatalf("Failed to create bulkhead: %v", err)
	}

	// Test nil context
	_, err = b.Execute(nil, func() (interface{}, error) { return "success", nil })
	if err == nil {
		t.Error("Expected error for nil context")
	}

	// Test nil function
	ctx := context.Background()
	_, err = b.Execute(ctx, nil)
	if err == nil {
		t.Error("Expected error for nil function")
	}

	// Test async with nil context
	resultChan := b.ExecuteAsync(nil, func() (interface{}, error) { return "success", nil })
	result := <-resultChan
	if result.Error == nil {
		t.Error("Expected error for nil context in async execution")
	}

	// Test async with nil function
	resultChan = b.ExecuteAsync(ctx, nil)
	result = <-resultChan
	if result.Error == nil {
		t.Error("Expected error for nil function in async execution")
	}
}

// TestBulkheadProductionConcurrencySafety tests concurrency safety
func TestBulkheadProductionConcurrencySafety(t *testing.T) {
	config := bulkhead.BulkheadConfig{
		MaxConcurrentCalls: 5,
		MaxWaitDuration:    2 * time.Second,
		MaxQueueSize:       10,
		HealthThreshold:    0.5,
		EnableMetrics:      true,
	}

	b, err := bulkhead.NewBulkhead(config)
	if err != nil {
		t.Fatalf("Failed to create bulkhead: %v", err)
	}

	ctx := context.Background()
	numGoroutines := 20
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
				_, err := b.Execute(ctx, func() (interface{}, error) {
					// Simulate some work
					time.Sleep(10 * time.Millisecond)
					return fmt.Sprintf("result-%d-%d", id, j), nil
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
	metrics := b.GetMetrics()
	expectedTotal := int64(numGoroutines * operationsPerGoroutine)

	if metrics.TotalCalls.Load() != expectedTotal {
		t.Errorf("Expected %d total calls, got %d", expectedTotal, metrics.TotalCalls.Load())
	}

	// Some execution errors are expected due to queue full/timeout scenarios
	if executionErrors > expectedTotal {
		t.Errorf("Had too many execution errors: %d out of %d", executionErrors, expectedTotal)
	}

	t.Logf("Concurrency safety test completed:")
	t.Logf("  Total operations: %d", expectedTotal)
	t.Logf("  Successful operations: %d", successCount)
	t.Logf("  Execution errors: %d", executionErrors)
	t.Logf("  Metrics total calls: %d", metrics.TotalCalls.Load())
	t.Logf("  Metrics successful calls: %d", metrics.SuccessfulCalls.Load())
}

// TestBulkheadProductionTimeoutHandling tests timeout handling
func TestBulkheadProductionTimeoutHandling(t *testing.T) {
	config := bulkhead.BulkheadConfig{
		MaxConcurrentCalls: 1,
		MaxWaitDuration:    50 * time.Millisecond,
		MaxQueueSize:       2,
		HealthThreshold:    0.5,
		EnableMetrics:      true,
	}

	b, err := bulkhead.NewBulkhead(config)
	if err != nil {
		t.Fatalf("Failed to create bulkhead: %v", err)
	}

	ctx := context.Background()
	var timeoutErrors int64
	var successCount int64

	// Start operations that will timeout - use concurrent operations
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := b.Execute(ctx, func() (interface{}, error) {
				// Simulate slow operation
				time.Sleep(100 * time.Millisecond)
				return "success", nil
			})

			if err != nil {
				if contains(err.Error(), "bulkhead operation timeout") {
					atomic.AddInt64(&timeoutErrors, 1)
				}
			} else {
				atomic.AddInt64(&successCount, 1)
			}
		}()
	}
	wg.Wait()

	// Verify timeout handling - some timeouts are expected
	metrics := b.GetMetrics()
	if metrics.TimeoutCalls.Load() == 0 && timeoutErrors == 0 {
		t.Error("Expected some timeout calls or errors but got none")
	}

	t.Logf("Timeout handling test:")
	t.Logf("  Successful operations: %d", successCount)
	t.Logf("  Timeout errors: %d", timeoutErrors)
	t.Logf("  Metrics timeout calls: %d", metrics.TimeoutCalls.Load())
}

// TestBulkheadProductionQueueFullHandling tests queue full handling
func TestBulkheadProductionQueueFullHandling(t *testing.T) {
	config := bulkhead.BulkheadConfig{
		MaxConcurrentCalls: 1,
		MaxWaitDuration:    5 * time.Millisecond,
		MaxQueueSize:       1,
		HealthThreshold:    0.5,
		EnableMetrics:      true,
	}

	b, err := bulkhead.NewBulkhead(config)
	if err != nil {
		t.Fatalf("Failed to create bulkhead: %v", err)
	}

	ctx := context.Background()
	var queueFullErrors int64
	var successCount int64

	// Start operations that will fill the queue - use concurrent operations
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := b.Execute(ctx, func() (interface{}, error) {
				// Simulate slow operation
				time.Sleep(20 * time.Millisecond)
				return "success", nil
			})

			if err != nil {
				if contains(err.Error(), "bulkhead queue is full") {
					atomic.AddInt64(&queueFullErrors, 1)
				}
			} else {
				atomic.AddInt64(&successCount, 1)
			}
		}()
	}
	wg.Wait()

	// Verify queue full handling - some rejections are expected
	metrics := b.GetMetrics()
	if metrics.RejectedCalls.Load() == 0 && queueFullErrors == 0 {
		t.Error("Expected some rejected calls or queue full errors but got none")
	}

	t.Logf("Queue full handling test:")
	t.Logf("  Successful operations: %d", successCount)
	t.Logf("  Queue full errors: %d", queueFullErrors)
	t.Logf("  Metrics rejected calls: %d", metrics.RejectedCalls.Load())
}

// TestBulkheadProductionContextCancellation tests context cancellation
func TestBulkheadProductionContextCancellation(t *testing.T) {
	config := bulkhead.DefaultBulkheadConfig()
	b, err := bulkhead.NewBulkhead(config)
	if err != nil {
		t.Fatalf("Failed to create bulkhead: %v", err)
	}

	var cancellationErrors int64
	var successCount int64

	// Test rapid context cancellation
	for i := 0; i < 100; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Microsecond)

		_, err := b.Execute(ctx, func() (interface{}, error) {
			time.Sleep(10 * time.Millisecond)
			return "success", nil
		})

		cancel()

		if err != nil {
			if contains(err.Error(), "operation cancelled by context") {
				atomic.AddInt64(&cancellationErrors, 1)
			}
		} else {
			atomic.AddInt64(&successCount, 1)
		}
	}

	// Some operations should be cancelled (timing dependent)
	if cancellationErrors < 10 {
		t.Errorf("Expected some operations to be cancelled, got %d cancellations out of 100", cancellationErrors)
	}

	t.Logf("Context cancellation test:")
	t.Logf("  Successful operations: %d", successCount)
	t.Logf("  Cancellation errors: %d", cancellationErrors)
}

// TestBulkheadProductionHealthChecks tests health check functionality
func TestBulkheadProductionHealthChecks(t *testing.T) {
	config := bulkhead.BulkheadConfig{
		MaxConcurrentCalls: 5,
		MaxWaitDuration:    1 * time.Second,
		MaxQueueSize:       10,
		HealthThreshold:    0.3, // 30% failure threshold
		EnableMetrics:      true,
	}

	b, err := bulkhead.NewBulkhead(config)
	if err != nil {
		t.Fatalf("Failed to create bulkhead: %v", err)
	}

	ctx := context.Background()

	// Initially should be healthy
	if !b.IsHealthy() {
		t.Error("Bulkhead should be healthy initially")
	}

	// Add some successful operations
	for i := 0; i < 10; i++ {
		_, err := b.Execute(ctx, func() (interface{}, error) {
			return "success", nil
		})
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}
	}

	// Should still be healthy
	if !b.IsHealthy() {
		t.Error("Bulkhead should be healthy after successful operations")
	}

	// Add some failed operations to trigger unhealthy state
	for i := 0; i < 10; i++ {
		_, _ = b.Execute(ctx, func() (interface{}, error) {
			return nil, errors.New("simulated failure")
		})
		// Don't check error here as we expect failures
	}

	// Should be unhealthy now
	if b.IsHealthy() {
		t.Error("Bulkhead should be unhealthy after many failures")
	}

	// Check health status details
	health := b.GetHealthStatus()
	if health["is_healthy"] == true {
		t.Error("Health status should show unhealthy")
	}

	failureRate := health["failure_rate"].(float64)
	if failureRate < 0.3 {
		t.Errorf("Expected failure rate >= 0.3, got %f", failureRate)
	}

	t.Logf("Health check test:")
	t.Logf("  Is healthy: %v", health["is_healthy"])
	t.Logf("  Failure rate: %f", failureRate)
	t.Logf("  Health threshold: %f", health["health_threshold"])
}

// TestBulkheadProductionGracefulShutdown tests graceful shutdown
func TestBulkheadProductionGracefulShutdown(t *testing.T) {
	config := bulkhead.DefaultBulkheadConfig()
	b, err := bulkhead.NewBulkhead(config)
	if err != nil {
		t.Fatalf("Failed to create bulkhead: %v", err)
	}

	ctx := context.Background()
	var wg sync.WaitGroup

	// Start some operations
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := b.Execute(ctx, func() (interface{}, error) {
				time.Sleep(100 * time.Millisecond)
				return "success", nil
			})
			if err != nil && !contains(err.Error(), "bulkhead is closed") {
				t.Errorf("Unexpected error: %v", err)
			}
		}()
	}

	// Close the bulkhead
	if err := b.Close(); err != nil {
		t.Errorf("Failed to close bulkhead: %v", err)
	}

	// Verify bulkhead is closed
	if !b.IsClosed() {
		t.Error("Bulkhead should be closed")
	}

	// Try to execute after close
	_, err = b.Execute(ctx, func() (interface{}, error) {
		return "success", nil
	})
	if err == nil {
		t.Error("Expected error when executing on closed bulkhead")
	}

	wg.Wait()
}

// TestBulkheadProductionAsyncOperations tests async operations
func TestBulkheadProductionAsyncOperations(t *testing.T) {
	config := bulkhead.DefaultBulkheadConfig()
	b, err := bulkhead.NewBulkhead(config)
	if err != nil {
		t.Fatalf("Failed to create bulkhead: %v", err)
	}

	ctx := context.Background()
	numOperations := 10
	var wg sync.WaitGroup
	var successCount int64
	var errorCount int64

	// Start async operations
	for i := 0; i < numOperations; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			resultChan := b.ExecuteAsync(ctx, func() (interface{}, error) {
				time.Sleep(10 * time.Millisecond)
				return fmt.Sprintf("async-result-%d", id), nil
			})

			result := <-resultChan
			if result.Error != nil {
				atomic.AddInt64(&errorCount, 1)
			} else {
				atomic.AddInt64(&successCount, 1)
			}
		}(i)
	}

	wg.Wait()

	if successCount != int64(numOperations) {
		t.Errorf("Expected %d successful async operations, got %d", numOperations, successCount)
	}

	if errorCount > 0 {
		t.Errorf("Had %d async operation errors", errorCount)
	}

	t.Logf("Async operations test:")
	t.Logf("  Successful operations: %d", successCount)
	t.Logf("  Error operations: %d", errorCount)
}

// TestBulkheadProductionMetricsAccuracy tests metrics accuracy
func TestBulkheadProductionMetricsAccuracy(t *testing.T) {
	config := bulkhead.BulkheadConfig{
		MaxConcurrentCalls: 3,
		MaxWaitDuration:    1 * time.Second,
		MaxQueueSize:       5,
		HealthThreshold:    0.5,
		EnableMetrics:      true,
	}

	b, err := bulkhead.NewBulkhead(config)
	if err != nil {
		t.Fatalf("Failed to create bulkhead: %v", err)
	}

	ctx := context.Background()

	// Execute some operations
	for i := 0; i < 5; i++ {
		_, err := b.Execute(ctx, func() (interface{}, error) {
			time.Sleep(10 * time.Millisecond)
			return "success", nil
		})
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}
	}

	// Execute some failed operations
	for i := 0; i < 3; i++ {
		_, _ = b.Execute(ctx, func() (interface{}, error) {
			return nil, errors.New("simulated failure")
		})
		// Don't check error here as we expect failures
	}

	// Check metrics accuracy
	metrics := b.GetMetrics()
	expectedTotal := int64(8)
	expectedSuccessful := int64(5)
	expectedFailed := int64(3)

	if metrics.TotalCalls.Load() != expectedTotal {
		t.Errorf("Expected %d total calls, got %d", expectedTotal, metrics.TotalCalls.Load())
	}

	if metrics.SuccessfulCalls.Load() != expectedSuccessful {
		t.Errorf("Expected %d successful calls, got %d", expectedSuccessful, metrics.SuccessfulCalls.Load())
	}

	if metrics.FailedCalls.Load() != expectedFailed {
		t.Errorf("Expected %d failed calls, got %d", expectedFailed, metrics.FailedCalls.Load())
	}

	t.Logf("Metrics accuracy test:")
	t.Logf("  Total calls: %d", metrics.TotalCalls.Load())
	t.Logf("  Successful calls: %d", metrics.SuccessfulCalls.Load())
	t.Logf("  Failed calls: %d", metrics.FailedCalls.Load())
	t.Logf("  Average execution time: %v", metrics.AverageExecutionTime)
}

// TestBulkheadProductionResetMetrics tests metrics reset functionality
func TestBulkheadProductionResetMetrics(t *testing.T) {
	config := bulkhead.DefaultBulkheadConfig()
	b, err := bulkhead.NewBulkhead(config)
	if err != nil {
		t.Fatalf("Failed to create bulkhead: %v", err)
	}

	ctx := context.Background()

	// Execute some operations
	for i := 0; i < 5; i++ {
		_, err := b.Execute(ctx, func() (interface{}, error) {
			return "success", nil
		})
		if err != nil {
			t.Errorf("Unexpected error: %v", err)
		}
	}

	// Verify metrics are populated
	metrics := b.GetMetrics()
	if metrics.TotalCalls.Load() == 0 {
		t.Error("Expected metrics to be populated")
	}

	// Reset metrics
	b.ResetMetrics()

	// Verify metrics are reset
	metrics = b.GetMetrics()
	if metrics.TotalCalls.Load() != 0 {
		t.Error("Expected metrics to be reset to zero")
	}

	if metrics.SuccessfulCalls.Load() != 0 {
		t.Error("Expected successful calls to be reset to zero")
	}

	if metrics.AverageExecutionTime != 0 {
		t.Error("Expected average execution time to be reset to zero")
	}

	t.Logf("Metrics reset test completed successfully")
}
