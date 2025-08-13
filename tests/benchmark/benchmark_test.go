package benchmark

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/SeaSBee/go-patternx/patternx/bloom"
	"github.com/SeaSBee/go-patternx/patternx/bulkhead"
	"github.com/SeaSBee/go-patternx/patternx/cb"
	"github.com/SeaSBee/go-patternx/patternx/dlq"
	"github.com/SeaSBee/go-patternx/patternx/lock"
	"github.com/SeaSBee/go-patternx/patternx/pool"
	"github.com/SeaSBee/go-patternx/patternx/retry"
)

// BenchmarkBloomFilter tests bloom filter performance
func BenchmarkBloomFilter(b *testing.B) {
	config := &bloom.Config{
		ExpectedItems:     10000000, // Much larger capacity
		FalsePositiveRate: 0.01,
	}
	bf, err := bloom.NewBloomFilter(config)
	if err != nil {
		b.Fatalf("Failed to create bloom filter: %v", err)
	}
	defer bf.Close()

	b.ResetTimer()
	b.Run("Add", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			item := fmt.Sprintf("item_%d", i)
			err := bf.Add(context.Background(), item)
			if err != nil {
				b.Fatalf("Failed to add item: %v", err)
			}
		}
	})

	b.Run("Contains", func(b *testing.B) {
		// Pre-populate with some items
		for i := 0; i < 1000; i++ {
			item := fmt.Sprintf("item_%d", i)
			bf.Add(context.Background(), item)
		}

		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			item := fmt.Sprintf("item_%d", i%1000)
			bf.Contains(context.Background(), item)
		}
	})

	b.Run("AddBatch", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			items := make([]string, 100)
			for j := 0; j < 100; j++ {
				items[j] = fmt.Sprintf("batch_item_%d_%d", i, j)
			}
			err := bf.AddBatch(context.Background(), items)
			if err != nil {
				b.Fatalf("Failed to add batch: %v", err)
			}
		}
	})
}

// BenchmarkBulkhead tests bulkhead pattern performance
func BenchmarkBulkhead(b *testing.B) {
	config := bulkhead.BulkheadConfig{
		MaxConcurrentCalls: 10,
		MaxQueueSize:       100,
		MaxWaitDuration:    1 * time.Second,
		HealthThreshold:    0.5,
	}
	bh, err := bulkhead.NewBulkhead(config)
	if err != nil {
		b.Fatalf("Failed to create bulkhead: %v", err)
	}
	defer bh.Close()

	b.ResetTimer()
	b.Run("Execute", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			_, err := bh.Execute(context.Background(), func() (interface{}, error) {
				time.Sleep(1 * time.Millisecond)
				return "success", nil
			})
			if err != nil {
				b.Fatalf("Failed to execute: %v", err)
			}
		}
	})

	b.Run("ExecuteAsync", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			resultChan := bh.ExecuteAsync(context.Background(), func() (interface{}, error) {
				time.Sleep(1 * time.Millisecond)
				return "success", nil
			})
			result := <-resultChan
			if result.Error != nil {
				b.Fatalf("Failed to execute async: %v", result.Error)
			}
		}
	})

	b.Run("ConcurrentExecute", func(b *testing.B) {
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				_, err := bh.Execute(context.Background(), func() (interface{}, error) {
					time.Sleep(1 * time.Millisecond)
					return "success", nil
				})
				if err != nil {
					b.Fatalf("Failed to execute: %v", err)
				}
			}
		})
	})
}

// BenchmarkCircuitBreaker tests circuit breaker performance
func BenchmarkCircuitBreaker(b *testing.B) {
	config := cb.DefaultConfig()
	circuitBreaker, err := cb.New(config)
	if err != nil {
		b.Fatalf("Failed to create circuit breaker: %v", err)
	}

	b.ResetTimer()
	b.Run("ExecuteSuccess", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			err := circuitBreaker.Execute(func() error {
				time.Sleep(1 * time.Millisecond)
				return nil
			})
			if err != nil {
				b.Fatalf("Failed to execute: %v", err)
			}
		}
	})

	b.Run("ExecuteFailure", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			err := circuitBreaker.Execute(func() error {
				time.Sleep(1 * time.Millisecond)
				return fmt.Errorf("intentional failure")
			})
			if err == nil {
				b.Fatal("Expected error")
			}
		}
	})

	b.Run("ConcurrentExecute", func(b *testing.B) {
		// Reset circuit breaker state
		circuitBreaker.Reset()
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				err := circuitBreaker.Execute(func() error {
					time.Sleep(1 * time.Millisecond)
					return nil
				})
				if err != nil {
					b.Fatalf("Failed to execute: %v", err)
				}
			}
		})
	})
}

// BenchmarkDLQ tests dead letter queue performance
func BenchmarkDLQ(b *testing.B) {
	// Use custom config with larger queue sizes for benchmarks
	config := &dlq.Config{
		MaxRetries:    3,
		RetryDelay:    10 * time.Millisecond,
		WorkerCount:   10,    // More workers for benchmarks
		QueueSize:     10000, // Much larger queue for benchmarks
		EnableMetrics: true,
	}
	dq, err := dlq.NewDeadLetterQueue(config)
	if err != nil {
		b.Fatalf("Failed to create DLQ: %v", err)
	}
	defer dq.Close()

	b.ResetTimer()
	b.Run("AddFailedOperation", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			err := dq.AddFailedOperation(&dlq.FailedOperation{
				HandlerType: "test_handler",
				Data:        fmt.Sprintf("data_%d", i),
				Error:       "test error",
				Operation:   "test_operation",
				Key:         fmt.Sprintf("key_%d", i),
			})
			if err != nil {
				b.Fatalf("Failed to add operation: %v", err)
			}
		}
	})

	b.Run("ConcurrentAdd", func(b *testing.B) {
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				err := dq.AddFailedOperation(&dlq.FailedOperation{
					HandlerType: "test_handler",
					Data:        fmt.Sprintf("data_%d", i),
					Error:       "test error",
					Operation:   "test_operation",
					Key:         fmt.Sprintf("key_%d", i),
				})
				if err != nil {
					b.Fatalf("Failed to add operation: %v", err)
				}
				i++
			}
		})
	})
}

// BenchmarkRedlock tests redlock performance
func BenchmarkRedlock(b *testing.B) {
	clients := []lock.LockClient{
		NewMockLockClient(false, 0),
		NewMockLockClient(false, 0),
		NewMockLockClient(false, 0),
	}

	config := &lock.Config{
		Clients:       clients,
		Quorum:        2,
		RetryDelay:    10 * time.Millisecond,
		MaxRetries:    3,
		DriftFactor:   0.01,
		EnableMetrics: true,
	}

	rl, err := lock.NewRedlock(config)
	if err != nil {
		b.Fatalf("Failed to create Redlock: %v", err)
	}

	b.ResetTimer()
	b.Run("Lock", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			lock, err := rl.Lock(context.Background(), fmt.Sprintf("resource_%d", i), 1*time.Second)
			if err != nil {
				b.Fatalf("Failed to acquire lock: %v", err)
			}
			if lock != nil {
				lock.Unlock(context.Background())
			}
		}
	})

	b.Run("TryLock", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			lock, err := rl.TryLock(context.Background(), fmt.Sprintf("resource_%d", i), 1*time.Second)
			if err != nil {
				b.Fatalf("Failed to try lock: %v", err)
			}
			if lock != nil {
				lock.Unlock(context.Background())
			}
		}
	})

	b.Run("ConcurrentLock", func(b *testing.B) {
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				lock, err := rl.Lock(context.Background(), fmt.Sprintf("resource_%d", i), 1*time.Second)
				if err != nil {
					b.Fatalf("Failed to acquire lock: %v", err)
				}
				if lock != nil {
					lock.Unlock(context.Background())
				}
				i++
			}
		})
	})
}

// BenchmarkWorkerPool tests worker pool performance
func BenchmarkWorkerPool(b *testing.B) {
	// Use custom config with larger queue sizes for benchmarks
	config := pool.Config{
		MinWorkers:         5,
		MaxWorkers:         20,    // More workers for benchmarks
		QueueSize:          10000, // Maximum allowed queue size for benchmarks
		IdleTimeout:        30 * time.Second,
		ScaleUpThreshold:   100, // High threshold to prevent scaling during benchmarks
		ScaleDownThreshold: 50,
		ScaleUpCooldown:    1 * time.Second,
		ScaleDownCooldown:  5 * time.Second,
		EnableMetrics:      true,
	}

	wp, err := pool.New(config)
	if err != nil {
		b.Fatalf("Failed to create worker pool: %v", err)
	}
	defer wp.Close()

	b.ResetTimer()
	b.Run("Submit", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			job := pool.Job{
				ID:   fmt.Sprintf("job_%d", i),
				Task: func() (interface{}, error) { return "success", nil },
			}
			err := wp.Submit(job)
			if err != nil {
				// Skip if queue is full (expected in high-load benchmarks)
				continue
			}
		}
	})

	b.Run("SubmitWithTimeout", func(b *testing.B) {
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			job := pool.Job{
				ID:      fmt.Sprintf("job_%d", i),
				Task:    func() (interface{}, error) { return "success", nil },
				Timeout: 1 * time.Second,
			}
			err := wp.Submit(job)
			if err != nil {
				// Skip if queue is full (expected in high-load benchmarks)
				continue
			}
		}
	})

	b.Run("ConcurrentSubmit", func(b *testing.B) {
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			i := 0
			for pb.Next() {
				job := pool.Job{
					ID:   fmt.Sprintf("job_%d", i),
					Task: func() (interface{}, error) { return "success", nil },
				}
				err := wp.Submit(job)
				if err != nil {
					// Skip if queue is full (expected in high-load benchmarks)
					continue
				}
				i++
			}
		})
	})
}

// BenchmarkRetry tests retry pattern performance
func BenchmarkRetry(b *testing.B) {
	policy := retry.DefaultPolicy()

	b.ResetTimer()
	b.Run("RetrySuccess", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			err := retry.Retry(policy, func() error {
				time.Sleep(1 * time.Millisecond)
				return nil
			})
			if err != nil {
				b.Fatalf("Failed to retry: %v", err)
			}
		}
	})

	b.Run("RetryFailure", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			err := retry.Retry(policy, func() error {
				time.Sleep(1 * time.Millisecond)
				return fmt.Errorf("intentional failure")
			})
			if err == nil {
				b.Fatal("Expected error")
			}
		}
	})

	b.Run("RetryWithResult", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			result, err := retry.RetryWithResult[string](policy, func() (string, error) {
				time.Sleep(1 * time.Millisecond)
				return "success", nil
			})
			if err != nil {
				b.Fatalf("Failed to retry with result: %v", err)
			}
			if result != "success" {
				b.Fatal("Expected success result")
			}
		}
	})

	b.Run("ConcurrentRetry", func(b *testing.B) {
		b.ResetTimer()
		b.RunParallel(func(pb *testing.PB) {
			for pb.Next() {
				err := retry.Retry(policy, func() error {
					time.Sleep(1 * time.Millisecond)
					return nil
				})
				if err != nil {
					b.Fatalf("Failed to retry: %v", err)
				}
			}
		})
	})
}

// BenchmarkIntegration tests integration performance
func BenchmarkIntegration(b *testing.B) {
	// Create all patterns
	bloomConfig := &bloom.Config{
		ExpectedItems:     10000,
		FalsePositiveRate: 0.01,
	}
	bf, err := bloom.NewBloomFilter(bloomConfig)
	if err != nil {
		b.Fatalf("Failed to create bloom filter: %v", err)
	}
	defer bf.Close()

	bulkheadConfig := bulkhead.BulkheadConfig{
		MaxConcurrentCalls: 5,
		MaxQueueSize:       50,
		MaxWaitDuration:    1 * time.Second,
		HealthThreshold:    0.5,
	}
	bh, err := bulkhead.NewBulkhead(bulkheadConfig)
	if err != nil {
		b.Fatalf("Failed to create bulkhead: %v", err)
	}
	defer bh.Close()

	cbConfig := cb.DefaultConfig()
	circuitBreaker, err := cb.New(cbConfig)
	if err != nil {
		b.Fatalf("Failed to create circuit breaker: %v", err)
	}

	dlqConfig := dlq.DefaultConfig()
	dq, err := dlq.NewDeadLetterQueue(dlqConfig)
	if err != nil {
		b.Fatalf("Failed to create DLQ: %v", err)
	}
	defer dq.Close()

	poolConfig := pool.DefaultConfig()
	poolConfig.MinWorkers = 3
	poolConfig.MaxWorkers = 5
	poolConfig.QueueSize = 50
	wp, err := pool.New(poolConfig)
	if err != nil {
		b.Fatalf("Failed to create worker pool: %v", err)
	}
	defer wp.Close()

	retryPolicy := retry.DefaultPolicy()

	b.ResetTimer()
	b.Run("FullStack", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			// Use bulkhead to limit concurrency
			_, err := bh.Execute(context.Background(), func() (interface{}, error) {
				// Use circuit breaker for external service call
				err := circuitBreaker.Execute(func() error {
					// Use retry for resilience
					return retry.Retry(retryPolicy, func() error {
						// Use worker pool for processing
						job := pool.Job{
							ID:   fmt.Sprintf("integration_job_%d", i),
							Task: func() (interface{}, error) { return "processed", nil },
						}
						err := wp.Submit(job)
						if err != nil {
							return err
						}

						// Use bloom filter for caching
						item := fmt.Sprintf("cache_item_%d", i)
						err = bf.Add(context.Background(), item)
						if err != nil {
							return err
						}

						// Check if item exists
						exists, err := bf.Contains(context.Background(), item)
						if err != nil {
							return err
						}
						if !exists {
							return fmt.Errorf("item not found in bloom filter")
						}

						return nil
					})
				})
				return "success", err
			})

			if err != nil {
				// Add to DLQ if operation fails
				dq.AddFailedOperation(&dlq.FailedOperation{
					HandlerType: "integration_handler",
					Data:        fmt.Sprintf("failed_operation_%d", i),
					Error:       err.Error(),
					Operation:   "integration_operation",
					Key:         fmt.Sprintf("failed_key_%d", i),
				})
			}
		}
	})
}

// MockLockClient for benchmarking
type MockLockClient struct {
	shouldFail bool
	delay      time.Duration
}

func NewMockLockClient(shouldFail bool, delay time.Duration) *MockLockClient {
	return &MockLockClient{
		shouldFail: shouldFail,
		delay:      delay,
	}
}

func (m *MockLockClient) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	if m.shouldFail {
		return fmt.Errorf("mock failure")
	}
	return nil
}

func (m *MockLockClient) Get(ctx context.Context, key string) ([]byte, error) {
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	if m.shouldFail {
		return nil, fmt.Errorf("mock failure")
	}
	return []byte("mock_value"), nil
}

func (m *MockLockClient) Del(ctx context.Context, key string) error {
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	if m.shouldFail {
		return fmt.Errorf("mock failure")
	}
	return nil
}

func (m *MockLockClient) Eval(ctx context.Context, script string, keys []string, args ...interface{}) (interface{}, error) {
	if m.delay > 0 {
		time.Sleep(m.delay)
	}
	if m.shouldFail {
		return nil, fmt.Errorf("mock failure")
	}
	return int64(1), nil
}
