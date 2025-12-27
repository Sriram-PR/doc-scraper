package detect

import (
	"sync"
)

// SelectorCache caches detected selectors per domain to avoid repeated detection
type SelectorCache struct {
	mu    sync.RWMutex
	cache map[string]DetectionResult
}

// NewSelectorCache creates a new selector cache
func NewSelectorCache() *SelectorCache {
	return &SelectorCache{
		cache: make(map[string]DetectionResult),
	}
}

// Get retrieves a cached detection result for a domain
func (c *SelectorCache) Get(domain string) (DetectionResult, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	result, ok := c.cache[domain]
	return result, ok
}

// Set stores a detection result for a domain
func (c *SelectorCache) Set(domain string, result DetectionResult) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache[domain] = result
}

// Clear removes all cached entries
func (c *SelectorCache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.cache = make(map[string]DetectionResult)
}

// Size returns the number of cached entries
func (c *SelectorCache) Size() int {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return len(c.cache)
}
