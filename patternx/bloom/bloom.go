package bloom

import (
	"context"
	"crypto/md5"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"math"
	"sync"
	"sync/atomic"
	"time"

	"github.com/seasbee/go-logx"
)

// Common errors for Bloom Filter operations
var (
	ErrInvalidConfig         = errors.New("invalid bloom filter configuration")
	ErrEmptyItem             = errors.New("item cannot be empty")
	ErrItemTooLong           = errors.New("item exceeds maximum length")
	ErrCapacityExceeded      = errors.New("bloom filter capacity exceeded")
	ErrStoreUnavailable      = errors.New("store is not available")
	ErrContextCancelled      = errors.New("operation cancelled by context")
	ErrInvalidFalsePositive  = errors.New("false positive rate must be between 0 and 1")
	ErrInvalidExpectedItems  = errors.New("expected items must be greater than 0")
	ErrInvalidSize           = errors.New("bloom filter size is invalid")
	ErrInvalidHashCount      = errors.New("hash count is invalid")
	ErrStoreOperationFailed  = errors.New("store operation failed")
	ErrSerializationFailed   = errors.New("failed to serialize bloom filter state")
	ErrDeserializationFailed = errors.New("failed to deserialize bloom filter state")
	ErrFilterClosed          = errors.New("bloom filter is closed")
)

// Constants for production constraints
const (
	MaxItemLength    = 1024 * 1024   // 1MB max item length
	MaxExpectedItems = 1_000_000_000 // 1 billion max expected items
	MinExpectedItems = 1             // Minimum expected items
	MaxFalsePositive = 0.5           // 50% max false positive rate
	MinFalsePositive = 1e-6          // 0.0001% min false positive rate
	MaxHashCount     = 50            // Maximum number of hash functions
	MinHashCount     = 1             // Minimum number of hash functions
	MaxBitsetSize    = 1e10          // 10 billion max bitset size
	DefaultTTL       = 24 * time.Hour
	DefaultKeyPrefix = "bloom"
	MaxRetryAttempts = 3
	RetryDelay       = 100 * time.Millisecond
)

// BloomFilter implements a production-ready probabilistic data structure for membership testing
type BloomFilter struct {
	mu                sync.RWMutex
	storeMu           sync.Mutex // Dedicated mutex for store operations
	bitset            []bool
	size              uint64
	hashCount         int
	itemCount         uint64
	maxItems          uint64
	falsePositiveRate float64
	store             BloomStore
	keyPrefix         string
	ttl               time.Duration
	closed            int32 // Atomic flag for closed state
	metrics           *Metrics
}

// Metrics tracks Bloom filter performance and usage statistics with atomic operations
type Metrics struct {
	AddOperations           atomic.Uint64
	ContainsOperations      atomic.Uint64
	AddBatchOperations      atomic.Uint64
	ContainsBatchOperations atomic.Uint64
	FalsePositives          atomic.Uint64
	TrueNegatives           atomic.Uint64
	StoreOperations         atomic.Uint64
	StoreErrors             atomic.Uint64
	mu                      sync.RWMutex // For complex metrics that can't be atomic
	LastAddTime             time.Time
	LastContainsTime        time.Time
	AverageAddLatency       time.Duration
	AverageContainsLatency  time.Duration
}

// BloomStore defines the interface for persisting bloom filter state
type BloomStore interface {
	Set(ctx context.Context, key string, value []byte, ttl time.Duration) error
	Get(ctx context.Context, key string) ([]byte, error)
	Del(ctx context.Context, key string) error
	Exists(ctx context.Context, key string) (bool, error)
}

// Config holds Bloom filter configuration with validation
type Config struct {
	ExpectedItems     uint64        // Expected number of items
	FalsePositiveRate float64       // Desired false positive rate (0.01 = 1%)
	Store             BloomStore    // Optional store for persistence
	KeyPrefix         string        // Key prefix for store operations
	TTL               time.Duration // TTL for stored data
	EnableMetrics     bool          // Enable performance metrics
}

// BloomFilterState represents the serializable state of a Bloom filter
type BloomFilterState struct {
	ItemCount uint64    `json:"item_count"`
	Size      uint64    `json:"size"`
	HashCount int       `json:"hash_count"`
	Bitset    []bool    `json:"bitset"`
	Version   string    `json:"version"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// NewBloomFilter creates a new production-ready Bloom filter with comprehensive validation
func NewBloomFilter(config *Config) (*BloomFilter, error) {
	// Validate configuration
	if err := validateConfig(config); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidConfig, err)
	}

	// Apply defaults
	applyDefaults(config)

	// Calculate optimal size and hash count with validation
	size, hashCount, err := calculateOptimalParameters(config.ExpectedItems, config.FalsePositiveRate)
	if err != nil {
		return nil, fmt.Errorf("failed to calculate optimal parameters: %w", err)
	}

	// Create Bloom filter
	bf := &BloomFilter{
		bitset:            make([]bool, size),
		size:              size,
		hashCount:         hashCount,
		maxItems:          config.ExpectedItems,
		falsePositiveRate: config.FalsePositiveRate,
		store:             config.Store,
		keyPrefix:         config.KeyPrefix,
		ttl:               config.TTL,
		metrics:           &Metrics{},
	}

	// Load existing state if store is provided
	if config.Store != nil {
		if err := bf.loadFromStoreWithRetry(context.Background()); err != nil {
			logx.Warn("Failed to load bloom filter from store, starting fresh",
				logx.ErrorField(err),
				logx.String("key_prefix", config.KeyPrefix))
		}
	}

	logx.Info("Bloom filter created successfully",
		logx.String("size", fmt.Sprintf("%d", size)),
		logx.Int("hash_count", hashCount),
		logx.String("expected_items", fmt.Sprintf("%d", config.ExpectedItems)),
		logx.Float64("false_positive_rate", config.FalsePositiveRate),
		logx.String("key_prefix", config.KeyPrefix))

	return bf, nil
}

// Add adds an item to the Bloom filter with comprehensive validation and improved context handling
func (bf *BloomFilter) Add(ctx context.Context, item string) error {
	// Check if filter is closed
	if atomic.LoadInt32(&bf.closed) == 1 {
		return ErrFilterClosed
	}

	// Validate input
	if err := validateItem(item); err != nil {
		return fmt.Errorf("invalid item: %w", err)
	}

	// Check context cancellation before starting operation
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrContextCancelled, err)
	}

	start := time.Now()
	defer func() {
		bf.updateAddMetrics(time.Since(start))
	}()

	// Use a timeout context to prevent indefinite blocking
	opCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	bf.mu.Lock()
	defer bf.mu.Unlock()

	// Check context cancellation after acquiring lock with timeout context
	if err := opCtx.Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrContextCancelled, err)
	}

	// Check capacity
	if bf.itemCount >= bf.maxItems {
		logx.Warn("Bloom filter at capacity, item not added",
			logx.String("item_count", fmt.Sprintf("%d", bf.itemCount)),
			logx.String("max_items", fmt.Sprintf("%d", bf.maxItems)),
			logx.String("item", truncateString(item, 100)))
		return fmt.Errorf("%w: current=%d, max=%d", ErrCapacityExceeded, bf.itemCount, bf.maxItems)
	}

	// Get hash positions
	positions := bf.getHashPositions(item)

	// Set bits
	for _, pos := range positions {
		bf.bitset[pos] = true
	}

	bf.itemCount++
	bf.metrics.AddOperations.Add(1)

	// Persist to store if available with dedicated store mutex
	if bf.store != nil {
		if err := bf.saveToStoreWithRetry(opCtx); err != nil {
			logx.Error("Failed to persist bloom filter state",
				logx.ErrorField(err),
				logx.String("item", truncateString(item, 100)))
			bf.metrics.StoreErrors.Add(1)
			// Don't fail the operation if persistence fails
		}
	}

	return nil
}

// Contains checks if an item might be in the Bloom filter with validation and improved context handling
func (bf *BloomFilter) Contains(ctx context.Context, item string) (bool, error) {
	// Check if filter is closed
	if atomic.LoadInt32(&bf.closed) == 1 {
		return false, ErrFilterClosed
	}

	// Validate input
	if err := validateItem(item); err != nil {
		return false, fmt.Errorf("invalid item: %w", err)
	}

	// Check context cancellation before starting operation
	if err := ctx.Err(); err != nil {
		return false, fmt.Errorf("%w: %v", ErrContextCancelled, err)
	}

	start := time.Now()
	defer func() {
		bf.updateContainsMetrics(time.Since(start))
	}()

	// Use a timeout context to prevent indefinite blocking
	opCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	bf.mu.RLock()
	defer bf.mu.RUnlock()

	// Check context cancellation after acquiring lock with timeout context
	if err := opCtx.Err(); err != nil {
		return false, fmt.Errorf("%w: %v", ErrContextCancelled, err)
	}

	// Get hash positions
	positions := bf.getHashPositions(item)

	// Check if all bits are set
	for _, pos := range positions {
		if !bf.bitset[pos] {
			bf.metrics.TrueNegatives.Add(1)
			return false, nil
		}
	}

	bf.metrics.ContainsOperations.Add(1)
	// Note: We can't distinguish between true positives and false positives
	// without external verification, so we don't update false positive metrics here
	return true, nil
}

// AddBatch adds multiple items to the Bloom filter with validation and improved context handling
func (bf *BloomFilter) AddBatch(ctx context.Context, items []string) error {
	// Check if filter is closed
	if atomic.LoadInt32(&bf.closed) == 1 {
		return ErrFilterClosed
	}

	// Validate input
	if err := validateBatch(items); err != nil {
		return fmt.Errorf("invalid batch: %w", err)
	}

	// Check context cancellation before starting operation
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrContextCancelled, err)
	}

	start := time.Now()
	defer func() {
		bf.updateAddBatchMetrics(time.Since(start), len(items))
	}()

	// Use a timeout context to prevent indefinite blocking
	opCtx, cancel := context.WithTimeout(ctx, 60*time.Second) // Longer timeout for batch operations
	defer cancel()

	bf.mu.Lock()
	defer bf.mu.Unlock()

	// Check context cancellation after acquiring lock with timeout context
	if err := opCtx.Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrContextCancelled, err)
	}

	addedCount := 0
	for i, item := range items {
		// Check context cancellation periodically during batch processing with timeout context
		if i%100 == 0 && opCtx.Err() != nil {
			return fmt.Errorf("%w: %v", ErrContextCancelled, opCtx.Err())
		}

		// Check capacity
		if bf.itemCount >= bf.maxItems {
			logx.Warn("Bloom filter at capacity during batch add",
				logx.String("item_count", fmt.Sprintf("%d", bf.itemCount)),
				logx.String("max_items", fmt.Sprintf("%d", bf.maxItems)),
				logx.Int("items_processed", addedCount),
				logx.Int("total_items", len(items)))
			break
		}

		// Get hash positions
		positions := bf.getHashPositions(item)

		// Set bits
		for _, pos := range positions {
			bf.bitset[pos] = true
		}

		bf.itemCount++
		addedCount++
	}

	// Update metrics accurately for batch operations with atomic operations
	bf.metrics.AddBatchOperations.Add(1)
	bf.metrics.AddOperations.Add(uint64(addedCount)) // Count individual adds

	// Persist to store if available with dedicated store mutex
	if bf.store != nil && addedCount > 0 {
		if err := bf.saveToStoreWithRetry(opCtx); err != nil {
			logx.Error("Failed to persist bloom filter state after batch add",
				logx.ErrorField(err),
				logx.Int("items_added", addedCount))
			bf.metrics.StoreErrors.Add(1)
			// Don't fail the operation if persistence fails
		}
	}

	return nil
}

// ContainsBatch checks if multiple items might be in the Bloom filter with improved context handling
func (bf *BloomFilter) ContainsBatch(ctx context.Context, items []string) (map[string]bool, error) {
	// Check if filter is closed
	if atomic.LoadInt32(&bf.closed) == 1 {
		return nil, ErrFilterClosed
	}

	// Validate input
	if err := validateBatch(items); err != nil {
		return nil, fmt.Errorf("invalid batch: %w", err)
	}

	// Check context cancellation before starting operation
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrContextCancelled, err)
	}

	start := time.Now()
	defer func() {
		bf.updateContainsBatchMetrics(time.Since(start), len(items))
	}()

	// Use a timeout context to prevent indefinite blocking
	opCtx, cancel := context.WithTimeout(ctx, 60*time.Second) // Longer timeout for batch operations
	defer cancel()

	bf.mu.RLock()
	defer bf.mu.RUnlock()

	// Check context cancellation after acquiring lock with timeout context
	if err := opCtx.Err(); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrContextCancelled, err)
	}

	results := make(map[string]bool, len(items))

	for i, item := range items {
		// Check context cancellation periodically during batch processing with timeout context
		if i%100 == 0 && opCtx.Err() != nil {
			return nil, fmt.Errorf("%w: %v", ErrContextCancelled, opCtx.Err())
		}

		// Get hash positions
		positions := bf.getHashPositions(item)

		// Check if all bits are set
		contains := true
		for _, pos := range positions {
			if !bf.bitset[pos] {
				contains = false
				break
			}
		}

		results[item] = contains
	}

	// Update metrics accurately for batch operations with atomic operations
	bf.metrics.ContainsBatchOperations.Add(1)
	bf.metrics.ContainsOperations.Add(uint64(len(items))) // Count individual contains

	return results, nil
}

// Clear clears the Bloom filter
func (bf *BloomFilter) Clear(ctx context.Context) error {
	// Check if filter is closed
	if atomic.LoadInt32(&bf.closed) == 1 {
		return errors.New("bloom filter is closed")
	}

	// Check context cancellation
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrContextCancelled, err)
	}

	bf.mu.Lock()
	defer bf.mu.Unlock()

	// Clear bitset
	for i := range bf.bitset {
		bf.bitset[i] = false
	}

	bf.itemCount = 0

	// Clear from store if available
	if bf.store != nil {
		if err := bf.store.Del(ctx, bf.getStoreKey()); err != nil {
			logx.Error("Failed to clear bloom filter from store", logx.ErrorField(err))
			bf.metrics.StoreErrors.Add(1)
			return fmt.Errorf("%w: %v", ErrStoreOperationFailed, err)
		}
	}

	logx.Info("Bloom filter cleared successfully")
	return nil
}

// GetStats returns comprehensive Bloom filter statistics with improved thread safety
func (bf *BloomFilter) GetStats() map[string]interface{} {
	bf.mu.RLock()
	defer bf.mu.RUnlock()

	// Calculate current false positive rate
	currentFPR := bf.calculateCurrentFalsePositiveRate()
	bitsSet := bf.countSetBits()

	stats := map[string]interface{}{
		"size":                        bf.size,
		"hash_count":                  bf.hashCount,
		"item_count":                  bf.itemCount,
		"max_items":                   bf.maxItems,
		"desired_false_positive_rate": bf.falsePositiveRate,
		"current_false_positive_rate": currentFPR,
		"load_factor":                 float64(bf.itemCount) / float64(bf.maxItems),
		"bits_set":                    bitsSet,
		"bits_set_percentage":         float64(bitsSet) / float64(bf.size) * 100,
		"is_at_capacity":              bf.itemCount >= bf.maxItems,
		"available_capacity":          bf.maxItems - bf.itemCount,
	}

	// Add metrics with atomic operations for thread safety
	if bf.metrics != nil {
		stats["add_operations"] = bf.metrics.AddOperations.Load()
		stats["contains_operations"] = bf.metrics.ContainsOperations.Load()
		stats["add_batch_operations"] = bf.metrics.AddBatchOperations.Load()
		stats["contains_batch_operations"] = bf.metrics.ContainsBatchOperations.Load()
		stats["false_positives"] = bf.metrics.FalsePositives.Load()
		stats["true_negatives"] = bf.metrics.TrueNegatives.Load()
		stats["store_operations"] = bf.metrics.StoreOperations.Load()
		stats["store_errors"] = bf.metrics.StoreErrors.Load()

		// Use mutex only for complex metrics that can't be atomic
		bf.metrics.mu.RLock()
		defer bf.metrics.mu.RUnlock()
		stats["average_add_latency_ns"] = bf.metrics.AverageAddLatency.Nanoseconds()
		stats["average_contains_latency_ns"] = bf.metrics.AverageContainsLatency.Nanoseconds()
	}

	return stats
}

// Close closes the Bloom filter and releases resources
func (bf *BloomFilter) Close() error {
	if !atomic.CompareAndSwapInt32(&bf.closed, 0, 1) {
		return errors.New("bloom filter already closed")
	}

	logx.Info("Bloom filter closed successfully")
	return nil
}

// IsClosed returns true if the Bloom filter is closed
func (bf *BloomFilter) IsClosed() bool {
	return atomic.LoadInt32(&bf.closed) == 1
}

// getHashPositions calculates hash positions for an item using multiple hash functions
func (bf *BloomFilter) getHashPositions(item string) []uint64 {
	positions := make([]uint64, bf.hashCount)

	// Use multiple hash functions for better distribution
	h1 := fnv.New64a()
	h1.Write([]byte(item))
	hash1 := h1.Sum64()

	h2 := md5.New()
	h2.Write([]byte(item))
	hash2 := binary.BigEndian.Uint64(h2.Sum(nil))

	h3 := sha256.New()
	h3.Write([]byte(item))
	hash3 := binary.BigEndian.Uint64(h3.Sum(nil))

	for i := 0; i < bf.hashCount; i++ {
		// Combine hashes using triple hashing for better distribution
		hash := hash1 + uint64(i)*hash2 + uint64(i*i)*hash3
		positions[i] = hash % bf.size
	}

	return positions
}

// countSetBits counts the number of set bits in the bitset efficiently
func (bf *BloomFilter) countSetBits() uint64 {
	count := uint64(0)
	for _, bit := range bf.bitset {
		if bit {
			count++
		}
	}
	return count
}

// calculateCurrentFalsePositiveRate calculates the current false positive rate
func (bf *BloomFilter) calculateCurrentFalsePositiveRate() float64 {
	if bf.itemCount == 0 {
		return 0.0
	}

	// Calculate probability of a bit being set
	p := float64(bf.countSetBits()) / float64(bf.size)

	// False positive rate = p^hashCount
	return math.Pow(p, float64(bf.hashCount))
}

// getStoreKey returns the key for storing bloom filter state
func (bf *BloomFilter) getStoreKey() string {
	return fmt.Sprintf("%s:filter", bf.keyPrefix)
}

// saveToStoreWithRetry saves the bloom filter state to the store with retry logic and store synchronization
func (bf *BloomFilter) saveToStoreWithRetry(ctx context.Context) error {
	if bf.store == nil {
		return ErrStoreUnavailable
	}

	var lastErr error
	for attempt := 0; attempt < MaxRetryAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%w: %v", ErrContextCancelled, err)
		}

		// Use dedicated store mutex to prevent concurrent store operations
		// but release it between retries to prevent deadlocks
		bf.storeMu.Lock()
		err := bf.saveToStore(ctx)
		bf.storeMu.Unlock()

		if err != nil {
			lastErr = err
			if attempt < MaxRetryAttempts-1 {
				// Sleep outside of the mutex to prevent deadlocks
				select {
				case <-ctx.Done():
					return fmt.Errorf("%w: %v", ErrContextCancelled, ctx.Err())
				case <-time.After(RetryDelay * time.Duration(attempt+1)):
					continue
				}
			}
		} else {
			bf.metrics.StoreOperations.Add(1)
			return nil
		}
	}

	return fmt.Errorf("%w: %v", ErrStoreOperationFailed, lastErr)
}

// saveToStore saves the bloom filter state to the store with enhanced serialization
func (bf *BloomFilter) saveToStore(ctx context.Context) error {
	// Check context cancellation before starting store operation
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrContextCancelled, err)
	}

	state := &BloomFilterState{
		ItemCount: bf.itemCount,
		Size:      bf.size,
		HashCount: bf.hashCount,
		Bitset:    make([]bool, len(bf.bitset)),
		Version:   "1.1", // Enhanced version for production
		CreatedAt: time.Now(),
		UpdatedAt: time.Now(),
	}

	// Copy bitset
	copy(state.Bitset, bf.bitset)

	// Enhanced serialization with compression and validation
	data, err := bf.serializeState(state)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrSerializationFailed, err)
	}

	// Check context cancellation before store operation
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrContextCancelled, err)
	}

	return bf.store.Set(ctx, bf.getStoreKey(), data, bf.ttl)
}

// serializeState provides enhanced serialization for production
func (bf *BloomFilter) serializeState(state *BloomFilterState) ([]byte, error) {
	// Create enhanced serialization structure
	enhancedState := map[string]interface{}{
		"version":    state.Version,
		"item_count": state.ItemCount,
		"size":       state.Size,
		"hash_count": state.HashCount,
		"created_at": state.CreatedAt.Format(time.RFC3339),
		"updated_at": state.UpdatedAt.Format(time.RFC3339),
		"metadata": map[string]interface{}{
			"false_positive_rate": bf.falsePositiveRate,
			"max_items":           bf.maxItems,
			"key_prefix":          bf.keyPrefix,
		},
	}

	// Convert bitset to compact binary format for efficiency
	bitsetBytes := bf.bitsetToBytes(state.Bitset)
	enhancedState["bitset"] = bitsetBytes

	// Serialize to JSON with proper error handling
	data, err := json.Marshal(enhancedState)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal enhanced state: %w", err)
	}

	return data, nil
}

// bitsetToBytes converts bitset to compact binary format
func (bf *BloomFilter) bitsetToBytes(bitset []bool) []byte {
	// Calculate required bytes (8 bits per byte)
	byteCount := (len(bitset) + 7) / 8
	bytes := make([]byte, byteCount)

	for i, bit := range bitset {
		if bit {
			byteIndex := i / 8
			bitIndex := i % 8
			bytes[byteIndex] |= 1 << bitIndex
		}
	}

	return bytes
}

// loadFromStoreWithRetry loads the bloom filter state from the store with retry logic and store synchronization
func (bf *BloomFilter) loadFromStoreWithRetry(ctx context.Context) error {
	if bf.store == nil {
		return ErrStoreUnavailable
	}

	var lastErr error
	for attempt := 0; attempt < MaxRetryAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("%w: %v", ErrContextCancelled, err)
		}

		// Use dedicated store mutex to prevent concurrent store operations
		// but release it between retries to prevent deadlocks
		bf.storeMu.Lock()
		err := bf.loadFromStore(ctx)
		bf.storeMu.Unlock()

		if err != nil {
			lastErr = err
			if attempt < MaxRetryAttempts-1 {
				// Sleep outside of the mutex to prevent deadlocks
				select {
				case <-ctx.Done():
					return fmt.Errorf("%w: %v", ErrContextCancelled, ctx.Err())
				case <-time.After(RetryDelay * time.Duration(attempt+1)):
					continue
				}
			}
		} else {
			bf.metrics.StoreOperations.Add(1)
			return nil
		}
	}

	return fmt.Errorf("%w: %v", ErrStoreOperationFailed, lastErr)
}

// loadFromStore loads the bloom filter state from the store with enhanced deserialization
func (bf *BloomFilter) loadFromStore(ctx context.Context) error {
	// Check context cancellation before starting store operation
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrContextCancelled, err)
	}

	data, err := bf.store.Get(ctx, bf.getStoreKey())
	if err != nil {
		return err
	}

	// Check context cancellation after store operation
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("%w: %v", ErrContextCancelled, err)
	}

	state, err := bf.deserializeState(data)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrDeserializationFailed, err)
	}

	// Validate loaded state
	if err := validateBloomFilterState(state, bf.size, bf.hashCount); err != nil {
		return fmt.Errorf("invalid loaded state: %w", err)
	}

	// Apply loaded state
	bf.mu.Lock()
	defer bf.mu.Unlock()

	bf.itemCount = state.ItemCount
	copy(bf.bitset, state.Bitset)

	logx.Info("Bloom filter state loaded from store",
		logx.String("item_count", fmt.Sprintf("%d", state.ItemCount)),
		logx.String("version", state.Version),
		logx.String("updated_at", state.UpdatedAt.Format(time.RFC3339)))

	return nil
}

// deserializeState provides enhanced deserialization for production
func (bf *BloomFilter) deserializeState(data []byte) (*BloomFilterState, error) {
	// Try enhanced format first
	var enhancedState map[string]interface{}
	if err := json.Unmarshal(data, &enhancedState); err != nil {
		// Fallback to legacy format
		return bf.deserializeLegacyState(data)
	}

	// Extract version and validate
	version, ok := enhancedState["version"].(string)
	if !ok {
		return nil, errors.New("invalid version in enhanced state")
	}

	// Extract basic fields
	itemCount, ok := enhancedState["item_count"].(float64)
	if !ok {
		return nil, errors.New("invalid item_count in enhanced state")
	}

	size, ok := enhancedState["size"].(float64)
	if !ok {
		return nil, errors.New("invalid size in enhanced state")
	}

	hashCount, ok := enhancedState["hash_count"].(float64)
	if !ok {
		return nil, errors.New("invalid hash_count in enhanced state")
	}

	// Extract bitset
	bitsetBytes, ok := enhancedState["bitset"].([]interface{})
	if !ok {
		return nil, errors.New("invalid bitset in enhanced state")
	}

	// Convert bitset bytes back to bool slice
	bitset := bf.bytesToBitset(bitsetBytes, int(size))

	// Parse timestamps
	createdAtStr, ok := enhancedState["created_at"].(string)
	if !ok {
		createdAtStr = time.Now().Format(time.RFC3339)
	}

	updatedAtStr, ok := enhancedState["updated_at"].(string)
	if !ok {
		updatedAtStr = time.Now().Format(time.RFC3339)
	}

	createdAt, _ := time.Parse(time.RFC3339, createdAtStr)
	updatedAt, _ := time.Parse(time.RFC3339, updatedAtStr)

	state := &BloomFilterState{
		ItemCount: uint64(itemCount),
		Size:      uint64(size),
		HashCount: int(hashCount),
		Bitset:    bitset,
		Version:   version,
		CreatedAt: createdAt,
		UpdatedAt: updatedAt,
	}

	return state, nil
}

// deserializeLegacyState handles legacy serialization format
func (bf *BloomFilter) deserializeLegacyState(data []byte) (*BloomFilterState, error) {
	var state BloomFilterState
	if err := json.Unmarshal(data, &state); err != nil {
		return nil, fmt.Errorf("failed to unmarshal legacy state: %w", err)
	}

	// Set default values for missing fields
	if state.Version == "" {
		state.Version = "1.0"
	}
	if state.CreatedAt.IsZero() {
		state.CreatedAt = time.Now()
	}
	if state.UpdatedAt.IsZero() {
		state.UpdatedAt = time.Now()
	}

	return &state, nil
}

// bytesToBitset converts compact binary format back to bool slice
func (bf *BloomFilter) bytesToBitset(bytes []interface{}, size int) []bool {
	bitset := make([]bool, size)

	for i := 0; i < size; i++ {
		byteIndex := i / 8
		bitIndex := i % 8

		if byteIndex < len(bytes) {
			if byteVal, ok := bytes[byteIndex].(float64); ok {
				bitset[i] = (uint8(byteVal) & (1 << bitIndex)) != 0
			}
		}
	}

	return bitset
}

// updateAddMetrics updates add operation metrics with improved thread safety
func (bf *BloomFilter) updateAddMetrics(duration time.Duration) {
	if bf.metrics == nil {
		return
	}

	// Use mutex only for complex metrics that can't be atomic
	bf.metrics.mu.Lock()
	defer bf.metrics.mu.Unlock()

	bf.metrics.LastAddTime = time.Now()
	if bf.metrics.AverageAddLatency == 0 {
		bf.metrics.AverageAddLatency = duration
	} else {
		bf.metrics.AverageAddLatency = (bf.metrics.AverageAddLatency + duration) / 2
	}
}

// updateContainsMetrics updates contains operation metrics with improved thread safety
func (bf *BloomFilter) updateContainsMetrics(duration time.Duration) {
	if bf.metrics == nil {
		return
	}

	// Use mutex only for complex metrics that can't be atomic
	bf.metrics.mu.Lock()
	defer bf.metrics.mu.Unlock()

	bf.metrics.LastContainsTime = time.Now()
	if bf.metrics.AverageContainsLatency == 0 {
		bf.metrics.AverageContainsLatency = duration
	} else {
		bf.metrics.AverageContainsLatency = (bf.metrics.AverageContainsLatency + duration) / 2
	}
}

// updateAddBatchMetrics updates batch add operation metrics with accurate counting and improved thread safety
func (bf *BloomFilter) updateAddBatchMetrics(duration time.Duration, itemCount int) {
	if bf.metrics == nil {
		return
	}

	// Use mutex only for complex metrics that can't be atomic
	bf.metrics.mu.Lock()
	defer bf.metrics.mu.Unlock()

	bf.metrics.LastAddTime = time.Now()

	// Calculate average latency per item for batch operations
	if itemCount > 0 {
		avgPerItem := duration / time.Duration(itemCount)
		if bf.metrics.AverageAddLatency == 0 {
			bf.metrics.AverageAddLatency = avgPerItem
		} else {
			// Weighted average to account for batch operations
			bf.metrics.AverageAddLatency = (bf.metrics.AverageAddLatency + avgPerItem) / 2
		}
	}
}

// updateContainsBatchMetrics updates batch contains operation metrics with accurate counting and improved thread safety
func (bf *BloomFilter) updateContainsBatchMetrics(duration time.Duration, itemCount int) {
	if bf.metrics == nil {
		return
	}

	// Use mutex only for complex metrics that can't be atomic
	bf.metrics.mu.Lock()
	defer bf.metrics.mu.Unlock()

	bf.metrics.LastContainsTime = time.Now()

	// Calculate average latency per item for batch operations
	if itemCount > 0 {
		avgPerItem := duration / time.Duration(itemCount)
		if bf.metrics.AverageContainsLatency == 0 {
			bf.metrics.AverageContainsLatency = avgPerItem
		} else {
			// Weighted average to account for batch operations
			bf.metrics.AverageContainsLatency = (bf.metrics.AverageContainsLatency + avgPerItem) / 2
		}
	}
}

// validateConfig validates Bloom filter configuration
func validateConfig(config *Config) error {
	if config == nil {
		return errors.New("config cannot be nil")
	}

	if config.ExpectedItems < MinExpectedItems || config.ExpectedItems > MaxExpectedItems {
		return fmt.Errorf("%w: expected items must be between %d and %d, got %d",
			ErrInvalidExpectedItems, MinExpectedItems, MaxExpectedItems, config.ExpectedItems)
	}

	if config.FalsePositiveRate < MinFalsePositive || config.FalsePositiveRate > MaxFalsePositive {
		return fmt.Errorf("%w: false positive rate must be between %f and %f, got %f",
			ErrInvalidFalsePositive, MinFalsePositive, MaxFalsePositive, config.FalsePositiveRate)
	}

	return nil
}

// validateItem validates a single item
func validateItem(item string) error {
	if item == "" {
		return ErrEmptyItem
	}

	if len(item) > MaxItemLength {
		return fmt.Errorf("%w: item length %d exceeds maximum %d",
			ErrItemTooLong, len(item), MaxItemLength)
	}

	return nil
}

// validateBatch validates a slice of items
func validateBatch(items []string) error {
	if len(items) == 0 {
		return errors.New("items slice cannot be empty")
	}

	for i, item := range items {
		if err := validateItem(item); err != nil {
			return fmt.Errorf("invalid item at index %d: %w", i, err)
		}
	}
	return nil
}

// validateBloomFilterState validates the loaded Bloom filter state
func validateBloomFilterState(state *BloomFilterState, expectedSize uint64, expectedHashCount int) error {
	if state == nil {
		return errors.New("state cannot be nil")
	}

	if state.Size != expectedSize {
		return fmt.Errorf("loaded size %d does not match expected %d", state.Size, expectedSize)
	}

	if state.HashCount != expectedHashCount {
		return fmt.Errorf("loaded hash count %d does not match expected %d", state.HashCount, expectedHashCount)
	}

	if len(state.Bitset) != int(expectedSize) {
		return fmt.Errorf("loaded bitset length %d does not match expected size %d", len(state.Bitset), expectedSize)
	}

	return nil
}

// applyDefaults applies default values to configuration
func applyDefaults(config *Config) {
	if config.KeyPrefix == "" {
		config.KeyPrefix = DefaultKeyPrefix
	}

	if config.TTL == 0 {
		config.TTL = DefaultTTL
	}
}

// calculateOptimalParameters calculates optimal size and hash count with validation
func calculateOptimalParameters(n uint64, p float64) (size uint64, hashCount int, err error) {
	// Calculate optimal size
	size = calculateOptimalSize(n, p)
	if size == 0 || size > MaxBitsetSize {
		return 0, 0, fmt.Errorf("%w: calculated size %d is invalid", ErrInvalidSize, size)
	}

	// Calculate optimal hash count
	hashCount = calculateOptimalHashCount(size, n)
	if hashCount < MinHashCount || hashCount > MaxHashCount {
		return 0, 0, fmt.Errorf("%w: calculated hash count %d is invalid", ErrInvalidHashCount, hashCount)
	}

	return size, hashCount, nil
}

// calculateOptimalSize calculates the optimal size for the bitset
func calculateOptimalSize(n uint64, p float64) uint64 {
	m := float64(n) * math.Log(p) / (math.Log(2) * math.Log(2))
	return uint64(math.Ceil(-m))
}

// calculateOptimalHashCount calculates the optimal number of hash functions
func calculateOptimalHashCount(m, n uint64) int {
	k := float64(m) / float64(n) * math.Log(2)
	return int(math.Ceil(k))
}

// truncateString truncates a string for logging purposes
func truncateString(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

// DefaultConfig returns a default Bloom filter configuration
func DefaultConfig(store BloomStore) *Config {
	return &Config{
		ExpectedItems:     10000,
		FalsePositiveRate: 0.01, // 1%
		Store:             store,
		KeyPrefix:         DefaultKeyPrefix,
		TTL:               DefaultTTL,
		EnableMetrics:     true,
	}
}

// ConservativeConfig returns a conservative Bloom filter configuration
func ConservativeConfig(store BloomStore) *Config {
	return &Config{
		ExpectedItems:     50000,
		FalsePositiveRate: 0.001, // 0.1%
		Store:             store,
		KeyPrefix:         DefaultKeyPrefix,
		TTL:               DefaultTTL,
		EnableMetrics:     true,
	}
}

// AggressiveConfig returns an aggressive Bloom filter configuration
func AggressiveConfig(store BloomStore) *Config {
	return &Config{
		ExpectedItems:     1000,
		FalsePositiveRate: 0.05, // 5%
		Store:             store,
		KeyPrefix:         DefaultKeyPrefix,
		TTL:               DefaultTTL,
		EnableMetrics:     true,
	}
}
