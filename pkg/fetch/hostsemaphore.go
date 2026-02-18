package fetch

import (
	"context"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"golang.org/x/sync/semaphore"
)

// hostEntry tracks a single host's semaphore and its usage state.
type hostEntry struct {
	sem         *semaphore.Weighted
	activeCount int64     // number of held + waiting permits
	lastRelease time.Time // updated on every Release; zero if never released
}

// HostSemaphorePool manages per-host semaphores for rate limiting concurrent
// requests to each host. A single pool should be shared across all components
// (crawler, image processor) so the per-host limit is enforced globally.
type HostSemaphorePool struct {
	entries map[string]*hostEntry
	mu      sync.Mutex
	limit   int64
	log     *logrus.Entry
}

// NewHostSemaphorePool creates a new pool with the given per-host concurrency limit.
func NewHostSemaphorePool(maxPerHost int, log *logrus.Entry) *HostSemaphorePool {
	limit := int64(maxPerHost)
	if limit <= 0 {
		limit = 2
		log.Warnf("max_requests_per_host invalid or zero, defaulting to %d", limit)
	}
	return &HostSemaphorePool{
		entries: make(map[string]*hostEntry),
		limit:   limit,
		log:     log,
	}
}

// Acquire gets or creates a host semaphore and acquires one permit.
// Blocks until the permit is available or ctx is cancelled.
func (p *HostSemaphorePool) Acquire(ctx context.Context, host string) error {
	p.mu.Lock()
	entry, exists := p.entries[host]
	if !exists {
		entry = &hostEntry{sem: semaphore.NewWeighted(p.limit)}
		p.entries[host] = entry
		p.log.WithFields(logrus.Fields{"host": host, "limit": p.limit}).Debug("Created new host semaphore")
	}
	entry.activeCount++
	p.mu.Unlock()

	if err := entry.sem.Acquire(ctx, 1); err != nil {
		p.mu.Lock()
		entry.activeCount--
		p.mu.Unlock()
		return err
	}
	return nil
}

// Release releases one permit for the given host.
func (p *HostSemaphorePool) Release(host string) {
	p.mu.Lock()
	entry, exists := p.entries[host]
	if !exists {
		p.mu.Unlock()
		p.log.Errorf("hostsemaphore: Release called for unknown host: %s", host)
		return
	}
	entry.activeCount--
	entry.lastRelease = time.Now()
	p.mu.Unlock()

	entry.sem.Release(1)
}

// RunEviction periodically removes idle host entries. Should be run in a goroutine.
func (p *HostSemaphorePool) RunEviction(ctx context.Context, interval time.Duration) {
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	p.log.Info("Host semaphore eviction goroutine started.")

	for {
		select {
		case <-ticker.C:
			p.evictIdle(interval)
		case <-ctx.Done():
			p.log.Infof("Stopping host semaphore eviction: %v", ctx.Err())
			return
		}
	}
}

// evictIdle removes entries that have been idle longer than maxIdle.
func (p *HostSemaphorePool) evictIdle(maxIdle time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	evicted := 0
	for host, entry := range p.entries {
		if entry.activeCount == 0 && !entry.lastRelease.IsZero() && now.Sub(entry.lastRelease) >= maxIdle {
			delete(p.entries, host)
			evicted++
		}
	}
	if evicted > 0 {
		p.log.Debugf("Evicted %d idle host semaphores, %d remain", evicted, len(p.entries))
	}
}

// Len returns the current number of tracked hosts.
func (p *HostSemaphorePool) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.entries)
}
