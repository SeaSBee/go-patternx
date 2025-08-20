package patternx

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Error types for circuit breaker operations
var (
// Circuit Breaker specific errors are now defined in errors.go
)

// Constants for production constraints
const (
	MaxThresholdLimitCircuitBreaker   = 10000
	MinThresholdLimitCircuitBreaker   = 1
	MaxTimeoutLimitCircuitBreaker     = 300 * time.Second
	MinTimeoutLimitCircuitBreaker     = 1 * time.Millisecond
	MaxHalfOpenLimitCircuitBreaker    = 1000
	MinHalfOpenLimitCircuitBreaker    = 1
	DefaultThresholdCircuitBreaker    = 5
	DefaultTimeoutCircuitBreaker      = 30 * time.Second
	DefaultHalfOpenMaxCircuitBreaker  = 3
	MaxOperationTimeoutCircuitBreaker = 60 * time.Second
	MinOperationTimeoutCircuitBreaker = 1 * time.Millisecond
)

// State represents the circuit breaker state
type State int

const (
	StateClosed State = iota
	StateOpen
	StateHalfOpen
)

type CircuitBreakerConfig struct {
	Threshold   int           // Number of failures before opening circuit
	Timeout     time.Duration // How long to keep circuit open
	HalfOpenMax int           // Max requests in half-open state
}

func (s State) String() string {
	switch s {
	case StateClosed:
		return "CLOSED"
	case StateOpen:
		return "OPEN"
	case StateHalfOpen:
		return "HALF_OPEN"
	default:
		return "UNKNOWN"
	}
}

// CircuitBreaker implements the circuit breaker pattern with production-ready features
type CircuitBreaker struct {
	// Configuration
	threshold   int
	timeout     time.Duration
	halfOpenMax int

	// State management with atomic operations
	state        int32 // atomic state
	failureCount int64
	lastFailure  int64 // atomic timestamp
	successCount int64
	mu           sync.RWMutex // For complex state transitions

	// Metrics with atomic operations
	totalRequests  int64
	totalFailures  int64
	totalSuccesses int64
	totalTimeouts  int64
	totalPanics    int64

	// Health and monitoring
	lastStateChange int64 // atomic timestamp
	healthStatus    int32 // atomic health flag
}

// Config holds circuit breaker configuration with validation
type ConfigCircuitBreaker struct {
	Threshold   int           // Number of failures before opening circuit
	Timeout     time.Duration // How long to keep circuit open
	HalfOpenMax int           // Max requests in half-open state
}

// DefaultConfig returns a default circuit breaker configuration
func DefaultConfigCircuitBreaker() ConfigCircuitBreaker {
	return ConfigCircuitBreaker{
		Threshold:   DefaultThresholdCircuitBreaker,
		Timeout:     DefaultTimeoutCircuitBreaker,
		HalfOpenMax: DefaultHalfOpenMaxCircuitBreaker,
	}
}

// HighPerformanceConfig returns a high-performance circuit breaker configuration
func HighPerformanceConfigCircuitBreaker() ConfigCircuitBreaker {
	return ConfigCircuitBreaker{
		Threshold:   10,
		Timeout:     10 * time.Second,
		HalfOpenMax: 5,
	}
}

// ConservativeConfig returns a conservative circuit breaker configuration
func ConservativeConfigCircuitBreaker() ConfigCircuitBreaker {
	return ConfigCircuitBreaker{
		Threshold:   3,
		Timeout:     60 * time.Second,
		HalfOpenMax: 2,
	}
}

// EnterpriseConfig returns an enterprise-grade circuit breaker configuration
func EnterpriseConfigCircuitBreaker() ConfigCircuitBreaker {
	return ConfigCircuitBreaker{
		Threshold:   20,
		Timeout:     30 * time.Second,
		HalfOpenMax: 10,
	}
}

// New creates a new circuit breaker with comprehensive validation
func NewCircuitBreaker(config ConfigCircuitBreaker) (*CircuitBreaker, error) {
	// Validate configuration
	if err := validateCircuitBreakerConfig(config); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}

	// Apply defaults for missing values
	applyCircuitBreakerDefaults(&config)

	cb := &CircuitBreaker{
		threshold:   config.Threshold,
		timeout:     config.Timeout,
		halfOpenMax: config.HalfOpenMax,
		state:       int32(StateClosed),
	}

	// Initialize health status
	atomic.StoreInt32(&cb.healthStatus, 1) // Healthy initially
	atomic.StoreInt64(&cb.lastStateChange, time.Now().UnixNano())

	return cb, nil
}

// Execute runs the operation with circuit breaker protection and comprehensive error handling
func (cb *CircuitBreaker) Execute(operation func() error) error {
	// Validate input
	if operation == nil {
		return ErrInvalidOperation
	}

	// Increment total requests atomically
	atomic.AddInt64(&cb.totalRequests, 1)

	// Check if circuit is open with proper state management
	if err := cb.checkCircuitState(); err != nil {
		return err
	}

	// Execute operation with panic recovery
	return cb.executeWithRecovery(operation)
}

// ExecuteWithContext runs the operation with context and circuit breaker protection
func (cb *CircuitBreaker) ExecuteWithContext(ctx context.Context, operation func() error) error {
	// Validate inputs
	if err := validateExecuteInputsCircuitBreaker(ctx, operation); err != nil {
		return err
	}

	// Check context cancellation before starting
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrContextCancelled, err)
	}

	// Increment total requests atomically
	atomic.AddInt64(&cb.totalRequests, 1)

	// Check if circuit is open with proper state management
	if err := cb.checkCircuitState(); err != nil {
		return err
	}

	// Execute operation with context and panic recovery
	return cb.executeWithContextAndRecovery(ctx, operation)
}

// ExecuteWithTimeout runs the operation with timeout and circuit breaker protection
func (cb *CircuitBreaker) ExecuteWithTimeout(timeout time.Duration, operation func() error) error {
	// Validate timeout
	if err := validateTimeoutCircuitBreaker(timeout); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return cb.ExecuteWithContext(ctx, operation)
}

// checkCircuitState checks the circuit state with proper synchronization
func (cb *CircuitBreaker) checkCircuitState() error {
	state := cb.getState()

	switch state {
	case StateOpen:
		if cb.shouldAttemptReset() {
			cb.transitionToHalfOpen()
		} else {
			return ErrCircuitBreakerOpen
		}
	case StateHalfOpen:
		// Allow limited requests in half-open state
		successes := atomic.LoadInt64(&cb.successCount)
		if successes >= int64(cb.halfOpenMax) {
			return ErrCircuitBreakerOpen
		}
	}

	return nil
}

// executeWithRecovery executes operation with panic recovery
func (cb *CircuitBreaker) executeWithRecovery(operation func() error) error {
	defer func() {
		if r := recover(); r != nil {
			atomic.AddInt64(&cb.totalPanics, 1)
			cb.recordFailure()
		}
	}()

	err := operation()
	if err != nil {
		cb.recordFailure()
	} else {
		cb.recordSuccess()
	}

	return err
}

// executeWithContextAndRecovery executes operation with context and panic recovery
func (cb *CircuitBreaker) executeWithContextAndRecovery(ctx context.Context, operation func() error) error {
	// Create done channel with buffer to prevent goroutine leak
	done := make(chan error, 1)

	// Execute operation in goroutine with panic recovery
	go func() {
		defer func() {
			if r := recover(); r != nil {
				atomic.AddInt64(&cb.totalPanics, 1)
				done <- fmt.Errorf("%w: %v", ErrOperationPanic, r)
			}
		}()

		done <- operation()
	}()

	// Wait for completion or context cancellation
	select {
	case err := <-done:
		if err != nil {
			cb.recordFailure()
		} else {
			cb.recordSuccess()
		}
		return err
	case <-ctx.Done():
		atomic.AddInt64(&cb.totalTimeouts, 1)
		cb.recordFailure()
		return fmt.Errorf("%w: %v", ErrCircuitBreakerTimeout, ctx.Err())
	}
}

// getState returns the current state atomically
func (cb *CircuitBreaker) getState() State {
	return State(atomic.LoadInt32(&cb.state))
}

// setState sets the state atomically and updates last state change
func (cb *CircuitBreaker) setState(state State) {
	atomic.StoreInt32(&cb.state, int32(state))
	atomic.StoreInt64(&cb.lastStateChange, time.Now().UnixNano())
}

// shouldAttemptReset checks if enough time has passed to attempt reset
func (cb *CircuitBreaker) shouldAttemptReset() bool {
	lastFailure := atomic.LoadInt64(&cb.lastFailure)
	if lastFailure == 0 {
		return false
	}
	return time.Since(time.Unix(0, lastFailure)) >= cb.timeout
}

// transitionToHalfOpen transitions the circuit to half-open state with proper locking
func (cb *CircuitBreaker) transitionToHalfOpen() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	currentState := cb.getState()
	if currentState == StateOpen {
		cb.setState(StateHalfOpen)
		atomic.StoreInt64(&cb.successCount, 0)
	}
}

// recordFailure records a failure and potentially opens the circuit with proper synchronization
func (cb *CircuitBreaker) recordFailure() {
	atomic.AddInt64(&cb.totalFailures, 1)
	atomic.StoreInt64(&cb.lastFailure, time.Now().UnixNano())

	// Use proper locking for state transitions
	cb.mu.Lock()
	defer cb.mu.Unlock()

	currentState := cb.getState()
	switch currentState {
	case StateClosed:
		failures := atomic.AddInt64(&cb.failureCount, 1)
		if failures >= int64(cb.threshold) {
			cb.setState(StateOpen)
			atomic.StoreInt32(&cb.healthStatus, 0) // Mark as unhealthy
		}
	case StateHalfOpen:
		cb.setState(StateOpen)
		atomic.StoreInt32(&cb.healthStatus, 0) // Mark as unhealthy
	}
}

// recordSuccess records a success and potentially closes the circuit with proper synchronization
func (cb *CircuitBreaker) recordSuccess() {
	atomic.AddInt64(&cb.totalSuccesses, 1)

	// Use proper locking for state transitions
	cb.mu.Lock()
	defer cb.mu.Unlock()

	currentState := cb.getState()
	switch currentState {
	case StateClosed:
		// Reset failure count on success
		atomic.StoreInt64(&cb.failureCount, 0)
	case StateHalfOpen:
		successes := atomic.AddInt64(&cb.successCount, 1)
		if successes >= int64(cb.halfOpenMax) {
			cb.setState(StateClosed)
			atomic.StoreInt64(&cb.failureCount, 0)
			atomic.StoreInt32(&cb.healthStatus, 1) // Mark as healthy
		}
	}
}

// ForceOpen forces the circuit breaker to open state
func (cb *CircuitBreaker) ForceOpen() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.setState(StateOpen)
	atomic.StoreInt32(&cb.healthStatus, 0) // Mark as unhealthy
}

// ForceClose forces the circuit breaker to closed state
func (cb *CircuitBreaker) ForceClose() {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	cb.setState(StateClosed)
	atomic.StoreInt64(&cb.failureCount, 0)
	atomic.StoreInt64(&cb.successCount, 0)
	atomic.StoreInt64(&cb.lastFailure, 0)
	atomic.StoreInt64(&cb.totalRequests, 0)
	atomic.StoreInt64(&cb.totalFailures, 0)
	atomic.StoreInt64(&cb.totalSuccesses, 0)
	atomic.StoreInt64(&cb.totalTimeouts, 0)
	atomic.StoreInt64(&cb.totalPanics, 0)
	atomic.StoreInt32(&cb.healthStatus, 1) // Mark as healthy
}

// GetState returns the current state
func (cb *CircuitBreaker) GetState() State {
	return cb.getState()
}

// IsHealthy returns true if the circuit breaker is in a healthy state
func (cb *CircuitBreaker) IsHealthy() bool {
	return atomic.LoadInt32(&cb.healthStatus) == 1
}

// GetHealthStatus returns detailed health information
func (cb *CircuitBreaker) GetHealthStatus() map[string]interface{} {
	stats := cb.GetStats()

	health := map[string]interface{}{
		"is_healthy":        cb.IsHealthy(),
		"state":             stats.State.String(),
		"total_requests":    stats.TotalRequests,
		"total_failures":    stats.TotalFailures,
		"total_successes":   stats.TotalSuccesses,
		"total_timeouts":    stats.TotalTimeouts,
		"total_panics":      stats.TotalPanics,
		"failure_rate":      stats.FailureRate(),
		"success_rate":      stats.SuccessRate(),
		"last_failure":      stats.LastFailure,
		"last_state_change": time.Unix(0, atomic.LoadInt64(&cb.lastStateChange)),
		"threshold":         cb.threshold,
		"timeout":           cb.timeout,
		"half_open_max":     cb.halfOpenMax,
	}

	return health
}

// GetStats returns circuit breaker statistics with thread safety
func (cb *CircuitBreaker) GetStats() Stats {
	return Stats{
		State:          cb.getState(),
		FailureCount:   atomic.LoadInt64(&cb.failureCount),
		SuccessCount:   atomic.LoadInt64(&cb.successCount),
		LastFailure:    time.Unix(0, atomic.LoadInt64(&cb.lastFailure)),
		TotalRequests:  atomic.LoadInt64(&cb.totalRequests),
		TotalFailures:  atomic.LoadInt64(&cb.totalFailures),
		TotalSuccesses: atomic.LoadInt64(&cb.totalSuccesses),
		TotalTimeouts:  atomic.LoadInt64(&cb.totalTimeouts),
		TotalPanics:    atomic.LoadInt64(&cb.totalPanics),
		Config:         cb.getConfig(),
	}
}

// getConfig returns the current configuration
func (cb *CircuitBreaker) getConfig() ConfigCircuitBreaker {
	return ConfigCircuitBreaker{
		Threshold:   cb.threshold,
		Timeout:     cb.timeout,
		HalfOpenMax: cb.halfOpenMax,
	}
}

// Reset resets all metrics and state
func (cb *CircuitBreaker) Reset() {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	cb.setState(StateClosed)
	atomic.StoreInt64(&cb.failureCount, 0)
	atomic.StoreInt64(&cb.successCount, 0)
	atomic.StoreInt64(&cb.lastFailure, 0)
	atomic.StoreInt64(&cb.totalRequests, 0)
	atomic.StoreInt64(&cb.totalFailures, 0)
	atomic.StoreInt64(&cb.totalSuccesses, 0)
	atomic.StoreInt64(&cb.totalTimeouts, 0)
	atomic.StoreInt64(&cb.totalPanics, 0)
	atomic.StoreInt32(&cb.healthStatus, 1)
	atomic.StoreInt64(&cb.lastStateChange, time.Now().UnixNano())
}

// Stats holds circuit breaker statistics
type Stats struct {
	State          State
	FailureCount   int64
	SuccessCount   int64
	LastFailure    time.Time
	TotalRequests  int64
	TotalFailures  int64
	TotalSuccesses int64
	TotalTimeouts  int64
	TotalPanics    int64
	Config         ConfigCircuitBreaker
}

// IsHealthy returns true if the circuit breaker is in a healthy state
func (s Stats) IsHealthy() bool {
	return s.State == StateClosed || s.State == StateHalfOpen
}

// FailureRate returns the failure rate as a percentage
func (s Stats) FailureRate() float64 {
	if s.TotalRequests == 0 {
		return 0
	}
	return float64(s.TotalFailures) / float64(s.TotalRequests) * 100
}

// SuccessRate returns the success rate as a percentage
func (s Stats) SuccessRate() float64 {
	if s.TotalRequests == 0 {
		return 0
	}
	return float64(s.TotalSuccesses) / float64(s.TotalRequests) * 100
}

// validateCircuitBreakerConfig validates circuit breaker configuration
func validateCircuitBreakerConfig(config ConfigCircuitBreaker) error {
	if config.Threshold < MinThresholdLimitCircuitBreaker || config.Threshold > MaxThresholdLimitCircuitBreaker {
		return fmt.Errorf("threshold must be between %d and %d, got %d",
			MinThresholdLimitCircuitBreaker, MaxThresholdLimitCircuitBreaker, config.Threshold)
	}

	if config.Timeout < MinTimeoutLimitCircuitBreaker || config.Timeout > MaxTimeoutLimitCircuitBreaker {
		return fmt.Errorf("timeout must be between %v and %v, got %v",
			MinTimeoutLimitCircuitBreaker, MaxTimeoutLimitCircuitBreaker, config.Timeout)
	}

	if config.HalfOpenMax < MinHalfOpenLimitCircuitBreaker || config.HalfOpenMax > MaxHalfOpenLimitCircuitBreaker {
		return fmt.Errorf("half open max must be between %d and %d, got %d",
			MinHalfOpenLimitCircuitBreaker, MaxHalfOpenLimitCircuitBreaker, config.HalfOpenMax)
	}

	return nil
}

// applyCircuitBreakerDefaults applies default values to configuration
func applyCircuitBreakerDefaults(config *ConfigCircuitBreaker) {
	if config.Threshold <= 0 {
		config.Threshold = DefaultThresholdCircuitBreaker
	}
	if config.Timeout <= 0 {
		config.Timeout = DefaultTimeoutCircuitBreaker
	}
	if config.HalfOpenMax <= 0 {
		config.HalfOpenMax = DefaultHalfOpenMaxCircuitBreaker
	}
}

// validateExecuteInputs validates inputs for Execute methods
func validateExecuteInputsCircuitBreaker(ctx context.Context, operation func() error) error {
	if ctx == nil {
		return errors.New("context cannot be nil")
	}
	if operation == nil {
		return ErrInvalidOperation
	}
	return nil
}

// validateTimeout validates timeout duration
func validateTimeoutCircuitBreaker(timeout time.Duration) error {
	if timeout < MinOperationTimeoutCircuitBreaker || timeout > MaxOperationTimeoutCircuitBreaker {
		return fmt.Errorf("timeout must be between %v and %v, got %v",
			MinOperationTimeoutCircuitBreaker, MaxOperationTimeoutCircuitBreaker, timeout)
	}
	return nil
}
