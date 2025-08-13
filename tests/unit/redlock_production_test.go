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

	"github.com/SeaSBee/go-patternx/patternx/lock"
)

// ProductionMockLockClient implements LockClient for production testing
type ProductionMockLockClient struct {
	mu               sync.RWMutex
	store            map[string][]byte
	ttlStore         map[string]time.Time
	shouldFail       bool
	delay            time.Duration
	networkPartition bool
}

func NewProductionMockLockClient() *ProductionMockLockClient {
	return &ProductionMockLockClient{
		store:    make(map[string][]byte),
		ttlStore: make(map[string]time.Time),
	}
}

func (m *ProductionMockLockClient) Set(ctx context.Context, key string, value []byte, ttl time.Duration) error {
	if m.shouldFail {
		return errors.New("mock client failure")
	}

	if m.networkPartition {
		return errors.New("network partition")
	}

	if m.delay > 0 {
		select {
		case <-time.After(m.delay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Simulate Redis SET NX behavior - only set if key doesn't exist
	if _, exists := m.store[key]; exists {
		// Key already exists - lock acquisition failed
		return errors.New("key already exists")
	}

	m.store[key] = value
	m.ttlStore[key] = time.Now().Add(ttl)
	return nil
}

func (m *ProductionMockLockClient) Del(ctx context.Context, key string) error {
	if m.shouldFail {
		return errors.New("mock client failure")
	}

	if m.networkPartition {
		return errors.New("network partition")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	delete(m.store, key)
	delete(m.ttlStore, key)
	return nil
}

func (m *ProductionMockLockClient) Get(ctx context.Context, key string) ([]byte, error) {
	if m.shouldFail {
		return nil, errors.New("mock client failure")
	}

	if m.networkPartition {
		return nil, errors.New("network partition")
	}

	// Special case for health check
	if key == "__health_check__" {
		return []byte("healthy"), nil
	}

	m.mu.RLock()
	defer m.mu.RUnlock()

	value, exists := m.store[key]
	if !exists {
		return nil, errors.New("key not found")
	}

	// Check if expired
	if expiry, exists := m.ttlStore[key]; exists && time.Now().After(expiry) {
		delete(m.store, key)
		delete(m.ttlStore, key)
		return nil, errors.New("key expired")
	}

	return value, nil
}

func (m *ProductionMockLockClient) Eval(ctx context.Context, script string, keys []string, args ...interface{}) (interface{}, error) {
	if m.shouldFail {
		return nil, errors.New("mock client failure")
	}

	if m.networkPartition {
		return nil, errors.New("network partition")
	}

	if len(keys) == 0 {
		return nil, errors.New("no keys provided")
	}

	key := keys[0]
	if len(args) == 0 {
		return nil, errors.New("no args provided")
	}

	expectedValue := args[0].(string)

	m.mu.Lock()
	defer m.mu.Unlock()

	currentValue, exists := m.store[key]
	if !exists {
		return 0, nil
	}

	// Check if expired
	if expiry, exists := m.ttlStore[key]; exists && time.Now().After(expiry) {
		delete(m.store, key)
		delete(m.ttlStore, key)
		return 0, nil
	}

	if string(currentValue) == expectedValue {
		// Check if this is a release script (contains "del") or extension script (contains "expire")
		if strings.Contains(script, "del") {
			// Simulate DEL operation
			delete(m.store, key)
			delete(m.ttlStore, key)
			return 1, nil
		} else if strings.Contains(script, "expire") {
			// Simulate EXPIRE operation
			if len(args) >= 2 {
				ttlSeconds := args[1].(int)
				m.ttlStore[key] = time.Now().Add(time.Duration(ttlSeconds) * time.Second)
				return 1, nil
			}
			return 0, nil
		}
		return 1, nil
	}

	return 0, nil
}

func (m *ProductionMockLockClient) SetShouldFail(shouldFail bool) {
	m.shouldFail = shouldFail
}

func (m *ProductionMockLockClient) SetDelay(delay time.Duration) {
	m.delay = delay
}

func (m *ProductionMockLockClient) SetNetworkPartition(partition bool) {
	m.networkPartition = partition
}

// TestRedlockProductionConfigValidation tests comprehensive configuration validation
func TestRedlockProductionConfigValidation(t *testing.T) {
	clients := []lock.LockClient{NewProductionMockLockClient(), NewProductionMockLockClient(), NewProductionMockLockClient()}

	tests := []struct {
		name        string
		config      *lock.Config
		expectError bool
		errorMsg    string
	}{
		{
			name:        "Valid Default Config",
			config:      lock.DefaultConfig(clients),
			expectError: false,
		},
		{
			name:        "Valid Conservative Config",
			config:      lock.ConservativeConfig(clients),
			expectError: false,
		},
		{
			name:        "Valid Enterprise Config",
			config:      lock.EnterpriseConfig(clients),
			expectError: false,
		},
		{
			name:        "Nil Config",
			config:      nil,
			expectError: true,
			errorMsg:    "configuration cannot be nil",
		},
		{
			name: "Empty Clients",
			config: &lock.Config{
				Clients:       []lock.LockClient{},
				Quorum:        0,
				RetryDelay:    100 * time.Millisecond,
				MaxRetries:    3,
				DriftFactor:   0.01,
				EnableMetrics: true,
			},
			expectError: true,
			errorMsg:    "at least one client is required",
		},
		{
			name: "Nil Client",
			config: &lock.Config{
				Clients:       []lock.LockClient{nil, NewProductionMockLockClient()},
				Quorum:        0,
				RetryDelay:    100 * time.Millisecond,
				MaxRetries:    3,
				DriftFactor:   0.01,
				EnableMetrics: true,
			},
			expectError: true,
			errorMsg:    "client at index 0 cannot be nil",
		},
		{
			name: "Invalid Quorum - Negative",
			config: &lock.Config{
				Clients:       clients,
				Quorum:        -1,
				RetryDelay:    100 * time.Millisecond,
				MaxRetries:    3,
				DriftFactor:   0.01,
				EnableMetrics: true,
			},
			expectError: true,
			errorMsg:    "quorum cannot be negative",
		},
		{
			name: "Invalid Quorum - Too High",
			config: &lock.Config{
				Clients:       clients,
				Quorum:        5,
				RetryDelay:    100 * time.Millisecond,
				MaxRetries:    3,
				DriftFactor:   0.01,
				EnableMetrics: true,
			},
			expectError: true,
			errorMsg:    "quorum cannot be greater than number of clients",
		},
		{
			name: "Invalid RetryDelay - Too High",
			config: &lock.Config{
				Clients:       clients,
				Quorum:        0,
				RetryDelay:    20 * time.Second,
				MaxRetries:    3,
				DriftFactor:   0.01,
				EnableMetrics: true,
			},
			expectError: true,
			errorMsg:    "retry delay must be between 1ms and 10s",
		},
		{
			name: "Invalid MaxRetries - Too High",
			config: &lock.Config{
				Clients:       clients,
				Quorum:        0,
				RetryDelay:    100 * time.Millisecond,
				MaxRetries:    200,
				DriftFactor:   0.01,
				EnableMetrics: true,
			},
			expectError: true,
			errorMsg:    "max retries must be between 0 and 100",
		},
		{
			name: "Invalid DriftFactor - Too High",
			config: &lock.Config{
				Clients:       clients,
				Quorum:        0,
				RetryDelay:    100 * time.Millisecond,
				MaxRetries:    3,
				DriftFactor:   0.5,
				EnableMetrics: true,
			},
			expectError: true,
			errorMsg:    "drift factor must be between 0.001000 and 0.100000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			redlock, err := lock.NewRedlock(tt.config)

			if tt.expectError {
				if err == nil {
					t.Errorf("Expected error but got none")
					return
				}
				if tt.errorMsg != "" && !containsString(err.Error(), tt.errorMsg) {
					t.Errorf("Expected error message containing '%s', got '%s'", tt.errorMsg, err.Error())
				}
			} else {
				if err != nil {
					t.Errorf("Unexpected error: %v", err)
					return
				}
				if redlock == nil {
					t.Error("Expected Redlock instance but got nil")
				}
				defer redlock.Close()
			}
		})
	}
}

// TestRedlockProductionInputValidation tests input validation for Redlock operations
func TestRedlockProductionInputValidation(t *testing.T) {
	clients := []lock.LockClient{NewProductionMockLockClient(), NewProductionMockLockClient(), NewProductionMockLockClient()}
	config := lock.DefaultConfig(clients)
	redlock, err := lock.NewRedlock(config)
	if err != nil {
		t.Fatalf("Failed to create Redlock: %v", err)
	}
	defer redlock.Close()

	// Test nil context
	_, err = redlock.Lock(nil, "test-resource", 1*time.Second)
	if err == nil {
		t.Error("Expected error for nil context")
	}

	// Test empty resource name
	_, err = redlock.Lock(context.Background(), "", 1*time.Second)
	if err == nil {
		t.Error("Expected error for empty resource name")
	}

	// Test invalid TTL - too short
	_, err = redlock.Lock(context.Background(), "test-resource", 1*time.Nanosecond)
	if err == nil {
		t.Error("Expected error for too short TTL")
	}

	// Test invalid TTL - too long
	_, err = redlock.Lock(context.Background(), "test-resource", 48*time.Hour)
	if err == nil {
		t.Error("Expected error for too long TTL")
	}

	// Test invalid timeout
	_, err = redlock.LockWithTimeout(context.Background(), "test-resource", 1*time.Second, 2*time.Hour)
	if err == nil {
		t.Error("Expected error for too long timeout")
	}

	// Test very long resource name
	longResource := string(make([]byte, 300))
	_, err = redlock.Lock(context.Background(), longResource, 1*time.Second)
	if err == nil {
		t.Error("Expected error for too long resource name")
	}
}

// TestRedlockProductionConcurrencySafety tests concurrency safety
func TestRedlockProductionConcurrencySafety(t *testing.T) {
	clients := []lock.LockClient{NewProductionMockLockClient(), NewProductionMockLockClient(), NewProductionMockLockClient()}
	config := &lock.Config{
		Clients:       clients,
		Quorum:        0,
		RetryDelay:    10 * time.Millisecond,
		MaxRetries:    3,
		DriftFactor:   0.01,
		EnableMetrics: true,
	}

	redlock, err := lock.NewRedlock(config)
	if err != nil {
		t.Fatalf("Failed to create Redlock: %v", err)
	}
	defer redlock.Close()

	numGoroutines := 10
	resource := "concurrent-resource"
	var acquiredLocks int64
	var failedAcquisitions int64

	var wg sync.WaitGroup

	// Start concurrent lock acquisitions
	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			lock, err := redlock.Lock(context.Background(), resource, 500*time.Millisecond)
			if err != nil {
				atomic.AddInt64(&failedAcquisitions, 1)
				return
			}

			atomic.AddInt64(&acquiredLocks, 1)

			// Hold the lock for a longer time to create more contention
			time.Sleep(50 * time.Millisecond)

			// Release the lock
			err = lock.Unlock(context.Background())
			if err != nil {
				t.Errorf("Failed to unlock: %v", err)
			}
		}(i)
	}

	wg.Wait()

	// Verify metrics
	metrics := redlock.GetMetrics()
	if metrics == nil {
		t.Error("Expected metrics but got nil")
		return
	}

	t.Logf("Concurrency safety test completed:")
	t.Logf("  Total acquisitions: %d", metrics.TotalAcquisitions)
	t.Logf("  Successful acquisitions: %d", metrics.SuccessfulAcquisitions)
	t.Logf("  Failed acquisitions: %d", metrics.FailedAcquisitions)
	t.Logf("  Acquired locks: %d", acquiredLocks)
	t.Logf("  Failed acquisitions: %d", failedAcquisitions)

	// At least some locks should be acquired
	if metrics.SuccessfulAcquisitions == 0 {
		t.Error("Expected at least some successful acquisitions")
	}
}

// TestRedlockProductionLockAcquisition tests lock acquisition scenarios
func TestRedlockProductionLockAcquisition(t *testing.T) {
	clients := []lock.LockClient{NewProductionMockLockClient(), NewProductionMockLockClient(), NewProductionMockLockClient()}
	config := lock.DefaultConfig(clients)
	redlock, err := lock.NewRedlock(config)
	if err != nil {
		t.Fatalf("Failed to create Redlock: %v", err)
	}
	defer redlock.Close()

	// Test successful lock acquisition
	lock, err := redlock.Lock(context.Background(), "test-resource", 1*time.Second)
	if err != nil {
		t.Fatalf("Failed to acquire lock: %v", err)
	}

	if !lock.IsAcquired() {
		t.Error("Lock should be acquired")
	}

	if lock.GetResource() != "test-resource" {
		t.Errorf("Expected resource 'test-resource', got '%s'", lock.GetResource())
	}

	if lock.GetValue() == "" {
		t.Error("Lock value should not be empty")
	}

	// Test lock release
	err = lock.Unlock(context.Background())
	if err != nil {
		t.Fatalf("Failed to release lock: %v", err)
	}

	if lock.IsAcquired() {
		t.Error("Lock should not be acquired after release")
	}

	// Test double unlock
	err = lock.Unlock(context.Background())
	if err == nil {
		t.Error("Expected error for double unlock")
	}
}

// TestRedlockProductionLockExtension tests lock extension functionality
func TestRedlockProductionLockExtension(t *testing.T) {
	clients := []lock.LockClient{NewProductionMockLockClient(), NewProductionMockLockClient(), NewProductionMockLockClient()}
	config := lock.DefaultConfig(clients)
	redlock, err := lock.NewRedlock(config)
	if err != nil {
		t.Fatalf("Failed to create Redlock: %v", err)
	}
	defer redlock.Close()

	// Acquire a lock
	lock, err := redlock.Lock(context.Background(), "test-resource", 1*time.Second)
	if err != nil {
		t.Fatalf("Failed to acquire lock: %v", err)
	}

	// Extend the lock
	err = lock.Extend(context.Background(), 2*time.Second)
	if err != nil {
		t.Fatalf("Failed to extend lock: %v", err)
	}

	// Verify TTL was updated
	newTTL := lock.GetTTL()
	if newTTL <= 0 {
		t.Error("Lock TTL should be positive after extension")
	}

	// Release the lock
	err = lock.Unlock(context.Background())
	if err != nil {
		t.Fatalf("Failed to release lock: %v", err)
	}

	// Test extending released lock
	err = lock.Extend(context.Background(), 1*time.Second)
	if err == nil {
		t.Error("Expected error for extending released lock")
	}
}

// TestRedlockProductionContextCancellation tests context cancellation
func TestRedlockProductionContextCancellation(t *testing.T) {
	// Create clients with delay to ensure timeout is triggered
	client1 := NewProductionMockLockClient()
	client1.SetDelay(10 * time.Millisecond)
	client2 := NewProductionMockLockClient()
	client2.SetDelay(10 * time.Millisecond)
	client3 := NewProductionMockLockClient()
	client3.SetDelay(10 * time.Millisecond)

	clients := []lock.LockClient{client1, client2, client3}
	config := lock.DefaultConfig(clients)
	redlock, err := lock.NewRedlock(config)
	if err != nil {
		t.Fatalf("Failed to create Redlock: %v", err)
	}
	defer redlock.Close()

	// Test immediate context cancellation
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = redlock.Lock(ctx, "test-resource", 1*time.Second)
	if err == nil {
		t.Error("Expected error for cancelled context")
	}

	// Test timeout context with a very short timeout
	timeoutCtx, cancel := context.WithTimeout(context.Background(), 1*time.Millisecond)
	defer cancel()

	_, err = redlock.Lock(timeoutCtx, "test-resource", 1*time.Second)
	if err == nil {
		t.Error("Expected error for timeout context")
	}
}

// TestRedlockProductionHealthChecks tests health check functionality
func TestRedlockProductionHealthChecks(t *testing.T) {
	clients := []lock.LockClient{NewProductionMockLockClient(), NewProductionMockLockClient(), NewProductionMockLockClient()}
	config := lock.DefaultConfig(clients)
	redlock, err := lock.NewRedlock(config)
	if err != nil {
		t.Fatalf("Failed to create Redlock: %v", err)
	}
	defer redlock.Close()

	// Initially should be healthy
	if !redlock.IsHealthy() {
		t.Error("Expected Redlock to be healthy initially")
	}

	// Check health status details
	health := redlock.GetHealthStatus()
	if health["is_healthy"] != true {
		t.Error("Health status should show healthy")
	}

	if health["is_closed"] != false {
		t.Error("Health status should show not closed")
	}

	clientCount := health["client_count"].(int)
	if clientCount != 3 {
		t.Errorf("Expected client count 3, got %d", clientCount)
	}

	quorum := health["quorum"].(int)
	if quorum != 2 {
		t.Errorf("Expected quorum 2, got %d", quorum)
	}

	t.Logf("Health check test:")
	t.Logf("  Is healthy: %v", health["is_healthy"])
	t.Logf("  Is closed: %v", health["is_closed"])
	t.Logf("  Client count: %v", health["client_count"])
	t.Logf("  Quorum: %v", health["quorum"])
}

// TestRedlockProductionMetricsAccuracy tests metrics accuracy
func TestRedlockProductionMetricsAccuracy(t *testing.T) {
	clients := []lock.LockClient{NewProductionMockLockClient(), NewProductionMockLockClient(), NewProductionMockLockClient()}
	config := lock.DefaultConfig(clients)
	redlock, err := lock.NewRedlock(config)
	if err != nil {
		t.Fatalf("Failed to create Redlock: %v", err)
	}
	defer redlock.Close()

	// Acquire and release a single lock first
	lock, err := redlock.Lock(context.Background(), "test-resource-single", 1*time.Second)
	if err != nil {
		t.Fatalf("Failed to acquire single lock: %v", err)
	}

	// Release the lock
	err = lock.Unlock(context.Background())
	if err != nil {
		t.Fatalf("Failed to release single lock: %v", err)
	}

	// Now acquire and release several locks
	for i := 0; i < 5; i++ {
		lock, err := redlock.Lock(context.Background(), fmt.Sprintf("test-resource-%d", i), 1*time.Second)
		if err != nil {
			t.Fatalf("Failed to acquire lock %d: %v", i, err)
		}

		// Extend the lock
		err = lock.Extend(context.Background(), 2*time.Second)
		if err != nil {
			t.Fatalf("Failed to extend lock %d: %v", i, err)
		}

		// Release the lock
		err = lock.Unlock(context.Background())
		if err != nil {
			t.Fatalf("Failed to release lock %d: %v", i, err)
		}
	}

	// Verify metrics accuracy
	metrics := redlock.GetMetrics()
	expectedAcquisitions := int64(6) // 1 single + 5 in loop
	expectedExtensions := int64(5)   // Only the 5 in loop
	expectedReleases := int64(6)     // 1 single + 5 in loop

	if metrics.TotalAcquisitions != expectedAcquisitions {
		t.Errorf("Expected %d total acquisitions, got %d", expectedAcquisitions, metrics.TotalAcquisitions)
	}

	if metrics.SuccessfulAcquisitions != expectedAcquisitions {
		t.Errorf("Expected %d successful acquisitions, got %d", expectedAcquisitions, metrics.SuccessfulAcquisitions)
	}

	if metrics.TotalExtensions != expectedExtensions {
		t.Errorf("Expected %d total extensions, got %d", expectedExtensions, metrics.TotalExtensions)
	}

	if metrics.SuccessfulExtensions != expectedExtensions {
		t.Errorf("Expected %d successful extensions, got %d", expectedExtensions, metrics.SuccessfulExtensions)
	}

	if metrics.TotalReleases != expectedReleases {
		t.Errorf("Expected %d total releases, got %d", expectedReleases, metrics.TotalReleases)
	}

	if metrics.SuccessfulReleases != expectedReleases {
		t.Errorf("Expected %d successful releases, got %d", expectedReleases, metrics.SuccessfulReleases)
	}

	t.Logf("Metrics accuracy test:")
	t.Logf("  Total acquisitions: %d", metrics.TotalAcquisitions)
	t.Logf("  Successful acquisitions: %d", metrics.SuccessfulAcquisitions)
	t.Logf("  Total extensions: %d", metrics.TotalExtensions)
	t.Logf("  Successful extensions: %d", metrics.SuccessfulExtensions)
	t.Logf("  Total releases: %d", metrics.TotalReleases)
	t.Logf("  Successful releases: %d", metrics.SuccessfulReleases)
}

// TestRedlockProductionGracefulShutdown tests graceful shutdown
func TestRedlockProductionGracefulShutdown(t *testing.T) {
	clients := []lock.LockClient{NewProductionMockLockClient(), NewProductionMockLockClient(), NewProductionMockLockClient()}
	config := lock.DefaultConfig(clients)
	redlock, err := lock.NewRedlock(config)
	if err != nil {
		t.Fatalf("Failed to create Redlock: %v", err)
	}

	// Initially should be healthy
	if !redlock.IsHealthy() {
		t.Error("Expected Redlock to be healthy initially")
	}

	// Close the Redlock
	err = redlock.Close()
	if err != nil {
		t.Errorf("Unexpected error during shutdown: %v", err)
	}

	// Verify Redlock is closed
	if redlock.IsHealthy() {
		t.Error("Expected Redlock to be unhealthy after close")
	}

	// Try to acquire a lock after close - should fail
	_, err = redlock.Lock(context.Background(), "test-resource", 1*time.Second)
	if err == nil {
		t.Error("Expected error when acquiring lock on closed Redlock")
	}

	t.Logf("Graceful shutdown test completed successfully")
}

// TestRedlockProductionTryLock tests non-blocking lock acquisition
func TestRedlockProductionTryLock(t *testing.T) {
	clients := []lock.LockClient{NewProductionMockLockClient(), NewProductionMockLockClient(), NewProductionMockLockClient()}
	config := lock.DefaultConfig(clients)
	redlock, err := lock.NewRedlock(config)
	if err != nil {
		t.Fatalf("Failed to create Redlock: %v", err)
	}
	defer redlock.Close()

	// Try to acquire a lock without blocking
	lock, err := redlock.TryLock(context.Background(), "test-resource", 1*time.Second)
	if err != nil {
		t.Fatalf("Failed to try lock: %v", err)
	}

	if !lock.IsAcquired() {
		t.Error("TryLock should acquire the lock")
	}

	// Try to acquire the same resource again - should fail
	_, err = redlock.TryLock(context.Background(), "test-resource", 1*time.Second)
	if err == nil {
		t.Error("Expected error when trying to acquire already locked resource")
	}

	// Release the lock
	err = lock.Unlock(context.Background())
	if err != nil {
		t.Fatalf("Failed to release lock: %v", err)
	}

	// Now should be able to acquire it again
	lock2, err := redlock.TryLock(context.Background(), "test-resource", 1*time.Second)
	if err != nil {
		t.Fatalf("Failed to try lock after release: %v", err)
	}

	if !lock2.IsAcquired() {
		t.Error("TryLock should acquire the lock after release")
	}

	// Release the second lock
	err = lock2.Unlock(context.Background())
	if err != nil {
		t.Fatalf("Failed to release second lock: %v", err)
	}
}

// TestRedlockProductionLockWithTimeout tests lock acquisition with timeout
func TestRedlockProductionLockWithTimeout(t *testing.T) {
	clients := []lock.LockClient{NewProductionMockLockClient(), NewProductionMockLockClient(), NewProductionMockLockClient()}
	config := lock.DefaultConfig(clients)
	redlock, err := lock.NewRedlock(config)
	if err != nil {
		t.Fatalf("Failed to create Redlock: %v", err)
	}
	defer redlock.Close()

	// Test successful lock acquisition with timeout
	lock, err := redlock.LockWithTimeout(context.Background(), "test-resource", 1*time.Second, 5*time.Second)
	if err != nil {
		t.Fatalf("Failed to acquire lock with timeout: %v", err)
	}

	if !lock.IsAcquired() {
		t.Error("Lock should be acquired")
	}

	// Release the lock
	err = lock.Unlock(context.Background())
	if err != nil {
		t.Fatalf("Failed to release lock: %v", err)
	}

	// Test timeout scenario
	_, err = redlock.LockWithTimeout(context.Background(), "test-resource", 1*time.Second, 1*time.Microsecond)
	if err == nil {
		t.Error("Expected error for timeout scenario")
	}
}

// TestRedlockProductionNetworkPartition tests behavior during network partitions
func TestRedlockProductionNetworkPartition(t *testing.T) {
	clients := []lock.LockClient{NewProductionMockLockClient(), NewProductionMockLockClient(), NewProductionMockLockClient()}
	config := lock.DefaultConfig(clients)
	redlock, err := lock.NewRedlock(config)
	if err != nil {
		t.Fatalf("Failed to create Redlock: %v", err)
	}
	defer redlock.Close()

	// Simulate network partition on one client
	mockClient := clients[0].(*ProductionMockLockClient)
	mockClient.SetNetworkPartition(true)

	// Try to acquire lock - should still succeed with remaining clients
	lock, err := redlock.Lock(context.Background(), "test-resource", 1*time.Second)
	if err != nil {
		t.Fatalf("Failed to acquire lock during network partition: %v", err)
	}

	if !lock.IsAcquired() {
		t.Error("Lock should be acquired despite network partition")
	}

	// Release the lock
	err = lock.Unlock(context.Background())
	if err != nil {
		t.Fatalf("Failed to release lock: %v", err)
	}

	// Simulate network partition on all clients
	for _, client := range clients {
		mockClient := client.(*ProductionMockLockClient)
		mockClient.SetNetworkPartition(true)
	}

	// Try to acquire lock - should fail
	_, err = redlock.Lock(context.Background(), "test-resource", 1*time.Second)
	if err == nil {
		t.Error("Expected error when all clients have network partition")
	}
}

// TestRedlockProductionLockExpiration tests lock expiration behavior
func TestRedlockProductionLockExpiration(t *testing.T) {
	clients := []lock.LockClient{NewProductionMockLockClient(), NewProductionMockLockClient(), NewProductionMockLockClient()}
	config := lock.DefaultConfig(clients)
	redlock, err := lock.NewRedlock(config)
	if err != nil {
		t.Fatalf("Failed to create Redlock: %v", err)
	}
	defer redlock.Close()

	// Acquire a lock with very short TTL
	lock, err := redlock.Lock(context.Background(), "test-resource", 10*time.Millisecond)
	if err != nil {
		t.Fatalf("Failed to acquire lock: %v", err)
	}

	// Wait for lock to expire
	time.Sleep(20 * time.Millisecond)

	// Try to extend expired lock - should fail
	err = lock.Extend(context.Background(), 1*time.Second)
	if err == nil {
		t.Error("Expected error when extending expired lock")
	}

	// Try to unlock expired lock - should fail
	err = lock.Unlock(context.Background())
	if err == nil {
		t.Error("Expected error when unlocking expired lock")
	}
}

// TestRedlockProductionQuorumFailure tests quorum failure scenarios
func TestRedlockProductionQuorumFailure(t *testing.T) {
	clients := []lock.LockClient{NewProductionMockLockClient(), NewProductionMockLockClient(), NewProductionMockLockClient()}
	config := &lock.Config{
		Clients:       clients,
		Quorum:        3, // Require all clients
		RetryDelay:    100 * time.Millisecond,
		MaxRetries:    3,
		DriftFactor:   0.01,
		EnableMetrics: true,
	}

	redlock, err := lock.NewRedlock(config)
	if err != nil {
		t.Fatalf("Failed to create Redlock: %v", err)
	}
	defer redlock.Close()

	// Make one client fail
	mockClient := clients[0].(*ProductionMockLockClient)
	mockClient.SetShouldFail(true)

	// Try to acquire lock - should fail due to quorum not reached
	_, err = redlock.Lock(context.Background(), "test-resource", 1*time.Second)
	if err == nil {
		t.Error("Expected error when quorum not reached")
	}

	// Verify metrics show failed acquisition
	metrics := redlock.GetMetrics()
	if metrics.FailedAcquisitions == 0 {
		t.Error("Expected failed acquisitions in metrics")
	}
}

// Helper function to check if string contains substring
func containsString(s, substr string) bool {
	return len(s) >= len(substr) && (s == substr || len(s) > len(substr) && (s[:len(substr)] == substr || s[len(s)-len(substr):] == substr || containsSubstring(s, substr)))
}

func containsSubstring(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
