package sitemap

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/semaphore"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/fetch"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/models"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/queue"
)

// mockFetcher implements fetch.HTTPFetcher.
// It returns a fresh response body on each call (so the body can be read multiple times).
type mockFetcher struct {
	body []byte // raw body bytes; a fresh io.NopCloser is created per call
	err  error
}

func newMockFetcher(body string) *mockFetcher {
	return &mockFetcher{body: []byte(body)}
}

func newErrFetcher(err error) *mockFetcher {
	return &mockFetcher{err: err}
}

func (m *mockFetcher) FetchWithRetry(_ *http.Request, _ context.Context) (*http.Response, error) {
	if m.err != nil {
		return nil, m.err
	}
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(bytes.NewReader(m.body)),
	}, nil
}

// routeFetcher returns a different body per exact request URL, so a sitemap
// index can point at many distinct nested sitemaps.
type routeFetcher struct {
	routes map[string]string
}

func (m *routeFetcher) FetchWithRetry(req *http.Request, _ context.Context) (*http.Response, error) {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(m.routes[req.URL.String()])),
	}, nil
}

// mockPageStore implements storage.PageStore
type mockPageStore struct {
	mu      sync.Mutex
	visited map[string]bool
	err     error // if set, MarkPageVisited returns this error
}

func newMockPageStore() *mockPageStore {
	return &mockPageStore{visited: make(map[string]bool)}
}

func (m *mockPageStore) MarkPageVisited(normalizedPageURL string, _ int) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return false, m.err
	}
	if m.visited[normalizedPageURL] {
		return false, nil
	}
	m.visited[normalizedPageURL] = true
	return true, nil
}

func (m *mockPageStore) CheckPageStatus(string) (models.PageStatus, *models.PageDBEntry, error) {
	return models.PageStatusUnset, nil, nil
}

func (m *mockPageStore) UpdatePageStatus(string, *models.PageDBEntry) error { return nil }

func (m *mockPageStore) GetPageContentHash(string) (string, bool, error) { return "", false, nil }

func (m *mockPageStore) visitedCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.visited)
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func defaultSiteCfg() *config.SiteConfig {
	return &config.SiteConfig{
		AllowedDomain:     "example.com",
		AllowedPathPrefix: "/docs",
	}
}

func defaultAppCfg() *config.AppConfig {
	return &config.AppConfig{
		DefaultUserAgent:    "test-agent",
		DefaultDelayPerHost: 0,
	}
}

// newTestProcessor creates a SitemapProcessor wired for testing.
func newTestProcessor(
	t *testing.T,
	f fetch.HTTPFetcher,
	store *mockPageStore,
	siteCfg *config.SiteConfig,
	disallowed []*regexp.Regexp,
) (*SitemapProcessor, chan string, *queue.ThreadSafePriorityQueue, *sync.WaitGroup) {
	t.Helper()
	log := discardLogger()
	pq := queue.NewThreadSafePriorityQueue(log)
	rl := fetch.NewRateLimiter(0, log)
	sem := semaphore.NewWeighted(10)
	sitemapQueue := make(chan string, 10)
	var wg sync.WaitGroup

	sp := NewSitemapProcessor(
		sitemapQueue, pq, store, f, rl, sem, fetch.NewHostSemaphorePool(10, log),
		disallowed, siteCfg, defaultAppCfg(), log, &wg,
	)
	return sp, sitemapQueue, pq, &wg
}

// waitForPQLen polls the priority queue length until it reaches want or the timeout expires.
func waitForPQLen(t *testing.T, pq *queue.ThreadSafePriorityQueue, want int, timeout time.Duration) {
	t.Helper()
	require.Eventuallyf(t, func() bool { return pq.Len() >= want }, timeout, 5*time.Millisecond,
		"timed out waiting for PQ length %d", want)
}

// drainPQ closes the queue and pops all items.
func drainPQ(pq *queue.ThreadSafePriorityQueue) []*models.WorkItem {
	pq.Close()
	var items []*models.WorkItem
	for {
		item, ok := pq.Pop()
		if !ok {
			break
		}
		items = append(items, item)
	}
	return items
}

// drainPQAndBalance drains the PQ and calls wg.Done() for each item to
// balance the wg.Add(1) the processor did when enqueueing pages.
func drainPQAndBalance(pq *queue.ThreadSafePriorityQueue, wg *sync.WaitGroup) []*models.WorkItem {
	items := drainPQ(pq)
	for range items {
		wg.Done()
	}
	return items
}

func TestMarkSitemapProcessed(t *testing.T) {
	store := newMockPageStore()
	f := newMockFetcher("")
	sp, _, _, _ := newTestProcessor(t, f, store, defaultSiteCfg(), nil)

	assert.True(t, sp.MarkSitemapProcessed("http://example.com/sitemap.xml"), "first call")
	assert.False(t, sp.MarkSitemapProcessed("http://example.com/sitemap.xml"), "second call")
	assert.True(t, sp.MarkSitemapProcessed("http://example.com/sitemap2.xml"), "different URL")
}

func TestProcessURLSet(t *testing.T) {
	xmlBody := `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>https://example.com/docs/page1</loc></url>
  <url><loc>https://example.com/docs/page2</loc><lastmod>2024-01-01</lastmod></url>
  <url><loc>https://other.com/docs/page3</loc></url>
</urlset>`

	store := newMockPageStore()
	f := newMockFetcher(xmlBody)
	sp, sitemapQueue, pq, wg := newTestProcessor(t, f, store, defaultSiteCfg(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sp.Start(ctx)

	wg.Add(1)
	sitemapQueue <- "https://example.com/sitemap.xml"

	// Wait for the 2 in-scope URLs to appear in the PQ
	waitForPQLen(t, pq, 2, 5*time.Second)

	// Drain PQ, balancing the wg.Add(1) per page, then wait for sitemap goroutine
	items := drainPQAndBalance(pq, wg)
	wg.Wait()

	assert.Len(t, items, 2)
	assert.Equal(t, 2, store.visitedCount())
}

func TestProcessURLSetDisallowedPath(t *testing.T) {
	xmlBody := `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>https://example.com/docs/page1</loc></url>
  <url><loc>https://example.com/docs/internal/secret</loc></url>
</urlset>`

	disallowed := []*regexp.Regexp{regexp.MustCompile(`/internal/`)}
	store := newMockPageStore()
	f := newMockFetcher(xmlBody)
	sp, sitemapQueue, pq, wg := newTestProcessor(t, f, store, defaultSiteCfg(), disallowed)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sp.Start(ctx)

	wg.Add(1)
	sitemapQueue <- "https://example.com/sitemap.xml"

	waitForPQLen(t, pq, 1, 5*time.Second)
	items := drainPQAndBalance(pq, wg)
	wg.Wait()

	assert.Len(t, items, 1, "disallowed URL must be filtered")
}

func TestProcessSitemapIndex(t *testing.T) {
	xmlBody := `<?xml version="1.0" encoding="UTF-8"?>
<sitemapindex xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <sitemap><loc>https://example.com/sitemap-pages.xml</loc></sitemap>
  <sitemap><loc>https://example.com/sitemap-blog.xml</loc></sitemap>
</sitemapindex>`

	store := newMockPageStore()
	f := newMockFetcher(xmlBody)
	sp, sitemapQueue, _, wg := newTestProcessor(t, f, store, defaultSiteCfg(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sp.Start(ctx)

	// Send the index sitemap
	wg.Add(1)
	sitemapQueue <- "https://example.com/sitemap_index.xml"

	// The index processing queues 2 nested sitemaps (wg.Add(1) each).
	// Each nested sitemap goroutine fetches the same index XML, finds
	// both nested sitemaps already marked processed, then tries URL set
	// parse (fails on index XML), and returns with wg.Done().
	// So wg naturally balances to 0.
	wg.Wait()

	assert.False(t, sp.MarkSitemapProcessed("https://example.com/sitemap-pages.xml"), "sitemap-pages.xml should already be marked processed")
	assert.False(t, sp.MarkSitemapProcessed("https://example.com/sitemap-blog.xml"), "sitemap-blog.xml should already be marked processed")
}

func TestProcessSitemapFetchError(t *testing.T) {
	store := newMockPageStore()
	f := newErrFetcher(errors.New("connection refused"))
	sp, sitemapQueue, pq, wg := newTestProcessor(t, f, store, defaultSiteCfg(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sp.Start(ctx)

	wg.Add(1)
	sitemapQueue <- "https://example.com/sitemap.xml"

	wg.Wait()

	items := drainPQ(pq)
	assert.Empty(t, items)
	assert.Equal(t, 0, store.visitedCount())
}

func TestProcessSitemapInvalidXML(t *testing.T) {
	store := newMockPageStore()
	f := newMockFetcher("this is not XML at all")
	sp, sitemapQueue, pq, wg := newTestProcessor(t, f, store, defaultSiteCfg(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sp.Start(ctx)

	wg.Add(1)
	sitemapQueue <- "https://example.com/sitemap.xml"

	wg.Wait()

	items := drainPQ(pq)
	assert.Empty(t, items)
}

// blockingFetcher reports when a fetch starts and holds it until the request
// context is cancelled.
type blockingFetcher struct {
	started chan struct{}
	once    sync.Once
}

func (f *blockingFetcher) FetchWithRetry(_ *http.Request, ctx context.Context) (*http.Response, error) {
	f.once.Do(func() { close(f.started) })
	<-ctx.Done()
	return nil, ctx.Err()
}

func TestProcessSitemapContextCancellation(t *testing.T) {
	store := newMockPageStore()
	f := &blockingFetcher{started: make(chan struct{})}
	sp, sitemapQueue, _, wg := newTestProcessor(t, f, store, defaultSiteCfg(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	sp.Start(ctx)

	wg.Add(1)
	sitemapQueue <- "https://example.com/sitemap.xml"

	select {
	case <-f.started:
	case <-time.After(5 * time.Second):
		t.Fatal("sitemap fetch never started")
	}
	cancel()

	done := make(chan struct{})
	go func() {
		wg.Wait()
		close(done)
	}()

	select {
	case <-done:
		// success - processor exited gracefully
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for processor to exit after context cancellation")
	}
}

func TestProcessURLSetDBError(t *testing.T) {
	xmlBody := `<?xml version="1.0" encoding="UTF-8"?>
<urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9">
  <url><loc>https://example.com/docs/page1</loc></url>
  <url><loc>https://example.com/docs/page2</loc></url>
  <url><loc>https://example.com/docs/page3</loc></url>
</urlset>`

	store := newMockPageStore()
	store.err = errors.New("db write failed")
	f := newMockFetcher(xmlBody)
	sp, sitemapQueue, pq, wg := newTestProcessor(t, f, store, defaultSiteCfg(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sp.Start(ctx)

	wg.Add(1)
	sitemapQueue <- "https://example.com/sitemap.xml"

	// All DB writes fail, so no pages are added to PQ and wg balances to 0
	wg.Wait()

	items := drainPQ(pq)
	assert.Empty(t, items)
	assert.Equal(t, 0, store.visitedCount())
}

func TestProcessSitemapIndexFanOut(t *testing.T) {
	const n = 200
	const ns = `xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"`

	routes := map[string]string{}
	var idx strings.Builder
	fmt.Fprintf(&idx, `<?xml version="1.0" encoding="UTF-8"?><sitemapindex %s>`, ns)
	for i := range n {
		nested := fmt.Sprintf("https://example.com/docs/sitemap-%d.xml", i)
		page := fmt.Sprintf("https://example.com/docs/page-%d", i)
		fmt.Fprintf(&idx, `<sitemap><loc>%s</loc></sitemap>`, nested)
		routes[nested] = fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?><urlset %s><url><loc>%s</loc></url></urlset>`, ns, page)
	}
	idx.WriteString(`</sitemapindex>`)
	indexURL := "https://example.com/sitemap_index.xml"
	routes[indexURL] = idx.String()

	store := newMockPageStore()
	log := discardLogger()
	pq := queue.NewThreadSafePriorityQueue(log)
	rl := fetch.NewRateLimiter(0, log)
	sem := semaphore.NewWeighted(8)
	sitemapQueue := make(chan string, 8)
	var wg sync.WaitGroup
	appCfg := defaultAppCfg()
	appCfg.MaxRequests = 8 // exercise the bounded worker pool
	sp := NewSitemapProcessor(sitemapQueue, pq, store, &routeFetcher{routes}, rl, sem, fetch.NewHostSemaphorePool(8, log), nil, defaultSiteCfg(), appCfg, log, &wg)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sp.Start(ctx)

	wg.Add(1)
	sitemapQueue <- indexURL

	// Every one of the n nested sitemaps must be fetched and its page queued;
	// none may be dropped despite the large fan-out through the bounded pool.
	waitForPQLen(t, pq, n, 10*time.Second)
	items := drainPQAndBalance(pq, &wg)
	wg.Wait()

	assert.Len(t, items, n)
	assert.Equal(t, n, store.visitedCount())
}

// hostConcurrencyFetcher records the peak number of simultaneous in-flight
// requests per host, holding each request open long enough to overlap.
type hostConcurrencyFetcher struct {
	routes map[string]string
	hold   time.Duration

	mu      sync.Mutex
	active  map[string]int
	peak    map[string]int
	fetched int
}

func newHostConcurrencyFetcher(routes map[string]string, hold time.Duration) *hostConcurrencyFetcher {
	return &hostConcurrencyFetcher{
		routes: routes,
		hold:   hold,
		active: make(map[string]int),
		peak:   make(map[string]int),
	}
}

func (m *hostConcurrencyFetcher) FetchWithRetry(req *http.Request, _ context.Context) (*http.Response, error) {
	host := req.URL.Hostname()
	m.mu.Lock()
	m.active[host]++
	m.fetched++
	if m.active[host] > m.peak[host] {
		m.peak[host] = m.active[host]
	}
	m.mu.Unlock()

	time.Sleep(m.hold)

	m.mu.Lock()
	m.active[host]--
	m.mu.Unlock()

	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(m.routes[req.URL.String()])),
	}, nil
}

func (m *hostConcurrencyFetcher) peakFor(host string) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.peak[host]
}

// sitemapIndexOver builds a sitemap index pointing at one nested sitemap per
// entry, each nested sitemap holding a single in-scope page.
func sitemapIndexOver(hosts []string, perHost int) (map[string]string, []string) {
	const ns = `xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"`
	routes := map[string]string{}
	indexURLs := make([]string, 0, len(hosts))
	for _, host := range hosts {
		var idx strings.Builder
		fmt.Fprintf(&idx, `<?xml version="1.0" encoding="UTF-8"?><sitemapindex %s>`, ns)
		for i := range perHost {
			nested := fmt.Sprintf("https://%s/docs/sitemap-%d.xml", host, i)
			page := fmt.Sprintf("https://example.com/docs/%s-page-%d", host, i)
			fmt.Fprintf(&idx, `<sitemap><loc>%s</loc></sitemap>`, nested)
			routes[nested] = fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?><urlset %s><url><loc>%s</loc></url></urlset>`, ns, page)
		}
		idx.WriteString(`</sitemapindex>`)
		indexURL := fmt.Sprintf("https://%s/sitemap_index.xml", host)
		routes[indexURL] = idx.String()
		indexURLs = append(indexURLs, indexURL)
	}
	return routes, indexURLs
}

func startConcurrencyProcessor(t *testing.T, f fetch.HTTPFetcher, workers, perHost int) (*queue.ThreadSafePriorityQueue, chan string, *sync.WaitGroup, context.CancelFunc) {
	t.Helper()
	log := discardLogger()
	pq := queue.NewThreadSafePriorityQueue(log)
	rl := fetch.NewRateLimiter(0, log)
	sitemapQueue := make(chan string, 8)
	var wg sync.WaitGroup
	appCfg := defaultAppCfg()
	appCfg.MaxRequests = workers

	sp := NewSitemapProcessor(
		sitemapQueue, pq, newMockPageStore(), f, rl,
		semaphore.NewWeighted(int64(workers)), fetch.NewHostSemaphorePool(perHost, log),
		nil, defaultSiteCfg(), appCfg, log, &wg,
	)
	ctx, cancel := context.WithCancel(context.Background())
	sp.Start(ctx)
	return pq, sitemapQueue, &wg, cancel
}

func TestProcessSitemapRespectsPerHostLimit(t *testing.T) {
	const perHost = 2
	const nested = 24
	routes, indexURLs := sitemapIndexOver([]string{"example.com"}, nested)
	f := newHostConcurrencyFetcher(routes, 15*time.Millisecond)

	pq, sitemapQueue, wg, cancel := startConcurrencyProcessor(t, f, 8, perHost)
	defer cancel()

	wg.Add(1)
	sitemapQueue <- indexURLs[0]

	waitForPQLen(t, pq, nested, 20*time.Second)
	drainPQAndBalance(pq, wg)
	wg.Wait()

	assert.LessOrEqual(t, f.peakFor("example.com"), perHost, "sitemap fetches exceeded max_requests_per_host")
}

func TestProcessSitemapDistinctHostsNotSerialized(t *testing.T) {
	const perHost = 2
	const nested = 12
	hosts := []string{"a.example.com", "b.example.com"}
	routes, indexURLs := sitemapIndexOver(hosts, nested)
	f := newHostConcurrencyFetcher(routes, 15*time.Millisecond)

	pq, sitemapQueue, wg, cancel := startConcurrencyProcessor(t, f, 8, perHost)
	defer cancel()

	for _, u := range indexURLs {
		wg.Add(1)
		sitemapQueue <- u
	}

	waitForPQLen(t, pq, nested*len(hosts), 20*time.Second)
	drainPQAndBalance(pq, wg)
	wg.Wait()

	for _, host := range hosts {
		assert.LessOrEqual(t, f.peakFor(host), perHost, "host %s exceeded per-host limit", host)
		assert.GreaterOrEqual(t, f.peakFor(host), 2, "host %s never reached its own limit; hosts appear serialized", host)
	}
}

func TestProbe_SkipsNonSitemapsAndProcessesFirstIndex(t *testing.T) {
	const ns = `xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"`
	routes := map[string]string{
		"https://example.com/sitemap.xml":        `<html><body>Soft 404</body></html>`,
		"https://example.com/sitemap_index.xml":  `<?xml version="1.0"?><urlset ` + ns + `></urlset>`,
		"https://example.com/docs/sitemap.xml":   `<?xml version="1.0"?><sitemapindex ` + ns + `><sitemap><loc>https://example.com/docs/sitemap-0.xml</loc></sitemap></sitemapindex>`,
		"https://example.com/docs/sitemap-0.xml": `<?xml version="1.0"?><urlset ` + ns + `><url><loc>https://example.com/docs/page</loc></url></urlset>`,
	}
	store := newMockPageStore()
	sp, _, pq, wg := newTestProcessor(t, &routeFetcher{routes}, store, defaultSiteCfg(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sp.Start(ctx)

	got, ok := sp.Probe(ctx, []string{
		"https://example.com/sitemap.xml",
		"https://example.com/sitemap-index.xml", // empty body
		"https://example.com/sitemap_index.xml", // empty urlset
		"https://example.com/docs/sitemap.xml",
		"https://example.com/docs/sitemap_index.xml",
	})
	require.True(t, ok)
	assert.Equal(t, "https://example.com/docs/sitemap.xml", got)

	waitForPQLen(t, pq, 1, 5*time.Second)
	items := drainPQAndBalance(pq, wg)
	wg.Wait()
	require.Len(t, items, 1)
	assert.Equal(t, "https://example.com/docs/page", items[0].URL)
	assert.True(t, sp.MarkSitemapProcessed("https://example.com/docs/sitemap_index.xml"), "probing must stop at the first sitemap")
}

func TestProbe_NoSitemapFound(t *testing.T) {
	sp, _, pq, _ := newTestProcessor(t, &routeFetcher{map[string]string{}}, newMockPageStore(), defaultSiteCfg(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sp.Start(ctx)

	_, ok := sp.Probe(ctx, []string{"https://example.com/sitemap.xml"})
	assert.False(t, ok)
	assert.Equal(t, 0, pq.Len())
}

func TestProbe_StopsOnCancelledContext(t *testing.T) {
	routes := map[string]string{
		"https://example.com/sitemap.xml": `<?xml version="1.0"?><urlset xmlns="http://www.sitemaps.org/schemas/sitemap/0.9"><url><loc>https://example.com/docs/page</loc></url></urlset>`,
	}
	sp, _, pq, _ := newTestProcessor(t, &routeFetcher{routes}, newMockPageStore(), defaultSiteCfg(), nil)
	ctx, cancel := context.WithCancel(context.Background())
	sp.Start(ctx)
	cancel()

	_, ok := sp.Probe(ctx, []string{"https://example.com/sitemap.xml"})
	assert.False(t, ok)
	assert.Equal(t, 0, pq.Len())
}
