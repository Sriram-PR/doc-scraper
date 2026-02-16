// FILE: pkg/crawler/crawler.go
package crawler

import (
	"bytes"
	"context"
	"encoding/json"

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
	"gopkg.in/yaml.v3"

	"github.com/Sriram-PR/doc-scraper/pkg/config"
	"github.com/Sriram-PR/doc-scraper/pkg/fetch"
	"github.com/Sriram-PR/doc-scraper/pkg/models"
	"github.com/Sriram-PR/doc-scraper/pkg/parse"
	"github.com/Sriram-PR/doc-scraper/pkg/process"
	"github.com/Sriram-PR/doc-scraper/pkg/queue"
	"github.com/Sriram-PR/doc-scraper/pkg/sitemap"
	"github.com/Sriram-PR/doc-scraper/pkg/storage"
	"github.com/Sriram-PR/doc-scraper/pkg/utils"
)

// Pre-compiled regexes for markdown link and image extraction.
var (
	mdLinkRe  = regexp.MustCompile(`(?:^|[^!])\[([^\]]*)\]\(([^)]+)\)`)
	mdImageRe = regexp.MustCompile(`!\[([^\]]*)\]\(([^)]+)\)`)
)

// Crawler orchestrates the web crawling process for a single configured site
type Crawler struct {
	log                        *logrus.Entry // Logger contextualized with site_key
	appCfg                     config.AppConfig
	siteCfg                    config.SiteConfig
	siteKey                    string // Site identifier from config
	siteOutputDir              string // Base output directory for *this specific site's* files
	compiledDisallowedPatterns []*regexp.Regexp

	// Core components
	store            storage.VisitedStore
	pq               *queue.ThreadSafePriorityQueue
	fetcher          *fetch.Fetcher
	robotsHandler    *fetch.RobotsHandler
	rateLimiter      *fetch.RateLimiter
	sitemapProcessor *sitemap.SitemapProcessor
	contentProcessor *process.ContentProcessor
	imageProcessor   *process.ImageProcessor // Initialized, used by ContentProcessor
	linkProcessor    *process.LinkProcessor

	// Concurrency control
	globalSemaphore  *semaphore.Weighted
	hostSemaphores   map[string]*semaphore.Weighted
	hostSemaphoresMu sync.Mutex

	// Tracking and coordination
	wg               sync.WaitGroup     // Main WaitGroup for all active tasks (pages, sitemaps)
	processedCounter atomic.Int64       // Counter for tasks processed by workers
	crawlCtx         context.Context    // Master context for the entire crawl of this site
	cancelCrawl      context.CancelFunc // Function to cancel the crawlCtx

	// Sitemap handling
	sitemapQueue    chan string     // Channel for sitemap URLs to be processed
	foundSitemaps   map[string]bool // Tracks sitemaps discovered by robots.txt
	foundSitemapsMu sync.Mutex      // Protects foundSitemaps

	// For simple TSV mapping
	mappingFile     *os.File   // File handle for the TSV mapping file
	mappingFileMu   sync.Mutex // Protects concurrent writes to mappingFile
	mappingFilePath string     // Full path to the site-specific TSV mapping file

	// For YAML Metadata Output
	crawlStartTime        time.Time             // Timestamp when this specific crawl run was initiated
	collectedPageMetadata []models.PageMetadata // Slice to store metadata for each processed page
	metadataMutex         sync.Mutex            // Protects concurrent appends to collectedPageMetadata

	// For JSONL Output (RAG pipeline ingestion)
	jsonlFile     *os.File   // File handle for the JSONL output file
	jsonlFileMu   sync.Mutex // Protects concurrent writes to jsonlFile
	jsonlFilePath string     // Full path to the site-specific JSONL output file

	// For Chunks Output (RAG vector ingestion)
	chunksFile     *os.File   // File handle for the chunks output file
	chunksFileMu   sync.Mutex // Protects concurrent writes to chunksFile
	chunksFilePath string     // Full path to the site-specific chunks output file
}

// CrawlerOptions contains optional parameters for NewCrawler
type CrawlerOptions struct {
	// SharedSemaphore allows sharing a global semaphore across multiple crawlers
	// If nil, the crawler creates its own semaphore based on appCfg.MaxRequests
	SharedSemaphore *semaphore.Weighted
}

// NewCrawler creates and initializes a new Crawler instance and its components
func NewCrawler(
	appCfg config.AppConfig,
	siteCfg config.SiteConfig,
	siteKey string, // The key for this site from the config map
	baseLogger *logrus.Logger, // Base logger from main
	store storage.VisitedStore,
	fetcher *fetch.Fetcher,
	rateLimiter *fetch.RateLimiter,
	crawlCtx context.Context,
	cancelCrawl context.CancelFunc,
	resume bool, // Flag indicating if this is a resumed crawl
) (*Crawler, error) {
	return NewCrawlerWithOptions(appCfg, siteCfg, siteKey, baseLogger, store, fetcher, rateLimiter, crawlCtx, cancelCrawl, resume, nil)
}

// NewCrawlerWithOptions creates a new Crawler with optional configuration
func NewCrawlerWithOptions(
	appCfg config.AppConfig,
	siteCfg config.SiteConfig,
	siteKey string,
	baseLogger *logrus.Logger,
	store storage.VisitedStore,
	fetcher *fetch.Fetcher,
	rateLimiter *fetch.RateLimiter,
	crawlCtx context.Context,
	cancelCrawl context.CancelFunc,
	resume bool,
	opts *CrawlerOptions,
) (*Crawler, error) {

	// Contextualize logger for this specific crawler instance
	logger := baseLogger.WithField("site_key", siteKey)

	compiledDisallowedPatterns, err := utils.CompileRegexPatterns(siteCfg.DisallowedPathPatterns)
	if err != nil {
		return nil, fmt.Errorf("compiling disallowed patterns for site '%s': %w", siteKey, err)
	}
	if len(compiledDisallowedPatterns) > 0 {
		logger.Infof("Compiled %d disallowed path patterns.", len(compiledDisallowedPatterns))
	}

	siteOutputDir := filepath.Join(appCfg.OutputBaseDir, utils.SanitizeFilename(siteCfg.AllowedDomain))

	// Use shared semaphore if provided, otherwise create a new one
	var globalSem *semaphore.Weighted
	if opts != nil && opts.SharedSemaphore != nil {
		globalSem = opts.SharedSemaphore
		logger.Debug("Using shared global semaphore")
	} else {
		globalSem = semaphore.NewWeighted(int64(appCfg.MaxRequests))
	}

	c := &Crawler{
		log:                        logger,
		appCfg:                     appCfg,
		siteCfg:                    siteCfg,
		siteKey:                    siteKey,
		siteOutputDir:              siteOutputDir,
		compiledDisallowedPatterns: compiledDisallowedPatterns,
		store:                      store,
		pq:                         queue.NewThreadSafePriorityQueue(logger.Logger), // Pass contextualized logger
		fetcher:                    fetcher,
		rateLimiter:                rateLimiter,
		globalSemaphore:            globalSem,
		hostSemaphores:             make(map[string]*semaphore.Weighted),
		crawlCtx:                   crawlCtx,
		cancelCrawl:                cancelCrawl,
		sitemapQueue:               make(chan string, 100), // Buffer size can be configured if needed
		foundSitemaps:              make(map[string]bool),
		// Initialize YAML metadata collection
		collectedPageMetadata: make([]models.PageMetadata, 0),
	}

	// --- Ensure output directory exists before creating any output files ---
	if err := os.MkdirAll(c.siteOutputDir, 0755); err != nil {
		return nil, fmt.Errorf("failed to create site output directory '%s': %w", c.siteOutputDir, err)
	}

	// --- Initialize Simple TSV Mapping File (if enabled) ---
	effectiveEnableTSVMapping := config.GetEffectiveEnableOutputMapping(c.siteCfg, c.appCfg)
	if effectiveEnableTSVMapping {
		tsvMappingFilename := config.GetEffectiveOutputMappingFilename(c.siteCfg, c.appCfg)
		c.mappingFilePath = filepath.Join(c.siteOutputDir, tsvMappingFilename)
		c.log.Infof("Simple TSV URL-to-FilePath mapping enabled. Output file: %s", c.mappingFilePath)

		openFlags := os.O_CREATE | os.O_WRONLY
		if resume {
			c.log.Infof("Resume mode: Appending to TSV mapping file: %s", c.mappingFilePath)
			openFlags |= os.O_APPEND
		} else {
			c.log.Infof("Non-resume mode: Truncating TSV mapping file: %s", c.mappingFilePath)
			openFlags |= os.O_TRUNC
		}
		file, err := os.OpenFile(c.mappingFilePath, openFlags, 0644)
		if err != nil {
			c.log.Errorf("Failed to open/create TSV mapping file '%s': %v. TSV Mapping will be disabled.", c.mappingFilePath, err)
			c.mappingFile = nil // Ensure it's nil so writes are skipped
		} else {
			c.mappingFile = file
		}
	} else {
		c.log.Info("Simple TSV URL-to-FilePath mapping is disabled.")
	}

	// --- Initialize JSONL Output File (if enabled) ---
	effectiveEnableJSONL := config.GetEffectiveEnableJSONLOutput(c.siteCfg, c.appCfg)
	if effectiveEnableJSONL {
		jsonlFilename := config.GetEffectiveJSONLOutputFilename(c.siteCfg, c.appCfg)
		c.jsonlFilePath = filepath.Join(c.siteOutputDir, jsonlFilename)
		c.log.Infof("JSONL output enabled. Output file: %s", c.jsonlFilePath)

		openFlags := os.O_CREATE | os.O_WRONLY
		if resume {
			c.log.Infof("Resume mode: Appending to JSONL file: %s", c.jsonlFilePath)
			openFlags |= os.O_APPEND
		} else {
			c.log.Infof("Non-resume mode: Truncating JSONL file: %s", c.jsonlFilePath)
			openFlags |= os.O_TRUNC
		}
		file, err := os.OpenFile(c.jsonlFilePath, openFlags, 0644)
		if err != nil {
			c.log.Errorf("Failed to open/create JSONL file '%s': %v. JSONL output will be disabled.", c.jsonlFilePath, err)
			c.jsonlFile = nil
		} else {
			c.jsonlFile = file
		}
	} else {
		c.log.Info("JSONL output is disabled.")
	}

	// --- Initialize Chunks Output File (if chunking enabled) ---
	effectiveEnableChunking := config.GetEffectiveChunkingEnabled(c.siteCfg, c.appCfg)
	if effectiveEnableChunking {
		chunksFilename := config.GetEffectiveChunkingOutputFilename(c.siteCfg, c.appCfg)
		c.chunksFilePath = filepath.Join(c.siteOutputDir, chunksFilename)
		c.log.Infof("Chunking enabled. Output file: %s", c.chunksFilePath)

		openFlags := os.O_CREATE | os.O_WRONLY
		if resume {
			c.log.Infof("Resume mode: Appending to chunks file: %s", c.chunksFilePath)
			openFlags |= os.O_APPEND
		} else {
			c.log.Infof("Non-resume mode: Truncating chunks file: %s", c.chunksFilePath)
			openFlags |= os.O_TRUNC
		}
		file, err := os.OpenFile(c.chunksFilePath, openFlags, 0644)
		if err != nil {
			c.log.Errorf("Failed to open/create chunks file '%s': %v. Chunking output will be disabled.", c.chunksFilePath, err)
			c.chunksFile = nil
		} else {
			c.chunksFile = file
		}
	} else {
		c.log.Info("Chunking output is disabled.")
	}

	// --- Initialize Tokenizer for Token Counting (if enabled) ---
	if c.appCfg.EnableTokenCounting {
		encoding := c.appCfg.TokenizerEncoding
		if encoding == "" {
			encoding = "cl100k_base" // Default to GPT-4/Claude encoding
		}
		if err := process.InitTokenizer(encoding); err != nil {
			c.log.Warnf("Failed to initialize tokenizer with encoding '%s': %v. Token counting will use estimates.", encoding, err)
		} else {
			c.log.Infof("Token counting enabled with encoding: %s", encoding)
		}
	}

	// Initialize components that depend on the crawler or other components
	// Pass the crawler's contextualized logger to these components
	c.robotsHandler = fetch.NewRobotsHandler(fetcher, rateLimiter, c.globalSemaphore, c, appCfg, logger.Logger)
	c.sitemapProcessor = sitemap.NewSitemapProcessor(c.sitemapQueue, c.pq, c.store, c.fetcher, c.rateLimiter, c.globalSemaphore, c.compiledDisallowedPatterns, c.siteCfg, c.appCfg, logger.Logger, &c.wg)
	c.imageProcessor = process.NewImageProcessor(c.store, c.fetcher, c.robotsHandler, c.rateLimiter, c.globalSemaphore, c.appCfg, logger.Logger)
	c.contentProcessor = process.NewContentProcessor(c.imageProcessor, c.appCfg, logger.Logger)
	c.linkProcessor = process.NewLinkProcessor(c.store, c.pq, c.compiledDisallowedPatterns, logger.Logger)

	return c, nil
}

// FoundSitemap implements fetch.SitemapDiscoverer for the RobotsHandler callback.
// It's called by RobotsHandler when a sitemap URL is found in robots.txt.
func (c *Crawler) FoundSitemap(sitemapURL string) {
	c.foundSitemapsMu.Lock()
	isNew := false
	if _, exists := c.foundSitemaps[sitemapURL]; !exists {
		c.foundSitemaps[sitemapURL] = true
		isNew = true
	}
	c.foundSitemapsMu.Unlock()

	if isNew {
		// Use crawler's logger which includes site_key
		c.log.Debugf("Crawler notified of newly found sitemap: %s", sitemapURL)
	}
}

// CrawlerProgress contains progress information for a crawler
type CrawlerProgress struct {
	SiteKey        string
	PagesProcessed int64
	PagesQueued    int
	IsRunning      bool
}

// GetProgress returns the current progress of the crawler
func (c *Crawler) GetProgress() CrawlerProgress {
	return CrawlerProgress{
		SiteKey:        c.siteKey,
		PagesProcessed: c.processedCounter.Load(),
		PagesQueued:    c.pq.Len(),
		IsRunning:      c.crawlCtx.Err() == nil,
	}
}

// Run starts the crawling process for the configured site and blocks until completion or cancellation.
func (c *Crawler) Run(resume bool) error {
	c.crawlStartTime = time.Now() // Record CRAWL START TIME for metadata
	// logFields already part of c.log (site_key). Add resume-specific info.
	runLogFields := logrus.Fields{"domain": c.siteCfg.AllowedDomain, "resume": resume}
	c.log.WithFields(runLogFields).Infof("Crawl starting with %d worker(s)...", c.appCfg.NumWorkers)
	overallCrawlStartTimeForDuration := time.Now() // For calculating overall duration visible in final log

	// --- DEFER CLEANUP ACTIONS ---
	defer c.closeMappingFile() // Handles nil check internally for TSV file
	defer c.closeJSONLFile()   // Handles nil check internally for JSONL file
	defer c.closeChunksFile()  // Handles nil check internally for chunks file
	defer func() {             // Defer writing YAML metadata at the very end
		if err := c.writeMetadataYAML(); err != nil {
			c.log.WithFields(runLogFields).Errorf("Failed to write final metadata YAML: %v", err)
		}
	}()

	// --- Start URL Validation ---
	var validStartURLs []string
	var firstValidParsedURL *url.URL // Used for initial robots.txt fetch
	c.log.WithFields(runLogFields).Infof("Validating %d provided start URLs...", len(c.siteCfg.StartURLs))
	for i, startURLStr := range c.siteCfg.StartURLs {
		// Use task-specific logger for each start URL validation attempt
		startValidateLog := c.log.WithFields(logrus.Fields{"index": i, "url": startURLStr})
		parsed, err := url.ParseRequestURI(startURLStr)
		if err != nil {
			startValidateLog.Warnf("Invalid format: %v. Skipping.", err)
			continue
		}
		if parsed.Hostname() != c.siteCfg.AllowedDomain {
			startValidateLog.Warnf("Domain mismatch (%s != %s). Skipping.", parsed.Hostname(), c.siteCfg.AllowedDomain)
			continue
		}
		targetPath := parsed.Path
		if targetPath == "" {
			targetPath = "/" // Normalize empty path to root
		}
		if !strings.HasPrefix(targetPath, c.siteCfg.AllowedPathPrefix) {
			startValidateLog.Warnf("Path prefix mismatch ('%s' not under '%s'). Skipping.", targetPath, c.siteCfg.AllowedPathPrefix)
			continue
		}
		startValidateLog.Debug("Start URL format and scope validated.")
		validStartURLs = append(validStartURLs, startURLStr)
		if firstValidParsedURL == nil {
			firstValidParsedURL = parsed // Store the first valid one
		}
	}
	if len(validStartURLs) == 0 {
		return fmt.Errorf("no valid start_urls found for site '%s' matching scope", c.siteKey)
	}
	c.siteCfg.StartURLs = validStartURLs // Update SiteConfig to use only validated URLs
	c.log.WithFields(runLogFields).Infof("Using %d valid StartURLs: %v", len(c.siteCfg.StartURLs), c.siteCfg.StartURLs)

	// --- Clean/Prepare Output Directory ---
	c.log.WithFields(runLogFields).Infof("Site output target directory: %s", c.siteOutputDir)
	if !resume {
		if err := c.cleanSiteOutputDir(); err != nil {
			// Log error but attempt to continue; subsequent MkdirAll might succeed or fail clearly.
			c.log.WithFields(runLogFields).Errorf("Failed to clean site output directory, attempting to continue: %v", err)
		}
	}
	// Ensure base site directory and image subdirectory exist
	err := os.MkdirAll(filepath.Join(c.siteOutputDir, process.ImageDir), 0755)
	if err != nil {
		return fmt.Errorf("error creating site output dir '%s' for site '%s': %w", c.siteOutputDir, c.siteKey, err)
	}
	c.log.WithFields(runLogFields).Infof("Ensured site output directory exists: %s", c.siteOutputDir)

	// --- Requeue Incomplete Tasks from DB (if resuming) ---
	initialTasksFromDB := 0
	if resume {
		c.log.WithFields(runLogFields).Info("Resume mode: Scanning database for incomplete tasks to requeue...")
		requeueChan := make(chan models.WorkItem, 100) // Buffered channel for items from DB
		var requeueWg sync.WaitGroup
		requeueWg.Add(1)
		go func() { // Goroutine to add items from store scan to the main priority queue
			defer requeueWg.Done()
			for item := range requeueChan {
				c.wg.Add(1) // Increment main WaitGroup for each task being requeued
				c.pq.Add(&item)
				initialTasksFromDB++
			}
		}()

		// RequeueIncomplete scans DB and sends items to requeueChan
		_, _, scanErr := c.store.RequeueIncomplete(c.crawlCtx, requeueChan)
		close(requeueChan) // Close channel once scan is complete
		requeueWg.Wait()   // Wait for all items from channel to be added to PQ

		if scanErr != nil && !errors.Is(scanErr, context.Canceled) && !errors.Is(scanErr, context.DeadlineExceeded) {
			c.log.WithFields(runLogFields).Errorf("Error encountered during DB requeue scan: %v", scanErr)
		}
		if c.crawlCtx.Err() != nil { // Check if context was cancelled during scan
			c.log.WithFields(runLogFields).Warnf("Crawl context cancelled during resume scan: %v", c.crawlCtx.Err())
			return c.crawlCtx.Err() // Exit if cancelled
		}
		c.log.WithFields(runLogFields).Infof("DB requeue scan complete. Requeued %d tasks.", initialTasksFromDB)
	}

	// --- Start Background Processes (Workers, Sitemap Processor) ---
	c.log.WithFields(runLogFields).Infof("Starting %d workers...", c.appCfg.NumWorkers)
	for i := 1; i <= c.appCfg.NumWorkers; i++ {
		// Each worker gets a logger with its ID (site_key is already in c.log)
		workerLog := c.log.WithField("worker_id", i)
		go c.worker(workerLog)
	}
	c.log.WithFields(runLogFields).Infof("%d workers started.", c.appCfg.NumWorkers)
	c.sitemapProcessor.Start(c.crawlCtx) // Sitemap processor uses its own contextualized logger

	// --- Waiter Goroutine (Coordinates Startup Dependencies & Shutdown) ---
	waiterDone := make(chan struct{})
	go func() { // This goroutine manages the sequence of startup and waiting for completion.
		defer close(waiterDone) // Signal that the waiter goroutine itself has finished.

		// Progress Reporting Goroutine (nested)
		progTicker := time.NewTicker(30 * time.Second) // Report progress periodically
		progDone := make(chan bool)                    // Channel to signal progress reporter to stop
		defer func() {
			progTicker.Stop()
			close(progDone)
			c.log.WithFields(runLogFields).Info("Waiter: Progress reporter stopped.")
		}()
		go func() { // Progress reporting loop
			c.log.WithFields(runLogFields).Info("Progress reporter started.")
			for {
				select {
				case <-progDone:
					return // Exit progress reporter
				case <-c.crawlCtx.Done():
					return // Exit if main crawl context is cancelled
				case <-progTicker.C: // On each tick, log progress
					vCount, _ := c.store.GetVisitedCount()
					pqLen := c.pq.Len()
					smQLen := len(c.sitemapQueue) // Approximate, as it's a buffered channel
					procCount := c.processedCounter.Load()
					c.log.WithFields(logrus.Fields{ // Use a new Fields map for progress-specific logs
						"site_key":          c.siteKey, // Include site_key for clarity
						"visited_db":        vCount,
						"page_queue_len":    pqLen,
						"sitemap_queue_len": smQLen,
						"processed_tasks":   procCount,
					}).Info("Crawl Progress")
				}
			}
		}()

		// Initial Robots.txt Fetch (must complete before seeding sitemaps from it)
		if firstValidParsedURL != nil { // Ensure we have a valid URL to derive host
			c.log.WithFields(runLogFields).Info("Triggering initial robots.txt fetch...")
			initialRobotsDone := make(chan bool, 1)                                              // Buffered channel to signal completion
			go c.robotsHandler.GetRobotsData(firstValidParsedURL, initialRobotsDone, c.crawlCtx) // robotsHandler uses its own logger
			select {
			case <-initialRobotsDone:
				c.log.WithFields(runLogFields).Info("Waiter: Initial robots.txt fetch signaled complete.")
			case <-c.crawlCtx.Done(): // If context is cancelled while waiting
				c.log.WithFields(runLogFields).Warnf("Waiter: Context cancelled while waiting for initial robots.txt: %v", c.crawlCtx.Err())
				return // Exit waiter goroutine
			}
		} else {
			c.log.WithFields(runLogFields).Warn("No valid start URL found to fetch initial robots.txt.")
		}

		// Queue Sitemaps Found During Initial Robots Fetch
		c.log.WithFields(runLogFields).Info("Waiter: Processing initially discovered sitemaps...")
		c.foundSitemapsMu.Lock()
		var initialSitemapsToQueue []string
		for smURL := range c.foundSitemaps { // Iterate over sitemaps reported by RobotsHandler
			// MarkSitemapProcessed ensures we don't queue the same sitemap multiple times via this initial step
			if c.sitemapProcessor.MarkSitemapProcessed(smURL) {
				initialSitemapsToQueue = append(initialSitemapsToQueue, smURL)
			}
		}
		c.foundSitemapsMu.Unlock()

		if len(initialSitemapsToQueue) > 0 {
			c.log.WithFields(runLogFields).Infof("Waiter: Found %d initial sitemaps to queue.", len(initialSitemapsToQueue))
			for _, smURL := range initialSitemapsToQueue {
				c.wg.Add(1) // Increment main WaitGroup for each sitemap task
				select {
				case c.sitemapQueue <- smURL:
					c.log.WithFields(runLogFields).Debugf("Waiter: Sent initial sitemap %s to queue.", smURL)
				case <-c.crawlCtx.Done():
					c.log.WithFields(runLogFields).Warnf("Waiter: Context cancelled while sending initial sitemap %s: %v", smURL, c.crawlCtx.Err())
					c.wg.Done() // Decrement WG as task won't be processed
				case <-time.After(10 * time.Second): // Timeout for sending to queue
					c.log.WithFields(runLogFields).Errorf("Waiter: Timeout sending initial sitemap %s. Undoing WG.", smURL)
					c.wg.Done() // Decrement WG
				}
			}
		} else {
			c.log.WithFields(runLogFields).Info("Waiter: No new initial sitemaps found to queue from robots.txt.")
		}

		// Wait for All Tasks (page workers + sitemap tasks via c.wg)
		c.log.WithFields(runLogFields).Info("Waiter: Waiting for ALL tasks (pages, sitemaps) via WaitGroup...")
		waitTasksDone := make(chan struct{})
		go func() { c.wg.Wait(); close(waitTasksDone) }() // Wait for WG in a separate goroutine
		select {
		case <-waitTasksDone: // WG completed normally
			c.log.WithFields(runLogFields).Info("Waiter: WaitGroup finished normally (all tasks done).")
		case <-c.crawlCtx.Done(): // Main crawl context cancelled/timed out
			c.log.WithFields(runLogFields).Warnf("Waiter: Global context cancelled/timed out (%v) while waiting for tasks. Initiating shutdown.", c.crawlCtx.Err())
		}

		// Initiate Shutdown of Queues (signals workers and sitemap processor to stop)
		c.log.WithFields(runLogFields).Info("Waiter: Closing priority queue for pages...")
		c.pq.Close()
		c.log.WithFields(runLogFields).Info("Waiter: Closing sitemap processing queue...")
		close(c.sitemapQueue)
	}()

	// --- Seed Queue with Initial Start URLs ---
	c.log.WithFields(runLogFields).Info("Seeding priority queue with validated start URLs...")
	initialURLsAddedFromSeed := 0
	for _, startURLStr := range c.siteCfg.StartURLs { // Use the validated list
		c.log.WithFields(runLogFields).Infof("Adding start URL '%s' to queue (Depth 0).", startURLStr)
		c.wg.Add(1) // Increment main WaitGroup for each initial URL
		c.pq.Add(&models.WorkItem{URL: startURLStr, Depth: 0})
		initialURLsAddedFromSeed++
	}
	if initialURLsAddedFromSeed == 0 && initialTasksFromDB == 0 && len(c.foundSitemaps) == 0 { // Check all potential sources
		c.log.WithFields(runLogFields).Error("CRITICAL: No tasks seeded (no valid start URLs, no resume tasks, no initial sitemaps). Crawl will likely terminate.")
		// Optionally, call c.cancelCrawl() here if this is a fatal startup condition
	} else {
		c.log.WithFields(runLogFields).Infof("Finished seeding %d start URLs. Total initial WG count from seeding & resume: %d.",
			initialURLsAddedFromSeed, initialTasksFromDB+initialURLsAddedFromSeed)
	}

	// --- Wait for Waiter Goroutine to Finish (signals all processing is done or context was cancelled) ---
	c.log.WithFields(runLogFields).Info("Main: Waiting for waiter goroutine to complete...")
	select {
	case <-waiterDone: // Waiter completed its sequence (including waiting for wg)
		c.log.WithFields(runLogFields).Info("Main: Waiter finished signal received.")
	case <-c.crawlCtx.Done(): // Main context cancelled while waiting for waiter (should be rare)
		c.log.WithFields(runLogFields).Warnf("Main: Crawl context cancelled while waiting for waiter: %v", c.crawlCtx.Err())
		<-waiterDone // Still wait for waiter to finish its cleanup (closing queues, etc.)
		c.log.WithFields(runLogFields).Info("Main: Waiter finished after context cancellation.")
	}

	// --- Final Summary Logging ---
	duration := time.Since(overallCrawlStartTimeForDuration)
	finalVisitedCount, countErr := c.store.GetVisitedCount()
	if countErr != nil {
		c.log.WithFields(runLogFields).Warnf("Could not get final visited count from DB: %v", countErr)
		finalVisitedCount = -1 // Indicate error in count
	}
	finalProcessedCount := c.processedCounter.Load()
	// Base log already includes site_key. Add domain for clarity in this specific summary.
	summaryLog := c.log.WithFields(logrus.Fields{"domain": c.siteCfg.AllowedDomain})
	summaryLog.Info("========================================================================")
	summaryLog.Info("CRAWL FINISHED")
	summaryLog.Infof("Duration:         %v", duration)
	c.metadataMutex.Lock() // Safely read length for final log
	totalPagesSavedForYAML := len(c.collectedPageMetadata)
	c.metadataMutex.Unlock()
	summaryLog.Infof("Final Stats: Visited (DB Est): %d, Processed Tasks: %d, Pages Saved (for YAML): %d",
		finalVisitedCount, finalProcessedCount, totalPagesSavedForYAML)
	summaryLog.Info("========================================================================")

	return c.crawlCtx.Err() // Return error from context (nil if completed normally, Canceled/DeadlineExceeded otherwise)
}

// closeMappingFile closes the simple TSV mapping file, if it was opened.
func (c *Crawler) closeMappingFile() {
	c.mappingFileMu.Lock()
	defer c.mappingFileMu.Unlock()

	if c.mappingFile != nil {
		// Use crawler's logger (which includes site_key)
		c.log.Infof("Closing TSV mapping file: %s", c.mappingFilePath)
		if err := c.mappingFile.Close(); err != nil {
			c.log.Errorf("Error closing TSV mapping file '%s': %v", c.mappingFilePath, err)
		}
		c.mappingFile = nil // Mark as closed to prevent further writes
	}
}

// closeJSONLFile closes the JSONL output file handle if it was opened.
func (c *Crawler) closeJSONLFile() {
	c.jsonlFileMu.Lock()
	defer c.jsonlFileMu.Unlock()

	if c.jsonlFile != nil {
		c.log.Infof("Closing JSONL output file: %s", c.jsonlFilePath)
		if err := c.jsonlFile.Close(); err != nil {
			c.log.Errorf("Error closing JSONL file '%s': %v", c.jsonlFilePath, err)
		}
		c.jsonlFile = nil
	}
}

// closeChunksFile closes the chunks output file handle if it was opened.
func (c *Crawler) closeChunksFile() {
	c.chunksFileMu.Lock()
	defer c.chunksFileMu.Unlock()

	if c.chunksFile != nil {
		c.log.Infof("Closing chunks output file: %s", c.chunksFilePath)
		if err := c.chunksFile.Close(); err != nil {
			c.log.Errorf("Error closing chunks file '%s': %v", c.chunksFilePath, err)
		}
		c.chunksFile = nil
	}
}

// writeToMappingFile writes a line to the simple TSV mapping file (if enabled and open).
func (c *Crawler) writeToMappingFile(pageURL, absoluteFilePath string, taskLog *logrus.Entry) {
	c.mappingFileMu.Lock()
	defer c.mappingFileMu.Unlock()

	if c.mappingFile == nil { // File wasn't opened or was closed due to an error
		return
	}

	// For TSV, absolute path is generally fine.
	// If relative path is desired:
	// relativePath, err := filepath.Rel(c.siteOutputDir, absoluteFilePath) // Relative to site's output dir
	// if err != nil {
	//    taskLog.Warnf("Could not make path relative for TSV mapping file (%s to %s): %v", c.siteOutputDir, absoluteFilePath, err)
	//    relativePath = absoluteFilePath // Fallback
	// }
	// line := fmt.Sprintf("%s\t%s\n", pageURL, filepath.ToSlash(relativePath))

	line := fmt.Sprintf("%s\t%s\n", pageURL, absoluteFilePath)
	if _, err := c.mappingFile.WriteString(line); err != nil {
		// taskLog already contains URL, depth, site_key, worker_id. Add file-specific info.
		taskLog.WithFields(logrus.Fields{
			"tsv_mapping_file": c.mappingFilePath,
			"line_content":     strings.TrimSpace(line), // Log line without newline for cleaner logs
		}).Errorf("Failed to write to TSV mapping file: %v", err)
	}
}

// writeToJSONLFile writes a page entry to the JSONL output file (if enabled and open).
func (c *Crawler) writeToJSONLFile(page models.PageJSONL, taskLog *logrus.Entry) {
	c.jsonlFileMu.Lock()
	defer c.jsonlFileMu.Unlock()

	if c.jsonlFile == nil {
		return
	}

	jsonBytes, err := json.Marshal(page)
	if err != nil {
		taskLog.WithField("jsonl_file", c.jsonlFilePath).Errorf("Failed to marshal page to JSON: %v", err)
		return
	}

	// Write JSON line followed by newline
	if _, err := c.jsonlFile.Write(append(jsonBytes, '\n')); err != nil {
		taskLog.WithField("jsonl_file", c.jsonlFilePath).Errorf("Failed to write to JSONL file: %v", err)
	}
}

// writeToChunksFile writes chunk entries to the chunks output file (if enabled and open).
func (c *Crawler) writeToChunksFile(chunks []models.ChunkJSONL, taskLog *logrus.Entry) {
	c.chunksFileMu.Lock()
	defer c.chunksFileMu.Unlock()

	if c.chunksFile == nil {
		return
	}

	for _, chunk := range chunks {
		jsonBytes, err := json.Marshal(chunk)
		if err != nil {
			taskLog.WithField("chunks_file", c.chunksFilePath).Errorf("Failed to marshal chunk to JSON: %v", err)
			continue
		}

		// Write JSON line followed by newline
		if _, err := c.chunksFile.Write(append(jsonBytes, '\n')); err != nil {
			taskLog.WithField("chunks_file", c.chunksFilePath).Errorf("Failed to write to chunks file: %v", err)
		}
	}
}

// writeMetadataYAML is called at the end of the crawl to write all collected page metadata to a YAML file.
func (c *Crawler) writeMetadataYAML() error {
	effectiveEnableYAML := config.GetEffectiveEnableMetadataYAML(c.siteCfg, c.appCfg)
	if !effectiveEnableYAML {
		c.log.Info("YAML metadata output is disabled.") // Logger includes site_key
		return nil
	}

	filename := config.GetEffectiveMetadataYAMLFilename(c.siteCfg, c.appCfg)
	yamlFilePath := filepath.Join(c.siteOutputDir, filename)

	c.log.Infof("Preparing to write crawl metadata to: %s", yamlFilePath)

	// Create a serializable representation of SiteConfig.
	// Marshalling to YAML and then unmarshalling to map[string]interface{} is a robust way.
	var siteConfigMap map[string]interface{}
	siteConfigBytes, errCfgMarshal := yaml.Marshal(c.siteCfg) // Marshal original SiteConfig
	if errCfgMarshal != nil {
		c.log.Warnf("Could not marshal site_configuration for YAML metadata: %v", errCfgMarshal)
	} else {
		if errCfgUnmarshal := yaml.Unmarshal(siteConfigBytes, &siteConfigMap); errCfgUnmarshal != nil {
			c.log.Warnf("Could not unmarshal site_configuration into map for YAML metadata: %v", errCfgUnmarshal)
			siteConfigMap = nil // Ensure it's nil if unmarshalling fails
		}
	}

	c.metadataMutex.Lock() // Lock before accessing collectedPageMetadata
	// Create a deep copy of collectedPageMetadata for marshalling.
	// This avoids holding the lock during the potentially time-consuming YAML marshalling.
	pagesToMarshal := make([]models.PageMetadata, len(c.collectedPageMetadata))
	copy(pagesToMarshal, c.collectedPageMetadata)
	c.metadataMutex.Unlock() // Release lock as soon as copy is done

	metadata := models.CrawlMetadata{
		SiteKey:           c.siteKey,
		AllowedDomain:     c.siteCfg.AllowedDomain,
		CrawlStartTime:    c.crawlStartTime, // Recorded at the start of Run()
		CrawlEndTime:      time.Now(),       // Current time as crawl is ending
		TotalPagesSaved:   len(pagesToMarshal),
		SiteConfiguration: siteConfigMap, // Use the map representation
		Pages:             pagesToMarshal,
	}

	yamlData, errMarshal := yaml.Marshal(&metadata)
	if errMarshal != nil {
		// Log error using crawler's logger (includes site_key)
		c.log.Errorf("Failed to marshal crawl metadata to YAML: %v", errMarshal)
		return fmt.Errorf("failed to marshal crawl metadata to YAML for site '%s': %w", c.siteKey, errMarshal)
	}

	errWrite := os.WriteFile(yamlFilePath, yamlData, 0644)
	if errWrite != nil {
		c.log.Errorf("Failed to write metadata YAML file '%s': %v", yamlFilePath, errWrite)
		return fmt.Errorf("failed to write metadata YAML file '%s' for site '%s': %w", yamlFilePath, c.siteKey, errWrite)
	}

	c.log.Infof("Successfully wrote crawl metadata (%d pages) to %s", metadata.TotalPagesSaved, yamlFilePath)
	return nil
}

// getHostSemaphore retrieves or creates a host-specific semaphore.
func (c *Crawler) getHostSemaphore(host string) *semaphore.Weighted {
	c.hostSemaphoresMu.Lock()
	defer c.hostSemaphoresMu.Unlock()

	sem, exists := c.hostSemaphores[host]
	if !exists {
		limit := int64(c.appCfg.MaxRequestsPerHost)
		if limit <= 0 { // Ensure limit is positive
			limit = 2 // Default if invalid config from appCfg
			c.log.Warnf("max_requests_per_host invalid or zero for host '%s', defaulting to %d", host, limit)
		}
		sem = semaphore.NewWeighted(limit)
		c.hostSemaphores[host] = sem
		c.log.WithFields(logrus.Fields{"host": host, "limit": limit}).Debug("Created new host semaphore")
	}
	return sem
}

// worker runs the loop for a single worker goroutine, processing tasks from the priority queue.
func (c *Crawler) worker(workerLog *logrus.Entry) { // workerLog already has site_key and worker_id
	workerLog.Info("Worker starting")
	defer workerLog.Info("Worker finished")

	for {
		// Check context before potentially blocking Pop, to allow quick exit if cancelled
		select {
		case <-c.crawlCtx.Done():
			workerLog.Warnf("Worker shutting down due to context cancellation: %v", c.crawlCtx.Err())
			return
		default:
			// Context is active, proceed to Pop
		}

		// Pop blocks until an item is available or the queue is closed and empty
		workItemPtr, ok := c.pq.Pop()
		if !ok { // Queue closed and empty, means no more work
			if c.crawlCtx.Err() != nil { // Check if closed due to context cancellation
				workerLog.Warnf("Worker shutting down (queue closed & context cancelled): %v", c.crawlCtx.Err())
			} else {
				workerLog.Info("Worker shutting down (queue closed & empty, all tasks processed).")
			}
			return
		}

		// Process the retrieved task
		c.processSinglePageTask(*workItemPtr, workerLog) // Pass the worker's contextualized logger
	}
}

// cleanSiteOutputDir removes the site-specific output directory safely.
// This is typically called when not in resume mode.
func (c *Crawler) cleanSiteOutputDir() error {
	// Use crawler's logger which includes site_key
	c.log.Warnf("Attempting to remove existing site output directory: %s", c.siteOutputDir)

	// Safety Check: Resolve absolute paths to prevent accidental deletion outside base_dir
	absBase, errBase := filepath.Abs(c.appCfg.OutputBaseDir)
	if errBase != nil {
		return fmt.Errorf("safety check failed (resolving base path '%s'): %w", c.appCfg.OutputBaseDir, errBase)
	}
	absSite, errSite := filepath.Abs(c.siteOutputDir)
	if errSite != nil {
		return fmt.Errorf("safety check failed (resolving site path '%s'): %w", c.siteOutputDir, errSite)
	}

	// Ensure site path is truly a subdirectory of the base output directory.
	// Also check it's not empty and not the same as the base path.
	absBaseSeparator := absBase + string(filepath.Separator) // Ensure trailing separator for prefix check
	if absSite != "" && absSite != absBase && strings.HasPrefix(absSite, absBaseSeparator) {
		c.log.Debugf("Safety check passed for RemoveAll. BaseAbs: '%s', SiteAbs: '%s'", absBase, absSite)
		err := os.RemoveAll(c.siteOutputDir)
		if err != nil && !errors.Is(err, os.ErrNotExist) { // ErrNotExist is fine
			return fmt.Errorf("failed remove site output dir '%s': %w", c.siteOutputDir, err)
		} else if err == nil {
			c.log.Infof("Successfully removed existing site output directory: %s", c.siteOutputDir)
		}
		return nil // Success or directory didn't exist
	}

	// Safety check failed. Log and return error to prevent dangerous deletion.
	errMsg := fmt.Sprintf("safety check failed: would not remove dir (BaseDir: '%s', SiteOutputDir: '%s', BaseAbs: '%s', SiteAbs: '%s')",
		c.appCfg.OutputBaseDir, c.siteOutputDir, absBase, absSite)
	c.log.Error(errMsg)
	return errors.New(errMsg)
}

// processSinglePageTask orchestrates the processing pipeline for a single URL (WorkItem).
func (c *Crawler) processSinglePageTask(workItem models.WorkItem, workerLog *logrus.Entry) {
	currentURL := workItem.URL
	currentDepth := workItem.Depth
	// workerLog already has site_key and worker_id. Add URL-specific context for this task.
	taskLog := workerLog.WithFields(logrus.Fields{"url": currentURL, "depth": currentDepth})
	startTime := time.Now()

	// Create per-page timeout context if configured
	taskCtx := c.crawlCtx
	if c.appCfg.PerPageTimeout > 0 {
		var cancel context.CancelFunc
		taskCtx, cancel = context.WithTimeout(c.crawlCtx, c.appCfg.PerPageTimeout)
		defer cancel()
	}

	// Variables to be populated during the task.
	// pageTitle and savedContentPath are used in defer logging.
	// normalizedURLString is used for DB updates and YAML metadata.
	var taskErr error                          // Stores the first critical error encountered in the pipeline.
	var finalStatus models.PageStatus          // PageStatusSuccess or PageStatusFailure (only set for non-skipped tasks)
	var finalErrorType string = "None"         // Categorized error type on failure.
	var skipped bool = false                   // True if task is skipped due to prior processing or policy.
	var pageTitle string               // Populated on successful content extraction.
	var savedContentPath string        // Absolute path to the saved .md file.
	var normalizedURLString string     // Populated from handleSetupAndResumeCheck.
	var rawHTMLHash string             // Hash of raw HTML for incremental crawling.

	// Deferred function for panic recovery, final status logging, DB update, and WaitGroup decrement.
	defer func() {
		panicked := false
		if r := recover(); r != nil { // Panic recovery
			panicked = true
			skipped = false                      // Panic overrides any prior skip status
			taskErr = fmt.Errorf("panic: %v", r) // Capture panic as the task error
			stackTrace := string(debug.Stack())
			// Log panic with full context
			taskLog.WithFields(logrus.Fields{
				"panic_info":  r,
				"duration":    time.Since(startTime).String(), // Use .String() for consistent format
				"stage":       "PanicRecovery",
				"stack_trace": stackTrace,
			}).Error("PANIC recovered in processSinglePageTask")
		}

		// Determine final status and log task outcome
		logFields := logrus.Fields{"duration": time.Since(startTime).String()}
		if pageTitle != "" { // Add page title to log if available
			logFields["page_title"] = pageTitle
		}

		if taskErr != nil { // Task failed
			finalStatus = models.PageStatusFailure
			finalErrorType = utils.CategorizeError(taskErr) // Categorize the error
			logFields["category"] = finalErrorType
			if !panicked { // Log non-panic errors (panic already logged above)
				taskLog.WithFields(logFields).Warnf("Task failed: %v", taskErr)
			}
		} else if skipped { // Task was skipped
			// finalStatus not set for skipped tasks (DB not updated)
			taskLog.WithFields(logFields).Info("Task skipped")
		} else { // Task succeeded
			finalStatus = models.PageStatusSuccess
			finalErrorType = "None"
			if savedContentPath != "" { // Add saved path to log if content was saved
				logFields["saved_path"] = savedContentPath
			}
			taskLog.WithFields(logFields).Info("Task completed successfully")
		}

		// Update DB status if the task was not skipped and URL was successfully normalized
		if !skipped && normalizedURLString != "" {
			pageEntry := &models.PageDBEntry{
				Status:      finalStatus,
				ErrorType:   finalErrorType,
				LastAttempt: time.Now(),
				Depth:       currentDepth,
			}
			if finalStatus == models.PageStatusSuccess { // Mark ProcessedAt only on success
				pageEntry.ProcessedAt = pageEntry.LastAttempt
				pageEntry.ContentHash = rawHTMLHash // Store hash for incremental crawling
			}
			// Update page status in the persistent store
			if dbUpdateErr := c.store.UpdatePageStatus(normalizedURLString, pageEntry); dbUpdateErr != nil {
				taskLog.Errorf("Failed update final DB status for '%s' to '%s': %v", normalizedURLString, finalStatus, dbUpdateErr)
			}
		} else if !skipped { // Not skipped, but normalization might have failed
			taskLog.Warnf("URL '%s' normalization failed or was not set; cannot update DB status.", currentURL)
		}

		c.processedCounter.Add(1) // Increment global counter for processed tasks
		c.wg.Done()               // Decrement main WaitGroup, signaling this task is finished
	}() // End defer.

	// Helper function to store the first critical error encountered in the pipeline.
	// Returns true if an error was handled (i.e., err was not nil).
	handleTaskError := func(err error) bool {
		if err == nil {
			return false // No error to handle
		}
		if taskErr == nil { // If no critical error has been recorded yet for this task
			taskErr = err // Store this error as the task's primary error
		}
		return true // Indicate that an error was handled
	}

	// --- Orchestration Pipeline for Processing a Single Page ---

	// 1. Setup & Resume Check: Parse URL, normalize, check DB if resuming.
	var parsedOriginalURL *url.URL // Parsed version of currentURL
	var host string                // Hostname from currentURL
	var setupErr error             // Error from this stage
	var setupShouldSkip bool
	// normalizedURLString is populated here for use in defer and metadata
	parsedOriginalURL, normalizedURLString, host, setupShouldSkip, setupErr = c.handleSetupAndResumeCheck(currentURL, taskLog)
	if handleTaskError(setupErr) {
		return
	} // If error, set taskErr and exit
	if setupShouldSkip {
		skipped = true
		return
	} // If skipped, set flag and exit
	taskLog = taskLog.WithField("host", host) // Add host to subsequent logs for this task

	// 2. Policy Checks: Depth, robots.txt.
	if handleTaskError(c.runPolicyChecks(parsedOriginalURL, currentDepth, taskLog)) {
		return
	}

	// 3. Acquire Resources: Semaphores (global, per-host), apply rate limit.
	cleanupResources, acquireErr := c.acquireResources(host, taskLog)
	defer cleanupResources() // Ensure semaphores are released when task finishes
	if handleTaskError(acquireErr) {
		return
	}

	// 4. Fetch & Validate Page: HTTP GET with retries, validate response and final URL.
	finalURL, resp, fetchErr := c.fetchAndValidatePage(currentURL, parsedOriginalURL, taskLog)
	// fetchAndValidatePage closes resp.Body on error if resp is not nil.
	if handleTaskError(fetchErr) {
		return
	}
	// If successful, resp.Body is open and passed to the next stage.

	// 5. Read & Parse Body: Read response body into goquery.Document.
	var parseBodyErr error
	var originalDoc *goquery.Document
	originalDoc, rawHTMLHash, parseBodyErr = c.readAndParseBody(resp, finalURL, taskLog) // Closes resp.Body
	if handleTaskError(parseBodyErr) {
		return
	}

	// 5.5. Incremental Crawling Check: Compare hash with stored hash
	if c.appCfg.EnableIncremental {
		existingHash, exists, hashErr := c.store.GetPageContentHash(normalizedURLString)
		if hashErr != nil {
			taskLog.Warnf("Failed to check content hash for incremental crawl: %v", hashErr)
			// Continue processing despite hash check error
		} else if exists && existingHash == rawHTMLHash {
			taskLog.Info("Page unchanged (hash match) - skipping processing")
			skipped = true
			return
		} else if exists {
			taskLog.Debug("Page content changed - will reprocess")
		} else {
			taskLog.Debug("New page (no previous hash) - will process")
		}
	}

	// 6. Extract & Queue Links: Find new links on the page and add to priority queue.
	// Non-critical errors (e.g., DB error during link check) are logged within linkProcessor.
	if _, linkErr := c.linkProcessor.ExtractAndQueueLinks(originalDoc, finalURL, currentDepth, c.siteCfg, &c.wg, taskLog); linkErr != nil {
		taskLog.Warnf("Non-fatal error encountered during link extraction/queueing: %v", linkErr)
	}

	// 7. Process & Save Content: Extract content, process images/links, convert to MD, save.
	var tempPageTitle, tempSavedPath string // Use temp vars for return values from contentProcessor
	var tempImageCount int
	var contentErr error
	// pageTitle and savedContentPath (function-scoped) will be set from these if successful.
	tempPageTitle, tempSavedPath, tempImageCount, contentErr = c.contentProcessor.ExtractProcessAndSaveContent(originalDoc, finalURL, c.siteCfg, c.siteOutputDir, taskLog, taskCtx)
	if handleTaskError(contentErr) { // If content processing/saving fails, set taskErr and exit.
		return
	}
	// If successful, assign to function-scoped variables for use in defer logging and metadata collection.
	pageTitle = tempPageTitle
	savedContentPath = tempSavedPath // This is the ABSOLUTE path to the saved .md file.

	// --- After successful content saving (taskErr is still nil here) ---
	// This block executes only if all prior critical stages succeeded.
	if savedContentPath != "" { // Ensure a file path was actually returned (i.e., save was successful)
		// Write to simple TSV mapping file (if enabled and file is open)
		if c.mappingFile != nil {
			c.writeToMappingFile(finalURL.String(), savedContentPath, taskLog)
		}

		// Check if we need to read the markdown file for metadata or JSONL output
		enableYAML := config.GetEffectiveEnableMetadataYAML(c.siteCfg, c.appCfg)
		enableJSONL := config.GetEffectiveEnableJSONLOutput(c.siteCfg, c.appCfg)

		var markdownBytes []byte
		var contentHash string
		var tokenCount int
		if enableYAML || enableJSONL {
			var readErr error
			markdownBytes, readErr = os.ReadFile(savedContentPath)
			if readErr != nil {
				taskLog.Warnf("Failed to read saved markdown file '%s': %v", savedContentPath, readErr)
			} else {
				contentHash = utils.CalculateStringSHA256(string(markdownBytes))
				// Calculate token count if enabled
				if c.appCfg.EnableTokenCounting {
					tokenCount = process.CountTokens(string(markdownBytes))
				}
			}
		}

		// Collect YAML Page Metadata (if YAML metadata is enabled for this site)
		if enableYAML {
			// Calculate path relative to the site's output directory for portability in metadata.yaml
			relativeLocalPath, relErr := filepath.Rel(c.siteOutputDir, savedContentPath)
			if relErr != nil {
				taskLog.Warnf("Could not make path relative for metadata.yaml (Base: '%s', Target: '%s'): %v",
					c.siteOutputDir, savedContentPath, relErr)
				relativeLocalPath = savedContentPath // Fallback to absolute path if error
			}

			// Create PageMetadata entry
			pageMeta := models.PageMetadata{
				OriginalURL:   finalURL.String(),                   // Final URL after redirects
				NormalizedURL: normalizedURLString,                 // Normalized URL used for DB keys, etc.
				LocalFilePath: filepath.ToSlash(relativeLocalPath), // Store relative path with forward slashes
				Title:         pageTitle,                           // Extracted page title
				Depth:         currentDepth,                        // Crawl depth
				ProcessedAt:   time.Now(),                          // Timestamp of this successful processing
				ContentHash:   contentHash,                         // SHA-256 hash of the Markdown content
				ImageCount:    tempImageCount,                       // Count of successfully rewritten images
				TokenCount:    tokenCount,                          // Token count for LLM context planning
			}

			// Add to the collected metadata slice (thread-safe)
			c.metadataMutex.Lock()
			c.collectedPageMetadata = append(c.collectedPageMetadata, pageMeta)
			c.metadataMutex.Unlock()
		}

		// Write JSONL output (if enabled)
		if enableJSONL && c.jsonlFile != nil && markdownBytes != nil {
			// Extract headings from markdown content
			headings := process.ExtractHeadings(markdownBytes)

			// Extract links and images from markdown (simple regex extraction)
			links, images := extractLinksAndImages(string(markdownBytes))

			pageJSONL := models.PageJSONL{
				URL:         finalURL.String(),
				Title:       pageTitle,
				Content:     string(markdownBytes),
				Headings:    headings,
				Links:       links,
				Images:      images,
				ContentHash: contentHash,
				CrawledAt:   time.Now().Format(time.RFC3339),
				Depth:       currentDepth,
				TokenCount:  tokenCount,
			}
			c.writeToJSONLFile(pageJSONL, taskLog)
		}

		// Write chunks output (if chunking enabled)
		enableChunking := config.GetEffectiveChunkingEnabled(c.siteCfg, c.appCfg)
		if enableChunking && c.chunksFile != nil && markdownBytes != nil {
			// Get chunking configuration
			chunkCfg := process.ChunkerConfig{
				MaxChunkSize: config.GetEffectiveChunkingMaxSize(c.siteCfg, c.appCfg),
				ChunkOverlap: config.GetEffectiveChunkingOverlap(c.siteCfg, c.appCfg),
			}

			// Chunk the markdown content
			chunks, chunkErr := process.ChunkMarkdown(string(markdownBytes), chunkCfg)
			if chunkErr != nil {
				taskLog.Warnf("Failed to chunk markdown content: %v", chunkErr)
			} else if len(chunks) > 0 {
				// Convert to ChunkJSONL format
				crawledAt := time.Now().Format(time.RFC3339)
				chunkJSONLs := make([]models.ChunkJSONL, len(chunks))
				for i, chunk := range chunks {
					chunkJSONLs[i] = models.ChunkJSONL{
						URL:              finalURL.String(),
						ChunkIndex:       i,
						Content:          chunk.Content,
						HeadingHierarchy: chunk.HeadingHierarchy,
						TokenCount:       chunk.TokenCount,
						PageTitle:        pageTitle,
						CrawledAt:        crawledAt,
					}
				}
				c.writeToChunksFile(chunkJSONLs, taskLog)
				taskLog.Debugf("Wrote %d chunks for page", len(chunks))
			}
		}
	}
	// If execution reaches here, taskErr is still nil, indicating success.
	// The deferred function will handle logging this success and updating DB.
}

// --- Helper methods for processSinglePageTask stages ---

// handleSetupAndResumeCheck parses the URL, normalizes it, and checks its status in the DB.
// It determines if the URL should be skipped (e.g., already successfully processed).
func (c *Crawler) handleSetupAndResumeCheck(currentURL string, taskLog *logrus.Entry) (parsedURL *url.URL, normalizedURLStr string, host string, shouldSkip bool, err error) {
	taskLog.Debug("Performing setup and resume check...")
	parsedTargetURL, parseErr := url.Parse(currentURL) // Use a distinct variable name for initial parsing
	if parseErr != nil {
		err = fmt.Errorf("%w: parsing URL '%s': %w", utils.ErrParsing, currentURL, parseErr)
		return nil, "", "", false, err
	}
	parsedURL = parsedTargetURL // Assign to the return variable

	normalizedURLStr = parse.NormalizeURL(parsedURL) // Get the normalized string representation
	host = parsedURL.Hostname()
	if host == "" && parsedURL.Scheme != "file" { // Check scheme for file URLs which don't have a host
		err = fmt.Errorf("URL '%s' missing host (and not a file:// URL)", currentURL)
		return parsedURL, normalizedURLStr, "", false, err
	}

	// Check status in the persistent store
	pageStatus, _, checkErr := c.store.CheckPageStatus(normalizedURLStr)
	if checkErr != nil {
		taskLog.Errorf("DB error checking status for '%s', proceeding as if not found: %v", normalizedURLStr, checkErr)
		// Do not return 'err' here; let the crawl attempt proceed if DB check fails.
		// The error is logged, and status will default to PageStatusNotFound effectively.
	} else if pageStatus == models.PageStatusSuccess {
		taskLog.Info("Skipping already successfully processed page (from DB).")
		shouldSkip = true
		return parsedURL, normalizedURLStr, host, shouldSkip, nil // Return to skip
	} else if pageStatus == models.PageStatusFailure {
		taskLog.Warnf("Retrying previously failed page (from DB).")
	} else if pageStatus == models.PageStatusPending {
		taskLog.Debug("Processing page previously marked pending (from DB).")
	} // If PageStatusNotFound or any other unexpected status, proceed to crawl normally.

	return parsedURL, normalizedURLStr, host, false, nil // Proceed with crawling
}

// runPolicyChecks verifies if the URL adheres to defined crawl policies (depth, robots.txt).
func (c *Crawler) runPolicyChecks(parsedURL *url.URL, depth int, taskLog *logrus.Entry) error {
	taskLog.Debug("Running policy checks...")
	// Depth Check
	if c.siteCfg.MaxDepth > 0 && depth >= c.siteCfg.MaxDepth {
		err := utils.ErrMaxDepthExceeded
		taskLog.Infof("%s (Current Depth: %d, Max Depth: %d)", err.Error(), depth, c.siteCfg.MaxDepth)
		return err // Return error to stop processing this URL
	}

	// Robots.txt Check
	userAgent := c.siteCfg.UserAgent
	if userAgent == "" { // Fallback to global default User-Agent if site-specific one is not set
		userAgent = c.appCfg.DefaultUserAgent
	}
	if !c.robotsHandler.TestAgent(parsedURL, userAgent, c.crawlCtx) { // TestAgent handles fetching/caching robots.txt
		err := fmt.Errorf("%w: URL '%s' disallowed for agent '%s'", utils.ErrRobotsDisallowed, parsedURL.RequestURI(), userAgent)
		taskLog.Warn(err.Error()) // Log warning
		return err                // Return error to stop processing
	}
	taskLog.Debug("Policy checks passed.")
	return nil
}

// acquireResources attempts to acquire necessary semaphores (global, per-host) and applies rate limiting.
// Returns a cleanup function to release semaphores.
func (c *Crawler) acquireResources(host string, taskLog *logrus.Entry) (cleanupFunc func(), err error) {
	taskLog.Debug("Acquiring resources (semaphores, rate limit)...")
	acquiredHostSem, acquiredGlobalSem := false, false
	// Cleanup function will release acquired semaphores.
	cleanupFunc = func() {
		if acquiredHostSem {
			c.getHostSemaphore(host).Release(1)
			taskLog.Debugf("Released host semaphore for: %s", host)
		}
		if acquiredGlobalSem {
			c.globalSemaphore.Release(1)
			taskLog.Debug("Released global semaphore.")
		}
	}

	semTimeout := c.appCfg.SemaphoreAcquireTimeout // Get timeout from app config

	// 1. Acquire Host-Specific Semaphore
	hostSem := c.getHostSemaphore(host)
	ctxHost, cancelHost := context.WithTimeout(c.crawlCtx, semTimeout) // Context for acquiring host semaphore
	defer cancelHost()                                                 // Ensure timer is cleaned up
	taskLog.Debugf("Attempting to acquire host semaphore for: %s (timeout: %v)", host, semTimeout)
	if semErr := hostSem.Acquire(ctxHost, 1); semErr != nil {
		// Wrap error for better context (e.g., distinguish timeout from other errors)
		return cleanupFunc, utils.WrapErrorf(semErr, "acquire host semaphore for '%s'", host)
	}
	acquiredHostSem = true
	taskLog.Debugf("Acquired host semaphore for: %s", host)

	// 2. Acquire Global Semaphore
	ctxGlobal, cancelGlobal := context.WithTimeout(c.crawlCtx, semTimeout) // Context for acquiring global semaphore
	defer cancelGlobal()                                                   // Ensure timer is cleaned up
	taskLog.Debugf("Attempting to acquire global semaphore (timeout: %v)", semTimeout)
	if semErr := c.globalSemaphore.Acquire(ctxGlobal, 1); semErr != nil {
		// If global semaphore fails, host semaphore (if acquired) will be released by defer cleanupFunc.
		return cleanupFunc, utils.WrapErrorf(semErr, "acquire global semaphore")
	}
	acquiredGlobalSem = true
	taskLog.Debug("Acquired global semaphore.")

	// 3. Apply Rate Limit Delay (after acquiring semaphores to avoid delaying semaphore acquisition)
	// Determine effective delay: site-specific, then global, then none if both are zero/negative.
	delayPerHost := c.siteCfg.DelayPerHost
	if delayPerHost <= 0 { // If site-specific delay is not positive, use global default
		delayPerHost = c.appCfg.DefaultDelayPerHost
	}
	if delayPerHost > 0 { // Only apply delay if it's positive
		c.rateLimiter.ApplyDelay(host, delayPerHost) // ApplyDelay logs if it sleeps
	}

	taskLog.Debug("Resource acquisition successful.")
	return cleanupFunc, nil // Success
}

// fetchAndValidatePage performs the HTTP GET request with retries and validates the response.
// It handles redirects and ensures the final URL is within scope and allowed by robots.txt.
// If successful, returns the final URL and an open http.Response (caller must close Body).
// On error, it ensures resp.Body is closed if resp is not nil.
func (c *Crawler) fetchAndValidatePage(reqURLString string, originalParsedURL *url.URL, taskLog *logrus.Entry) (finalURL *url.URL, resp *http.Response, err error) {
	taskLog.Debugf("Fetching page: %s", reqURLString)
	userAgent := c.siteCfg.UserAgent
	if userAgent == "" { // Fallback to global default User-Agent
		userAgent = c.appCfg.DefaultUserAgent
	}

	// Create HTTP request with context for cancellation
	req, reqErr := http.NewRequestWithContext(c.crawlCtx, "GET", reqURLString, nil)
	if reqErr != nil {
		// Wrap error for clarity
		return nil, nil, fmt.Errorf("%w: creating request for '%s': %w", utils.ErrRequestCreation, reqURLString, reqErr)
	}
	req.Header.Set("User-Agent", userAgent) // Set User-Agent header

	// Fetch using the configured Fetcher component (handles retries, HTTP errors)
	resp, fetchErr := c.fetcher.FetchWithRetry(req, c.crawlCtx)
	// Update rate limiter's last request time for the host *after* the attempt (success or failure)
	c.rateLimiter.UpdateLastRequestTime(originalParsedURL.Hostname())

	if fetchErr != nil {
		// Fetcher component already logged details of fetch/retry failures.
		// It also ensures resp.Body is closed if resp is not nil and an error occurred.
		return nil, resp, fetchErr // Propagate error; resp might be non-nil if an HTTP error occurred (e.g., 404)
	}
	// If fetchErr is nil, we have a successful 2xx response, and resp.Body is open.

	// --- Post-fetch Validation (after successful fetch and potential redirects) ---
	finalURL = resp.Request.URL            // URL after any redirects handled by the HTTP client
	if finalURL.String() != reqURLString { // Log if URL changed due to redirect
		taskLog = taskLog.WithField("final_url", finalURL.String())
		taskLog.Info("URL redirected.")
	}
	taskLog.Debug("Validating final URL scope and policies...")

	finalHost := finalURL.Hostname()
	finalPath := finalURL.Path
	if finalPath == "" { // Normalize empty path to root
		finalPath = "/"
	}

	// Scope Check: Domain and Path Prefix for the final URL
	if finalHost != c.siteCfg.AllowedDomain || !strings.HasPrefix(finalPath, c.siteCfg.AllowedPathPrefix) {
		err = fmt.Errorf("%w: redirected URL '%s' out of scope (Expected Domain: '%s', Path Prefix: '%s')",
			utils.ErrScopeViolation, finalURL.String(), c.siteCfg.AllowedDomain, c.siteCfg.AllowedPathPrefix)
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()         // Must close body before returning
		return finalURL, nil, err // Return nil for resp as its body is now closed
	}

	// Scope Check: Disallowed Path Patterns for the final URL
	for _, pattern := range c.compiledDisallowedPatterns {
		if pattern.MatchString(finalURL.Path) { // Match against the path part of the final URL
			err = fmt.Errorf("%w: redirected URL '%s' matches disallowed pattern '%s'",
				utils.ErrScopeViolation, finalURL.String(), pattern.String())
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return finalURL, nil, err
		}
	}

	// Robots.txt Check for the final URL, especially if the host changed due to redirect
	if finalHost != originalParsedURL.Hostname() { // If redirected to a different host (within allowed_domain)
		taskLog.Debugf("Host changed due to redirect (%s -> %s), re-checking robots.txt for final URL.",
			originalParsedURL.Hostname(), finalHost)
		if !c.robotsHandler.TestAgent(finalURL, userAgent, c.crawlCtx) {
			err = fmt.Errorf("%w: redirected URL '%s' disallowed by robots.txt on new host",
				utils.ErrRobotsDisallowed, finalURL.String())
			io.Copy(io.Discard, resp.Body)
			resp.Body.Close()
			return finalURL, nil, err
		}
	}

	// Basic Content-Type Check (informational, doesn't stop processing)
	contentType := resp.Header.Get("Content-Type")
	ctLower := strings.ToLower(contentType)
	// Check for common HTML content types
	if !strings.HasPrefix(ctLower, "text/html") && !strings.HasPrefix(ctLower, "application/xhtml+xml") {
		taskLog.Warnf("Unexpected Content-Type '%s' for '%s'. Proceeding with parsing attempt.", contentType, finalURL.String())
	}

	taskLog.Debug("Fetch and validation successful.")
	return finalURL, resp, nil // Success: return final URL and open response
}

// readAndParseBody reads the HTTP response body and parses it into a goquery.Document.
// It ensures resp.Body is closed after reading.
// Returns the goquery document and the raw HTML hash for incremental crawling.
func (c *Crawler) readAndParseBody(resp *http.Response, finalURL *url.URL, taskLog *logrus.Entry) (doc *goquery.Document, rawHTMLHash string, err error) {
	taskLog.Debugf("Reading response body from: %s", finalURL.String())
	defer resp.Body.Close() // Ensure response body is closed when this function returns

	// Read the entire response body. For very large pages, consider alternatives if memory becomes an issue.
	bodyBytes, readErr := io.ReadAll(resp.Body) // Go 1.16+ can use io.ReadAll directly
	if readErr != nil {
		return nil, "", fmt.Errorf("%w: reading body from '%s': %w", utils.ErrResponseBodyRead, finalURL.String(), readErr)
	}
	taskLog.Debugf("Read %d bytes from response body of %s", len(bodyBytes), finalURL.String())

	// Calculate hash of raw HTML for incremental crawling
	rawHTMLHash = utils.CalculateStringSHA256(string(bodyBytes))

	// Parse the HTML content using goquery
	doc, parseErr := goquery.NewDocumentFromReader(bytes.NewReader(bodyBytes))
	if parseErr != nil {
		return nil, rawHTMLHash, fmt.Errorf("%w: parsing HTML from '%s': %w", utils.ErrParsing, finalURL.String(), parseErr)
	}

	taskLog.Debug("Successfully parsed HTML into goquery document.")
	return doc, rawHTMLHash, nil
}

// extractLinksAndImages extracts markdown links and image references from markdown content.
// Returns two slices: links (from [text](url)) and images (from ![alt](url)).
func extractLinksAndImages(markdown string) (links []string, images []string) {
	linkMatches := mdLinkRe.FindAllStringSubmatch(markdown, -1)
	for _, match := range linkMatches {
		if len(match) >= 3 {
			linkURL := strings.TrimSpace(match[2])
			if linkURL != "" {
				links = append(links, linkURL)
			}
		}
	}

	imageMatches := mdImageRe.FindAllStringSubmatch(markdown, -1)
	for _, match := range imageMatches {
		if len(match) >= 3 {
			imageURL := strings.TrimSpace(match[2])
			if imageURL != "" {
				images = append(images, imageURL)
			}
		}
	}

	return links, images
}
