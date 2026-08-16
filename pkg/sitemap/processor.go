package sitemap

import (
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

// SitemapProcessor handles fetching, parsing, and processing sitemaps.
type SitemapProcessor struct {
	sitemapQueue               chan string
	pq                         *queue.ThreadSafePriorityQueue
	store                      storage.PageStore
	fetcher                    fetch.HTTPFetcher
	rateLimiter                *fetch.RateLimiter
	globalSemaphore            *semaphore.Weighted
	compiledDisallowedPatterns []*regexp.Regexp
	siteCfg                    *config.SiteConfig
	appCfg                     *config.AppConfig
	log                        *slog.Logger
	wg                         *sync.WaitGroup // main crawler WaitGroup
	sitemapsProcessed          map[string]bool
	sitemapsProcessedMu        sync.Mutex
}

func NewSitemapProcessor(
	sitemapQueue chan string,
	pq *queue.ThreadSafePriorityQueue,
	store storage.PageStore,
	fetcher fetch.HTTPFetcher,
	rateLimiter *fetch.RateLimiter,
	globalSemaphore *semaphore.Weighted,
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
		compiledDisallowedPatterns: compiledDisallowedPatterns,
		siteCfg:                    siteCfg,
		appCfg:                     appCfg,
		log:                        log.With("component", "sitemap_processor"),
		wg:                         wg,
		sitemapsProcessed:          make(map[string]bool),
	}
}

// Start runs the sitemap processing loop in a goroutine.
func (sp *SitemapProcessor) Start(ctx context.Context) {
	sp.log.Info("Sitemap processing goroutine starting.")
	go sp.run(ctx)
}

// MarkSitemapProcessed marks a sitemap URL as queued. Returns true if newly marked.
func (sp *SitemapProcessor) MarkSitemapProcessed(sitemapURL string) bool {
	sp.sitemapsProcessedMu.Lock()
	defer sp.sitemapsProcessedMu.Unlock()
	if !sp.sitemapsProcessed[sitemapURL] {
		sp.sitemapsProcessed[sitemapURL] = true
		return true
	}
	return false
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
	items  []string
	head   int
	closed bool
}

func newSitemapBacklog() *sitemapBacklog {
	b := &sitemapBacklog{}
	b.cond = sync.NewCond(&b.mu)
	return b
}

func (b *sitemapBacklog) push(url string) {
	b.mu.Lock()
	b.items = append(b.items, url)
	b.mu.Unlock()
	b.cond.Signal()
}

// pop blocks until an item is available, returning ok=false only once the
// backlog is both closed and fully drained.
func (b *sitemapBacklog) pop() (string, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for b.head == len(b.items) && !b.closed {
		b.cond.Wait()
	}
	if b.head == len(b.items) {
		return "", false
	}
	url := b.items[b.head]
	b.items[b.head] = ""
	b.head++
	if b.head > 1024 && b.head*2 >= len(b.items) {
		b.items = append(b.items[:0], b.items[b.head:]...)
		b.head = 0
	}
	return url, true
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
	backlog := newSitemapBacklog()

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
				backlog.push(sitemapURL)
			}
		}
	}()

	var workers sync.WaitGroup
	for range sp.workerCount() {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for {
				smURL, ok := backlog.pop()
				if !ok {
					return
				}
				sp.processOne(ctx, smURL, backlog)
			}
		}()
	}

	workers.Wait()
	sp.log.Info("Sitemap processing finished; all workers exited.")
}

// processOne runs one sitemap task, always balancing the WaitGroup token added
// when the sitemap was enqueued and recovering from panics so a single bad
// sitemap cannot take down a worker.
func (sp *SitemapProcessor) processOne(ctx context.Context, smURL string, backlog *sitemapBacklog) {
	defer sp.wg.Done()
	defer sp.recoverPanic(smURL)
	sp.processSitemap(ctx, smURL, backlog)
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

// processSitemap acquires a global request slot, fetches one sitemap, and
// dispatches on whether it is a sitemap index or a URL set.
func (sp *SitemapProcessor) processSitemap(ctx context.Context, smURL string, backlog *sitemapBacklog) {
	sitemapLog := sp.log.With("sitemap_url", smURL)
	sitemapLog.Info("Processing sitemap")

	parsedSitemapURL, err := url.Parse(smURL)
	if err != nil {
		sitemapLog.Error(fmt.Sprintf("Failed parse URL: %v", err))
		return
	}
	sitemapHost := parsedSitemapURL.Hostname()

	if !sp.acquireGlobalSlot(ctx, sitemapLog) {
		return
	}
	defer sp.globalSemaphore.Release(1)

	sp.rateLimiter.ApplyDelay(ctx, sitemapHost, sp.appCfg.DefaultDelayPerHost)

	sitemapBytes, ok := sp.fetchSitemap(ctx, smURL, sitemapLog)
	if !ok {
		return
	}

	var index parse.XMLSitemapIndex
	errIndex := xml.Unmarshal(sitemapBytes, &index)
	if errIndex == nil && len(index.Sitemaps) > 0 {
		sp.handleSitemapIndex(index, backlog, sitemapLog)
		return
	}
	sp.handleURLSet(sitemapBytes, errIndex, sitemapLog)
}

func (sp *SitemapProcessor) acquireGlobalSlot(ctx context.Context, sitemapLog *slog.Logger) bool {
	ctxG, cancelG := context.WithTimeout(ctx, config.DefaultSemaphoreAcquireTimeout)
	err := sp.globalSemaphore.Acquire(ctxG, 1)
	cancelG()
	if err == nil {
		return true
	}
	switch {
	case errors.Is(err, context.DeadlineExceeded) && ctx.Err() != nil:
		sitemapLog.Warn(fmt.Sprintf("Could not acquire GLOBAL semaphore due to main context cancellation: %v", ctx.Err()))
	case errors.Is(err, context.DeadlineExceeded):
		sitemapLog.Error(fmt.Sprintf("Timeout acquiring GLOBAL semaphore: %v", err))
	default:
		sitemapLog.Error(fmt.Sprintf("Error acquiring GLOBAL semaphore: %v", err))
	}
	return false
}

// fetchSitemap retrieves and reads the sitemap body, capped at 10 MB. The
// second return is false if the request could not be completed.
func (sp *SitemapProcessor) fetchSitemap(ctx context.Context, smURL string, sitemapLog *slog.Logger) ([]byte, bool) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, smURL, nil)
	if err != nil {
		sitemapLog.Error(fmt.Sprintf("Req Create error: %v", err))
		return nil, false
	}
	req.Header.Set("User-Agent", sp.appCfg.DefaultUserAgent)

	resp, fetchErr := sp.fetcher.FetchWithRetry(req, ctx)
	if fetchErr != nil {
		sitemapLog.Error(fmt.Sprintf("Fetch failed: %v", fetchErr))
		if resp != nil {
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
		}
		return nil, false
	}
	defer resp.Body.Close()

	const maxSitemapSize = 10 * 1024 * 1024 // 10 MB
	sitemapBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, maxSitemapSize))
	if readErr != nil {
		sitemapLog.Error(fmt.Sprintf("Read body error: %v", readErr))
		return nil, false
	}
	return sitemapBytes, true
}

// handleSitemapIndex queues each valid, not-yet-seen nested sitemap onto the
// backlog for the worker pool to process.
func (sp *SitemapProcessor) handleSitemapIndex(index parse.XMLSitemapIndex, backlog *sitemapBacklog, sitemapLog *slog.Logger) {
	sitemapLog.Info(fmt.Sprintf("Parsed as Sitemap Index, found %d references.", len(index.Sitemaps)))
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
		backlog.push(nestedSmURL)
		nestedSmLog.Debug("Queued nested sitemap.")
		queuedCount++
	}
	sitemapLog.Info(fmt.Sprintf("Queued %d nested sitemaps.", queuedCount))
}

// handleURLSet parses a <urlset> and enqueues every in-scope, new page URL.
func (sp *SitemapProcessor) handleURLSet(sitemapBytes []byte, errIndex error, sitemapLog *slog.Logger) {
	var urlSet parse.XMLURLSet
	errURLSet := xml.Unmarshal(sitemapBytes, &urlSet)
	if errURLSet != nil {
		// Only surface an error if it also failed to parse as an index.
		if errIndex != nil {
			sitemapLog.Error(fmt.Sprintf("Failed parse XML (Index err=%v; URLSet err=%v)", errIndex, errURLSet))
		} else {
			sitemapLog.Warn(fmt.Sprintf("Content was not a valid Sitemap Index or URL Set (URLSet err=%v)", errURLSet))
		}
		return
	}

	sitemapLog.Info(fmt.Sprintf("Parsed as URL Set, found %d URLs.", len(urlSet.URLs)))
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

		added, visitErr := sp.store.MarkPageVisited(normalizedPageURL)
		if visitErr != nil {
			sitemapLog.Error(fmt.Sprintf("Sitemap URL DB mark error: %v", visitErr))
			dbErrorCount++
			continue
		}
		if added {
			sp.wg.Add(1)
			// Sitemap URLs have no link-graph depth; seed at depth 1 (one hop from
			// the root) so max_depth still bounds them and max_depth=1 stays start-only.
			sp.pq.Add(&models.WorkItem{URL: normalizedPageURL, Depth: 1})
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
	if parsedPageURL.Hostname() != sp.siteCfg.AllowedDomain {
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
