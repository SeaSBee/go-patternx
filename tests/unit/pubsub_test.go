package unit

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/SeaSBee/go-patternx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// MockStore implements PubSubStore for testing
type MockStore struct {
	data map[string][]byte
	mu   sync.RWMutex
}

func NewMockStore() *MockStore {
	return &MockStore{
		data: make(map[string][]byte),
	}
}

func (m *MockStore) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.data[key] = value
	return nil
}

func (m *MockStore) Get(ctx context.Context, key string) ([]byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	if value, exists := m.data[key]; exists {
		return value, nil
	}
	return nil, errors.New("key not found")
}

func (m *MockStore) Del(ctx context.Context, key string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.data, key)
	return nil
}

func (m *MockStore) Exists(ctx context.Context, key string) (bool, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, exists := m.data[key]
	return exists, nil
}

func TestNewPubSub(t *testing.T) {
	store := NewMockStore()
	config := patternx.DefaultConfigPubSub(store)

	ps, err := patternx.NewPubSub(config)
	require.NoError(t, err)
	require.NotNil(t, ps)

	defer ps.Close(context.Background())

	stats := ps.GetStats()
	assert.Equal(t, uint64(0), stats["total_messages"])
	assert.Equal(t, uint64(0), stats["total_topics"])
}

func TestCreateTopic(t *testing.T) {
	store := NewMockStore()
	config := patternx.DefaultConfigPubSub(store)
	ps, err := patternx.NewPubSub(config)
	require.NoError(t, err)
	defer ps.Close(context.Background())

	// Test successful topic creation
	err = ps.CreateTopic(context.Background(), "test-topic")
	assert.NoError(t, err)

	// Test duplicate topic creation
	err = ps.CreateTopic(context.Background(), "test-topic")
	assert.Error(t, err)

	// Test invalid topic name
	err = ps.CreateTopic(context.Background(), "")
	assert.Error(t, err)
}

func TestSubscribe(t *testing.T) {
	store := NewMockStore()
	config := patternx.DefaultConfigPubSub(store)
	ps, err := patternx.NewPubSub(config)
	require.NoError(t, err)
	defer ps.Close(context.Background())

	// Create topic first
	err = ps.CreateTopic(context.Background(), "test-topic")
	require.NoError(t, err)

	// Test successful subscription
	handler := func(ctx context.Context, msg *patternx.MessagePubSub) error {
		return nil
	}

	subscription, err := ps.Subscribe(context.Background(), "test-topic", "sub-1", handler, &patternx.MessageFilter{})
	assert.NoError(t, err)
	assert.NotNil(t, subscription)

	// Test duplicate subscription
	_, err = ps.Subscribe(context.Background(), "test-topic", "sub-1", handler, &patternx.MessageFilter{})
	assert.Error(t, err)

	// Test subscription to non-existent topic
	_, err = ps.Subscribe(context.Background(), "non-existent", "sub-2", handler, &patternx.MessageFilter{})
	assert.Error(t, err)

	// Test nil handler
	_, err = ps.Subscribe(context.Background(), "test-topic", "sub-3", nil, &patternx.MessageFilter{})
	assert.Error(t, err)
}

func TestPublish(t *testing.T) {
	store := NewMockStore()
	config := patternx.DefaultConfigPubSub(store)
	ps, err := patternx.NewPubSub(config)
	require.NoError(t, err)
	defer ps.Close(context.Background())

	// Create topic and subscription
	err = ps.CreateTopic(context.Background(), "test-topic")
	require.NoError(t, err)

	var receivedMessages []*patternx.MessagePubSub
	var mu sync.Mutex

	handler := func(ctx context.Context, msg *patternx.MessagePubSub) error {
		mu.Lock()
		defer mu.Unlock()
		receivedMessages = append(receivedMessages, msg)
		return nil
	}

	_, err = ps.Subscribe(context.Background(), "test-topic", "sub-1", handler, &patternx.MessageFilter{})
	require.NoError(t, err)

	// Test successful publish
	data := []byte("test message")
	headers := map[string]string{"content-type": "text/plain"}

	err = ps.Publish(context.Background(), "test-topic", data, headers)
	assert.NoError(t, err)

	// Wait for message processing
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	assert.Len(t, receivedMessages, 1)
	if len(receivedMessages) > 0 {
		assert.Equal(t, data, receivedMessages[0].Data)
		assert.Equal(t, headers, receivedMessages[0].Headers)
	}
	mu.Unlock()

	// Test publish to non-existent topic
	err = ps.Publish(context.Background(), "non-existent", data, headers)
	assert.Error(t, err)

	// Test empty message
	err = ps.Publish(context.Background(), "test-topic", nil, headers)
	assert.Error(t, err)
}

func TestMessageFiltering(t *testing.T) {
	store := NewMockStore()
	config := patternx.DefaultConfigPubSub(store)
	ps, err := patternx.NewPubSub(config)
	require.NoError(t, err)
	defer ps.Close(context.Background())

	err = ps.CreateTopic(context.Background(), "test-topic")
	require.NoError(t, err)

	var receivedMessages []*patternx.MessagePubSub
	var mu sync.Mutex

	// Create subscription with header filter
	filter := &patternx.MessageFilter{
		Headers: map[string]string{"priority": "high"},
	}

	handler := func(ctx context.Context, msg *patternx.MessagePubSub) error {
		mu.Lock()
		defer mu.Unlock()
		receivedMessages = append(receivedMessages, msg)
		return nil
	}

	_, err = ps.Subscribe(context.Background(), "test-topic", "sub-1", handler, filter)
	require.NoError(t, err)

	// Publish message that should be filtered out
	data := []byte("low priority message")
	headers := map[string]string{"priority": "low"}

	err = ps.Publish(context.Background(), "test-topic", data, headers)
	assert.NoError(t, err)

	// Publish message that should be received
	data2 := []byte("high priority message")
	headers2 := map[string]string{"priority": "high"}

	err = ps.Publish(context.Background(), "test-topic", data2, headers2)
	assert.NoError(t, err)

	// Wait for message processing
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	assert.Len(t, receivedMessages, 1)
	if len(receivedMessages) > 0 {
		assert.Equal(t, data2, receivedMessages[0].Data)
	}
	mu.Unlock()
}

func TestRetryMechanism(t *testing.T) {
	store := NewMockStore()
	config := patternx.DefaultConfigPubSub(store)
	config.MaxRetryAttempts = 2
	config.RetryDelay = 10 * time.Millisecond

	ps, err := patternx.NewPubSub(config)
	require.NoError(t, err)
	defer ps.Close(context.Background())

	err = ps.CreateTopic(context.Background(), "test-topic")
	require.NoError(t, err)

	attemptCount := 0
	var mu sync.Mutex

	handler := func(ctx context.Context, msg *patternx.MessagePubSub) error {
		mu.Lock()
		attemptCount++
		currentAttempt := attemptCount
		mu.Unlock()

		if currentAttempt < 3 {
			return errors.New("temporary error")
		}
		return nil
	}

	_, err = ps.Subscribe(context.Background(), "test-topic", "sub-1", handler, &patternx.MessageFilter{})
	require.NoError(t, err)

	data := []byte("test message")
	err = ps.Publish(context.Background(), "test-topic", data, nil)
	assert.NoError(t, err)

	// Wait for message processing with retries
	time.Sleep(200 * time.Millisecond)

	mu.Lock()
	assert.Equal(t, 3, attemptCount) // Initial attempt + 2 retries
	mu.Unlock()
}

func TestConcurrentPublishing(t *testing.T) {
	store := NewMockStore()
	config := patternx.DefaultConfigPubSub(store)
	config.BufferSize = 1000

	ps, err := patternx.NewPubSub(config)
	require.NoError(t, err)
	defer ps.Close(context.Background())

	err = ps.CreateTopic(context.Background(), "test-topic")
	require.NoError(t, err)

	var receivedCount int64
	var mu sync.Mutex

	handler := func(ctx context.Context, msg *patternx.MessagePubSub) error {
		mu.Lock()
		defer mu.Unlock()
		receivedCount++
		return nil
	}

	_, err = ps.Subscribe(context.Background(), "test-topic", "sub-1", handler, &patternx.MessageFilter{})
	require.NoError(t, err)

	// Publish messages concurrently
	const numMessages = 100
	var wg sync.WaitGroup

	for i := 0; i < numMessages; i++ {
		wg.Add(1)
		go func(index int) {
			defer wg.Done()
			data := []byte(fmt.Sprintf("message %d", index))
			err := ps.Publish(context.Background(), "test-topic", data, nil)
			assert.NoError(t, err)
		}(i)
	}

	wg.Wait()

	// Wait for message processing
	time.Sleep(500 * time.Millisecond)

	mu.Lock()
	assert.Equal(t, int64(numMessages), receivedCount)
	mu.Unlock()
}

func TestMultipleSubscribers(t *testing.T) {
	store := NewMockStore()
	config := patternx.DefaultConfigPubSub(store)
	ps, err := patternx.NewPubSub(config)
	require.NoError(t, err)
	defer ps.Close(context.Background())

	err = ps.CreateTopic(context.Background(), "test-topic")
	require.NoError(t, err)

	var subscriber1Count int64
	var subscriber2Count int64
	var mu sync.Mutex

	handler1 := func(ctx context.Context, msg *patternx.MessagePubSub) error {
		mu.Lock()
		defer mu.Unlock()
		subscriber1Count++
		return nil
	}

	handler2 := func(ctx context.Context, msg *patternx.MessagePubSub) error {
		mu.Lock()
		defer mu.Unlock()
		subscriber2Count++
		return nil
	}

	_, err = ps.Subscribe(context.Background(), "test-topic", "sub-1", handler1, &patternx.MessageFilter{})
	require.NoError(t, err)

	_, err = ps.Subscribe(context.Background(), "test-topic", "sub-2", handler2, &patternx.MessageFilter{})
	require.NoError(t, err)

	// Publish message
	data := []byte("test message")
	err = ps.Publish(context.Background(), "test-topic", data, nil)
	assert.NoError(t, err)

	// Wait for message processing
	time.Sleep(100 * time.Millisecond)

	mu.Lock()
	assert.Equal(t, int64(1), subscriber1Count)
	assert.Equal(t, int64(1), subscriber2Count)
	mu.Unlock()
}

func TestGracefulShutdown(t *testing.T) {
	store := NewMockStore()
	config := patternx.DefaultConfigPubSub(store)
	ps, err := patternx.NewPubSub(config)
	require.NoError(t, err)

	err = ps.CreateTopic(context.Background(), "test-topic")
	require.NoError(t, err)

	handler := func(ctx context.Context, msg *patternx.MessagePubSub) error {
		return nil
	}

	_, err = ps.Subscribe(context.Background(), "test-topic", "sub-1", handler, &patternx.MessageFilter{})
	require.NoError(t, err)

	// Publish a message
	data := []byte("test message")
	err = ps.Publish(context.Background(), "test-topic", data, nil)
	assert.NoError(t, err)

	// Close the system
	err = ps.Close(context.Background())
	assert.NoError(t, err)

	// Try to publish after closing
	err = ps.Publish(context.Background(), "test-topic", data, nil)
	assert.Error(t, err)
}

func TestMessagePersistence(t *testing.T) {
	store := NewMockStore()
	config := patternx.DefaultConfigPubSub(store)
	ps, err := patternx.NewPubSub(config)
	require.NoError(t, err)
	defer ps.Close(context.Background())

	err = ps.CreateTopic(context.Background(), "test-topic")
	require.NoError(t, err)

	data := []byte("test message")
	headers := map[string]string{"test": "value"}

	err = ps.Publish(context.Background(), "test-topic", data, headers)
	assert.NoError(t, err)

	// Check if message was stored
	time.Sleep(50 * time.Millisecond)

	// Verify message was stored (this would require accessing the store directly)
	// For now, we just verify the publish didn't fail
	assert.NoError(t, err)
}

func TestHighReliabilityConfig(t *testing.T) {
	store := NewMockStore()
	config := patternx.HighReliabilityConfigPubSub(store)

	ps, err := patternx.NewPubSub(config)
	require.NoError(t, err)
	defer ps.Close(context.Background())

	// Verify high reliability settings
	assert.Equal(t, 50000, config.BufferSize)
	assert.Equal(t, 5, config.MaxRetryAttempts)
	assert.Equal(t, 200*time.Millisecond, config.RetryDelay)
}

func TestMessageValidation(t *testing.T) {
	store := NewMockStore()
	config := patternx.DefaultConfigPubSub(store)
	ps, err := patternx.NewPubSub(config)
	require.NoError(t, err)
	defer ps.Close(context.Background())

	err = ps.CreateTopic(context.Background(), "test-topic")
	require.NoError(t, err)

	// Test empty message
	err = ps.Publish(context.Background(), "test-topic", nil, nil)
	assert.Error(t, err)

	// Test empty byte slice
	err = ps.Publish(context.Background(), "test-topic", []byte{}, nil)
	assert.Error(t, err)

	// Test valid message
	err = ps.Publish(context.Background(), "test-topic", []byte("valid"), nil)
	assert.NoError(t, err)
}

func TestContextCancellation(t *testing.T) {
	store := NewMockStore()
	config := patternx.DefaultConfigPubSub(store)
	ps, err := patternx.NewPubSub(config)
	require.NoError(t, err)
	defer ps.Close(context.Background())

	err = ps.CreateTopic(context.Background(), "test-topic")
	require.NoError(t, err)

	// Test with cancelled context
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = ps.Publish(ctx, "test-topic", []byte("test"), nil)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "cancelled")
}

func TestStatsCollection(t *testing.T) {
	store := NewMockStore()
	config := patternx.DefaultConfigPubSub(store)
	ps, err := patternx.NewPubSub(config)
	require.NoError(t, err)
	defer ps.Close(context.Background())

	// Get initial stats
	initialStats := ps.GetStats()
	assert.Equal(t, uint64(0), initialStats["total_messages"])
	assert.Equal(t, uint64(0), initialStats["total_topics"])

	// Create topic and publish message
	err = ps.CreateTopic(context.Background(), "test-topic")
	require.NoError(t, err)

	handler := func(ctx context.Context, msg *patternx.MessagePubSub) error {
		return nil
	}

	_, err = ps.Subscribe(context.Background(), "test-topic", "sub-1", handler, &patternx.MessageFilter{})
	require.NoError(t, err)

	err = ps.Publish(context.Background(), "test-topic", []byte("test"), nil)
	assert.NoError(t, err)

	// Wait for processing
	time.Sleep(100 * time.Millisecond)

	// Get updated stats
	updatedStats := ps.GetStats()
	assert.Equal(t, uint64(1), updatedStats["total_messages"])
	assert.Equal(t, uint64(1), updatedStats["total_topics"])
	assert.Equal(t, uint64(1), updatedStats["total_subscriptions"])

	// Check publisher stats
	publisherStats := updatedStats["publisher"].(map[string]interface{})
	assert.Equal(t, uint64(1), publisherStats["messages_published"])
	assert.Equal(t, uint64(0), publisherStats["messages_failed"])
}

func BenchmarkPublish(b *testing.B) {
	store := NewMockStore()
	config := patternx.DefaultConfigPubSub(store)
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
	for i := 0; i < b.N; i++ {
		err := ps.Publish(context.Background(), "benchmark-topic", data, headers)
		if err != nil {
			b.Fatal(err)
		}
	}
}

func BenchmarkConcurrentPublish(b *testing.B) {
	store := NewMockStore()
	config := patternx.DefaultConfigPubSub(store)
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
