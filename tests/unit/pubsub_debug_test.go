package unit

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/SeaSBee/go-patternx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDebugMessageDelivery tests basic message delivery to identify issues
func TestDebugMessageDelivery(t *testing.T) {
	store := NewMockStore()
	config := patternx.DefaultConfigPubSub(store)
	ps, err := patternx.NewPubSub(config)
	require.NoError(t, err)
	defer ps.Close(context.Background())

	// Create topic
	err = ps.CreateTopic(context.Background(), "debug-topic")
	require.NoError(t, err)

	// Create subscription
	var receivedMessages []*patternx.MessagePubSub
	var mu sync.Mutex

	handler := func(ctx context.Context, msg *patternx.MessagePubSub) error {
		mu.Lock()
		defer mu.Unlock()
		receivedMessages = append(receivedMessages, msg)
		return nil
	}

	_, err = ps.Subscribe(context.Background(), "debug-topic", "debug-sub", handler, &patternx.MessageFilter{})
	require.NoError(t, err)

	// Publish message
	data := []byte("debug message")
	err = ps.Publish(context.Background(), "debug-topic", data, nil)
	require.NoError(t, err)

	// Wait for message processing
	time.Sleep(500 * time.Millisecond)

	// Check if message was received
	mu.Lock()
	messageCount := len(receivedMessages)
	mu.Unlock()

	t.Logf("Received %d messages", messageCount)
	assert.Greater(t, messageCount, 0, "No messages were received")

	if messageCount > 0 {
		mu.Lock()
		receivedData := receivedMessages[0].Data
		mu.Unlock()
		assert.Equal(t, data, receivedData, "Message data doesn't match")
	}
}

// TestDebugStats tests if stats are being collected properly
func TestDebugStats(t *testing.T) {
	store := NewMockStore()
	config := patternx.DefaultConfigPubSub(store)
	ps, err := patternx.NewPubSub(config)
	require.NoError(t, err)
	defer ps.Close(context.Background())

	// Create topic
	err = ps.CreateTopic(context.Background(), "stats-topic")
	require.NoError(t, err)

	// Create subscription
	handler := func(ctx context.Context, msg *patternx.MessagePubSub) error {
		return nil
	}

	_, err = ps.Subscribe(context.Background(), "stats-topic", "stats-sub", handler, &patternx.MessageFilter{})
	require.NoError(t, err)

	// Publish message
	data := []byte("stats message")
	err = ps.Publish(context.Background(), "stats-topic", data, nil)
	require.NoError(t, err)

	// Wait for processing
	time.Sleep(100 * time.Millisecond)

	// Check stats
	stats := ps.GetStats()
	t.Logf("Stats: %+v", stats)

	totalMessages := stats["total_messages"].(uint64)
	assert.Greater(t, totalMessages, uint64(0), "No messages were published")

	publisherStats := stats["publisher"].(map[string]interface{})
	messagesPublished := publisherStats["messages_published"].(uint64)
	assert.Greater(t, messagesPublished, uint64(0), "No messages were published according to publisher stats")
}
