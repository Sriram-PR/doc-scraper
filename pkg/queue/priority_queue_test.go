package queue

import (
	"io"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"

	"doc-scraper/pkg/models"
)

// testLogger returns a logger that discards output
func testLogger() *logrus.Logger {
	log := logrus.New()
	log.SetOutput(io.Discard)
	return log
}

// --- Basic Operations Tests ---

func TestNewThreadSafePriorityQueue(t *testing.T) {
	pq := NewThreadSafePriorityQueue(testLogger())
	if pq == nil {
		t.Fatal("NewThreadSafePriorityQueue() returned nil")
	}
	if pq.Len() != 0 {
		t.Errorf("New queue Len() = %d, want 0", pq.Len())
	}
}

func TestThreadSafePriorityQueue_AddAndPop(t *testing.T) {
	pq := NewThreadSafePriorityQueue(testLogger())

	item := &models.WorkItem{URL: "http://example.com", Depth: 0}
	pq.Add(item)

	if pq.Len() != 1 {
		t.Errorf("After Add, Len() = %d, want 1", pq.Len())
	}

	result, ok := pq.Pop()
	if !ok {
		t.Fatal("Pop() returned ok=false, want true")
	}
	if result.URL != item.URL {
		t.Errorf("Pop() URL = %q, want %q", result.URL, item.URL)
	}
	if pq.Len() != 0 {
		t.Errorf("After Pop, Len() = %d, want 0", pq.Len())
	}
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
		if !ok {
			t.Fatalf("Pop() #%d returned ok=false", i)
		}
		if item.URL != expected {
			t.Errorf("Pop() #%d URL = %q, want %q", i, item.URL, expected)
		}
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
	for i := 0; i < 3; i++ {
		item, ok := pq.Pop()
		if !ok {
			t.Fatalf("Pop() #%d returned ok=false", i)
		}
		urls[item.URL] = true
	}

	if len(urls) != 3 {
		t.Errorf("Expected 3 unique URLs, got %d", len(urls))
	}
	for _, url := range []string{"a", "b", "c"} {
		if !urls[url] {
			t.Errorf("URL %q was not retrieved", url)
		}
	}
}

// --- Close Tests ---

func TestThreadSafePriorityQueue_Close(t *testing.T) {
	pq := NewThreadSafePriorityQueue(testLogger())
	pq.Close()

	// Pop on closed empty queue should return false
	item, ok := pq.Pop()
	if ok {
		t.Error("Pop() on closed empty queue returned ok=true, want false")
	}
	if item != nil {
		t.Errorf("Pop() on closed empty queue returned item %v, want nil", item)
	}
}

func TestThreadSafePriorityQueue_CloseWithItems(t *testing.T) {
	pq := NewThreadSafePriorityQueue(testLogger())

	pq.Add(&models.WorkItem{URL: "a", Depth: 0})
	pq.Add(&models.WorkItem{URL: "b", Depth: 1})
	pq.Close()

	// Should still be able to pop existing items
	item1, ok1 := pq.Pop()
	if !ok1 || item1 == nil {
		t.Error("Pop() after Close should return existing items")
	}

	item2, ok2 := pq.Pop()
	if !ok2 || item2 == nil {
		t.Error("Pop() after Close should return existing items")
	}

	// Now queue is empty and closed
	item3, ok3 := pq.Pop()
	if ok3 {
		t.Error("Pop() on closed empty queue returned ok=true")
	}
	if item3 != nil {
		t.Error("Pop() on closed empty queue returned non-nil item")
	}
}

func TestThreadSafePriorityQueue_AddAfterClose(t *testing.T) {
	pq := NewThreadSafePriorityQueue(testLogger())
	pq.Close()

	// Add after close should be a no-op (with warning log)
	pq.Add(&models.WorkItem{URL: "test", Depth: 0})

	if pq.Len() != 0 {
		t.Errorf("Add after Close: Len() = %d, want 0", pq.Len())
	}
}

func TestThreadSafePriorityQueue_DoubleClose(t *testing.T) {
	pq := NewThreadSafePriorityQueue(testLogger())

	// Double close should not panic
	pq.Close()
	pq.Close() // Should be safe
}

// --- Blocking Behavior Tests ---

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
		if item == nil {
			t.Error("Pop() returned nil after Add()")
		} else if item.URL != "unblock" {
			t.Errorf("Pop() URL = %q, want %q", item.URL, "unblock")
		}
	case <-time.After(1 * time.Second):
		t.Fatal("Pop() did not return after Add()")
	}
}

func TestThreadSafePriorityQueue_CloseUnblocksWaiters(t *testing.T) {
	pq := NewThreadSafePriorityQueue(testLogger())

	var wg sync.WaitGroup
	results := make(chan bool, 3)

	// Start multiple waiting goroutines
	for i := 0; i < 3; i++ {
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
		if ok {
			t.Error("Blocked Pop() returned ok=true after Close()")
		}
	}
}

// --- Concurrency Tests ---

func TestThreadSafePriorityQueue_ConcurrentAdd(t *testing.T) {
	pq := NewThreadSafePriorityQueue(testLogger())

	var wg sync.WaitGroup
	numItems := 100

	// Concurrently add items
	for i := 0; i < numItems; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			pq.Add(&models.WorkItem{URL: "url", Depth: id % 10})
		}(i)
	}

	wg.Wait()

	if pq.Len() != numItems {
		t.Errorf("After concurrent Add, Len() = %d, want %d", pq.Len(), numItems)
	}
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
	for i := 0; i < numConsumers; i++ {
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
	for i := 0; i < numProducers; i++ {
		producerWg.Add(1)
		go func(producerID int) {
			defer producerWg.Done()
			for j := 0; j < itemsPerProducer; j++ {
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
	if int(poppedCount) != totalItems {
		t.Errorf("Popped %d items, want %d", poppedCount, totalItems)
	}
	countMu.Unlock()
}

// --- Len Tests ---

func TestThreadSafePriorityQueue_LenAccuracy(t *testing.T) {
	pq := NewThreadSafePriorityQueue(testLogger())

	for i := 0; i < 10; i++ {
		pq.Add(&models.WorkItem{URL: "url", Depth: i})
		if pq.Len() != i+1 {
			t.Errorf("After Add #%d, Len() = %d, want %d", i, pq.Len(), i+1)
		}
	}

	for i := 10; i > 0; i-- {
		pq.Pop()
		if pq.Len() != i-1 {
			t.Errorf("After Pop (remaining=%d), Len() = %d, want %d", i-1, pq.Len(), i-1)
		}
	}
}
