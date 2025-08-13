package unit

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SeaSBee/go-patternx/patternx/pubsub"
)

// TestEnhancedConcurrencyLimits tests the improved concurrency limits
func TestEnhancedConcurrencyLimits(t *testing.T) {
	store := NewMockStore()
	config := pubsub.DefaultConfig(store)
	config.MaxConcurrentOperations = 3 // Very low limit for testing
	config.OperationTimeout = 100 * time.Millisecond
	ps, err := pubsub.NewPubSub(config)
	require.NoError(t, err)
	defer ps.Close(context.Background())

	err = ps.CreateTopic(context.Background(), "concurrency-test")
	require.NoError(t, err)

	// Create slow handler to create backpressure
	handler := func(ctx context.Context, msg *pubsub.Message) error {
		time.Sleep(50 * time.Millisecond) // Slow processing
		return nil
	}

	_, err = ps.Subscribe(context.Background(), "concurrency-test", "sub-1", handler, &pubsub.MessageFilter{})
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

	// Some operations should succeed, some should fail due to concurrency limit
	assert.Greater(t, atomic.LoadInt64(&successCount), int64(0), "Some operations should succeed")
	assert.Greater(t, atomic.LoadInt64(&failureCount), int64(0), "Some operations should fail due to concurrency limit")

	t.Logf("Success: %d, Failures: %d", atomic.LoadInt64(&successCount), atomic.LoadInt64(&failureCount))
}

// TestEnhancedTimeoutHandling tests the improved timeout handling
func TestEnhancedTimeoutHandling(t *testing.T) {
	store := NewMockStore()
	config := pubsub.DefaultConfig(store)
	config.OperationTimeout = 50 * time.Millisecond // Short timeout
	ps, err := pubsub.NewPubSub(config)
	require.NoError(t, err)
	defer ps.Close(context.Background())

	err = ps.CreateTopic(context.Background(), "timeout-test")
	require.NoError(t, err)

	// Create slow handler that exceeds timeout
	handler := func(ctx context.Context, msg *pubsub.Message) error {
		time.Sleep(200 * time.Millisecond) // Longer than timeout
		return nil
	}

	_, err = ps.Subscribe(context.Background(), "timeout-test", "sub-1", handler, &pubsub.MessageFilter{})
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
	config := pubsub.DefaultConfig(store)
	config.EnableDeadLetterQueue = true
	config.DeadLetterHandler = func(ctx context.Context, msg *pubsub.Message, err error) error {
		return nil // Always succeed for testing
	}
	ps, err := pubsub.NewPubSub(config)
	require.NoError(t, err)
	defer ps.Close(context.Background())

	err = ps.CreateTopic(context.Background(), "error-test")
	require.NoError(t, err)

	// Test various error scenarios
	testCases := []struct {
		name        string
		handler     pubsub.MessageHandler
		expectError bool
		errorType   string
	}{
		{
			name: "handler-returns-error",
			handler: func(ctx context.Context, msg *pubsub.Message) error {
				return errors.New("handler error")
			},
			expectError: true,
			errorType:   "handler_error",
		},
		{
			name: "handler-panics",
			handler: func(ctx context.Context, msg *pubsub.Message) error {
				panic("handler panic")
			},
			expectError: true,
			errorType:   "panic",
		},
		{
			name: "handler-timeout",
			handler: func(ctx context.Context, msg *pubsub.Message) error {
				time.Sleep(200 * time.Millisecond) // Exceeds timeout
				return nil
			},
			expectError: true,
			errorType:   "timeout",
		},
		{
			name: "handler-succeeds",
			handler: func(ctx context.Context, msg *pubsub.Message) error {
				return nil
			},
			expectError: false,
			errorType:   "success",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			subscriptionID := fmt.Sprintf("sub-%s", tc.name)
			_, err := ps.Subscribe(context.Background(), "error-test", subscriptionID, tc.handler, &pubsub.MessageFilter{})
			require.NoError(t, err)

			data := []byte("error test message")
			err = ps.Publish(context.Background(), "error-test", data, nil)
			require.NoError(t, err)

			// Wait for processing
			time.Sleep(300 * time.Millisecond)

			// Check detailed stats
			stats := ps.GetStats()
			subscriptionStats := stats["subscriptions"].(map[string]interface{})
			subStats := subscriptionStats[subscriptionID].(map[string]interface{})

			messagesFailed := subStats["messages_failed"].(uint64)
			messagesDLQ := subStats["messages_dlq"].(uint64)
			panicRecoveries := subStats["panic_recoveries"].(uint64)
			timeoutErrors := subStats["timeout_errors"].(uint64)

			if tc.expectError {
				assert.Greater(t, messagesFailed, uint64(0), "Should have failed messages")

				switch tc.errorType {
				case "panic":
					assert.Greater(t, panicRecoveries, uint64(0), "Should have panic recoveries")
				case "timeout":
					assert.Greater(t, timeoutErrors, uint64(0), "Should have timeout errors")
				}
			} else {
				assert.Equal(t, uint64(0), messagesFailed, "Should not have failed messages")
			}

			t.Logf("Failed: %d, DLQ: %d, Panics: %d, Timeouts: %d",
				messagesFailed, messagesDLQ, panicRecoveries, timeoutErrors)
		})
	}
}

// TestEnhancedMetrics tests the comprehensive metrics collection
func TestEnhancedMetrics(t *testing.T) {
	store := NewMockStore()
	config := pubsub.DefaultConfig(store)
	ps, err := pubsub.NewPubSub(config)
	require.NoError(t, err)
	defer ps.Close(context.Background())

	err = ps.CreateTopic(context.Background(), "metrics-test")
	require.NoError(t, err)

	// Create handlers with different behaviors
	successHandler := func(ctx context.Context, msg *pubsub.Message) error {
		return nil
	}

	errorHandler := func(ctx context.Context, msg *pubsub.Message) error {
		return errors.New("test error")
	}

	timeoutHandler := func(ctx context.Context, msg *pubsub.Message) error {
		time.Sleep(200 * time.Millisecond) // Exceeds timeout
		return nil
	}

	// Create subscriptions
	_, err = ps.Subscribe(context.Background(), "metrics-test", "success-sub", successHandler, &pubsub.MessageFilter{})
	require.NoError(t, err)

	_, err = ps.Subscribe(context.Background(), "metrics-test", "error-sub", errorHandler, &pubsub.MessageFilter{})
	require.NoError(t, err)

	_, err = ps.Subscribe(context.Background(), "metrics-test", "timeout-sub", timeoutHandler, &pubsub.MessageFilter{})
	require.NoError(t, err)

	// Publish messages
	for i := 0; i < 5; i++ {
		data := []byte(fmt.Sprintf("message %d", i))
		err := ps.Publish(context.Background(), "metrics-test", data, nil)
		require.NoError(t, err)
	}

	// Wait for processing
	time.Sleep(500 * time.Millisecond)

	// Check comprehensive metrics
	stats := ps.GetStats()

	// System metrics
	totalMessages := stats["total_messages"].(uint64)
	assert.Equal(t, uint64(15), totalMessages, "Should have 15 total messages (5 messages * 3 subscriptions)")

	// Publisher metrics
	publisherStats := stats["publisher"].(map[string]interface{})
	messagesPublished := publisherStats["messages_published"].(uint64)
	assert.Equal(t, uint64(5), messagesPublished, "Should have 5 published messages")

	// Subscription metrics
	subscriptionStats := stats["subscriptions"].(map[string]interface{})

	// Success subscription
	successStats := subscriptionStats["success-sub"].(map[string]interface{})
	successProcessed := successStats["messages_processed"].(uint64)
	assert.Equal(t, uint64(5), successProcessed, "Success subscription should process all messages")

	// Error subscription
	errorStats := subscriptionStats["error-sub"].(map[string]interface{})
	errorFailed := errorStats["messages_failed"].(uint64)
	assert.Equal(t, uint64(5), errorFailed, "Error subscription should fail all messages")

	// Timeout subscription
	timeoutStats := subscriptionStats["timeout-sub"].(map[string]interface{})
	timeoutErrors := timeoutStats["timeout_errors"].(uint64)
	assert.Greater(t, timeoutErrors, uint64(0), "Timeout subscription should have timeout errors")

	t.Logf("Total messages: %d", totalMessages)
	t.Logf("Published: %d", messagesPublished)
	t.Logf("Success processed: %d", successProcessed)
	t.Logf("Error failed: %d", errorFailed)
	t.Logf("Timeout errors: %d", timeoutErrors)
}

// TestEnhancedBatchOperations tests enhanced batch operations with concurrency limits
func TestEnhancedBatchOperations(t *testing.T) {
	store := NewMockStore()
	config := pubsub.DefaultConfig(store)
	config.MaxConcurrentOperations = 5
	ps, err := pubsub.NewPubSub(config)
	require.NoError(t, err)
	defer ps.Close(context.Background())

	err = ps.CreateTopic(context.Background(), "batch-test")
	require.NoError(t, err)

	var receivedCount int64
	handler := func(ctx context.Context, msg *pubsub.Message) error {
		atomic.AddInt64(&receivedCount, 1)
		return nil
	}

	_, err = ps.Subscribe(context.Background(), "batch-test", "sub-1", handler, &pubsub.MessageFilter{})
	require.NoError(t, err)

	// Create large batch
	messages := make([]pubsub.Message, 20)
	for i := 0; i < 20; i++ {
		messages[i] = pubsub.Message{
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
	config := pubsub.DefaultConfig(store)
	config.MaxConcurrentOperations = 100
	config.BufferSize = 10000
	ps, err := pubsub.NewPubSub(config)
	require.NoError(b, err)
	defer ps.Close(context.Background())

	err = ps.CreateTopic(context.Background(), "benchmark-topic")
	require.NoError(b, err)

	handler := func(ctx context.Context, msg *pubsub.Message) error {
		return nil
	}

	_, err = ps.Subscribe(context.Background(), "benchmark-topic", "sub-1", handler, &pubsub.MessageFilter{})
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
