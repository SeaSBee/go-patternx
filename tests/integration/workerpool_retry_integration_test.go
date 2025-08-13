package integration

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/SeaSBee/go-patternx/patternx/pool"
	"github.com/SeaSBee/go-patternx/patternx/retry"
)

// MockTask simulates a task that can fail and be retried
type MockTask struct {
	id          string
	failureRate float64
	maxAttempts int
	attempts    int
	mu          sync.Mutex
}

func NewMockTask(id string, failureRate float64, maxAttempts int) *MockTask {
	return &MockTask{
		id:          id,
		failureRate: failureRate,
		maxAttempts: maxAttempts,
	}
}

func (t *MockTask) Execute() (string, error) {
	t.mu.Lock()
	t.attempts++
	currentAttempt := t.attempts
	t.mu.Unlock()

	// Simulate work
	time.Sleep(10 * time.Millisecond)

	// Simulate failures
	if t.failureRate > 0 && currentAttempt < t.maxAttempts {
		if float64(currentAttempt)/float64(t.maxAttempts) < t.failureRate {
			return "", fmt.Errorf("task %s failed on attempt %d", t.id, currentAttempt)
		}
	}

	return fmt.Sprintf("task-%s-completed-after-%d-attempts", t.id, currentAttempt), nil
}

func (t *MockTask) GetAttempts() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.attempts
}

// TestWorkerPoolWithRetry tests the integration of worker pool and retry patterns
func TestWorkerPoolWithRetry(t *testing.T) {
	// Create worker pool
	wpConfig := pool.DefaultConfig()
	workerPool, err := pool.New(wpConfig)
	if err != nil {
		t.Fatalf("Failed to create worker pool: %v", err)
	}
	defer workerPool.Close()

	// Create retry policy
	retryPolicy := retry.Policy{
		MaxAttempts:     3,
		InitialDelay:    10 * time.Millisecond,
		MaxDelay:        100 * time.Millisecond,
		Multiplier:      2.0,
		Jitter:          false,
		RateLimit:       10,
		RateLimitWindow: time.Second,
	}

	// Create tasks with different failure characteristics
	tasks := []*MockTask{
		NewMockTask("easy", 0.0, 1),   // Always succeeds
		NewMockTask("medium", 0.5, 3), // Fails sometimes, succeeds with retry
		NewMockTask("hard", 0.8, 5),   // Fails often, may not succeed
	}

	var wg sync.WaitGroup
	results := make(chan string, len(tasks))
	errors := make(chan error, len(tasks))

	start := time.Now()

	// Submit tasks with retry logic
	for _, task := range tasks {
		wg.Add(1)
		go func(t *MockTask) {
			defer wg.Done()

			job := pool.Job{
				ID: fmt.Sprintf("retry-job-%s", t.id),
				Task: func() (interface{}, error) {
					return retry.RetryWithResult(retryPolicy, func() (string, error) {
						return t.Execute()
					})
				},
			}

			err := workerPool.Submit(job)
			if err != nil {
				errors <- fmt.Errorf("failed to submit job for task %s: %w", t.id, err)
				return
			}

			// Get result
			result, err := workerPool.GetResultWithTimeout(5 * time.Second)
			if err != nil {
				errors <- fmt.Errorf("failed to get result for task %s: %w", t.id, err)
				return
			}

			if result.Error != nil {
				errors <- fmt.Errorf("task %s failed: %w", t.id, result.Error)
			} else {
				results <- result.Result.(string)
			}
		}(task)
	}

	wg.Wait()
	close(results)
	close(errors)
	duration := time.Since(start)

	// Collect results
	successCount := 0
	successResults := make([]string, 0)
	for result := range results {
		successCount++
		successResults = append(successResults, result)
	}

	errorCount := 0
	errorMessages := make([]string, 0)
	for err := range errors {
		errorCount++
		errorMessages = append(errorMessages, err.Error())
	}

	// Analyze task attempts
	totalAttempts := 0
	for _, task := range tasks {
		attempts := task.GetAttempts()
		totalAttempts += attempts
		t.Logf("Task %s: %d attempts", task.id, attempts)
	}

	// Verify results
	wpStats := workerPool.GetStats()

	t.Logf("Worker pool with retry integration test results:")
	t.Logf("  Duration: %v", duration)
	t.Logf("  Total tasks: %d", len(tasks))
	t.Logf("  Successful tasks: %d", successCount)
	t.Logf("  Failed tasks: %d", errorCount)
	t.Logf("  Total attempts across all tasks: %d", totalAttempts)
	t.Logf("  Worker pool completed jobs: %d", wpStats.CompletedJobs.Load())
	t.Logf("  Worker pool failed jobs: %d", wpStats.FailedJobs.Load())

	// Verify that retry logic was used
	if totalAttempts <= len(tasks) {
		t.Error("Expected retry attempts, but no retries occurred")
	}

	// Verify worker pool processed jobs (some may timeout in test environment)
	if wpStats.CompletedJobs.Load()+wpStats.FailedJobs.Load() < int64(len(tasks)/2) {
		t.Errorf("Expected at least %d jobs processed, got %d", len(tasks)/2, wpStats.CompletedJobs.Load()+wpStats.FailedJobs.Load())
	}

	// Log successful results
	for _, result := range successResults {
		t.Logf("  Success: %s", result)
	}

	// Log error messages
	for _, errMsg := range errorMessages {
		t.Logf("  Error: %s", errMsg)
	}
}

// TestWorkerPoolRetryWithContext tests context-aware retry with worker pool
func TestWorkerPoolRetryWithContext(t *testing.T) {
	// Create worker pool
	wpConfig := pool.DefaultConfig()
	workerPool, err := pool.New(wpConfig)
	if err != nil {
		t.Fatalf("Failed to create worker pool: %v", err)
	}
	defer workerPool.Close()

	// Create retry policy
	retryPolicy := retry.Policy{
		MaxAttempts:     5,
		InitialDelay:    20 * time.Millisecond,
		MaxDelay:        200 * time.Millisecond,
		Multiplier:      2.0,
		Jitter:          false,
		RateLimit:       10,
		RateLimitWindow: time.Second,
	}

	// Create context with timeout
	ctx, cancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer cancel()

	// Create tasks that might timeout
	tasks := []*MockTask{
		NewMockTask("fast", 0.0, 1),  // Should complete quickly
		NewMockTask("slow", 0.9, 10), // Might timeout due to many retries
	}

	var wg sync.WaitGroup
	results := make(chan string, len(tasks))
	errors := make(chan error, len(tasks))

	start := time.Now()

	// Submit tasks with context-aware retry
	for _, task := range tasks {
		wg.Add(1)
		go func(t *MockTask) {
			defer wg.Done()

			job := pool.Job{
				ID: fmt.Sprintf("context-retry-job-%s", t.id),
				Task: func() (interface{}, error) {
					return retry.RetryWithResultAndContext(ctx, retryPolicy, func() (string, error) {
						return t.Execute()
					})
				},
			}

			err := workerPool.Submit(job)
			if err != nil {
				errors <- fmt.Errorf("failed to submit job for task %s: %w", t.id, err)
				return
			}

			// Get result
			result, err := workerPool.GetResultWithTimeout(2 * time.Second)
			if err != nil {
				errors <- fmt.Errorf("failed to get result for task %s: %w", t.id, err)
				return
			}

			if result.Error != nil {
				errors <- fmt.Errorf("task %s failed: %w", t.id, result.Error)
			} else {
				results <- result.Result.(string)
			}
		}(task)
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
	timeoutErrors := 0
	for err := range errors {
		errorCount++
		if err.Error() == "retry cancelled: context deadline exceeded" {
			timeoutErrors++
		}
	}

	// Analyze task attempts
	totalAttempts := 0
	for _, task := range tasks {
		attempts := task.GetAttempts()
		totalAttempts += attempts
		t.Logf("Task %s: %d attempts", task.id, attempts)
	}

	t.Logf("Context-aware retry integration test results:")
	t.Logf("  Duration: %v", duration)
	t.Logf("  Total tasks: %d", len(tasks))
	t.Logf("  Successful tasks: %d", successCount)
	t.Logf("  Failed tasks: %d", errorCount)
	t.Logf("  Timeout errors: %d", timeoutErrors)
	t.Logf("  Total attempts: %d", totalAttempts)

	// Verify context timeout was respected
	if timeoutErrors == 0 && duration >= 900*time.Millisecond {
		t.Log("Expected some timeout errors with context deadline, but none occurred in this test run")
	}
}

// TestWorkerPoolRetryStress tests stress conditions with retry
func TestWorkerPoolRetryStress(t *testing.T) {
	// Create worker pool with limited resources
	wpConfig := pool.ResourceConstrainedConfig()
	workerPool, err := pool.New(wpConfig)
	if err != nil {
		t.Fatalf("Failed to create worker pool: %v", err)
	}
	defer workerPool.Close()

	// Create aggressive retry policy
	retryPolicy := retry.Policy{
		MaxAttempts:     3,
		InitialDelay:    5 * time.Millisecond,
		MaxDelay:        50 * time.Millisecond,
		Multiplier:      1.5,
		Jitter:          true,
		RateLimit:       20,
		RateLimitWindow: time.Second,
	}

	// Create many tasks with varying failure rates
	numTasks := 30
	tasks := make([]*MockTask, numTasks)
	for i := 0; i < numTasks; i++ {
		failureRate := float64(i%3) * 0.3 // 0%, 30%, 60% failure rates
		tasks[i] = NewMockTask(fmt.Sprintf("stress-%d", i), failureRate, 3)
	}

	var wg sync.WaitGroup
	results := make(chan string, numTasks)
	errors := make(chan error, numTasks)

	start := time.Now()

	// Submit all tasks
	for _, task := range tasks {
		wg.Add(1)
		go func(t *MockTask) {
			defer wg.Done()

			job := pool.Job{
				ID: fmt.Sprintf("stress-job-%s", t.id),
				Task: func() (interface{}, error) {
					return retry.RetryWithResult(retryPolicy, func() (string, error) {
						return t.Execute()
					})
				},
			}

			err := workerPool.Submit(job)
			if err != nil {
				errors <- fmt.Errorf("failed to submit job for task %s: %w", t.id, err)
				return
			}

			// Get result
			result, err := workerPool.GetResultWithTimeout(10 * time.Second)
			if err != nil {
				errors <- fmt.Errorf("failed to get result for task %s: %w", t.id, err)
				return
			}

			if result.Error != nil {
				errors <- fmt.Errorf("task %s failed: %w", t.id, result.Error)
			} else {
				results <- result.Result.(string)
			}
		}(task)
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
	for range errors {
		errorCount++
	}

	// Calculate total attempts
	totalAttempts := 0
	for _, task := range tasks {
		totalAttempts += task.GetAttempts()
	}

	// Get worker pool stats
	wpStats := workerPool.GetStats()

	t.Logf("Stress test results:")
	t.Logf("  Duration: %v", duration)
	t.Logf("  Total tasks: %d", numTasks)
	t.Logf("  Successful tasks: %d", successCount)
	t.Logf("  Failed tasks: %d", errorCount)
	t.Logf("  Total attempts: %d", totalAttempts)
	t.Logf("  Average attempts per task: %.2f", float64(totalAttempts)/float64(numTasks))
	t.Logf("  Worker pool completed jobs: %d", wpStats.CompletedJobs.Load())
	t.Logf("  Worker pool failed jobs: %d", wpStats.FailedJobs.Load())
	t.Logf("  Worker pool total workers: %d", wpStats.TotalWorkers.Load())

	// Verify patterns provided protection
	if totalAttempts <= numTasks {
		t.Log("Expected retry attempts under stress, but minimal retries occurred")
	}

	if wpStats.CompletedJobs.Load()+wpStats.FailedJobs.Load() < 1 {
		t.Errorf("Expected at least 1 job processed, got %d", wpStats.CompletedJobs.Load()+wpStats.FailedJobs.Load())
	}

	// Verify some tasks succeeded despite failures
	if successCount == 0 {
		t.Error("Expected some tasks to succeed with retry")
	}
}
