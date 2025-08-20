package patternx

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/seasbee/go-logx"
)

// Error types for DLQ operations
var (
	ErrNoHandlerRegistered  = errors.New("no retry handler registered")
	ErrMaxRetriesExceeded   = errors.New("maximum retries exceeded")
	ErrHandlerRejectedRetry = errors.New("handler rejected retry")
)

// Constants for production constraints
const (
	MaxRetriesLimitDLQ      = 100
	MinRetriesLimitDLQ      = 0
	MaxRetryDelayLimitDLQ   = 24 * time.Hour
	MinRetryDelayLimitDLQ   = 1 * time.Millisecond
	MaxWorkerCountLimitDLQ  = 100
	MinWorkerCountLimitDLQ  = 1
	MaxQueueSizeLimitDLQ    = 100000
	MinQueueSizeLimitDLQ    = 10
	DefaultMaxRetriesDLQ    = 3
	DefaultRetryDelayDLQ    = 5 * time.Minute
	DefaultWorkerCountDLQ   = 2
	DefaultQueueSizeDLQ     = 1000
	MaxOperationTimeoutDLQ  = 60 * time.Second
	MinOperationTimeoutDLQ  = 1 * time.Millisecond
	GracefulShutdownWaitDLQ = 5 * time.Second
)

// DeadLetterQueue manages failed operations with retry capabilities
type DeadLetterQueue struct {
	// Configuration
	maxRetries  int
	retryDelay  time.Duration
	workerCount int
	queueSize   int

	// State management
	mu       sync.RWMutex
	queue    map[string]*FailedOperation
	handlers map[string]RetryHandler
	closed   int32 // atomic flag for graceful shutdown

	// Worker management
	ctx        context.Context
	cancel     context.CancelFunc
	workerChan chan *FailedOperation
	workerWg   sync.WaitGroup

	// Metrics with atomic operations
	metrics *Metrics
}

// FailedOperation represents a failed operation
type FailedOperation struct {
	ID          string                 `json:"id"`
	Operation   string                 `json:"operation"`
	Key         string                 `json:"key"`
	Data        interface{}            `json:"data"`
	Error       string                 `json:"error"`
	RetryCount  int                    `json:"retry_count"`
	MaxRetries  int                    `json:"max_retries"`
	CreatedAt   time.Time              `json:"created_at"`
	LastRetry   time.Time              `json:"last_retry"`
	NextRetry   time.Time              `json:"next_retry"`
	Metadata    map[string]interface{} `json:"metadata"`
	HandlerType string                 `json:"handler_type"`
}

// RetryHandler defines the interface for retrying failed operations
type RetryHandler interface {
	Retry(ctx context.Context, operation *FailedOperation) error
	ShouldRetry(operation *FailedOperation) bool
}

// Metrics tracks dead-letter queue statistics with atomic operations
type Metrics struct {
	TotalFailed    int64           `json:"total_failed"`
	TotalRetried   int64           `json:"total_retried"`
	TotalSucceeded int64           `json:"total_succeeded"`
	TotalDropped   int64           `json:"total_dropped"`
	CurrentQueue   int64           `json:"current_queue"`
	AverageRetries float64         `json:"average_retries"`
	LastRetryTime  time.Time       `json:"last_retry_time"`
	RetryDurations []time.Duration `json:"retry_durations"`
	mu             sync.RWMutex    // For non-atomic fields
}

// Config holds dead-letter queue configuration with validation
type ConfigDLQ struct {
	MaxRetries    int           `json:"max_retries"`
	RetryDelay    time.Duration `json:"retry_delay"`
	WorkerCount   int           `json:"worker_count"`
	QueueSize     int           `json:"queue_size"`
	EnableMetrics bool          `json:"enable_metrics"`
}

// DefaultConfig returns a default DLQ configuration
func DefaultConfigDLQ() *ConfigDLQ {
	return &ConfigDLQ{
		MaxRetries:    DefaultMaxRetriesDLQ,
		RetryDelay:    DefaultRetryDelayDLQ,
		WorkerCount:   DefaultWorkerCountDLQ,
		QueueSize:     DefaultQueueSizeDLQ,
		EnableMetrics: true,
	}
}

// HighPerformanceConfig returns a high-performance DLQ configuration
func HighPerformanceConfigDLQ() *ConfigDLQ {
	return &ConfigDLQ{
		MaxRetries:    10,
		RetryDelay:    1 * time.Minute,
		WorkerCount:   10,
		QueueSize:     10000,
		EnableMetrics: true,
	}
}

// ConservativeConfig returns a conservative DLQ configuration
func ConservativeConfigDLQ() *ConfigDLQ {
	return &ConfigDLQ{
		MaxRetries:    5,
		RetryDelay:    10 * time.Minute,
		WorkerCount:   2,
		QueueSize:     1000,
		EnableMetrics: true,
	}
}

// EnterpriseConfig returns an enterprise-grade DLQ configuration
func EnterpriseConfigDLQ() *ConfigDLQ {
	return &ConfigDLQ{
		MaxRetries:    20,
		RetryDelay:    2 * time.Minute,
		WorkerCount:   20,
		QueueSize:     50000,
		EnableMetrics: true,
	}
}

// NewDeadLetterQueue creates a new dead-letter queue with comprehensive validation
func NewDeadLetterQueue(config *ConfigDLQ) (*DeadLetterQueue, error) {
	// Validate configuration
	if err := validateDLQConfig(config); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}

	// Apply defaults for missing values
	applyDLQDefaults(config)

	ctx, cancel := context.WithCancel(context.Background())

	dlq := &DeadLetterQueue{
		maxRetries:  config.MaxRetries,
		retryDelay:  config.RetryDelay,
		workerCount: config.WorkerCount,
		queueSize:   config.QueueSize,
		queue:       make(map[string]*FailedOperation),
		handlers:    make(map[string]RetryHandler),
		ctx:         ctx,
		cancel:      cancel,
		workerChan:  make(chan *FailedOperation, config.QueueSize),
	}

	// Initialize metrics if enabled
	if config.EnableMetrics {
		dlq.metrics = &Metrics{}
	}

	// Start worker goroutines
	dlq.startWorkers()

	return dlq, nil
}

// AddFailedOperation adds a failed operation to the dead-letter queue with validation
func (dlq *DeadLetterQueue) AddFailedOperation(operation *FailedOperation) error {
	// Check if DLQ is closed
	if atomic.LoadInt32(&dlq.closed) == 1 {
		return ErrDLQClosed
	}

	// Validate operation
	if err := validateFailedOperation(operation); err != nil {
		return fmt.Errorf("%w: %v", ErrInvalidOperation, err)
	}

	// Set default values
	dlq.setOperationDefaults(operation)

	// Add to queue with proper synchronization
	dlq.mu.Lock()
	dlq.queue[operation.ID] = operation
	queueSize := len(dlq.queue)
	dlq.mu.Unlock()

	// Update metrics atomically
	if dlq.metrics != nil {
		atomic.AddInt64(&dlq.metrics.TotalFailed, 1)
		atomic.AddInt64(&dlq.metrics.CurrentQueue, 1)
	}

	// Send to worker channel with timeout
	select {
	case dlq.workerChan <- operation:
		logx.Info("Failed operation added to DLQ",
			logx.String("id", operation.ID),
			logx.String("operation", operation.Operation),
			logx.String("key", operation.Key),
			logx.Int("queue_size", queueSize))
	case <-time.After(100 * time.Millisecond):
		// Remove from queue if we can't send to worker
		dlq.mu.Lock()
		delete(dlq.queue, operation.ID)
		dlq.mu.Unlock()

		if dlq.metrics != nil {
			atomic.AddInt64(&dlq.metrics.CurrentQueue, -1)
		}

		logx.Error("DLQ worker channel full, operation dropped",
			logx.String("id", operation.ID))
		return ErrWorkerChannelFull
	}

	return nil
}

// RegisterHandler registers a retry handler for a specific operation type
func (dlq *DeadLetterQueue) RegisterHandler(operationType string, handler RetryHandler) error {
	if operationType == "" {
		return errors.New("operation type cannot be empty")
	}
	if handler == nil {
		return errors.New("handler cannot be nil")
	}

	dlq.mu.Lock()
	defer dlq.mu.Unlock()
	dlq.handlers[operationType] = handler

	logx.Info("Retry handler registered",
		logx.String("operation_type", operationType))
	return nil
}

// startWorkers starts the worker goroutines
func (dlq *DeadLetterQueue) startWorkers() {
	for i := 0; i < dlq.workerCount; i++ {
		dlq.workerWg.Add(1)
		go dlq.worker(i)
	}
	logx.Info("DLQ workers started", logx.Int("worker_count", dlq.workerCount))
}

// worker processes failed operations with proper error handling
func (dlq *DeadLetterQueue) worker(id int) {
	defer dlq.workerWg.Done()
	logx.Info("DLQ worker started", logx.Int("worker_id", id))

	for {
		select {
		case operation := <-dlq.workerChan:
			if operation == nil {
				continue
			}
			dlq.processOperation(operation)
		case <-dlq.ctx.Done():
			logx.Info("DLQ worker stopped", logx.Int("worker_id", id))
			return
		}
	}
}

// processOperation processes a single failed operation with context awareness
func (dlq *DeadLetterQueue) processOperation(operation *FailedOperation) {
	// Check if DLQ is closed
	if atomic.LoadInt32(&dlq.closed) == 1 {
		dlq.dropOperation(operation, "DLQ closed")
		return
	}

	// Wait until next retry time with context awareness
	if time.Now().Before(operation.NextRetry) {
		waitTime := time.Until(operation.NextRetry)
		select {
		case <-time.After(waitTime):
		case <-dlq.ctx.Done():
			dlq.dropOperation(operation, "context cancelled during wait")
			return
		}
	}

	// Check if we should retry
	if operation.RetryCount >= operation.MaxRetries {
		dlq.dropOperation(operation, "max retries exceeded")
		return
	}

	// Get handler for operation type
	dlq.mu.RLock()
	handler, exists := dlq.handlers[operation.HandlerType]
	dlq.mu.RUnlock()

	if !exists {
		dlq.dropOperation(operation, "no handler registered")
		return
	}

	// Check if handler thinks we should retry
	if !handler.ShouldRetry(operation) {
		dlq.dropOperation(operation, "handler rejected retry")
		return
	}

	// Attempt retry with timeout
	operation.RetryCount++
	operation.LastRetry = time.Now()
	startTime := time.Now()

	// Create context with timeout for retry operation
	retryCtx, cancel := context.WithTimeout(dlq.ctx, MaxOperationTimeoutDLQ)
	defer cancel()

	err := handler.Retry(retryCtx, operation)

	// Update metrics
	if dlq.metrics != nil {
		atomic.AddInt64(&dlq.metrics.TotalRetried, 1)
		dlq.metrics.mu.Lock()
		dlq.metrics.LastRetryTime = time.Now()
		dlq.metrics.RetryDurations = append(dlq.metrics.RetryDurations, time.Since(startTime))
		dlq.metrics.mu.Unlock()
	}

	if err == nil {
		// Success - remove from queue
		dlq.succeedOperation(operation)
	} else {
		// Failed - schedule next retry or drop
		operation.Error = err.Error()
		operation.NextRetry = time.Now().Add(dlq.retryDelay * time.Duration(operation.RetryCount))

		logx.Error("Retry failed",
			logx.String("id", operation.ID),
			logx.String("operation", operation.Operation),
			logx.String("key", operation.Key),
			logx.Int("retry_count", operation.RetryCount),
			logx.ErrorField(err))

		// Re-queue for next retry with timeout
		select {
		case dlq.workerChan <- operation:
		case <-time.After(100 * time.Millisecond):
			dlq.dropOperation(operation, "worker channel full during requeue")
		}
	}
}

// succeedOperation marks an operation as successful with proper synchronization
func (dlq *DeadLetterQueue) succeedOperation(operation *FailedOperation) {
	dlq.mu.Lock()
	delete(dlq.queue, operation.ID)
	dlq.mu.Unlock()

	if dlq.metrics != nil {
		atomic.AddInt64(&dlq.metrics.TotalSucceeded, 1)
		atomic.AddInt64(&dlq.metrics.CurrentQueue, -1)
	}

	logx.Info("Operation retry succeeded",
		logx.String("id", operation.ID),
		logx.String("operation", operation.Operation),
		logx.String("key", operation.Key),
		logx.Int("retry_count", operation.RetryCount))
}

// dropOperation drops an operation from the queue with proper synchronization
func (dlq *DeadLetterQueue) dropOperation(operation *FailedOperation, reason string) {
	dlq.mu.Lock()
	delete(dlq.queue, operation.ID)
	dlq.mu.Unlock()

	if dlq.metrics != nil {
		atomic.AddInt64(&dlq.metrics.TotalDropped, 1)
		atomic.AddInt64(&dlq.metrics.CurrentQueue, -1)
	}

	logx.Warn("Operation dropped from DLQ",
		logx.String("id", operation.ID),
		logx.String("operation", operation.Operation),
		logx.String("key", operation.Key),
		logx.String("reason", reason),
		logx.Int("retry_count", operation.RetryCount))
}

// GetMetrics returns current metrics with thread safety
func (dlq *DeadLetterQueue) GetMetrics() *Metrics {
	if dlq.metrics == nil {
		return nil
	}

	dlq.metrics.mu.RLock()
	defer dlq.metrics.mu.RUnlock()

	// Calculate average retries
	var totalRetries int64
	dlq.mu.RLock()
	for _, op := range dlq.queue {
		totalRetries += int64(op.RetryCount)
	}
	dlq.mu.RUnlock()

	avgRetries := 0.0
	currentQueue := atomic.LoadInt64(&dlq.metrics.CurrentQueue)
	if currentQueue > 0 {
		avgRetries = float64(totalRetries) / float64(currentQueue)
	}

	metrics := &Metrics{
		TotalFailed:    atomic.LoadInt64(&dlq.metrics.TotalFailed),
		TotalRetried:   atomic.LoadInt64(&dlq.metrics.TotalRetried),
		TotalSucceeded: atomic.LoadInt64(&dlq.metrics.TotalSucceeded),
		TotalDropped:   atomic.LoadInt64(&dlq.metrics.TotalDropped),
		CurrentQueue:   currentQueue,
		AverageRetries: avgRetries,
		LastRetryTime:  dlq.metrics.LastRetryTime,
		RetryDurations: make([]time.Duration, len(dlq.metrics.RetryDurations)),
	}
	copy(metrics.RetryDurations, dlq.metrics.RetryDurations)

	return metrics
}

// GetQueue returns all operations in the queue with thread safety
func (dlq *DeadLetterQueue) GetQueue() []*FailedOperation {
	dlq.mu.RLock()
	defer dlq.mu.RUnlock()

	operations := make([]*FailedOperation, 0, len(dlq.queue))
	for _, op := range dlq.queue {
		operations = append(operations, op)
	}

	return operations
}

// ClearQueue clears all operations from the queue with proper synchronization
func (dlq *DeadLetterQueue) ClearQueue() {
	dlq.mu.Lock()
	count := len(dlq.queue)
	dlq.queue = make(map[string]*FailedOperation)
	dlq.mu.Unlock()

	if dlq.metrics != nil {
		atomic.StoreInt64(&dlq.metrics.CurrentQueue, 0)
	}

	logx.Info("DLQ queue cleared", logx.Int("operations_cleared", count))
}

// IsHealthy returns true if the DLQ is in a healthy state
func (dlq *DeadLetterQueue) IsHealthy() bool {
	if atomic.LoadInt32(&dlq.closed) == 1 {
		return false
	}

	// Check if workers are responsive
	select {
	case dlq.workerChan <- nil: // Test if channel is responsive
		// Remove the nil operation
		select {
		case <-dlq.workerChan:
		default:
		}
		return true
	default:
		return false
	}
}

// GetHealthStatus returns detailed health information
func (dlq *DeadLetterQueue) GetHealthStatus() map[string]interface{} {
	metrics := dlq.GetMetrics()

	health := map[string]interface{}{
		"is_healthy":      dlq.IsHealthy(),
		"is_closed":       atomic.LoadInt32(&dlq.closed) == 1,
		"worker_count":    dlq.workerCount,
		"queue_size":      dlq.queueSize,
		"current_queue":   metrics.CurrentQueue,
		"total_failed":    metrics.TotalFailed,
		"total_retried":   metrics.TotalRetried,
		"total_succeeded": metrics.TotalSucceeded,
		"total_dropped":   metrics.TotalDropped,
		"average_retries": metrics.AverageRetries,
		"last_retry_time": metrics.LastRetryTime,
		"max_retries":     dlq.maxRetries,
		"retry_delay":     dlq.retryDelay,
	}

	return health
}

// Close closes the dead-letter queue with graceful shutdown
func (dlq *DeadLetterQueue) Close() error {
	// Set closed flag
	if !atomic.CompareAndSwapInt32(&dlq.closed, 0, 1) {
		return nil // Already closed
	}

	// Cancel context to stop workers
	dlq.cancel()

	// Wait for workers to finish with timeout
	done := make(chan struct{})
	go func() {
		dlq.workerWg.Wait()
		close(done)
	}()

	select {
	case <-done:
		logx.Info("DLQ workers stopped gracefully")
	case <-time.After(GracefulShutdownWaitDLQ):
		logx.Warn("DLQ workers did not stop within timeout")
	}

	// Close worker channel
	close(dlq.workerChan)

	logx.Info("Dead-letter queue closed")
	return nil
}

// WriteBehindHandler implements RetryHandler for write-behind operations
type WriteBehindHandler struct {
	writer func(ctx context.Context, key string, data interface{}) error
}

// NewWriteBehindHandler creates a new write-behind handler
func NewWriteBehindHandler(writer func(ctx context.Context, key string, data interface{}) error) *WriteBehindHandler {
	if writer == nil {
		panic("writer function cannot be nil")
	}
	return &WriteBehindHandler{
		writer: writer,
	}
}

// Retry retries a write-behind operation
func (h *WriteBehindHandler) Retry(ctx context.Context, operation *FailedOperation) error {
	return h.writer(ctx, operation.Key, operation.Data)
}

// ShouldRetry determines if a write-behind operation should be retried
func (h *WriteBehindHandler) ShouldRetry(operation *FailedOperation) bool {
	// Retry write-behind operations unless they've exceeded max retries
	return operation.RetryCount < operation.MaxRetries
}

// validateDLQConfig validates DLQ configuration
func validateDLQConfig(config *ConfigDLQ) error {
	if config == nil {
		return errors.New("configuration cannot be nil")
	}

	if config.MaxRetries < MinRetriesLimitDLQ || config.MaxRetries > MaxRetriesLimitDLQ {
		return fmt.Errorf("max retries must be between %d and %d, got %d",
			MinRetriesLimitDLQ, MaxRetriesLimitDLQ, config.MaxRetries)
	}

	if config.RetryDelay < MinRetryDelayLimitDLQ || config.RetryDelay > MaxRetryDelayLimitDLQ {
		return fmt.Errorf("retry delay must be between %v and %v, got %v",
			MinRetryDelayLimitDLQ, MaxRetryDelayLimitDLQ, config.RetryDelay)
	}

	if config.WorkerCount < MinWorkerCountLimitDLQ || config.WorkerCount > MaxWorkerCountLimitDLQ {
		return fmt.Errorf("worker count must be between %d and %d, got %d",
			MinWorkerCountLimitDLQ, MaxWorkerCountLimitDLQ, config.WorkerCount)
	}

	if config.QueueSize < MinQueueSizeLimitDLQ || config.QueueSize > MaxQueueSizeLimitDLQ {
		return fmt.Errorf("queue size must be between %d and %d, got %d",
			MinQueueSizeLimitDLQ, MaxQueueSizeLimitDLQ, config.QueueSize)
	}

	return nil
}

// applyDLQDefaults applies default values to configuration
func applyDLQDefaults(config *ConfigDLQ) {
	if config.MaxRetries <= 0 {
		config.MaxRetries = DefaultMaxRetriesDLQ
	}
	if config.RetryDelay <= 0 {
		config.RetryDelay = DefaultRetryDelayDLQ
	}
	if config.WorkerCount <= 0 {
		config.WorkerCount = DefaultWorkerCountDLQ
	}
	if config.QueueSize <= 0 {
		config.QueueSize = DefaultQueueSizeDLQ
	}
}

// validateFailedOperation validates failed operation
func validateFailedOperation(operation *FailedOperation) error {
	if operation == nil {
		return errors.New("operation cannot be nil")
	}
	if operation.Operation == "" {
		return errors.New("operation type cannot be empty")
	}
	if operation.Key == "" {
		return errors.New("operation key cannot be empty")
	}
	if operation.HandlerType == "" {
		return errors.New("handler type cannot be empty")
	}
	return nil
}

// setOperationDefaults sets default values for operation
func (dlq *DeadLetterQueue) setOperationDefaults(operation *FailedOperation) {
	if operation.ID == "" {
		operation.ID = fmt.Sprintf("%s-%d", operation.Key, time.Now().UnixNano())
	}
	if operation.CreatedAt.IsZero() {
		operation.CreatedAt = time.Now()
	}
	if operation.MaxRetries == 0 {
		operation.MaxRetries = dlq.maxRetries
	}
	if operation.NextRetry.IsZero() {
		operation.NextRetry = time.Now().Add(dlq.retryDelay)
	}
	if operation.Metadata == nil {
		operation.Metadata = make(map[string]interface{})
	}
}
