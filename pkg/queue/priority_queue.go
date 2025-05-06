package queue

import (
	"container/heap"
	"sync"

	"doc-scraper/pkg/models"

	"github.com/sirupsen/logrus"
)

// --- Priority Queue Implementation ---

// PQItem represents an item in the priority queue
type PQItem struct {
	workItem *models.WorkItem
	priority int // Lower value means higher priority (e.g., Depth)
	index    int // The index of the item in the heap (required by heap interface)
}

// PriorityQueue implements heap.Interface
type PriorityQueue []*PQItem

func (pq PriorityQueue) Len() int { return len(pq) }

func (pq PriorityQueue) Less(i, j int) bool {
	// Pop should return the item with the smallest priority value (lowest depth)
	return pq[i].priority < pq[j].priority
}

func (pq PriorityQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
	pq[i].index = i
	pq[j].index = j
}

// Push adds an element to the heap
func (pq *PriorityQueue) Push(x any) {
	n := len(*pq)
	item := x.(*PQItem)
	item.index = n
	*pq = append(*pq, item)
}

// Pop removes and returns the highest priority element (minimum value) from the heap
func (pq *PriorityQueue) Pop() any {
	old := *pq
	n := len(old)
	item := old[n-1]
	old[n-1] = nil  // avoid memory leak
	item.index = -1 // for safety
	*pq = old[0 : n-1]
	return item
}

// ThreadSafePriorityQueue wraps PriorityQueue with concurrency controls
type ThreadSafePriorityQueue struct {
	pq     PriorityQueue
	mu     sync.Mutex
	cond   *sync.Cond // Condition variable to wait for items
	closed bool
	log    *logrus.Logger // Reference to the main logger
}

// NewThreadSafePriorityQueue creates a new thread-safe priority queue
func NewThreadSafePriorityQueue(logger *logrus.Logger) *ThreadSafePriorityQueue {
	tspq := &ThreadSafePriorityQueue{log: logger}
	tspq.cond = sync.NewCond(&tspq.mu) // Initialize condition variable
	heap.Init(&tspq.pq)                // Initialize the underlying heap
	return tspq
}

// Add pushes a work item onto the queue with priority based on depth
func (tspq *ThreadSafePriorityQueue) Add(item *models.WorkItem) {
	tspq.mu.Lock()
	defer tspq.mu.Unlock()

	if tspq.closed {
		tspq.log.Warnf("Attempted to add item to closed queue: %s", item.URL)
		return
	}

	pqItem := &PQItem{
		workItem: item,
		priority: item.Depth, // Use Depth as priority (lower depth = higher priority)
	}
	heap.Push(&tspq.pq, pqItem) // Add item to the heap
	tspq.cond.Signal()          // Signal one waiting worker that an item is available
}

// Pop retrieves and removes the highest priority work item
// It blocks if the queue is empty until an item is added or the queue is closed
// Returns the item and true, or nil and false if the queue is closed and empty
func (tspq *ThreadSafePriorityQueue) Pop() (*models.WorkItem, bool) {
	tspq.mu.Lock()
	defer tspq.mu.Unlock()

	// Wait while the queue is empty AND not closed
	for len(tspq.pq) == 0 {
		if tspq.closed {
			return nil, false // Queue closed and empty, signal worker to exit
		}
		// Wait releases the lock and waits for a Signal/Broadcast; reacquires lock upon waking
		tspq.cond.Wait()
	}

	// Re-check after waking up, in case Close() was called concurrently
	if len(tspq.pq) == 0 && tspq.closed {
		return nil, false
	}

	// Pop the highest priority item from the heap
	pqItem := heap.Pop(&tspq.pq).(*PQItem)
	return pqItem.workItem, true
}

// Close signals that no more items will be added to the queue
func (tspq *ThreadSafePriorityQueue) Close() {
	tspq.mu.Lock()
	defer tspq.mu.Unlock()
	if !tspq.closed {
		tspq.closed = true
		tspq.cond.Broadcast() // Wake up ALL waiting workers so they can check the closed status
	}
}

// Len returns the current number of items in the queue (thread-safe)
func (tspq *ThreadSafePriorityQueue) Len() int {
	tspq.mu.Lock()
	defer tspq.mu.Unlock()
	return len(tspq.pq)
}
