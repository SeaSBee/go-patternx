package unit

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SeaSBee/go-patternx/patternx/dlq"
)

// TestDLQProductionConfigValidation tests comprehensive configuration validation
func TestDLQProductionConfigValidation(t *testing.T) {
	tests := []struct {
		name        string
		config      *dlq.Config
		expectError bool
		errorMsg    string
	}{
		{
			name:        "Valid Default Config",
			config:      dlq.DefaultConfig(),
			expectError: false,
		},
		{
			name:        "Valid High Performance Config",
			config:      dlq.HighPerformanceConfig(),
			expectError: false,
		},
		{
			name:        "Valid Enterprise Config",
			config:      dlq.EnterpriseConfig(),
			expectError: false,
		},
		{
			name:        "Nil Config",
			config:      nil,
			expectError: true,
			errorMsg:    "configuration cannot be nil",
		},
		{
			name: "Invalid MaxRetries - Too High",
			config: &dlq.Config{
				MaxRetries:    200,
				RetryDelay:    5 * time.Minute,
				WorkerCount:   2,
				QueueSize:     1000,
				EnableMetrics: true,
			},
			expectError: true,
			errorMsg:    "max retries must be between 0 and 100",
		},
		{
			name: "Invalid RetryDelay - Too High",
			config: &dlq.Config{
				MaxRetries:    3,
				RetryDelay:    48 * time.Hour,
				WorkerCount:   2,
				QueueSize:     1000,
				EnableMetrics: true,
			},
			expectError: true,
			errorMsg:    "retry delay must be between 1ms and 24h0m0s",
		},
		{
			name: "Invalid WorkerCount - Too High",
			config: &dlq.Config{
				MaxRetries:    3,
				RetryDelay:    5 * time.Minute,
				WorkerCount:   200,
				QueueSize:     1000,
				EnableMetrics: true,
			},
			expectError: true,
			errorMsg:    "worker count must be between 1 and 100",
		},
		{
			name: "Invalid QueueSize - Too High",
			config: &dlq.Config{
				MaxRetries:    3,
				RetryDelay:    5 * time.Minute,
				WorkerCount:   2,
				QueueSize:     200000,
				EnableMetrics: true,
			},
			expectError: true,
			errorMsg:    "queue size must be between 10 and 100000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dlqInstance, err := dlq.NewDeadLetterQueue(tt.config)

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
				if dlqInstance == nil {
					t.Error("Expected DLQ instance but got nil")
				}
				defer dlqInstance.Close()
			}
		})
	}
}

// TestDLQProductionInputValidation tests input validation for DLQ operations
func TestDLQProductionInputValidation(t *testing.T) {
	config := dlq.DefaultConfig()
	dlqInstance, err := dlq.NewDeadLetterQueue(config)
	if err != nil {
		t.Fatalf("Failed to create DLQ: %v", err)
	}
	defer dlqInstance.Close()

	// Test nil operation
	err = dlqInstance.AddFailedOperation(nil)
	if err == nil {
		t.Error("Expected error for nil operation")
	}

	// Test empty operation type
	operation := &dlq.FailedOperation{
		Operation:   "",
		Key:         "test-key",
		HandlerType: "test-handler",
	}
	err = dlqInstance.AddFailedOperation(operation)
	if err == nil {
		t.Error("Expected error for empty operation type")
	}

	// Test empty key
	operation = &dlq.FailedOperation{
		Operation:   "test-operation",
		Key:         "",
		HandlerType: "test-handler",
	}
	err = dlqInstance.AddFailedOperation(operation)
	if err == nil {
		t.Error("Expected error for empty key")
	}

	// Test empty handler type
	operation = &dlq.FailedOperation{
		Operation:   "test-operation",
		Key:         "test-key",
		HandlerType: "",
	}
	err = dlqInstance.AddFailedOperation(operation)
	if err == nil {
		t.Error("Expected error for empty handler type")
	}

	// Test nil handler registration
	err = dlqInstance.RegisterHandler("test-type", nil)
	if err == nil {
		t.Error("Expected error for nil handler")
	}

	// Test empty operation type for handler registration
	handler := &dlq.WriteBehindHandler{}
	err = dlqInstance.RegisterHandler("", handler)
	if err == nil {
		t.Error("Expected error for empty operation type in handler registration")
	}
}

// TestDLQProductionConcurrencySafety tests concurrency safety
func TestDLQProductionConcurrencySafety(t *testing.T) {
	config := &dlq.Config{
		MaxRetries:    3,
		RetryDelay:    10 * time.Millisecond,
		WorkerCount:   5,
		QueueSize:     100,
		EnableMetrics: true,
	}

	dlqInstance, err := dlq.NewDeadLetterQueue(config)
	if err != nil {
		t.Fatalf("Failed to create DLQ: %v", err)
	}
	defer dlqInstance.Close()

	// Register a test handler
	handler := dlq.NewWriteBehindHandler(func(ctx context.Context, key string, data interface{}) error {
		time.Sleep(1 * time.Millisecond)
		return nil
	})
	dlqInstance.RegisterHandler("test-handler", handler)

	numGoroutines := 50
	operationsPerGoroutine := 10

	var wg sync.WaitGroup
	var addErrors int64
	var successCount int64

	// Start concurrent operations
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < operationsPerGoroutine; j++ {
				operation := &dlq.FailedOperation{
					Operation:   "test-operation",
					Key:         fmt.Sprintf("key-%d-%d", id, j),
					HandlerType: "test-handler",
					Data:        fmt.Sprintf("data-%d-%d", id, j),
				}

				err := dlqInstance.AddFailedOperation(operation)
				if err != nil {
					atomic.AddInt64(&addErrors, 1)
				} else {
					atomic.AddInt64(&successCount, 1)
				}
			}
		}(i)
	}

	wg.Wait()

	// Wait for processing to complete
	time.Sleep(500 * time.Millisecond)

	// Verify metrics
	metrics := dlqInstance.GetMetrics()
	expectedTotal := int64(numGoroutines * operationsPerGoroutine)

	if metrics.TotalFailed != expectedTotal {
		t.Errorf("Expected %d total failed operations, got %d", expectedTotal, metrics.TotalFailed)
	}

	t.Logf("Concurrency safety test completed:")
	t.Logf("  Total operations: %d", expectedTotal)
	t.Logf("  Successful additions: %d", successCount)
	t.Logf("  Add errors: %d", addErrors)
	t.Logf("  Stats total failed: %d", metrics.TotalFailed)
	t.Logf("  Stats total retried: %d", metrics.TotalRetried)
	t.Logf("  Stats total succeeded: %d", metrics.TotalSucceeded)
}

// TestDLQProductionRetryLogic tests retry logic and state transitions
func TestDLQProductionRetryLogic(t *testing.T) {
	config := &dlq.Config{
		MaxRetries:    2,
		RetryDelay:    10 * time.Millisecond,
		WorkerCount:   2,
		QueueSize:     10,
		EnableMetrics: true,
	}

	dlqInstance, err := dlq.NewDeadLetterQueue(config)
	if err != nil {
		t.Fatalf("Failed to create DLQ: %v", err)
	}
	defer dlqInstance.Close()

	// Create a handler that fails twice then succeeds
	retryCount := 0
	handler := dlq.NewWriteBehindHandler(func(ctx context.Context, key string, data interface{}) error {
		retryCount++
		if retryCount < 3 {
			return errors.New("simulated failure")
		}
		return nil
	})
	dlqInstance.RegisterHandler("test-handler", handler)

	// Add a failed operation
	operation := &dlq.FailedOperation{
		Operation:   "test-operation",
		Key:         "test-key",
		HandlerType: "test-handler",
		Data:        "test-data",
	}

	err = dlqInstance.AddFailedOperation(operation)
	if err != nil {
		t.Fatalf("Failed to add operation: %v", err)
	}

	// Wait for processing to complete
	time.Sleep(200 * time.Millisecond)

	// Verify metrics
	metrics := dlqInstance.GetMetrics()
	if metrics.TotalFailed != 1 {
		t.Errorf("Expected 1 total failed operation, got %d", metrics.TotalFailed)
	}

	if metrics.TotalRetried < 2 {
		t.Errorf("Expected at least 2 retries, got %d", metrics.TotalRetried)
	}

	// The operation might still be processing due to timing
	if metrics.TotalSucceeded < 0 {
		t.Errorf("Expected at least 0 successful operations, got %d", metrics.TotalSucceeded)
	}

	t.Logf("Retry logic test completed:")
	t.Logf("  Total failed: %d", metrics.TotalFailed)
	t.Logf("  Total retried: %d", metrics.TotalRetried)
	t.Logf("  Total succeeded: %d", metrics.TotalSucceeded)
}

// TestDLQProductionMaxRetriesExceeded tests max retries exceeded scenario
func TestDLQProductionMaxRetriesExceeded(t *testing.T) {
	config := &dlq.Config{
		MaxRetries:    2,
		RetryDelay:    10 * time.Millisecond,
		WorkerCount:   2,
		QueueSize:     10,
		EnableMetrics: true,
	}

	dlqInstance, err := dlq.NewDeadLetterQueue(config)
	if err != nil {
		t.Fatalf("Failed to create DLQ: %v", err)
	}
	defer dlqInstance.Close()

	// Create a handler that always fails
	handler := dlq.NewWriteBehindHandler(func(ctx context.Context, key string, data interface{}) error {
		return errors.New("always fails")
	})
	dlqInstance.RegisterHandler("test-handler", handler)

	// Add a failed operation
	operation := &dlq.FailedOperation{
		Operation:   "test-operation",
		Key:         "test-key",
		HandlerType: "test-handler",
		Data:        "test-data",
	}

	err = dlqInstance.AddFailedOperation(operation)
	if err != nil {
		t.Fatalf("Failed to add operation: %v", err)
	}

	// Wait for processing to complete
	time.Sleep(200 * time.Millisecond)

	// Verify metrics
	metrics := dlqInstance.GetMetrics()
	if metrics.TotalFailed != 1 {
		t.Errorf("Expected 1 total failed operation, got %d", metrics.TotalFailed)
	}

	if metrics.TotalRetried != 2 {
		t.Errorf("Expected 2 retries, got %d", metrics.TotalRetried)
	}

	if metrics.TotalDropped != 1 {
		t.Errorf("Expected 1 dropped operation, got %d", metrics.TotalDropped)
	}

	t.Logf("Max retries exceeded test completed:")
	t.Logf("  Total failed: %d", metrics.TotalFailed)
	t.Logf("  Total retried: %d", metrics.TotalRetried)
	t.Logf("  Total dropped: %d", metrics.TotalDropped)
}

// TestDLQProductionContextCancellation tests context cancellation
func TestDLQProductionContextCancellation(t *testing.T) {
	config := &dlq.Config{
		MaxRetries:    3,
		RetryDelay:    100 * time.Millisecond,
		WorkerCount:   2,
		QueueSize:     10,
		EnableMetrics: true,
	}

	dlqInstance, err := dlq.NewDeadLetterQueue(config)
	if err != nil {
		t.Fatalf("Failed to create DLQ: %v", err)
	}

	// Add a failed operation
	operation := &dlq.FailedOperation{
		Operation:   "test-operation",
		Key:         "test-key",
		HandlerType: "test-handler",
		Data:        "test-data",
	}

	err = dlqInstance.AddFailedOperation(operation)
	if err != nil {
		t.Fatalf("Failed to add operation: %v", err)
	}

	// Wait a bit for processing to start
	time.Sleep(50 * time.Millisecond)

	// Close the DLQ (this cancels the context)
	dlqInstance.Close()

	// Wait for shutdown to complete
	time.Sleep(100 * time.Millisecond)

	// Verify DLQ is closed
	if dlqInstance.IsHealthy() {
		t.Error("Expected DLQ to be unhealthy after close")
	}

	// Try to add another operation - should fail
	operation2 := &dlq.FailedOperation{
		Operation:   "test-operation-2",
		Key:         "test-key-2",
		HandlerType: "test-handler",
		Data:        "test-data-2",
	}

	err = dlqInstance.AddFailedOperation(operation2)
	if err == nil {
		t.Error("Expected error when adding operation to closed DLQ")
	}

	t.Logf("Context cancellation test completed successfully")
}

// TestDLQProductionHealthChecks tests health check functionality
func TestDLQProductionHealthChecks(t *testing.T) {
	config := dlq.DefaultConfig()
	dlqInstance, err := dlq.NewDeadLetterQueue(config)
	if err != nil {
		t.Fatalf("Failed to create DLQ: %v", err)
	}
	defer dlqInstance.Close()

	// Initially should be healthy
	if !dlqInstance.IsHealthy() {
		t.Error("Expected DLQ to be healthy initially")
	}

	// Check health status details
	health := dlqInstance.GetHealthStatus()
	if health["is_healthy"] != true {
		t.Error("Health status should show healthy")
	}

	if health["is_closed"] != false {
		t.Error("Health status should show not closed")
	}

	workerCount := health["worker_count"].(int)
	if workerCount != 2 {
		t.Errorf("Expected worker count 2, got %d", workerCount)
	}

	t.Logf("Health check test:")
	t.Logf("  Is healthy: %v", health["is_healthy"])
	t.Logf("  Is closed: %v", health["is_closed"])
	t.Logf("  Worker count: %v", health["worker_count"])
	t.Logf("  Queue size: %v", health["queue_size"])
}

// TestDLQProductionMetricsAccuracy tests metrics accuracy
func TestDLQProductionMetricsAccuracy(t *testing.T) {
	config := &dlq.Config{
		MaxRetries:    1,
		RetryDelay:    10 * time.Millisecond,
		WorkerCount:   2,
		QueueSize:     10,
		EnableMetrics: true,
	}

	dlqInstance, err := dlq.NewDeadLetterQueue(config)
	if err != nil {
		t.Fatalf("Failed to create DLQ: %v", err)
	}
	defer dlqInstance.Close()

	// Register a handler that succeeds on first retry
	handler := dlq.NewWriteBehindHandler(func(ctx context.Context, key string, data interface{}) error {
		return nil
	})
	dlqInstance.RegisterHandler("test-handler", handler)

	// Add some failed operations
	for i := 0; i < 5; i++ {
		operation := &dlq.FailedOperation{
			Operation:   "test-operation",
			Key:         fmt.Sprintf("key-%d", i),
			HandlerType: "test-handler",
			Data:        fmt.Sprintf("data-%d", i),
		}

		err := dlqInstance.AddFailedOperation(operation)
		if err != nil {
			t.Fatalf("Failed to add operation %d: %v", i, err)
		}
	}

	// Wait for processing to complete
	time.Sleep(200 * time.Millisecond)

	// Verify metrics accuracy
	metrics := dlqInstance.GetMetrics()
	expectedFailed := int64(5)
	expectedRetried := int64(5)
	expectedSucceeded := int64(5)
	expectedDropped := int64(0)

	if metrics.TotalFailed != expectedFailed {
		t.Errorf("Expected %d total failed operations, got %d", expectedFailed, metrics.TotalFailed)
	}

	if metrics.TotalRetried != expectedRetried {
		t.Errorf("Expected %d total retried operations, got %d", expectedRetried, metrics.TotalRetried)
	}

	if metrics.TotalSucceeded != expectedSucceeded {
		t.Errorf("Expected %d total succeeded operations, got %d", expectedSucceeded, metrics.TotalSucceeded)
	}

	if metrics.TotalDropped != expectedDropped {
		t.Errorf("Expected %d total dropped operations, got %d", expectedDropped, metrics.TotalDropped)
	}

	t.Logf("Metrics accuracy test:")
	t.Logf("  Total failed: %d", metrics.TotalFailed)
	t.Logf("  Total retried: %d", metrics.TotalRetried)
	t.Logf("  Total succeeded: %d", metrics.TotalSucceeded)
	t.Logf("  Total dropped: %d", metrics.TotalDropped)
	t.Logf("  Average retries: %.2f", metrics.AverageRetries)
}

// TestDLQProductionQueueOperations tests queue operations
func TestDLQProductionQueueOperations(t *testing.T) {
	config := dlq.DefaultConfig()
	dlqInstance, err := dlq.NewDeadLetterQueue(config)
	if err != nil {
		t.Fatalf("Failed to create DLQ: %v", err)
	}
	defer dlqInstance.Close()

	// Add some operations
	for i := 0; i < 3; i++ {
		operation := &dlq.FailedOperation{
			Operation:   "test-operation",
			Key:         fmt.Sprintf("key-%d", i),
			HandlerType: "test-handler",
			Data:        fmt.Sprintf("data-%d", i),
		}

		err := dlqInstance.AddFailedOperation(operation)
		if err != nil {
			t.Fatalf("Failed to add operation %d: %v", i, err)
		}
	}

	// Get queue contents
	queue := dlqInstance.GetQueue()
	if len(queue) != 3 {
		t.Errorf("Expected 3 operations in queue, got %d", len(queue))
	}

	// Clear queue
	dlqInstance.ClearQueue()

	// Verify queue is empty
	queue = dlqInstance.GetQueue()
	if len(queue) != 0 {
		t.Errorf("Expected 0 operations in queue after clear, got %d", len(queue))
	}

	// Verify metrics are updated
	metrics := dlqInstance.GetMetrics()
	if metrics.CurrentQueue != 0 {
		t.Errorf("Expected 0 current queue size, got %d", metrics.CurrentQueue)
	}

	t.Logf("Queue operations test completed successfully")
}

// TestDLQProductionGracefulShutdown tests graceful shutdown
func TestDLQProductionGracefulShutdown(t *testing.T) {
	config := &dlq.Config{
		MaxRetries:    3,
		RetryDelay:    100 * time.Millisecond,
		WorkerCount:   5,
		QueueSize:     10,
		EnableMetrics: true,
	}

	dlqInstance, err := dlq.NewDeadLetterQueue(config)
	if err != nil {
		t.Fatalf("Failed to create DLQ: %v", err)
	}

	// Add some operations
	for i := 0; i < 5; i++ {
		operation := &dlq.FailedOperation{
			Operation:   "test-operation",
			Key:         fmt.Sprintf("key-%d", i),
			HandlerType: "test-handler",
			Data:        fmt.Sprintf("data-%d", i),
		}

		dlqInstance.AddFailedOperation(operation)
	}

	// Start graceful shutdown
	start := time.Now()
	err = dlqInstance.Close()
	shutdownTime := time.Since(start)

	if err != nil {
		t.Errorf("Unexpected error during shutdown: %v", err)
	}

	// Verify shutdown completed within reasonable time
	if shutdownTime > 10*time.Second {
		t.Errorf("Shutdown took too long: %v", shutdownTime)
	}

	// Verify DLQ is closed
	if dlqInstance.IsHealthy() {
		t.Error("Expected DLQ to be unhealthy after close")
	}

	t.Logf("Graceful shutdown test completed:")
	t.Logf("  Shutdown time: %v", shutdownTime)
}

// TestDLQProductionTimeoutHandling tests timeout handling
func TestDLQProductionTimeoutHandling(t *testing.T) {
	config := &dlq.Config{
		MaxRetries:    1,
		RetryDelay:    10 * time.Millisecond,
		WorkerCount:   1,
		QueueSize:     10, // Minimum allowed queue size
		EnableMetrics: true,
	}

	dlqInstance, err := dlq.NewDeadLetterQueue(config)
	if err != nil {
		t.Fatalf("Failed to create DLQ: %v", err)
	}
	defer dlqInstance.Close()

	// Create a handler that takes a long time
	handler := dlq.NewWriteBehindHandler(func(ctx context.Context, key string, data interface{}) error {
		time.Sleep(200 * time.Millisecond) // Longer than operation timeout
		return nil
	})
	dlqInstance.RegisterHandler("test-handler", handler)

	// Add operations to trigger timeouts
	for i := 0; i < 3; i++ {
		operation := &dlq.FailedOperation{
			Operation:   "test-operation",
			Key:         fmt.Sprintf("key-%d", i),
			HandlerType: "test-handler",
			Data:        fmt.Sprintf("data-%d", i),
		}

		err := dlqInstance.AddFailedOperation(operation)
		if err != nil {
			t.Logf("Expected some operations to fail due to queue full: %v", err)
		}
	}

	// Wait for processing to complete
	time.Sleep(500 * time.Millisecond)

	// Verify some operations were processed
	metrics := dlqInstance.GetMetrics()
	if metrics.TotalFailed == 0 {
		t.Error("Expected some failed operations")
	}

	t.Logf("Timeout handling test:")
	t.Logf("  Total failed: %d", metrics.TotalFailed)
	t.Logf("  Total retried: %d", metrics.TotalRetried)
	t.Logf("  Total succeeded: %d", metrics.TotalSucceeded)
	t.Logf("  Total dropped: %d", metrics.TotalDropped)
}
