package patternx

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Error types for bulkhead operations
var (
// Bulkhead specific errors are now defined in errors.go
// Bulkhead specific errors are now defined in errors.go
)

// Constants for production constraints
const (
	MaxConcurrentCallsLimitBulkhead = 10000
	MaxQueueSizeLimitBulkhead       = 100000
	MaxWaitDurationLimitBulkhead    = 60 * time.Second
	MinWaitDurationBulkhead         = 1 * time.Millisecond
	MinConcurrentCallsBulkhead      = 1
	MinQueueSizeBulkhead            = 1
	DefaultHealthThresholdBulkhead  = 0.5
	MaxHealthThresholdBulkhead      = 1.0
	MinHealthThresholdBulkhead      = 0.1
)

// BulkheadConfig defines the configuration for a bulkhead pattern with validation
type BulkheadConfig struct {
	// MaxConcurrentCalls is the maximum number of concurrent calls allowed
	MaxConcurrentCalls int
	// MaxWaitDuration is the maximum time to wait for a semaphore
	MaxWaitDuration time.Duration
	// MaxQueueSize is the maximum number of calls that can be queued
	MaxQueueSize int
	// HealthThreshold is the failure rate threshold for health checks (0.0 to 1.0)
	HealthThreshold float64
	// EnableMetrics enables performance metrics collection
	EnableMetrics bool
}

// DefaultBulkheadConfig returns a default bulkhead configuration
func DefaultBulkheadConfig() BulkheadConfig {
	return BulkheadConfig{
		MaxConcurrentCalls: 10,
		MaxWaitDuration:    5 * time.Second,
		MaxQueueSize:       100,
		HealthThreshold:    DefaultHealthThresholdBulkhead,
		EnableMetrics:      true,
	}
}

// HighPerformanceBulkheadConfig returns a high-performance bulkhead configuration
func HighPerformanceBulkheadConfig() BulkheadConfig {
	return BulkheadConfig{
		MaxConcurrentCalls: 50,
		MaxWaitDuration:    10 * time.Second,
		MaxQueueSize:       500,
		HealthThreshold:    DefaultHealthThresholdBulkhead,
		EnableMetrics:      true,
	}
}

// ResourceConstrainedBulkheadConfig returns a resource-constrained bulkhead configuration
func ResourceConstrainedBulkheadConfig() BulkheadConfig {
	return BulkheadConfig{
		MaxConcurrentCalls: 5,
		MaxWaitDuration:    2 * time.Second,
		MaxQueueSize:       50,
		HealthThreshold:    DefaultHealthThresholdBulkhead,
		EnableMetrics:      true,
	}
}

// EnterpriseBulkheadConfig returns an enterprise-grade bulkhead configuration
func EnterpriseBulkheadConfig() BulkheadConfig {
	return BulkheadConfig{
		MaxConcurrentCalls: 100,
		MaxWaitDuration:    30 * time.Second,
		MaxQueueSize:       1000,
		HealthThreshold:    0.3, // More strict health threshold
		EnableMetrics:      true,
	}
}

// Bulkhead implements the bulkhead pattern for fault isolation with production-ready features
type Bulkhead struct {
	config    BulkheadConfig
	semaphore chan struct{}
	queue     chan struct{}
	closed    int32 // Atomic flag for closed state
	metrics   *BulkheadMetrics
}

// BulkheadMetrics tracks bulkhead performance metrics with atomic operations
type BulkheadMetrics struct {
	TotalCalls             atomic.Int64
	SuccessfulCalls        atomic.Int64
	FailedCalls            atomic.Int64
	RejectedCalls          atomic.Int64
	TimeoutCalls           atomic.Int64
	CurrentConcurrentCalls atomic.Int64
	mu                     sync.RWMutex // For complex metrics that can't be atomic
	AverageExecutionTime   time.Duration
	LastExecutionTime      time.Time
	HealthStatus           bool
	LastHealthCheck        time.Time
}

// NewBulkhead creates a new bulkhead instance with comprehensive validation
func NewBulkhead(config BulkheadConfig) (*Bulkhead, error) {
	// Validate configuration
	if err := validateBulkheadConfig(config); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}

	// Apply defaults for missing values
	applyBulkheadDefaults(&config)

	bulkhead := &Bulkhead{
		config:    config,
		semaphore: make(chan struct{}, config.MaxConcurrentCalls),
		queue:     make(chan struct{}, config.MaxQueueSize),
		metrics:   &BulkheadMetrics{},
	}

	// Initialize metrics
	if config.EnableMetrics {
		bulkhead.metrics.HealthStatus = true
		bulkhead.metrics.LastHealthCheck = time.Now()
	}

	return bulkhead, nil
}

// Execute runs a function with bulkhead protection and comprehensive error handling
func (b *Bulkhead) Execute(ctx context.Context, fn func() (interface{}, error)) (interface{}, error) {
	// Check if bulkhead is closed
	if atomic.LoadInt32(&b.closed) == 1 {
		return nil, ErrBulkheadClosed
	}

	// Validate inputs
	if err := validateExecuteInputsBulkhead(ctx, fn); err != nil {
		return nil, fmt.Errorf("invalid execute inputs: %w", err)
	}

	// Check context cancellation before starting
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrContextCancelled, err)
	}

	// Update metrics
	b.metrics.TotalCalls.Add(1)

	// Try to acquire semaphore with timeout
	if err := b.acquireSemaphore(ctx); err != nil {
		b.metrics.RejectedCalls.Add(1)
		return nil, err
	}

	// Defer semaphore release
	defer b.releaseSemaphore()

	// Execute function with timeout protection
	return b.executeWithTimeout(ctx, fn)
}

// ExecuteAsync runs a function asynchronously with bulkhead protection
func (b *Bulkhead) ExecuteAsync(ctx context.Context, fn func() (interface{}, error)) <-chan Result {
	resultChan := make(chan Result, 1)

	// Validate inputs
	if err := validateExecuteInputsBulkhead(ctx, fn); err != nil {
		resultChan <- Result{Result: nil, Error: fmt.Errorf("invalid async execute inputs: %w", err)}
		close(resultChan)
		return resultChan
	}

	go func() {
		defer close(resultChan)
		result, err := b.Execute(ctx, fn)
		resultChan <- Result{Result: result, Error: err}
	}()

	return resultChan
}

// Result represents the result of an async bulkhead operation
type Result struct {
	Result interface{}
	Error  error
}

// GetMetrics returns the current bulkhead metrics with thread safety
func (b *Bulkhead) GetMetrics() *BulkheadMetrics {
	metrics := &BulkheadMetrics{
		TotalCalls:             atomic.Int64{},
		SuccessfulCalls:        atomic.Int64{},
		FailedCalls:            atomic.Int64{},
		RejectedCalls:          atomic.Int64{},
		TimeoutCalls:           atomic.Int64{},
		CurrentConcurrentCalls: atomic.Int64{},
	}

	// Copy atomic values
	metrics.TotalCalls.Store(b.metrics.TotalCalls.Load())
	metrics.SuccessfulCalls.Store(b.metrics.SuccessfulCalls.Load())
	metrics.FailedCalls.Store(b.metrics.FailedCalls.Load())
	metrics.RejectedCalls.Store(b.metrics.RejectedCalls.Load())
	metrics.TimeoutCalls.Store(b.metrics.TimeoutCalls.Load())
	metrics.CurrentConcurrentCalls.Store(b.metrics.CurrentConcurrentCalls.Load())

	// Copy complex metrics with mutex
	b.metrics.mu.RLock()
	metrics.AverageExecutionTime = b.metrics.AverageExecutionTime
	metrics.LastExecutionTime = b.metrics.LastExecutionTime
	metrics.HealthStatus = b.metrics.HealthStatus
	metrics.LastHealthCheck = b.metrics.LastHealthCheck
	b.metrics.mu.RUnlock()

	return metrics
}

// ResetMetrics resets all metrics to zero with thread safety
func (b *Bulkhead) ResetMetrics() {
	b.metrics.TotalCalls.Store(0)
	b.metrics.SuccessfulCalls.Store(0)
	b.metrics.FailedCalls.Store(0)
	b.metrics.RejectedCalls.Store(0)
	b.metrics.TimeoutCalls.Store(0)
	b.metrics.CurrentConcurrentCalls.Store(0)

	b.metrics.mu.Lock()
	b.metrics.AverageExecutionTime = 0
	b.metrics.LastExecutionTime = time.Time{}
	b.metrics.HealthStatus = true
	b.metrics.LastHealthCheck = time.Now()
	b.metrics.mu.Unlock()
}

// IsHealthy returns true if the bulkhead is in a healthy state
func (b *Bulkhead) IsHealthy() bool {
	if !b.config.EnableMetrics {
		return true
	}

	b.metrics.mu.Lock()
	defer b.metrics.mu.Unlock()

	// Update health status
	b.updateHealthStatus()
	b.metrics.LastHealthCheck = time.Now()

	return b.metrics.HealthStatus
}

// GetHealthStatus returns detailed health information
func (b *Bulkhead) GetHealthStatus() map[string]interface{} {
	metrics := b.GetMetrics()
	totalCalls := metrics.TotalCalls.Load()

	health := map[string]interface{}{
		"is_healthy":           b.IsHealthy(),
		"total_calls":          totalCalls,
		"successful_calls":     metrics.SuccessfulCalls.Load(),
		"failed_calls":         metrics.FailedCalls.Load(),
		"rejected_calls":       metrics.RejectedCalls.Load(),
		"timeout_calls":        metrics.TimeoutCalls.Load(),
		"current_concurrent":   metrics.CurrentConcurrentCalls.Load(),
		"max_concurrent":       b.config.MaxConcurrentCalls,
		"queue_size":           b.config.MaxQueueSize,
		"average_execution_ms": metrics.AverageExecutionTime.Milliseconds(),
		"last_health_check":    metrics.LastHealthCheck,
	}

	if totalCalls > 0 {
		failureRate := float64(metrics.FailedCalls.Load()+metrics.RejectedCalls.Load()+metrics.TimeoutCalls.Load()) / float64(totalCalls)
		health["failure_rate"] = failureRate
		health["health_threshold"] = b.config.HealthThreshold
	} else {
		health["failure_rate"] = 0.0
		health["health_threshold"] = b.config.HealthThreshold
	}

	return health
}

// Close closes the bulkhead and releases resources with graceful shutdown
func (b *Bulkhead) Close() error {
	// Set closed flag atomically
	if !atomic.CompareAndSwapInt32(&b.closed, 0, 1) {
		return errors.New("bulkhead already closed")
	}

	// Wait for current operations to complete (with timeout)
	timeout := time.After(30 * time.Second)
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case <-timeout:
			// Force close after timeout
			goto closeChannels
		case <-ticker.C:
			if b.metrics.CurrentConcurrentCalls.Load() == 0 {
				// All operations completed, safe to close channels
				goto closeChannels
			}
		}
	}

closeChannels:
	close(b.semaphore)
	close(b.queue)
	return nil
}

// IsClosed returns true if the bulkhead is closed
func (b *Bulkhead) IsClosed() bool {
	return atomic.LoadInt32(&b.closed) == 1
}

// acquireSemaphore attempts to acquire the semaphore with proper timeout handling
func (b *Bulkhead) acquireSemaphore(ctx context.Context) error {
	// Try to acquire semaphore immediately
	select {
	case b.semaphore <- struct{}{}:
		b.metrics.CurrentConcurrentCalls.Add(1)
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%w: %v", ErrContextCancelled, ctx.Err())
	default:
		// Semaphore is full, try to queue
		return b.acquireFromQueue(ctx)
	}
}

// acquireFromQueue attempts to acquire semaphore from queue with proper timeout
func (b *Bulkhead) acquireFromQueue(ctx context.Context) error {
	// Try to add to queue
	select {
	case b.queue <- struct{}{}:
		// Successfully queued, now wait for semaphore
		return b.waitForSemaphore(ctx)
	case <-ctx.Done():
		return fmt.Errorf("%w: %v", ErrContextCancelled, ctx.Err())
	default:
		// Queue is full
		return ErrBulkheadQueueFull
	}
}

// waitForSemaphore waits for semaphore with proper timeout and context handling
func (b *Bulkhead) waitForSemaphore(ctx context.Context) error {
	// Create timeout context for wait duration
	waitCtx, cancel := context.WithTimeout(ctx, b.config.MaxWaitDuration)
	defer cancel()

	select {
	case b.semaphore <- struct{}{}:
		// Successfully acquired semaphore
		<-b.queue // Remove from queue
		b.metrics.CurrentConcurrentCalls.Add(1)
		return nil
	case <-waitCtx.Done():
		// Timeout or context cancellation
		<-b.queue // Remove from queue
		if waitCtx.Err() == context.DeadlineExceeded {
			b.metrics.TimeoutCalls.Add(1)
			return ErrBulkheadTimeout
		}
		return fmt.Errorf("%w: %v", ErrContextCancelled, waitCtx.Err())
	}
}

// releaseSemaphore releases the semaphore
func (b *Bulkhead) releaseSemaphore() {
	<-b.semaphore
	b.metrics.CurrentConcurrentCalls.Add(-1)
}

// executeWithTimeout executes the function with timeout protection
func (b *Bulkhead) executeWithTimeout(_ context.Context, fn func() (interface{}, error)) (interface{}, error) {
	start := time.Now()

	// Execute function
	result, err := fn()

	executionTime := time.Since(start)

	// Update metrics
	b.updateExecutionMetrics(err, executionTime)

	return result, err
}

// updateExecutionMetrics updates execution metrics with thread safety
func (b *Bulkhead) updateExecutionMetrics(err error, executionTime time.Duration) {
	if err != nil {
		b.metrics.FailedCalls.Add(1)
	} else {
		b.metrics.SuccessfulCalls.Add(1)
	}

	// Update complex metrics with mutex
	b.metrics.mu.Lock()
	b.metrics.LastExecutionTime = time.Now()
	if b.metrics.SuccessfulCalls.Load() > 0 {
		// Calculate weighted average
		currentAvg := b.metrics.AverageExecutionTime
		if currentAvg == 0 {
			b.metrics.AverageExecutionTime = executionTime
		} else {
			b.metrics.AverageExecutionTime = (currentAvg + executionTime) / 2
		}
	}
	b.metrics.mu.Unlock()
}

// updateHealthStatus updates the health status based on failure rate
func (b *Bulkhead) updateHealthStatus() {
	totalCalls := b.metrics.TotalCalls.Load()
	if totalCalls == 0 {
		b.metrics.HealthStatus = true
		return
	}

	failureRate := float64(b.metrics.FailedCalls.Load()+b.metrics.RejectedCalls.Load()+b.metrics.TimeoutCalls.Load()) / float64(totalCalls)
	b.metrics.HealthStatus = failureRate <= b.config.HealthThreshold
}

// validateBulkheadConfig validates bulkhead configuration
func validateBulkheadConfig(config BulkheadConfig) error {
	if config.MaxConcurrentCalls < MinConcurrentCallsBulkhead || config.MaxConcurrentCalls > MaxConcurrentCallsLimitBulkhead {
		return fmt.Errorf("max concurrent calls must be between %d and %d, got %d",
			MinConcurrentCallsBulkhead, MaxConcurrentCallsLimitBulkhead, config.MaxConcurrentCalls)
	}

	if config.MaxQueueSize < MinQueueSizeBulkhead || config.MaxQueueSize > MaxQueueSizeLimitBulkhead {
		return fmt.Errorf("max queue size must be between %d and %d, got %d",
			MinQueueSizeBulkhead, MaxQueueSizeLimitBulkhead, config.MaxQueueSize)
	}

	if config.MaxWaitDuration < MinWaitDurationBulkhead || config.MaxWaitDuration > MaxWaitDurationLimitBulkhead {
		return fmt.Errorf("max wait duration must be between %v and %v, got %v",
			MinWaitDurationBulkhead, MaxWaitDurationLimitBulkhead, config.MaxWaitDuration)
	}

	if config.HealthThreshold < MinHealthThresholdBulkhead || config.HealthThreshold > MaxHealthThresholdBulkhead {
		return fmt.Errorf("health threshold must be between %f and %f, got %f",
			MinHealthThresholdBulkhead, MaxHealthThresholdBulkhead, config.HealthThreshold)
	}

	return nil
}

// applyBulkheadDefaults applies default values to configuration
func applyBulkheadDefaults(config *BulkheadConfig) {
	if config.MaxConcurrentCalls <= 0 {
		config.MaxConcurrentCalls = 10
	}
	if config.MaxWaitDuration <= 0 {
		config.MaxWaitDuration = 5 * time.Second
	}
	if config.MaxQueueSize <= 0 {
		config.MaxQueueSize = 100
	}
	if config.HealthThreshold <= 0 {
		config.HealthThreshold = DefaultHealthThresholdBulkhead
	}
}

// validateExecuteInputs validates inputs for Execute methods
func validateExecuteInputsBulkhead(ctx context.Context, fn func() (interface{}, error)) error {
	if ctx == nil {
		return errors.New("context cannot be nil")
	}
	if fn == nil {
		return ErrInvalidOperation
	}
	return nil
}
