package lock

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/seasbee/go-logx"
)

// Error types for Redlock operations
var (
	ErrRedlockClosed         = errors.New("redlock is closed")
	ErrInvalidConfig         = errors.New("invalid redlock configuration")
	ErrInvalidResource       = errors.New("invalid resource name")
	ErrInvalidTTL            = errors.New("invalid TTL duration")
	ErrInvalidTimeout        = errors.New("invalid timeout duration")
	ErrLockNotAcquired       = errors.New("lock not acquired")
	ErrLockAcquisitionFailed = errors.New("lock acquisition failed")
	ErrLockReleaseFailed     = errors.New("lock release failed")
	ErrLockExtensionFailed   = errors.New("lock extension failed")
	ErrQuorumNotReached      = errors.New("quorum not reached")
	ErrContextCancelled      = errors.New("operation cancelled by context")
	ErrLockExpired           = errors.New("lock expired")
	ErrClientUnavailable     = errors.New("client unavailable")
	ErrInvalidQuorum         = errors.New("invalid quorum value")
)

// Constants for production constraints
const (
	MaxTTLLimit           = 24 * time.Hour
	MinTTLLimit           = 1 * time.Millisecond
	MaxTimeoutLimit       = 60 * time.Second
	MinTimeoutLimit       = 1 * time.Millisecond
	MaxRetryDelayLimit    = 10 * time.Second
	MinRetryDelayLimit    = 1 * time.Millisecond
	MaxRetriesLimit       = 100
	MinRetriesLimit       = 0
	MaxDriftFactorLimit   = 0.1
	MinDriftFactorLimit   = 0.001
	DefaultRetryDelay     = 100 * time.Millisecond
	DefaultMaxRetries     = 3
	DefaultDriftFactor    = 0.01
	MaxResourceNameLength = 256
	MinResourceNameLength = 1
	GracefulShutdownWait  = 5 * time.Second
)

// Redlock implements a distributed lock using the Redlock algorithm with production-ready features
type Redlock struct {
	// Configuration
	clients     []LockClient
	quorum      int
	retryDelay  time.Duration
	maxRetries  int
	driftFactor float64

	// State management
	closed int32 // atomic flag for graceful shutdown
	mu     sync.RWMutex

	// Metrics
	metrics *RedlockMetrics
}

// LockClient defines the interface for lock operations
type LockClient interface {
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Del(ctx context.Context, key string) error
	Get(ctx context.Context, key string) ([]byte, error)
	Eval(ctx context.Context, script string, keys []string, args ...interface{}) (interface{}, error)
}

// Lock represents a distributed lock with thread-safe state management
type Lock struct {
	resource string
	value    string
	ttl      time.Duration
	redlock  *Redlock
	acquired int32 // atomic flag for lock state
	mu       sync.RWMutex
	created  time.Time
}

// LockResult represents the result of a lock acquisition attempt
type LockResult struct {
	Acquired bool
	Lock     *Lock
	Error    error
}

// RedlockMetrics tracks Redlock statistics with atomic operations
type RedlockMetrics struct {
	TotalAcquisitions      int64
	SuccessfulAcquisitions int64
	FailedAcquisitions     int64
	TotalReleases          int64
	SuccessfulReleases     int64
	FailedReleases         int64
	TotalExtensions        int64
	SuccessfulExtensions   int64
	FailedExtensions       int64
	AverageAcquisitionTime time.Duration
	mu                     sync.RWMutex // For non-atomic fields
}

// Config holds Redlock configuration with validation
type Config struct {
	Clients       []LockClient  `json:"clients"`
	Quorum        int           `json:"quorum"`
	RetryDelay    time.Duration `json:"retry_delay"`
	MaxRetries    int           `json:"max_retries"`
	DriftFactor   float64       `json:"drift_factor"`
	EnableMetrics bool          `json:"enable_metrics"`
}

// DefaultConfig returns a default Redlock configuration
func DefaultConfig(clients []LockClient) *Config {
	return &Config{
		Clients:       clients,
		Quorum:        0, // Will be calculated as majority
		RetryDelay:    DefaultRetryDelay,
		MaxRetries:    DefaultMaxRetries,
		DriftFactor:   DefaultDriftFactor,
		EnableMetrics: true,
	}
}

// ConservativeConfig returns a conservative Redlock configuration
func ConservativeConfig(clients []LockClient) *Config {
	return &Config{
		Clients:       clients,
		Quorum:        0, // Will be calculated as majority
		RetryDelay:    500 * time.Millisecond,
		MaxRetries:    5,
		DriftFactor:   0.005,
		EnableMetrics: true,
	}
}

// AggressiveConfig returns an aggressive Redlock configuration
func AggressiveConfig(clients []LockClient) *Config {
	return &Config{
		Clients:       clients,
		Quorum:        0, // Will be calculated as majority
		RetryDelay:    50 * time.Millisecond,
		MaxRetries:    10,
		DriftFactor:   0.02,
		EnableMetrics: true,
	}
}

// EnterpriseConfig returns an enterprise-grade Redlock configuration
func EnterpriseConfig(clients []LockClient) *Config {
	return &Config{
		Clients:       clients,
		Quorum:        0, // Will be calculated as majority
		RetryDelay:    200 * time.Millisecond,
		MaxRetries:    15,
		DriftFactor:   0.008,
		EnableMetrics: true,
	}
}

// NewRedlock creates a new Redlock instance with comprehensive validation
func NewRedlock(config *Config) (*Redlock, error) {
	// Validate configuration
	if err := validateRedlockConfig(config); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}

	// Apply defaults for missing values
	applyRedlockDefaults(config)

	// Calculate quorum if not specified
	quorum := config.Quorum
	if quorum == 0 {
		quorum = len(config.Clients)/2 + 1
	}

	redlock := &Redlock{
		clients:     config.Clients,
		quorum:      quorum,
		retryDelay:  config.RetryDelay,
		maxRetries:  config.MaxRetries,
		driftFactor: config.DriftFactor,
	}

	// Initialize metrics if enabled
	if config.EnableMetrics {
		redlock.metrics = &RedlockMetrics{}
	}

	return redlock, nil
}

// Lock attempts to acquire a distributed lock with validation
func (rl *Redlock) Lock(ctx context.Context, resource string, ttl time.Duration) (*Lock, error) {
	return rl.LockWithRetry(ctx, resource, ttl, rl.retryDelay, rl.maxRetries)
}

// LockWithRetry attempts to acquire a distributed lock with retry logic and context awareness
func (rl *Redlock) LockWithRetry(ctx context.Context, resource string, ttl time.Duration, retryDelay time.Duration, maxRetries int) (*Lock, error) {
	// Check if Redlock is closed
	if atomic.LoadInt32(&rl.closed) == 1 {
		return nil, ErrRedlockClosed
	}

	// Validate inputs
	if err := validateLockInputs(ctx, resource, ttl, retryDelay, maxRetries); err != nil {
		return nil, err
	}

	value := generateLockValue()
	startTime := time.Now()

	for attempt := 0; attempt <= maxRetries; attempt++ {
		// Check context cancellation before each attempt
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("%w: %v", ErrContextCancelled, err)
		}

		// Attempt to acquire lock with timeout
		lock, err := rl.tryAcquireLockWithTimeout(ctx, resource, value, ttl)
		if err == nil && lock != nil {
			// Update metrics
			if rl.metrics != nil {
				atomic.AddInt64(&rl.metrics.TotalAcquisitions, 1)
				atomic.AddInt64(&rl.metrics.SuccessfulAcquisitions, 1)
				rl.metrics.mu.Lock()
				rl.metrics.AverageAcquisitionTime = time.Since(startTime)
				rl.metrics.mu.Unlock()
			}

			logx.Info("Lock acquired successfully",
				logx.String("resource", resource),
				logx.String("value", value),
				logx.Int("attempt", attempt+1),
				logx.String("duration", time.Since(startTime).String()))

			return lock, nil
		}

		// Update metrics for failed attempt
		if rl.metrics != nil {
			atomic.AddInt64(&rl.metrics.TotalAcquisitions, 1)
			atomic.AddInt64(&rl.metrics.FailedAcquisitions, 1)
		}

		// If this is the last attempt, return error
		if attempt == maxRetries {
			return nil, fmt.Errorf("%w after %d attempts: %v", ErrLockAcquisitionFailed, maxRetries+1, err)
		}

		// Wait before retry with context awareness
		if retryDelay > 0 {
			select {
			case <-time.After(retryDelay):
			case <-ctx.Done():
				return nil, fmt.Errorf("%w: %v", ErrContextCancelled, ctx.Err())
			}
		}
	}

	return nil, ErrLockAcquisitionFailed
}

// tryAcquireLockWithTimeout attempts to acquire the lock on all clients with timeout
func (rl *Redlock) tryAcquireLockWithTimeout(ctx context.Context, resource, value string, ttl time.Duration) (*Lock, error) {
	startTime := time.Now()
	successCount := 0
	var lastError error

	// Try to acquire lock on all clients with timeout
	for _, client := range rl.clients {
		// Create timeout context for each client operation
		clientCtx, cancel := context.WithTimeout(ctx, 5*time.Second)

		err := client.Set(clientCtx, resource, []byte(value), ttl)
		cancel()

		if err == nil {
			successCount++
		} else {
			lastError = err
			logx.Warn("Failed to acquire lock on client",
				logx.String("resource", resource),
				logx.ErrorField(err))
		}
	}

	// Check if we have quorum
	if successCount < rl.quorum {
		// Release locks on clients where we succeeded
		rl.releaseLocksWithTimeout(ctx, resource, value, successCount)
		return nil, fmt.Errorf("%w: %d/%d clients, last error: %w", ErrQuorumNotReached, successCount, rl.quorum, lastError)
	}

	// Calculate validity time with improved clock drift handling
	driftTime := time.Duration(float64(ttl) * rl.driftFactor)
	validityTime := ttl - time.Since(startTime) - driftTime

	if validityTime <= 0 {
		// Release locks if validity time is negative
		rl.releaseLocksWithTimeout(ctx, resource, value, successCount)
		return nil, fmt.Errorf("%w: validity time is negative", ErrLockExpired)
	}

	lock := &Lock{
		resource: resource,
		value:    value,
		ttl:      validityTime,
		redlock:  rl,
		created:  time.Now(),
	}

	// Set acquired flag atomically
	atomic.StoreInt32(&lock.acquired, 1)

	logx.Info("Lock acquired",
		logx.String("resource", resource),
		logx.String("value", value),
		logx.String("validity", validityTime.String()),
		logx.Int("success_count", successCount),
		logx.String("drift_time", driftTime.String()))

	return lock, nil
}

// releaseLocksWithTimeout releases locks on clients where we succeeded with timeout
func (rl *Redlock) releaseLocksWithTimeout(ctx context.Context, resource, value string, successCount int) {
	released := 0
	for _, client := range rl.clients {
		// Create timeout context for each client operation
		clientCtx, cancel := context.WithTimeout(ctx, 2*time.Second)

		err := rl.releaseLockWithTimeout(clientCtx, client, resource, value)
		cancel()

		if err == nil {
			released++
		} else {
			logx.Warn("Failed to release lock on client",
				logx.String("resource", resource),
				logx.ErrorField(err))
		}
	}

	logx.Info("Released locks",
		logx.String("resource", resource),
		logx.Int("released", released),
		logx.Int("success_count", successCount))
}

// releaseLockWithTimeout releases a lock using Lua script for atomicity with timeout
func (rl *Redlock) releaseLockWithTimeout(ctx context.Context, client LockClient, resource, value string) error {
	script := `
		if redis.call("get", KEYS[1]) == ARGV[1] then
			return redis.call("del", KEYS[1])
		else
			return 0
		end
	`

	result, err := client.Eval(ctx, script, []string{resource}, value)
	if err != nil {
		return fmt.Errorf("failed to release lock: %w", err)
	}

	if result == nil || result == 0 {
		return fmt.Errorf("lock not found or value mismatch")
	}

	return nil
}

// Unlock releases the distributed lock with thread safety
func (l *Lock) Unlock(ctx context.Context) error {
	// Check if lock is acquired atomically
	if atomic.LoadInt32(&l.acquired) == 0 {
		return ErrLockNotAcquired
	}

	// Set acquired flag to false atomically
	if !atomic.CompareAndSwapInt32(&l.acquired, 1, 0) {
		return ErrLockNotAcquired
	}

	// Release lock on all clients with timeout
	released := 0
	for _, client := range l.redlock.clients {
		// Create timeout context for each client operation
		clientCtx, cancel := context.WithTimeout(ctx, 2*time.Second)

		err := l.redlock.releaseLockWithTimeout(clientCtx, client, l.resource, l.value)
		cancel()

		if err == nil {
			released++
		} else {
			logx.Warn("Failed to release lock on client",
				logx.String("resource", l.resource),
				logx.ErrorField(err))
		}
	}

	// Update metrics
	if l.redlock.metrics != nil {
		atomic.AddInt64(&l.redlock.metrics.TotalReleases, 1)
		if released > 0 {
			atomic.AddInt64(&l.redlock.metrics.SuccessfulReleases, 1)
		} else {
			atomic.AddInt64(&l.redlock.metrics.FailedReleases, 1)
		}
	}

	logx.Info("Lock released",
		logx.String("resource", l.resource),
		logx.String("value", l.value),
		logx.Int("released", released))

	if released == 0 {
		return fmt.Errorf("%w: no clients released", ErrLockReleaseFailed)
	}

	return nil
}

// Extend extends the lock validity time with thread safety
func (l *Lock) Extend(ctx context.Context, ttl time.Duration) error {
	// Validate TTL
	if err := validateTTL(ttl); err != nil {
		return err
	}

	// Check if lock is acquired atomically
	if atomic.LoadInt32(&l.acquired) == 0 {
		return ErrLockNotAcquired
	}

	// Try to extend lock on all clients with timeout
	successCount := 0
	for _, client := range l.redlock.clients {
		// Create timeout context for each client operation
		clientCtx, cancel := context.WithTimeout(ctx, 5*time.Second)

		script := `
			if redis.call("get", KEYS[1]) == ARGV[1] then
				return redis.call("expire", KEYS[1], ARGV[2])
			else
				return 0
			end
		`

		result, err := client.Eval(clientCtx, script, []string{l.resource}, l.value, int(ttl.Seconds()))
		cancel()

		if err == nil && result == 1 {
			successCount++
		} else {
			logx.Warn("Failed to extend lock on client",
				logx.String("resource", l.resource),
				logx.ErrorField(err))
		}
	}

	// Check if we have quorum
	if successCount < l.redlock.quorum {
		// Update metrics
		if l.redlock.metrics != nil {
			atomic.AddInt64(&l.redlock.metrics.TotalExtensions, 1)
			atomic.AddInt64(&l.redlock.metrics.FailedExtensions, 1)
		}

		return fmt.Errorf("%w: %d/%d clients", ErrLockExtensionFailed, successCount, l.redlock.quorum)
	}

	// Update lock TTL
	l.mu.Lock()
	l.ttl = ttl
	l.mu.Unlock()

	// Update metrics
	if l.redlock.metrics != nil {
		atomic.AddInt64(&l.redlock.metrics.TotalExtensions, 1)
		atomic.AddInt64(&l.redlock.metrics.SuccessfulExtensions, 1)
	}

	logx.Info("Lock extended",
		logx.String("resource", l.resource),
		logx.String("value", l.value),
		logx.String("new_ttl", ttl.String()),
		logx.Int("success_count", successCount))

	return nil
}

// IsAcquired returns whether the lock is currently acquired (thread-safe)
func (l *Lock) IsAcquired() bool {
	return atomic.LoadInt32(&l.acquired) == 1
}

// GetResource returns the lock resource name
func (l *Lock) GetResource() string {
	return l.resource
}

// GetValue returns the lock value
func (l *Lock) GetValue() string {
	return l.value
}

// GetTTL returns the lock TTL (thread-safe)
func (l *Lock) GetTTL() time.Duration {
	l.mu.RLock()
	defer l.mu.RUnlock()
	return l.ttl
}

// GetCreatedTime returns when the lock was created
func (l *Lock) GetCreatedTime() time.Time {
	return l.created
}

// IsExpired returns whether the lock has expired
func (l *Lock) IsExpired() bool {
	return time.Since(l.created) > l.ttl
}

// GetMetrics returns current Redlock metrics
func (rl *Redlock) GetMetrics() *RedlockMetrics {
	if rl.metrics == nil {
		return nil
	}

	rl.metrics.mu.RLock()
	defer rl.metrics.mu.RUnlock()

	metrics := &RedlockMetrics{
		TotalAcquisitions:      atomic.LoadInt64(&rl.metrics.TotalAcquisitions),
		SuccessfulAcquisitions: atomic.LoadInt64(&rl.metrics.SuccessfulAcquisitions),
		FailedAcquisitions:     atomic.LoadInt64(&rl.metrics.FailedAcquisitions),
		TotalReleases:          atomic.LoadInt64(&rl.metrics.TotalReleases),
		SuccessfulReleases:     atomic.LoadInt64(&rl.metrics.SuccessfulReleases),
		FailedReleases:         atomic.LoadInt64(&rl.metrics.FailedReleases),
		TotalExtensions:        atomic.LoadInt64(&rl.metrics.TotalExtensions),
		SuccessfulExtensions:   atomic.LoadInt64(&rl.metrics.SuccessfulExtensions),
		FailedExtensions:       atomic.LoadInt64(&rl.metrics.FailedExtensions),
		AverageAcquisitionTime: rl.metrics.AverageAcquisitionTime,
	}

	return metrics
}

// IsHealthy returns true if the Redlock is in a healthy state
func (rl *Redlock) IsHealthy() bool {
	if atomic.LoadInt32(&rl.closed) == 1 {
		return false
	}

	// Check if we have enough healthy clients
	healthyClients := 0
	for _, client := range rl.clients {
		// Simple health check - try to get a test key
		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
		_, err := client.Get(ctx, "__health_check__")
		cancel()

		if err == nil {
			healthyClients++
		}
	}

	return healthyClients >= rl.quorum
}

// GetHealthStatus returns detailed health information
func (rl *Redlock) GetHealthStatus() map[string]interface{} {
	metrics := rl.GetMetrics()

	health := map[string]interface{}{
		"is_healthy":               rl.IsHealthy(),
		"is_closed":                atomic.LoadInt32(&rl.closed) == 1,
		"client_count":             len(rl.clients),
		"quorum":                   rl.quorum,
		"retry_delay":              rl.retryDelay,
		"max_retries":              rl.maxRetries,
		"drift_factor":             rl.driftFactor,
		"total_acquisitions":       metrics.TotalAcquisitions,
		"successful_acquisitions":  metrics.SuccessfulAcquisitions,
		"failed_acquisitions":      metrics.FailedAcquisitions,
		"total_releases":           metrics.TotalReleases,
		"successful_releases":      metrics.SuccessfulReleases,
		"failed_releases":          metrics.FailedReleases,
		"total_extensions":         metrics.TotalExtensions,
		"successful_extensions":    metrics.SuccessfulExtensions,
		"failed_extensions":        metrics.FailedExtensions,
		"average_acquisition_time": metrics.AverageAcquisitionTime,
	}

	return health
}

// Close closes the Redlock with graceful shutdown
func (rl *Redlock) Close() error {
	// Set closed flag
	if !atomic.CompareAndSwapInt32(&rl.closed, 0, 1) {
		return nil // Already closed
	}

	logx.Info("Redlock closed")
	return nil
}

// generateLockValue generates a unique lock value
func generateLockValue() string {
	bytes := make([]byte, 16)
	_, err := rand.Read(bytes)
	if err != nil {
		// Fallback to timestamp-based value if crypto/rand fails
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(bytes)
}

// TryLock attempts to acquire a lock without blocking
func (rl *Redlock) TryLock(ctx context.Context, resource string, ttl time.Duration) (*Lock, error) {
	// Check if Redlock is closed
	if atomic.LoadInt32(&rl.closed) == 1 {
		return nil, ErrRedlockClosed
	}

	// Validate inputs
	if err := validateLockInputs(ctx, resource, ttl, 0, 0); err != nil {
		return nil, err
	}

	return rl.tryAcquireLockWithTimeout(ctx, resource, generateLockValue(), ttl)
}

// LockWithTimeout attempts to acquire a lock with a timeout
func (rl *Redlock) LockWithTimeout(ctx context.Context, resource string, ttl, timeout time.Duration) (*Lock, error) {
	// Validate timeout
	if err := validateTimeout(timeout); err != nil {
		return nil, err
	}

	timeoutCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return rl.Lock(timeoutCtx, resource, ttl)
}

// validateRedlockConfig validates Redlock configuration
func validateRedlockConfig(config *Config) error {
	if config == nil {
		return errors.New("configuration cannot be nil")
	}

	if len(config.Clients) == 0 {
		return errors.New("at least one client is required")
	}

	for i, client := range config.Clients {
		if client == nil {
			return fmt.Errorf("client at index %d cannot be nil", i)
		}
	}

	if config.Quorum < 0 {
		return fmt.Errorf("%w: quorum cannot be negative", ErrInvalidQuorum)
	}

	if config.Quorum > len(config.Clients) {
		return fmt.Errorf("%w: quorum cannot be greater than number of clients", ErrInvalidQuorum)
	}

	if config.RetryDelay < MinRetryDelayLimit || config.RetryDelay > MaxRetryDelayLimit {
		return fmt.Errorf("retry delay must be between %v and %v, got %v",
			MinRetryDelayLimit, MaxRetryDelayLimit, config.RetryDelay)
	}

	if config.MaxRetries < MinRetriesLimit || config.MaxRetries > MaxRetriesLimit {
		return fmt.Errorf("max retries must be between %d and %d, got %d",
			MinRetriesLimit, MaxRetriesLimit, config.MaxRetries)
	}

	if config.DriftFactor < MinDriftFactorLimit || config.DriftFactor > MaxDriftFactorLimit {
		return fmt.Errorf("drift factor must be between %f and %f, got %f",
			MinDriftFactorLimit, MaxDriftFactorLimit, config.DriftFactor)
	}

	return nil
}

// applyRedlockDefaults applies default values to configuration
func applyRedlockDefaults(config *Config) {
	if config.RetryDelay <= 0 {
		config.RetryDelay = DefaultRetryDelay
	}
	if config.MaxRetries <= 0 {
		config.MaxRetries = DefaultMaxRetries
	}
	if config.DriftFactor <= 0 {
		config.DriftFactor = DefaultDriftFactor
	}
}

// validateLockInputs validates lock operation inputs
func validateLockInputs(ctx context.Context, resource string, ttl time.Duration, retryDelay time.Duration, maxRetries int) error {
	if ctx == nil {
		return errors.New("context cannot be nil")
	}

	if err := validateResource(resource); err != nil {
		return err
	}

	if err := validateTTL(ttl); err != nil {
		return err
	}

	if retryDelay < 0 {
		return fmt.Errorf("%w: retry delay cannot be negative", ErrInvalidTimeout)
	}

	if maxRetries < 0 {
		return fmt.Errorf("max retries cannot be negative")
	}

	return nil
}

// validateResource validates resource name
func validateResource(resource string) error {
	if resource == "" {
		return fmt.Errorf("%w: resource name cannot be empty", ErrInvalidResource)
	}

	if len(resource) > MaxResourceNameLength {
		return fmt.Errorf("%w: resource name too long (max %d characters)", ErrInvalidResource, MaxResourceNameLength)
	}

	if len(resource) < MinResourceNameLength {
		return fmt.Errorf("%w: resource name too short (min %d characters)", ErrInvalidResource, MinResourceNameLength)
	}

	return nil
}

// validateTTL validates TTL duration
func validateTTL(ttl time.Duration) error {
	if ttl < MinTTLLimit || ttl > MaxTTLLimit {
		return fmt.Errorf("%w: TTL must be between %v and %v, got %v",
			ErrInvalidTTL, MinTTLLimit, MaxTTLLimit, ttl)
	}
	return nil
}

// validateTimeout validates timeout duration
func validateTimeout(timeout time.Duration) error {
	if timeout < MinTimeoutLimit || timeout > MaxTimeoutLimit {
		return fmt.Errorf("%w: timeout must be between %v and %v, got %v",
			ErrInvalidTimeout, MinTimeoutLimit, MaxTimeoutLimit, timeout)
	}
	return nil
}
