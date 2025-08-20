package integration

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/SeaSBee/go-patternx"
)

// MockService simulates a real service that can fail
type MockService struct {
	failureRate float64
	latency     time.Duration
	mu          sync.Mutex
	callCount   int
}

func NewMockService(failureRate float64, latency time.Duration) *MockService {
	return &MockService{
		failureRate: failureRate,
		latency:     latency,
	}
}

func (s *MockService) Call(ctx context.Context) (string, error) {
	s.mu.Lock()
	s.callCount++
	s.mu.Unlock()

	// Simulate latency
	select {
	case <-time.After(s.latency):
	case <-ctx.Done():
		return "", ctx.Err()
	}

	// Simulate failures
	if s.failureRate > 0 && float64(s.callCount%10)/10.0 < s.failureRate {
		return "", errors.New("service temporarily unavailable")
	}

	return fmt.Sprintf("success-response-%d", s.callCount), nil
}

func (s *MockService) GetCallCount() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.callCount
}

// TestBulkheadWithCircuitBreaker tests the integration of bulkhead and circuit breaker patterns
func TestBulkheadWithCircuitBreaker(t *testing.T) {
	// Create a service with high failure rate
	service := NewMockService(0.7, 100*time.Millisecond)

	// Create circuit breaker with aggressive settings
	cbConfig := patternx.ConfigCircuitBreaker{
		Threshold:   3,
		Timeout:     200 * time.Millisecond,
		HalfOpenMax: 2,
	}
	circuitBreaker, err := patternx.NewCircuitBreaker(cbConfig)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	// Create bulkhead for resource isolation
	bhConfig := patternx.BulkheadConfig{
		MaxConcurrentCalls: 5,
		MaxWaitDuration:    1 * time.Second,
		MaxQueueSize:       10,
		HealthThreshold:    0.5,
	}
	bulkhead, err := patternx.NewBulkhead(bhConfig)
	if err != nil {
		t.Fatalf("Failed to create bulkhead: %v", err)
	}
	defer bulkhead.Close()

	// Test scenario: Multiple concurrent calls with circuit breaker protection
	var wg sync.WaitGroup
	numCalls := 20
	results := make([]string, numCalls)
	errors := make([]error, numCalls)

	start := time.Now()

	for i := 0; i < numCalls; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			// Execute with bulkhead protection and circuit breaker
			result, err := bulkhead.Execute(context.Background(), func() (interface{}, error) {
				err := circuitBreaker.Execute(func() error {
					response, err := service.Call(context.Background())
					if err != nil {
						return err
					}
					results[id] = response
					return nil
				})
				return nil, err
			})

			if err != nil {
				errors[id] = err
			} else if result != nil {
				// Handle the result if needed
			}
		}(i)
	}

	wg.Wait()
	duration := time.Since(start)

	// Analyze results
	successCount := 0
	circuitOpenCount := 0
	bulkheadRejectedCount := 0
	serviceErrorCount := 0

	for _, err := range errors {
		if err == nil {
			successCount++
		} else {
			switch err.Error() {
			case "circuit breaker is open":
				circuitOpenCount++
			case "bulkhead timeout", "bulkhead queue full":
				bulkheadRejectedCount++
			default:
				serviceErrorCount++
			}
		}
	}

	// Verify circuit breaker behavior
	cbStats := circuitBreaker.GetStats()
	if cbStats.State == patternx.StateOpen && circuitOpenCount == 0 {
		t.Error("Circuit breaker is open but no calls were rejected")
	}

	// Verify bulkhead behavior
	bhMetrics := bulkhead.GetMetrics()
	if bhMetrics.TotalCalls.Load() != int64(numCalls) {
		t.Errorf("Expected %d total calls, got %d", numCalls, bhMetrics.TotalCalls.Load())
	}

	// Verify service was called
	serviceCalls := service.GetCallCount()
	if serviceCalls == 0 {
		t.Error("Service was never called")
	}

	t.Logf("Integration test results:")
	t.Logf("  Duration: %v", duration)
	t.Logf("  Total calls: %d", numCalls)
	t.Logf("  Successful calls: %d", successCount)
	t.Logf("  Circuit breaker rejections: %d", circuitOpenCount)
	t.Logf("  Bulkhead rejections: %d", bulkheadRejectedCount)
	t.Logf("  Service errors: %d", serviceErrorCount)
	t.Logf("  Service calls made: %d", serviceCalls)
	t.Logf("  Circuit breaker state: %s", cbStats.State)
	t.Logf("  Circuit breaker failures: %d", cbStats.TotalFailures)
}

// TestBulkheadCircuitBreakerRecovery tests recovery scenarios
func TestBulkheadCircuitBreakerRecovery(t *testing.T) {
	// Create a service that starts failing then recovers
	service := NewMockService(0.9, 50*time.Millisecond)

	// Create circuit breaker
	cbConfig := patternx.ConfigCircuitBreaker{
		Threshold:   2,
		Timeout:     100 * time.Millisecond,
		HalfOpenMax: 1,
	}
	circuitBreaker, err := patternx.NewCircuitBreaker(cbConfig)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	// Create bulkhead
	bhConfig := patternx.BulkheadConfig{
		MaxConcurrentCalls: 3,
		MaxWaitDuration:    500 * time.Millisecond,
		MaxQueueSize:       5,
		HealthThreshold:    0.5,
	}
	bulkhead, err := patternx.NewBulkhead(bhConfig)
	if err != nil {
		t.Fatalf("Failed to create bulkhead: %v", err)
	}
	defer bulkhead.Close()

	// Phase 1: Cause circuit breaker to open
	t.Log("Phase 1: Causing circuit breaker to open")
	for i := 0; i < 5; i++ {
		err := circuitBreaker.Execute(func() error {
			_, err := service.Call(context.Background())
			return err
		})
		if err != nil && err.Error() == "circuit breaker is open" {
			t.Logf("Circuit breaker opened after %d calls", i+1)
			break
		}
	}

	// Verify circuit breaker is open
	cbStats := circuitBreaker.GetStats()
	if cbStats.State != patternx.StateOpen {
		t.Error("Circuit breaker should be open")
	}

	// Phase 2: Wait for circuit breaker to transition to half-open
	t.Log("Phase 2: Waiting for circuit breaker to transition to half-open")
	time.Sleep(150 * time.Millisecond)

	// Phase 3: Test recovery with successful calls
	t.Log("Phase 3: Testing recovery with successful calls")

	// Temporarily reduce failure rate for recovery
	service.failureRate = 0.0

	var wg sync.WaitGroup
	recoveryCalls := 5
	successCount := 0

	for i := 0; i < recoveryCalls; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()

			_, err := bulkhead.Execute(context.Background(), func() (interface{}, error) {
				err := circuitBreaker.Execute(func() error {
					_, err := service.Call(context.Background())
					return err
				})
				return nil, err
			})

			if err == nil {
				successCount++
			}
		}()
	}

	wg.Wait()

	// Verify recovery
	finalStats := circuitBreaker.GetStats()
	if finalStats.State == patternx.StateClosed {
		t.Log("Circuit breaker successfully recovered to closed state")
	} else {
		t.Logf("Circuit breaker state: %s", finalStats.State)
	}

	t.Logf("Recovery test results:")
	t.Logf("  Recovery calls attempted: %d", recoveryCalls)
	t.Logf("  Successful recovery calls: %d", successCount)
	t.Logf("  Final circuit breaker state: %s", finalStats.State)
}

// TestBulkheadCircuitBreakerStress tests stress conditions
func TestBulkheadCircuitBreakerStress(t *testing.T) {
	// Create a service with variable performance
	service := NewMockService(0.3, 200*time.Millisecond)

	// Create circuit breaker
	cbConfig := patternx.ConfigCircuitBreaker{
		Threshold:   5,
		Timeout:     1 * time.Second,
		HalfOpenMax: 3,
	}
	circuitBreaker, err := patternx.NewCircuitBreaker(cbConfig)
	if err != nil {
		t.Fatalf("Failed to create circuit breaker: %v", err)
	}

	// Create bulkhead with limited resources
	bhConfig := patternx.BulkheadConfig{
		MaxConcurrentCalls: 2,
		MaxWaitDuration:    300 * time.Millisecond,
		MaxQueueSize:       3,
		HealthThreshold:    0.5,
	}
	bulkhead, err := patternx.NewBulkhead(bhConfig)
	if err != nil {
		t.Fatalf("Failed to create bulkhead: %v", err)
	}
	defer bulkhead.Close()

	// Stress test: Many concurrent calls
	var wg sync.WaitGroup
	numCalls := 50
	results := make(chan string, numCalls)
	errors := make(chan error, numCalls)

	start := time.Now()

	for i := 0; i < numCalls; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()

			_, err := bulkhead.Execute(context.Background(), func() (interface{}, error) {
				err := circuitBreaker.Execute(func() error {
					response, err := service.Call(context.Background())
					if err != nil {
						return err
					}
					results <- fmt.Sprintf("call-%d: %s", id, response)
					return nil
				})
				return nil, err
			})

			if err != nil {
				errors <- fmt.Errorf("call-%d: %w", id, err)
			}
		}(i)
	}

	wg.Wait()
	close(results)
	close(errors)
	duration := time.Since(start)

	// Collect results
	successCount := 0
	for range results {
		successCount++
	}

	errorCount := 0
	errorTypes := make(map[string]int)
	for err := range errors {
		errorCount++
		errorTypes[err.Error()]++
	}

	// Verify patterns worked correctly
	cbStats := circuitBreaker.GetStats()
	bhMetrics := bulkhead.GetMetrics()

	t.Logf("Stress test results:")
	t.Logf("  Duration: %v", duration)
	t.Logf("  Total calls: %d", numCalls)
	t.Logf("  Successful calls: %d", successCount)
	t.Logf("  Failed calls: %d", errorCount)
	t.Logf("  Circuit breaker state: %s", cbStats.State)
	t.Logf("  Circuit breaker failures: %d", cbStats.TotalFailures)
	t.Logf("  Bulkhead total calls: %d", bhMetrics.TotalCalls.Load())
	t.Logf("  Bulkhead rejected calls: %d", bhMetrics.RejectedCalls.Load())
	t.Logf("  Bulkhead timeout calls: %d", bhMetrics.TimeoutCalls.Load())

	// Verify error distribution
	for errorType, count := range errorTypes {
		t.Logf("  Error type '%s': %d", errorType, count)
	}

	// Verify that patterns provided protection
	if bhMetrics.RejectedCalls.Load() == 0 && numCalls > bhConfig.MaxConcurrentCalls {
		t.Error("Bulkhead should have rejected some calls under stress")
	}

	if cbStats.TotalFailures == 0 && service.failureRate > 0 {
		t.Error("Circuit breaker should have recorded some failures")
	}
}
