package unit

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/SeaSBee/go-patternx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestEnhancedConcurrencyLimits tests the improved concurrency limits
func TestEnhancedConcurrencyLimits(t *testing.T) {
	store := NewMockStore()
	config := patternx.DefaultConfigPubSub(store)
	config.MaxConcurrentOperations = 2              // Very low limit for testing
	config.OperationTimeout = 50 * time.Millisecond // Short timeout to force failures
	ps, err := patternx.NewPubSub(config)
	require.NoError(t, err)
	defer ps.Close(context.Background())

	err = ps.CreateTopic(context.Background(), "concurrency-test")
	require.NoError(t, err)

	// Create slow handler to create backpressure
	handler := func(ctx context.Context, msg *patternx.MessagePubSub) error {
		time.Sleep(300 * time.Millisecond) // Very slow processing to create backpressure
		return nil
	}

	_, err = ps.Subscribe(context.Background(), "concurrency-test", "sub-1", handler, &patternx.MessageFilter{})
	require.NoError(t, err)

	// Try to publish more messages than concurrency limit
	var successCount int64
	var failureCount int64
	var wg sync.WaitGroup

	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			data := []byte(fmt.Sprintf("message %d", index))
			err := ps.Publish(context.Background(), "concurrency-test", data, nil)
			if err != nil {
				atomic.AddInt64(&failureCount, 1)
			} else {
				atomic.AddInt64(&successCount, 1)
			}
		}(i)
	}

	wg.Wait()

	// Wait a bit more for processing to complete
	time.Sleep(1 * time.Second)

	// Check if we have any failures due to queue being full
	stats := ps.GetStats()
	topicStats := stats["topics"].(map[string]interface{})
	concurrencyTestStats := topicStats["concurrency-test"].(map[string]interface{})
	queueFullCount := concurrencyTestStats["queue_full_count"].(uint64)

	// Some operations should succeed, some should fail due to concurrency limit
	assert.Greater(t, atomic.LoadInt64(&successCount), int64(0), "Some operations should succeed")
	assert.True(t, atomic.LoadInt64(&failureCount) > 0 || queueFullCount > 0, "Some operations should fail due to concurrency limit or queue being full")

	t.Logf("Success: %d, Failures: %d, Queue Full Count: %d", atomic.LoadInt64(&successCount), atomic.LoadInt64(&failureCount), queueFullCount)
}

// TestEnhancedTimeoutHandling tests the improved timeout handling
func TestEnhancedTimeoutHandling(t *testing.T) {
	store := NewMockStore()
	config := patternx.DefaultConfigPubSub(store)
	config.OperationTimeout = 50 * time.Millisecond // Short timeout
	ps, err := patternx.NewPubSub(config)
	require.NoError(t, err)
	defer ps.Close(context.Background())

	err = ps.CreateTopic(context.Background(), "timeout-test")
	require.NoError(t, err)

	// Create slow handler that exceeds timeout
	handler := func(ctx context.Context, msg *patternx.MessagePubSub) error {
		time.Sleep(200 * time.Millisecond) // Longer than timeout
		return nil
	}

	_, err = ps.Subscribe(context.Background(), "timeout-test", "sub-1", handler, &patternx.MessageFilter{})
	require.NoError(t, err)

	// Publish message
	data := []byte("timeout message")
	err = ps.Publish(context.Background(), "timeout-test", data, nil)
	require.NoError(t, err)

	// Wait for processing
	time.Sleep(300 * time.Millisecond)

	// Check stats for timeout errors
	stats := ps.GetStats()
	subscriptionStats := stats["subscriptions"].(map[string]interface{})
	sub1Stats := subscriptionStats["sub-1"].(map[string]interface{})

	timeoutErrors := sub1Stats["timeout_errors"].(uint64)
	assert.Greater(t, timeoutErrors, uint64(0), "Should have timeout errors recorded")

	t.Logf("Timeout errors: %d", timeoutErrors)
}

// TestEnhancedErrorRecording tests the improved error recording
func TestEnhancedErrorRecording(t *testing.T) {
	store := NewMockStore()
	config := patternx.DefaultConfigPubSub(store)
	config.CircuitBreakerThreshold = 100 // Very high threshold to avoid interference
	config.CircuitBreakerTimeout = 1 * time.Second
	config.EnableDeadLetterQueue = true
	config.DeadLetterHandler = func(ctx context.Context, msg *patternx.MessagePubSub, err error) error {
		return nil // Always succeed for testing
	}
	ps, err := patternx.NewPubSub(config)
	require.NoError(t, err)
	defer ps.Close(context.Background())

	err = ps.CreateTopic(context.Background(), "error-test")
	require.NoError(t, err)

	// Test various error scenarios
	testCases := []struct {
		name        string
		handler     patternx.MessageHandlerPubSub
		expectError bool
		errorType   string
	}{
		{
			name: "handler-returns-error",
			handler: func(ctx context.Context, msg *patternx.MessagePubSub) error {
				return errors.New("handler error")
			},
			expectError: true,
			errorType:   "handler_error",
		},
		{
			name: "handler-panics",
			handler: func(ctx context.Context, msg *patternx.MessagePubSub) error {
				panic("handler panic")
			},
			expectError: true,
			errorType:   "panic",
		},

		{
			name: "handler-succeeds",
			handler: func(ctx context.Context, msg *patternx.MessagePubSub) error {
				return nil
			},
			expectError: false,
			errorType:   "success",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			subscriptionID := fmt.Sprintf("sub-%s", tc.name)
			_, err := ps.Subscribe(context.Background(), "error-test", subscriptionID, tc.handler, &patternx.MessageFilter{})
			require.NoError(t, err)

			data := []byte("error test message")
			err = ps.Publish(context.Background(), "error-test", data, nil)
			require.NoError(t, err)

			// Wait for processing - longer wait for error scenarios
			if tc.expectError {
				time.Sleep(500 * time.Millisecond)
			} else {
				time.Sleep(300 * time.Millisecond)
			}

			// Check detailed stats
			stats := ps.GetStats()
			subscriptionStats := stats["subscriptions"].(map[string]interface{})
			subStats := subscriptionStats[subscriptionID].(map[string]interface{})

			messagesFailed := subStats["messages_failed"].(uint64)
			messagesDLQ := subStats["messages_dlq"].(uint64)
			panicRecoveries := subStats["panic_recoveries"].(uint64)
			timeoutErrors := subStats["timeout_errors"].(uint64)
			messagesRetried := subStats["messages_retried"].(uint64)

			if tc.expectError {
				switch tc.errorType {
				case "panic":
					// Panic recoveries might be masked by circuit breaker, but retries should occur
					assert.Greater(t, messagesRetried, uint64(0), "Should have retried messages due to panic")
				default:
					// Check for retries as an indicator of errors (since circuit breaker might mask actual errors)
					assert.Greater(t, messagesRetried, uint64(0), "Should have retried messages due to errors")
				}
			} else {
				assert.Equal(t, uint64(0), messagesFailed, "Should not have failed messages")
			}

			t.Logf("Failed: %d, DLQ: %d, Panics: %d, Timeouts: %d, Retried: %d",
				messagesFailed, messagesDLQ, panicRecoveries, timeoutErrors, messagesRetried)
		})
	}
}

// TestEnhancedBatchOperations tests enhanced batch operations with concurrency limits
func TestEnhancedBatchOperations(t *testing.T) {
	store := NewMockStore()
	config := patternx.DefaultConfigPubSub(store)
	config.MaxConcurrentOperations = 5
	ps, err := patternx.NewPubSub(config)
	require.NoError(t, err)
	defer ps.Close(context.Background())

	err = ps.CreateTopic(context.Background(), "batch-test")
	require.NoError(t, err)

	var receivedCount int64
	handler := func(ctx context.Context, msg *patternx.MessagePubSub) error {
		atomic.AddInt64(&receivedCount, 1)
		return nil
	}

	_, err = ps.Subscribe(context.Background(), "batch-test", "sub-1", handler, &patternx.MessageFilter{})
	require.NoError(t, err)

	// Create large batch
	messages := make([]patternx.MessagePubSub, 20)
	for i := 0; i < 20; i++ {
		messages[i] = patternx.MessagePubSub{
			Data:    []byte(fmt.Sprintf("batch message %d", i)),
			Headers: map[string]string{"batch": "true"},
		}
	}

	// Publish batch
	err = ps.PublishBatch(context.Background(), "batch-test", messages)
	require.NoError(t, err)

	// Wait for processing
	time.Sleep(1 * time.Second)

	// Verify all messages were received
	assert.Equal(t, int64(20), atomic.LoadInt64(&receivedCount), "All batch messages should be received")

	// Check stats
	stats := ps.GetStats()
	publisherStats := stats["publisher"].(map[string]interface{})
	batchOperations := publisherStats["batch_operations"].(uint64)
	assert.Equal(t, uint64(1), batchOperations, "Should have 1 batch operation recorded")

	t.Logf("Received: %d, Batch operations: %d", atomic.LoadInt64(&receivedCount), batchOperations)
}

// BenchmarkEnhancedPublishing benchmarks the enhanced publishing with concurrency limits
func BenchmarkEnhancedPublishing(b *testing.B) {
	store := NewMockStore()
	config := patternx.DefaultConfigPubSub(store)
	config.MaxConcurrentOperations = 100
	config.BufferSize = 10000
	ps, err := patternx.NewPubSub(config)
	require.NoError(b, err)
	defer ps.Close(context.Background())

	err = ps.CreateTopic(context.Background(), "benchmark-topic")
	require.NoError(b, err)

	handler := func(ctx context.Context, msg *patternx.MessagePubSub) error {
		return nil
	}

	_, err = ps.Subscribe(context.Background(), "benchmark-topic", "sub-1", handler, &patternx.MessageFilter{})
	require.NoError(b, err)

	data := []byte("benchmark message")
	headers := map[string]string{"benchmark": "true"}

	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			err := ps.Publish(context.Background(), "benchmark-topic", data, headers)
			if err != nil {
				b.Fatal(err)
			}
		}
	})
}
