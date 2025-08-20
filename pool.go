package patternx

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"
)

// Production constants
const (
	MaxWorkersLimitPool            = 1000
	MinWorkersLimitPool            = 1
	MaxQueueSizeLimitPool          = 10000
	MinQueueSizeLimitPool          = 1
	MaxIdleTimeoutLimitPool        = 10 * time.Minute
	MinIdleTimeoutLimitPool        = 1 * time.Second
	MaxScaleUpThresholdLimitPool   = 100
	MinScaleUpThresholdLimitPool   = 1
	MaxScaleDownThresholdLimitPool = 50
	MinScaleDownThresholdLimitPool = 1
	MaxScaleUpCooldownLimitPool    = 1 * time.Minute
	MinScaleUpCooldownLimitPool    = 100 * time.Millisecond
	MaxScaleDownCooldownLimitPool  = 5 * time.Minute
	MinScaleDownCooldownLimitPool  = 1 * time.Second
	MaxJobTimeoutLimitPool         = 1 * time.Hour
	MinJobTimeoutLimitPool         = 1 * time.Millisecond
	DefaultJobTimeoutPool          = 30 * time.Second
	GracefulShutdownTimeoutPool    = 30 * time.Second
	AutoScaleIntervalPool          = 5 * time.Second
	MetricsCollectionIntervalPool  = 1 * time.Second
)

// WorkerPool provides a configurable worker pool with backpressure for batch operations
type WorkerPool struct {
	// Configuration
	config ConfigPool

	// Worker management
	workers    []*Worker
	workerPool chan *Worker
	mu         sync.RWMutex

	// Job management
	jobQueue   chan JobPool
	jobResults chan JobResultPool

	// Statistics - all atomic for thread safety
	stats *PoolStatsPool

	// Lifecycle
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	closed int32

	// Health monitoring
	healthStatus    atomic.Value // *HealthStatus
	lastHealthCheck time.Time
	healthMu        sync.RWMutex
}

// Config defines worker pool configuration
type ConfigPool struct {
	// Pool size configuration
	MinWorkers  int           `json:"min_workers"`
	MaxWorkers  int           `json:"max_workers"`
	QueueSize   int           `json:"queue_size"`
	IdleTimeout time.Duration `json:"idle_timeout"`

	// Performance tuning
	ScaleUpThreshold   int           `json:"scale_up_threshold"`
	ScaleDownThreshold int           `json:"scale_down_threshold"`
	ScaleUpCooldown    time.Duration `json:"scale_up_cooldown"`
	ScaleDownCooldown  time.Duration `json:"scale_down_cooldown"`

	// Monitoring
	EnableMetrics bool `json:"enable_metrics"`
}

// DefaultConfig returns a default configuration
func DefaultConfigPool() ConfigPool {
	return ConfigPool{
		MinWorkers:         2,
		MaxWorkers:         10,
		QueueSize:          100,
		IdleTimeout:        30 * time.Second,
		ScaleUpThreshold:   5,
		ScaleDownThreshold: 2,
		ScaleUpCooldown:    5 * time.Second,
		ScaleDownCooldown:  10 * time.Second,
		EnableMetrics:      true,
	}
}

// HighPerformanceConfig returns a configuration optimized for high throughput
func HighPerformanceConfigPool() ConfigPool {
	return ConfigPool{
		MinWorkers:         5,
		MaxWorkers:         50,
		QueueSize:          1000,
		IdleTimeout:        60 * time.Second,
		ScaleUpThreshold:   10,
		ScaleDownThreshold: 3,
		ScaleUpCooldown:    2 * time.Second,
		ScaleDownCooldown:  15 * time.Second,
		EnableMetrics:      true,
	}
}

// ResourceConstrainedConfig returns a configuration for resource-constrained environments
func ResourceConstrainedConfigPool() ConfigPool {
	return ConfigPool{
		MinWorkers:         1,
		MaxWorkers:         5,
		QueueSize:          50,
		IdleTimeout:        15 * time.Second,
		ScaleUpThreshold:   3,
		ScaleDownThreshold: 1,
		ScaleUpCooldown:    10 * time.Second,
		ScaleDownCooldown:  5 * time.Second,
		EnableMetrics:      false,
	}
}

// EnterpriseConfig returns a configuration for enterprise environments
func EnterpriseConfigPool() ConfigPool {
	return ConfigPool{
		MinWorkers:         10,
		MaxWorkers:         200,
		QueueSize:          5000,
		IdleTimeout:        5 * time.Minute,
		ScaleUpThreshold:   20,
		ScaleDownThreshold: 5,
		ScaleUpCooldown:    1 * time.Second,
		ScaleDownCooldown:  30 * time.Second,
		EnableMetrics:      true,
	}
}

// JobPool represents a task to be executed by a worker
type JobPool struct {
	ID       string                      `json:"id"`
	Task     func() (interface{}, error) `json:"-"`
	Priority int                         `json:"priority"`
	Timeout  time.Duration               `json:"timeout"`
	Created  time.Time                   `json:"created"`
	Metadata map[string]interface{}      `json:"metadata,omitempty"`
}

// JobResultPool represents the result of a job execution
type JobResultPool struct {
	JobID    string                 `json:"job_id"`
	Result   interface{}            `json:"result"`
	Error    error                  `json:"error"`
	Duration time.Duration          `json:"duration"`
	WorkerID int                    `json:"worker_id"`
	Metadata map[string]interface{} `json:"metadata,omitempty"`
}

// PoolStats holds worker pool statistics - all atomic for thread safety
type PoolStatsPool struct {
	ActiveWorkers    atomic.Int64
	IdleWorkers      atomic.Int64
	TotalWorkers     atomic.Int64
	QueuedJobs       atomic.Int64
	CompletedJobs    atomic.Int64
	FailedJobs       atomic.Int64
	TotalJobTime     atomic.Int64 // nanoseconds
	AverageJobTime   atomic.Int64 // nanoseconds
	ScaleUpCount     atomic.Int64
	ScaleDownCount   atomic.Int64
	LastScaleUp      atomic.Value // time.Time
	LastScaleDown    atomic.Value // time.Time
	JobsPerSecond    atomic.Value // float64
	QueueUtilization atomic.Value // float64
}

// HealthStatus represents the health status of the worker pool
type HealthStatus struct {
	IsHealthy   bool      `json:"is_healthy"`
	IsClosed    bool      `json:"is_closed"`
	LastCheck   time.Time `json:"last_check"`
	ErrorCount  int64     `json:"error_count"`
	WorkerCount int64     `json:"worker_count"`
	QueueSize   int64     `json:"queue_size"`
	Utilization float64   `json:"utilization"`
	Issues      []string  `json:"issues,omitempty"`
}

// New creates a new worker pool with the given configuration
func NewPool(config ConfigPool) (*WorkerPool, error) {
	if err := validateConfigPool(config); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrPoolInvalidConfigPool, err)
	}

	ctx, cancel := context.WithCancel(context.Background())

	pool := &WorkerPool{
		config:     config,
		jobQueue:   make(chan JobPool, config.QueueSize),
		jobResults: make(chan JobResultPool, config.QueueSize),
		stats:      &PoolStatsPool{},
		ctx:        ctx,
		cancel:     cancel,
	}

	// Initialize health status
	pool.healthStatus.Store(&HealthStatus{
		IsHealthy: true,
		LastCheck: time.Now(),
	})

	// Initialize workers
	if err := pool.initializeWorkers(); err != nil {
		cancel()
		return nil, fmt.Errorf("%w: %v", ErrPoolWorkerCreationFailed, err)
	}

	// Start background processes
	pool.startBackgroundProcesses()

	return pool, nil
}

// validateConfig validates the pool configuration
func validateConfigPool(config ConfigPool) error {
	if config.MinWorkers < MinWorkersLimitPool || config.MinWorkers > MaxWorkersLimitPool {
		return fmt.Errorf("min workers must be between %d and %d, got %d", MinWorkersLimitPool, MaxWorkersLimitPool, config.MinWorkers)
	}
	if config.MaxWorkers < MinWorkersLimitPool || config.MaxWorkers > MaxWorkersLimitPool {
		return fmt.Errorf("max workers must be between %d and %d, got %d", MinWorkersLimitPool, MaxWorkersLimitPool, config.MaxWorkers)
	}
	if config.MinWorkers > config.MaxWorkers {
		return fmt.Errorf("min workers (%d) cannot exceed max workers (%d)", config.MinWorkers, config.MaxWorkers)
	}
	if config.QueueSize < MinQueueSizeLimitPool || config.QueueSize > MaxQueueSizeLimitPool {
		return fmt.Errorf("queue size must be between %d and %d, got %d", MinQueueSizeLimitPool, MaxQueueSizeLimitPool, config.QueueSize)
	}
	if config.IdleTimeout < MinIdleTimeoutLimitPool || config.IdleTimeout > MaxIdleTimeoutLimitPool {
		return fmt.Errorf("idle timeout must be between %v and %v, got %v", MinIdleTimeoutLimitPool, MaxIdleTimeoutLimitPool, config.IdleTimeout)
	}
	if config.ScaleUpThreshold < MinScaleUpThresholdLimitPool || config.ScaleUpThreshold > MaxScaleUpThresholdLimitPool {
		return fmt.Errorf("scale up threshold must be between %d and %d, got %d", MinScaleUpThresholdLimitPool, MaxScaleUpThresholdLimitPool, config.ScaleUpThreshold)
	}
	if config.ScaleDownThreshold < MinScaleDownThresholdLimitPool || config.ScaleDownThreshold > MaxScaleDownThresholdLimitPool {
		return fmt.Errorf("scale down threshold must be between %d and %d, got %d", MinScaleDownThresholdLimitPool, MaxScaleDownThresholdLimitPool, config.ScaleDownThreshold)
	}
	if config.ScaleUpCooldown < MinScaleUpCooldownLimitPool || config.ScaleUpCooldown > MaxScaleUpCooldownLimitPool {
		return fmt.Errorf("scale up cooldown must be between %v and %v, got %v", MinScaleUpCooldownLimitPool, MaxScaleUpCooldownLimitPool, config.ScaleUpCooldown)
	}
	if config.ScaleDownCooldown < MinScaleDownCooldownLimitPool || config.ScaleDownCooldown > MaxScaleDownCooldownLimitPool {
		return fmt.Errorf("scale down cooldown must be between %v and %v, got %v", MinScaleDownCooldownLimitPool, MaxScaleDownCooldownLimitPool, config.ScaleDownCooldown)
	}
	return nil
}

// validateJob validates a job before submission
func validateJobPool(job JobPool) error {
	if job.Task == nil {
		return fmt.Errorf("job task function cannot be nil")
	}
	if job.Timeout < 0 {
		return fmt.Errorf("job timeout cannot be negative")
	}
	if job.Timeout > 0 && (job.Timeout < MinJobTimeoutLimitPool || job.Timeout > MaxJobTimeoutLimitPool) {
		return fmt.Errorf("job timeout must be between %v and %v, got %v", MinJobTimeoutLimitPool, MaxJobTimeoutLimitPool, job.Timeout)
	}
	return nil
}

// initializeWorkers creates the initial set of workers
func (wp *WorkerPool) initializeWorkers() error {
	wp.workerPool = make(chan *Worker, wp.config.MaxWorkers)
	wp.workers = make([]*Worker, 0, wp.config.MaxWorkers)

	// Create minimum number of workers
	for i := 0; i < wp.config.MinWorkers; i++ {
		worker := wp.createWorker(i)
		if worker == nil {
			return fmt.Errorf("failed to create worker %d", i)
		}
		wp.workers = append(wp.workers, worker)
		wp.workerPool <- worker
		wp.stats.TotalWorkers.Add(1)
		wp.stats.IdleWorkers.Add(1)
	}

	return nil
}

// createWorker creates a new worker
func (wp *WorkerPool) createWorker(id int) *Worker {
	worker := &Worker{
		id:       id,
		pool:     wp,
		jobChan:  make(chan JobPool, 1),
		stopChan: make(chan struct{}, 1), // Buffered to prevent blocking
		stats:    &WorkerStats{},
		active:   0,
	}

	wp.wg.Add(1)
	go worker.start()

	return worker
}

// startBackgroundProcesses starts background monitoring and scaling processes
func (wp *WorkerPool) startBackgroundProcesses() {
	// Start job result processor
	go wp.processJobResults()

	// Start auto-scaling if enabled and metrics are enabled
	if wp.config.MaxWorkers > wp.config.MinWorkers && wp.config.EnableMetrics {
		go wp.autoScale()
	}

	// Start metrics collection if enabled
	if wp.config.EnableMetrics {
		go wp.collectMetrics()
	}

	// Start health monitoring
	go wp.monitorHealth()
}

// Submit submits a job to the worker pool
func (wp *WorkerPool) Submit(job JobPool) error {
	if atomic.LoadInt32(&wp.closed) == 1 {
		return ErrPoolClosed
	}

	// Validate job
	if err := validateJobPool(job); err != nil {
		return fmt.Errorf("%w: %v", ErrPoolInvalidJob, err)
	}

	// Set default values
	if job.ID == "" {
		job.ID = fmt.Sprintf("job-%d", time.Now().UnixNano())
	}
	if job.Created.IsZero() {
		job.Created = time.Now()
	}
	if job.Timeout == 0 {
		job.Timeout = DefaultJobTimeoutPool
	}

	// Try to submit job (this provides backpressure)
	select {
	case wp.jobQueue <- job:
		wp.stats.QueuedJobs.Add(1)
		return nil
	default:
		return ErrPoolJobQueueFull
	}
}

// SubmitWithContext submits a job with context support
func (wp *WorkerPool) SubmitWithContext(ctx context.Context, job JobPool) error {
	if atomic.LoadInt32(&wp.closed) == 1 {
		return ErrPoolClosed
	}

	// Validate context
	if ctx == nil {
		return fmt.Errorf("%w: context cannot be nil", ErrPoolInvalidJob)
	}

	// Validate job
	if err := validateJobPool(job); err != nil {
		return fmt.Errorf("%w: %v", ErrPoolInvalidJob, err)
	}

	// Set default values
	if job.ID == "" {
		job.ID = fmt.Sprintf("job-%d", time.Now().UnixNano())
	}
	if job.Created.IsZero() {
		job.Created = time.Now()
	}
	if job.Timeout == 0 {
		job.Timeout = DefaultJobTimeoutPool
	}

	// Try to submit job with context
	select {
	case wp.jobQueue <- job:
		wp.stats.QueuedJobs.Add(1)
		return nil
	case <-ctx.Done():
		return fmt.Errorf("%w: %v", ErrPoolContextCancelled, ctx.Err())
	default:
		return ErrPoolJobQueueFull
	}
}

// SubmitBatch submits multiple jobs as a batch
func (wp *WorkerPool) SubmitBatch(jobs []JobPool) ([]error, error) {
	if atomic.LoadInt32(&wp.closed) == 1 {
		return nil, ErrPoolClosed
	}

	if len(jobs) == 0 {
		return nil, fmt.Errorf("%w: batch cannot be empty", ErrPoolInvalidJob)
	}

	errors := make([]error, len(jobs))
	var batchError error
	successCount := 0

	for i, job := range jobs {
		if err := wp.Submit(JobPool(job)); err != nil {
			errors[i] = err
			if batchError == nil {
				batchError = fmt.Errorf("%w: %d/%d jobs failed", ErrPoolInvalidJob, len(jobs)-successCount, len(jobs))
			}
		} else {
			successCount++
		}
	}

	return errors, batchError
}

// GetResult retrieves a job result
func (wp *WorkerPool) GetResult() (JobResultPool, error) {
	select {
	case result := <-wp.jobResults:
		return result, nil
	case <-wp.ctx.Done():
		return JobResultPool{}, ErrPoolClosed
	default:
		return JobResultPool{}, ErrPoolNoResultsAvailable
	}
}

// GetResultWithTimeout retrieves a job result with timeout
func (wp *WorkerPool) GetResultWithTimeout(timeout time.Duration) (JobResultPool, error) {
	if timeout <= 0 {
		return JobResultPool{}, fmt.Errorf("%w: timeout must be positive", ErrPoolTimeout)
	}

	select {
	case result := <-wp.jobResults:
		return result, nil
	case <-time.After(timeout):
		return JobResultPool{}, ErrPoolTimeout
	case <-wp.ctx.Done():
		return JobResultPool{}, ErrPoolClosed
	}
}

// GetStats returns current pool statistics
func (wp *WorkerPool) GetStats() *PoolStatsPool {
	// Calculate derived metrics
	completedJobs := wp.stats.CompletedJobs.Load()
	totalJobTime := wp.stats.TotalJobTime.Load()

	if completedJobs > 0 {
		wp.stats.AverageJobTime.Store(totalJobTime / completedJobs)
	}

	// Calculate jobs per second (rolling average)
	wp.calculateJobsPerSecond()

	// Calculate queue utilization
	queuedJobs := wp.stats.QueuedJobs.Load()
	queueSize := int64(wp.config.QueueSize)
	if queueSize > 0 {
		utilization := float64(queuedJobs) / float64(queueSize)
		wp.stats.QueueUtilization.Store(utilization)
	}

	return wp.stats
}

// calculateJobsPerSecond calculates the jobs per second rate
func (wp *WorkerPool) calculateJobsPerSecond() {
	// This is a simplified calculation - in production you might want a more sophisticated rolling average
	completedJobs := wp.stats.CompletedJobs.Load()
	if completedJobs > 0 {
		// Simple calculation - in production you'd track time windows
		wp.stats.JobsPerSecond.Store(float64(completedJobs) / time.Since(wp.lastHealthCheck).Seconds())
	}
}

// ScaleUp adds workers to the pool
func (wp *WorkerPool) ScaleUp(count int) error {
	if count <= 0 {
		return fmt.Errorf("%w: scale up count must be positive", ErrPoolScalingFailed)
	}

	wp.mu.Lock()
	defer wp.mu.Unlock()

	currentWorkers := len(wp.workers)
	maxNewWorkers := wp.config.MaxWorkers - currentWorkers

	if maxNewWorkers <= 0 {
		return fmt.Errorf("%w: cannot scale up - already at maximum workers (%d)", ErrPoolScalingFailed, wp.config.MaxWorkers)
	}

	if count > maxNewWorkers {
		count = maxNewWorkers
	}

	for i := 0; i < count; i++ {
		workerID := currentWorkers + i
		worker := wp.createWorker(workerID)
		if worker == nil {
			return fmt.Errorf("%w: failed to create worker %d", ErrPoolScalingFailed, workerID)
		}
		wp.workers = append(wp.workers, worker)
		wp.workerPool <- worker
		wp.stats.TotalWorkers.Add(1)
		wp.stats.IdleWorkers.Add(1)
	}

	wp.stats.ScaleUpCount.Add(1)
	wp.stats.LastScaleUp.Store(time.Now())

	return nil
}

// ScaleDown removes workers from the pool
func (wp *WorkerPool) ScaleDown(count int) error {
	if count <= 0 {
		return fmt.Errorf("%w: scale down count must be positive", ErrPoolScalingFailed)
	}

	wp.mu.Lock()
	defer wp.mu.Unlock()

	currentWorkers := len(wp.workers)
	minWorkers := wp.config.MinWorkers

	if currentWorkers <= minWorkers {
		return fmt.Errorf("%w: cannot scale down - already at minimum workers (%d)", ErrPoolScalingFailed, minWorkers)
	}

	maxRemovable := currentWorkers - minWorkers
	if count > maxRemovable {
		count = maxRemovable
	}

	// Stop workers from the end of the list
	for i := 0; i < count; i++ {
		workerIndex := currentWorkers - 1 - i
		worker := wp.workers[workerIndex]
		worker.stop()
	}

	// Remove workers from slice
	wp.workers = wp.workers[:currentWorkers-count]

	// Update worker counts
	wp.stats.TotalWorkers.Add(-int64(count))
	wp.stats.IdleWorkers.Add(-int64(count))

	wp.stats.ScaleDownCount.Add(1)
	wp.stats.LastScaleDown.Store(time.Now())

	return nil
}

// Close gracefully shuts down the worker pool
func (wp *WorkerPool) Close() error {
	if !atomic.CompareAndSwapInt32(&wp.closed, 0, 1) {
		return fmt.Errorf("worker pool already closed")
	}

	// Cancel context to stop background processes
	wp.cancel()

	// Wait a bit for background processes to stop
	time.Sleep(100 * time.Millisecond)

	// Stop all workers
	workers := make([]*Worker, 0)
	wp.mu.RLock()
	workers = append(workers, wp.workers...)
	wp.mu.RUnlock()

	for _, worker := range workers {
		worker.stop()
	}

	// Wait for all workers to finish with timeout
	done := make(chan struct{})
	go func() {
		wp.wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// All workers finished successfully
	case <-time.After(GracefulShutdownTimeoutPool):
		// Force shutdown after timeout
	}

	// Close channels
	close(wp.jobQueue)
	close(wp.jobResults)
	close(wp.workerPool)

	// Update health status
	wp.updateHealthStatus(false, []string{"pool closed"})

	return nil
}

// IsHealthy returns true if the worker pool is healthy
func (wp *WorkerPool) IsHealthy() bool {
	health := wp.healthStatus.Load().(*HealthStatus)
	return health.IsHealthy
}

// GetHealthStatus returns detailed health information
func (wp *WorkerPool) GetHealthStatus() *HealthStatus {
	wp.healthMu.RLock()
	defer wp.healthMu.RUnlock()

	health := wp.healthStatus.Load().(*HealthStatus)
	return health
}

// updateHealthStatus updates the health status
func (wp *WorkerPool) updateHealthStatus(isHealthy bool, issues []string) {
	wp.healthMu.Lock()
	defer wp.healthMu.Unlock()

	stats := wp.GetStats()
	health := &HealthStatus{
		IsHealthy:   isHealthy,
		IsClosed:    atomic.LoadInt32(&wp.closed) == 1,
		LastCheck:   time.Now(),
		ErrorCount:  stats.FailedJobs.Load(),
		WorkerCount: stats.TotalWorkers.Load(),
		QueueSize:   stats.QueuedJobs.Load(),
		Utilization: stats.QueueUtilization.Load().(float64),
		Issues:      issues,
	}

	wp.healthStatus.Store(health)
	wp.lastHealthCheck = time.Now()
}

// processJobResults processes completed job results
func (wp *WorkerPool) processJobResults() {
	for {
		select {
		case result := <-wp.jobResults:
			wp.stats.QueuedJobs.Add(-1)
			if result.Error != nil {
				wp.stats.FailedJobs.Add(1)
			} else {
				wp.stats.CompletedJobs.Add(1)
			}

			// Update total job time atomically
			wp.stats.TotalJobTime.Add(int64(result.Duration))

		case <-wp.ctx.Done():
			return
		}
	}
}

// autoScale automatically scales the worker pool based on load
func (wp *WorkerPool) autoScale() {
	ticker := time.NewTicker(AutoScaleIntervalPool)
	defer ticker.Stop()

	var lastScaleUp, lastScaleDown time.Time

	for {
		select {
		case <-ticker.C:
			// Check if pool is closed before attempting to scale
			if atomic.LoadInt32(&wp.closed) == 1 {
				return
			}

			stats := wp.GetStats()

			// Check if we should scale up
			if stats.QueuedJobs.Load() >= int64(wp.config.ScaleUpThreshold) &&
				time.Since(lastScaleUp) >= wp.config.ScaleUpCooldown &&
				stats.TotalWorkers.Load() < int64(wp.config.MaxWorkers) {

				scaleUpCount := 1
				if stats.QueuedJobs.Load() >= int64(wp.config.ScaleUpThreshold*2) {
					scaleUpCount = 2
				}

				// Scale up directly to avoid deadlocks
				if err := wp.ScaleUp(scaleUpCount); err == nil {
					lastScaleUp = time.Now()
				}
			}

			// Check if we should scale down
			if stats.IdleWorkers.Load() >= int64(wp.config.ScaleDownThreshold) &&
				time.Since(lastScaleDown) >= wp.config.ScaleDownCooldown &&
				stats.TotalWorkers.Load() > int64(wp.config.MinWorkers) {

				scaleDownCount := 1
				if stats.IdleWorkers.Load() >= int64(wp.config.ScaleDownThreshold*2) {
					scaleDownCount = 2
				}

				// Scale down directly to avoid deadlocks
				if err := wp.ScaleDown(scaleDownCount); err == nil {
					lastScaleDown = time.Now()
				}
			}

		case <-wp.ctx.Done():
			return
		}
	}
}

// collectMetrics collects detailed metrics if enabled
func (wp *WorkerPool) collectMetrics() {
	ticker := time.NewTicker(MetricsCollectionIntervalPool)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// Update health status
			wp.updateHealthStatus(true, nil)
		case <-wp.ctx.Done():
			return
		}
	}
}

// monitorHealth monitors the health of the worker pool
func (wp *WorkerPool) monitorHealth() {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			stats := wp.GetStats()
			var issues []string

			// Check for potential issues
			if stats.FailedJobs.Load() > stats.CompletedJobs.Load()/2 {
				issues = append(issues, "high failure rate")
			}
			if stats.QueueUtilization.Load().(float64) > 0.9 {
				issues = append(issues, "high queue utilization")
			}
			if stats.TotalWorkers.Load() == 0 {
				issues = append(issues, "no workers available")
			}

			isHealthy := len(issues) == 0
			wp.updateHealthStatus(isHealthy, issues)

		case <-wp.ctx.Done():
			return
		}
	}
}
