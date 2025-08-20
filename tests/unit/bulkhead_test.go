package unit

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/SeaSBee/go-patternx"
)

func TestNewBulkhead(t *testing.T) {
	config := patternx.DefaultBulkheadConfig()
	b, err := patternx.NewBulkhead(config)

	if err != nil {
		t.Fatalf("Failed to create bulkhead: %v", err)
	}

	if b == nil {
		t.Fatal("Expected bulkhead to be created")
	}

	// Test that the bulkhead works with the configuration
	result, err := b.Execute(context.Background(), func() (interface{}, error) {
		return "test", nil
	})

	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}

	if result != "test" {
		t.Errorf("Expected result 'test', got %v", result)
	}
}

func TestBulkheadExecute(t *testing.T) {
	config := patternx.BulkheadConfig{
		MaxConcurrentCalls: 2,
		MaxWaitDuration:    1 * time.Second,
		MaxQueueSize:       5,
		HealthThreshold:    0.5,
	}
	b, err := patternx.NewBulkhead(config)
	if err != nil {
		t.Fatalf("Failed to create bulkhead: %v", err)
	}
	defer b.Close()

	// Test successful execution
	result, err := b.Execute(context.Background(), func() (interface{}, error) {
		return "success", nil
	})

	if err != nil {
		t.Errorf("Expected no error, got %v", err)
	}

	if result != "success" {
		t.Errorf("Expected result 'success', got %v", result)
	}
}

func TestBulkheadExecuteWithError(t *testing.T) {
	config := patternx.DefaultBulkheadConfig()
	b, err := patternx.NewBulkhead(config)
	if err != nil {
		t.Fatalf("Failed to create bulkhead: %v", err)
	}
	defer b.Close()

	expectedErr := errors.New("test error")
	result, err := b.Execute(context.Background(), func() (interface{}, error) {
		return nil, expectedErr
	})

	if err != expectedErr {
		t.Errorf("Expected error %v, got %v", expectedErr, err)
	}

	if result != nil {
		t.Errorf("Expected nil result, got %v", result)
	}
}

func TestBulkheadConcurrencyLimit(t *testing.T) {
	config := patternx.BulkheadConfig{
		MaxConcurrentCalls: 2,
		MaxWaitDuration:    100 * time.Millisecond,
		MaxQueueSize:       5,
		HealthThreshold:    0.5,
	}
	b, err := patternx.NewBulkhead(config)
	if err != nil {
		t.Fatalf("Failed to create bulkhead: %v", err)
	}
	defer b.Close()

	var wg sync.WaitGroup
	var concurrentCount int
	var mu sync.Mutex

	// Start 5 goroutines, only 2 should execute concurrently
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			b.Execute(context.Background(), func() (interface{}, error) {
				mu.Lock()
				concurrentCount++
				current := concurrentCount
				mu.Unlock()

				// Simulate work
				time.Sleep(50 * time.Millisecond)

				mu.Lock()
				concurrentCount--
				mu.Unlock()

				return current, nil
			})
		}()
	}

	wg.Wait()

	// Check that maximum concurrent executions was respected
	if concurrentCount > 2 {
		t.Errorf("Expected max concurrent executions to be 2, got %d", concurrentCount)
	}
}

func TestBulkheadTimeout(t *testing.T) {
	config := patternx.BulkheadConfig{
		MaxConcurrentCalls: 1,
		MaxWaitDuration:    50 * time.Millisecond,
		MaxQueueSize:       1,
		HealthThreshold:    0.5,
	}
	b, err := patternx.NewBulkhead(config)
	if err != nil {
		t.Fatalf("Failed to create bulkhead: %v", err)
	}
	defer b.Close()

	// Fill the semaphore
	done := make(chan struct{})
	go func() {
		b.Execute(context.Background(), func() (interface{}, error) {
			time.Sleep(200 * time.Millisecond)
			return "done", nil
		})
		close(done)
	}()

	// Wait a bit for the first call to start
	time.Sleep(10 * time.Millisecond)

	// This call should timeout
	_, err = b.Execute(context.Background(), func() (interface{}, error) {
		return "should timeout", nil
	})

	if err == nil || !strings.Contains(err.Error(), "timeout") {
		t.Errorf("Expected timeout error, got %v", err)
	}

	<-done
}

func TestBulkheadQueueFull(t *testing.T) {
	config := patternx.BulkheadConfig{
		MaxConcurrentCalls: 1,
		MaxWaitDuration:    50 * time.Millisecond,
		MaxQueueSize:       1,
		HealthThreshold:    0.5,
	}
	b, err := patternx.NewBulkhead(config)
	if err != nil {
		t.Fatalf("Failed to create bulkhead: %v", err)
	}
	defer b.Close()

	// Fill the semaphore and queue
	done := make(chan struct{})
	go func() {
		b.Execute(context.Background(), func() (interface{}, error) {
			time.Sleep(200 * time.Millisecond)
			return "done", nil
		})
		close(done)
	}()

	// Wait a bit for the first call to start
	time.Sleep(10 * time.Millisecond)

	// Fill the queue
	go func() {
		b.Execute(context.Background(), func() (interface{}, error) {
			time.Sleep(100 * time.Millisecond)
			return "queued", nil
		})
	}()

	// Wait a bit for the queue to fill
	time.Sleep(10 * time.Millisecond)

	// This call should be rejected
	_, err = b.Execute(context.Background(), func() (interface{}, error) {
		return "should be rejected", nil
	})

	if err == nil || !strings.Contains(err.Error(), "queue") {
		t.Errorf("Expected queue full error, got %v", err)
	}

	<-done
}

func TestBulkheadExecuteAsync(t *testing.T) {
	config := patternx.DefaultBulkheadConfig()
	b, err := patternx.NewBulkhead(config)
	if err != nil {
		t.Fatalf("Failed to create bulkhead: %v", err)
	}
	defer b.Close()

	resultChan := b.ExecuteAsync(context.Background(), func() (interface{}, error) {
		return "async success", nil
	})

	result := <-resultChan
	if result.Error != nil {
		t.Errorf("Expected no error, got %v", result.Error)
	}

	if result.Result != "async success" {
		t.Errorf("Expected result 'async success', got %v", result.Result)
	}
}

func TestBulkheadMetrics(t *testing.T) {
	config := patternx.DefaultBulkheadConfig()
	b, err := patternx.NewBulkhead(config)
	if err != nil {
		t.Fatalf("Failed to create bulkhead: %v", err)
	}
	defer b.Close()

	// Execute some operations
	b.Execute(context.Background(), func() (interface{}, error) {
		return "success", nil
	})

	b.Execute(context.Background(), func() (interface{}, error) {
		return nil, errors.New("failure")
	})

	metrics := b.GetMetrics()

	if metrics.TotalCalls.Load() != 2 {
		t.Errorf("Expected 2 total calls, got %d", metrics.TotalCalls.Load())
	}

	if metrics.SuccessfulCalls.Load() != 1 {
		t.Errorf("Expected 1 successful call, got %d", metrics.SuccessfulCalls.Load())
	}

	if metrics.FailedCalls.Load() != 1 {
		t.Errorf("Expected 1 failed call, got %d", metrics.FailedCalls.Load())
	}
}

func TestBulkheadResetMetrics(t *testing.T) {
	config := patternx.DefaultBulkheadConfig()
	b, err := patternx.NewBulkhead(config)
	if err != nil {
		t.Fatalf("Failed to create bulkhead: %v", err)
	}
	defer b.Close()

	// Execute an operation
	b.Execute(context.Background(), func() (interface{}, error) {
		return "success", nil
	})

	// Reset metrics
	b.ResetMetrics()

	metrics := b.GetMetrics()

	if metrics.TotalCalls.Load() != 0 {
		t.Errorf("Expected 0 total calls after reset, got %d", metrics.TotalCalls.Load())
	}

	if metrics.SuccessfulCalls.Load() != 0 {
		t.Errorf("Expected 0 successful calls after reset, got %d", metrics.SuccessfulCalls.Load())
	}
}

func TestBulkheadIsHealthy(t *testing.T) {
	config := patternx.DefaultBulkheadConfig()
	b, err := patternx.NewBulkhead(config)
	if err != nil {
		t.Fatalf("Failed to create bulkhead: %v", err)
	}
	defer b.Close()

	// Should be healthy initially
	if !b.IsHealthy() {
		t.Error("Expected bulkhead to be healthy initially")
	}

	// Execute some successful operations
	for i := 0; i < 10; i++ {
		b.Execute(context.Background(), func() (interface{}, error) {
			return "success", nil
		})
	}

	// Should still be healthy
	if !b.IsHealthy() {
		t.Error("Expected bulkhead to be healthy after successful operations")
	}

	// Execute some failed operations
	for i := 0; i < 10; i++ {
		b.Execute(context.Background(), func() (interface{}, error) {
			return nil, errors.New("failure")
		})
	}

	// Should still be healthy (50% failure rate threshold)
	if !b.IsHealthy() {
		t.Error("Expected bulkhead to be healthy with 50% failure rate")
	}

	// Execute more failures to exceed threshold
	for i := 0; i < 10; i++ {
		b.Execute(context.Background(), func() (interface{}, error) {
			return nil, errors.New("failure")
		})
	}

	// Should be unhealthy now
	if b.IsHealthy() {
		t.Error("Expected bulkhead to be unhealthy with high failure rate")
	}
}

func TestBulkheadContextCancellation(t *testing.T) {
	config := patternx.DefaultBulkheadConfig()
	b, err := patternx.NewBulkhead(config)
	if err != nil {
		t.Fatalf("Failed to create bulkhead: %v", err)
	}
	defer b.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately

	_, err = b.Execute(ctx, func() (interface{}, error) {
		return "should not execute", nil
	})

	if err == nil {
		t.Error("Expected context.Canceled error, got nil")
	}
}

func TestBulkheadConfigValidation(t *testing.T) {
	// Test with invalid config values
	config := patternx.BulkheadConfig{
		MaxConcurrentCalls: 0,
		MaxWaitDuration:    0,
		MaxQueueSize:       0,
		HealthThreshold:    0.5,
	}

	_, err := patternx.NewBulkhead(config)
	if err == nil {
		t.Error("Expected error for invalid config, got nil")
	}
	if !strings.Contains(err.Error(), "max concurrent calls must be between 1 and 10000") {
		t.Errorf("Expected validation error, got %v", err)
	}
}

func TestBulkheadConfigPresets(t *testing.T) {
	// Test default config
	defaultConfig := patternx.DefaultBulkheadConfig()
	if defaultConfig.MaxConcurrentCalls != 10 {
		t.Errorf("Expected default MaxConcurrentCalls 10, got %d", defaultConfig.MaxConcurrentCalls)
	}
	if defaultConfig.MaxWaitDuration != 5*time.Second {
		t.Errorf("Expected default MaxWaitDuration 5s, got %v", defaultConfig.MaxWaitDuration)
	}
	if defaultConfig.MaxQueueSize != 100 {
		t.Errorf("Expected default MaxQueueSize 100, got %d", defaultConfig.MaxQueueSize)
	}

	// Test high performance config
	highPerfConfig := patternx.HighPerformanceBulkheadConfig()
	if highPerfConfig.MaxConcurrentCalls != 50 {
		t.Errorf("Expected high perf MaxConcurrentCalls 50, got %d", highPerfConfig.MaxConcurrentCalls)
	}
	if highPerfConfig.MaxWaitDuration != 10*time.Second {
		t.Errorf("Expected high perf MaxWaitDuration 10s, got %v", highPerfConfig.MaxWaitDuration)
	}
	if highPerfConfig.MaxQueueSize != 500 {
		t.Errorf("Expected high perf MaxQueueSize 500, got %d", highPerfConfig.MaxQueueSize)
	}

	// Test resource constrained config
	resourceConfig := patternx.ResourceConstrainedBulkheadConfig()
	if resourceConfig.MaxConcurrentCalls != 5 {
		t.Errorf("Expected resource constrained MaxConcurrentCalls 5, got %d", resourceConfig.MaxConcurrentCalls)
	}
	if resourceConfig.MaxWaitDuration != 2*time.Second {
		t.Errorf("Expected resource constrained MaxWaitDuration 2s, got %v", resourceConfig.MaxWaitDuration)
	}
	if resourceConfig.MaxQueueSize != 50 {
		t.Errorf("Expected resource constrained MaxQueueSize 50, got %d", resourceConfig.MaxQueueSize)
	}
}

func TestBulkheadConcurrentAccess(t *testing.T) {
	config := patternx.DefaultBulkheadConfig()
	b, err := patternx.NewBulkhead(config)
	if err != nil {
		t.Fatalf("Failed to create bulkhead: %v", err)
	}
	defer b.Close()

	var wg sync.WaitGroup
	numGoroutines := 100

	// Test concurrent access to metrics and health checks
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			// Execute some operations
			b.Execute(context.Background(), func() (interface{}, error) {
				return "success", nil
			})

			// Check metrics
			_ = b.GetMetrics()

			// Check health
			_ = b.IsHealthy()
		}()
	}

	wg.Wait()

	// Verify final state
	metrics := b.GetMetrics()
	if metrics.TotalCalls.Load() != int64(numGoroutines) {
		t.Errorf("Expected %d total calls, got %d", numGoroutines, metrics.TotalCalls.Load())
	}
}
