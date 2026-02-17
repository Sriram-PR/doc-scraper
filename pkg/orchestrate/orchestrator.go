package orchestrate

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"golang.org/x/sync/semaphore"

	"github.com/Sriram-PR/doc-scraper/pkg/config"
	"github.com/Sriram-PR/doc-scraper/pkg/crawler"
	"github.com/Sriram-PR/doc-scraper/pkg/fetch"
	"github.com/Sriram-PR/doc-scraper/pkg/storage"
)

// SiteResult contains the result of crawling a single site
type SiteResult struct {
	SiteKey        string
	Success        bool
	Error          error
	PagesProcessed int64
	Duration       time.Duration
}

// Orchestrator manages parallel crawling of multiple sites
type Orchestrator struct {
	appCfg      *config.AppConfig
	log         *logrus.Entry
	siteKeys    []string
	resume      bool

	// Shared resources
	httpClient      *fetch.Fetcher
	rateLimiter     *fetch.RateLimiter
	globalSemaphore *semaphore.Weighted

	// Results
	results   []SiteResult
	resultsMu sync.Mutex

	// Coordination
	ctx    context.Context
	cancel context.CancelFunc
}

// NewOrchestrator creates a new orchestrator for parallel site crawling
func NewOrchestrator(appCfg *config.AppConfig, siteKeys []string, resume bool, log *logrus.Entry) *Orchestrator {
	ctx, cancel := context.WithCancel(context.Background())

	// Create shared HTTP client
	httpClient := fetch.NewClient(appCfg.HTTPClientSettings, log)
	fetcher := fetch.NewFetcher(httpClient, appCfg, log)

	// Create shared rate limiter
	rateLimiter := fetch.NewRateLimiter(appCfg.DefaultDelayPerHost, log)

	// Create shared global semaphore for request limiting across all sites
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

// Run starts crawling all sites in parallel and waits for completion
func (o *Orchestrator) Run() []SiteResult {
	startTime := time.Now()
	o.log.Infof("Starting parallel crawl of %d sites: %v", len(o.siteKeys), o.siteKeys)

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

	// Wait for all sites to complete
	wg.Wait()

	totalDuration := time.Since(startTime)
	o.logSummary(totalDuration)

	return o.results
}

// crawlSite crawls a single site with shared resources
func (o *Orchestrator) crawlSite(siteKey string) SiteResult {
	startTime := time.Now()
	result := SiteResult{
		SiteKey: siteKey,
	}

	siteCfg, exists := o.appCfg.Sites[siteKey]
	if !exists {
		result.Error = fmt.Errorf("site '%s' not found in configuration", siteKey)
		result.Success = false
		o.log.Errorf("Site '%s' not found in configuration", siteKey)
		return result
	}

	// Create site-specific context
	siteCtx, siteCancel := context.WithCancel(o.ctx)
	defer siteCancel()

	// Create site-specific store
	store, err := storage.NewBadgerStore(siteCtx, o.appCfg.StateDir, siteCfg.AllowedDomain, o.resume, o.log)
	if err != nil {
		result.Error = fmt.Errorf("failed to create store for '%s': %w", siteKey, err)
		result.Success = false
		o.log.Errorf("Failed to create store for site '%s': %v", siteKey, err)
		return result
	}
	defer store.Close()

	// Create crawler with shared semaphore
	opts := &crawler.CrawlerOptions{
		SharedSemaphore: o.globalSemaphore,
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
		o.log.Errorf("Failed to create crawler for site '%s': %v", siteKey, err)
		return result
	}

	// Run the crawler
	o.log.Infof("Starting crawl for site '%s'", siteKey)
	if err := c.Run(o.resume); err != nil {
		result.Error = err
		result.Success = false
		o.log.Errorf("Crawl failed for site '%s': %v", siteKey, err)
	} else {
		result.Success = true
		o.log.Infof("Crawl completed for site '%s'", siteKey)
	}

	// Get final progress
	progress := c.GetProgress()
	result.PagesProcessed = progress.PagesProcessed
	result.Duration = time.Since(startTime)

	return result
}

// Cancel cancels all running crawls
func (o *Orchestrator) Cancel() {
	o.log.Info("Cancelling all crawls...")
	o.cancel()
}

// GetProgress returns the current progress of all sites
func (o *Orchestrator) GetProgress() []crawler.CrawlerProgress {
	// This would require keeping references to all crawlers
	// For now, return the results we have
	o.resultsMu.Lock()
	defer o.resultsMu.Unlock()

	progress := make([]crawler.CrawlerProgress, 0, len(o.results))
	for _, r := range o.results {
		progress = append(progress, crawler.CrawlerProgress{
			SiteKey:        r.SiteKey,
			PagesProcessed: r.PagesProcessed,
			IsRunning:      false,
		})
	}
	return progress
}

// logSummary logs a summary of all crawl results
func (o *Orchestrator) logSummary(totalDuration time.Duration) {
	o.log.Info("============================================")
	o.log.Infof("Parallel crawl completed in %v", totalDuration)
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

		o.log.Infof("  %s: %s - %d pages in %v", r.SiteKey, status, r.PagesProcessed, r.Duration)
		if r.Error != nil {
			o.log.Infof("    Error: %v", r.Error)
		}
	}

	o.log.Info("--------------------------------------------")
	o.log.Infof("Total: %d sites (%d success, %d failed), %d pages processed",
		len(o.results), successCount, failCount, totalPages)
	o.log.Info("============================================")
}

// ValidateSiteKeys checks that all provided site keys exist in the config
func ValidateSiteKeys(appCfg *config.AppConfig, siteKeys []string) error {
	for _, key := range siteKeys {
		if _, exists := appCfg.Sites[key]; !exists {
			available := make([]string, 0, len(appCfg.Sites))
			for k := range appCfg.Sites {
				available = append(available, k)
			}
			return fmt.Errorf("site '%s' not found. Available sites: %v", key, available)
		}
	}
	return nil
}

// GetAllSiteKeys returns all site keys from the config
func GetAllSiteKeys(appCfg *config.AppConfig) []string {
	keys := make([]string, 0, len(appCfg.Sites))
	for k := range appCfg.Sites {
		keys = append(keys, k)
	}
	return keys
}
