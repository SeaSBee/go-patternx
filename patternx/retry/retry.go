package retry

import (
	"context"
	"errors"
	"fmt"
	"math"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"
)

// Production constants
const (
	MaxAttemptsLimit         = 100
	MinAttemptsLimit         = 1
	MaxInitialDelayLimit     = 1 * time.Hour
	MinInitialDelayLimit     = 1 * time.Millisecond
	MaxDelayLimit            = 24 * time.Hour
	MinDelayLimit            = 1 * time.Millisecond
	MaxMultiplierLimit       = 10.0
	MinMultiplierLimit       = 0.1
	MaxJitterPercentLimit    = 50.0
	MinJitterPercentLimit    = 0.0
	MaxRetryableErrorsLimit  = 100
	DefaultJitterPercent     = 10.0
	MaxRetryBudget           = 10000
	RetryBudgetResetInterval = 1 * time.Hour
)

// Error types for production scenarios
var (
	ErrInvalidPolicy       = errors.New("invalid retry policy configuration")
	ErrMaxAttemptsExceeded = errors.New("maximum retry attempts exceeded")
	ErrContextCancelled    = errors.New("retry operation cancelled by context")
	ErrRetryBudgetExceeded = errors.New("retry budget exceeded")
	ErrOperationTimeout    = errors.New("operation timeout")
	ErrInvalidOperation    = errors.New("invalid operation function")
	ErrRateLimitExceeded   = errors.New("rate limit exceeded")
	ErrCircuitBreakerOpen  = errors.New("circuit breaker is open")
)

// Policy defines retry behavior with comprehensive validation
type Policy struct {
	MaxAttempts     int           `json:"max_attempts"`
	InitialDelay    time.Duration `json:"initial_delay"`
	MaxDelay        time.Duration `json:"max_delay"`
	Multiplier      float64       `json:"multiplier"`
	Jitter          bool          `json:"jitter"`
	JitterPercent   float64       `json:"jitter_percent"`
	RetryableErrors []error       `json:"-"`
	Timeout         time.Duration `json:"timeout"`
	RateLimit       int           `json:"rate_limit"`        // requests per second
	RateLimitWindow time.Duration `json:"rate_limit_window"` // time window for rate limiting
}

// RetryStats holds comprehensive retry statistics
type RetryStats struct {
	TotalAttempts      atomic.Int64
	SuccessfulAttempts atomic.Int64
	FailedAttempts     atomic.Int64
	TotalDelay         atomic.Int64 // nanoseconds
	AverageDelay       atomic.Value // time.Duration
	LastAttemptTime    atomic.Value // time.Time
	LastSuccessTime    atomic.Value // time.Time
	LastError          atomic.Value // error
	RetryBudgetUsed    atomic.Int64
	RateLimitHits      atomic.Int64
}

// RetryManager manages global retry state and budgets
type RetryManager struct {
	stats           *RetryStats
	retryBudget     atomic.Int64
	lastBudgetReset atomic.Value // time.Time
	rateLimiters    map[string]*rateLimiter
	mu              sync.RWMutex
	closed          int32
}

// rateLimiter implements token bucket rate limiting
type rateLimiter struct {
	tokens     atomic.Int64
	lastRefill atomic.Value // time.Time
	capacity   int64
	rate       float64
	window     time.Duration
}

// DefaultPolicy returns a sensible default retry policy
func DefaultPolicy() Policy {
	return Policy{
		MaxAttempts:     3,
		InitialDelay:    100 * time.Millisecond,
		MaxDelay:        5 * time.Second,
		Multiplier:      2.0,
		Jitter:          true,
		JitterPercent:   DefaultJitterPercent,
		Timeout:         30 * time.Second,
		RateLimit:       100,
		RateLimitWindow: time.Second,
	}
}

// AggressivePolicy returns an aggressive retry policy for critical operations
func AggressivePolicy() Policy {
	return Policy{
		MaxAttempts:     5,
		InitialDelay:    50 * time.Millisecond,
		MaxDelay:        10 * time.Second,
		Multiplier:      1.5,
		Jitter:          true,
		JitterPercent:   DefaultJitterPercent,
		Timeout:         60 * time.Second,
		RateLimit:       200,
		RateLimitWindow: time.Second,
	}
}

// ConservativePolicy returns a conservative retry policy for non-critical operations
func ConservativePolicy() Policy {
	return Policy{
		MaxAttempts:     2,
		InitialDelay:    500 * time.Millisecond,
		MaxDelay:        2 * time.Second,
		Multiplier:      2.0,
		Jitter:          true,
		JitterPercent:   DefaultJitterPercent,
		Timeout:         15 * time.Second,
		RateLimit:       50,
		RateLimitWindow: time.Second,
	}
}

// EnterprisePolicy returns an enterprise-grade retry policy
func EnterprisePolicy() Policy {
	return Policy{
		MaxAttempts:     10,
		InitialDelay:    1 * time.Second,
		MaxDelay:        1 * time.Minute,
		Multiplier:      1.8,
		Jitter:          true,
		JitterPercent:   15.0,
		Timeout:         5 * time.Minute,
		RateLimit:       1000,
		RateLimitWindow: time.Second,
	}
}

// NewRetryManager creates a new retry manager
func NewRetryManager() *RetryManager {
	rm := &RetryManager{
		stats:        &RetryStats{},
		rateLimiters: make(map[string]*rateLimiter),
	}
	rm.lastBudgetReset.Store(time.Now())
	return rm
}

// validatePolicy validates retry policy configuration
func validatePolicy(policy Policy) error {
	if policy.MaxAttempts < MinAttemptsLimit || policy.MaxAttempts > MaxAttemptsLimit {
		return fmt.Errorf("%w: max attempts must be between %d and %d, got %d",
			ErrInvalidPolicy, MinAttemptsLimit, MaxAttemptsLimit, policy.MaxAttempts)
	}

	if policy.InitialDelay < MinInitialDelayLimit || policy.InitialDelay > MaxInitialDelayLimit {
		return fmt.Errorf("%w: initial delay must be between %v and %v, got %v",
			ErrInvalidPolicy, MinInitialDelayLimit, MaxInitialDelayLimit, policy.InitialDelay)
	}

	if policy.MaxDelay < MinDelayLimit || policy.MaxDelay > MaxDelayLimit {
		return fmt.Errorf("%w: max delay must be between %v and %v, got %v",
			ErrInvalidPolicy, MinDelayLimit, MaxDelayLimit, policy.MaxDelay)
	}

	if policy.InitialDelay > policy.MaxDelay {
		return fmt.Errorf("%w: initial delay (%v) cannot exceed max delay (%v)",
			ErrInvalidPolicy, policy.InitialDelay, policy.MaxDelay)
	}

	if policy.Multiplier < MinMultiplierLimit || policy.Multiplier > MaxMultiplierLimit {
		return fmt.Errorf("%w: multiplier must be between %f and %f, got %f",
			ErrInvalidPolicy, MinMultiplierLimit, MaxMultiplierLimit, policy.Multiplier)
	}

	if policy.JitterPercent < MinJitterPercentLimit || policy.JitterPercent > MaxJitterPercentLimit {
		return fmt.Errorf("%w: jitter percent must be between %f and %f, got %f",
			ErrInvalidPolicy, MinJitterPercentLimit, MaxJitterPercentLimit, policy.JitterPercent)
	}

	if len(policy.RetryableErrors) > MaxRetryableErrorsLimit {
		return fmt.Errorf("%w: retryable errors list cannot exceed %d items, got %d",
			ErrInvalidPolicy, MaxRetryableErrorsLimit, len(policy.RetryableErrors))
	}

	if policy.RateLimit < 0 {
		return fmt.Errorf("%w: rate limit cannot be negative, got %d",
			ErrInvalidPolicy, policy.RateLimit)
	}

	if policy.RateLimitWindow <= 0 {
		return fmt.Errorf("%w: rate limit window must be positive, got %v",
			ErrInvalidPolicy, policy.RateLimitWindow)
	}

	return nil
}

// Retry executes an operation with retry logic
func Retry(policy Policy, operation func() error) error {
	return RetryWithContext(context.Background(), policy, operation)
}

// RetryWithContext executes an operation with retry logic and context
func RetryWithContext(ctx context.Context, policy Policy, operation func() error) error {
	if err := validatePolicy(policy); err != nil {
		return err
	}

	if operation == nil {
		return fmt.Errorf("%w: operation function cannot be nil", ErrInvalidOperation)
	}

	if ctx == nil {
		ctx = context.Background()
	}

	var lastErr error
	totalDelay := time.Duration(0)

	for attempt := 0; attempt < policy.MaxAttempts; attempt++ {
		// Check context cancellation
		select {
		case <-ctx.Done():
			return fmt.Errorf("%w: %w", ErrContextCancelled, ctx.Err())
		default:
		}

		// Check timeout
		if policy.Timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, policy.Timeout)
			defer cancel()
		}

		// Execute operation
		err := operation()
		if err == nil {
			return nil // Success
		}

		lastErr = err

		// Check if error is retryable
		if !isRetryableError(err, policy.RetryableErrors) {
			return err
		}

		// Don't retry on last attempt
		if attempt == policy.MaxAttempts-1 {
			break
		}

		// Calculate delay
		delay := calculateDelay(policy, attempt)
		totalDelay += delay

		// Wait with context cancellation
		select {
		case <-time.After(delay):
			// Continue to next attempt
		case <-ctx.Done():
			return fmt.Errorf("%w: %w", ErrContextCancelled, ctx.Err())
		}
	}

	return fmt.Errorf("%w: operation failed after %d attempts: %w",
		ErrMaxAttemptsExceeded, policy.MaxAttempts, lastErr)
}

// RetryWithResult executes an operation that returns a result with retry logic
func RetryWithResult[T any](policy Policy, operation func() (T, error)) (T, error) {
	return RetryWithResultAndContext(context.Background(), policy, operation)
}

// RetryWithResultAndContext executes an operation that returns a result with retry logic and context
func RetryWithResultAndContext[T any](ctx context.Context, policy Policy, operation func() (T, error)) (T, error) {
	var zero T

	if err := validatePolicy(policy); err != nil {
		return zero, err
	}

	if operation == nil {
		return zero, fmt.Errorf("%w: operation function cannot be nil", ErrInvalidOperation)
	}

	if ctx == nil {
		ctx = context.Background()
	}

	var lastErr error
	totalDelay := time.Duration(0)

	for attempt := 0; attempt < policy.MaxAttempts; attempt++ {
		// Check context cancellation
		select {
		case <-ctx.Done():
			return zero, fmt.Errorf("%w: %w", ErrContextCancelled, ctx.Err())
		default:
		}

		// Check timeout
		if policy.Timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, policy.Timeout)
			defer cancel()
		}

		// Execute operation
		result, err := operation()
		if err == nil {
			return result, nil // Success
		}

		lastErr = err

		// Check if error is retryable
		if !isRetryableError(err, policy.RetryableErrors) {
			return zero, err
		}

		// Don't retry on last attempt
		if attempt == policy.MaxAttempts-1 {
			break
		}

		// Calculate delay
		delay := calculateDelay(policy, attempt)
		totalDelay += delay

		// Wait with context cancellation
		select {
		case <-time.After(delay):
			// Continue to next attempt
		case <-ctx.Done():
			return zero, fmt.Errorf("%w: %w", ErrContextCancelled, ctx.Err())
		}
	}

	return zero, fmt.Errorf("%w: operation failed after %d attempts: %w",
		ErrMaxAttemptsExceeded, policy.MaxAttempts, lastErr)
}

// calculateDelay calculates the delay for the given attempt using exponential backoff
func calculateDelay(policy Policy, attempt int) time.Duration {
	// Calculate exponential backoff
	delay := float64(policy.InitialDelay) * math.Pow(policy.Multiplier, float64(attempt))

	// Apply maximum delay cap
	if delay > float64(policy.MaxDelay) {
		delay = float64(policy.MaxDelay)
	}

	// Add jitter if enabled
	if policy.Jitter {
		jitter := delay * (policy.JitterPercent / 100.0)
		delay += (rand.Float64() * 2 * jitter) - jitter
	}

	// Final safety check to ensure non-negative delay
	if delay < 0 {
		delay = 0
	}

	return time.Duration(delay)
}

// isRetryableError checks if an error should trigger a retry
func isRetryableError(err error, retryableErrors []error) bool {
	// If no specific errors are defined, retry on all errors
	if len(retryableErrors) == 0 {
		return true
	}

	// Check if error matches any retryable error
	for _, retryableErr := range retryableErrors {
		if errors.Is(err, retryableErr) {
			return true
		}
	}

	return false
}

// RetryableError wraps an error to indicate it should be retried
type RetryableError struct {
	Err error
}

func (e RetryableError) Error() string {
	return fmt.Sprintf("retryable error: %v", e.Err)
}

func (e RetryableError) Unwrap() error {
	return e.Err
}

// NewRetryableError creates a new retryable error
func NewRetryableError(err error) RetryableError {
	return RetryableError{Err: err}
}

// IsRetryable checks if an error is retryable
func IsRetryable(err error) bool {
	var retryableErr RetryableError
	return errors.As(err, &retryableErr)
}

// RetryWithStats executes an operation with retry logic and returns statistics
func RetryWithStats(policy Policy, operation func() error) (*RetryStats, error) {
	return RetryWithStatsAndContext(context.Background(), policy, operation)
}

// RetryWithStatsAndContext executes an operation with retry logic and context, returning statistics
func RetryWithStatsAndContext(ctx context.Context, policy Policy, operation func() error) (*RetryStats, error) {
	stats := &RetryStats{}
	var lastErr error
	totalDelay := time.Duration(0)

	if err := validatePolicy(policy); err != nil {
		return stats, err
	}

	if operation == nil {
		return stats, fmt.Errorf("%w: operation function cannot be nil", ErrInvalidOperation)
	}

	if ctx == nil {
		ctx = context.Background()
	}

	for attempt := 0; attempt < policy.MaxAttempts; attempt++ {
		stats.TotalAttempts.Add(1)
		stats.LastAttemptTime.Store(time.Now())

		// Check context cancellation
		select {
		case <-ctx.Done():
			stats.LastError.Store(fmt.Errorf("%w: %w", ErrContextCancelled, ctx.Err()))
			return stats, fmt.Errorf("%w: %w", ErrContextCancelled, ctx.Err())
		default:
		}

		// Check timeout
		if policy.Timeout > 0 {
			var cancel context.CancelFunc
			ctx, cancel = context.WithTimeout(ctx, policy.Timeout)
			defer cancel()
		}

		// Execute operation
		err := operation()
		if err == nil {
			stats.SuccessfulAttempts.Add(1)
			stats.LastSuccessTime.Store(time.Now())
			stats.LastError.Store(errors.New(""))
			return stats, nil // Success
		}

		lastErr = err
		stats.FailedAttempts.Add(1)
		stats.LastError.Store(err)

		// Check if error is retryable
		if !isRetryableError(err, policy.RetryableErrors) {
			return stats, err
		}

		// Don't retry on last attempt
		if attempt == policy.MaxAttempts-1 {
			break
		}

		// Calculate delay
		delay := calculateDelay(policy, attempt)
		totalDelay += delay
		stats.TotalDelay.Add(int64(delay))

		// Wait with context cancellation
		select {
		case <-time.After(delay):
			// Continue to next attempt
		case <-ctx.Done():
			stats.LastError.Store(fmt.Errorf("%w: %w", ErrContextCancelled, ctx.Err()))
			return stats, fmt.Errorf("%w: %w", ErrContextCancelled, ctx.Err())
		}
	}

	// Calculate average delay
	if stats.TotalAttempts.Load() > 1 {
		avgDelay := time.Duration(stats.TotalDelay.Load() / (stats.TotalAttempts.Load() - 1))
		stats.AverageDelay.Store(avgDelay)
	}

	return stats, fmt.Errorf("%w: operation failed after %d attempts: %w",
		ErrMaxAttemptsExceeded, policy.MaxAttempts, lastErr)
}

// GetStats returns the current retry statistics
func (rm *RetryManager) GetStats() *RetryStats {
	return rm.stats
}

// ResetStats resets all retry statistics
func (rm *RetryManager) ResetStats() {
	rm.stats.TotalAttempts.Store(0)
	rm.stats.SuccessfulAttempts.Store(0)
	rm.stats.FailedAttempts.Store(0)
	rm.stats.TotalDelay.Store(0)
	rm.stats.RetryBudgetUsed.Store(0)
	rm.stats.RateLimitHits.Store(0)
	rm.stats.AverageDelay.Store(time.Duration(0))
	rm.stats.LastAttemptTime.Store(time.Now())
	rm.stats.LastSuccessTime.Store(time.Now())
	rm.stats.LastError.Store(errors.New(""))
}

// Close gracefully shuts down the retry manager
func (rm *RetryManager) Close() error {
	if !atomic.CompareAndSwapInt32(&rm.closed, 0, 1) {
		return fmt.Errorf("retry manager already closed")
	}

	rm.mu.Lock()
	defer rm.mu.Unlock()

	// Clear rate limiters
	rm.rateLimiters = make(map[string]*rateLimiter)

	return nil
}

// IsClosed returns true if the retry manager is closed
func (rm *RetryManager) IsClosed() bool {
	return atomic.LoadInt32(&rm.closed) == 1
}
