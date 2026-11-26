package mcp

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

type fakeClock struct{ t time.Time }

func (c *fakeClock) now() time.Time { return c.t }

func newTestPageCache() (*pageCache, *fakeClock) {
	clock := &fakeClock{t: time.Date(2026, 11, 25, 10, 0, 0, 0, time.UTC)}
	return &pageCache{now: clock.now}, clock
}

func pageOfSize(n int) cachedPage {
	return cachedPage{title: "T", content: strings.Repeat("x", n-1)}
}

func TestPageCache_ExpiresAfterTheTTL(t *testing.T) {
	c, clock := newTestPageCache()
	k := pageCacheKey{"https://d.example/a", "body"}
	c.put(k, cachedPage{title: "A", content: "body", fetchedAt: clock.t})

	clock.t = clock.t.Add(pageCacheTTL - time.Second)
	got, ok := c.get(k)
	assert.True(t, ok)
	assert.Equal(t, "body", got.content)

	clock.t = clock.t.Add(2 * time.Second)
	_, ok = c.get(k)
	assert.False(t, ok)
}

func TestPageCache_EvictsTheLeastRecentlyUsed(t *testing.T) {
	c, _ := newTestPageCache()
	a, b, d := pageCacheKey{"a", "body"}, pageCacheKey{"b", "body"}, pageCacheKey{"c", "body"}
	third := pageCacheMaxBytes / 3
	c.put(a, pageOfSize(third))
	c.put(b, pageOfSize(third))
	_, ok := c.get(a)
	assert.True(t, ok)
	c.put(d, pageOfSize(third+2))

	_, okA := c.get(a)
	_, okB := c.get(b)
	_, okC := c.get(d)
	assert.True(t, okA, "a was used after b")
	assert.False(t, okB, "b is the least recently used")
	assert.True(t, okC)
}

func TestPageCache_ReplaceKeepsTheByteCount(t *testing.T) {
	c, _ := newTestPageCache()
	a, b := pageCacheKey{"a", "body"}, pageCacheKey{"b", "body"}
	half := pageCacheMaxBytes / 2
	c.put(a, pageOfSize(half))
	c.put(a, pageOfSize(half))
	c.put(b, pageOfSize(half))

	_, okA := c.get(a)
	_, okB := c.get(b)
	assert.True(t, okA, "the replaced entry's old size was not subtracted")
	assert.True(t, okB)
}

func TestPageCache_IgnoresAnEntryOverTheCap(t *testing.T) {
	c, _ := newTestPageCache()
	a, big := pageCacheKey{"a", "body"}, pageCacheKey{"big", "body"}
	c.put(a, pageOfSize(10))
	c.put(big, pageOfSize(pageCacheMaxBytes+1))

	_, okA := c.get(a)
	_, okBig := c.get(big)
	assert.True(t, okA)
	assert.False(t, okBig)
}

func TestPageCache_ConcurrentUse(t *testing.T) {
	var c pageCache
	var wg sync.WaitGroup
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			k := pageCacheKey{string(rune('a' + i%3)), "body"}
			for range 100 {
				c.put(k, pageOfSize(100))
				c.get(k)
			}
		}()
	}
	wg.Wait()
	_, ok := c.get(pageCacheKey{"a", "body"})
	assert.True(t, ok)
}
