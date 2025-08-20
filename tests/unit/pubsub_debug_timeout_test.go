package unit

import (
	"context"
	"testing"
	"time"

	"github.com/SeaSBee/go-patternx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestDebugTimeoutHandling tests timeout handling with detailed logging
func TestDebugTimeoutHandling(t *testing.T) {
	store := NewMockStore()
	config := patternx.DefaultConfigPubSub(store)
	config.OperationTimeout = 50 * time.Millisecond // Short timeout
	ps, err := patternx.NewPubSub(config)
	require.NoError(t, err)
	defer ps.Close(context.Background())

	err = ps.CreateTopic(context.Background(), "timeout-debug")
	require.NoError(t, err)

	// Create slow handler that exceeds timeout
	handler := func(ctx context.Context, msg *patternx.MessagePubSub) error {
		t.Logf("Handler started for message: %s", msg.ID)
		time.Sleep(200 * time.Millisecond) // Longer than timeout
		t.Logf("Handler completed for message: %s", msg.ID)
		return nil
	}

	_, err = ps.Subscribe(context.Background(), "timeout-debug", "sub-1", handler, &patternx.MessageFilter{})
	require.NoError(t, err)

	// Publish message
	data := []byte("timeout debug message")
	err = ps.Publish(context.Background(), "timeout-debug", data, nil)
	require.NoError(t, err)

	// Wait for processing
	time.Sleep(500 * time.Millisecond)

	// Check all stats
	stats := ps.GetStats()
	t.Logf("Full stats: %+v", stats)

	// Check subscription stats
	subscriptionStats := stats["subscriptions"].(map[string]interface{})
	sub1Stats := subscriptionStats["sub-1"].(map[string]interface{})

	timeoutErrors := sub1Stats["timeout_errors"].(uint64)
	messagesFailed := sub1Stats["messages_failed"].(uint64)
	messagesProcessed := sub1Stats["messages_processed"].(uint64)
	panicRecoveries := sub1Stats["panic_recoveries"].(uint64)

	t.Logf("Timeout errors: %d", timeoutErrors)
	t.Logf("Messages failed: %d", messagesFailed)
	t.Logf("Messages processed: %d", messagesProcessed)
	t.Logf("Panic recoveries: %d", panicRecoveries)

	// Should have either timeout errors or failed messages
	assert.True(t, timeoutErrors > 0 || messagesFailed > 0, "Should have timeout errors or failed messages")
}
