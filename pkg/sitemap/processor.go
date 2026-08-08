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
	"time"

	"golang.org/x/sync/semaphore"

	"github.com/Sriram-PR/doc-scraper/pkg/config"
	"github.com/Sriram-PR/doc-scraper/pkg/fetch"
	"github.com/Sriram-PR/doc-scraper/pkg/models"
	"github.com/Sriram-PR/doc-scraper/pkg/parse"
	"github.com/Sriram-PR/doc-scraper/pkg/queue"
	"github.com/Sriram-PR/doc-scraper/pkg/storage"
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

func (sp *SitemapProcessor) unmarkSitemap(sitemapURL string) {
	sp.sitemapsProcessedMu.Lock()
	delete(sp.sitemapsProcessed, sitemapURL)
	sp.sitemapsProcessedMu.Unlock()
}

// run reads sitemap URLs off the queue and processes each in its own goroutine,
// waiting for all in-flight processing to finish before it exits.
func (sp *SitemapProcessor) run(ctx context.Context) {
	var processingWg sync.WaitGroup

	defer func() {
		sp.log.Info("Waiting for active sitemap processing tasks to finish before final exit...")
		processingWg.Wait()
		sp.log.Info("Sitemap processing goroutine finished waiting and exiting.")
	}()

	for {
		select {
		case <-ctx.Done():
			sp.log.Warn(fmt.Sprintf("Context cancelled, stopping sitemap processing: %v", ctx.Err()))
			return

		case sitemapURL, ok := <-sp.sitemapQueue:
			if !ok {
				sp.log.Info("Sitemap queue channel closed.")
				return
			}

			processingWg.Add(1)
			go func(smURL string) {
				defer sp.wg.Done()
				defer processingWg.Done()
				defer sp.recoverPanic(smURL)
				sp.processSitemap(ctx, smURL)
			}(sitemapURL)
		}
	}
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
func (sp *SitemapProcessor) processSitemap(ctx context.Context, smURL string) {
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
		sp.handleSitemapIndex(ctx, index, sitemapLog)
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

// handleSitemapIndex queues each valid, not-yet-seen nested sitemap.
func (sp *SitemapProcessor) handleSitemapIndex(ctx context.Context, index parse.XMLSitemapIndex, sitemapLog *slog.Logger) {
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
		if sp.queueNestedSitemap(ctx, nestedSmURL, nestedSmLog) {
			queuedCount++
		}
	}
	sitemapLog.Info(fmt.Sprintf("Queued %d nested sitemaps.", queuedCount))
}

// queueNestedSitemap sends a nested sitemap onto the queue, adding a WaitGroup
// token first. On cancellation or a send timeout it undoes both the token and
// the processed mark so the sitemap can be retried. Returns whether it queued.
func (sp *SitemapProcessor) queueNestedSitemap(ctx context.Context, nestedSmURL string, nestedSmLog *slog.Logger) bool {
	sp.wg.Add(1)
	select {
	case sp.sitemapQueue <- nestedSmURL:
		nestedSmLog.Debug("Successfully queued nested sitemap.")
		return true
	case <-ctx.Done():
		nestedSmLog.Warn(fmt.Sprintf("Context cancelled while trying to queue nested sitemap '%s': %v", nestedSmURL, ctx.Err()))
		sp.unmarkSitemap(nestedSmURL)
		sp.wg.Done()
		return false
	case <-time.After(5 * time.Second):
		nestedSmLog.Error("Timeout sending nested sitemap. Undoing WG and processed state.")
		sp.unmarkSitemap(nestedSmURL)
		sp.wg.Done()
		return false
	}
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
			// Enqueue the normalized URL so WorkItem.URL and the DB key agree;
			// see process/links.go for the rationale.
			sp.pq.Add(&models.WorkItem{URL: normalizedPageURL, Depth: 0})
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
