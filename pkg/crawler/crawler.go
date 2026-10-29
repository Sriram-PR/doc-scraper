package crawler

import (
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
	"golang.org/x/sync/semaphore"
	"log/slog"

	"github.com/Sriram-PR/doc-scraper/v2/pkg/config"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/fetch"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/models"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/pagebudget"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/parse"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/process"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/queue"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/sitemap"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/storage"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/storage/index"
	"github.com/Sriram-PR/doc-scraper/v2/pkg/utils"
)

// Pre-compiled regexes for markdown link and image extraction.
var (
	mdLinkRe  = regexp.MustCompile(`(?:^|[^!])\[([^\]]*)\]\(([^)]+)\)`)
	mdImageRe = regexp.MustCompile(`!\[([^\]]*)\]\(([^)]+)\)`)
)

// Crawler orchestrates the web crawling process for a single configured site
type Crawler struct {
	log                        *slog.Logger // Logger contextualized with site_key
	appCfg                     *config.AppConfig
	siteCfg                    *config.SiteConfig
	resolved                   *config.ResolvedSiteConfig
	siteKey                    string
	siteOutputDir              string // Base output directory for *this specific site's* files
	compiledDisallowedPatterns []*regexp.Regexp

	store            storage.VisitedStore
	pq               *queue.ThreadSafePriorityQueue
	fetcher          fetch.HTTPFetcher
	robotsHandler    *fetch.RobotsHandler
	rateLimiter      *fetch.RateLimiter
	sitemapProcessor *sitemap.SitemapProcessor
	contentProcessor *process.ContentProcessor
	imageProcessor   *process.ImageProcessor
	linkProcessor    *process.LinkProcessor

	globalSemaphore *semaphore.Weighted
	hostSemPool     *fetch.HostSemaphorePool

	wg               sync.WaitGroup // Main WaitGroup for all active tasks (pages, sitemaps)
	workersWg        sync.WaitGroup
	processedCounter atomic.Int64
	succeededCounter atomic.Int64
	unchangedCounter atomic.Int64
	savedCounter     atomic.Int64
	failures         failureTally
	crawlCtx         context.Context
	cancelCrawl      context.CancelFunc

	sitemapQueue    chan string
	foundSitemaps   map[string]bool // Tracks sitemaps discovered by robots.txt
	foundSitemapsMu sync.Mutex

	// Pages that answered 404 or 410; dropped from the corpus when the run completes.
	gonePages   []*url.URL
	gonePagesMu sync.Mutex

	// Output file management (Markdown files + JSONL + llms.txt)
	output *OutputManager

	// Optional crawl-history index handle; passed to OutputManager in Run.
	idx *index.Index

	// nil means work directly in the live output dir and visited DB.
	stage       *Stage
	allowShrink bool
	carry       *carryForward

	// Optional periodic progress reporter (e.g. MCP job status updates).
	// Invoked from the progress reporter goroutine, never per-page, so it
	// is safe to do a small amount of work (lock + write) inside.
	progressCallback func(processed, queued int64)
}

// CrawlerOptions contains optional parameters for NewCrawler
type CrawlerOptions struct {
	// SharedSemaphore allows sharing a global semaphore across multiple crawlers
	// If nil, the crawler creates its own semaphore based on appCfg.MaxRequests
	SharedSemaphore *semaphore.Weighted

	// ProgressCallback receives periodic (~30s) updates of processed page
	// count and remaining queue depth. Fired one final time when the
	// progress reporter exits so observers see the terminal state.
	ProgressCallback func(processed, queued int64)

	// Index, if non-nil, receives a crawl-history record at end of crawl.
	// nil disables history capture (useful for tests and the get_page MCP path).
	Index *index.Index

	// Stage, if non-nil, selects the output dir to write into and is committed
	// over the live corpus when a staged crawl completes. The store passed to
	// the crawler must live at Stage.DBPath(); a successful staged Run closes it.
	Stage *Stage

	// AllowShrink publishes a crawl even if it lost many pages or failed many
	// pages transiently; a crawl that saved nothing is still refused.
	AllowShrink bool
}

// NewCrawlerWithOptions wires a Crawler and its components; opts may be nil to use defaults.
func NewCrawlerWithOptions(
	appCfg *config.AppConfig,
	siteCfg *config.SiteConfig,
	siteKey string,
	baseLogger *slog.Logger,
	store storage.VisitedStore,
	fetcher fetch.HTTPFetcher,
	rateLimiter *fetch.RateLimiter,
	crawlCtx context.Context,
	cancelCrawl context.CancelFunc,
	resume bool,
	opts *CrawlerOptions,
) (*Crawler, error) {

	logger := baseLogger.With("site_key", siteKey)

	compiledDisallowedPatterns, err := utils.CompileRegexPatterns(siteCfg.DisallowedPathPatterns)
	if err != nil {
		return nil, fmt.Errorf("compiling disallowed patterns for site '%s': %w", siteKey, err)
	}
	if len(compiledDisallowedPatterns) > 0 {
		logger.Info(fmt.Sprintf("Compiled %d disallowed path patterns.", len(compiledDisallowedPatterns)))
	}

	siteOutputDir := appCfg.SiteOutputDir(siteKey)
	if opts != nil && opts.Stage != nil {
		siteOutputDir = opts.Stage.OutputDir()
	}

	var globalSem *semaphore.Weighted
	if opts != nil && opts.SharedSemaphore != nil {
		globalSem = opts.SharedSemaphore
		logger.Debug("Using shared global semaphore")
	} else {
		globalSem = semaphore.NewWeighted(int64(appCfg.MaxRequests))
	}

	hostSemPool := fetch.NewHostSemaphorePool(appCfg.MaxRequestsPerHost, logger)
	go hostSemPool.RunEviction(crawlCtx, 5*time.Minute)

	resolved := config.NewResolvedSiteConfig(siteCfg, appCfg)

	c := &Crawler{
		log:                        logger,
		appCfg:                     appCfg,
		siteCfg:                    siteCfg,
		resolved:                   resolved,
		siteKey:                    siteKey,
		siteOutputDir:              siteOutputDir,
		compiledDisallowedPatterns: compiledDisallowedPatterns,
		store:                      store,
		pq:                         queue.NewThreadSafePriorityQueue(logger),
		fetcher:                    fetcher,
		rateLimiter:                rateLimiter,
		globalSemaphore:            globalSem,
		hostSemPool:                hostSemPool,
		crawlCtx:                   crawlCtx,
		cancelCrawl:                cancelCrawl,
		sitemapQueue:               make(chan string, 100),
		foundSitemaps:              make(map[string]bool),
	}
	if opts != nil {
		c.progressCallback = opts.ProgressCallback
		c.idx = opts.Index
		c.stage = opts.Stage
		c.allowShrink = opts.AllowShrink
	}

	c.output = NewOutputManager(logger, resolved, siteCfg, siteKey, siteOutputDir)

	c.robotsHandler = fetch.NewRobotsHandler(fetcher, rateLimiter, c.globalSemaphore, c.hostSemPool, c, appCfg, logger)
	c.sitemapProcessor = sitemap.NewSitemapProcessor(c.sitemapQueue, c.pq, c.store, c.fetcher, c.rateLimiter, c.globalSemaphore, c.hostSemPool, c.compiledDisallowedPatterns, c.siteCfg, c.appCfg, logger, &c.wg)
	c.imageProcessor = process.NewImageProcessor(c.store, c.fetcher, c.robotsHandler, c.rateLimiter, c.globalSemaphore, c.hostSemPool, c.resolved, c.appCfg, logger)
	c.contentProcessor = process.NewContentProcessor(c.imageProcessor, c.appCfg, logger)
	c.linkProcessor = process.NewLinkProcessor(c.store, c.pq, c.compiledDisallowedPatterns, logger)

	return c, nil
}

// deriveMode picks the index.Mode label for a crawl run. EnableIncremental
// implies resume, so the incremental check wins.
func deriveMode(resume, incremental bool) index.Mode {
	switch {
	case incremental:
		return index.ModeIncremental
	case resume:
		return index.ModeResume
	default:
		return index.ModeFull
	}
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
		c.log.Debug(fmt.Sprintf("Crawler notified of newly found sitemap: %s", sitemapURL))
	}
}

type CrawlerProgress struct {
	SiteKey string
	// PagesProcessed counts attempted page tasks, failed ones included.
	PagesProcessed int64
	// PagesSaved counts pages written to output this run; noindex pages
	// succeed without being saved.
	PagesSaved  int64
	PagesFailed int64
	// PagesUnchanged counts incremental re-checks whose content hash matched;
	// they are fetched but not reprocessed, so PagesProcessed excludes them.
	PagesUnchanged int64
	// PagesCarriedForward counts pages that failed temporarily in a staged
	// crawl and kept their copy from the live corpus.
	PagesCarriedForward int64
	PagesQueued         int
	IsRunning           bool
	Failures            Failures
}

func (c *Crawler) GetProgress() CrawlerProgress {
	f := c.failures.snapshot()
	return CrawlerProgress{
		SiteKey:             c.siteKey,
		PagesProcessed:      c.processedCounter.Load(),
		PagesSaved:          c.savedCounter.Load(),
		PagesFailed:         c.processedCounter.Load() - c.succeededCounter.Load() - f.Noindex,
		PagesUnchanged:      c.unchangedCounter.Load(),
		PagesCarriedForward: c.carry.count(),
		PagesQueued:         c.pq.Len(),
		IsRunning:           c.crawlCtx.Err() == nil,
		Failures:            f,
	}
}

// Run starts the crawling process for the configured site and blocks until completion or cancellation.
func (c *Crawler) Run(resume bool) (err error) {
	c.output.crawlStartTime = time.Now().UTC()
	c.output.SetIndex(c.idx, deriveMode(resume, c.appCfg.EnableIncremental))
	runLog := c.log.With("domain", c.siteCfg.AllowedDomain, "resume", resume)
	runLog.Info(fmt.Sprintf("Crawl starting with %d worker(s)...", c.appCfg.NumWorkers))
	overallStart := time.Now()

	defer func() { err = c.finish(err, runLog) }()

	validStartURLs, firstValidParsedURL, err := c.validateStartURLs(runLog)
	if err != nil {
		return err
	}

	if err := c.prepareOutputDir(resume, runLog); err != nil {
		return err
	}
	c.output.OpenFiles(resume)
	if c.stage != nil && c.stage.Staged && c.resolved.EnableJSONLOutput {
		cf, err := newCarryForward(c.stage.liveOut, c.resolved.JSONLOutputFilename, runLog)
		if err != nil {
			runLog.Warn("Pages that fail temporarily will not keep their previous copy", "err", err)
		}
		c.carry = cf
	}

	initialTasksFromDB, err := c.requeueIncomplete(resume, runLog)
	if err != nil {
		return err
	}

	c.startWorkers(runLog)
	c.sitemapProcessor.Start(c.crawlCtx)

	// Guard token: keep the WaitGroup above zero until seeding finishes so the
	// waiter's wg.Wait() cannot observe a transient zero (fresh crawl, robots
	// with no sitemaps) and shut the queues down before the seed URLs are even
	// enqueued.
	c.wg.Add(1)

	waiterDone := make(chan struct{})
	go c.runWaiter(firstValidParsedURL, runLog, waiterDone)

	initialURLsAddedFromSeed := c.seedStartURLs(validStartURLs, runLog)
	c.wg.Done() // seeding complete; release the guard token
	if initialURLsAddedFromSeed == 0 && initialTasksFromDB == 0 && len(c.foundSitemaps) == 0 {
		if resume {
			runLog.Info("Resume: no incomplete tasks and no new seeds; prior crawl appears complete, nothing to do.")
		} else {
			runLog.Error("CRITICAL: No tasks seeded (no valid start URLs, no resume tasks, no initial sitemaps). Crawl will likely terminate.")
		}
	} else {
		runLog.Info(fmt.Sprintf("Finished seeding %d start URLs. Total initial WG count from seeding & resume: %d.",
			initialURLsAddedFromSeed, initialTasksFromDB+initialURLsAddedFromSeed))
	}

	runLog.Info("Main: Waiting for waiter goroutine to complete...")
	select {
	case <-waiterDone:
		runLog.Info("Main: Waiter finished signal received.")
	case <-c.crawlCtx.Done():
		runLog.Warn(fmt.Sprintf("Main: Crawl context cancelled while waiting for waiter: %v", c.crawlCtx.Err()))
		<-waiterDone // Still wait for waiter to finish its cleanup (closing queues, etc.)
		runLog.Info("Main: Waiter finished after context cancellation.")
	}
	// On cancellation the waiter stops waiting on tasks, so a worker can still be
	// saving a page; finalizing before it exits would drop that page's JSONL
	// record while the visited DB already marks it done.
	c.workersWg.Wait()

	c.logRunSummary(overallStart, runLog)
	return c.crawlCtx.Err()
}

// finish finalizes the output files and publishes the result. Only a completed
// run is recorded in crawl history and, when staged, swapped over the live
// corpus; the search index follows whatever corpus is live.
func (c *Crawler) finish(runErr error, runLog *slog.Logger) error {
	staged := c.stage != nil && c.stage.Staged
	// Decided before anything on disk changes: an in-place run deletes the
	// files of 404 pages in dropGonePages, which a refusal must not do.
	if runErr == nil {
		if lossy := c.checkLoss(staged); lossy != nil {
			runLog.Error(lossy.Error())
			runErr = lossy
		}
	}
	completed := runErr == nil
	if completed {
		if c.carry != nil {
			c.carry.carryAll(c.output, c.contentProcessor, c.siteCfg, c.siteOutputDir)
			if n := c.carry.count(); n > 0 {
				runLog.Info("Kept the previous copy of pages that failed with a temporary error", "count", n)
			}
		}
		c.dropGonePages(runLog)
	}
	c.output.Finalize()

	if staged {
		if !completed {
			runLog.Warn("Crawl incomplete; live corpus left untouched, staging kept so --resume can continue it", "staging", c.stage.OutputDir())
			return runErr
		}
		if err := c.commitStage(); err != nil {
			runLog.Error(fmt.Sprintf("Failed to swap staged crawl into place; previous corpus kept: %v", err))
			return fmt.Errorf("swapping staged crawl into place: %w", err)
		}
		c.output.Relocate(c.stage.liveOut)
	}
	if completed {
		c.output.writeToIndex()
	}
	c.output.writeChunks()
	return runErr
}

// checkLoss returns a *LossyCrawlError when the finished run lost too much to
// publish, nil otherwise.
func (c *Crawler) checkLoss(staged bool) *LossyCrawlError {
	c.gonePagesMu.Lock()
	gone := append([]*url.URL(nil), c.gonePages...)
	c.gonePagesMu.Unlock()
	f := c.failures.snapshot()

	in := lossInput{
		NewPages:    c.output.UniquePages(gone),
		Carryable:   c.carry.carryable(c.output),
		Transient:   int(f.Count(FailureTemporary)),
		Incremental: c.appCfg.EnableIncremental,
		Processed:   c.processedCounter.Load(),
		Succeeded:   c.succeededCounter.Load(),
		Unchanged:   c.unchangedCounter.Load(),
		AllowShrink: c.allowShrink,
	}
	in.Attempted = int(in.Succeeded+in.Unchanged) + in.Transient + int(f.Count(FailureSelector)+f.Count(FailureOther))
	if c.idx != nil && c.resolved.EnableJSONLOutput {
		lc, err := c.idx.GetLatestCrawl(context.Background(), c.siteKey)
		if err != nil {
			c.log.Warn("Could not read the last recorded crawl; skipping the shrink check", "err", err)
		} else if lc != nil {
			in.Baseline, in.HasBaseline = lc.TotalPages, true
		}
	}

	v := decideLoss(in)
	if !v.Refused {
		return nil
	}
	return &LossyCrawlError{
		SiteKey: c.siteKey, Reason: v.Reason,
		NewPages: v.NewPages, Baseline: v.Baseline, Transient: v.Transient, Attempted: v.Attempted, Processed: v.Processed,
		Failures: f, StagingKept: staged,
	}
}

// dropGonePages removes pages the site no longer serves. An incremental crawl
// works in place on the previous corpus, so without this a deleted page would
// stay in the output, the history index and search.
func (c *Crawler) dropGonePages(runLog *slog.Logger) {
	c.gonePagesMu.Lock()
	defer c.gonePagesMu.Unlock()
	for _, u := range c.gonePages {
		if p, ok := c.contentProcessor.OutputPathForURL(u, c.siteCfg, c.siteOutputDir); ok {
			if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
				runLog.Warn("Failed to remove the Markdown file of a page the site no longer serves", "path", p, "err", err)
			}
		}
		c.output.DropPage(u)
		runLog.Info("Removed a page the site no longer serves", "url", u.String())
	}
}

func (c *Crawler) commitStage() error {
	if err := c.store.Close(); err != nil {
		return fmt.Errorf("closing visited DB: %w", err)
	}
	return c.stage.Commit()
}

// validateStartURLs keeps only the configured start URLs that parse, match the
// allowed domain, and fall under the allowed path prefix. It returns the first
// valid parsed URL (used to derive the host for the initial robots.txt fetch),
// or an error if none survive.
func (c *Crawler) validateStartURLs(runLog *slog.Logger) ([]string, *url.URL, error) {
	var validStartURLs []string
	seenStartURLs := make(map[string]bool, len(c.siteCfg.StartURLs))
	var firstValidParsedURL *url.URL
	runLog.Info(fmt.Sprintf("Validating %d provided start URLs...", len(c.siteCfg.StartURLs)))
	for i, startURLStr := range c.siteCfg.StartURLs {
		startValidateLog := c.log.With("index", i, "url", startURLStr)
		if seenStartURLs[startURLStr] {
			startValidateLog.Warn("Duplicate start URL. Skipping.")
			continue
		}
		seenStartURLs[startURLStr] = true
		parsed, err := url.ParseRequestURI(startURLStr)
		if err != nil {
			startValidateLog.Warn(fmt.Sprintf("Invalid format: %v. Skipping.", err))
			continue
		}
		if parsed.Hostname() != c.siteCfg.AllowedDomain {
			startValidateLog.Warn(fmt.Sprintf("Domain mismatch (%s != %s). Skipping.", parsed.Hostname(), c.siteCfg.AllowedDomain))
			continue
		}
		targetPath := parsed.Path
		if targetPath == "" {
			targetPath = "/"
		}
		if !strings.HasPrefix(targetPath, c.siteCfg.AllowedPathPrefix) {
			startValidateLog.Warn(fmt.Sprintf("Path prefix mismatch ('%s' not under '%s'). Skipping.", targetPath, c.siteCfg.AllowedPathPrefix))
			continue
		}
		startValidateLog.Debug("Start URL format and scope validated.")
		validStartURLs = append(validStartURLs, startURLStr)
		if firstValidParsedURL == nil {
			firstValidParsedURL = parsed
		}
	}
	if len(validStartURLs) == 0 {
		return nil, nil, fmt.Errorf("no valid start_urls found for site '%s' matching scope", c.siteKey)
	}
	runLog.Info(fmt.Sprintf("Using %d valid StartURLs: %v", len(validStartURLs), validStartURLs))
	return validStartURLs, firstValidParsedURL, nil
}

// prepareOutputDir cleans (on a fresh crawl) and ensures the site output
// directory and its image subdirectory exist.
func (c *Crawler) prepareOutputDir(resume bool, runLog *slog.Logger) error {
	runLog.Info(fmt.Sprintf("Site output target directory: %s", c.siteOutputDir))
	if !resume && c.stage == nil {
		if err := c.cleanSiteOutputDir(); err != nil {
			runLog.Error(fmt.Sprintf("Failed to clean site output directory, attempting to continue: %v", err))
		}
	}
	if err := os.MkdirAll(filepath.Join(c.siteOutputDir, process.ImageDir), 0755); err != nil {
		return fmt.Errorf("error creating site output dir '%s' for site '%s': %w", c.siteOutputDir, c.siteKey, err)
	}
	runLog.Info(fmt.Sprintf("Ensured site output directory exists: %s", c.siteOutputDir))
	return nil
}

// requeueIncomplete, on a resume, scans the store for incomplete tasks and adds
// them to the priority queue. It returns the number requeued, or the context
// error if the crawl was cancelled mid-scan.
func (c *Crawler) requeueIncomplete(resume bool, runLog *slog.Logger) (int, error) {
	if !resume {
		return 0, nil
	}
	runLog.Info("Resume mode: Scanning database for incomplete tasks to requeue...")
	initialTasksFromDB := 0
	requeueChan := make(chan models.WorkItem, 100)
	var requeueWg sync.WaitGroup
	requeueWg.Add(1)
	go func() {
		defer requeueWg.Done()
		for item := range requeueChan {
			c.wg.Add(1)
			c.pq.Add(&item)
			initialTasksFromDB++
		}
	}()

	// In incremental mode also requeue previously-successful pages so they are
	// re-fetched and re-checked for content changes.
	_, _, scanErr := c.store.RequeueIncomplete(c.crawlCtx, requeueChan, c.appCfg.EnableIncremental)
	close(requeueChan)
	requeueWg.Wait()

	if scanErr != nil && !errors.Is(scanErr, context.Canceled) && !errors.Is(scanErr, context.DeadlineExceeded) {
		runLog.Error(fmt.Sprintf("Error encountered during DB requeue scan: %v", scanErr))
	}
	if c.crawlCtx.Err() != nil {
		runLog.Warn(fmt.Sprintf("Crawl context cancelled during resume scan: %v", c.crawlCtx.Err()))
		return initialTasksFromDB, c.crawlCtx.Err()
	}
	runLog.Info(fmt.Sprintf("DB requeue scan complete. Requeued %d tasks.", initialTasksFromDB))
	return initialTasksFromDB, nil
}

func (c *Crawler) startWorkers(runLog *slog.Logger) {
	runLog.Info(fmt.Sprintf("Starting %d workers...", c.appCfg.NumWorkers))
	for i := 1; i <= c.appCfg.NumWorkers; i++ {
		workerLog := c.log.With("worker_id", i)
		c.workersWg.Add(1)
		go func() {
			defer c.workersWg.Done()
			c.worker(workerLog)
		}()
	}
	runLog.Info(fmt.Sprintf("%d workers started.", c.appCfg.NumWorkers))
}

// runWaiter manages the startup-and-completion sequence: it starts the progress
// reporter, performs the initial robots.txt fetch, queues sitemaps discovered
// there (or probes the conventional locations when there are none), waits for
// all tasks to finish (or the context to cancel), and then closes the work
// queues. It closes waiterDone when it returns.
func (c *Crawler) runWaiter(firstValidParsedURL *url.URL, runLog *slog.Logger, waiterDone chan struct{}) {
	defer close(waiterDone)

	progTicker := time.NewTicker(30 * time.Second)
	progDone := make(chan bool)
	defer func() {
		progTicker.Stop()
		close(progDone)
		runLog.Info("Waiter: Progress reporter stopped.")
	}()
	go c.reportProgress(runLog, progTicker, progDone)

	if c.fetchInitialRobots(firstValidParsedURL, runLog) {
		if !c.queueInitialSitemaps(runLog) && firstValidParsedURL != nil {
			c.probeSitemap(firstValidParsedURL, runLog)
		}
		c.awaitTasks(runLog)
	}
	// Workers blocked in Pop only exit once the queue is closed, so it must
	// close on every path, including cancellation during the robots fetch.
	c.closeQueues(runLog)
}

func (c *Crawler) reportProgress(runLog *slog.Logger, progTicker *time.Ticker, progDone chan bool) {
	runLog.Info("Progress reporter started.")
	defer func() {
		// Fire one final callback so external observers (e.g. MCP
		// get_job_status) see the terminal state without waiting
		// for the next tick that will never come.
		if c.progressCallback != nil {
			c.progressCallback(c.processedCounter.Load(), int64(c.pq.Len()+len(c.sitemapQueue)))
		}
	}()
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
			c.log.Info("Crawl Progress",
				"site_key", c.siteKey,
				"visited_db", vCount,
				"page_queue_len", pqLen,
				"sitemap_queue_len", smQLen,
				"processed_tasks", procCount,
				"unchanged_pages", c.unchangedCounter.Load(),
			)
			if c.progressCallback != nil {
				c.progressCallback(procCount, int64(pqLen+smQLen))
			}
		}
	}
}

// fetchInitialRobots triggers the initial robots.txt fetch and blocks until it
// completes. It reports false if the crawl context was cancelled while waiting,
// signalling the waiter to abort.
func (c *Crawler) fetchInitialRobots(firstValidParsedURL *url.URL, runLog *slog.Logger) bool {
	if firstValidParsedURL == nil {
		runLog.Warn("No valid start URL found to fetch initial robots.txt.")
		return true
	}
	runLog.Info("Triggering initial robots.txt fetch...")
	initialRobotsDone := make(chan bool, 1)
	go c.robotsHandler.GetRobotsData(firstValidParsedURL, initialRobotsDone, c.crawlCtx)
	select {
	case <-initialRobotsDone:
		runLog.Info("Waiter: Initial robots.txt fetch signaled complete.")
		return true
	case <-c.crawlCtx.Done():
		runLog.Warn(fmt.Sprintf("Waiter: Context cancelled while waiting for initial robots.txt: %v", c.crawlCtx.Err()))
		return false
	}
}

// queueInitialSitemaps queues the sitemaps robots.txt announced and reports
// whether it announced any.
func (c *Crawler) queueInitialSitemaps(runLog *slog.Logger) bool {
	runLog.Info("Waiter: Processing initially discovered sitemaps...")
	c.foundSitemapsMu.Lock()
	announced := len(c.foundSitemaps) > 0
	var initialSitemapsToQueue []string
	for smURL := range c.foundSitemaps {
		if c.sitemapProcessor.MarkSitemapProcessed(smURL) {
			initialSitemapsToQueue = append(initialSitemapsToQueue, smURL)
		}
	}
	c.foundSitemapsMu.Unlock()

	if len(initialSitemapsToQueue) == 0 {
		runLog.Info("Waiter: No new initial sitemaps found to queue from robots.txt.")
		return announced
	}
	runLog.Info(fmt.Sprintf("Waiter: Found %d initial sitemaps to queue.", len(initialSitemapsToQueue)))
	for _, smURL := range initialSitemapsToQueue {
		c.wg.Add(1)
		select {
		case c.sitemapQueue <- smURL:
			runLog.Debug(fmt.Sprintf("Waiter: Sent initial sitemap %s to queue.", smURL))
		case <-c.crawlCtx.Done():
			runLog.Warn(fmt.Sprintf("Waiter: Context cancelled while sending initial sitemap %s: %v", smURL, c.crawlCtx.Err()))
			c.wg.Done()
		case <-time.After(10 * time.Second):
			runLog.Error(fmt.Sprintf("Waiter: Timeout sending initial sitemap %s. Undoing WG.", smURL))
			c.wg.Done()
		}
	}
	return announced
}

// probeSitemap looks for a sitemap at the locations `add` checks, so sites
// that publish one under a project path without announcing it in robots.txt
// (GitHub and GitLab Pages) are not crawled by link following alone.
func (c *Crawler) probeSitemap(startURL *url.URL, runLog *slog.Logger) {
	var candidates []string
	for _, cand := range parse.SitemapCandidates(startURL) {
		if u, err := url.Parse(cand); err == nil && c.robotsHandler.TestAgent(u, c.resolved.UserAgent, c.crawlCtx) {
			candidates = append(candidates, cand)
		}
	}
	if smURL, ok := c.sitemapProcessor.Probe(c.crawlCtx, candidates); ok {
		runLog.Info(fmt.Sprintf("Waiter: Using sitemap found by probing: %s", smURL))
	} else {
		runLog.Info("Waiter: No sitemap found at the conventional locations.")
	}
}

func (c *Crawler) awaitTasks(runLog *slog.Logger) {
	runLog.Info("Waiter: Waiting for ALL tasks (pages, sitemaps) via WaitGroup...")
	waitTasksDone := make(chan struct{})
	go func() { c.wg.Wait(); close(waitTasksDone) }()
	select {
	case <-waitTasksDone:
		runLog.Info("Waiter: WaitGroup finished normally (all tasks done).")
	case <-c.crawlCtx.Done():
		runLog.Warn(fmt.Sprintf("Waiter: Global context cancelled/timed out (%v) while waiting for tasks. Initiating shutdown.", c.crawlCtx.Err()))
	}
}

func (c *Crawler) closeQueues(runLog *slog.Logger) {
	runLog.Info("Waiter: Closing priority queue for pages...")
	c.pq.Close()
	runLog.Info("Waiter: Closing sitemap processing queue...")
	close(c.sitemapQueue)
}

func (c *Crawler) seedStartURLs(validStartURLs []string, runLog *slog.Logger) int {
	runLog.Info("Seeding priority queue with validated start URLs...")
	initialURLsAddedFromSeed := 0
	for _, startURLStr := range validStartURLs {
		normalizedSeed, _, normErr := parse.ParseAndNormalize(startURLStr)
		if normErr != nil {
			runLog.Warn(fmt.Sprintf("Skipping start URL '%s': normalize failed: %v", startURLStr, normErr))
			continue
		}
		// Mark the seed as visited before enqueueing so a same-page anchor in
		// the seed body cannot enqueue a duplicate WorkItem during link
		// extraction (MarkPageVisited would otherwise return added=true since
		// the seed has no DB entry until the deferred UpdatePageStatus runs).
		added, markErr := c.store.MarkPageVisited(normalizedSeed, 0)
		if markErr != nil {
			runLog.Error(fmt.Sprintf("Skipping start URL '%s': MarkPageVisited failed: %v", startURLStr, markErr))
			continue
		}
		if !added {
			runLog.Debug(fmt.Sprintf("Start URL '%s' already in DB (likely from resume); skipping re-seed.", startURLStr))
			continue
		}
		runLog.Info(fmt.Sprintf("Adding start URL '%s' to queue (Depth 0).", normalizedSeed))
		c.wg.Add(1)
		c.pq.Add(&models.WorkItem{URL: normalizedSeed, Depth: 0})
		initialURLsAddedFromSeed++
	}
	return initialURLsAddedFromSeed
}

func (c *Crawler) logRunSummary(overallStart time.Time, runLog *slog.Logger) {
	duration := time.Since(overallStart)
	finalVisitedCount, countErr := c.store.GetVisitedCount()
	if countErr != nil {
		runLog.Warn(fmt.Sprintf("Could not get final visited count from DB: %v", countErr))
		finalVisitedCount = -1
	}
	finalProcessedCount := c.processedCounter.Load()
	summaryLog := c.log.With("domain", c.siteCfg.AllowedDomain)
	summaryLog.Info("========================================================================")
	summaryLog.Info("CRAWL FINISHED")
	summaryLog.Info(fmt.Sprintf("Duration:         %v", duration))
	summaryLog.Info(fmt.Sprintf("Final Stats: Visited (DB Est): %d, Processed Tasks: %d, Unchanged Pages: %d, Pages Saved (for YAML): %d",
		finalVisitedCount, finalProcessedCount, c.unchangedCounter.Load(), c.output.PagesSaved()))
	f := c.failures.snapshot()
	for _, fc := range f.Classes {
		summaryLog.Info("Failed pages by class", "class", fc.Class, "count", fc.Count, "sample_url", fc.SampleURL, "sample_error", fc.SampleError)
	}
	if f.Noindex > 0 {
		summaryLog.Info("Pages skipped as noindex", "count", f.Noindex)
	}
	summaryLog.Info("========================================================================")
}

// worker runs the loop for a single worker goroutine, processing tasks from the priority queue.
func (c *Crawler) worker(workerLog *slog.Logger) { // workerLog already has site_key and worker_id
	workerLog.Info("Worker starting")
	defer workerLog.Info("Worker finished")

	for {
		// Check context before potentially blocking Pop, to allow quick exit if cancelled
		select {
		case <-c.crawlCtx.Done():
			workerLog.Warn(fmt.Sprintf("Worker shutting down due to context cancellation: %v", c.crawlCtx.Err()))
			return
		default:
		}

		// Pop blocks until an item is available or the queue is closed and empty
		workItemPtr, ok := c.pq.Pop()
		if !ok {
			if c.crawlCtx.Err() != nil {
				workerLog.Warn(fmt.Sprintf("Worker shutting down (queue closed & context cancelled): %v", c.crawlCtx.Err()))
			} else {
				workerLog.Info("Worker shutting down (queue closed & empty, all tasks processed).")
			}
			return
		}

		c.processSinglePageTask(*workItemPtr, workerLog)
	}
}

// cleanSiteOutputDir removes the site-specific output directory safely.
// This is typically called when not in resume mode.
func (c *Crawler) cleanSiteOutputDir() error {
	c.log.Warn(fmt.Sprintf("Attempting to remove existing site output directory: %s", c.siteOutputDir))

	// Safety Check: Resolve absolute paths to prevent accidental deletion outside base_dir
	absBase, errBase := filepath.Abs(c.appCfg.OutputBaseDir)
	if errBase != nil {
		return fmt.Errorf("safety check failed (resolving base path '%s'): %w", c.appCfg.OutputBaseDir, errBase)
	}
	absSite, errSite := filepath.Abs(c.siteOutputDir)
	if errSite != nil {
		return fmt.Errorf("safety check failed (resolving site path '%s'): %w", c.siteOutputDir, errSite)
	}

	if utils.IsWithinDir(absBase, absSite) {
		c.log.Debug(fmt.Sprintf("Safety check passed for RemoveAll. BaseAbs: '%s', SiteAbs: '%s'", absBase, absSite))
		err := os.RemoveAll(c.siteOutputDir)
		if err != nil && !errors.Is(err, os.ErrNotExist) { // ErrNotExist is fine
			return fmt.Errorf("failed remove site output dir '%s': %w", c.siteOutputDir, err)
		} else if err == nil {
			c.log.Info(fmt.Sprintf("Successfully removed existing site output directory: %s", c.siteOutputDir))
		}
		return nil // Success or directory didn't exist
	}

	// Safety check failed. Log and return error to prevent dangerous deletion.
	errMsg := fmt.Sprintf("safety check failed: would not remove dir (BaseDir: '%s', SiteOutputDir: '%s', BaseAbs: '%s', SiteAbs: '%s')",
		c.appCfg.OutputBaseDir, c.siteOutputDir, absBase, absSite)
	c.log.Error(errMsg)
	return errors.New(errMsg)
}

func contentRegionHash(regionHTML string) string {
	return utils.CalculateStringSHA256(fmt.Sprintf("markdown-format-%d\n%s", process.MarkdownFormatVersion, regionHTML))
}

// processSinglePageTask orchestrates the processing pipeline for a single URL (WorkItem).
func (c *Crawler) processSinglePageTask(workItem models.WorkItem, workerLog *slog.Logger) { //nolint:gocyclo // multi-stage page processing pipeline
	currentURL := workItem.URL
	currentDepth := workItem.Depth
	taskLog := workerLog.With("url", currentURL, "depth", currentDepth)
	startTime := time.Now()

	taskCtx := c.crawlCtx
	if c.appCfg.PerPageTimeout > 0 {
		var cancel context.CancelFunc
		taskCtx, cancel = context.WithTimeout(c.crawlCtx, c.appCfg.PerPageTimeout)
		defer cancel()
	}

	// pageTitle and savedContentPath feed the deferred logging; normalizedURLString feeds DB updates and YAML metadata.
	var taskErr error                 // Stores the first critical error encountered in the pipeline.
	var finalStatus models.PageStatus // PageStatusSuccess or PageStatusFailure (only set for non-skipped tasks)
	var finalErrorType = "None"       // Error message on failure.
	var skipped = false               // True if task is skipped due to prior processing or policy.
	var pageTitle string              // Populated on successful content extraction.
	var savedContentPath string       // Absolute path to the saved .md file.
	var normalizedURLString string    // Populated from handleSetupAndResumeCheck.
	var contentHash string            // Hash of the extracted content region for incremental crawling.
	var gone bool                     // The page answered 404 or 410.
	var noindexPage bool              // Saved nothing on purpose; neither success nor failure.

	// Deferred function for panic recovery, final status logging, DB update, and WaitGroup decrement.
	defer func() {
		panicked := false
		if r := recover(); r != nil {
			panicked = true
			skipped = false // Panic overrides any prior skip status
			taskErr = fmt.Errorf("panic: %v", r)
			stackTrace := string(debug.Stack())
			taskLog.Error("PANIC recovered in processSinglePageTask",
				"panic_info", r,
				"duration", time.Since(startTime).String(),
				"stage", "PanicRecovery",
				"stack_trace", stackTrace,
			)
		}

		logAttrs := []any{"duration", time.Since(startTime).String()}
		if pageTitle != "" {
			logAttrs = append(logAttrs, "page_title", pageTitle)
		}

		// A task cut off by cancelling the crawl did not fail: recording it would
		// count it as processed and wipe its content hash, so the next
		// incremental crawl would reprocess an unchanged page.
		if taskErr != nil && !panicked && c.crawlCtx.Err() != nil {
			taskLog.With(logAttrs...).Info(fmt.Sprintf("Task abandoned, crawl cancelled: %v", taskErr))
			c.wg.Done()
			return
		}

		if taskErr != nil {
			finalStatus = models.PageStatusFailure
			finalErrorType = taskErr.Error()
			if !panicked { // Log non-panic errors (panic already logged above)
				taskLog.With(logAttrs...).Warn(fmt.Sprintf("Task failed: %v", taskErr))
			}
		} else if skipped {
			// finalStatus not set for skipped tasks (DB not updated)
			taskLog.With(logAttrs...).Info("Task skipped")
		} else {
			finalStatus = models.PageStatusSuccess
			finalErrorType = "None"
			if savedContentPath != "" {
				logAttrs = append(logAttrs, "saved_path", savedContentPath)
			}
			taskLog.With(logAttrs...).Info("Task completed successfully")
		}

		if !skipped && normalizedURLString != "" {
			pageEntry := &models.PageDBEntry{
				Status:      finalStatus,
				ErrorType:   finalErrorType,
				LastAttempt: time.Now().UTC(),
				Depth:       currentDepth,
			}
			if finalStatus == models.PageStatusSuccess {
				pageEntry.ProcessedAt = pageEntry.LastAttempt
				pageEntry.ContentHash = contentHash
			}
			if dbUpdateErr := c.store.UpdatePageStatus(normalizedURLString, pageEntry); dbUpdateErr != nil {
				taskLog.Error(fmt.Sprintf("Failed update final DB status for '%s' to '%s': %v", normalizedURLString, finalStatus, dbUpdateErr))
			}
		} else if !skipped { // Not skipped, but normalization might have failed
			taskLog.Warn(fmt.Sprintf("URL '%s' normalization failed or was not set; cannot update DB status.", currentURL))
		}

		if !skipped {
			c.processedCounter.Add(1)
			if finalStatus == models.PageStatusSuccess && !noindexPage {
				c.succeededCounter.Add(1)
			}
		}
		if noindexPage {
			c.failures.markNoindex()
		}
		if taskErr != nil {
			class := classify(taskErr, gone)
			c.failures.record(currentURL, taskErr, class)
			if class == FailureTemporary && c.carry != nil {
				if u, err := url.Parse(currentURL); err == nil {
					c.carry.note(u)
				}
			}
		}
		c.wg.Done()
	}()

	// Helper function to store the first critical error encountered in the pipeline.
	// Returns true if an error was handled (i.e., err was not nil).
	handleTaskError := func(err error) bool {
		if err == nil {
			return false
		}
		if taskErr == nil {
			taskErr = err
		}
		return true
	}

	var parsedOriginalURL *url.URL
	var host string
	var setupErr error
	var setupShouldSkip bool
	// normalizedURLString is populated here for use in defer and metadata
	parsedOriginalURL, normalizedURLString, host, setupShouldSkip, setupErr = c.handleSetupAndResumeCheck(currentURL, taskLog)
	if handleTaskError(setupErr) {
		return
	}
	if setupShouldSkip {
		skipped = true
		return
	}
	taskLog = taskLog.With("host", host)

	if handleTaskError(c.runPolicyChecks(parsedOriginalURL, currentDepth, taskLog)) {
		return
	}

	cleanupResources, acquireErr := c.acquireResources(host, taskLog)
	defer cleanupResources() // Ensure semaphores are released when task finishes
	if handleTaskError(acquireErr) {
		return
	}

	finalURL, resp, fetchErr := c.fetchAndValidatePage(currentURL, parsedOriginalURL, taskLog)
	// fetchAndValidatePage closes resp.Body on error if resp is not nil.
	if handleTaskError(fetchErr) {
		if resp != nil && (resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusGone) {
			gone = true
			c.gonePagesMu.Lock()
			c.gonePages = append(c.gonePages, parsedOriginalURL)
			c.gonePagesMu.Unlock()
		}
		return
	}
	// If successful, resp.Body is open and passed to the next stage.

	bodyBytes, readErr := c.readBody(resp, finalURL, taskLog) // Closes resp.Body
	if handleTaskError(readErr) {
		return
	}
	// The request is done; holding request slots while waiting for the page
	// budget could starve image downloads of a page that holds the budget.
	cleanupResources()

	originalDoc, releaseDoc, parseBodyErr := parseBody(taskCtx, bodyBytes, finalURL)
	if handleTaskError(parseBodyErr) {
		return
	}
	defer releaseDoc()
	taskLog.Debug("Successfully parsed HTML into goquery document.")

	// Checked before content selection so a noindex page (mdBook's print.html
	// repeats every chapter) never seeds the selector cache or the corpus. No
	// content hash is recorded, so turning respect_noindex off later reprocesses it.
	if noindex, nofollow := process.RobotsDirectives(originalDoc, resp.Header); noindex && c.resolved.RespectNoindex {
		noindexPage = true
		cleanupResources()
		if nofollow && c.siteCfg.RespectNofollow {
			taskLog.Info("Page is marked noindex, nofollow; not saved, links not followed")
			return
		}
		taskLog.Info("Page is marked noindex; not saved, following its links")
		c.followLinks(originalDoc, finalURL, currentDepth, taskLog)
		return
	}

	mainContent, extractedTitle, selectErr := c.contentProcessor.SelectMainContent(originalDoc, finalURL, c.siteCfg, taskLog)
	if handleTaskError(selectErr) {
		// A landing page the selector misses is often the only route to the
		// docs on a site without a sitemap.
		cleanupResources()
		c.followLinks(originalDoc, finalURL, currentDepth, taskLog)
		return
	}
	pageTitle = extractedTitle

	// Hash the extracted content region rather than the raw page body so that
	// page-shell churn (nav, analytics, build timestamps, CSRF tokens) outside the
	// content selector does not defeat the incremental skip.
	contentRegionHTML, outerErr := goquery.OuterHtml(mainContent)
	if handleTaskError(outerErr) {
		return
	}
	contentHash = contentRegionHash(contentRegionHTML)

	// Release semaphores early: HTTP fetch is done, remaining work is local
	// computation + image downloads (which acquire their own semaphores). The
	// deferred cleanupResources is still in place as a safety net for error
	// paths above this point.
	cleanupResources()

	// Links come from the whole page, not just the hashed content region, so
	// they are extracted before the unchanged-content skip: a sidebar can gain
	// a page while every content hash stays the same.
	// Non-critical errors (e.g., DB error during link check) are logged within linkProcessor.
	c.followLinks(originalDoc, finalURL, currentDepth, taskLog)

	if c.appCfg.EnableIncremental {
		existingHash, exists, hashErr := c.store.GetPageContentHash(normalizedURLString)
		if hashErr != nil {
			taskLog.Warn(fmt.Sprintf("Failed to check content hash for incremental crawl: %v", hashErr))
			// Continue processing despite hash check error
		} else if exists && existingHash == contentHash && c.savedFileExists(finalURL) {
			taskLog.Info("Page content unchanged (hash match) - skipping processing")
			c.unchangedCounter.Add(1)
			skipped = true
			return
		} else if exists && existingHash == contentHash {
			taskLog.Info("Page content unchanged but its saved file is missing - saving it again")
		} else if exists {
			taskLog.Debug("Page content changed - will reprocess")
		} else {
			taskLog.Debug("New page (no previous hash) - will process")
		}
	}

	var tempSavedPath string
	var tempMarkdownBytes []byte
	var contentErr error
	// pageTitle is already set from SelectMainContent above; savedContentPath is set on success.
	tempSavedPath, tempMarkdownBytes, _, contentErr = c.contentProcessor.ProcessAndSaveContent(mainContent, pageTitle, finalURL, c.siteCfg, c.siteOutputDir, currentDepth, taskLog, taskCtx)
	if handleTaskError(contentErr) {
		return
	}
	savedContentPath = tempSavedPath

	if savedContentPath != "" {
		c.output.RecordPageOutput(finalURL.String(), tempMarkdownBytes, pageTitle, currentDepth, taskLog)
		c.savedCounter.Add(1)
	}
	// If execution reaches here, taskErr is still nil, indicating success.
	// The deferred function will handle logging this success and updating DB.
}

// savedFileExists reports whether a page's Markdown file is where this
// version saves it; a deleted file, or one saved under an older path layout,
// must not be skipped as unchanged.
func (c *Crawler) savedFileExists(pageURL *url.URL) bool {
	p, ok := c.contentProcessor.OutputPathForURL(pageURL, c.siteCfg, c.siteOutputDir)
	if !ok {
		return true
	}
	_, err := os.Stat(p)
	return err == nil
}

// handleSetupAndResumeCheck parses the URL, normalizes it, and checks its status in the DB.
// It determines if the URL should be skipped (e.g., already successfully processed).
func (c *Crawler) handleSetupAndResumeCheck(currentURL string, taskLog *slog.Logger) (parsedURL *url.URL, normalizedURLStr string, host string, shouldSkip bool, err error) {
	taskLog.Debug("Performing setup and resume check...")
	parsedTargetURL, parseErr := url.Parse(currentURL)
	if parseErr != nil {
		err = fmt.Errorf("%w: parsing URL '%s': %w", utils.ErrParsing, currentURL, parseErr)
		return nil, "", "", false, err
	}
	parsedURL = parsedTargetURL

	normalizedURLStr = parse.NormalizeURL(parsedURL)
	host = parsedURL.Hostname()
	if host == "" && parsedURL.Scheme != "file" { // Check scheme for file URLs which don't have a host
		err = fmt.Errorf("URL '%s' missing host (and not a file:// URL)", currentURL)
		return parsedURL, normalizedURLStr, "", false, err
	}

	pageStatus, _, checkErr := c.store.CheckPageStatus(normalizedURLStr)
	if checkErr != nil {
		taskLog.Error(fmt.Sprintf("DB error checking status for '%s', proceeding as if not found: %v", normalizedURLStr, checkErr))
		// Do not return 'err' here; let the crawl attempt proceed if DB check fails.
		// The error is logged, and status will default to PageStatusNotFound effectively.
	} else if pageStatus == models.PageStatusSuccess {
		if !c.appCfg.EnableIncremental {
			taskLog.Info("Skipping already successfully processed page (from DB).")
			shouldSkip = true
			return parsedURL, normalizedURLStr, host, shouldSkip, nil
		}
		taskLog.Debug("Re-checking previously processed page for content changes (incremental mode).")
	} else if pageStatus == models.PageStatusFailure {
		taskLog.Warn("Retrying previously failed page (from DB).")
	} else if pageStatus == models.PageStatusPending {
		taskLog.Debug("Processing page previously marked pending (from DB).")
	} // If PageStatusNotFound or any other unexpected status, proceed to crawl normally.

	return parsedURL, normalizedURLStr, host, false, nil
}

// runPolicyChecks verifies if the URL adheres to defined crawl policies (depth, robots.txt).
func (c *Crawler) runPolicyChecks(parsedURL *url.URL, depth int, taskLog *slog.Logger) error {
	taskLog.Debug("Running policy checks...")
	if c.siteCfg.MaxDepth > 0 && depth >= c.siteCfg.MaxDepth {
		err := utils.ErrMaxDepthExceeded
		taskLog.Info(fmt.Sprintf("%s (Current Depth: %d, Max Depth: %d)", err.Error(), depth, c.siteCfg.MaxDepth))
		return err
	}

	if !c.robotsHandler.TestAgent(parsedURL, c.resolved.UserAgent, c.crawlCtx) { // TestAgent handles fetching/caching robots.txt
		err := fmt.Errorf("%w: URL '%s' disallowed for agent '%s'", utils.ErrRobotsDisallowed, parsedURL.RequestURI(), c.resolved.UserAgent)
		taskLog.Warn(err.Error())
		return err
	}
	taskLog.Debug("Policy checks passed.")
	return nil
}

func (c *Crawler) followLinks(doc *goquery.Document, pageURL *url.URL, depth int, taskLog *slog.Logger) {
	if _, linkErr := c.linkProcessor.ExtractAndQueueLinks(doc, pageURL, depth, c.siteCfg, &c.wg, taskLog); linkErr != nil {
		taskLog.Warn(fmt.Sprintf("Non-fatal error encountered during link extraction/queueing: %v", linkErr))
	}
}

// acquireResources attempts to acquire necessary semaphores (global, per-host) and applies rate limiting.
// Returns a cleanup function to release semaphores.
func (c *Crawler) acquireResources(host string, taskLog *slog.Logger) (cleanupFunc func(), err error) {
	taskLog.Debug("Acquiring resources (semaphores, rate limit)...")
	acquiredHostSem, acquiredGlobalSem := false, false
	cleanupFunc = func() {
		if acquiredHostSem {
			c.hostSemPool.Release(host)
			acquiredHostSem = false
			taskLog.Debug(fmt.Sprintf("Released host semaphore for: %s", host))
		}
		if acquiredGlobalSem {
			c.globalSemaphore.Release(1)
			acquiredGlobalSem = false
			taskLog.Debug("Released global semaphore.")
		}
	}

	semTimeout := config.DefaultSemaphoreAcquireTimeout

	ctxHost, cancelHost := context.WithTimeout(c.crawlCtx, semTimeout)
	defer cancelHost()
	taskLog.Debug(fmt.Sprintf("Attempting to acquire host semaphore for: %s (timeout: %v)", host, semTimeout))
	if semErr := c.hostSemPool.Acquire(ctxHost, host); semErr != nil {
		// Wrap error for better context (e.g., distinguish timeout from other errors)
		return cleanupFunc, fmt.Errorf("%w: acquire host semaphore for '%s': %w", utils.ErrSemaphoreTimeout, host, semErr)
	}
	acquiredHostSem = true
	taskLog.Debug(fmt.Sprintf("Acquired host semaphore for: %s", host))

	ctxGlobal, cancelGlobal := context.WithTimeout(c.crawlCtx, semTimeout)
	defer cancelGlobal()
	taskLog.Debug(fmt.Sprintf("Attempting to acquire global semaphore (timeout: %v)", semTimeout))
	if semErr := c.globalSemaphore.Acquire(ctxGlobal, 1); semErr != nil {
		// If global semaphore fails, host semaphore (if acquired) will be released by defer cleanupFunc.
		return cleanupFunc, fmt.Errorf("%w: acquire global semaphore: %w", utils.ErrSemaphoreTimeout, semErr)
	}
	acquiredGlobalSem = true
	taskLog.Debug("Acquired global semaphore.")

	// Rate-limit only after acquiring the semaphores, so the delay does not hold slots while waiting.
	if c.resolved.DelayPerHost > 0 {
		c.rateLimiter.ApplyDelay(c.crawlCtx, host, c.resolved.DelayPerHost)
	}

	taskLog.Debug("Resource acquisition successful.")
	return cleanupFunc, nil
}

// drainClose drains and closes an HTTP response body so the keep-alive
// connection can be reused before the caller returns a nil response.
func drainClose(resp *http.Response) {
	io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
}

// fetchAndValidatePage performs the HTTP GET request with retries and validates the response.
// It handles redirects and ensures the final URL is within scope and allowed by robots.txt.
// If successful, returns the final URL and an open http.Response (caller must close Body).
// On error, it ensures resp.Body is closed if resp is not nil.
func (c *Crawler) fetchAndValidatePage(reqURLString string, originalParsedURL *url.URL, taskLog *slog.Logger) (finalURL *url.URL, resp *http.Response, err error) {
	taskLog.Debug(fmt.Sprintf("Fetching page: %s", reqURLString))

	req, reqErr := http.NewRequestWithContext(c.crawlCtx, http.MethodGet, reqURLString, nil)
	if reqErr != nil {
		return nil, nil, fmt.Errorf("%w: creating request for '%s': %w", utils.ErrRequestCreation, reqURLString, reqErr)
	}
	req.Header.Set("User-Agent", c.resolved.UserAgent)

	resp, fetchErr := c.fetcher.FetchWithRetry(req, c.crawlCtx)

	if fetchErr != nil {
		// Fetcher component already logged details of fetch/retry failures.
		// It also ensures resp.Body is closed if resp is not nil and an error occurred.
		return nil, resp, fetchErr // Propagate error; resp might be non-nil if an HTTP error occurred (e.g., 404)
	}
	// If fetchErr is nil, we have a successful 2xx response, and resp.Body is open.

	finalURL = resp.Request.URL // URL after any redirects handled by the HTTP client
	if finalURL.String() != reqURLString {
		taskLog = taskLog.With("final_url", finalURL.String())
		taskLog.Info("URL redirected.")
	}
	taskLog.Debug("Validating final URL scope and policies...")

	finalHost := finalURL.Hostname()
	finalPath := finalURL.Path
	if finalPath == "" {
		finalPath = "/"
	}

	if finalHost != c.siteCfg.AllowedDomain || !strings.HasPrefix(finalPath, c.siteCfg.AllowedPathPrefix) {
		err = fmt.Errorf("%w: redirected URL '%s' out of scope (Expected Domain: '%s', Path Prefix: '%s')",
			utils.ErrScopeViolation, finalURL.String(), c.siteCfg.AllowedDomain, c.siteCfg.AllowedPathPrefix)
		drainClose(resp)
		return finalURL, nil, err // resp body is now closed; return a nil response
	}

	for _, pattern := range c.compiledDisallowedPatterns {
		if pattern.MatchString(finalURL.Path) {
			err = fmt.Errorf("%w: redirected URL '%s' matches disallowed pattern '%s'",
				utils.ErrScopeViolation, finalURL.String(), pattern.String())
			drainClose(resp)
			return finalURL, nil, err
		}
	}

	// Re-check robots.txt since the redirect crossed hosts.
	if finalHost != originalParsedURL.Hostname() {
		taskLog.Debug(fmt.Sprintf("Host changed due to redirect (%s -> %s), re-checking robots.txt for final URL.",
			originalParsedURL.Hostname(), finalHost))
		if !c.robotsHandler.TestAgent(finalURL, c.resolved.UserAgent, c.crawlCtx) {
			err = fmt.Errorf("%w: redirected URL '%s' disallowed by robots.txt on new host",
				utils.ErrRobotsDisallowed, finalURL.String())
			drainClose(resp)
			return finalURL, nil, err
		}
	}

	// Content-Type Check: hard skip for unambiguous non-document types, warn for ambiguous ones
	contentType := resp.Header.Get("Content-Type")
	ctLower := strings.ToLower(contentType)
	if !strings.HasPrefix(ctLower, "text/html") && !strings.HasPrefix(ctLower, "application/xhtml+xml") {
		if strings.HasPrefix(ctLower, "image/") ||
			strings.HasPrefix(ctLower, "audio/") ||
			strings.HasPrefix(ctLower, "video/") ||
			strings.HasPrefix(ctLower, "font/") ||
			strings.HasPrefix(ctLower, "application/zip") ||
			strings.HasPrefix(ctLower, "application/gzip") ||
			strings.HasPrefix(ctLower, "application/pdf") ||
			strings.HasPrefix(ctLower, "application/octet-stream") {
			drainClose(resp)
			return finalURL, nil, fmt.Errorf("%w: '%s' for '%s'", utils.ErrNonHTMLContent, contentType, finalURL.String())
		}
		// Ambiguous types (text/plain, etc.) -- warn but proceed
		taskLog.Warn(fmt.Sprintf("Unexpected Content-Type '%s' for '%s'. Proceeding with parsing attempt.", contentType, finalURL.String()))
	}

	taskLog.Debug("Fetch and validation successful.")
	return finalURL, resp, nil
}

// readBody reads resp.Body, closing it when done.
func (c *Crawler) readBody(resp *http.Response, finalURL *url.URL, taskLog *slog.Logger) ([]byte, error) {
	taskLog.Debug(fmt.Sprintf("Reading response body from: %s", finalURL.String()))
	defer resp.Body.Close()

	maxPageSize := c.resolved.MaxPageSizeBytes
	limitedReader := io.LimitReader(resp.Body, maxPageSize+1) // +1 to detect exceeding the limit
	bodyBytes, readErr := io.ReadAll(limitedReader)
	if readErr != nil {
		return nil, fmt.Errorf("%w: reading body from '%s': %w", utils.ErrResponseBodyRead, finalURL.String(), readErr)
	}
	if int64(len(bodyBytes)) > maxPageSize {
		return nil, fmt.Errorf("%w: %w: '%s' (%d > %d bytes)", utils.ErrResponseBodyRead, utils.ErrPageTooLarge, finalURL.String(), len(bodyBytes), maxPageSize)
	}
	taskLog.Debug(fmt.Sprintf("Read %d bytes from response body of %s", len(bodyBytes), finalURL.String()))
	return bodyBytes, nil
}

// parseBody parses body through the shared page budget; release must be
// called when the page task is done with the document.
func parseBody(ctx context.Context, body []byte, finalURL *url.URL) (*goquery.Document, func(), error) {
	doc, release, err := pagebudget.Shared.Parse(ctx, body)
	switch {
	case err == nil:
		return doc, release, nil
	case errors.Is(err, pagebudget.ErrPageTooComplex):
		return nil, nil, fmt.Errorf("%w: page '%s': %w", utils.ErrResponseBodyRead, finalURL.String(), err)
	case ctx.Err() != nil:
		return nil, nil, err
	default:
		return nil, nil, fmt.Errorf("%w: parsing HTML from '%s': %w", utils.ErrParsing, finalURL.String(), err)
	}
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
