package sitemap

import (
	"context"
	"encoding/xml"
	"errors"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/sirupsen/logrus"
	"golang.org/x/sync/semaphore"

	"github.com/Sriram-PR/doc-scraper/pkg/config"
	"github.com/Sriram-PR/doc-scraper/pkg/fetch"
	"github.com/Sriram-PR/doc-scraper/pkg/models"
	"github.com/Sriram-PR/doc-scraper/pkg/parse"
	"github.com/Sriram-PR/doc-scraper/pkg/queue"
	"github.com/Sriram-PR/doc-scraper/pkg/storage"
)

// SitemapProcessor handles fetching, parsing, and processing sitemaps
type SitemapProcessor struct {
	sitemapQueue               chan string                    // Channel to receive sitemap URLs to process
	pq                         *queue.ThreadSafePriorityQueue // Main priority queue for page URLs
	store                      storage.PageStore              // To mark pages visited
	fetcher                    *fetch.Fetcher                 // For fetching sitemaps
	rateLimiter                *fetch.RateLimiter             // For rate limiting fetches
	globalSemaphore            *semaphore.Weighted            // Global request limit
	compiledDisallowedPatterns []*regexp.Regexp               // For filtering URLs found in sitemaps
	siteCfg                    config.SiteConfig              // Need for scope checks
	appCfg                     config.AppConfig               // Need for UA, timeouts
	log                        *logrus.Entry
	wg                         *sync.WaitGroup // Main crawler waitgroup
	sitemapsProcessed          map[string]bool // Track sitemaps submitted to this processor
	sitemapsProcessedMu        sync.Mutex      // Mutex for the processed map
}

// NewSitemapProcessor creates a new SitemapProcessor
func NewSitemapProcessor(
	sitemapQueue chan string,
	pq *queue.ThreadSafePriorityQueue,
	store storage.PageStore,
	fetcher *fetch.Fetcher,
	rateLimiter *fetch.RateLimiter,
	globalSemaphore *semaphore.Weighted,
	compiledDisallowedPatterns []*regexp.Regexp,
	siteCfg config.SiteConfig,
	appCfg config.AppConfig,
	log *logrus.Logger,
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
		log:                        log.WithField("component", "sitemap_processor"),
		wg:                         wg,
		sitemapsProcessed:          make(map[string]bool), // Initialize map
	}
}

// Start runs the sitemap processing loop in a goroutine
func (sp *SitemapProcessor) Start(ctx context.Context) {
	sp.log.Info("Sitemap processing goroutine starting.")
	go sp.run(ctx)
}

// MarkSitemapProcessed records that a sitemap URL has been queued for processing
// Returns true if it was newly marked, false if already marked
func (sp *SitemapProcessor) MarkSitemapProcessed(sitemapURL string) bool {
	sp.sitemapsProcessedMu.Lock()
	defer sp.sitemapsProcessedMu.Unlock()
	if !sp.sitemapsProcessed[sitemapURL] {
		sp.sitemapsProcessed[sitemapURL] = true
		return true
	}
	return false
}

// run is the main processing loop
func (sp *SitemapProcessor) run(ctx context.Context) {
	var sitemapProcessingWg sync.WaitGroup // Tracks active sitemap downloads/parses within this processor

	defer func() {
		sp.log.Info("Waiting for active sitemap processing tasks to finish before final exit...")
		sitemapProcessingWg.Wait()
		sp.log.Info("Sitemap processing goroutine finished waiting and exiting.")
	}()

	userAgent := sp.appCfg.DefaultUserAgent // Use default UA for sitemaps
	semTimeout := sp.appCfg.SemaphoreAcquireTimeout

	for {
		select {
		case <-ctx.Done(): // Check if the main crawl context has been cancelled
			sp.log.Warnf("Context cancelled, stopping sitemap processing: %v", ctx.Err())
			return

		case sitemapURL, ok := <-sp.sitemapQueue: // Try to receive a URL from the channel
			if !ok {
				sp.log.Info("Sitemap queue channel closed.")
				return
			}

			// --- Received a URL, Launch a Goroutine to Process It ---
			sitemapProcessingWg.Add(1) // Increment local WaitGroup for the task we are about to launch
			go func(smURL string) {    // Launch goroutine for concurrent processing
				// Ensure both WaitGroups are decremented when this goroutine finishes
				defer func() {
					sp.wg.Done()               // Decrement the main crawler WaitGroup (incremented when queued)
					sitemapProcessingWg.Done() // Decrement this processor's WaitGroup
				}()

				// --- Panic Recovery for this sitemap task ---
				defer func() {
					if r := recover(); r != nil {
						stackTrace := string(debug.Stack())
						sp.log.WithFields(logrus.Fields{
							"sitemap_url": smURL,
							"panic_info":  r,
							"stack_trace": stackTrace,
						}).Error("PANIC Recovered in sitemap processing goroutine")
					}
				}()

				sitemapLog := sp.log.WithField("sitemap_url", smURL)
				sitemapLog.Info("Processing sitemap")

				// --- Fetching Logic ---
				parsedSitemapURL, err := url.Parse(smURL)
				if err != nil {
					sitemapLog.Errorf("Failed parse URL: %v", err)
					return // Stop processing this invalid URL
				}
				sitemapHost := parsedSitemapURL.Hostname()

				// --- Acquire Global Semaphore (respecting context) ---
				ctxG, cancelG := context.WithTimeout(ctx, semTimeout) // Derive timeout from main context
				err = sp.globalSemaphore.Acquire(ctxG, 1)
				cancelG() // Release resources associated with the timed context
				if err != nil {
					// Check if the error was due to the main context being cancelled
					if errors.Is(err, context.DeadlineExceeded) && ctx.Err() != nil {
						sitemapLog.Warnf("Could not acquire GLOBAL semaphore due to main context cancellation: %v", ctx.Err())
					} else if errors.Is(err, context.DeadlineExceeded) {
						sitemapLog.Errorf("Timeout acquiring GLOBAL semaphore: %v", err)
					} else {
						sitemapLog.Errorf("Error acquiring GLOBAL semaphore: %v", err)
					}
					return // Stop processing this sitemap if semaphore not acquired
				}
				defer sp.globalSemaphore.Release(1) // Ensure release on exit

				// --- Apply Rate Limit ---
				sp.rateLimiter.ApplyDelay(sitemapHost, sp.appCfg.DefaultDelayPerHost)

				// --- Create Request (with context) ---
				req, err := http.NewRequestWithContext(ctx, "GET", smURL, nil)
				if err != nil {
					sitemapLog.Errorf("Req Create error: %v", err)
					return
				}
				req.Header.Set("User-Agent", userAgent)

				// --- Fetch Request (with context and retries) ---
				resp, fetchErr := sp.fetcher.FetchWithRetry(req, ctx)
				sp.rateLimiter.UpdateLastRequestTime(sitemapHost) // Update after attempt

				if fetchErr != nil {
					sitemapLog.Errorf("Fetch failed: %v", fetchErr)
					if resp != nil {
						io.Copy(io.Discard, resp.Body)
						resp.Body.Close()
					}
					return
				}
				// If fetch succeeded, resp is non-nil, 2xx status
				defer resp.Body.Close() // Ensure body is closed eventually

				// --- Read & Parse XML ---
				sitemapBytes, readErr := io.ReadAll(resp.Body)
				if readErr != nil {
					sitemapLog.Errorf("Read body error: %v", readErr)
					return
				}

				// --- Try Parsing as Sitemap Index ---
				var index parse.XMLSitemapIndex
				errIndex := xml.Unmarshal(sitemapBytes, &index)
				if errIndex == nil && len(index.Sitemaps) > 0 {
					sitemapLog.Infof("Parsed as Sitemap Index, found %d references.", len(index.Sitemaps))
					queuedCount := 0
					for _, sitemapEntry := range index.Sitemaps {
						nestedSmURL := sitemapEntry.Loc
						nestedSmLog := sitemapLog.WithField("nested_sitemap", nestedSmURL)
						_, nestedErr := url.ParseRequestURI(nestedSmURL)
						if nestedErr != nil {
							nestedSmLog.Warnf("Invalid nested sitemap URL: %v", nestedErr)
							continue // Skip invalid URLs
						}

						// --- Queue Nested Sitemap (check if already processed) ---
						// MarkSitemapProcessed is thread-safe and returns true if newly marked
						if sp.MarkSitemapProcessed(nestedSmURL) {
							// It's a new sitemap for the processor, increment WG for the eventual task
							sp.wg.Add(1)

							select {
							case sp.sitemapQueue <- nestedSmURL: // Attempt to send
								queuedCount++
								nestedSmLog.Debug("Successfully queued nested sitemap.")
								// WG remains incremented, will be decremented when task completes

							case <-ctx.Done(): // Check if main context is cancelled
								nestedSmLog.Warnf("Context cancelled while trying to queue nested sitemap '%s': %v", nestedSmURL, ctx.Err())
								// --- UNDO STATE ---
								sp.sitemapsProcessedMu.Lock()
								delete(sp.sitemapsProcessed, nestedSmURL) // Remove from processed map
								sp.sitemapsProcessedMu.Unlock()
								sp.wg.Done() // Decrement main WG as task won't be processed

							case <-time.After(5 * time.Second): // Timeout for queue send
								nestedSmLog.Error("Timeout sending nested sitemap. Undoing WG and processed state.")
								// --- UNDO STATE ---
								sp.sitemapsProcessedMu.Lock()
								delete(sp.sitemapsProcessed, nestedSmURL) // Remove from processed map
								sp.sitemapsProcessedMu.Unlock()
								sp.wg.Done() // Decrement main WG as task won't be processed
							}
						} else {
							nestedSmLog.Debugf("Nested sitemap already processed/queued: %s", nestedSmURL)
							// Already marked, so no WG increment/decrement needed here
						}
					} // End loop through nested sitemap entries
					sitemapLog.Infof("Queued %d nested sitemaps.", queuedCount)
					return // Return after processing index
				}

				// --- Try Parsing as URL Set ---
				var urlSet parse.XMLURLSet
				errURLSet := xml.Unmarshal(sitemapBytes, &urlSet)
				if errURLSet != nil {
					// Only log error if it wasn't successfully parsed as an index either
					if errIndex != nil {
						sitemapLog.Errorf("Failed parse XML (Index err=%v; URLSet err=%v)", errIndex, errURLSet)
					} else {
						sitemapLog.Warnf("Content was not a valid Sitemap Index or URL Set (URLSet err=%v)", errURLSet)
					}
					return
				}

				// --- Process URL Set ---
				sitemapLog.Infof("Parsed as URL Set, found %d URLs.", len(urlSet.URLs))
				queuedCount := 0
				dbErrorCount := 0
				for _, urlEntry := range urlSet.URLs {
					pageURL := urlEntry.Loc
					pageLastMod := urlEntry.LastMod

					// Log the URL and its last modified date (if present)
					if pageLastMod != "" {
						sitemapLog.Debugf("Found URL: %s (LastMod: %s)", pageURL, pageLastMod)
					} else {
						sitemapLog.Debugf("Found URL: %s (No LastMod specified)", pageURL)
					}

					// --- Scope Check Logic ---
					parsedPageURL, err := url.Parse(pageURL)
					if err != nil {
						sitemapLog.Warnf("Sitemap URL parse error: %v", err)
						continue
					}
					if parsedPageURL.Scheme != "http" && parsedPageURL.Scheme != "https" {
						continue
					}
					if parsedPageURL.Hostname() != sp.siteCfg.AllowedDomain {
						continue
					}
					targetPath := parsedPageURL.Path
					if targetPath == "" {
						targetPath = "/"
					}
					if !strings.HasPrefix(targetPath, sp.siteCfg.AllowedPathPrefix) {
						continue
					}
					isDisallowed := false
					for _, pattern := range sp.compiledDisallowedPatterns {
						if pattern.MatchString(parsedPageURL.Path) {
							isDisallowed = true
							break
						}
					}
					if isDisallowed {
						continue
					}
					// --- End Scope Check ---

					// Normalize URL
					normalizedPageURL, _, errNorm := parse.ParseAndNormalize(pageURL)
					if errNorm != nil {
						sitemapLog.Warnf("Sitemap URL normalize error: %v", errNorm)
						continue
					}

					// --- Check/Add to DB and Queue if New ---
					// Use store's MarkPageVisited
					added, visitErr := sp.store.MarkPageVisited(normalizedPageURL)
					if visitErr != nil {
						sitemapLog.Errorf("Sitemap URL DB mark error: %v", visitErr)
						dbErrorCount++
						continue // Skip this URL if DB error occurs
					}

					if added { // If markVisited added the URL (it was new)
						sp.wg.Add(1) // Increment main WaitGroup
						// Add with Depth 0 as sitemaps don't have inherent depth
						sitemapWorkItem := models.WorkItem{URL: pageURL, Depth: 0}
						sp.pq.Add(&sitemapWorkItem) // Add to the main priority queue
						queuedCount++
					}
				} // End loop through URL entries

				if dbErrorCount > 0 {
					sitemapLog.Warnf("Finished URL Set. Queued %d new URLs, encountered %d DB errors.", queuedCount, dbErrorCount)
				} else {
					sitemapLog.Infof("Finished URL Set. Queued %d new URLs.", queuedCount)
				}

			}(sitemapURL) // End the anonymous goroutine for processing a single sitemap
		}
	}
}
