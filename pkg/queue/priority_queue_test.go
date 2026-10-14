package queue

import (
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/models"
)

// testLogger returns a logger that discards output
func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestNewThreadSafePriorityQueue(t *testing.T) {
	pq := NewThreadSafePriorityQueue(testLogger())
	require.NotNil(t, pq)
	assert.Equal(t, 0, pq.Len())
}

func TestThreadSafePriorityQueue_AddAndPop(t *testing.T) {
	pq := NewThreadSafePriorityQueue(testLogger())

	item := &models.WorkItem{URL: "http://example.com", Depth: 0}
	pq.Add(item)

	assert.Equal(t, 1, pq.Len())

	result, ok := pq.Pop()
	require.True(t, ok)
	assert.Equal(t, item.URL, result.URL)
	assert.Equal(t, 0, pq.Len())
}

func TestThreadSafePriorityQueue_PriorityOrdering(t *testing.T) {
	pq := NewThreadSafePriorityQueue(testLogger())

	// Add items with different depths (priorities)
	// Lower depth = higher priority (should be popped first)
	pq.Add(&models.WorkItem{URL: "depth2", Depth: 2})
	pq.Add(&models.WorkItem{URL: "depth0", Depth: 0})
	pq.Add(&models.WorkItem{URL: "depth1", Depth: 1})
	pq.Add(&models.WorkItem{URL: "depth3", Depth: 3})

	expectedOrder := []string{"depth0", "depth1", "depth2", "depth3"}
	for i, expected := range expectedOrder {
		item, ok := pq.Pop()
		require.True(t, ok, "Pop() #%d", i)
		assert.Equal(t, expected, item.URL, "Pop() #%d", i)
	}
}

func TestThreadSafePriorityQueue_SamePriority(t *testing.T) {
	pq := NewThreadSafePriorityQueue(testLogger())

	// Add multiple items with same depth
	pq.Add(&models.WorkItem{URL: "a", Depth: 1})
	pq.Add(&models.WorkItem{URL: "b", Depth: 1})
	pq.Add(&models.WorkItem{URL: "c", Depth: 1})

	// All should be retrievable (order not guaranteed for same priority)
	urls := make(map[string]bool)
	for i := range 3 {
		item, ok := pq.Pop()
		require.True(t, ok, "Pop() #%d", i)
		urls[item.URL] = true
	}

	assert.Equal(t, map[string]bool{"a": true, "b": true, "c": true}, urls)
}

func TestThreadSafePriorityQueue_Close(t *testing.T) {
	pq := NewThreadSafePriorityQueue(testLogger())
	pq.Close()

	// Pop on closed empty queue should return false
	item, ok := pq.Pop()
	assert.False(t, ok)
	assert.Nil(t, item)
}

func TestThreadSafePriorityQueue_CloseWithItems(t *testing.T) {
	pq := NewThreadSafePriorityQueue(testLogger())

	pq.Add(&models.WorkItem{URL: "a", Depth: 0})
	pq.Add(&models.WorkItem{URL: "b", Depth: 1})
	pq.Close()

	// Should still be able to pop existing items
	for range 2 {
		item, ok := pq.Pop()
		assert.True(t, ok, "Pop() after Close should return existing items")
		assert.NotNil(t, item)
	}

	// Now queue is empty and closed
	item, ok := pq.Pop()
	assert.False(t, ok)
	assert.Nil(t, item)
}

func TestThreadSafePriorityQueue_AddAfterClose(t *testing.T) {
	pq := NewThreadSafePriorityQueue(testLogger())
	pq.Close()

	// Add after close should be a no-op (with warning log)
	pq.Add(&models.WorkItem{URL: "test", Depth: 0})

	assert.Equal(t, 0, pq.Len())
}

func TestThreadSafePriorityQueue_DoubleClose(t *testing.T) {
	pq := NewThreadSafePriorityQueue(testLogger())

	// Double close should not panic
	pq.Close()
	pq.Close() // Should be safe
}

func TestThreadSafePriorityQueue_PopBlocks(t *testing.T) {
	pq := NewThreadSafePriorityQueue(testLogger())

	resultChan := make(chan *models.WorkItem, 1)
	go func() {
		item, ok := pq.Pop() // This should block
		if ok {
			resultChan <- item
		} else {
			resultChan <- nil
		}
	}()

	// Give goroutine time to start blocking
	time.Sleep(50 * time.Millisecond)

	// Verify no result yet (still blocking)
	select {
	case <-resultChan:
		t.Fatal("Pop() returned before Add(), should have blocked")
	default:
		// Expected - still blocking
	}

	// Add an item to unblock
	pq.Add(&models.WorkItem{URL: "unblock", Depth: 0})

	// Should receive result now
	select {
	case item := <-resultChan:
		require.NotNil(t, item, "Pop() returned nil after Add()")
		assert.Equal(t, "unblock", item.URL)
	case <-time.After(1 * time.Second):
		t.Fatal("Pop() did not return after Add()")
	}
}

func TestThreadSafePriorityQueue_CloseUnblocksWaiters(t *testing.T) {
	pq := NewThreadSafePriorityQueue(testLogger())

	var wg sync.WaitGroup
	results := make(chan bool, 3)

	// Start multiple waiting goroutines
	for range 3 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, ok := pq.Pop() // Block waiting
			results <- ok
		}()
	}

	// Give goroutines time to start blocking
	time.Sleep(50 * time.Millisecond)

	// Close should unblock all waiters
	pq.Close()

	// Wait for all goroutines with timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// All goroutines finished
	case <-time.After(1 * time.Second):
		t.Fatal("Close() did not unblock waiting goroutines")
	}

	// All should have returned false (queue closed and empty)
	close(results)
	for ok := range results {
		assert.False(t, ok, "Blocked Pop() returned ok=true after Close()")
	}
}

func TestThreadSafePriorityQueue_ConcurrentAdd(t *testing.T) {
	pq := NewThreadSafePriorityQueue(testLogger())

	var wg sync.WaitGroup
	numItems := 100

	// Concurrently add items
	for i := range numItems {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			pq.Add(&models.WorkItem{URL: "url", Depth: id % 10})
		}(i)
	}

	wg.Wait()

	assert.Equal(t, numItems, pq.Len())
}

func TestThreadSafePriorityQueue_ConcurrentAddPop(t *testing.T) {
	pq := NewThreadSafePriorityQueue(testLogger())

	var wg sync.WaitGroup
	numProducers := 5
	numConsumers := 3
	itemsPerProducer := 20
	totalItems := numProducers * itemsPerProducer

	// Track items popped
	var poppedCount int64
	var countMu sync.Mutex

	// Start consumers
	for range numConsumers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				_, ok := pq.Pop()
				if !ok {
					return // Queue closed and empty
				}
				countMu.Lock()
				poppedCount++
				countMu.Unlock()
			}
		}()
	}

	// Start producers
	var producerWg sync.WaitGroup
	for i := range numProducers {
		producerWg.Add(1)
		go func(producerID int) {
			defer producerWg.Done()
			for range itemsPerProducer {
				pq.Add(&models.WorkItem{
					URL:   "url",
					Depth: producerID,
				})
			}
		}(i)
	}

	// Wait for all producers, then close
	producerWg.Wait()
	pq.Close()

	// Wait for consumers with timeout
	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// Success
	case <-time.After(5 * time.Second):
		t.Fatal("Consumers did not finish in time")
	}

	countMu.Lock()
	assert.Equal(t, totalItems, int(poppedCount))
	countMu.Unlock()
}

func TestThreadSafePriorityQueue_LenAccuracy(t *testing.T) {
	pq := NewThreadSafePriorityQueue(testLogger())

	for i := range 10 {
		pq.Add(&models.WorkItem{URL: "url", Depth: i})
		assert.Equal(t, i+1, pq.Len(), "after Add #%d", i)
	}

	for i := 10; i > 0; i-- {
		pq.Pop()
		assert.Equal(t, i-1, pq.Len(), "after Pop")
	}
}
