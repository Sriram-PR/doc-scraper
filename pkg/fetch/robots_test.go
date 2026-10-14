package fetch

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/temoto/robotstxt"
	"golang.org/x/sync/semaphore"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/utils"
)

func newTestRobotsHandler() *RobotsHandler {
	return &RobotsHandler{
		robotsCache: make(map[string]robotsCacheEntry),
		log:         testLogger(),
	}
}

func TestRobotsCache_MissWhenEmpty(t *testing.T) {
	rh := newTestRobotsHandler()
	data, found := rh.lookupCache("example.com")
	assert.False(t, found)
	assert.Nil(t, data)
}

func TestRobotsCache_SuccessCachedIndefinitely(t *testing.T) {
	rh := newTestRobotsHandler()
	data, err := robotstxt.FromBytes([]byte("User-agent: *\nDisallow: /private\n"))
	require.NoError(t, err)
	rh.cacheSuccess("example.com", data)

	got, found := rh.lookupCache("example.com")
	assert.True(t, found)
	assert.Same(t, data, got)
}

func TestRobotsCache_FailureLivesWithinTTL(t *testing.T) {
	rh := newTestRobotsHandler()
	rh.cacheFailure("example.com")

	data, found := rh.lookupCache("example.com")
	assert.True(t, found, "fresh negative entry should be reported as present (fail-open)")
	assert.Nil(t, data)
}

func TestRobotsCache_FailureReattemptedAfterTTL(t *testing.T) {
	rh := newTestRobotsHandler()
	rh.robotsCache["example.com"] = robotsCacheEntry{expires: time.Now().Add(-time.Minute)}

	_, found := rh.lookupCache("example.com")
	assert.False(t, found, "expired negative entry should be reported absent so it re-fetches")
}

// countingRobotsFetcher records how many robots.txt requests actually go out
// and blocks briefly so concurrent callers overlap.
type countingRobotsFetcher struct {
	mu     sync.Mutex
	byURL  map[string]int
	body   string
	status int
}

func (c *countingRobotsFetcher) FetchWithRetry(req *http.Request, _ context.Context) (*http.Response, error) {
	c.mu.Lock()
	if c.byURL == nil {
		c.byURL = map[string]int{}
	}
	c.byURL[req.URL.String()]++
	c.mu.Unlock()

	time.Sleep(20 * time.Millisecond)

	status := c.status
	if status == 0 {
		status = http.StatusOK
	}
	resp := &http.Response{
		StatusCode: status,
		Status:     http.StatusText(status),
		Header:     http.Header{},
		Body:       io.NopCloser(strings.NewReader(c.body)),
	}
	if status >= 400 {
		return resp, fmt.Errorf("%w: status %d", utils.ErrClientHTTPError, status)
	}
	return resp, nil
}

func (c *countingRobotsFetcher) count(u string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.byURL[u]
}

func newFetchingRobotsHandler(f HTTPFetcher) *RobotsHandler {
	cfg := &config.AppConfig{DefaultUserAgent: "test-agent", MaxRequests: 8}
	return NewRobotsHandler(f, NewRateLimiter(0, testLogger()), semaphore.NewWeighted(8), NewHostSemaphorePool(4, testLogger()), nil, cfg, testLogger())
}

// Concurrent misses for one host must collapse into a single request.
func TestGetRobotsData_ConcurrentMissesFetchOnce(t *testing.T) {
	f := &countingRobotsFetcher{body: "User-agent: *\nDisallow: /private\n"}
	rh := newFetchingRobotsHandler(f)

	target, err := url.Parse("https://example.com/docs/page.html")
	require.NoError(t, err)

	const callers = 8
	var wg sync.WaitGroup
	results := make([]*robotstxt.RobotsData, callers)
	for i := range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results[i] = rh.GetRobotsData(target, nil, context.Background())
		}()
	}
	wg.Wait()

	assert.Equal(t, 1, f.count("https://example.com/robots.txt"))
	for i, r := range results {
		assert.NotNil(t, r, "caller %d got nil robots data", i)
	}
}

// A 404 must also be fetched once, and the negative cache must hold afterwards.
func TestGetRobotsData_ConcurrentMissesFetchOnceOn404(t *testing.T) {
	f := &countingRobotsFetcher{status: http.StatusNotFound}
	rh := newFetchingRobotsHandler(f)

	target, _ := url.Parse("https://example.com/docs/page.html")

	var wg sync.WaitGroup
	for range 6 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rh.GetRobotsData(target, nil, context.Background())
		}()
	}
	wg.Wait()

	assert.Equal(t, 1, f.count("https://example.com/robots.txt"))

	// Subsequent calls are served by the negative cache, not refetched.
	rh.GetRobotsData(target, nil, context.Background())
	assert.Equal(t, 1, f.count("https://example.com/robots.txt"), "refetched after negative caching")
}

// Distinct hosts must not share a singleflight slot.
func TestGetRobotsData_DistinctHostsFetchSeparately(t *testing.T) {
	f := &countingRobotsFetcher{body: "User-agent: *\n"}
	rh := newFetchingRobotsHandler(f)

	a, _ := url.Parse("https://a.example.com/x.html")
	b, _ := url.Parse("https://b.example.com/x.html")

	var wg sync.WaitGroup
	for _, u := range []*url.URL{a, b, a, b} {
		wg.Add(1)
		go func() {
			defer wg.Done()
			rh.GetRobotsData(u, nil, context.Background())
		}()
	}
	wg.Wait()

	assert.Equal(t, 1, f.count("https://a.example.com/robots.txt"))
	assert.Equal(t, 1, f.count("https://b.example.com/robots.txt"))
}

// The same host on different ports is a different origin and needs its own fetch.
func TestGetRobotsData_PortIsPartOfCacheKey(t *testing.T) {
	f := &countingRobotsFetcher{body: "User-agent: *\n"}
	rh := newFetchingRobotsHandler(f)

	p1, _ := url.Parse("http://example.com:8080/x.html")
	p2, _ := url.Parse("http://example.com:9090/x.html")

	rh.GetRobotsData(p1, nil, context.Background())
	rh.GetRobotsData(p2, nil, context.Background())

	assert.Equal(t, 1, f.count("http://example.com:8080/robots.txt"))
	assert.Equal(t, 1, f.count("http://example.com:9090/robots.txt"))
}
