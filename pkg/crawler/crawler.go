package crawler

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/sirupsen/logrus"
	"golang.org/x/sync/semaphore"

	"doc-scraper/pkg/config"
	"doc-scraper/pkg/fetch"
	"doc-scraper/pkg/models"
	"doc-scraper/pkg/parse"
	"doc-scraper/pkg/process"
	"doc-scraper/pkg/queue"
	"doc-scraper/pkg/sitemap"
	"doc-scraper/pkg/storage"
	"doc-scraper/pkg/utils"
)

// Crawler orchestrates the web crawling process for a single configured site
type Crawler struct {
	log                        *logrus.Logger
	appCfg                     config.AppConfig
	siteCfg                    config.SiteConfig
	siteOutputDir              string
	compiledDisallowedPatterns []*regexp.Regexp

	// Core components
	store            storage.VisitedStore
	pq               *queue.ThreadSafePriorityQueue
	fetcher          *fetch.Fetcher
	robotsHandler    *fetch.RobotsHandler
	rateLimiter      *fetch.RateLimiter
	sitemapProcessor *sitemap.SitemapProcessor
	contentProcessor *process.ContentProcessor
	imageProcessor   *process.ImageProcessor
	linkProcessor    *process.LinkProcessor

	// Concurrency control
	globalSemaphore  *semaphore.Weighted
	hostSemaphores   map[string]*semaphore.Weighted
	hostSemaphoresMu sync.Mutex

	// Tracking and coordination
	wg               sync.WaitGroup
	processedCounter atomic.Int64
	crawlCtx         context.Context
	cancelCrawl      context.CancelFunc

	// Sitemap handling
	sitemapQueue    chan string
	foundSitemaps   map[string]bool
	foundSitemapsMu sync.Mutex
}

// NewCrawler creates and initializes a new Crawler instance and its components
func NewCrawler(
	appCfg config.AppConfig,
	siteCfg config.SiteConfig,
	log *logrus.Logger,
	store storage.VisitedStore,
	fetcher *fetch.Fetcher,
	rateLimiter *fetch.RateLimiter,
	crawlCtx context.Context,
	cancelCrawl context.CancelFunc,
) (*Crawler, error) {

	compiledDisallowedPatterns, err := utils.CompileRegexPatterns(siteCfg.DisallowedPathPatterns)
	if err != nil {
		return nil, fmt.Errorf("compiling disallowed patterns for '%s': %w", siteCfg.AllowedDomain, err)
	}
	if len(compiledDisallowedPatterns) > 0 {
		log.Infof("Compiled %d disallowed path patterns.", len(compiledDisallowedPatterns))
	}

	siteOutputDir := filepath.Join(appCfg.OutputBaseDir, utils.SanitizeFilename(siteCfg.AllowedDomain))

	c := &Crawler{
		log:                        log,
		appCfg:                     appCfg,
		siteCfg:                    siteCfg,
		siteOutputDir:              siteOutputDir,
		compiledDisallowedPatterns: compiledDisallowedPatterns,
		store:                      store,
		pq:                         queue.NewThreadSafePriorityQueue(log),
		fetcher:                    fetcher,
		rateLimiter:                rateLimiter,
		globalSemaphore:            semaphore.NewWeighted(int64(appCfg.MaxRequests)),
		hostSemaphores:             make(map[string]*semaphore.Weighted),
		crawlCtx:                   crawlCtx,
		cancelCrawl:                cancelCrawl,
		sitemapQueue:               make(chan string, 100),
		foundSitemaps:              make(map[string]bool),
	}

	// Initialize components that depend on the crawler or other components
	c.robotsHandler = fetch.NewRobotsHandler(fetcher, rateLimiter, c.globalSemaphore, c, appCfg, log) // Crawler implements SitemapDiscoverer
	c.sitemapProcessor = sitemap.NewSitemapProcessor(c.sitemapQueue, c.pq, c.store, c.fetcher, c.rateLimiter, c.globalSemaphore, c.compiledDisallowedPatterns, c.siteCfg, c.appCfg, c.log, &c.wg)
	c.imageProcessor = process.NewImageProcessor(c.store, c.fetcher, c.robotsHandler, c.rateLimiter, c.globalSemaphore, c.appCfg, c.log)
	c.contentProcessor = process.NewContentProcessor(c.imageProcessor, c.appCfg, c.log)
	c.linkProcessor = process.NewLinkProcessor(c.store, c.pq, c.compiledDisallowedPatterns, c.log)

	return c, nil
}

// FoundSitemap implements fetch.SitemapDiscoverer for the RobotsHandler callback
func (c *Crawler) FoundSitemap(sitemapURL string) {
	c.foundSitemapsMu.Lock()
	isNew := false
	if _, exists := c.foundSitemaps[sitemapURL]; !exists {
		c.foundSitemaps[sitemapURL] = true
		isNew = true
	}
	c.foundSitemapsMu.Unlock()

	if isNew {
		c.log.Debugf("Crawler notified of newly found sitemap: %s", sitemapURL)
	}
}

// Run starts the crawling process and blocks until completion or cancellation
func (c *Crawler) Run(resume bool) error {
	c.log.Infof("Crawl starting for site '%s' with %d worker(s)...", c.siteCfg.AllowedDomain, c.appCfg.NumWorkers)
	crawlStartTime := time.Now()

	// Validate start URLs and find the first valid one for initial robots fetch
	var validStartURLs []string
	var firstValidParsedURL *url.URL
	c.log.Infof("Validating %d provided start URLs...", len(c.siteCfg.StartURLs))
	for i, startURL := range c.siteCfg.StartURLs {
		startLog := c.log.WithFields(logrus.Fields{"index": i, "url": startURL})
		parsed, err := url.ParseRequestURI(startURL)
		if err != nil {
			startLog.Warnf("Invalid format: %v. Skipping.", err)
			continue
		}
		// Basic scope check
		if parsed.Hostname() != c.siteCfg.AllowedDomain {
			startLog.Warnf("Domain mismatch (%s != %s). Skipping.", parsed.Hostname(), c.siteCfg.AllowedDomain)
			continue
		}
		targetPath := parsed.Path
		if targetPath == "" {
			targetPath = "/"
		}
		if !strings.HasPrefix(targetPath, c.siteCfg.AllowedPathPrefix) {
			startLog.Warnf("Path prefix mismatch (%s not under %s). Skipping.", targetPath, c.siteCfg.AllowedPathPrefix)
			continue
		}
		startLog.Debug("Start URL format and scope validated.")
		validStartURLs = append(validStartURLs, startURL)
		if firstValidParsedURL == nil {
			firstValidParsedURL = parsed
		}
	}
	if len(validStartURLs) == 0 {
		return fmt.Errorf("no valid start_urls found for '%s' matching scope", c.siteCfg.AllowedDomain)
	}
	c.siteCfg.StartURLs = validStartURLs // Use only validated URLs
	c.log.Infof("Using %d valid StartURLs: %v", len(c.siteCfg.StartURLs), c.siteCfg.StartURLs)

	// Clean/Prepare output directory
	c.log.Infof("Site output target directory: %s", c.siteOutputDir)
	if !resume {
		if err := c.cleanSiteOutputDir(); err != nil {
			c.log.Errorf("Failed to clean site output directory, attempting to continue: %v", err)
		}
	}
	err := os.MkdirAll(filepath.Join(c.siteOutputDir, process.ImageDir), 0755)
	if err != nil {
		return fmt.Errorf("error creating site output dir '%s': %w", c.siteOutputDir, err)
	}
	c.log.Infof("Ensured site output directory exists: %s", c.siteOutputDir)

	// Requeue incomplete tasks from DB if resuming
	initialTasksFromDB := 0
	if resume {
		requeueChan := make(chan models.WorkItem, 100)
		var requeueWg sync.WaitGroup
		requeueWg.Add(1)
		go func() { // Goroutine to add items from store scan to the main queue
			defer requeueWg.Done()
			for item := range requeueChan {
				c.wg.Add(1)
				c.pq.Add(&item)
				initialTasksFromDB++
			}
		}()

		_, _, scanErr := c.store.RequeueIncomplete(c.crawlCtx, requeueChan)
		close(requeueChan)
		requeueWg.Wait() // Wait for queueing to finish

		if scanErr != nil && !errors.Is(scanErr, context.Canceled) && !errors.Is(scanErr, context.DeadlineExceeded) {
			c.log.Errorf("Error encountered during DB requeue scan: %v", scanErr)
		}
		if c.crawlCtx.Err() != nil {
			c.log.Warnf("Crawl context cancelled during resume scan: %v", c.crawlCtx.Err())
			return c.crawlCtx.Err()
		}
	}

	// --- Start Background Processes ---
	c.log.Infof("Starting %d workers...", c.appCfg.NumWorkers)
	for i := 1; i <= c.appCfg.NumWorkers; i++ {
		workerScopedLog := c.log.WithFields(logrus.Fields{"worker_id": i, "domain": c.siteCfg.AllowedDomain})
		go c.worker(workerScopedLog)
	}
	c.log.Infof("%d workers started.", c.appCfg.NumWorkers)
	c.sitemapProcessor.Start(c.crawlCtx)

	// --- Waiter Goroutine (Coordinates Startup & Shutdown) ---
	waiterDone := make(chan struct{})
	go func() { // Main coordination goroutine
		defer close(waiterDone) // Signal main when done

		// Progress Reporting (nested goroutine)
		progTicker := time.NewTicker(30 * time.Second)
		progDone := make(chan bool)
		defer func() { progTicker.Stop(); close(progDone); c.log.Info("Waiter: Progress reporter stopped.") }()
		go func() {
			c.log.Info("Progress reporter started.")
			for {
				select {
				case <-progDone:
					return
				case <-c.crawlCtx.Done():
					return
				case <-progTicker.C:
					vCount, _ := c.store.GetVisitedCount()
					pqLen := c.pq.Len()
					smQLen := len(c.sitemapQueue)
					procCount := c.processedCounter.Load()
					c.log.WithFields(logrus.Fields{"visited_db": vCount, "page_queue_len": pqLen, "sitemap_queue_len": smQLen, "processed_tasks": procCount}).Info("Crawl Progress")
				}
			}
		}()

		// Initial Robots.txt fetch
		initialRobotsDone := make(chan bool, 1)
		c.log.Info("Triggering initial robots.txt fetch...")
		go c.robotsHandler.GetRobotsData(firstValidParsedURL, initialRobotsDone, c.crawlCtx)
		select {
		case <-initialRobotsDone:
			c.log.Info("Waiter: Initial robots.txt fetch signaled complete.")
		case <-c.crawlCtx.Done():
			c.log.Warnf("Waiter: Context cancelled while waiting for initial robots.txt: %v", c.crawlCtx.Err())
			return // Exit if cancelled during startup
		}

		// Queue sitemaps found during initial robots fetch
		c.log.Info("Waiter: Processing initially discovered sitemaps...")
		c.foundSitemapsMu.Lock()
		var initialSitemapsToQueue []string
		for smURL := range c.foundSitemaps {
			if c.sitemapProcessor.MarkSitemapProcessed(smURL) {
				initialSitemapsToQueue = append(initialSitemapsToQueue, smURL)
			}
		}
		c.foundSitemapsMu.Unlock()
		if len(initialSitemapsToQueue) > 0 {
			c.log.Infof("Waiter: Found %d initial sitemaps to queue.", len(initialSitemapsToQueue))
			for _, smURL := range initialSitemapsToQueue {
				c.wg.Add(1)
				select {
				case c.sitemapQueue <- smURL:
					c.log.Debugf("Waiter: Sent initial sitemap %s", smURL)
				case <-c.crawlCtx.Done():
					c.log.Warnf("Waiter: Context cancelled while sending initial sitemap %s: %v", smURL, c.crawlCtx.Err())
					c.wg.Done()
				case <-time.After(10 * time.Second): // Increased timeout
					c.log.Errorf("Waiter: Timeout sending initial sitemap %s. Undoing WG.", smURL)
					c.wg.Done()
				}
			}
		} else {
			c.log.Info("Waiter: No new initial sitemaps found to queue.")
		}

		// Wait for all tasks (page workers + sitemap tasks) to complete
		c.log.Info("Waiter: Waiting for ALL tasks via WaitGroup...")
		waitTasksDone := make(chan struct{})
		go func() { c.wg.Wait(); close(waitTasksDone) }() // Wait in background
		select {
		case <-waitTasksDone:
			c.log.Info("Waiter: WaitGroup finished normally.")
		case <-c.crawlCtx.Done():
			c.log.Warnf("Waiter: Global context cancelled/timed out (%v). Initiating shutdown.", c.crawlCtx.Err())
		}

		// Initiate shutdown of queues
		c.log.Info("Waiter: Closing priority queue...")
		c.pq.Close()
		c.log.Info("Waiter: Closing sitemap processing queue...")
		close(c.sitemapQueue)

	}()

	// Seed queue with initial start URLs
	c.log.Info("Seeding priority queue with validated start URLs...")
	initialURLsAddedFromSeed := 0
	for _, startURL := range c.siteCfg.StartURLs {
		c.log.Infof("Adding start URL '%s' to queue (Depth 0).", startURL)
		c.wg.Add(1)
		c.pq.Add(&models.WorkItem{URL: startURL, Depth: 0})
		initialURLsAddedFromSeed++
	}
	if initialURLsAddedFromSeed == 0 {
		c.log.Error("CRITICAL: No validated start URLs were seeded.")
	} else {
		c.log.Infof("Finished seeding %d start URLs.", initialURLsAddedFromSeed)
	}
	c.log.Infof("Total initial tasks added to WaitGroup (DB Scan + Seeding): %d", initialTasksFromDB+initialURLsAddedFromSeed)

	// Wait for the waiter goroutine to finish (signals crawl completion or cancellation handling)
	c.log.Info("Main: Waiting for waiter goroutine...")
	select {
	case <-waiterDone:
		c.log.Info("Main: Waiter finished signal received.")
	case <-c.crawlCtx.Done():
		c.log.Warnf("Main: Crawl context cancelled while waiting for waiter: %v", c.crawlCtx.Err())
		<-waiterDone // Ensure waiter cleanup completes
		c.log.Info("Main: Waiter finished after context cancellation.")
	}

	// Log final summary.
	duration := time.Since(crawlStartTime)
	finalVisitedCount, countErr := c.store.GetVisitedCount()
	if countErr != nil {
		c.log.Warnf("Could not get final visited count: %v", countErr)
		finalVisitedCount = -1
	}
	finalProcessedCount := c.processedCounter.Load()
	c.log.Infof("==================== CRAWL FINISHED ====================")
	c.log.Infof("Site Domain:      %s", c.siteCfg.AllowedDomain)
	c.log.Infof("Duration:         %v", duration)
	c.log.Infof("Final Stats: Visited (DB Est): %d, Processed Tasks: %d", finalVisitedCount, finalProcessedCount)
	c.log.Infof("========================================================")

	// Return context error if crawl was cancelled/timed out
	return c.crawlCtx.Err()
}

// getHostSemaphore retrieves or creates a host-specific semaphore
func (c *Crawler) getHostSemaphore(host string) *semaphore.Weighted {
	c.hostSemaphoresMu.Lock()
	defer c.hostSemaphoresMu.Unlock()

	sem, exists := c.hostSemaphores[host]
	if !exists {
		limit := int64(c.appCfg.MaxRequestsPerHost)
		if limit <= 0 {
			limit = 2 // Default if invalid config
			c.log.Warnf("max_requests_per_host invalid for host %s, defaulting to %d", host, limit)
		}
		sem = semaphore.NewWeighted(limit)
		c.hostSemaphores[host] = sem
		c.log.WithFields(logrus.Fields{"host": host, "limit": limit}).Debug("Created new host semaphore")
	}
	return sem
}

// worker runs the loop for a single worker goroutine, processing tasks from the queue
func (c *Crawler) worker(workerLog *logrus.Entry) {
	workerLog.Info("Worker starting")
	defer workerLog.Info("Worker finished")

	for {
		// Check context before potentially blocking Pop
		select {
		case <-c.crawlCtx.Done():
			workerLog.Warnf("Worker shutting down due to context cancellation: %v", c.crawlCtx.Err())
			return
		default:
		}

		// Pop blocks until item available or queue closed
		workItemPtr, ok := c.pq.Pop()
		if !ok { // Queue closed and empty
			if c.crawlCtx.Err() != nil {
				workerLog.Warnf("Worker shutting down (queue closed & context cancelled): %v", c.crawlCtx.Err())
			} else {
				workerLog.Info("Worker shutting down (queue closed & empty)")
			}
			return
		}

		// Process the task
		c.processSinglePageTask(*workItemPtr, workerLog)
	}
}

// cleanSiteOutputDir removes the site-specific output directory safely
func (c *Crawler) cleanSiteOutputDir() error {
	c.log.Warnf("Attempting to remove existing site output directory: %s", c.siteOutputDir)

	// Safety Check: Resolve absolute paths
	absBase, errBase := filepath.Abs(c.appCfg.OutputBaseDir)
	if errBase != nil {
		return fmt.Errorf("safety check failed (base path): %w", errBase)
	}
	absSite, errSite := filepath.Abs(c.siteOutputDir)
	if errSite != nil {
		return fmt.Errorf("safety check failed (site path): %w", errSite)
	}

	// Check: Site path must be non-empty, different from base, and nested under base
	absBaseSeparator := absBase + string(filepath.Separator)
	if absSite != "" && absSite != absBase && strings.HasPrefix(absSite, absBaseSeparator) {
		c.log.Debugf("Safety check passed. Attempting os.RemoveAll...")
		err := os.RemoveAll(c.siteOutputDir)
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("failed remove site output dir '%s': %w", c.siteOutputDir, err)
		} else if err == nil {
			c.log.Infof("Removed existing site output directory: %s", c.siteOutputDir)
		}
		return nil // Success or didn't exist
	}

	// Safety check failed.
	errMsg := fmt.Sprintf("safety check failed: would not remove dir (BaseAbs: %s, SiteAbs: %s)", absBase, absSite)
	c.log.Error(errMsg)
	return errors.New(errMsg)
}

// --- Task Processing Logic ---

// processSinglePageTask orchestrates the processing pipeline for a single URL
func (c *Crawler) processSinglePageTask(workItem models.WorkItem, workerLog *logrus.Entry) {
	currentURL := workItem.URL
	currentDepth := workItem.Depth
	taskLog := workerLog.WithFields(logrus.Fields{"url": currentURL, "depth": currentDepth})
	startTime := time.Now()

	var taskErr error
	var finalStatus string
	var finalErrorType string = "None"
	var skipped bool = false
	var normalizedURL string

	// Combined defer for panic recovery, status update, and bookkeeping
	defer func() {
		if r := recover(); r != nil {
			skipped = false // Panic overrides skip
			taskErr = fmt.Errorf("panic: %v", r)
			stackTrace := string(debug.Stack())
			taskLog.WithFields(logrus.Fields{
				"panic_info": r, "duration": time.Since(startTime), "stage": "PanicRecovery", "stack_trace": stackTrace,
			}).Error("PANIC recovered in processSinglePageTask")
		}

		// Determine final status based on errors/skip flag
		if taskErr != nil {
			finalStatus = "failure"
			finalErrorType = utils.CategorizeError(taskErr)
			if recover() == nil { // Log non-panic errors
				taskLog.WithFields(logrus.Fields{"category": finalErrorType, "duration": time.Since(startTime)}).
					Warnf("Task failed: %v", taskErr)
			}
		} else if skipped {
			finalStatus = "skipped"
			taskLog.WithField("duration", time.Since(startTime)).Info("Task skipped")
		} else {
			finalStatus = "success"
			finalErrorType = "None"
			taskLog.WithField("duration", time.Since(startTime)).Info("Task completed successfully")
		}

		// Update DB status if not skipped and URL was successfully normalized
		if !skipped && normalizedURL != "" {
			pageEntry := &models.PageDBEntry{
				Status: finalStatus, ErrorType: finalErrorType, LastAttempt: time.Now(), Depth: currentDepth,
			}
			if finalStatus == "success" {
				pageEntry.ProcessedAt = pageEntry.LastAttempt
			}
			if dbUpdateErr := c.store.UpdatePageStatus(normalizedURL, pageEntry); dbUpdateErr != nil {
				taskLog.Errorf("Failed update final DB status for '%s' to '%s': %v", normalizedURL, finalStatus, dbUpdateErr)
			} else {
				taskLog.Debugf("Updated DB status to '%s' for %s", finalStatus, normalizedURL)
			}
		} else if !skipped {
			taskLog.Warnf("URL '%s' normalization failed earlier; cannot update DB status.", currentURL)
		}

		c.processedCounter.Add(1) // Increment attempt counter
		c.wg.Done()               // Signal WaitGroup
	}() // End defer.

	// Stores first critical error encountered - Returns true if error occurred
	handleTaskError := func(err error) bool {
		if err == nil {
			return false
		}
		if taskErr == nil {
			taskErr = err
		}
		return true
	}

	// --- Orchestration Pipeline ---
	// 1. Setup & Resume Check
	var parsedOriginalURL *url.URL
	var host string
	var err error
	parsedOriginalURL, normURL, host, shouldSkip, err := c.handleSetupAndResumeCheck(currentURL, taskLog)
	if handleTaskError(err) {
		return
	}
	if shouldSkip {
		skipped = true
		return
	}
	normalizedURL = normURL
	taskLog = taskLog.WithField("host", host)

	// 2. Policy Checks
	err = c.runPolicyChecks(parsedOriginalURL, currentDepth, taskLog)
	if handleTaskError(err) {
		return
	}

	// 3. Acquire Resources
	var cleanupResources func()
	cleanupResources, err = c.acquireResources(host, taskLog)
	defer cleanupResources()
	if handleTaskError(err) {
		return
	}

	// 4. Fetch & Validate Page
	var finalURL *url.URL
	var resp *http.Response
	finalURL, resp, err = c.fetchAndValidatePage(currentURL, parsedOriginalURL, taskLog)
	if handleTaskError(err) {
		return
	}
	// Success: resp body is open, passed to next stage

	// 5. Read & Parse Body
	var originalDoc *goquery.Document
	originalDoc, err = c.readAndParseBody(resp, finalURL, taskLog) // Closes resp.Body
	if handleTaskError(err) {
		return
	}

	// 6. Extract & Queue Links (non-critical errors logged within)
	_, linkErr := c.linkProcessor.ExtractAndQueueLinks(originalDoc, finalURL, currentDepth, c.siteCfg, &c.wg, taskLog)
	if linkErr != nil {
		taskLog.Warnf("Non-fatal error during link extraction/queueing: %v", linkErr)
	}

	// 7. Process & Save Content
	_, err = c.contentProcessor.ExtractProcessAndSaveContent(originalDoc, finalURL, c.siteCfg, c.siteOutputDir, taskLog, c.crawlCtx)
	handleTaskError(err) // Critical if content saving fails
}

// --- Helper methods for processSinglePageTask stages ---

// handleSetupAndResumeCheck parses URL, normalizes, checks DB status if resuming
func (c *Crawler) handleSetupAndResumeCheck(currentURL string, taskLog *logrus.Entry) (parsedURL *url.URL, normalizedURL string, host string, shouldSkip bool, err error) {
	taskLog.Debug("Setup/resume check...")
	parsedURL, parseErr := url.Parse(currentURL)
	if parseErr != nil {
		err = fmt.Errorf("%w: parsing URL '%s': %w", utils.ErrParsing, currentURL, parseErr)
		return nil, "", "", false, err
	}

	normalizedURL = parse.NormalizeURL(parsedURL)
	host = parsedURL.Hostname()
	if host == "" && parsedURL.Scheme != "file" {
		err = fmt.Errorf("URL '%s' missing host", currentURL)
		return parsedURL, normalizedURL, "", false, err
	}

	pageStatus, _, checkErr := c.store.CheckPageStatus(normalizedURL)
	if checkErr != nil {
		taskLog.Errorf("DB error checking status for '%s', proceeding: %v", normalizedURL, checkErr)
	} else if pageStatus == "success" {
		taskLog.Info("Skipping already processed page.")
		shouldSkip = true
		return parsedURL, normalizedURL, host, shouldSkip, nil
	} else if pageStatus == "failure" {
		taskLog.Warnf("Retrying previously failed page.")
	} else if pageStatus == "pending" {
		taskLog.Debug("Processing page previously marked pending.")
	} // 'not_found' or unexpected -> proceed normally

	return parsedURL, normalizedURL, host, false, nil // Proceed
}

// runPolicyChecks verifies depth and robots.txt rules
func (c *Crawler) runPolicyChecks(parsedURL *url.URL, depth int, taskLog *logrus.Entry) error {
	taskLog.Debug("Policy checks...")
	// Depth check
	if c.siteCfg.MaxDepth > 0 && depth >= c.siteCfg.MaxDepth {
		err := utils.ErrMaxDepthExceeded
		taskLog.Infof("%s (Depth: %d, Max: %d)", err, depth, c.siteCfg.MaxDepth)
		return err
	}

	// Robots check
	userAgent := c.siteCfg.UserAgent
	if userAgent == "" {
		userAgent = c.appCfg.DefaultUserAgent
	}
	if !c.robotsHandler.TestAgent(parsedURL, userAgent, c.crawlCtx) {
		err := fmt.Errorf("%w: '%s' for agent '%s'", utils.ErrRobotsDisallowed, parsedURL.RequestURI(), userAgent)
		taskLog.Warn(err.Error())
		return err
	}
	return nil
}

// acquireResources gets semaphores and applies rate limit - Returns cleanup func
func (c *Crawler) acquireResources(host string, taskLog *logrus.Entry) (cleanupFunc func(), err error) {
	taskLog.Debug("Acquiring resources...")
	acquiredHostSem, acquiredGlobalSem := false, false
	cleanupFunc = func() { // Cleanup releases acquired semaphores
		if acquiredHostSem {
			c.getHostSemaphore(host).Release(1)
		}
		if acquiredGlobalSem {
			c.globalSemaphore.Release(1)
		}
	}

	semTimeout := c.appCfg.SemaphoreAcquireTimeout

	// Acquire host semaphore
	hostSem := c.getHostSemaphore(host)
	ctxHost, cancelHost := context.WithTimeout(c.crawlCtx, semTimeout)
	defer cancelHost()
	if semErr := hostSem.Acquire(ctxHost, 1); semErr != nil {
		err = utils.WrapErrorf(semErr, "acquire host sem '%s' (timeout: %v)", host, semTimeout)
		return cleanupFunc, err
	}
	acquiredHostSem = true

	// Acquire global semaphore
	ctxGlobal, cancelGlobal := context.WithTimeout(c.crawlCtx, semTimeout)
	defer cancelGlobal()
	if semErr := c.globalSemaphore.Acquire(ctxGlobal, 1); semErr != nil {
		err = utils.WrapErrorf(semErr, "acquire global sem (timeout: %v)", semTimeout)
		return cleanupFunc, err // Must return cleanup to release host sem
	}
	acquiredGlobalSem = true

	// Apply rate limit delay
	c.rateLimiter.ApplyDelay(host, c.siteCfg.DelayPerHost)

	return cleanupFunc, nil // Success
}

// fetchAndValidatePage fetches the URL with retries and validates the final response/URL
func (c *Crawler) fetchAndValidatePage(reqURLString string, originalParsedURL *url.URL, taskLog *logrus.Entry) (finalURL *url.URL, resp *http.Response, err error) {
	taskLog.Debug("Fetching page...")
	userAgent := c.siteCfg.UserAgent
	if userAgent == "" {
		userAgent = c.appCfg.DefaultUserAgent
	}

	req, reqErr := http.NewRequestWithContext(c.crawlCtx, "GET", reqURLString, nil)
	if reqErr != nil {
		err = fmt.Errorf("%w: creating request '%s': %w", utils.ErrRequestCreation, reqURLString, reqErr)
		return nil, nil, err
	}
	req.Header.Set("User-Agent", userAgent)

	// Fetch using component (handles retries)
	resp, fetchErr := c.fetcher.FetchWithRetry(req, c.crawlCtx)
	c.rateLimiter.UpdateLastRequestTime(originalParsedURL.Hostname()) // Update after attempt

	if fetchErr != nil {
		// fetcher ensures body closed if resp exists
		return nil, nil, fetchErr
	}
	// Success: resp non-nil, 2xx status, open body

	// --- Post-fetch Validation ---
	finalURL = resp.Request.URL // URL after redirects
	taskLog.Debug("Validating final URL...")
	finalHost := finalURL.Hostname()
	finalPath := finalURL.Path
	if finalPath == "" {
		finalPath = "/"
	}

	if finalURL.String() != reqURLString {
		taskLog = taskLog.WithField("final_url", finalURL.String())
	}

	// Scope Check (Final URL - Domain/Prefix)
	if finalHost != c.siteCfg.AllowedDomain || !strings.HasPrefix(finalPath, c.siteCfg.AllowedPathPrefix) {
		err = fmt.Errorf("%w: redirected URL '%s' out of scope", utils.ErrScopeViolation, finalURL.String())
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		return finalURL, nil, err
	}

	// Scope Check (Final URL - Disallowed Patterns)
	for _, pattern := range c.compiledDisallowedPatterns {
		if pattern.MatchString(finalURL.Path) {
			err = fmt.Errorf("%w: redirected URL '%s' matches disallowed pattern", utils.ErrScopeViolation, finalURL.String())
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return finalURL, nil, err
		}
	}

	// Robots Check (Final URL - only if host changed)
	if finalHost != originalParsedURL.Hostname() {
		taskLog.Debugf("Host changed (%s -> %s), re-checking robots.txt", originalParsedURL.Hostname(), finalHost)
		if !c.robotsHandler.TestAgent(finalURL, userAgent, c.crawlCtx) {
			err = fmt.Errorf("%w: redirected URL '%s' disallowed by robots", utils.ErrRobotsDisallowed, finalURL.String())
			taskLog.Warn(err.Error())
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return finalURL, nil, err
		}
	}

	// Basic Content-Type check
	contentType := resp.Header.Get("Content-Type")
	ctLower := strings.ToLower(contentType)
	if !strings.HasPrefix(ctLower, "text/html") && !strings.HasPrefix(ctLower, "application/xhtml+xml") {
		taskLog.Warnf("Unexpected Content-Type '%s' for '%s'.", contentType, finalURL.String())
	}

	return finalURL, resp, nil // Success, return open response
}

// readAndParseBody reads and parses the response body into a goquery document - Closes body
func (c *Crawler) readAndParseBody(resp *http.Response, finalURL *url.URL, taskLog *logrus.Entry) (doc *goquery.Document, err error) {
	taskLog.Debug("Reading/parsing body...")
	defer resp.Body.Close()

	bodyBytes, readErr := io.ReadAll(resp.Body) // Read full body for goquery
	if readErr != nil {
		err = fmt.Errorf("%w: reading body from '%s': %w", utils.ErrResponseBodyRead, finalURL.String(), readErr)
		return nil, err
	}
	taskLog.Debugf("Read %d bytes", len(bodyBytes))

	doc, parseErr := goquery.NewDocumentFromReader(bytes.NewReader(bodyBytes))
	if parseErr != nil {
		err = fmt.Errorf("%w: parsing HTML from '%s': %w", utils.ErrParsing, finalURL.String(), parseErr)
		return nil, err
	}
	return doc, nil
}
