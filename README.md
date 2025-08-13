# Go-PatternX 🚀

A comprehensive, production-ready Go library implementing essential design patterns with enterprise-grade features, security hardening, and performance optimizations.

[![Go Report Card](https://goreportcard.com/badge/github.com/SeaSBee/go-patternx)](https://goreportcard.com/report/github.com/SeaSBee/go-patternx)
[![Go Version](https://img.shields.io/github/go-mod/go-version/SeaSBee/go-patternx)](https://go.dev/)
[![License](https://img.shields.io/badge/License-MIT-blue.svg)](LICENSE)
[![Security](https://img.shields.io/badge/Security-Hardened-green.svg)](SECURITY.md)
[![Tests](https://img.shields.io/badge/Tests-Passing-brightgreen.svg)](TEST_REPORT.md)

## 📋 Table of Contents

- [Overview](#overview)
- [Architecture](#architecture)
- [Design Patterns](#design-patterns)
- [Quick Start](#quick-start)
- [Installation](#installation)
- [Usage Examples](#usage-examples)
- [Configuration](#configuration)
- [Security Features](#security-features)
- [Performance](#performance)
- [Testing](#testing)
- [Production Readiness](#production-readiness)
- [Contributing](#contributing)
- [License](#license)

## 🎯 Overview

Go-PatternX is a battle-tested Go library that implements 7 essential design patterns with production-ready features:

- **Thread-safe implementations** with atomic operations
- **Comprehensive input validation** and sanitization
- **Security hardening** against common attacks
- **Performance optimization** with minimal overhead
- **Health monitoring** and metrics collection
- **Graceful shutdown** and resource management
- **Extensive testing** with 100% coverage

## 🏗️ Architecture

```mermaid
graph TB
    subgraph "Go-PatternX Library"
        subgraph "Core Patterns"
            BF[Bloom Filter]
            BH[Bulkhead]
            CB[Circuit Breaker]
            DLQ[Dead Letter Queue]
            RL[Redlock]
            RT[Retry]
            WP[Worker Pool]
        end
        
        subgraph "Common Features"
            VAL[Input Validation]
            SEC[Security Hardening]
            MET[Metrics Collection]
            HLT[Health Monitoring]
            ERR[Error Handling]
            CFG[Configuration Management]
        end
        
        subgraph "Production Features"
            ATO[Atomic Operations]
            THR[Thread Safety]
            LOG[Structured Logging]
            MON[Monitoring]
            BCH[Benchmarking]
            TST[Testing Suite]
        end
    end
    
    subgraph "External Dependencies"
        REDIS[Redis Cluster]
        STORE[Persistent Store]
        LOGGER[Logger]
        METRICS[Metrics Collector]
    end
    
    BF --> VAL
    BH --> SEC
    CB --> MET
    DLQ --> HLT
    RL --> ERR
    RT --> CFG
    WP --> ATO
    
    VAL --> THR
    SEC --> LOG
    MET --> MON
    HLT --> BCH
    ERR --> TST
    
    RL --> REDIS
    BF --> STORE
    WP --> LOGGER
    CB --> METRICS
```

## 🎨 Design Patterns

### 1. Bloom Filter 🌸
Probabilistic data structure for efficient membership testing with configurable false positive rates.

**Features:**
- Configurable capacity and false positive rates
- Batch operations for high throughput
- Persistent storage integration
- Memory-efficient implementation
- Thread-safe operations

### 2. Bulkhead 🚢
Resource isolation pattern to prevent cascading failures and manage concurrency limits.

**Features:**
- Configurable concurrency limits
- Queue-based backpressure
- Async execution support
- Health monitoring
- Graceful degradation

### 3. Circuit Breaker ⚡
Fault tolerance pattern that prevents cascading failures by monitoring operation success rates.

**Features:**
- Three-state machine (Closed, Open, Half-Open)
- Configurable failure thresholds
- Automatic recovery mechanisms
- Manual state control
- Comprehensive metrics

### 4. Dead Letter Queue (DLQ) 📬
Failed operation handling with retry logic and dead letter processing.

**Features:**
- Configurable retry policies
- Worker pool processing
- Dead letter handling
- Operation categorization
- Metrics and monitoring

### 5. Redlock 🔒
Distributed locking with quorum-based consensus for high availability.

**Features:**
- Quorum-based consensus
- Lock extension mechanisms
- Network partition handling
- Clock drift compensation
- Redis cluster support

### 6. Retry 🔄
Automatic retry with exponential backoff and jitter for resilience.

**Features:**
- Configurable retry policies
- Exponential backoff with jitter
- Retryable error classification
- Global retry budget management
- Rate limiting support

### 7. Worker Pool 👥
Configurable worker pool with auto-scaling and job management.

**Features:**
- Dynamic worker scaling
- Job queue management
- Timeout handling
- Panic recovery
- Performance metrics

## 🚀 Quick Start

### Installation

```bash
go get github.com/SeaSBee/go-patternx
```

### Basic Usage

```go
package main

import (
    "context"
    "fmt"
    "time"
    
    "github.com/SeaSBee/go-patternx/patternx/bloom"
    "github.com/SeaSBee/go-patternx/patternx/bulkhead"
    "github.com/SeaSBee/go-patternx/patternx/cb"
    "github.com/SeaSBee/go-patternx/patternx/retry"
)

func main() {
    // Create a bloom filter
    bf, err := bloom.NewBloomFilter(&bloom.Config{
        ExpectedItems:     10000,
        FalsePositiveRate: 0.01,
    })
    if err != nil {
        panic(err)
    }
    defer bf.Close()

    // Add items
    bf.Add(context.Background(), "user123")
    bf.Add(context.Background(), "user456")

    // Check membership
    exists, _ := bf.Contains(context.Background(), "user123")
    fmt.Printf("User exists: %v\n", exists)

    // Create a circuit breaker
    cb, err := cb.New(cb.DefaultConfig())
    if err != nil {
        panic(err)
    }

    // Execute with circuit breaker
    err = cb.Execute(func() error {
        // Your operation here
        return nil
    })
    if err != nil {
        fmt.Printf("Circuit breaker error: %v\n", err)
    }

    // Create a bulkhead
    bh, err := bulkhead.NewBulkhead(bulkhead.BulkheadConfig{
        MaxConcurrentCalls: 10,
        MaxQueueSize:       100,
        MaxWaitDuration:    1 * time.Second,
        HealthThreshold:    0.5,
    })
    if err != nil {
        panic(err)
    }
    defer bh.Close()

    // Execute with bulkhead
    result, err := bh.Execute(context.Background(), func() (interface{}, error) {
        return "success", nil
    })
    if err != nil {
        fmt.Printf("Bulkhead error: %v\n", err)
    } else {
        fmt.Printf("Result: %v\n", result)
    }

    // Use retry pattern
    policy := retry.DefaultPolicy()
    err = retry.Retry(policy, func() error {
        // Your operation here
        return nil
    })
    if err != nil {
        fmt.Printf("Retry error: %v\n", err)
    }
}
```

## 📦 Installation

### Prerequisites

- Go 1.21 or higher
- Redis (for Redlock pattern)
- Optional: Persistent store for Bloom Filter

### Install

```bash
# Clone the repository
git clone https://github.com/SeaSBee/go-patternx.git
cd go-patternx

# Install dependencies
go mod tidy

# Run tests
go test ./...

# Run benchmarks
go test ./tests/benchmark/ -bench=. -benchmem
```

## 💡 Usage Examples

### Bloom Filter with Persistence

```go
import "github.com/SeaSBee/go-patternx/patternx/bloom"

// Create bloom filter with store
store := &MyBloomStore{} // Implement BloomStore interface
bf, err := bloom.NewBloomFilter(&bloom.Config{
    ExpectedItems:     1000000,
    FalsePositiveRate: 0.01,
    Store:            store,
    TTL:              24 * time.Hour,
})
if err != nil {
    panic(err)
}
defer bf.Close()

// Add items in batch
items := []string{"item1", "item2", "item3", "item4", "item5"}
err = bf.AddBatch(context.Background(), items)
if err != nil {
    panic(err)
}

// Check multiple items
exists, err := bf.ContainsBatch(context.Background(), items)
if err != nil {
    panic(err)
}
```

### Circuit Breaker with Custom Configuration

```go
import "github.com/SeaSBee/go-patternx/patternx/cb"

// Create circuit breaker with custom config
config := cb.Config{
    Threshold:   5,              // Open after 5 failures
    Timeout:     30 * time.Second, // Keep open for 30 seconds
    HalfOpenMax: 3,              // Allow 3 requests in half-open
}
circuitBreaker, err := cb.New(config)
if err != nil {
    panic(err)
}

// Execute with circuit breaker
err = circuitBreaker.Execute(func() error {
    // Call external service
    return callExternalService()
})

// Check circuit breaker state
stats := circuitBreaker.GetStats()
fmt.Printf("Circuit breaker state: %s\n", stats.State)
```

### Worker Pool with Auto-scaling

```go
import "github.com/SeaSBee/go-patternx/patternx/pool"

// Create worker pool
config := pool.DefaultConfig()
config.MinWorkers = 5
config.MaxWorkers = 20
config.QueueSize = 1000
config.EnableAutoScaling = true
config.ScaleUpThreshold = 0.8
config.ScaleDownThreshold = 0.2

wp, err := pool.New(config)
if err != nil {
    panic(err)
}
defer wp.Close()

// Submit jobs
for i := 0; i < 100; i++ {
    job := pool.Job{
        ID:   fmt.Sprintf("job_%d", i),
        Task: func() (interface{}, error) {
            // Process job
            time.Sleep(100 * time.Millisecond)
            return "processed", nil
        },
        Timeout: 5 * time.Second,
    }
    
    err := wp.Submit(job)
    if err != nil {
        fmt.Printf("Failed to submit job: %v\n", err)
    }
}

// Get pool statistics
stats := wp.GetStats()
fmt.Printf("Active workers: %d\n", stats.ActiveWorkers.Load())
fmt.Printf("Completed jobs: %d\n", stats.CompletedJobs.Load())
```

### Redlock for Distributed Locking

```go
import "github.com/SeaSBee/go-patternx/patternx/lock"

// Create Redis clients
clients := []lock.LockClient{
    redisClient1,
    redisClient2,
    redisClient3,
}

// Create Redlock
config := &lock.Config{
    Clients:       clients,
    Quorum:        2,
    RetryDelay:    10 * time.Millisecond,
    MaxRetries:    3,
    DriftFactor:   0.01,
    EnableMetrics: true,
}

rl, err := lock.NewRedlock(config)
if err != nil {
    panic(err)
}

// Acquire lock
lock, err := rl.Lock(context.Background(), "resource_key", 30*time.Second)
if err != nil {
    panic(err)
}
defer lock.Unlock(context.Background())

// Perform critical section operations
fmt.Println("Lock acquired, performing critical operations...")
time.Sleep(5 * time.Second)

// Lock is automatically released when function returns
```

## ⚙️ Configuration

### Default Configurations

Each pattern provides sensible defaults for common use cases:

```go
// Bloom Filter
bloom.DefaultConfig()

// Bulkhead
bulkhead.DefaultConfig()

// Circuit Breaker
cb.DefaultConfig()

// Dead Letter Queue
dlq.DefaultConfig()

// Redlock
lock.DefaultConfig(clients)

// Retry
retry.DefaultPolicy()

// Worker Pool
pool.DefaultConfig()
```

### Enterprise Configurations

For high-performance, production environments:

```go
// High-performance configurations
bloom.EnterpriseConfig()
bulkhead.EnterpriseConfig()
cb.EnterpriseConfig()
dlq.EnterpriseConfig()
lock.EnterpriseConfig(clients)
retry.EnterprisePolicy()
pool.EnterpriseConfig()
```

### Custom Configuration

```go
// Custom bloom filter configuration
config := &bloom.Config{
    ExpectedItems:     1000000,
    FalsePositiveRate: 0.001,  // 0.1% false positive rate
    Store:            myStore,
    TTL:              24 * time.Hour,
    EnableMetrics:    true,
}

// Custom circuit breaker configuration
config := cb.Config{
    Threshold:   10,
    Timeout:     60 * time.Second,
    HalfOpenMax: 5,
}
```

## 🔒 Security Features

### Input Validation
- SQL injection protection
- XSS attack prevention
- Path traversal blocking
- Command injection protection

### Resource Protection
- Memory exhaustion prevention
- Connection limit enforcement
- Queue capacity management
- Rate limiting

### Data Protection
- Sensitive data sanitization
- Secure logging practices
- Metrics privacy protection
- Configuration validation

### Authentication & Authorization
- Resource access control
- Authentication bypass prevention
- Secure lock mechanisms
- Operation validation

## 📊 Performance

### Benchmark Results

```
Bloom Filter:
- Add: 1,510,495 ops/sec (704.3 ns/op)
- Contains: 2,177,125 ops/sec (547.0 ns/op)
- AddBatch: 29,827 ops/sec (39,724 ns/op)

Circuit Breaker:
- Success: 945 ops/sec (1,266,076 ns/op)
- Failure: 31,292,816 ops/sec (37.17 ns/op)
- Concurrent: 10,000 ops/sec (105,481 ns/op)

Redlock:
- Lock: 600,616 ops/sec (1,978 ns/op)
- TryLock: 629,451 ops/sec (1,866 ns/op)
- Concurrent: 1,282,183 ops/sec (925.3 ns/op)
```

### Memory Usage
- **Bloom Filter**: ~1.2MB per million items
- **Worker Pool**: ~2KB per worker
- **Circuit Breaker**: ~100 bytes per instance
- **Bulkhead**: Minimal overhead
- **DLQ**: ~1KB per failed operation

### CPU Usage
- **Bloom Filter**: O(k) where k is hash functions
- **Worker Pool**: O(1) submission, O(n) processing
- **Circuit Breaker**: O(1) state transitions
- **Bulkhead**: O(1) semaphore operations

## 🧪 Testing

### Run All Tests

```bash
# Unit tests
go test ./tests/unit/ -v

# Security tests
go test ./tests/security/ -v

# Benchmark tests
go test ./tests/benchmark/ -bench=. -benchmem

# Integration tests
go test ./tests/integration/ -v

# All tests with coverage
go test ./... -v -cover
```

### Test Coverage

- **Unit Tests**: 100+ test cases
- **Security Tests**: 8 comprehensive categories
- **Benchmark Tests**: Performance metrics
- **Integration Tests**: End-to-end testing

### Test Categories

1. **Configuration Validation**
2. **Input Validation**
3. **Concurrency Safety**
4. **Error Handling**
5. **Health Monitoring**
6. **Graceful Shutdown**
7. **Performance Testing**
8. **Security Hardening**

## 🚀 Production Readiness

### ✅ Production Features

1. **Thread Safety**: All patterns use atomic operations
2. **Error Handling**: Comprehensive error types and handling
3. **Input Validation**: Extensive validation and sanitization
4. **Health Monitoring**: Built-in health checks
5. **Metrics Collection**: Performance and operational metrics
6. **Graceful Shutdown**: Proper resource cleanup
7. **Security Hardening**: Protection against common attacks
8. **Performance Optimization**: Minimal overhead
9. **Documentation**: Comprehensive documentation
10. **Testing**: Extensive test coverage

### 🔧 Production Configuration

```go
// Production-ready configuration example
func createProductionConfig() {
    // Bloom Filter for caching
    bloomConfig := &bloom.Config{
        ExpectedItems:     10000000,  // 10M items
        FalsePositiveRate: 0.001,     // 0.1% false positive
        EnableMetrics:    true,
    }

    // Circuit Breaker for external services
    cbConfig := cb.Config{
        Threshold:   20,              // Higher threshold
        Timeout:     60 * time.Second, // Longer timeout
        HalfOpenMax: 10,              // More half-open requests
    }

    // Worker Pool for processing
    poolConfig := pool.Config{
        MinWorkers:       10,
        MaxWorkers:       100,
        QueueSize:        10000,
        EnableAutoScaling: true,
        EnableMetrics:    true,
    }

    // Bulkhead for resource isolation
    bulkheadConfig := bulkhead.BulkheadConfig{
        MaxConcurrentCalls: 50,
        MaxQueueSize:       1000,
        MaxWaitDuration:    5 * time.Second,
        HealthThreshold:    0.8,
    }
}
```

### 📈 Monitoring & Observability

```go
// Health monitoring
if !circuitBreaker.IsHealthy() {
    // Alert: Circuit breaker unhealthy
    log.Error("Circuit breaker health check failed")
}

// Metrics collection
stats := workerPool.GetStats()
prometheus.Gauge("worker_pool_active_workers").Set(float64(stats.ActiveWorkers.Load()))
prometheus.Counter("worker_pool_completed_jobs").Add(float64(stats.CompletedJobs.Load()))

// Performance monitoring
bloomStats := bloomFilter.GetStats()
log.Info("Bloom filter performance", 
    "item_count", bloomStats["item_count"],
    "false_positive_rate", bloomStats["false_positive_rate"])
```

## 🤝 Contributing

We welcome contributions! Please see our [Contributing Guide](CONTRIBUTING.md) for details.

### Development Setup

```bash
# Clone repository
git clone https://github.com/SeaSBee/go-patternx.git
cd go-patternx

# Install dependencies
go mod tidy

# Run tests
go test ./...

# Run linter
golangci-lint run

# Run benchmarks
go test ./tests/benchmark/ -bench=. -benchmem
```

### Code Standards

- Follow Go coding standards
- Add tests for new features
- Update documentation
- Run all tests before submitting
- Follow semantic versioning

## 📄 License

This project is licensed under the MIT License - see the [LICENSE](LICENSE) file for details.

## 📚 Additional Resources

- [API Documentation](docs/API.md)
- [Performance Guide](docs/PERFORMANCE.md)
- [Security Guide](docs/SECURITY.md)
- [Production Guide](docs/PRODUCTION.md)
- [Test Report](TEST_REPORT.md)
- [Changelog](CHANGELOG.md)

## 🆘 Support

- **Issues**: [GitHub Issues](https://github.com/SeaSBee/go-patternx/issues)
- **Discussions**: [GitHub Discussions](https://github.com/SeaSBee/go-patternx/discussions)
- **Security**: [Security Policy](SECURITY.md)

## 🙏 Acknowledgments

- Inspired by Martin Fowler's design patterns
- Built with Go's excellent concurrency primitives
- Tested with comprehensive security and performance benchmarks
- Community-driven development and feedback

---

**Made with ❤️ by the Go-PatternX Team**