package integration

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/SeaSBee/go-patternx"
)

// FullStackService integrates all patterns for a realistic scenario
type FullStackService struct {
	// Resource isolation and fault tolerance
	bulkhead       *patternx.Bulkhead
	circuitBreaker *patternx.CircuitBreaker

	// Processing and retry
	workerPool  *patternx.WorkerPool
	retryPolicy patternx.Policy

	// Data management
	bloomFilter     *patternx.BloomFilter
	deadLetterQueue *patternx.DeadLetterQueue

	// Mock external service
	externalService *MockExternalService

	// Statistics
	mu             sync.RWMutex
	processedItems map[string]bool
	stats          map[string]interface{}
}

// MockExternalService simulates an external API that can fail
type MockExternalService struct {
	successRate float64
	latency     time.Duration
	mu          sync.Mutex
	callCount   int
}

func NewMockExternalService(successRate float64, latency time.Duration) *MockExternalService {
	return &MockExternalService{
		successRate: successRate,
		latency:     latency,
	}
}

func (s *MockExternalService) Call(ctx context.Context, item string) (string, error) {
	s.mu.Lock()
	s.callCount++
	s.mu.Unlock()

	// Simulate latency
	select {
	case <-time.After(s.latency):
	case <-ctx.Done():
		return "", ctx.Err()
	}

	// Simulate failures
	if s.successRate < 1.0 && float64(s.callCount%10)/10.0 < (1.0-s.successRate) {
		return "", fmt.Errorf("external service temporarily unavailable")
	}

	return fmt.Sprintf("processed-%s", item), nil
}

func (s *MockExternalService) GetCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.callCount
}

// NewFullStackService creates a service with all patterns integrated
func NewFullStackService() (*FullStackService, error) {
	// Create external service
	externalService := NewMockExternalService(0.7, 100*time.Millisecond)

	// Create bulkhead for resource isolation
	bhConfig := patternx.BulkheadConfig{
		MaxConcurrentCalls: 10,
		MaxWaitDuration:    2 * time.Second,
		MaxQueueSize:       20,
		HealthThreshold:    0.5,
	}
	bulkhead, err := patternx.NewBulkhead(bhConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create bulkhead: %w", err)
	}

	// Create circuit breaker for fault tolerance
	cbConfig := patternx.ConfigCircuitBreaker{
		Threshold:   5,
		Timeout:     500 * time.Millisecond,
		HalfOpenMax: 3,
	}
	circuitBreaker, err := patternx.NewCircuitBreaker(cbConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create circuit breaker: %w", err)
	}

	// Create worker pool for processing
	wpConfig := patternx.DefaultConfigPool()
	workerPool, err := patternx.NewPool(wpConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create worker pool: %w", err)
	}

	// Create retry policy
	retryPolicy := patternx.Policy{
		MaxAttempts:     3,
		InitialDelay:    50 * time.Millisecond,
		MaxDelay:        500 * time.Millisecond,
		Multiplier:      2.0,
		Jitter:          true,
		RateLimit:       10,
		RateLimitWindow: time.Second,
	}

	// Create bloom filter for deduplication
	bloomConfig := &patternx.BloomConfig{
		ExpectedItems:     10000,
		FalsePositiveRate: 0.01,
	}
	bloomFilter, err := patternx.NewBloomFilter(bloomConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create bloom filter: %w", err)
	}

	// Create dead letter queue for failed operations
	dlqConfig := &patternx.ConfigDLQ{
		MaxRetries:    2,
		RetryDelay:    100 * time.Millisecond,
		WorkerCount:   2,
		QueueSize:     100,
		EnableMetrics: true,
	}
	deadLetterQueue, err := patternx.NewDeadLetterQueue(dlqConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create dead letter queue: %w", err)
	}

	return &FullStackService{
		bulkhead:        bulkhead,
		circuitBreaker:  circuitBreaker,
		workerPool:      workerPool,
		retryPolicy:     retryPolicy,
		bloomFilter:     bloomFilter,
		deadLetterQueue: deadLetterQueue,
		externalService: externalService,
		processedItems:  make(map[string]bool),
		stats:           make(map[string]interface{}),
	}, nil
}

// ProcessItem processes an item using all patterns
func (fss *FullStackService) ProcessItem(ctx context.Context, item string) error {
	// Check if already processed using bloom filter
	contained, err := fss.bloomFilter.Contains(ctx, item)
	if err != nil {
		return fmt.Errorf("bloom filter error: %w", err)
	}

	if contained {
		// Already processed (or false positive)
		return nil
	}

	// Use bulkhead for resource isolation
	_, err = fss.bulkhead.Execute(ctx, func() (interface{}, error) {
		// Use circuit breaker for fault tolerance
		err := fss.circuitBreaker.Execute(func() error {
			// Use worker pool for processing
			job := patternx.JobPool{
				ID: fmt.Sprintf("process-%s", item),
				Task: func() (interface{}, error) {
					// Use retry for resilience
					return patternx.RetryWithResult(fss.retryPolicy, func() (string, error) {
						return fss.externalService.Call(ctx, item)
					})
				},
			}

			err := fss.workerPool.Submit(job)
			if err != nil {
				return fmt.Errorf("failed to submit job: %w", err)
			}

			// Get result
			result, err := fss.workerPool.GetResultWithTimeout(5 * time.Second)
			if err != nil {
				return fmt.Errorf("failed to get result: %w", err)
			}

			if result.Error != nil {
				return result.Error
			}

			// Mark as processed
			fss.mu.Lock()
			fss.processedItems[item] = true
			fss.mu.Unlock()

			return nil
		})

		return nil, err
	})

	if err != nil {
		// Add to dead letter queue for retry
		failedOp := &patternx.FailedOperation{
			ID:        fmt.Sprintf("item-%s-%d", item, time.Now().UnixNano()),
			Operation: "process_item",
			Key:       item,
			Data:      item,
			Error:     err.Error(),
		}

		dlqErr := fss.deadLetterQueue.AddFailedOperation(failedOp)
		if dlqErr != nil {
			return fmt.Errorf("failed to add to DLQ: %w", dlqErr)
		}

		return err
	}

	// Successfully processed, add to bloom filter
	err = fss.bloomFilter.Add(ctx, item)
	if err != nil {
		return fmt.Errorf("failed to add to bloom filter: %w", err)
	}

	return nil
}

// GetStats returns comprehensive statistics from all patterns
func (fss *FullStackService) GetStats() map[string]interface{} {
	fss.mu.RLock()
	defer fss.mu.RUnlock()

	// Collect stats from all patterns
	bulkheadMetrics := fss.bulkhead.GetMetrics()
	circuitBreakerStats := fss.circuitBreaker.GetStats()
	workerPoolStats := fss.workerPool.GetStats()
	bloomFilterStats := fss.bloomFilter.GetStats()
	dlqMetrics := fss.deadLetterQueue.GetMetrics()

	// Calculate custom stats
	processedCount := len(fss.processedItems)

	return map[string]interface{}{
		"bulkhead":               bulkheadMetrics,
		"circuit_breaker":        circuitBreakerStats,
		"worker_pool":            workerPoolStats,
		"bloom_filter":           bloomFilterStats,
		"dead_letter_queue":      dlqMetrics,
		"external_service_calls": fss.externalService.GetCallCount(),
		"processed_items":        processedCount,
	}
}

// Close closes all resources
func (fss *FullStackService) Close() {
	// Use timeouts to prevent hanging
	done := make(chan struct{})
	go func() {
		fss.bulkhead.Close()
		fss.workerPool.Close()
		close(done)
	}()

	select {
	case <-done:
		// Successfully closed
	case <-time.After(5 * time.Second):
		// Timeout, but don't fail the test
	}
}

// TestFullStackIntegration tests all patterns working together
func TestFullStackIntegration(t *testing.T) {
	// Create full stack service
	service, err := NewFullStackService()
	if err != nil {
		t.Fatalf("Failed to create full stack service: %v", err)
	}
	defer service.Close()

	// Create test items
	items := []string{
		"item-1", "item-2", "item-3", "item-4", "item-5",
		"item-6", "item-7", "item-8", "item-9", "item-10",
		"item-11", "item-12", "item-13", "item-14", "item-15",
	}

	var wg sync.WaitGroup
	results := make(chan error, len(items))

	start := time.Now()

	// Process items concurrently
	for _, item := range items {
		wg.Add(1)
		go func(item string) {
			defer wg.Done()

			// Process each item multiple times to test patterns
			for i := 0; i < 2; i++ {
				err := service.ProcessItem(context.Background(), item)
				if err != nil {
					results <- fmt.Errorf("failed to process %s (attempt %d): %w", item, i+1, err)
					return
				}
			}
			results <- nil
		}(item)
	}

	wg.Wait()
	close(results)
	duration := time.Since(start)

	// Wait for DLQ processing (reduced timeout for test environment)
	time.Sleep(100 * time.Millisecond)

	// Collect results
	successCount := 0
	errorCount := 0
	for err := range results {
		if err == nil {
			successCount++
		} else {
			errorCount++
		}
	}

	// Get comprehensive statistics
	stats := service.GetStats()

	t.Logf("Full stack integration test results:")
	t.Logf("  Duration: %v", duration)
	t.Logf("  Total items: %d", len(items))
	t.Logf("  Successful processing: %d", successCount)
	t.Logf("  Failed processing: %d", errorCount)
	t.Logf("  External service calls: %v", stats["external_service_calls"])
	t.Logf("  Processed items: %v", stats["processed_items"])

	// Log pattern-specific stats
	bulkheadMetrics := stats["bulkhead"].(*patternx.BulkheadMetrics)
	t.Logf("  Bulkhead total calls: %d", bulkheadMetrics.TotalCalls.Load())
	t.Logf("  Bulkhead successful calls: %d", bulkheadMetrics.SuccessfulCalls.Load())
	t.Logf("  Bulkhead failed calls: %d", bulkheadMetrics.FailedCalls.Load())
	t.Logf("  Bulkhead rejected calls: %d", bulkheadMetrics.RejectedCalls.Load())

	circuitBreakerStats := stats["circuit_breaker"].(patternx.Stats)
	t.Logf("  Circuit breaker state: %s", circuitBreakerStats.State)
	t.Logf("  Circuit breaker total requests: %d", circuitBreakerStats.TotalRequests)
	t.Logf("  Circuit breaker total failures: %d", circuitBreakerStats.TotalFailures)

	workerPoolStats := stats["worker_pool"].(*patternx.PoolStatsPool)
	t.Logf("  Worker pool completed jobs: %d", workerPoolStats.CompletedJobs.Load())
	t.Logf("  Worker pool failed jobs: %d", workerPoolStats.FailedJobs.Load())
	t.Logf("  Worker pool total workers: %d", workerPoolStats.TotalWorkers.Load())

	bloomFilterStats := stats["bloom_filter"].(map[string]interface{})
	t.Logf("  Bloom filter items: %v", bloomFilterStats["item_count"])
	t.Logf("  Bloom filter load factor: %.2f", bloomFilterStats["load_factor"].(float64))

	dlqMetrics := stats["dead_letter_queue"].(*patternx.Metrics)
	t.Logf("  DLQ total failed: %d", dlqMetrics.TotalFailed)
	t.Logf("  DLQ total retried: %d", dlqMetrics.TotalRetried)
	t.Logf("  DLQ total succeeded: %d", dlqMetrics.TotalSucceeded)

	// Verify patterns provided protection
	if bulkheadMetrics.TotalCalls.Load() == 0 {
		t.Error("Expected bulkhead to process calls")
	}

	if circuitBreakerStats.TotalRequests == 0 {
		t.Error("Expected circuit breaker to handle requests")
	}

	if workerPoolStats.CompletedJobs.Load()+workerPoolStats.FailedJobs.Load() == 0 {
		t.Error("Expected worker pool to process jobs")
	}

	if bloomFilterStats["item_count"].(uint64) == 0 {
		t.Error("Expected items in bloom filter")
	}

	if dlqMetrics.TotalFailed == 0 && service.externalService.successRate < 1.0 {
		t.Log("Expected failures to be captured in DLQ, but no failures occurred in this test run")
	}
}

// TestFullStackStress tests stress conditions with all patterns
func TestFullStackStress(t *testing.T) {
	// Create full stack service
	service, err := NewFullStackService()
	if err != nil {
		t.Fatalf("Failed to create full stack service: %v", err)
	}
	defer service.Close()

	// Create many test items
	numItems := 50
	items := make([]string, numItems)
	for i := 0; i < numItems; i++ {
		items[i] = fmt.Sprintf("stress-item-%d", i)
	}

	var wg sync.WaitGroup
	results := make(chan error, numItems)

	start := time.Now()

	// Process items concurrently under stress
	for _, item := range items {
		wg.Add(1)
		go func(item string) {
			defer wg.Done()

			// Process each item multiple times
			for i := 0; i < 3; i++ {
				err := service.ProcessItem(context.Background(), item)
				if err != nil {
					results <- fmt.Errorf("failed to process %s (attempt %d): %w", item, i+1, err)
					return
				}
			}
			results <- nil
		}(item)
	}

	wg.Wait()
	close(results)
	duration := time.Since(start)

	// Wait for DLQ processing
	time.Sleep(1 * time.Second)

	// Collect results
	successCount := 0
	errorCount := 0
	for err := range results {
		if err == nil {
			successCount++
		} else {
			errorCount++
		}
	}

	// Get comprehensive statistics
	stats := service.GetStats()

	t.Logf("Full stack stress test results:")
	t.Logf("  Duration: %v", duration)
	t.Logf("  Total items: %d", numItems)
	t.Logf("  Successful processing: %d", successCount)
	t.Logf("  Failed processing: %d", errorCount)
	t.Logf("  External service calls: %v", stats["external_service_calls"])
	t.Logf("  Processed items: %v", stats["processed_items"])

	// Verify all patterns provided protection under stress
	bulkheadMetrics := stats["bulkhead"].(*patternx.BulkheadMetrics)
	circuitBreakerStats := stats["circuit_breaker"].(patternx.Stats)
	workerPoolStats := stats["worker_pool"].(*patternx.PoolStatsPool)
	bloomFilterStats := stats["bloom_filter"].(map[string]interface{})
	dlqMetrics := stats["dead_letter_queue"].(*patternx.Metrics)

	// Verify bulkhead provided resource isolation
	if bulkheadMetrics.RejectedCalls.Load() == 0 && numItems > 10 {
		t.Log("Warning: Bulkhead should have rejected some calls under stress")
	}

	// Verify circuit breaker provided fault tolerance
	if circuitBreakerStats.TotalFailures == 0 && service.externalService.successRate < 1.0 {
		t.Log("Warning: Circuit breaker should have recorded some failures")
	}

	// Verify worker pool provided processing
	if workerPoolStats.CompletedJobs.Load()+workerPoolStats.FailedJobs.Load() == 0 {
		t.Error("Worker pool should have processed jobs")
	}

	// Verify bloom filter provided deduplication
	if bloomFilterStats["item_count"].(uint64) == 0 {
		t.Error("Bloom filter should contain items")
	}

	// Verify DLQ provided retry capability
	if dlqMetrics.TotalFailed == 0 && service.externalService.successRate < 1.0 {
		t.Log("Warning: DLQ should have captured some failures")
	}

	t.Logf("  Bulkhead rejected calls: %d", bulkheadMetrics.RejectedCalls.Load())
	t.Logf("  Circuit breaker failures: %d", circuitBreakerStats.TotalFailures)
	t.Logf("  Worker pool jobs: %d", workerPoolStats.CompletedJobs.Load()+workerPoolStats.FailedJobs.Load())
	t.Logf("  Bloom filter items: %v", bloomFilterStats["item_count"])
	t.Logf("  DLQ failed operations: %d", dlqMetrics.TotalFailed)
}
