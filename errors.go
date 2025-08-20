package patternx

import "errors"

// Common errors for all patterns
var (
	ErrInvalidConfig    = errors.New("invalid configuration")
	ErrContextCancelled = errors.New("operation cancelled by context")
	ErrInvalidOperation = errors.New("invalid operation")
)

// Bloom Filter specific errors
var (
	ErrEmptyItem             = errors.New("item cannot be empty")
	ErrItemTooLong           = errors.New("item exceeds maximum length")
	ErrCapacityExceeded      = errors.New("bloom filter capacity exceeded")
	ErrStoreUnavailable      = errors.New("store is not available")
	ErrInvalidFalsePositive  = errors.New("false positive rate must be between 0 and 1")
	ErrInvalidExpectedItems  = errors.New("expected items must be greater than 0")
	ErrInvalidSize           = errors.New("bloom filter size is invalid")
	ErrInvalidHashCount      = errors.New("hash count is invalid")
	ErrStoreOperationFailed  = errors.New("store operation failed")
	ErrSerializationFailed   = errors.New("failed to serialize bloom filter state")
	ErrDeserializationFailed = errors.New("failed to deserialize bloom filter state")
	ErrFilterClosed          = errors.New("bloom filter is closed")
)

// Bulkhead specific errors
var (
	ErrBulkheadClosed    = errors.New("bulkhead is closed")
	ErrBulkheadTimeout   = errors.New("bulkhead operation timeout")
	ErrBulkheadRejected  = errors.New("bulkhead rejected operation")
	ErrBulkheadQueueFull = errors.New("bulkhead queue is full")
	ErrBulkheadUnhealthy = errors.New("bulkhead is unhealthy")
)

// Circuit Breaker specific errors
var (
	ErrCircuitBreakerOpen    = errors.New("circuit breaker is open")
	ErrCircuitBreakerTimeout = errors.New("circuit breaker operation timeout")
	ErrCircuitBreakerForced  = errors.New("circuit breaker is forced open")
	ErrOperationPanic        = errors.New("operation panicked")
)

// Dead Letter Queue specific errors
var (
	ErrDLQClosed             = errors.New("dead letter queue is closed")
	ErrDLQTimeout            = errors.New("dead letter queue operation timeout")
	ErrDLQQueueFull          = errors.New("dead letter queue is full")
	ErrDLQHandlerNotFound    = errors.New("dead letter queue handler not found")
	ErrDLQMaxRetriesExceeded = errors.New("dead letter queue max retries exceeded")
	ErrDLQOperationFailed    = errors.New("dead letter queue operation failed")
)

// Redlock specific errors
var (
	ErrRedlockTimeout      = errors.New("redlock operation timeout")
	ErrRedlockQuorumFailed = errors.New("redlock quorum not achieved")
	ErrRedlockNotAcquired  = errors.New("redlock not acquired")
	ErrRedlockExpired      = errors.New("redlock has expired")
	ErrRedlockNoClients    = errors.New("no redlock clients provided")
)

// Error types for Redlock operations
var (
	ErrRedlockClosed                = errors.New("redlock is closed")
	ErrRedlockInvalidConfig         = errors.New("invalid redlock configuration")
	ErrRedlockInvalidResource       = errors.New("invalid resource name")
	ErrRedlockInvalidTTL            = errors.New("invalid TTL duration")
	ErrRedlockInvalidTimeout        = errors.New("invalid timeout duration")
	ErrRedlockLockNotAcquired       = errors.New("lock not acquired")
	ErrRedlockLockAcquisitionFailed = errors.New("lock acquisition failed")
	ErrRedlockLockReleaseFailed     = errors.New("lock release failed")
	ErrRedlockLockExtensionFailed   = errors.New("lock extension failed")
	ErrRedlockQuorumNotReached      = errors.New("quorum not reached")
	ErrRedlockContextCancelled      = errors.New("operation cancelled by context")
	ErrRedlockLockExpired           = errors.New("lock expired")
	ErrRedlockClientUnavailable     = errors.New("client unavailable")
	ErrRedlockInvalidQuorum         = errors.New("invalid quorum value")
)

// Worker Pool specific errors
var (
	ErrPoolClosed                   = errors.New("worker pool is closed")
	ErrPoolTimeout                  = errors.New("worker pool operation timeout")
	ErrPoolQueueFull                = errors.New("worker pool queue is full")
	ErrPoolNoWorkers                = errors.New("no workers available")
	ErrPoolJobFailed                = errors.New("worker pool job failed")
	ErrWorkerChannelFull            = errors.New("worker channel full")
	ErrPoolInvalidConfigPool        = errors.New("invalid worker pool configuration")
	ErrPoolContextCancelled         = errors.New("operation cancelled by context")
	ErrPoolInvalidJobPool           = errors.New("invalid job")
	ErrPoolJobQueueFullPool         = errors.New("job queue is full")
	ErrPoolWorkerCreationFailedPool = errors.New("failed to create worker")
	ErrPoolScalingFailedPool        = errors.New("scaling operation failed")
	ErrPoolNoResultsAvailablePool   = errors.New("no results available")
)

// Error types for better error handling
var (
	ErrPoolInvalidJob           = errors.New("invalid job")
	ErrPoolJobQueueFull         = errors.New("job queue is full")
	ErrPoolWorkerCreationFailed = errors.New("failed to create worker")
	ErrPoolScalingFailed        = errors.New("scaling operation failed")
	ErrPoolNoResultsAvailable   = errors.New("no results available")
)

// PubSub specific errors
var (
	ErrPubSubClosed             = errors.New("pubsub is closed")
	ErrPubSubTimeout            = errors.New("pubsub operation timeout")
	ErrPubSubTopicNotFound      = errors.New("pubsub topic not found")
	ErrPubSubSubscriberNotFound = errors.New("pubsub subscriber not found")
	ErrPubSubMessageInvalid     = errors.New("pubsub message is invalid")
	ErrPubSubDeliveryFailed     = errors.New("pubsub message delivery failed")
)

// Retry specific errors
var (
	ErrRetryTimeout              = errors.New("retry operation timeout")
	ErrRetryMaxAttemptsExceeded  = errors.New("retry max attempts exceeded")
	ErrRetryNonRetryable         = errors.New("retry non-retryable error")
	ErrRetryOperationFailed      = errors.New("retry operation failed")
	ErrNoRetryHandlerRegistered  = errors.New("no retry handler registered")
	ErrMaxRetriesExceededRetry   = errors.New("maximum retries exceeded")
	ErrHandlerRejectedRetryRetry = errors.New("handler rejected retry")
)

// Error types for production scenarios
var (
	ErrRetryInvalidPolicy          = errors.New("invalid retry policy configuration")
	ErrRetryBudgetExceeded         = errors.New("retry budget exceeded")
	ErrRetryOperationTimeout       = errors.New("operation timeout")
	ErrRetryRateLimitExceeded      = errors.New("rate limit exceeded")
	ErrRetryInvalidTimeout         = errors.New("invalid timeout duration")
	ErrRetryInvalidRateLimit       = errors.New("invalid rate limit")
	ErrRetryInvalidRateLimitWindow = errors.New("invalid rate limit window")
	ErrRetryInvalidJitterPercent   = errors.New("invalid jitter percent")
	ErrRetryInvalidMultiplier      = errors.New("invalid multiplier")
	ErrRetryInvalidInitialDelay    = errors.New("invalid initial delay")
)
