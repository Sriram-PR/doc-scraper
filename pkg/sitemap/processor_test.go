package sitemap

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"regexp"
	"sync"
	"testing"
	"time"

	"github.com/sirupsen/logrus"
	"golang.org/x/sync/semaphore"

	"github.com/Sriram-PR/doc-scraper/pkg/config"
	"github.com/Sriram-PR/doc-scraper/pkg/fetch"
	"github.com/Sriram-PR/doc-scraper/pkg/models"
	"github.com/Sriram-PR/doc-scraper/pkg/queue"
)

// --- Mock types ---

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

// mockPageStore implements storage.PageStore
type mockPageStore struct {
	mu      sync.Mutex
	visited map[string]bool
	err     error // if set, MarkPageVisited returns this error
}

func newMockPageStore() *mockPageStore {
	return &mockPageStore{visited: make(map[string]bool)}
}

func (m *mockPageStore) MarkPageVisited(normalizedPageURL string) (bool, error) {
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

// --- Helpers ---

func discardLogger() *logrus.Entry {
	l := logrus.New()
	l.SetOutput(io.Discard)
	return logrus.NewEntry(l)
}

func defaultSiteCfg() *config.SiteConfig {
	return &config.SiteConfig{
		AllowedDomain:     "example.com",
		AllowedPathPrefix: "/docs",
	}
}

func defaultAppCfg() *config.AppConfig {
	return &config.AppConfig{
		DefaultUserAgent:        "test-agent",
		DefaultDelayPerHost:     0,
		SemaphoreAcquireTimeout: 5 * time.Second,
	}
}

// newTestProcessor creates a SitemapProcessor wired for testing.
func newTestProcessor(
	t *testing.T,
	f *mockFetcher,
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
		sitemapQueue, pq, store, f, rl, sem,
		disallowed, siteCfg, defaultAppCfg(), log, &wg,
	)
	return sp, sitemapQueue, pq, &wg
}

// waitForPQLen polls the priority queue length until it reaches want or the timeout expires.
func waitForPQLen(t *testing.T, pq *queue.ThreadSafePriorityQueue, want int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if pq.Len() >= want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for PQ length %d, got %d", want, pq.Len())
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

// --- Tests ---

func TestMarkSitemapProcessed(t *testing.T) {
	store := newMockPageStore()
	f := newMockFetcher("")
	sp, _, _, _ := newTestProcessor(t, f, store, defaultSiteCfg(), nil)

	if !sp.MarkSitemapProcessed("http://example.com/sitemap.xml") {
		t.Fatal("first call should return true")
	}
	if sp.MarkSitemapProcessed("http://example.com/sitemap.xml") {
		t.Fatal("second call should return false")
	}
	if !sp.MarkSitemapProcessed("http://example.com/sitemap2.xml") {
		t.Fatal("different URL should return true")
	}
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

	if len(items) != 2 {
		t.Fatalf("expected 2 queued items, got %d", len(items))
	}
	if store.visitedCount() != 2 {
		t.Fatalf("expected 2 visited entries, got %d", store.visitedCount())
	}
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

	if len(items) != 1 {
		t.Fatalf("expected 1 queued item (disallowed filtered), got %d", len(items))
	}
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

	if sp.MarkSitemapProcessed("https://example.com/sitemap-pages.xml") {
		t.Fatal("sitemap-pages.xml should already be marked processed")
	}
	if sp.MarkSitemapProcessed("https://example.com/sitemap-blog.xml") {
		t.Fatal("sitemap-blog.xml should already be marked processed")
	}
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
	if len(items) != 0 {
		t.Fatalf("expected 0 queued items on fetch error, got %d", len(items))
	}
	if store.visitedCount() != 0 {
		t.Fatalf("expected 0 visited entries on fetch error, got %d", store.visitedCount())
	}
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
	if len(items) != 0 {
		t.Fatalf("expected 0 queued items for invalid XML, got %d", len(items))
	}
}

func TestProcessSitemapContextCancellation(t *testing.T) {
	store := newMockPageStore()
	f := newMockFetcher("<urlset></urlset>")
	sp, sitemapQueue, _, wg := newTestProcessor(t, f, store, defaultSiteCfg(), nil)

	ctx, cancel := context.WithCancel(context.Background())
	sp.Start(ctx)

	// Send a URL so the processor has something in-flight, then cancel
	wg.Add(1)
	sitemapQueue <- "https://example.com/sitemap.xml"

	// Give the processor a moment to pick up the item, then cancel
	time.Sleep(10 * time.Millisecond)
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
	if len(items) != 0 {
		t.Fatalf("expected 0 queued items when DB errors, got %d", len(items))
	}
	if store.visitedCount() != 0 {
		t.Fatalf("expected 0 visited entries when DB errors, got %d", store.visitedCount())
	}
}
