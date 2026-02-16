package fetch

import (
	"sync"

	"github.com/sirupsen/logrus"
	"golang.org/x/sync/semaphore"
)

// HostSemaphorePool manages per-host semaphores for rate limiting concurrent
// requests to each host. A single pool should be shared across all components
// (crawler, image processor) so the per-host limit is enforced globally.
type HostSemaphorePool struct {
	semaphores map[string]*semaphore.Weighted
	mu         sync.Mutex
	limit      int64
	log        *logrus.Entry
}

// NewHostSemaphorePool creates a new pool with the given per-host concurrency limit.
func NewHostSemaphorePool(maxPerHost int, log *logrus.Entry) *HostSemaphorePool {
	limit := int64(maxPerHost)
	if limit <= 0 {
		limit = 2
		log.Warnf("max_requests_per_host invalid or zero, defaulting to %d", limit)
	}
	return &HostSemaphorePool{
		semaphores: make(map[string]*semaphore.Weighted),
		limit:      limit,
		log:        log,
	}
}

// Get retrieves or creates a semaphore for the given host.
func (p *HostSemaphorePool) Get(host string) *semaphore.Weighted {
	p.mu.Lock()
	defer p.mu.Unlock()

	sem, exists := p.semaphores[host]
	if !exists {
		sem = semaphore.NewWeighted(p.limit)
		p.semaphores[host] = sem
		p.log.WithFields(logrus.Fields{"host": host, "limit": p.limit}).Debug("Created new host semaphore")
	}
	return sem
}
