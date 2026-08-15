package queue

import (
	"container/heap"
	"log/slog"
	"sync"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/models"
)

// PQItem represents an item in the priority queue.
type PQItem struct {
	workItem *models.WorkItem
	priority int // lower = higher priority (depth-first: shallowest pages first)
	index    int // required by heap.Interface
}

// PriorityQueue implements heap.Interface.
type PriorityQueue []*PQItem

func (pq PriorityQueue) Len() int { return len(pq) }

func (pq PriorityQueue) Less(i, j int) bool {
	return pq[i].priority < pq[j].priority
}

func (pq PriorityQueue) Swap(i, j int) {
	pq[i], pq[j] = pq[j], pq[i]
	pq[i].index = i
	pq[j].index = j
}

func (pq *PriorityQueue) Push(x any) {
	n := len(*pq)
	item := x.(*PQItem)
	item.index = n
	*pq = append(*pq, item)
}

func (pq *PriorityQueue) Pop() any {
	old := *pq
	n := len(old)
	item := old[n-1]
	old[n-1] = nil  // prevent memory leak
	item.index = -1 // mark as removed
	*pq = old[0 : n-1]
	return item
}

// ThreadSafePriorityQueue wraps PriorityQueue with a mutex and condition variable.
type ThreadSafePriorityQueue struct {
	pq     PriorityQueue
	mu     sync.Mutex
	cond   *sync.Cond
	closed bool
	log    *slog.Logger
}

func NewThreadSafePriorityQueue(logger *slog.Logger) *ThreadSafePriorityQueue {
	tspq := &ThreadSafePriorityQueue{log: logger}
	tspq.cond = sync.NewCond(&tspq.mu)
	heap.Init(&tspq.pq)
	return tspq
}

// Add pushes a work item onto the queue (priority = depth).
func (tspq *ThreadSafePriorityQueue) Add(item *models.WorkItem) {
	tspq.mu.Lock()
	defer tspq.mu.Unlock()

	if tspq.closed {
		tspq.log.Warn("Attempted to add item to closed queue", "url", item.URL)
		return
	}

	pqItem := &PQItem{
		workItem: item,
		priority: item.Depth,
	}
	heap.Push(&tspq.pq, pqItem)
	tspq.cond.Signal()
}

// Pop blocks until an item is available or the queue is closed.
// Returns (item, true) or (nil, false) when closed and empty.
func (tspq *ThreadSafePriorityQueue) Pop() (*models.WorkItem, bool) {
	tspq.mu.Lock()
	defer tspq.mu.Unlock()

	for len(tspq.pq) == 0 {
		if tspq.closed {
			return nil, false
		}
		tspq.cond.Wait()
	}

	pqItem := heap.Pop(&tspq.pq).(*PQItem)
	return pqItem.workItem, true
}

// Close marks the queue as closed and wakes all blocked Pop callers.
func (tspq *ThreadSafePriorityQueue) Close() {
	tspq.mu.Lock()
	defer tspq.mu.Unlock()
	if !tspq.closed {
		tspq.closed = true
		tspq.cond.Broadcast()
	}
}

// Len returns the current number of items in the queue.
func (tspq *ThreadSafePriorityQueue) Len() int {
	tspq.mu.Lock()
	defer tspq.mu.Unlock()
	return len(tspq.pq)
}
