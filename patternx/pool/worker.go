package pool

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"
)

// Worker represents a worker in the pool
type Worker struct {
	ID       int
	pool     *WorkerPool
	jobChan  chan Job
	stopChan chan struct{}
	stats    *WorkerStats
	active   int32
	stopped  int32
}

// WorkerStats holds worker-specific statistics - all atomic for thread safety
type WorkerStats struct {
	JobsProcessed atomic.Int64
	JobsFailed    atomic.Int64
	TotalJobTime  atomic.Int64 // nanoseconds
	LastJobTime   atomic.Value // time.Time
	IdleTime      atomic.Int64 // nanoseconds
	LastIdleStart atomic.Value // time.Time
	PanicCount    atomic.Int64
}

// start starts the worker loop with panic recovery
func (w *Worker) start() {
	defer func() {
		if r := recover(); r != nil {
			// Log panic and increment panic count
			w.stats.PanicCount.Add(1)
			// Continue running the worker unless it's been stopped
			if atomic.LoadInt32(&w.stopped) == 0 {
				// Don't restart immediately to avoid infinite loops
				time.Sleep(10 * time.Millisecond)
				go w.start() // Restart the worker
			}
		}
		w.pool.wg.Done()
	}()

	for {
		// Check if worker has been stopped
		if atomic.LoadInt32(&w.stopped) == 1 {
			return
		}

		select {
		case job := <-w.pool.jobQueue:
			// Mark worker as active
			atomic.StoreInt32(&w.active, 1)
			w.pool.stats.ActiveWorkers.Add(1)
			w.pool.stats.IdleWorkers.Add(-1)

			// Process the job with panic recovery
			result := w.processJob(job)

			// Send result back to pool
			select {
			case w.pool.jobResults <- result:
			case <-w.pool.ctx.Done():
				return
			}

			// Mark worker as idle
			atomic.StoreInt32(&w.active, 0)
			w.pool.stats.ActiveWorkers.Add(-1)
			w.pool.stats.IdleWorkers.Add(1)

			// Return worker to pool
			select {
			case w.pool.workerPool <- w:
			case <-w.pool.ctx.Done():
				return
			}

		case <-w.stopChan:
			atomic.StoreInt32(&w.stopped, 1)
			return

		case <-w.pool.ctx.Done():
			atomic.StoreInt32(&w.stopped, 1)
			return
		}
	}
}

// processJob processes a single job with comprehensive error handling
func (w *Worker) processJob(job Job) JobResult {
	start := time.Now()
	result := JobResult{
		JobID:    job.ID,
		WorkerID: w.ID,
	}

	// Validate job
	if job.Task == nil {
		result.Error = fmt.Errorf("job task is nil")
		result.Duration = time.Since(start)
		w.updateJobStats(result)
		return result
	}

	// Create context with timeout
	ctx := context.Background()
	if job.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, job.Timeout)
		defer cancel()
	}

	// Execute the job with panic recovery
	jobResult, err := w.executeJob(ctx, job)
	result.Result = jobResult
	result.Error = err
	result.Duration = time.Since(start)

	// Update worker statistics
	w.updateJobStats(result)

	return result
}

// executeJob executes the job with proper error handling and panic recovery
func (w *Worker) executeJob(ctx context.Context, job Job) (interface{}, error) {
	// Check if context is already cancelled
	select {
	case <-ctx.Done():
		return nil, fmt.Errorf("context cancelled before execution: %w", ctx.Err())
	default:
	}

	// Execute the job with panic recovery
	done := make(chan struct{})
	var result interface{}
	var err error
	var panicErr interface{}

	go func() {
		defer func() {
			if r := recover(); r != nil {
				panicErr = r
				w.stats.PanicCount.Add(1)
			}
			close(done)
		}()
		result, err = job.Task()
	}()

	// Wait for completion or timeout
	select {
	case <-done:
		if panicErr != nil {
			return nil, fmt.Errorf("job panicked: %v", panicErr)
		}
		return result, err
	case <-ctx.Done():
		// Context was cancelled - this could be timeout or manual cancellation
		if ctx.Err() == context.DeadlineExceeded {
			return nil, fmt.Errorf("job execution timeout: %w", ctx.Err())
		}
		return nil, fmt.Errorf("context cancelled: %w", ctx.Err())
	}
}

// updateJobStats updates worker statistics atomically
func (w *Worker) updateJobStats(result JobResult) {
	w.stats.JobsProcessed.Add(1)
	if result.Error != nil {
		w.stats.JobsFailed.Add(1)
	}

	// Update total job time
	w.stats.TotalJobTime.Add(int64(result.Duration))

	// Update last job time
	w.stats.LastJobTime.Store(time.Now())
}

// stop stops the worker gracefully
func (w *Worker) stop() {
	// Mark worker as stopped
	atomic.StoreInt32(&w.stopped, 1)

	// Send stop signal
	select {
	case w.stopChan <- struct{}{}:
	default:
		// Worker might already be stopped
	}
}

// IsActive returns true if the worker is currently processing a job
func (w *Worker) IsActive() bool {
	return atomic.LoadInt32(&w.active) == 1
}

// IsStopped returns true if the worker has been stopped
func (w *Worker) IsStopped() bool {
	return atomic.LoadInt32(&w.stopped) == 1
}

// GetStats returns the worker's statistics
func (w *Worker) GetStats() *WorkerStats {
	stats := &WorkerStats{}

	// Copy atomic values
	stats.JobsProcessed.Store(w.stats.JobsProcessed.Load())
	stats.JobsFailed.Store(w.stats.JobsFailed.Load())
	stats.TotalJobTime.Store(w.stats.TotalJobTime.Load())
	stats.PanicCount.Store(w.stats.PanicCount.Load())

	// Copy atomic values
	if lastJobTime := w.stats.LastJobTime.Load(); lastJobTime != nil {
		stats.LastJobTime.Store(lastJobTime)
	}
	if lastIdleStart := w.stats.LastIdleStart.Load(); lastIdleStart != nil {
		stats.LastIdleStart.Store(lastIdleStart)
	}

	return stats
}

// GetHealthStatus returns the worker's health status
func (w *Worker) GetHealthStatus() map[string]interface{} {
	stats := w.GetStats()

	health := map[string]interface{}{
		"is_active":      w.IsActive(),
		"is_stopped":     w.IsStopped(),
		"jobs_processed": stats.JobsProcessed.Load(),
		"jobs_failed":    stats.JobsFailed.Load(),
		"panic_count":    stats.PanicCount.Load(),
	}

	// Calculate success rate
	processed := stats.JobsProcessed.Load()
	if processed > 0 {
		successRate := float64(processed-stats.JobsFailed.Load()) / float64(processed)
		health["success_rate"] = successRate
	}

	// Calculate average job time
	if processed > 0 {
		avgJobTime := time.Duration(stats.TotalJobTime.Load() / processed)
		health["average_job_time"] = avgJobTime
	}

	return health
}
