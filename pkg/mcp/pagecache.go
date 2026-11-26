package mcp

import (
	"container/list"
	"sync"
	"time"
)

const (
	pageCacheTTL      = 5 * time.Minute
	pageCacheMaxBytes = 16 << 20
)

type pageCacheKey struct{ url, selector string }

type cachedPage struct {
	title, content string
	fetchedAt      time.Time
}

func (p cachedPage) size() int { return len(p.title) + len(p.content) }

// pageCache keeps get_page results so reading a page in parts fetches it once.
// The zero value is ready to use.
type pageCache struct {
	mu    sync.Mutex
	order *list.List
	items map[pageCacheKey]*list.Element
	bytes int
	now   func() time.Time
}

type pageCacheItem struct {
	key     pageCacheKey
	page    cachedPage
	expires time.Time
}

// get returns the entry for k; false when absent or expired. A hit counts as use.
func (c *pageCache) get(k pageCacheKey) (cachedPage, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	el, ok := c.items[k]
	if !ok {
		return cachedPage{}, false
	}
	item := el.Value.(*pageCacheItem)
	if !c.clock().Before(item.expires) {
		c.remove(el)
		return cachedPage{}, false
	}
	c.order.MoveToFront(el)
	return item.page, true
}

// put stores p under k, replacing any entry, and evicts least recently used
// entries until the total fits; an entry over the cap is not stored, but still
// drops k's older entry.
func (c *pageCache) put(k pageCacheKey, p cachedPage) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.items == nil {
		c.items = make(map[pageCacheKey]*list.Element)
		c.order = list.New()
	}
	if el, ok := c.items[k]; ok {
		c.remove(el)
	}
	if p.size() > pageCacheMaxBytes {
		return
	}
	c.items[k] = c.order.PushFront(&pageCacheItem{key: k, page: p, expires: c.clock().Add(pageCacheTTL)})
	c.bytes += p.size()
	for c.bytes > pageCacheMaxBytes {
		c.remove(c.order.Back())
	}
}

func (c *pageCache) remove(el *list.Element) {
	item := c.order.Remove(el).(*pageCacheItem)
	delete(c.items, item.key)
	c.bytes -= item.page.size()
}

func (c *pageCache) clock() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}
