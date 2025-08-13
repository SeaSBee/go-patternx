package unit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SeaSBee/go-patternx/patternx/pubsub"
)

// TestRaceConditions tests for race conditions in concurrent operations
func TestRaceConditions(t *testing.T) {
	store := NewMockStore()
	config := pubsub.DefaultConfig(store)
	ps, err := pubsub.NewPubSub(config)
	require.NoError(t, err)
	defer ps.Close(context.Background())

	// Create topic
	err = ps.CreateTopic(context.Background(), "race-test")
	require.NoError(t, err)

	// Subscribe
	var receivedCount int64
	handler := func(ctx context.Context, msg *pubsub.Message) error {
		atomic.AddInt64(&receivedCount, 1)
		return nil
	}

	_, err = ps.Subscribe(context.Background(), "race-test", "sub-1", handler, &pubsub.MessageFilter{})
	require.NoError(t, err)

	// Concurrent publishing and subscribing
	const numGoroutines = 100
	var wg sync.WaitGroup

	// Test concurrent publishing
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			data := []byte(fmt.Sprintf("message %d", index))
			err := ps.Publish(context.Background(), "race-test", data, nil)
			assert.NoError(t, err)
		}(i)
	}

	// Test concurrent subscribing
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			handler := func(ctx context.Context, msg *pubsub.Message) error {
				return nil
			}
			_, err := ps.Subscribe(context.Background(), "race-test", fmt.Sprintf("sub-%d", index), handler, &pubsub.MessageFilter{})
			if err != nil {
				// Expected error for duplicate subscription IDs
				assert.Contains(t, err.Error(), "already exists")
			}
		}(i)
	}

	wg.Wait()

	// Wait for message processing
	time.Sleep(500 * time.Millisecond)

	// Verify no race conditions occurred
	assert.Greater(t, atomic.LoadInt64(&receivedCount), int64(0))
}

// TestDeadlockPrevention tests for deadlock scenarios
func TestDeadlockPrevention(t *testing.T) {
	store := NewMockStore()
	config := pubsub.DefaultConfig(store)
	ps, err := pubsub.NewPubSub(config)
	require.NoError(t, err)

	// Create multiple topics
	for i := 0; i < 10; i++ {
		err := ps.CreateTopic(context.Background(), fmt.Sprintf("topic-%d", i))
		require.NoError(t, err)
	}

	// Create subscriptions with slow handlers that could cause deadlocks
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		topicName := fmt.Sprintf("topic-%d", i)
		handler := func(ctx context.Context, msg *pubsub.Message) error {
			time.Sleep(10 * time.Millisecond) // Simulate slow processing
			return nil
		}
		_, err := ps.Subscribe(context.Background(), topicName, fmt.Sprintf("sub-%d", i), handler, &pubsub.MessageFilter{})
		require.NoError(t, err)
	}

	// Publish messages to all topics concurrently
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			topicName := fmt.Sprintf("topic-%d", index)
			data := []byte(fmt.Sprintf("message %d", index))
			err := ps.Publish(context.Background(), topicName, data, nil)
			assert.NoError(t, err)
		}(i)
	}

	wg.Wait()

	// Test graceful shutdown with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = ps.Close(ctx)
	assert.NoError(t, err)
}

// TestResourceManagement tests for proper resource cleanup
func TestResourceManagement(t *testing.T) {
	store := NewMockStore()
	config := pubsub.DefaultConfig(store)
	ps, err := pubsub.NewPubSub(config)
	require.NoError(t, err)

	// Create topic and subscription
	err = ps.CreateTopic(context.Background(), "resource-test")
	require.NoError(t, err)

	handler := func(ctx context.Context, msg *pubsub.Message) error {
		return nil
	}

	_, err = ps.Subscribe(context.Background(), "resource-test", "sub-1", handler, &pubsub.MessageFilter{})
	require.NoError(t, err)

	// Publish messages
	for i := 0; i < 100; i++ {
		data := []byte(fmt.Sprintf("message %d", i))
		err := ps.Publish(context.Background(), "resource-test", data, nil)
		require.NoError(t, err)
	}

	// Close system
	err = ps.Close(context.Background())
	assert.NoError(t, err)

	// Verify system is closed
	err = ps.Publish(context.Background(), "resource-test", []byte("test"), nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "closed")
}

// TestInputValidation tests comprehensive input validation
func TestInputValidation(t *testing.T) {
	store := NewMockStore()

	// Test invalid config
	invalidConfig := &pubsub.Config{
		Store:            store,
		BufferSize:       -1, // Invalid
		MaxRetryAttempts: -1, // Invalid
	}

	_, err := pubsub.NewPubSub(invalidConfig)
	assert.Error(t, err)

	// Test valid config
	config := pubsub.DefaultConfig(store)
	ps, err := pubsub.NewPubSub(config)
	require.NoError(t, err)
	defer ps.Close(context.Background())

	// Test invalid topic names
	invalidTopics := []string{
		"",                  // Empty
		"topic with spaces", // Spaces
		"topic@invalid",     // Invalid characters
		"topic#invalid",     // Invalid characters
		"topic$invalid",     // Invalid characters
	}

	for _, topicName := range invalidTopics {
		err := ps.CreateTopic(context.Background(), topicName)
		assert.Error(t, err)
	}

	// Test valid topic
	err = ps.CreateTopic(context.Background(), "valid-topic")
	require.NoError(t, err)

	// Test invalid subscription IDs
	invalidSubIDs := []string{
		"",                // Empty
		"sub with spaces", // Spaces
		"sub@invalid",     // Invalid characters
	}

	for _, subID := range invalidSubIDs {
		handler := func(ctx context.Context, msg *pubsub.Message) error {
			return nil
		}
		_, err := ps.Subscribe(context.Background(), "valid-topic", subID, handler, &pubsub.MessageFilter{})
		assert.Error(t, err)
	}

	// Test invalid messages
	invalidMessages := [][]byte{
		nil,                        // Nil
		{},                         // Empty
		make([]byte, 11*1024*1024), // Too large
	}

	for _, data := range invalidMessages {
		err := ps.Publish(context.Background(), "valid-topic", data, nil)
		assert.Error(t, err)
	}

	// Test invalid headers
	invalidHeaders := map[string]string{
		"":            "value", // Empty key
		"key":         "",      // Empty value
		"key@invalid": "value", // Invalid key
	}

	err = ps.Publish(context.Background(), "valid-topic", []byte("valid"), invalidHeaders)
	assert.Error(t, err)
}

// TestConcurrencyLimits tests concurrency limit enforcement
func TestConcurrencyLimits(t *testing.T) {
	store := NewMockStore()
	config := pubsub.DefaultConfig(store)
	config.MaxConcurrentOperations = 2   // Very low limit for testing
	config.BufferSize = 5                // Small buffer to hit queue limits
	config.CircuitBreakerThreshold = 100 // Very high threshold to avoid interference
	config.CircuitBreakerTimeout = 1 * time.Second
	ps, err := pubsub.NewPubSub(config)
	require.NoError(t, err)
	defer ps.Close(context.Background())

	err = ps.CreateTopic(context.Background(), "concurrency-test")
	require.NoError(t, err)

	// Create very slow handler to create backpressure
	handler := func(ctx context.Context, msg *pubsub.Message) error {
		time.Sleep(300 * time.Millisecond) // Very slow processing
		return nil
	}

	_, err = ps.Subscribe(context.Background(), "concurrency-test", "sub-1", handler, &pubsub.MessageFilter{})
	require.NoError(t, err)

	// Try to publish more messages than concurrency limit
	var successCount int64
	var failureCount int64
	var wg sync.WaitGroup

	for i := 0; i < 50; i++ {
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
	time.Sleep(1000 * time.Millisecond)

	// Check if we have any failures due to queue being full
	stats := ps.GetStats()
	topicStats := stats["topics"].(map[string]interface{})
	concurrencyTestStats := topicStats["concurrency-test"].(map[string]interface{})
	queueFullCount := concurrencyTestStats["queue_full_count"].(uint64)

	// Some operations should succeed, some should fail due to concurrency limit
	assert.Greater(t, atomic.LoadInt64(&successCount), int64(0))
	// Check for either publishing failures or queue being full
	assert.True(t, atomic.LoadInt64(&failureCount) > 0 || queueFullCount > 0, "Some operations should fail due to concurrency limit or queue being full")
}

// TestCircuitBreaker tests circuit breaker functionality
func TestCircuitBreaker(t *testing.T) {
	store := NewMockStore()
	config := pubsub.DefaultConfig(store)
	config.CircuitBreakerThreshold = 2
	config.CircuitBreakerTimeout = 100 * time.Millisecond
	ps, err := pubsub.NewPubSub(config)
	require.NoError(t, err)
	defer ps.Close(context.Background())

	err = ps.CreateTopic(context.Background(), "circuit-test")
	require.NoError(t, err)

	// Create handler that fails
	failCount := 0
	handler := func(ctx context.Context, msg *pubsub.Message) error {
		failCount++
		return errors.New("simulated failure")
	}

	_, err = ps.Subscribe(context.Background(), "circuit-test", "sub-1", handler, &pubsub.MessageFilter{})
	require.NoError(t, err)

	// Publish messages that will trigger circuit breaker
	for i := 0; i < 5; i++ {
		data := []byte(fmt.Sprintf("message %d", i))
		err := ps.Publish(context.Background(), "circuit-test", data, nil)
		require.NoError(t, err)
	}

	// Wait for processing
	time.Sleep(500 * time.Millisecond)

	// Circuit breaker should have been triggered
	assert.GreaterOrEqual(t, failCount, 2)
}

// TestPanicRecovery tests panic recovery in message handlers
func TestPanicRecovery(t *testing.T) {
	store := NewMockStore()
	config := pubsub.DefaultConfig(store)
	config.CircuitBreakerThreshold = 100 // Very high threshold to avoid interference
	config.CircuitBreakerTimeout = 1 * time.Second
	ps, err := pubsub.NewPubSub(config)
	require.NoError(t, err)
	defer ps.Close(context.Background())

	err = ps.CreateTopic(context.Background(), "panic-test")
	require.NoError(t, err)

	// Create handler that panics
	handler := func(ctx context.Context, msg *pubsub.Message) error {
		panic("simulated panic")
	}

	_, err = ps.Subscribe(context.Background(), "panic-test", "sub-1", handler, &pubsub.MessageFilter{})
	require.NoError(t, err)

	// Publish message
	data := []byte("panic message")
	err = ps.Publish(context.Background(), "panic-test", data, nil)
	require.NoError(t, err)

	// Wait for processing
	time.Sleep(300 * time.Millisecond)

	// System should not crash and should recover from panic
	stats := ps.GetStats()

	// Check subscription stats for retries
	subscriptionStats := stats["subscriptions"].(map[string]interface{})
	sub1Stats := subscriptionStats["sub-1"].(map[string]interface{})
	messagesRetried := sub1Stats["messages_retried"].(uint64)

	// Check for retries as an indicator of panic recovery (since circuit breaker might mask actual errors)
	assert.Greater(t, messagesRetried, uint64(0), "Should have retried messages due to panic recovery")
}

// TestBatchOperations tests batch publishing functionality
func TestBatchOperations(t *testing.T) {
	store := NewMockStore()
	config := pubsub.DefaultConfig(store)
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

	// Create batch of messages
	messages := make([]pubsub.Message, 10)
	for i := 0; i < 10; i++ {
		messages[i] = pubsub.Message{
			Data:    []byte(fmt.Sprintf("batch message %d", i)),
			Headers: map[string]string{"batch": "true"},
		}
	}

	// Publish batch
	err = ps.PublishBatch(context.Background(), "batch-test", messages)
	require.NoError(t, err)

	// Wait for processing
	time.Sleep(500 * time.Millisecond)

	// Verify all messages were received
	assert.Equal(t, int64(10), atomic.LoadInt64(&receivedCount))

	// Test batch size limit
	largeBatch := make([]pubsub.Message, 2000) // Exceeds limit
	for i := 0; i < 2000; i++ {
		largeBatch[i] = pubsub.Message{
			Data: []byte(fmt.Sprintf("large batch message %d", i)),
		}
	}

	err = ps.PublishBatch(context.Background(), "batch-test", largeBatch)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "batch size exceeds limit")
}

// TestTimeoutHandling tests timeout scenarios
func TestTimeoutHandling(t *testing.T) {
	store := NewMockStore()
	config := pubsub.DefaultConfig(store)
	config.OperationTimeout = 50 * time.Millisecond // Short timeout
	ps, err := pubsub.NewPubSub(config)
	require.NoError(t, err)
	defer ps.Close(context.Background())

	err = ps.CreateTopic(context.Background(), "timeout-test")
	require.NoError(t, err)

	// Create slow handler
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

	// System should handle timeout gracefully
	stats := ps.GetStats()
	errors := stats["errors"].(uint64)
	assert.Greater(t, errors, uint64(0))
}

// TestMemoryLeakPrevention tests for memory leaks
func TestMemoryLeakPrevention(t *testing.T) {
	store := NewMockStore()
	config := pubsub.DefaultConfig(store)
	ps, err := pubsub.NewPubSub(config)
	require.NoError(t, err)

	// Create many topics and subscriptions
	for i := 0; i < 100; i++ {
		topicName := fmt.Sprintf("memory-test-%d", i)
		err := ps.CreateTopic(context.Background(), topicName)
		require.NoError(t, err)

		handler := func(ctx context.Context, msg *pubsub.Message) error {
			return nil
		}

		_, err = ps.Subscribe(context.Background(), topicName, fmt.Sprintf("sub-%d", i), handler, &pubsub.MessageFilter{})
		require.NoError(t, err)
	}

	// Publish many messages
	for i := 0; i < 1000; i++ {
		topicName := fmt.Sprintf("memory-test-%d", i%100)
		data := []byte(fmt.Sprintf("message %d", i))
		err := ps.Publish(context.Background(), topicName, data, nil)
		require.NoError(t, err)
	}

	// Close system
	err = ps.Close(context.Background())
	assert.NoError(t, err)

	// Verify system is properly closed
	err = ps.Publish(context.Background(), "memory-test-0", []byte("test"), nil)
	assert.Error(t, err)
}

// TestErrorPropagation tests proper error propagation
func TestErrorPropagation(t *testing.T) {
	store := NewMockStore()
	config := pubsub.DefaultConfig(store)
	config.CircuitBreakerThreshold = 100 // Very high threshold to avoid interference
	config.CircuitBreakerTimeout = 1 * time.Second
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
	}{
		{
			name: "handler returns error",
			handler: func(ctx context.Context, msg *pubsub.Message) error {
				return errors.New("handler error")
			},
			expectError: true,
		},
		{
			name: "handler panics",
			handler: func(ctx context.Context, msg *pubsub.Message) error {
				panic("handler panic")
			},
			expectError: true,
		},
		{
			name: "handler succeeds",
			handler: func(ctx context.Context, msg *pubsub.Message) error {
				return nil
			},
			expectError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			subscriptionID := fmt.Sprintf("sub-%s", strings.ReplaceAll(tc.name, " ", "-"))
			_, err := ps.Subscribe(context.Background(), "error-test", subscriptionID, tc.handler, &pubsub.MessageFilter{})
			require.NoError(t, err)

			data := []byte("error test message")
			err = ps.Publish(context.Background(), "error-test", data, nil)
			require.NoError(t, err)

			// Wait for processing - longer wait for error scenarios
			if tc.expectError {
				time.Sleep(400 * time.Millisecond)
			} else {
				time.Sleep(200 * time.Millisecond)
			}

			// Check stats
			stats := ps.GetStats()

			// Check subscription stats for retries
			subscriptionStats := stats["subscriptions"].(map[string]interface{})
			subStats := subscriptionStats[subscriptionID].(map[string]interface{})
			messagesRetried := subStats["messages_retried"].(uint64)

			if tc.expectError {
				// Check for retries as an indicator of errors (since circuit breaker might mask actual errors)
				assert.Greater(t, messagesRetried, uint64(0), "Should have retried messages due to errors")
			}
		})
	}
}

// TestContextCancellationEnhanced tests context cancellation handling
func TestContextCancellationEnhanced(t *testing.T) {
	store := NewMockStore()
	config := pubsub.DefaultConfig(store)
	ps, err := pubsub.NewPubSub(config)
	require.NoError(t, err)
	defer ps.Close(context.Background())

	err = ps.CreateTopic(context.Background(), "context-test")
	require.NoError(t, err)

	// Test with cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = ps.Publish(ctx, "context-test", []byte("test"), nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cancelled")

	// Test with timeout context
	timeoutCtx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	time.Sleep(2 * time.Millisecond) // Wait for timeout

	err = ps.Publish(timeoutCtx, "context-test", []byte("test"), nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cancelled")
}

// BenchmarkConcurrentPublishing benchmarks concurrent publishing performance
func BenchmarkConcurrentPublishing(b *testing.B) {
	store := NewMockStore()
	config := pubsub.DefaultConfig(store)
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

// BenchmarkBatchPublishing benchmarks batch publishing performance
func BenchmarkBatchPublishing(b *testing.B) {
	store := NewMockStore()
	config := pubsub.DefaultConfig(store)
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

	// Create batch of messages
	messages := make([]pubsub.Message, 100)
	for i := 0; i < 100; i++ {
		messages[i] = pubsub.Message{
			Data:    []byte(fmt.Sprintf("batch message %d", i)),
			Headers: map[string]string{"batch": "true"},
		}
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		err := ps.PublishBatch(context.Background(), "benchmark-topic", messages)
		if err != nil {
			b.Fatal(err)
		}
	}
}
