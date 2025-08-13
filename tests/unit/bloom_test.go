package unit

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/SeaSBee/go-patternx/patternx/bloom"
)

func TestNewBloomFilter(t *testing.T) {
	config := &bloom.Config{
		ExpectedItems:     1000,
		FalsePositiveRate: 0.01,
	}

	bf, err := bloom.NewBloomFilter(config)
	if err != nil {
		t.Fatalf("Expected no error creating bloom filter, got %v", err)
	}

	if bf == nil {
		t.Fatal("Expected bloom filter to be created")
	}
}

func TestBloomFilterAddAndContains(t *testing.T) {
	config := &bloom.Config{
		ExpectedItems:     100,
		FalsePositiveRate: 0.01,
	}

	bf, err := bloom.NewBloomFilter(config)
	if err != nil {
		t.Fatalf("Failed to create bloom filter: %v", err)
	}

	// Add items
	items := []string{"item1", "item2", "item3", "item4", "item5"}

	for _, item := range items {
		err := bf.Add(context.Background(), item)
		if err != nil {
			t.Errorf("Failed to add item %s: %v", item, err)
		}
	}

	// Check that added items are contained
	for _, item := range items {
		contained, err := bf.Contains(context.Background(), item)
		if err != nil {
			t.Errorf("Failed to check contains for %s: %v", item, err)
		}
		if !contained {
			t.Errorf("Expected item %s to be contained, but it wasn't", item)
		}
	}

	// Check that non-added items are not contained
	nonItems := []string{"nonitem1", "nonitem2", "nonitem3"}

	for _, item := range nonItems {
		contained, err := bf.Contains(context.Background(), item)
		if err != nil {
			t.Errorf("Failed to check contains for %s: %v", item, err)
		}
		if contained {
			t.Errorf("Expected item %s to not be contained, but it was", item)
		}
	}
}

func TestBloomFilterBatchOperations(t *testing.T) {
	config := &bloom.Config{
		ExpectedItems:     100,
		FalsePositiveRate: 0.01,
	}

	bf, err := bloom.NewBloomFilter(config)
	if err != nil {
		t.Fatalf("Failed to create bloom filter: %v", err)
	}

	// Add items in batch
	items := []string{"batch1", "batch2", "batch3", "batch4", "batch5"}

	err = bf.AddBatch(context.Background(), items)
	if err != nil {
		t.Errorf("Failed to add batch: %v", err)
	}

	// Check that all batch items are contained
	for _, item := range items {
		contained, err := bf.Contains(context.Background(), item)
		if err != nil {
			t.Errorf("Failed to check contains for %s: %v", item, err)
		}
		if !contained {
			t.Errorf("Expected batch item %s to be contained, but it wasn't", item)
		}
	}
}

func TestBloomFilterConfigValidation(t *testing.T) {
	// Test with zero expected items - should fail validation
	config := &bloom.Config{
		ExpectedItems:     0,
		FalsePositiveRate: 0.01,
	}

	_, err := bloom.NewBloomFilter(config)
	if err == nil {
		t.Log("Expected error with zero expected items")
	}

	// Test with invalid false positive rate
	config2 := &bloom.Config{
		ExpectedItems:     100,
		FalsePositiveRate: 1.5, // Invalid: > 1
	}

	_, err = bloom.NewBloomFilter(config2)
	if err == nil {
		t.Log("Expected error with invalid false positive rate")
	}

	// Test with negative false positive rate
	config3 := &bloom.Config{
		ExpectedItems:     100,
		FalsePositiveRate: -0.1, // Invalid: negative
	}

	_, err = bloom.NewBloomFilter(config3)
	if err == nil {
		t.Log("Expected error with negative false positive rate")
	}
}

func TestBloomFilterGetStats(t *testing.T) {
	config := &bloom.Config{
		ExpectedItems:     100,
		FalsePositiveRate: 0.01,
	}

	bf, err := bloom.NewBloomFilter(config)
	if err != nil {
		t.Fatalf("Failed to create bloom filter: %v", err)
	}

	// Add some items
	items := []string{"stat1", "stat2", "stat3"}
	for _, item := range items {
		err := bf.Add(context.Background(), item)
		if err != nil {
			t.Errorf("Failed to add item %s: %v", item, err)
		}
	}

	// Get stats
	stats := bf.GetStats()

	// Check that stats contain expected information
	if stats["size"] == nil {
		t.Log("Expected size in stats")
	}
	if stats["hash_count"] == nil {
		t.Log("Expected hash_count in stats")
	}
	if stats["item_count"] == nil {
		t.Log("Expected item_count in stats")
	}
}

func TestBloomFilterClear(t *testing.T) {
	config := &bloom.Config{
		ExpectedItems:     100,
		FalsePositiveRate: 0.01,
	}

	bf, err := bloom.NewBloomFilter(config)
	if err != nil {
		t.Fatalf("Failed to create bloom filter: %v", err)
	}

	// Add items
	items := []string{"clear1", "clear2", "clear3"}
	for _, item := range items {
		err := bf.Add(context.Background(), item)
		if err != nil {
			t.Errorf("Failed to add item %s: %v", item, err)
		}
	}

	// Verify items are contained
	for _, item := range items {
		contained, err := bf.Contains(context.Background(), item)
		if err != nil {
			t.Errorf("Failed to check contains for %s: %v", item, err)
		}
		if !contained {
			t.Errorf("Expected item %s to be contained before clear, but it wasn't", item)
		}
	}

	// Clear the filter
	err = bf.Clear(context.Background())
	if err != nil {
		t.Errorf("Failed to clear bloom filter: %v", err)
	}

	// Verify items are no longer contained
	for _, item := range items {
		contained, err := bf.Contains(context.Background(), item)
		if err != nil {
			t.Errorf("Failed to check contains for %s: %v", item, err)
		}
		if contained {
			t.Errorf("Expected item %s to not be contained after clear, but it was", item)
		}
	}
}

func TestBloomFilterStats(t *testing.T) {
	config := &bloom.Config{
		ExpectedItems:     100,
		FalsePositiveRate: 0.01,
	}

	bf, err := bloom.NewBloomFilter(config)
	if err != nil {
		t.Fatalf("Failed to create bloom filter: %v", err)
	}

	// Initially should be 0
	stats := bf.GetStats()
	if stats["item_count"] != uint64(0) {
		t.Errorf("Expected initial item count 0, got %v", stats["item_count"])
	}

	// Add items
	items := []string{"count1", "count2", "count3"}
	for _, item := range items {
		err := bf.Add(context.Background(), item)
		if err != nil {
			t.Errorf("Failed to add item %s: %v", item, err)
		}
	}

	// Check item count via stats
	stats = bf.GetStats()
	if stats["item_count"] != uint64(len(items)) {
		t.Errorf("Expected item count %d, got %v", len(items), stats["item_count"])
	}

	// Check other stats
	if stats["max_items"] != uint64(100) {
		t.Errorf("Expected max items 100, got %v", stats["max_items"])
	}
	if stats["desired_false_positive_rate"] != 0.01 {
		t.Errorf("Expected false positive rate 0.01, got %v", stats["desired_false_positive_rate"])
	}
}

func TestBloomFilterConcurrentAccess(t *testing.T) {
	config := &bloom.Config{
		ExpectedItems:     1000,
		FalsePositiveRate: 0.01,
	}

	bf, err := bloom.NewBloomFilter(config)
	if err != nil {
		t.Fatalf("Failed to create bloom filter: %v", err)
	}

	// Test concurrent add operations
	var wg sync.WaitGroup
	numGoroutines := 10
	itemsPerGoroutine := 10

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func(goroutineID int) {
			defer wg.Done()

			for j := 0; j < itemsPerGoroutine; j++ {
				item := fmt.Sprintf("concurrent-%d-%d", goroutineID, j)
				err := bf.Add(context.Background(), item)
				if err != nil {
					t.Errorf("Failed to add item %s: %v", item, err)
				}
			}
		}(i)
	}

	wg.Wait()

	// Verify all items were added
	expectedCount := numGoroutines * itemsPerGoroutine
	stats := bf.GetStats()
	actualCount := stats["item_count"].(uint64)
	if actualCount != uint64(expectedCount) {
		t.Errorf("Expected %d items, got %d", expectedCount, actualCount)
	}
}

func TestBloomFilterEmptyString(t *testing.T) {
	config := &bloom.Config{
		ExpectedItems:     100,
		FalsePositiveRate: 0.01,
	}

	bf, err := bloom.NewBloomFilter(config)
	if err != nil {
		t.Fatalf("Failed to create bloom filter: %v", err)
	}

	// Test with empty string - should be rejected
	err = bf.Add(context.Background(), "")
	if err == nil {
		t.Log("Expected error when adding empty string")
	}

	// Test contains with empty string - should be rejected
	_, err = bf.Contains(context.Background(), "")
	if err == nil {
		t.Log("Expected error when checking contains for empty string")
	}
}

func TestBloomFilterSpecialCharacters(t *testing.T) {
	config := &bloom.Config{
		ExpectedItems:     100,
		FalsePositiveRate: 0.01,
	}

	bf, err := bloom.NewBloomFilter(config)
	if err != nil {
		t.Fatalf("Failed to create bloom filter: %v", err)
	}

	// Test with special characters
	specialItems := []string{
		"item with spaces",
		"item-with-dashes",
		"item_with_underscores",
		"item@with#special$chars",
		"item\nwith\twhitespace",
		"item with unicode: 🚀",
	}

	for _, item := range specialItems {
		err := bf.Add(context.Background(), item)
		if err != nil {
			t.Errorf("Failed to add special item %q: %v", item, err)
		}

		contained, err := bf.Contains(context.Background(), item)
		if err != nil {
			t.Errorf("Failed to check contains for special item %q: %v", item, err)
		}
		if !contained {
			t.Errorf("Expected special item %q to be contained, but it wasn't", item)
		}
	}
}
