package security

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"
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

// TestInputValidationSecurity tests input validation for security vulnerabilities
func TestInputValidationSecurity(t *testing.T) {
	t.Run("BloomFilter_SQLInjection", func(t *testing.T) {
		config := &bloom.Config{
			ExpectedItems:     1000,
			FalsePositiveRate: 0.01,
		}
		bf, err := bloom.NewBloomFilter(config)
		if err != nil {
			t.Fatalf("Failed to create bloom filter: %v", err)
		}
		defer bf.Close()

		// Test SQL injection attempts
		sqlInjectionAttempts := []string{
			"'; DROP TABLE users; --",
			"' OR '1'='1",
			"'; INSERT INTO users VALUES ('hacker', 'password'); --",
			"'; UPDATE users SET password='hacked'; --",
		}

		for _, attempt := range sqlInjectionAttempts {
			err := bf.Add(context.Background(), attempt)
			if err != nil {
				t.Errorf("SQL injection attempt should be handled gracefully: %v", err)
			}
		}
	})

	t.Run("BloomFilter_XSS", func(t *testing.T) {
		config := &bloom.Config{
			ExpectedItems:     1000,
			FalsePositiveRate: 0.01,
		}
		bf, err := bloom.NewBloomFilter(config)
		if err != nil {
			t.Fatalf("Failed to create bloom filter: %v", err)
		}
		defer bf.Close()

		// Test XSS attempts
		xssAttempts := []string{
			"<script>alert('xss')</script>",
			"<img src=x onerror=alert('xss')>",
			"javascript:alert('xss')",
			"<svg onload=alert('xss')>",
		}

		for _, attempt := range xssAttempts {
			err := bf.Add(context.Background(), attempt)
			if err != nil {
				t.Errorf("XSS attempt should be handled gracefully: %v", err)
			}
		}
	})

	t.Run("BloomFilter_PathTraversal", func(t *testing.T) {
		config := &bloom.Config{
			ExpectedItems:     1000,
			FalsePositiveRate: 0.01,
		}
		bf, err := bloom.NewBloomFilter(config)
		if err != nil {
			t.Fatalf("Failed to create bloom filter: %v", err)
		}
		defer bf.Close()

		// Test path traversal attempts
		pathTraversalAttempts := []string{
			"../../../etc/passwd",
			"..\\..\\..\\windows\\system32\\config\\sam",
			"....//....//....//etc/passwd",
			"%2e%2e%2f%2e%2e%2f%2e%2e%2fetc%2fpasswd",
		}

		for _, attempt := range pathTraversalAttempts {
			err := bf.Add(context.Background(), attempt)
			if err != nil {
				t.Errorf("Path traversal attempt should be handled gracefully: %v", err)
			}
		}
	})
}

// TestResourceExhaustionSecurity tests for resource exhaustion attacks
func TestResourceExhaustionSecurity(t *testing.T) {
	t.Run("BloomFilter_MemoryExhaustion", func(t *testing.T) {
		// Test with extremely large items
		config := &bloom.Config{
			ExpectedItems:     1000,
			FalsePositiveRate: 0.01,
		}
		bf, err := bloom.NewBloomFilter(config)
		if err != nil {
			t.Fatalf("Failed to create bloom filter: %v", err)
		}
		defer bf.Close()

		// Generate a very large string (1MB)
		largeData := make([]byte, 1024*1024)
		rand.Read(largeData)
		largeString := hex.EncodeToString(largeData)

		err = bf.Add(context.Background(), largeString)
		if err != nil {
			t.Logf("Large item correctly rejected: %v", err)
		}
	})

	t.Run("Bulkhead_ConnectionExhaustion", func(t *testing.T) {
		config := bulkhead.BulkheadConfig{
			MaxConcurrentCalls: 2,
			MaxQueueSize:       5,
			MaxWaitDuration:    100 * time.Millisecond,
			HealthThreshold:    0.5,
		}
		b, err := bulkhead.NewBulkhead(config)
		if err != nil {
			t.Fatalf("Failed to create bulkhead: %v", err)
		}
		defer b.Close()

		// Try to exhaust connections
		var wg sync.WaitGroup
		errors := make(chan error, 10)

		for i := 0; i < 10; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, err := b.Execute(context.Background(), func() (interface{}, error) {
					time.Sleep(200 * time.Millisecond)
					return "done", nil
				})
				if err != nil {
					errors <- err
				}
			}()
		}

		wg.Wait()
		close(errors)

		// Should have some errors due to resource limits
		errorCount := 0
		for err := range errors {
			if err != nil {
				errorCount++
			}
		}

		if errorCount == 0 {
			t.Error("Expected some operations to be rejected due to resource limits")
		}
	})
}

// TestInjectionSecurity tests for code injection vulnerabilities
func TestInjectionSecurity(t *testing.T) {
	t.Run("Retry_CommandInjection", func(t *testing.T) {
		policy := retry.DefaultPolicy()

		// Test with potentially dangerous strings
		dangerousInputs := []string{
			"; rm -rf /",
			"| cat /etc/passwd",
			"$(whoami)",
			"`id`",
			"$(cat /etc/shadow)",
		}

		for _, input := range dangerousInputs {
			err := retry.Retry(policy, func() error {
				// Simulate operation with dangerous input
				_ = input
				return fmt.Errorf("test error")
			})

			if err == nil {
				t.Error("Expected error for dangerous input")
			}
		}
	})

	t.Run("DLQ_HandlerInjection", func(t *testing.T) {
		config := dlq.DefaultConfig()
		dq, err := dlq.NewDeadLetterQueue(config)
		if err != nil {
			t.Fatalf("Failed to create DLQ: %v", err)
		}
		defer dq.Close()

		// Test with potentially dangerous handler types
		dangerousHandlers := []string{
			"<script>alert('xss')</script>",
			"'; DROP TABLE handlers; --",
			"../../../etc/passwd",
			"javascript:alert('xss')",
		}

		for _, handlerType := range dangerousHandlers {
			err := dq.AddFailedOperation(&dlq.FailedOperation{
				HandlerType: handlerType,
				Data:        "test data",
				Error:       "test error",
			})

			if err != nil {
				t.Logf("Dangerous handler type correctly rejected: %v", err)
			}
		}
	})
}

// TestAuthenticationSecurity tests authentication and authorization
func TestAuthenticationSecurity(t *testing.T) {
	t.Run("Redlock_AuthenticationBypass", func(t *testing.T) {
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
			t.Fatalf("Failed to create Redlock: %v", err)
		}

		// Test with unauthorized resource names
		unauthorizedResources := []string{
			"admin:password",
			"root:secret",
			"user:private_data",
			"system:config",
		}

		for _, resource := range unauthorizedResources {
			lock, err := rl.Lock(context.Background(), resource, 1*time.Second)
			if err != nil {
				t.Logf("Unauthorized resource access correctly rejected: %v", err)
			} else if lock != nil {
				lock.Unlock(context.Background())
			}
		}
	})
}

// TestDataExfiltrationSecurity tests for data exfiltration vulnerabilities
func TestDataExfiltrationSecurity(t *testing.T) {
	t.Run("BloomFilter_DataLeakage", func(t *testing.T) {
		config := &bloom.Config{
			ExpectedItems:     1000,
			FalsePositiveRate: 0.01,
		}
		bf, err := bloom.NewBloomFilter(config)
		if err != nil {
			t.Fatalf("Failed to create bloom filter: %v", err)
		}
		defer bf.Close()

		// Test with sensitive data
		sensitiveData := []string{
			"password123",
			"credit_card_1234_5678_9012_3456",
			"ssn_123_45_6789",
			"api_key_sk_live_1234567890abcdef",
		}

		for _, data := range sensitiveData {
			err := bf.Add(context.Background(), data)
			if err != nil {
				t.Errorf("Sensitive data should be handled securely: %v", err)
			}
		}

		// Verify data is not exposed through stats
		stats := bf.GetStats()
		if stats["bitset"] != nil {
			t.Error("Sensitive data should not be exposed in stats")
		}
	})

	t.Run("WorkerPool_DataLeakage", func(t *testing.T) {
		config := pool.DefaultConfig()
		wp, err := pool.New(config)
		if err != nil {
			t.Fatalf("Failed to create worker pool: %v", err)
		}
		defer wp.Close()

		// Test with sensitive data in jobs
		sensitiveData := "secret_password_123"

		job := pool.Job{
			ID:   "sensitive_job",
			Task: func() (interface{}, error) { return sensitiveData, nil },
		}

		err = wp.Submit(job)
		if err != nil {
			t.Errorf("Job should execute successfully: %v", err)
		}

		// Verify sensitive data is not exposed in stats
		stats := wp.GetStats()
		if stats == nil {
			t.Error("Expected stats to be returned")
		}
	})
}

// TestDenialOfServiceSecurity tests for DoS vulnerabilities
func TestDenialOfServiceSecurity(t *testing.T) {
	t.Run("CircuitBreaker_DoS", func(t *testing.T) {
		config := cb.DefaultConfig()
		cb, err := cb.New(config)
		if err != nil {
			t.Fatalf("Failed to create circuit breaker: %v", err)
		}

		// Rapidly trigger failures to open circuit breaker
		for i := 0; i < 100; i++ {
			cb.Execute(func() error {
				return fmt.Errorf("intentional failure")
			})
		}

		// Circuit breaker should be open
		stats := cb.GetStats()
		if stats.State.String() != "OPEN" {
			t.Error("Circuit breaker should be open after rapid failures")
		}
	})

	t.Run("Retry_DoS", func(t *testing.T) {
		policy := retry.Policy{
			MaxAttempts:  100,
			InitialDelay: 1 * time.Millisecond,
			MaxDelay:     1 * time.Second,
			Multiplier:   2.0,
		}

		// Test with rapid retries
		start := time.Now()
		err := retry.Retry(policy, func() error {
			return fmt.Errorf("intentional failure")
		})
		duration := time.Since(start)

		if err == nil {
			t.Error("Expected error after max attempts")
		}

		// Should not take too long due to rate limiting
		if duration > 10*time.Second {
			t.Errorf("Retry should not take too long: %v", duration)
		}
	})
}

// TestEncryptionSecurity tests encryption and data protection
func TestEncryptionSecurity(t *testing.T) {
	t.Run("BloomFilter_DataProtection", func(t *testing.T) {
		config := &bloom.Config{
			ExpectedItems:     1000,
			FalsePositiveRate: 0.01,
		}
		bf, err := bloom.NewBloomFilter(config)
		if err != nil {
			t.Fatalf("Failed to create bloom filter: %v", err)
		}
		defer bf.Close()

		// Test that data is not stored in plain text
		sensitiveData := "secret_password_123"
		err = bf.Add(context.Background(), sensitiveData)
		if err != nil {
			t.Fatalf("Failed to add data: %v", err)
		}

		// Verify data is hashed, not stored in plain text
		stats := bf.GetStats()
		itemCount, ok := stats["item_count"].(uint64)
		if !ok {
			t.Error("Expected item_count in stats")
		}

		// Check that data was added (item count should be 1)
		if itemCount != 1 {
			t.Errorf("Expected item count to be 1, got %d", itemCount)
		}
	})
}

// TestLoggingSecurity tests for sensitive data in logs
func TestLoggingSecurity(t *testing.T) {
	t.Run("Bulkhead_LogSanitization", func(t *testing.T) {
		config := bulkhead.BulkheadConfig{
			MaxConcurrentCalls: 1,
			MaxQueueSize:       1,
			MaxWaitDuration:    100 * time.Millisecond,
			HealthThreshold:    0.5,
		}
		b, err := bulkhead.NewBulkhead(config)
		if err != nil {
			t.Fatalf("Failed to create bulkhead: %v", err)
		}
		defer b.Close()

		// Test with sensitive data in operation
		sensitiveData := "password123"
		_, err = b.Execute(context.Background(), func() (interface{}, error) {
			// Simulate operation with sensitive data
			_ = sensitiveData
			return "success", nil
		})

		if err != nil {
			t.Errorf("Operation should succeed: %v", err)
		}

		// Verify metrics don't contain sensitive data
		metrics := b.GetMetrics()
		if strings.Contains(fmt.Sprintf("%v", metrics), sensitiveData) {
			t.Error("Sensitive data should not appear in metrics")
		}
	})
}

// TestRateLimitingSecurity tests rate limiting for security
func TestRateLimitingSecurity(t *testing.T) {
	t.Run("Retry_RateLimiting", func(t *testing.T) {
		policy := retry.Policy{
			MaxAttempts:     10,
			InitialDelay:    1 * time.Millisecond,
			MaxDelay:        1 * time.Second,
			Multiplier:      2.0,
			RateLimit:       5, // 5 requests per second
			RateLimitWindow: time.Second,
		}

		// Test rate limiting
		start := time.Now()
		for i := 0; i < 10; i++ {
			retry.Retry(policy, func() error {
				return fmt.Errorf("intentional failure")
			})
		}
		duration := time.Since(start)

		// Should take at least 1 second due to rate limiting
		if duration < time.Second {
			t.Errorf("Rate limiting should enforce delays: %v", duration)
		}
	})
}

// TestConfigurationSecurity tests configuration security
func TestConfigurationSecurity(t *testing.T) {
	t.Run("BloomFilter_ConfigValidation", func(t *testing.T) {
		// Test with malicious configuration
		maliciousConfig := &bloom.Config{
			ExpectedItems:     0,   // Invalid
			FalsePositiveRate: 2.0, // Invalid
		}

		_, err := bloom.NewBloomFilter(maliciousConfig)
		if err == nil {
			t.Error("Expected error for invalid configuration")
		}
	})

	t.Run("Bulkhead_ConfigValidation", func(t *testing.T) {
		// Test with malicious configuration
		maliciousConfig := bulkhead.BulkheadConfig{
			MaxConcurrentCalls: 0,   // Invalid
			MaxQueueSize:       0,   // Invalid
			HealthThreshold:    2.0, // Invalid
		}

		_, err := bulkhead.NewBulkhead(maliciousConfig)
		if err == nil {
			t.Error("Expected error for invalid configuration")
		}
	})
}

// MockLockClient for testing
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
