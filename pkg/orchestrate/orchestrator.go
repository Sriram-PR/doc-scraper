package orchestrate

import (
	"context"
	"fmt"
	"sync"
	"time"

	"golang.org/x/sync/semaphore"
	"log/slog"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/crawler"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/fetch"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/storage"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/storage/index"
)

// SiteResult contains the result of crawling a single site.
type SiteResult struct {
	SiteKey        string
	Success        bool
	Error          error
	PagesProcessed int64
	Duration       time.Duration
}

// Orchestrator manages parallel crawling of multiple sites.
type Orchestrator struct {
	appCfg          *config.AppConfig
	log             *slog.Logger
	siteKeys        []string
	resume          bool
	httpClient      fetch.HTTPFetcher
	rateLimiter     *fetch.RateLimiter
	globalSemaphore *semaphore.Weighted
	results         []SiteResult
	resultsMu       sync.Mutex
	ctx             context.Context
	cancel          context.CancelFunc
	idx             *index.Index
}

// WithIndex attaches a crawl-history index shared across every per-site
// crawler this orchestrator runs. nil is safe and disables history capture.
func (o *Orchestrator) WithIndex(idx *index.Index) *Orchestrator {
	o.idx = idx
	return o
}

// NewOrchestrator creates a new orchestrator for parallel site crawling. The
// orchestrator's context derives from parent, so cancelling parent (e.g. the
// watch scheduler's context on shutdown) aborts every in-flight crawl.
func NewOrchestrator(parent context.Context, appCfg *config.AppConfig, siteKeys []string, resume bool, log *slog.Logger) *Orchestrator {
	ctx, cancel := context.WithCancel(parent)

	fetcher, rateLimiter := fetch.NewStack(appCfg, log)
	globalSemaphore := semaphore.NewWeighted(int64(appCfg.MaxRequests))

	return &Orchestrator{
		appCfg:          appCfg,
		log:             log,
		siteKeys:        siteKeys,
		resume:          resume,
		httpClient:      fetcher,
		rateLimiter:     rateLimiter,
		globalSemaphore: globalSemaphore,
		results:         make([]SiteResult, 0, len(siteKeys)),
		ctx:             ctx,
		cancel:          cancel,
	}
}

// Run starts parallel crawling of all sites and blocks until all finish.
func (o *Orchestrator) Run() []SiteResult {
	startTime := time.Now()
	o.log.Info(fmt.Sprintf("Starting parallel crawl of %d sites: %v", len(o.siteKeys), o.siteKeys))

	var wg sync.WaitGroup

	for _, siteKey := range o.siteKeys {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			result := o.crawlSite(key)
			o.resultsMu.Lock()
			o.results = append(o.results, result)
			o.resultsMu.Unlock()
		}(siteKey)
	}

	wg.Wait()

	totalDuration := time.Since(startTime)
	o.logSummary(totalDuration)

	return o.results
}

func (o *Orchestrator) crawlSite(siteKey string) SiteResult {
	startTime := time.Now()
	result := SiteResult{
		SiteKey: siteKey,
	}

	siteCfg, exists := o.appCfg.Sites[siteKey]
	if !exists {
		result.Error = fmt.Errorf("site '%s' not found in configuration", siteKey)
		result.Success = false
		o.log.Error(fmt.Sprintf("Site '%s' not found in configuration", siteKey))
		return result
	}

	siteCtx, siteCancel := context.WithCancel(o.ctx)
	defer siteCancel()

	store, err := storage.NewBadgerStore(siteCtx, o.appCfg.StateDir, siteKey, o.resume, o.log)
	if err != nil {
		result.Error = fmt.Errorf("failed to create store for '%s': %w", siteKey, err)
		result.Success = false
		o.log.Error(fmt.Sprintf("Failed to create store for site '%s': %v", siteKey, err))
		return result
	}
	defer func() { _ = store.Close() }()
	go store.RunGC(siteCtx, 0)

	opts := &crawler.CrawlerOptions{
		SharedSemaphore: o.globalSemaphore,
		Index:           o.idx,
	}

	c, err := crawler.NewCrawlerWithOptions(
		o.appCfg,
		siteCfg,
		siteKey,
		o.log,
		store,
		o.httpClient,
		o.rateLimiter,
		siteCtx,
		siteCancel,
		o.resume,
		opts,
	)
	if err != nil {
		result.Error = fmt.Errorf("failed to create crawler for '%s': %w", siteKey, err)
		result.Success = false
		o.log.Error(fmt.Sprintf("Failed to create crawler for site '%s': %v", siteKey, err))
		return result
	}

	o.log.Info(fmt.Sprintf("Starting crawl for site '%s'", siteKey))
	if err := c.Run(o.resume); err != nil {
		result.Error = err
		result.Success = false
		o.log.Error(fmt.Sprintf("Crawl failed for site '%s': %v", siteKey, err))
	} else {
		result.Success = true
		o.log.Info(fmt.Sprintf("Crawl completed for site '%s'", siteKey))
	}

	progress := c.GetProgress()
	result.PagesProcessed = progress.PagesProcessed
	result.Duration = time.Since(startTime)

	return result
}

func (o *Orchestrator) Cancel() {
	o.log.Info("Cancelling all crawls...")
	o.cancel()
}

func (o *Orchestrator) logSummary(totalDuration time.Duration) {
	o.log.Info("============================================")
	o.log.Info(fmt.Sprintf("Parallel crawl completed in %v", totalDuration))
	o.log.Info("Site Results:")

	var totalPages int64
	successCount := 0
	failCount := 0

	for _, r := range o.results {
		status := "SUCCESS"
		if !r.Success {
			status = "FAILED"
			failCount++
		} else {
			successCount++
		}
		totalPages += r.PagesProcessed

		o.log.Info(fmt.Sprintf("  %s: %s - %d pages in %v", r.SiteKey, status, r.PagesProcessed, r.Duration))
		if r.Error != nil {
			o.log.Info(fmt.Sprintf("    Error: %v", r.Error))
		}
	}

	o.log.Info("--------------------------------------------")
	o.log.Info(fmt.Sprintf("Total: %d sites (%d success, %d failed), %d pages processed",
		len(o.results), successCount, failCount, totalPages))
	o.log.Info("============================================")
}
