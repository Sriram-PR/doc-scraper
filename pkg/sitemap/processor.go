package sitemap

import (
	"bufio"
	"compress/gzip"
	"context"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"

	"golang.org/x/sync/semaphore"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/fetch"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/models"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/parse"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/queue"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/storage"
)

const (
	// maxSitemapIndexDepth stops at an index this many levels below the first
	// sitemap, so the deepest chain followed is root index, index, urlset.
	maxSitemapIndexDepth = 2
	maxSitemapsPerSite   = 1000
)

// sitemapTask is one pending sitemap and how many index levels sit above it.
type sitemapTask struct {
	url   string
	depth int
}

// SitemapProcessor handles fetching, parsing, and processing sitemaps.
type SitemapProcessor struct {
	sitemapQueue               chan string
	pq                         *queue.ThreadSafePriorityQueue
	store                      storage.PageStore
	fetcher                    fetch.HTTPFetcher
	rateLimiter                *fetch.RateLimiter
	globalSemaphore            *semaphore.Weighted
	hostSemPool                *fetch.HostSemaphorePool
	compiledDisallowedPatterns []*regexp.Regexp
	siteCfg                    *config.SiteConfig
	appCfg                     *config.AppConfig
	log                        *slog.Logger
	wg                         *sync.WaitGroup // main crawler WaitGroup
	sitemapsProcessed          map[string]bool
	sitemapsProcessedMu        sync.Mutex
	sitemapLimitLogged         bool
	backlog                    *sitemapBacklog
}

func NewSitemapProcessor(
	sitemapQueue chan string,
	pq *queue.ThreadSafePriorityQueue,
	store storage.PageStore,
	fetcher fetch.HTTPFetcher,
	rateLimiter *fetch.RateLimiter,
	globalSemaphore *semaphore.Weighted,
	hostSemPool *fetch.HostSemaphorePool,
	compiledDisallowedPatterns []*regexp.Regexp,
	siteCfg *config.SiteConfig,
	appCfg *config.AppConfig,
	log *slog.Logger,
	wg *sync.WaitGroup,
) *SitemapProcessor {
	return &SitemapProcessor{
		sitemapQueue:               sitemapQueue,
		pq:                         pq,
		store:                      store,
		fetcher:                    fetcher,
		rateLimiter:                rateLimiter,
		globalSemaphore:            globalSemaphore,
		hostSemPool:                hostSemPool,
		compiledDisallowedPatterns: compiledDisallowedPatterns,
		siteCfg:                    siteCfg,
		appCfg:                     appCfg,
		log:                        log.With("component", "sitemap_processor"),
		wg:                         wg,
		sitemapsProcessed:          make(map[string]bool),
		backlog:                    newSitemapBacklog(),
	}
}

// Start runs the sitemap processing loop in a goroutine.
func (sp *SitemapProcessor) Start(ctx context.Context) {
	sp.log.Info("Sitemap processing goroutine starting.")
	go sp.run(ctx)
}

// MarkSitemapProcessed marks a sitemap URL as queued. It returns true if newly
// marked, and false once maxSitemapsPerSite have been marked.
func (sp *SitemapProcessor) MarkSitemapProcessed(sitemapURL string) bool {
	sp.sitemapsProcessedMu.Lock()
	defer sp.sitemapsProcessedMu.Unlock()
	if sp.sitemapsProcessed[sitemapURL] {
		return false
	}
	if len(sp.sitemapsProcessed) >= maxSitemapsPerSite {
		if !sp.sitemapLimitLogged {
			sp.sitemapLimitLogged = true
			sp.log.Warn(fmt.Sprintf("Sitemap limit reached; not fetching %s or any later sitemap", sitemapURL), "limit", maxSitemapsPerSite)
		}
		return false
	}
	sp.sitemapsProcessed[sitemapURL] = true
	return true
}

// sitemapBacklog is an unbounded FIFO of pending sitemap URLs. It decouples
// producers (the ingress pump and workers that discover nested sitemaps) from
// the bounded worker pool: a push never blocks, so a huge sitemap index can
// neither spawn unbounded goroutines nor deadlock workers that are trying to
// enqueue nested sitemaps back through a full channel. pop keeps returning
// buffered items after close so every WaitGroup token added on push is matched
// by a Done.
type sitemapBacklog struct {
	mu     sync.Mutex
	cond   *sync.Cond
	items  []sitemapTask
	head   int
	closed bool
}

func newSitemapBacklog() *sitemapBacklog {
	b := &sitemapBacklog{}
	b.cond = sync.NewCond(&b.mu)
	return b
}

func (b *sitemapBacklog) push(t sitemapTask) {
	b.mu.Lock()
	b.items = append(b.items, t)
	b.mu.Unlock()
	b.cond.Signal()
}

// pop blocks until an item is available, returning ok=false only once the
// backlog is both closed and fully drained.
func (b *sitemapBacklog) pop() (sitemapTask, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for b.head == len(b.items) && !b.closed {
		b.cond.Wait()
	}
	if b.head == len(b.items) {
		return sitemapTask{}, false
	}
	task := b.items[b.head]
	b.items[b.head] = sitemapTask{}
	b.head++
	if b.head > 1024 && b.head*2 >= len(b.items) {
		b.items = append(b.items[:0], b.items[b.head:]...)
		b.head = 0
	}
	return task, true
}

func (b *sitemapBacklog) close() {
	b.mu.Lock()
	b.closed = true
	b.mu.Unlock()
	b.cond.Broadcast()
}

func (sp *SitemapProcessor) workerCount() int {
	if sp.appCfg.MaxRequests < 1 {
		return 1
	}
	return sp.appCfg.MaxRequests
}

// run drains the ingress queue into an unbounded backlog and processes it with
// a bounded pool of workers, so the number of processing goroutines stays
// bounded regardless of sitemap-index size. It returns once the ingress ends
// (queue closed or context cancelled) and the backlog is fully drained.
func (sp *SitemapProcessor) run(ctx context.Context) {
	backlog := sp.backlog

	go func() {
		for {
			select {
			case <-ctx.Done():
				sp.log.Warn(fmt.Sprintf("Context cancelled, stopping sitemap ingress: %v", ctx.Err()))
				backlog.close()
				return
			case sitemapURL, ok := <-sp.sitemapQueue:
				if !ok {
					sp.log.Info("Sitemap queue channel closed.")
					backlog.close()
					return
				}
				backlog.push(sitemapTask{url: sitemapURL})
			}
		}
	}()

	var workers sync.WaitGroup
	for range sp.workerCount() {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				task, ok := backlog.pop()
				if !ok {
					return
				}
				sp.processOne(ctx, task, backlog)
			}
		}()
	}

	workers.Wait()
	sp.log.Info("Sitemap processing finished; all workers exited.")
}

// processOne runs one sitemap task, always balancing the WaitGroup token added
// when the sitemap was enqueued and recovering from panics so a single bad
// sitemap cannot take down a worker.
func (sp *SitemapProcessor) processOne(ctx context.Context, task sitemapTask, backlog *sitemapBacklog) {
	defer sp.wg.Done()
	defer sp.recoverPanic(task.url)
	sp.processSitemap(ctx, task, backlog)
}

func (sp *SitemapProcessor) recoverPanic(smURL string) {
	if r := recover(); r != nil {
		sp.log.Error("PANIC Recovered in sitemap processing goroutine",
			"sitemap_url", smURL,
			"panic_info", r,
			"stack_trace", string(debug.Stack()),
		)
	}
}

// processSitemap fetches one sitemap and dispatches on whether it is a
// sitemap index or a URL set.
func (sp *SitemapProcessor) processSitemap(ctx context.Context, task sitemapTask, backlog *sitemapBacklog) {
	smURL := task.url
	sitemapLog := sp.log.With("sitemap_url", smURL)
	sitemapLog.Info("Processing sitemap")

	sitemapBytes, err := sp.fetchSitemap(ctx, smURL, sitemapLog)
	if err != nil {
		if !errors.Is(err, errNoSlot) && !errors.Is(err, errOffHost) {
			sitemapLog.Error(fmt.Sprintf("Fetch failed: %v", err))
		}
		return
	}

	var index parse.XMLSitemapIndex
	errIndex := xml.Unmarshal(sitemapBytes, &index)
	if errIndex == nil && len(index.Sitemaps) > 0 {
		sp.handleSitemapIndex(index, task.depth, backlog, sitemapLog)
		return
	}
	var urlSet parse.XMLURLSet
	if errURLSet := xml.Unmarshal(sitemapBytes, &urlSet); errURLSet != nil {
		// Only surface an error if it also failed to parse as an index.
		if errIndex != nil {
			sitemapLog.Error(fmt.Sprintf("Failed parse XML (Index err=%v; URLSet err=%v)", errIndex, errURLSet))
		} else {
			sitemapLog.Warn(fmt.Sprintf("Content was not a valid Sitemap Index or URL Set (URLSet err=%v)", errURLSet))
		}
		return
	}
	sp.handleURLSet(urlSet, sitemapLog)
}

// Probe tries candidates in order and processes the first one that is a
// non-empty sitemap, reporting its URL. Misses are expected, so they are
// logged at debug level. Run it while the processor is started and before
// the sitemap queue is closed.
func (sp *SitemapProcessor) Probe(ctx context.Context, candidates []string) (string, bool) {
	for _, smURL := range candidates {
		if ctx.Err() != nil {
			return "", false
		}
		if !sp.MarkSitemapProcessed(smURL) {
			continue
		}
		probeLog := sp.log.With("sitemap_url", smURL)
		sitemapBytes, err := sp.fetchSitemap(ctx, smURL, probeLog)
		if err != nil {
			probeLog.Debug(fmt.Sprintf("Sitemap probe missed: %v", err))
			continue
		}
		var index parse.XMLSitemapIndex
		if xml.Unmarshal(sitemapBytes, &index) == nil && len(index.Sitemaps) > 0 {
			probeLog.Info("Found sitemap by probing")
			sp.handleSitemapIndex(index, 0, sp.backlog, probeLog)
			return smURL, true
		}
		var urlSet parse.XMLURLSet
		if xml.Unmarshal(sitemapBytes, &urlSet) == nil && len(urlSet.URLs) > 0 {
			probeLog.Info("Found sitemap by probing")
			sp.handleURLSet(urlSet, probeLog)
			return smURL, true
		}
		probeLog.Debug("Sitemap probe missed: not a non-empty sitemap")
	}
	return "", false
}

func (sp *SitemapProcessor) acquireHostSlot(ctx context.Context, host string, sitemapLog *slog.Logger) bool {
	ctxH, cancelH := context.WithTimeout(ctx, config.DefaultSemaphoreAcquireTimeout)
	err := sp.hostSemPool.Acquire(ctxH, host)
	cancelH()
	if err == nil {
		return true
	}
	logSlotFailure(ctx, sitemapLog, "HOST", err)
	return false
}

func (sp *SitemapProcessor) acquireGlobalSlot(ctx context.Context, sitemapLog *slog.Logger) bool {
	ctxG, cancelG := context.WithTimeout(ctx, config.DefaultSemaphoreAcquireTimeout)
	err := sp.globalSemaphore.Acquire(ctxG, 1)
	cancelG()
	if err == nil {
		return true
	}
	logSlotFailure(ctx, sitemapLog, "GLOBAL", err)
	return false
}

// logSlotFailure reports a failed semaphore acquisition, demoting the ordinary
// shutdown case to a warning so cancelling a crawl does not log errors.
func logSlotFailure(ctx context.Context, sitemapLog *slog.Logger, which string, err error) {
	switch {
	case errors.Is(err, context.DeadlineExceeded) && ctx.Err() != nil:
		sitemapLog.Warn(fmt.Sprintf("Could not acquire %s semaphore due to main context cancellation: %v", which, ctx.Err()))
	case errors.Is(err, context.DeadlineExceeded):
		sitemapLog.Error(fmt.Sprintf("Timeout acquiring %s semaphore: %v", which, err))
	default:
		sitemapLog.Error(fmt.Sprintf("Error acquiring %s semaphore: %v", which, err))
	}
}

// errNoSlot means a request slot could not be acquired; the cause is already
// logged.
var errNoSlot = errors.New("no request slot")

// errOffHost means the sitemap is not on the crawled site; the cause is
// already logged.
var errOffHost = errors.New("sitemap is not on the crawled site")

// fetchSitemap acquires the per-host and global request slots and reads the
// sitemap body, capped at 10 MB. Sitemaps off the allowed domain, before or
// after redirects, are refused.
func (sp *SitemapProcessor) fetchSitemap(ctx context.Context, smURL string, sitemapLog *slog.Logger) ([]byte, error) {
	parsedSitemapURL, err := url.Parse(smURL)
	if err != nil {
		return nil, fmt.Errorf("parse URL: %w", err)
	}
	sitemapHost := parsedSitemapURL.Hostname()
	if !parse.SameHost(sitemapHost, sp.siteCfg.AllowedDomain) {
		sitemapLog.Warn(fmt.Sprintf("Skipping sitemap on %s: only sitemaps on %s are fetched", sitemapHost, sp.siteCfg.AllowedDomain))
		return nil, errOffHost
	}

	// Host slot first, then global, matching the page, image, and robots.txt
	// fetch paths. Without it the sitemap pool issues up to max_requests
	// concurrent requests to a single host regardless of max_requests_per_host.
	if sp.hostSemPool != nil {
		if !sp.acquireHostSlot(ctx, sitemapHost, sitemapLog) {
			return nil, errNoSlot
		}
		defer sp.hostSemPool.Release(sitemapHost)
	}

	if !sp.acquireGlobalSlot(ctx, sitemapLog) {
		return nil, errNoSlot
	}
	defer sp.globalSemaphore.Release(1)

	sp.rateLimiter.ApplyDelay(ctx, sitemapHost, sp.appCfg.DefaultDelayPerHost)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, smURL, nil)
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	req.Header.Set("User-Agent", sp.appCfg.DefaultUserAgent)

	resp, err := sp.fetcher.FetchWithRetry(req, ctx)
	if err != nil {
		if resp != nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		return nil, err
	}
	defer resp.Body.Close()
	if resp.Request != nil && resp.Request.URL != nil && !parse.SameHost(resp.Request.URL.Hostname(), sp.siteCfg.AllowedDomain) {
		sitemapLog.Warn(fmt.Sprintf("Skipping sitemap redirected to %s: only sitemaps on %s are fetched", resp.Request.URL.Hostname(), sp.siteCfg.AllowedDomain))
		return nil, errOffHost
	}

	return readSitemapBody(resp.Body)
}

// maxSitemapSize caps how much sitemap data is read, measured after
// decompression for gzipped sitemaps.
const maxSitemapSize = 10 * 1024 * 1024 // 10 MB

// readSitemapBody reads a sitemap body, capped at maxSitemapSize. A body that
// starts with the gzip magic bytes (a .xml.gz file served without
// Content-Encoding, which the HTTP client does not decode) is decompressed
// first, and the cap applies to the decompressed bytes.
func readSitemapBody(body io.Reader) ([]byte, error) {
	br := bufio.NewReader(io.LimitReader(body, maxSitemapSize))
	var r io.Reader = br
	if magic, err := br.Peek(2); err == nil && magic[0] == 0x1f && magic[1] == 0x8b {
		zr, err := gzip.NewReader(br)
		if err != nil {
			return nil, fmt.Errorf("open gzip body: %w", err)
		}
		defer func() { _ = zr.Close() }()
		r = zr
	}
	sitemapBytes, err := io.ReadAll(io.LimitReader(r, maxSitemapSize))
	if err != nil {
		return nil, fmt.Errorf("read body: %w", err)
	}
	return sitemapBytes, nil
}

// handleSitemapIndex queues each valid, not-yet-seen nested sitemap of an
// index found at depth onto the backlog for the worker pool to process.
func (sp *SitemapProcessor) handleSitemapIndex(index parse.XMLSitemapIndex, depth int, backlog *sitemapBacklog, sitemapLog *slog.Logger) {
	sitemapLog.Info(fmt.Sprintf("Parsed as Sitemap Index, found %d references.", len(index.Sitemaps)))
	if depth >= maxSitemapIndexDepth {
		sitemapLog.Warn(fmt.Sprintf("Sitemap index nested too deep; skipping its %d sitemaps", len(index.Sitemaps)), "limit", maxSitemapIndexDepth)
		return
	}
	queuedCount := 0
	for _, sitemapEntry := range index.Sitemaps {
		nestedSmURL := sitemapEntry.Loc
		nestedSmLog := sitemapLog.With("nested_sitemap", nestedSmURL)
		if _, nestedErr := url.ParseRequestURI(nestedSmURL); nestedErr != nil {
			nestedSmLog.Warn(fmt.Sprintf("Invalid nested sitemap URL: %v", nestedErr))
			continue
		}
		if !sp.MarkSitemapProcessed(nestedSmURL) {
			nestedSmLog.Debug(fmt.Sprintf("Nested sitemap already processed/queued: %s", nestedSmURL))
			continue
		}
		sp.wg.Add(1)
		backlog.push(sitemapTask{url: nestedSmURL, depth: depth + 1})
		nestedSmLog.Debug("Queued nested sitemap.")
		queuedCount++
	}
	sitemapLog.Info(fmt.Sprintf("Queued %d nested sitemaps.", queuedCount))
}

// handleURLSet enqueues every in-scope, new page URL of a <urlset>.
// Sitemap URLs have no link-graph depth; they sit one hop from the root so
// max_depth still bounds them and max_depth=1 stays start-only.
const sitemapURLDepth = 1

func (sp *SitemapProcessor) handleURLSet(urlSet parse.XMLURLSet, sitemapLog *slog.Logger) {
	sitemapLog.Info(fmt.Sprintf("Parsed as URL Set, found %d URLs.", len(urlSet.URLs)))
	if sp.siteCfg.MaxDepth > 0 && sitemapURLDepth >= sp.siteCfg.MaxDepth {
		sitemapLog.Info(fmt.Sprintf("max_depth %d keeps the crawl to start pages; not queueing sitemap URLs.", sp.siteCfg.MaxDepth))
		return
	}
	queuedCount := 0
	dbErrorCount := 0
	for _, urlEntry := range urlSet.URLs {
		pageURL := urlEntry.Loc
		if urlEntry.LastMod != "" {
			sitemapLog.Debug(fmt.Sprintf("Found URL: %s (LastMod: %s)", pageURL, urlEntry.LastMod))
		} else {
			sitemapLog.Debug(fmt.Sprintf("Found URL: %s (No LastMod specified)", pageURL))
		}

		normalizedPageURL, ok := sp.normalizeInScopeURL(pageURL, sitemapLog)
		if !ok {
			continue
		}

		added, visitErr := sp.store.MarkPageVisited(normalizedPageURL, 1)
		if visitErr != nil {
			sitemapLog.Error(fmt.Sprintf("Sitemap URL DB mark error: %v", visitErr))
			dbErrorCount++
			continue
		}
		if added {
			sp.wg.Add(1)
			sp.pq.Add(&models.WorkItem{URL: normalizedPageURL, Depth: sitemapURLDepth})
			queuedCount++
		}
	}

	if dbErrorCount > 0 {
		sitemapLog.Warn(fmt.Sprintf("Finished URL Set. Queued %d new URLs, encountered %d DB errors.", queuedCount, dbErrorCount))
	} else {
		sitemapLog.Info(fmt.Sprintf("Finished URL Set. Queued %d new URLs.", queuedCount))
	}
}

// normalizeInScopeURL returns the normalized form of pageURL if it is an
// http(s) URL on the allowed domain, under the allowed path prefix, and not
// disallowed. The second return is false when the URL is out of scope or
// unparseable.
func (sp *SitemapProcessor) normalizeInScopeURL(pageURL string, sitemapLog *slog.Logger) (string, bool) {
	parsedPageURL, err := url.Parse(pageURL)
	if err != nil {
		sitemapLog.Warn(fmt.Sprintf("Sitemap URL parse error: %v", err))
		return "", false
	}
	if parsedPageURL.Scheme != "http" && parsedPageURL.Scheme != "https" {
		return "", false
	}
	if !parse.SameHost(parsedPageURL.Hostname(), sp.siteCfg.AllowedDomain) {
		return "", false
	}
	targetPath := parsedPageURL.Path
	if targetPath == "" {
		targetPath = "/"
	}
	if !strings.HasPrefix(targetPath, sp.siteCfg.AllowedPathPrefix) {
		return "", false
	}
	for _, pattern := range sp.compiledDisallowedPatterns {
		if pattern.MatchString(parsedPageURL.Path) {
			return "", false
		}
	}

	normalizedPageURL, _, errNorm := parse.ParseAndNormalize(pageURL)
	if errNorm != nil {
		sitemapLog.Warn(fmt.Sprintf("Sitemap URL normalize error: %v", errNorm))
		return "", false
	}
	return normalizedPageURL, true
}
