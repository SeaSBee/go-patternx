package unit

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/SeaSBee/go-patternx/patternx/dlq"
)

// MockRetryHandler implements RetryHandler interface for testing
type MockRetryHandler struct {
	shouldRetry bool
	shouldFail  bool
	delay       time.Duration
	mu          sync.Mutex
	retryCount  int
}

func NewMockRetryHandler(shouldRetry, shouldFail bool, delay time.Duration) *MockRetryHandler {
	return &MockRetryHandler{
		shouldRetry: shouldRetry,
		shouldFail:  shouldFail,
		delay:       delay,
	}
}

func (h *MockRetryHandler) Retry(ctx context.Context, operation *dlq.FailedOperation) error {
	h.mu.Lock()
	h.retryCount++
	h.mu.Unlock()

	time.Sleep(h.delay)

	if h.shouldFail {
		return errors.New("mock retry failed")
	}
	return nil
}

func (h *MockRetryHandler) ShouldRetry(operation *dlq.FailedOperation) bool {
	return h.shouldRetry && operation.RetryCount < operation.MaxRetries
}

func (h *MockRetryHandler) GetRetryCount() int {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.retryCount
}

func TestNewDeadLetterQueue(t *testing.T) {
	config := &dlq.Config{
		MaxRetries:    3,
		RetryDelay:    100 * time.Millisecond,
		WorkerCount:   2,
		QueueSize:     100,
		EnableMetrics: true,
	}

	dq, err := dlq.NewDeadLetterQueue(config)
	if err != nil {
		t.Fatalf("Expected no error creating DLQ, got %v", err)
	}
	if dq == nil {
		t.Fatal("Expected dead letter queue to be created")
	}
}

func TestNewDeadLetterQueueNilConfig(t *testing.T) {
	dq, err := dlq.NewDeadLetterQueue(nil)
	if err != nil {
		t.Fatalf("Expected no error creating DLQ, got %v", err)
	}
	if dq == nil {
		t.Fatal("Expected dead letter queue to be created with default config")
	}
}

func TestDeadLetterQueueAddFailedOperation(t *testing.T) {
	config := &dlq.Config{
		MaxRetries:    3,
		RetryDelay:    50 * time.Millisecond,
		WorkerCount:   1,
		QueueSize:     10,
		EnableMetrics: true,
	}

	dq, err := dlq.NewDeadLetterQueue(config)
	if err != nil {
		t.Fatalf("Expected no error creating DLQ, got %v", err)
	}

	failedOp := &dlq.FailedOperation{
		ID:         "test-operation-1",
		Operation:  "test_operation",
		Key:        "test-key",
		Data:       "test-data",
		Error:      "test error",
		RetryCount: 0,
		MaxRetries: 3,
		CreatedAt:  time.Now(),
	}

	err = dq.AddFailedOperation(failedOp)
	if err != nil {
		t.Fatalf("Expected no error adding failed operation, got %v", err)
	}

	// Wait for processing
	time.Sleep(100 * time.Millisecond)

	metrics := dq.GetMetrics()
	if metrics.TotalFailed != 1 {
		t.Errorf("Expected 1 total failed, got %d", metrics.TotalFailed)
	}
}

func TestDeadLetterQueueAddFailedOperationWithHandler(t *testing.T) {
	config := &dlq.Config{
		MaxRetries:    3,
		RetryDelay:    50 * time.Millisecond,
		WorkerCount:   1,
		QueueSize:     10,
		EnableMetrics: true,
	}

	dq, err := dlq.NewDeadLetterQueue(config)
	if err != nil {
		t.Fatalf("Expected no error creating DLQ, got %v", err)
	}

	// Register a retry handler
	handler := NewMockRetryHandler(true, false, 10*time.Millisecond)
	dq.RegisterHandler("test_operation", handler)

	failedOp := &dlq.FailedOperation{
		ID:          "test-operation-2",
		Operation:   "test_operation",
		Key:         "test-key",
		Data:        "test-data",
		Error:       "test error",
		RetryCount:  0,
		MaxRetries:  3,
		CreatedAt:   time.Now(),
		HandlerType: "test_operation",
	}

	err = dq.AddFailedOperation(failedOp)
	if err != nil {
		t.Fatalf("Expected no error adding failed operation, got %v", err)
	}

	// Wait for processing
	time.Sleep(200 * time.Millisecond)

	metrics := dq.GetMetrics()
	if metrics.TotalRetried == 0 {
		t.Error("Expected retries to occur")
	}

	if handler.GetRetryCount() == 0 {
		t.Error("Expected handler to be called")
	}
}

func TestDeadLetterQueueRetryWithSuccess(t *testing.T) {
	config := &dlq.Config{
		MaxRetries:    3,
		RetryDelay:    50 * time.Millisecond,
		WorkerCount:   1,
		QueueSize:     10,
		EnableMetrics: true,
	}

	dq, err := dlq.NewDeadLetterQueue(config)
	if err != nil {
		t.Fatalf("Expected no error creating DLQ, got %v", err)
	}

	// Register a successful retry handler
	handler := NewMockRetryHandler(true, false, 10*time.Millisecond)
	dq.RegisterHandler("success_operation", handler)

	failedOp := &dlq.FailedOperation{
		ID:          "test-operation-3",
		Operation:   "success_operation",
		Key:         "test-key",
		Data:        "test-data",
		Error:       "test error",
		RetryCount:  0,
		MaxRetries:  3,
		CreatedAt:   time.Now(),
		HandlerType: "success_operation",
	}

	err = dq.AddFailedOperation(failedOp)
	if err != nil {
		t.Fatalf("Expected no error adding failed operation, got %v", err)
	}

	// Wait for processing
	time.Sleep(300 * time.Millisecond)

	metrics := dq.GetMetrics()
	if metrics.TotalSucceeded == 0 {
		t.Error("Expected successful retries")
	}
}

func TestDeadLetterQueueRetryWithFailure(t *testing.T) {
	config := &dlq.Config{
		MaxRetries:    2,
		RetryDelay:    50 * time.Millisecond,
		WorkerCount:   1,
		QueueSize:     10,
		EnableMetrics: true,
	}

	dq, err := dlq.NewDeadLetterQueue(config)
	if err != nil {
		t.Fatalf("Expected no error creating DLQ, got %v", err)
	}

	// Register a failing retry handler
	handler := NewMockRetryHandler(true, true, 10*time.Millisecond)
	dq.RegisterHandler("failure_operation", handler)

	failedOp := &dlq.FailedOperation{
		ID:          "test-operation-4",
		Operation:   "failure_operation",
		Key:         "test-key",
		Data:        "test-data",
		Error:       "test error",
		RetryCount:  0,
		MaxRetries:  2,
		CreatedAt:   time.Now(),
		HandlerType: "failure_operation",
	}

	err = dq.AddFailedOperation(failedOp)
	if err != nil {
		t.Fatalf("Expected no error adding failed operation, got %v", err)
	}

	// Wait for processing
	time.Sleep(300 * time.Millisecond)

	metrics := dq.GetMetrics()
	if metrics.TotalDropped == 0 {
		t.Error("Expected operations to be dropped after max retries")
	}
}

func TestDeadLetterQueueNoRetryHandler(t *testing.T) {
	config := &dlq.Config{
		MaxRetries:    3,
		RetryDelay:    50 * time.Millisecond,
		WorkerCount:   1,
		QueueSize:     10,
		EnableMetrics: true,
	}

	dq, err := dlq.NewDeadLetterQueue(config)
	if err != nil {
		t.Fatalf("Expected no error creating DLQ, got %v", err)
	}

	failedOp := &dlq.FailedOperation{
		ID:         "test-operation-5",
		Operation:  "unknown_operation",
		Key:        "test-key",
		Data:       "test-data",
		Error:      "test error",
		RetryCount: 0,
		MaxRetries: 3,
		CreatedAt:  time.Now(),
	}

	err = dq.AddFailedOperation(failedOp)
	if err != nil {
		t.Fatalf("Expected no error adding failed operation, got %v", err)
	}

	// Wait for processing
	time.Sleep(100 * time.Millisecond)

	metrics := dq.GetMetrics()
	if metrics.TotalFailed != 1 {
		t.Errorf("Expected 1 total failed, got %d", metrics.TotalFailed)
	}
}

func TestDeadLetterQueueMaxRetriesExceeded(t *testing.T) {
	config := &dlq.Config{
		MaxRetries:    1,
		RetryDelay:    50 * time.Millisecond,
		WorkerCount:   1,
		QueueSize:     10,
		EnableMetrics: true,
	}

	dq, err := dlq.NewDeadLetterQueue(config)
	if err != nil {
		t.Fatalf("Expected no error creating DLQ, got %v", err)
	}

	// Register a handler that always fails
	handler := NewMockRetryHandler(true, true, 10*time.Millisecond)
	dq.RegisterHandler("max_retries_operation", handler)

	failedOp := &dlq.FailedOperation{
		ID:          "test-operation-6",
		Operation:   "max_retries_operation",
		Key:         "test-key",
		Data:        "test-data",
		Error:       "test error",
		RetryCount:  0,
		MaxRetries:  1,
		CreatedAt:   time.Now(),
		HandlerType: "max_retries_operation",
	}

	err = dq.AddFailedOperation(failedOp)
	if err != nil {
		t.Fatalf("Expected no error adding failed operation, got %v", err)
	}

	// Wait for processing
	time.Sleep(200 * time.Millisecond)

	metrics := dq.GetMetrics()
	if metrics.TotalDropped == 0 {
		t.Error("Expected operation to be dropped after max retries")
	}
}

func TestDeadLetterQueueConcurrentAccess(t *testing.T) {
	config := &dlq.Config{
		MaxRetries:    3,
		RetryDelay:    10 * time.Millisecond,
		WorkerCount:   3,
		QueueSize:     100,
		EnableMetrics: true,
	}

	dq, err := dlq.NewDeadLetterQueue(config)
	if err != nil {
		t.Fatalf("Expected no error creating DLQ, got %v", err)
	}

	// Register a handler
	handler := NewMockRetryHandler(true, false, 5*time.Millisecond)
	dq.RegisterHandler("concurrent_operation", handler)

	var wg sync.WaitGroup
	numOperations := 20

	for i := 0; i < numOperations; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			failedOp := &dlq.FailedOperation{
				ID:          fmt.Sprintf("test-operation-%d", id),
				Operation:   "concurrent_operation",
				Key:         fmt.Sprintf("test-key-%d", id),
				Data:        fmt.Sprintf("test-data-%d", id),
				Error:       "test error",
				RetryCount:  0,
				MaxRetries:  3,
				CreatedAt:   time.Now(),
				HandlerType: "concurrent_operation",
			}

			err := dq.AddFailedOperation(failedOp)
			if err != nil {
				t.Errorf("Failed to add operation %d: %v", id, err)
			}
		}(i)
	}

	wg.Wait()

	// Wait for processing
	time.Sleep(500 * time.Millisecond)

	metrics := dq.GetMetrics()
	if metrics.TotalFailed != int64(numOperations) {
		t.Errorf("Expected %d total failed, got %d", numOperations, metrics.TotalFailed)
	}
}

func TestDeadLetterQueueGetQueue(t *testing.T) {
	config := &dlq.Config{
		MaxRetries:    3,
		RetryDelay:    50 * time.Millisecond,
		WorkerCount:   1,
		QueueSize:     10,
		EnableMetrics: true,
	}

	dq, err := dlq.NewDeadLetterQueue(config)
	if err != nil {
		t.Fatalf("Expected no error creating DLQ, got %v", err)
	}

	// Add multiple operations
	for i := 0; i < 5; i++ {
		failedOp := &dlq.FailedOperation{
			ID:         fmt.Sprintf("test-operation-%d", i),
			Operation:  "test_operation",
			Key:        fmt.Sprintf("test-key-%d", i),
			Data:       fmt.Sprintf("test-data-%d", i),
			Error:      "test error",
			RetryCount: 0,
			MaxRetries: 3,
			CreatedAt:  time.Now(),
		}

		err := dq.AddFailedOperation(failedOp)
		if err != nil {
			t.Fatalf("Failed to add operation %d: %v", i, err)
		}
	}

	// Wait for processing
	time.Sleep(100 * time.Millisecond)

	// Get all operations
	operations := dq.GetQueue()
	if len(operations) != 5 {
		t.Errorf("Expected 5 operations, got %d", len(operations))
	}
}

func TestDeadLetterQueueClearQueue(t *testing.T) {
	config := &dlq.Config{
		MaxRetries:    3,
		RetryDelay:    50 * time.Millisecond,
		WorkerCount:   1,
		QueueSize:     10,
		EnableMetrics: true,
	}

	dq, err := dlq.NewDeadLetterQueue(config)
	if err != nil {
		t.Fatalf("Expected no error creating DLQ, got %v", err)
	}

	// Add an operation
	failedOp := &dlq.FailedOperation{
		ID:         "test-operation-9",
		Operation:  "test_operation",
		Key:        "test-key",
		Data:       "test-data",
		Error:      "test error",
		RetryCount: 0,
		MaxRetries: 3,
		CreatedAt:  time.Now(),
	}

	err = dq.AddFailedOperation(failedOp)
	if err != nil {
		t.Fatalf("Expected no error adding failed operation, got %v", err)
	}

	// Wait for processing
	time.Sleep(100 * time.Millisecond)

	// Clear the queue
	dq.ClearQueue()

	// Check that operations are cleared
	operations := dq.GetQueue()
	if len(operations) != 0 {
		t.Errorf("Expected 0 operations after clear, got %d", len(operations))
	}

	metrics := dq.GetMetrics()
	if metrics.CurrentQueue != 0 {
		t.Errorf("Expected 0 current queue after clear, got %d", metrics.CurrentQueue)
	}
}

func TestDeadLetterQueueMetrics(t *testing.T) {
	config := &dlq.Config{
		MaxRetries:    2,
		RetryDelay:    50 * time.Millisecond,
		WorkerCount:   1,
		QueueSize:     10,
		EnableMetrics: true,
	}

	dq, err := dlq.NewDeadLetterQueue(config)
	if err != nil {
		t.Fatalf("Expected no error creating DLQ, got %v", err)
	}

	// Register a handler that succeeds on retry
	handler := NewMockRetryHandler(true, false, 10*time.Millisecond)
	dq.RegisterHandler("metrics_operation", handler)

	failedOp := &dlq.FailedOperation{
		ID:          "test-operation-10",
		Operation:   "metrics_operation",
		Key:         "test-key",
		Data:        "test-data",
		Error:       "test error",
		RetryCount:  0,
		MaxRetries:  2,
		CreatedAt:   time.Now(),
		HandlerType: "metrics_operation",
	}

	err = dq.AddFailedOperation(failedOp)
	if err != nil {
		t.Fatalf("Expected no error adding failed operation, got %v", err)
	}

	// Wait for processing
	time.Sleep(300 * time.Millisecond)

	metrics := dq.GetMetrics()
	if metrics.TotalFailed != 1 {
		t.Errorf("Expected 1 total failed, got %d", metrics.TotalFailed)
	}
	if metrics.TotalRetried == 0 {
		t.Error("Expected retries to occur")
	}
	if metrics.TotalSucceeded == 0 {
		t.Error("Expected successful retries")
	}
	if metrics.CurrentQueue != 0 {
		t.Errorf("Expected 0 current queue, got %d", metrics.CurrentQueue)
	}
}

func TestDeadLetterQueueClose(t *testing.T) {
	config := &dlq.Config{
		MaxRetries:    3,
		RetryDelay:    50 * time.Millisecond,
		WorkerCount:   1,
		QueueSize:     10,
		EnableMetrics: true,
	}

	dq, err := dlq.NewDeadLetterQueue(config)
	if err != nil {
		t.Fatalf("Expected no error creating DLQ, got %v", err)
	}

	// Add an operation
	failedOp := &dlq.FailedOperation{
		ID:         "test-operation-11",
		Operation:  "test_operation",
		Key:        "test-key",
		Data:       "test-data",
		Error:      "test error",
		RetryCount: 0,
		MaxRetries: 3,
		CreatedAt:  time.Now(),
	}

	err = dq.AddFailedOperation(failedOp)
	if err != nil {
		t.Fatalf("Expected no error adding failed operation, got %v", err)
	}

	// Close the DLQ
	err = dq.Close()
	if err != nil {
		t.Fatalf("Expected no error closing DLQ, got %v", err)
	}
}

func TestWriteBehindHandler(t *testing.T) {
	// Test the built-in WriteBehindHandler
	var writeCount int
	var mu sync.Mutex

	writer := func(ctx context.Context, key string, data interface{}) error {
		mu.Lock()
		writeCount++
		mu.Unlock()
		return nil
	}

	handler := dlq.NewWriteBehindHandler(writer)

	operation := &dlq.FailedOperation{
		ID:         "test-write-behind",
		Operation:  "write_behind",
		Key:        "test-key",
		Data:       "test-data",
		Error:      "test error",
		RetryCount: 0,
		MaxRetries: 3,
		CreatedAt:  time.Now(),
	}

	ctx := context.Background()
	err := handler.Retry(ctx, operation)
	if err != nil {
		t.Fatalf("Expected no error from write-behind handler, got %v", err)
	}

	if writeCount != 1 {
		t.Errorf("Expected 1 write, got %d", writeCount)
	}

	// Test ShouldRetry
	if !handler.ShouldRetry(operation) {
		t.Error("Expected ShouldRetry to return true")
	}

	// Test with max retries exceeded
	operation.RetryCount = 3
	if handler.ShouldRetry(operation) {
		t.Error("Expected ShouldRetry to return false when max retries exceeded")
	}
}
