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
	if data, found := rh.lookupCache("example.com"); found || data != nil {
		t.Fatalf("expected miss on empty cache, got found=%v data=%v", found, data)
	}
}

func TestRobotsCache_SuccessCachedIndefinitely(t *testing.T) {
	rh := newTestRobotsHandler()
	data, err := robotstxt.FromBytes([]byte("User-agent: *\nDisallow: /private\n"))
	if err != nil {
		t.Fatalf("FromBytes: %v", err)
	}
	rh.cacheSuccess("example.com", data)

	got, found := rh.lookupCache("example.com")
	if !found || got != data {
		t.Fatalf("expected cached success entry, got found=%v", found)
	}
}

func TestRobotsCache_FailureLivesWithinTTL(t *testing.T) {
	rh := newTestRobotsHandler()
	rh.cacheFailure("example.com")

	data, found := rh.lookupCache("example.com")
	if !found {
		t.Fatal("expected fresh negative entry to be reported as present (fail-open)")
	}
	if data != nil {
		t.Fatalf("expected nil data for negative entry, got %v", data)
	}
}

func TestRobotsCache_FailureReattemptedAfterTTL(t *testing.T) {
	rh := newTestRobotsHandler()
	rh.robotsCache["example.com"] = robotsCacheEntry{expires: time.Now().Add(-time.Minute)}

	if _, found := rh.lookupCache("example.com"); found {
		t.Fatal("expected expired negative entry to be reported absent so it re-fetches")
	}
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
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

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

	if got := f.count("https://example.com/robots.txt"); got != 1 {
		t.Errorf("robots.txt fetched %d times, want 1", got)
	}
	for i, r := range results {
		if r == nil {
			t.Errorf("caller %d got nil robots data", i)
		}
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

	if got := f.count("https://example.com/robots.txt"); got != 1 {
		t.Errorf("robots.txt fetched %d times on 404, want 1", got)
	}

	// Subsequent calls are served by the negative cache, not refetched.
	rh.GetRobotsData(target, nil, context.Background())
	if got := f.count("https://example.com/robots.txt"); got != 1 {
		t.Errorf("robots.txt fetched %d times after negative caching, want 1", got)
	}
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

	if got := f.count("https://a.example.com/robots.txt"); got != 1 {
		t.Errorf("a.example.com fetched %d times, want 1", got)
	}
	if got := f.count("https://b.example.com/robots.txt"); got != 1 {
		t.Errorf("b.example.com fetched %d times, want 1", got)
	}
}

// The same host on different ports is a different origin and needs its own fetch.
func TestGetRobotsData_PortIsPartOfCacheKey(t *testing.T) {
	f := &countingRobotsFetcher{body: "User-agent: *\n"}
	rh := newFetchingRobotsHandler(f)

	p1, _ := url.Parse("http://example.com:8080/x.html")
	p2, _ := url.Parse("http://example.com:9090/x.html")

	rh.GetRobotsData(p1, nil, context.Background())
	rh.GetRobotsData(p2, nil, context.Background())

	if got := f.count("http://example.com:8080/robots.txt"); got != 1 {
		t.Errorf(":8080 fetched %d times, want 1", got)
	}
	if got := f.count("http://example.com:9090/robots.txt"); got != 1 {
		t.Errorf(":9090 fetched %d times, want 1", got)
	}
}
