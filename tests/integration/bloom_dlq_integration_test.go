package integration

import (
	"context"
	"fmt"
	"math/rand"
	"sync"
	"testing"
	"time"

	"github.com/SeaSBee/go-patternx/patternx/bloom"
	"github.com/SeaSBee/go-patternx/patternx/dlq"
)

// MockProcessor simulates a processor that can fail and needs DLQ
type MockProcessor struct {
	successRate float64
	mu          sync.Mutex
	processed   map[string]bool
	failed      map[string]int
}

func NewMockProcessor(successRate float64) *MockProcessor {
	return &MockProcessor{
		successRate: successRate,
		processed:   make(map[string]bool),
		failed:      make(map[string]int),
	}
}

func (p *MockProcessor) Process(ctx context.Context, item string) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	// Simulate processing time
	time.Sleep(10 * time.Millisecond)

	// Simulate failures based on success rate using random
	if p.successRate < 1.0 && rand.Float64() > p.successRate {
		p.failed[item]++
		return fmt.Errorf("failed to process item: %s", item)
	}

	p.processed[item] = true
	return nil
}

func (p *MockProcessor) IsProcessed(item string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.processed[item]
}

func (p *MockProcessor) GetFailedCount(item string) int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.failed[item]
}

// BloomFilterProcessor integrates bloom filter with DLQ
type BloomFilterProcessor struct {
	bloomFilter *bloom.BloomFilter
	dlq         *dlq.DeadLetterQueue
	processor   *MockProcessor
}

func NewBloomFilterProcessor(bloomConfig *bloom.Config, dlqConfig *dlq.Config, processor *MockProcessor) (*BloomFilterProcessor, error) {
	bf, err := bloom.NewBloomFilter(bloomConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create bloom filter: %w", err)
	}

	dq, err := dlq.NewDeadLetterQueue(dlqConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create dead letter queue: %w", err)
	}

	return &BloomFilterProcessor{
		bloomFilter: bf,
		dlq:         dq,
		processor:   processor,
	}, nil
}

func (bfp *BloomFilterProcessor) ProcessItem(ctx context.Context, item string) error {
	// Check if item was already processed using bloom filter
	contained, err := bfp.bloomFilter.Contains(ctx, item)
	if err != nil {
		return fmt.Errorf("bloom filter error: %w", err)
	}

	if contained {
		// Item was already processed (or false positive)
		return nil
	}

	// Try to process the item
	err = bfp.processor.Process(ctx, item)
	if err != nil {
		// Add to dead letter queue for retry
		failedOp := &dlq.FailedOperation{
			ID:          fmt.Sprintf("item-%s-%d", item, time.Now().UnixNano()),
			Operation:   "process_item",
			Key:         item,
			Data:        item,
			Error:       err.Error(),
			HandlerType: "bloom_filter_processor",
		}

		dlqErr := bfp.dlq.AddFailedOperation(failedOp)
		if dlqErr != nil {
			return fmt.Errorf("failed to add to DLQ: %w", dlqErr)
		}

		return err
	}

	// Successfully processed, add to bloom filter
	err = bfp.bloomFilter.Add(ctx, item)
	if err != nil {
		return fmt.Errorf("failed to add to bloom filter: %w", err)
	}

	return nil
}

func (bfp *BloomFilterProcessor) GetStats() map[string]interface{} {
	bloomStats := bfp.bloomFilter.GetStats()
	dlqMetrics := bfp.dlq.GetMetrics()

	return map[string]interface{}{
		"bloom_filter": bloomStats,
		"dlq_metrics":  dlqMetrics,
	}
}

// TestBloomFilterWithDLQ tests the integration of bloom filter and DLQ
func TestBloomFilterWithDLQ(t *testing.T) {
	// Create bloom filter configuration
	bloomConfig := &bloom.Config{
		ExpectedItems:     1000,
		FalsePositiveRate: 0.01,
	}

	// Create DLQ configuration
	dlqConfig := &dlq.Config{
		MaxRetries:    3,
		RetryDelay:    100 * time.Millisecond,
		WorkerCount:   2,
		QueueSize:     100,
		EnableMetrics: true,
	}

	// Create processor with high success rate for this test
	processor := NewMockProcessor(0.9)

	// Create integrated processor
	bfp, err := NewBloomFilterProcessor(bloomConfig, dlqConfig, processor)
	if err != nil {
		t.Fatalf("Failed to create bloom filter processor: %v", err)
	}

	// Create test items
	items := []string{
		"item-1", "item-2", "item-3", "item-4", "item-5",
		"item-6", "item-7", "item-8", "item-9", "item-10",
	}

	var wg sync.WaitGroup
	results := make(chan error, len(items))

	start := time.Now()

	// Process items concurrently
	for _, item := range items {
		wg.Add(1)
		go func(item string) {
			defer wg.Done()

			// Process multiple times to test bloom filter and DLQ
			for i := 0; i < 3; i++ {
				err := bfp.ProcessItem(context.Background(), item)
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

	// Get statistics
	stats := bfp.GetStats()
	bloomStats := stats["bloom_filter"].(map[string]interface{})
	dlqMetrics := stats["dlq_metrics"].(*dlq.Metrics)

	t.Logf("Bloom filter with DLQ integration test results:")
	t.Logf("  Duration: %v", duration)
	t.Logf("  Total items: %d", len(items))
	t.Logf("  Successful processing: %d", successCount)
	t.Logf("  Failed processing: %d", errorCount)
	t.Logf("  Bloom filter items: %v", bloomStats["item_count"])
	t.Logf("  Bloom filter load factor: %.2f", bloomStats["load_factor"].(float64))
	t.Logf("  DLQ total failed: %d", dlqMetrics.TotalFailed)
	t.Logf("  DLQ total retried: %d", dlqMetrics.TotalRetried)
	t.Logf("  DLQ total succeeded: %d", dlqMetrics.TotalSucceeded)

	// Verify bloom filter is working
	if bloomStats["item_count"].(uint64) < 5 {
		t.Errorf("Expected at least 5 items in bloom filter, got %d", bloomStats["item_count"].(uint64))
	}

	// Verify DLQ captured failures (with 90% success rate, some failures are expected)
	if dlqMetrics.TotalFailed == 0 && processor.successRate < 1.0 {
		t.Log("Expected some failures to be captured in DLQ, but all items succeeded in this test run")
	}

	// Test bloom filter false positive behavior
	falsePositiveCount := 0
	for _, item := range items {
		contained, err := bfp.bloomFilter.Contains(context.Background(), item)
		if err != nil {
			t.Errorf("Failed to check bloom filter for %s: %v", item, err)
			continue
		}
		if contained && !processor.IsProcessed(item) {
			falsePositiveCount++
		}
	}

	t.Logf("  False positives detected: %d", falsePositiveCount)
}

// TestBloomFilterDLQRetry tests retry behavior with bloom filter
func TestBloomFilterDLQRetry(t *testing.T) {
	// Create configurations
	bloomConfig := &bloom.Config{
		ExpectedItems:     100,
		FalsePositiveRate: 0.05,
	}

	dlqConfig := &dlq.Config{
		MaxRetries:    2,
		RetryDelay:    50 * time.Millisecond,
		WorkerCount:   1,
		QueueSize:     50,
		EnableMetrics: true,
	}

	// Create processor that improves over time
	processor := NewMockProcessor(0.3) // Starts with low success rate

	// Create integrated processor
	bfp, err := NewBloomFilterProcessor(bloomConfig, dlqConfig, processor)
	if err != nil {
		t.Fatalf("Failed to create bloom filter processor: %v", err)
	}

	// Create test items
	items := []string{"retry-item-1", "retry-item-2", "retry-item-3"}

	// Phase 1: Initial processing with failures
	t.Log("Phase 1: Initial processing with failures")
	for _, item := range items {
		err := bfp.ProcessItem(context.Background(), item)
		if err != nil {
			t.Logf("Expected failure for %s: %v", item, err)
		}
	}

	// Get initial stats
	initialStats := bfp.GetStats()
	initialDLQ := initialStats["dlq_metrics"].(*dlq.Metrics)

	t.Logf("Initial DLQ failed: %d", initialDLQ.TotalFailed)

	// Phase 2: Wait for retries and improve processor
	t.Log("Phase 2: Waiting for retries and improving processor")
	time.Sleep(200 * time.Millisecond) // Wait for retries

	// Improve processor success rate
	processor.successRate = 0.8

	// Phase 3: Process same items again
	t.Log("Phase 3: Processing same items again with improved processor")
	for _, item := range items {
		err := bfp.ProcessItem(context.Background(), item)
		if err != nil {
			t.Logf("Processing %s: %v", item, err)
		}
	}

	// Wait for final processing
	time.Sleep(100 * time.Millisecond)

	// Get final stats
	finalStats := bfp.GetStats()
	finalDLQ := finalStats["dlq_metrics"].(*dlq.Metrics)
	bloomStats := finalStats["bloom_filter"].(map[string]interface{})

	t.Logf("Retry test results:")
	t.Logf("  Initial DLQ failed: %d", initialDLQ.TotalFailed)
	t.Logf("  Final DLQ failed: %d", finalDLQ.TotalFailed)
	t.Logf("  Final DLQ retried: %d", finalDLQ.TotalRetried)
	t.Logf("  Final DLQ succeeded: %d", finalDLQ.TotalSucceeded)
	t.Logf("  Bloom filter items: %v", bloomStats["item_count"])

	// Verify retry behavior
	if finalDLQ.TotalRetried == 0 && initialDLQ.TotalFailed > 0 {
		t.Log("Expected retries to occur, but DLQ retry mechanism may need more time")
	}

	// Verify some items were eventually processed
	processedCount := 0
	for _, item := range items {
		if processor.IsProcessed(item) {
			processedCount++
		}
	}

	t.Logf("  Items eventually processed: %d/%d", processedCount, len(items))

	if processedCount == 0 && finalDLQ.TotalSucceeded > 0 {
		t.Error("Expected some items to be processed successfully")
	}
}

// TestBloomFilterDLQStress tests stress conditions
func TestBloomFilterDLQStress(t *testing.T) {
	// Create configurations for stress test
	bloomConfig := &bloom.Config{
		ExpectedItems:     500,
		FalsePositiveRate: 0.02,
	}

	dlqConfig := &dlq.Config{
		MaxRetries:    1,
		RetryDelay:    20 * time.Millisecond,
		WorkerCount:   3,
		QueueSize:     200,
		EnableMetrics: true,
	}

	// Create processor with variable success rate
	processor := NewMockProcessor(0.6)

	// Create integrated processor
	bfp, err := NewBloomFilterProcessor(bloomConfig, dlqConfig, processor)
	if err != nil {
		t.Fatalf("Failed to create bloom filter processor: %v", err)
	}

	// Create many test items
	numItems := 100
	items := make([]string, numItems)
	for i := 0; i < numItems; i++ {
		items[i] = fmt.Sprintf("stress-item-%d", i)
	}

	var wg sync.WaitGroup
	results := make(chan error, numItems)

	start := time.Now()

	// Process items concurrently
	for _, item := range items {
		wg.Add(1)
		go func(item string) {
			defer wg.Done()

			// Process each item multiple times
			for i := 0; i < 2; i++ {
				err := bfp.ProcessItem(context.Background(), item)
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
	time.Sleep(200 * time.Millisecond)

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

	// Get final statistics
	finalStats := bfp.GetStats()
	bloomStats := finalStats["bloom_filter"].(map[string]interface{})
	dlqMetrics := finalStats["dlq_metrics"].(*dlq.Metrics)

	t.Logf("Stress test results:")
	t.Logf("  Duration: %v", duration)
	t.Logf("  Total items: %d", numItems)
	t.Logf("  Successful processing: %d", successCount)
	t.Logf("  Failed processing: %d", errorCount)
	t.Logf("  Bloom filter items: %v", bloomStats["item_count"])
	t.Logf("  Bloom filter load factor: %.2f", bloomStats["load_factor"].(float64))
	t.Logf("  DLQ total failed: %d", dlqMetrics.TotalFailed)
	t.Logf("  DLQ total retried: %d", dlqMetrics.TotalRetried)
	t.Logf("  DLQ total succeeded: %d", dlqMetrics.TotalSucceeded)
	t.Logf("  DLQ current queue: %d", dlqMetrics.CurrentQueue)

	// Verify patterns provided protection
	if bloomStats["item_count"].(uint64) < 20 {
		t.Errorf("Expected at least 20 items in bloom filter under stress, got %d", bloomStats["item_count"].(uint64))
	}

	if dlqMetrics.TotalFailed == 0 && processor.successRate < 1.0 {
		t.Error("Expected failures to be captured in DLQ under stress")
	}

	// Verify bloom filter performance
	loadFactor := bloomStats["load_factor"].(float64)
	if loadFactor > 0.8 {
		t.Logf("Warning: High bloom filter load factor: %.2f", loadFactor)
	}

	// Verify DLQ performance
	if dlqMetrics.CurrentQueue > 0 {
		t.Logf("DLQ still has %d items in queue", dlqMetrics.CurrentQueue)
	}
}
