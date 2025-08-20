package unit

import (
	"context"
	"testing"
	"time"

	"github.com/SeaSBee/go-patternx"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestContextAwareHandler tests that handlers respect context cancellation
func TestContextAwareHandler(t *testing.T) {
	store := NewMockStore()
	config := patternx.DefaultConfigPubSub(store)
	config.OperationTimeout = 50 * time.Millisecond // Short timeout
	ps, err := patternx.NewPubSub(config)
	require.NoError(t, err)
	defer ps.Close(context.Background())

	err = ps.CreateTopic(context.Background(), "context-test")
	require.NoError(t, err)

	// Create handler that respects context
	handler := func(ctx context.Context, msg *patternx.MessagePubSub) error {
		t.Logf("Handler started for message: %s", msg.ID)

		// Check context periodically
		for i := 0; i < 10; i++ {
			select {
			case <-ctx.Done():
				t.Logf("Handler cancelled for message: %s", msg.ID)
				return ctx.Err()
			default:
				time.Sleep(20 * time.Millisecond) // Sleep in small increments
			}
		}

		t.Logf("Handler completed for message: %s", msg.ID)
		return nil
	}

	_, err = ps.Subscribe(context.Background(), "context-test", "sub-1", handler, &patternx.MessageFilter{})
	require.NoError(t, err)

	// Publish message
	data := []byte("context test message")
	err = ps.Publish(context.Background(), "context-test", data, nil)
	require.NoError(t, err)

	// Wait for processing
	time.Sleep(300 * time.Millisecond)

	// Check stats
	stats := ps.GetStats()
	subscriptionStats := stats["subscriptions"].(map[string]interface{})
	sub1Stats := subscriptionStats["sub-1"].(map[string]interface{})

	timeoutErrors := sub1Stats["timeout_errors"].(uint64)
	messagesFailed := sub1Stats["messages_failed"].(uint64)

	t.Logf("Timeout errors: %d", timeoutErrors)
	t.Logf("Messages failed: %d", messagesFailed)

	// Should have timeout errors since handler respects context
	assert.Greater(t, timeoutErrors, uint64(0), "Should have timeout errors when handler respects context")
}
