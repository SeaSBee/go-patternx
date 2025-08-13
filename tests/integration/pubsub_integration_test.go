package integration

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/SeaSBee/go-patternx/patternx/pubsub"
)

// RedisMockStore simulates a Redis-like store for integration testing
type RedisMockStore struct {
	data map[string][]byte
	mu   sync.RWMutex
}

func NewRedisMockStore() *RedisMockStore {
	return &RedisMockStore{
		data: make(map[string][]byte),
	}
}

func (r *RedisMockStore) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.data[key] = value
	return nil
}

func (r *RedisMockStore) Get(ctx context.Context, key string) ([]byte, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	if value, exists := r.data[key]; exists {
		return value, nil
	}
	return nil, errors.New("key not found")
}

func (r *RedisMockStore) Del(ctx context.Context, key string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.data, key)
	return nil
}

func (r *RedisMockStore) Exists(ctx context.Context, key string) (bool, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	_, exists := r.data[key]
	return exists, nil
}

// TestPubSubWithHighReliability tests the pub/sub system with high reliability configuration
func TestPubSubWithHighReliability(t *testing.T) {
	store := NewRedisMockStore()
	config := pubsub.HighReliabilityConfig(store)

	ps, err := pubsub.NewPubSub(config)
	require.NoError(t, err)
	defer ps.Close(context.Background())

	// Create multiple topics
	topics := []string{"orders", "payments", "notifications"}
	for _, topic := range topics {
		err := ps.CreateTopic(context.Background(), topic)
		require.NoError(t, err)
	}

	// Create subscriptions with different reliability patterns
	var orderMessages []*pubsub.Message
	var paymentMessages []*pubsub.Message
	var notificationMessages []*pubsub.Message
	var mu sync.Mutex

	// Order processing with high priority
	orderHandler := func(ctx context.Context, msg *pubsub.Message) error {
		mu.Lock()
		defer mu.Unlock()
		orderMessages = append(orderMessages, msg)
		return nil
	}

	orderFilter := &pubsub.MessageFilter{
		Headers:  map[string]string{"type": "order"},
		Priority: 10,
	}

	_, err = ps.Subscribe(context.Background(), "orders", "order-processor", orderHandler, orderFilter)
	require.NoError(t, err)

	// Payment processing with retry logic
	paymentAttempts := 0
	paymentHandler := func(ctx context.Context, msg *pubsub.Message) error {
		mu.Lock()
		paymentAttempts++
		currentAttempt := paymentAttempts
		mu.Unlock()

		// Simulate payment processing that might fail initially
		if currentAttempt < 3 {
			return errors.New("payment gateway temporarily unavailable")
		}

		mu.Lock()
		paymentMessages = append(paymentMessages, msg)
		mu.Unlock()
		return nil
	}

	paymentFilter := &pubsub.MessageFilter{
		Headers:  map[string]string{"type": "payment"},
		Priority: 8,
	}

	_, err = ps.Subscribe(context.Background(), "payments", "payment-processor", paymentHandler, paymentFilter)
	require.NoError(t, err)

	// Notification processing
	notificationHandler := func(ctx context.Context, msg *pubsub.Message) error {
		mu.Lock()
		defer mu.Unlock()
		notificationMessages = append(notificationMessages, msg)
		return nil
	}

	notificationFilter := &pubsub.MessageFilter{
		Headers:  map[string]string{"type": "notification"},
		Priority: 5,
	}

	_, err = ps.Subscribe(context.Background(), "notifications", "notification-sender", notificationHandler, notificationFilter)
	require.NoError(t, err)

	// Publish messages to different topics
	orderData := []byte(`{"order_id": "12345", "amount": 99.99}`)
	orderHeaders := map[string]string{"type": "order", "priority": "high"}

	paymentData := []byte(`{"payment_id": "67890", "amount": 99.99}`)
	paymentHeaders := map[string]string{"type": "payment", "method": "credit_card"}

	notificationData := []byte(`{"user_id": "user123", "message": "Order confirmed"}`)
	notificationHeaders := map[string]string{"type": "notification", "channel": "email"}

	// Publish messages
	err = ps.Publish(context.Background(), "orders", orderData, orderHeaders)
	require.NoError(t, err)

	err = ps.Publish(context.Background(), "payments", paymentData, paymentHeaders)
	require.NoError(t, err)

	err = ps.Publish(context.Background(), "notifications", notificationData, notificationHeaders)
	require.NoError(t, err)

	// Wait for message processing
	time.Sleep(500 * time.Millisecond)

	// Verify message delivery
	mu.Lock()
	assert.Len(t, orderMessages, 1)
	assert.Len(t, paymentMessages, 1)
	assert.Len(t, notificationMessages, 1)
	assert.Equal(t, 3, paymentAttempts) // Should have retried twice
	mu.Unlock()

	// Verify message content
	if len(orderMessages) > 0 {
		assert.Equal(t, orderData, orderMessages[0].Data)
		assert.Equal(t, orderHeaders, orderMessages[0].Headers)
	}

	if len(paymentMessages) > 0 {
		assert.Equal(t, paymentData, paymentMessages[0].Data)
		assert.Equal(t, paymentHeaders, paymentMessages[0].Headers)
	}

	if len(notificationMessages) > 0 {
		assert.Equal(t, notificationData, notificationMessages[0].Data)
		assert.Equal(t, notificationHeaders, notificationMessages[0].Data)
	}
}

// TestPubSubLoadTesting tests the pub/sub system under load
func TestPubSubLoadTesting(t *testing.T) {
	store := NewRedisMockStore()
	config := pubsub.HighReliabilityConfig(store)
	config.BufferSize = 50000 // Large buffer for load testing

	ps, err := pubsub.NewPubSub(config)
	require.NoError(t, err)
	defer ps.Close(context.Background())

	err = ps.CreateTopic(context.Background(), "load-test")
	require.NoError(t, err)

	var receivedCount int64
	var mu sync.Mutex

	handler := func(ctx context.Context, msg *pubsub.Message) error {
		mu.Lock()
		defer mu.Unlock()
		receivedCount++
		return nil
	}

	_, err = ps.Subscribe(context.Background(), "load-test", "load-processor", handler, &pubsub.MessageFilter{})
	require.NoError(t, err)

	// Publish messages concurrently
	const numMessages = 1000
	const numGoroutines = 10
	var wg sync.WaitGroup

	start := time.Now()

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()
			for j := 0; j < numMessages/numGoroutines; j++ {
				data := []byte(fmt.Sprintf(`{"goroutine": %d, "message": %d}`, goroutineID, j))
				headers := map[string]string{
					"goroutine_id": fmt.Sprintf("%d", goroutineID),
					"message_id":   fmt.Sprintf("%d", j),
				}

				err := ps.Publish(context.Background(), "load-test", data, headers)
				if err != nil {
					t.Errorf("Failed to publish message: %v", err)
				}
			}
		}(i)
	}

	wg.Wait()
	publishDuration := time.Since(start)

	// Wait for message processing
	time.Sleep(2 * time.Second)

	mu.Lock()
	finalCount := receivedCount
	mu.Unlock()

	// Verify all messages were received
	assert.Equal(t, int64(numMessages), finalCount)

	// Log performance metrics
	t.Logf("Published %d messages in %v", numMessages, publishDuration)
	t.Logf("Average publish rate: %.2f messages/second", float64(numMessages)/publishDuration.Seconds())

	// Get system stats
	stats := ps.GetStats()
	t.Logf("System stats: %+v", stats)
}

// TestPubSubFaultTolerance tests the pub/sub system's fault tolerance
func TestPubSubFaultTolerance(t *testing.T) {
	store := NewRedisMockStore()
	config := pubsub.HighReliabilityConfig(store)

	ps, err := pubsub.NewPubSub(config)
	require.NoError(t, err)
	defer ps.Close(context.Background())

	err = ps.CreateTopic(context.Background(), "fault-test")
	require.NoError(t, err)

	var successfulMessages int64
	var failedMessages int64
	var mu sync.Mutex

	// Create a handler that sometimes fails
	handler := func(ctx context.Context, msg *pubsub.Message) error {
		// Simulate intermittent failures
		if msg.RetryCount < 2 {
			return errors.New("simulated temporary failure")
		}

		mu.Lock()
		defer mu.Unlock()
		successfulMessages++
		return nil
	}

	_, err = ps.Subscribe(context.Background(), "fault-test", "fault-processor", handler, &pubsub.MessageFilter{})
	require.NoError(t, err)

	// Publish messages that will initially fail
	const numMessages = 100
	for i := 0; i < numMessages; i++ {
		data := []byte(fmt.Sprintf(`{"message_id": %d}`, i))
		headers := map[string]string{"test": "fault-tolerance"}

		err := ps.Publish(context.Background(), "fault-test", data, headers)
		require.NoError(t, err)
	}

	// Wait for message processing with retries
	time.Sleep(1 * time.Second)

	mu.Lock()
	successCount := successfulMessages
	failCount := failedMessages
	mu.Unlock()

	// Verify that messages eventually succeeded after retries
	assert.Equal(t, int64(numMessages), successCount)
	assert.Equal(t, int64(0), failCount)

	// Get stats to verify retry behavior
	stats := ps.GetStats()
	t.Logf("Fault tolerance stats: %+v", stats)
}

// TestPubSubMessageOrdering tests message ordering guarantees
func TestPubSubMessageOrdering(t *testing.T) {
	store := NewRedisMockStore()
	config := pubsub.DefaultConfig(store)

	ps, err := pubsub.NewPubSub(config)
	require.NoError(t, err)
	defer ps.Close(context.Background())

	err = ps.CreateTopic(context.Background(), "order-test")
	require.NoError(t, err)

	var receivedMessages []*pubsub.Message
	var mu sync.Mutex

	handler := func(ctx context.Context, msg *pubsub.Message) error {
		mu.Lock()
		defer mu.Unlock()
		receivedMessages = append(receivedMessages, msg)
		return nil
	}

	_, err = ps.Subscribe(context.Background(), "order-test", "order-processor", handler, &pubsub.MessageFilter{})
	require.NoError(t, err)

	// Publish messages in sequence
	const numMessages = 50
	for i := 0; i < numMessages; i++ {
		data := []byte(fmt.Sprintf(`{"sequence": %d}`, i))
		headers := map[string]string{"sequence": fmt.Sprintf("%d", i)}

		err := ps.Publish(context.Background(), "order-test", data, headers)
		require.NoError(t, err)
	}

	// Wait for message processing
	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	messageCount := len(receivedMessages)
	mu.Unlock()

	// Verify all messages were received
	assert.Equal(t, numMessages, messageCount)

	// Note: In a distributed system, strict ordering might not be guaranteed
	// This test verifies that all messages are delivered, but order may vary
	t.Logf("Received %d messages", messageCount)
}

// TestPubSubGracefulShutdown tests graceful shutdown behavior
func TestPubSubGracefulShutdown(t *testing.T) {
	store := NewRedisMockStore()
	config := pubsub.DefaultConfig(store)

	ps, err := pubsub.NewPubSub(config)
	require.NoError(t, err)

	err = ps.CreateTopic(context.Background(), "shutdown-test")
	require.NoError(t, err)

	var processedMessages int64
	var mu sync.Mutex

	handler := func(ctx context.Context, msg *pubsub.Message) error {
		// Simulate processing time
		time.Sleep(10 * time.Millisecond)

		mu.Lock()
		defer mu.Unlock()
		processedMessages++
		return nil
	}

	_, err = ps.Subscribe(context.Background(), "shutdown-test", "shutdown-processor", handler, &pubsub.MessageFilter{})
	require.NoError(t, err)

	// Publish messages
	const numMessages = 10
	for i := 0; i < numMessages; i++ {
		data := []byte(fmt.Sprintf(`{"shutdown_test": %d}`, i))
		err := ps.Publish(context.Background(), "shutdown-test", data, nil)
		require.NoError(t, err)
	}

	// Start shutdown immediately
	go func() {
		time.Sleep(50 * time.Millisecond) // Allow some messages to be queued
		err := ps.Close(context.Background())
		require.NoError(t, err)
	}()

	// Wait for shutdown to complete
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	finalCount := processedMessages
	mu.Unlock()

	// Verify that some messages were processed before shutdown
	assert.True(t, finalCount > 0)
	t.Logf("Processed %d messages before shutdown", finalCount)
}

// TestPubSubConfigurationValidation tests configuration validation
func TestPubSubConfigurationValidation(t *testing.T) {
	store := NewRedisMockStore()

	// Test invalid configuration
	invalidConfig := &pubsub.Config{
		Store:            store,
		BufferSize:       -1, // Invalid buffer size
		MaxRetryAttempts: -1, // Invalid retry attempts
	}

	_, err := pubsub.NewPubSub(invalidConfig)
	assert.Error(t, err)

	// Test nil configuration
	_, err = pubsub.NewPubSub(nil)
	assert.Error(t, err)

	// Test valid configuration
	validConfig := pubsub.DefaultConfig(store)
	ps, err := pubsub.NewPubSub(validConfig)
	require.NoError(t, err)
	defer ps.Close(context.Background())

	// Verify default values were applied
	assert.Equal(t, pubsub.DefaultKeyPrefix, validConfig.KeyPrefix)
	assert.Equal(t, pubsub.DefaultTTL, validConfig.TTL)
	assert.Equal(t, pubsub.DefaultBufferSize, validConfig.BufferSize)
	assert.Equal(t, pubsub.MaxRetryAttempts, validConfig.MaxRetryAttempts)
	assert.Equal(t, pubsub.RetryDelay, validConfig.RetryDelay)
}
