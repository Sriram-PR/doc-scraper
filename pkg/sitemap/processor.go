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
	log                        *logrus.Entry
	wg                         *sync.WaitGroup // main crawler WaitGroup
	sitemapsProcessed          map[string]bool
	sitemapsProcessedMu        sync.Mutex
}

// NewSitemapProcessor creates a new SitemapProcessor.
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
	log *logrus.Entry,
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
		log:               log.WithField("component", "sitemap_processor"),
		wg:                wg,
		sitemapsProcessed: make(map[string]bool),
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

func (sp *SitemapProcessor) run(ctx context.Context) { //nolint:gocyclo,funlen // recursive sitemap fetch/parse over multiple XML formats
	var sitemapProcessingWg sync.WaitGroup

	defer func() {
		sp.log.Info("Waiting for active sitemap processing tasks to finish before final exit...")
		sitemapProcessingWg.Wait()
		sp.log.Info("Sitemap processing goroutine finished waiting and exiting.")
	}()

	userAgent := sp.appCfg.DefaultUserAgent
	semTimeout := config.DefaultSemaphoreAcquireTimeout

	for {
		select {
		case <-ctx.Done():
			sp.log.Warnf("Context cancelled, stopping sitemap processing: %v", ctx.Err())
			return

		case sitemapURL, ok := <-sp.sitemapQueue:
			if !ok {
				sp.log.Info("Sitemap queue channel closed.")
				return
			}

			sitemapProcessingWg.Add(1)
			go func(smURL string) {
				defer func() {
					sp.wg.Done()
					sitemapProcessingWg.Done()
				}()

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

				parsedSitemapURL, err := url.Parse(smURL)
				if err != nil {
					sitemapLog.Errorf("Failed parse URL: %v", err)
					return
				}
				sitemapHost := parsedSitemapURL.Hostname()

				ctxG, cancelG := context.WithTimeout(ctx, semTimeout)
				err = sp.globalSemaphore.Acquire(ctxG, 1)
				cancelG()
				if err != nil {
					// Check if the error was due to the main context being cancelled
					switch {
					case errors.Is(err, context.DeadlineExceeded) && ctx.Err() != nil:
						sitemapLog.Warnf("Could not acquire GLOBAL semaphore due to main context cancellation: %v", ctx.Err())
					case errors.Is(err, context.DeadlineExceeded):
						sitemapLog.Errorf("Timeout acquiring GLOBAL semaphore: %v", err)
					default:
						sitemapLog.Errorf("Error acquiring GLOBAL semaphore: %v", err)
					}
					return // Stop processing this sitemap if semaphore not acquired
				}
				defer sp.globalSemaphore.Release(1)

				sp.rateLimiter.ApplyDelay(ctx, sitemapHost, sp.appCfg.DefaultDelayPerHost)

				req, err := http.NewRequestWithContext(ctx, http.MethodGet, smURL, nil)
				if err != nil {
					sitemapLog.Errorf("Req Create error: %v", err)
					return
				}
				req.Header.Set("User-Agent", userAgent)

				resp, fetchErr := sp.fetcher.FetchWithRetry(req, ctx)
				sp.rateLimiter.UpdateLastRequestTime(sitemapHost)

				if fetchErr != nil {
					sitemapLog.Errorf("Fetch failed: %v", fetchErr)
					if resp != nil {
						io.Copy(io.Discard, resp.Body)
						resp.Body.Close()
					}
					return
				}
				defer resp.Body.Close()

				const maxSitemapSize = 10 * 1024 * 1024 // 10 MB
				sitemapBytes, readErr := io.ReadAll(io.LimitReader(resp.Body, maxSitemapSize))
				if readErr != nil {
					sitemapLog.Errorf("Read body error: %v", readErr)
					return
				}

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
							continue
						}

						if sp.MarkSitemapProcessed(nestedSmURL) {
							sp.wg.Add(1)

							select {
							case sp.sitemapQueue <- nestedSmURL:
								queuedCount++
								nestedSmLog.Debug("Successfully queued nested sitemap.")

							case <-ctx.Done():
								nestedSmLog.Warnf("Context cancelled while trying to queue nested sitemap '%s': %v", nestedSmURL, ctx.Err())
								sp.sitemapsProcessedMu.Lock()
								delete(sp.sitemapsProcessed, nestedSmURL)
								sp.sitemapsProcessedMu.Unlock()
								sp.wg.Done()

							case <-time.After(5 * time.Second):
								nestedSmLog.Error("Timeout sending nested sitemap. Undoing WG and processed state.")
								sp.sitemapsProcessedMu.Lock()
								delete(sp.sitemapsProcessed, nestedSmURL)
								sp.sitemapsProcessedMu.Unlock()
								sp.wg.Done()
							}
						} else {
							nestedSmLog.Debugf("Nested sitemap already processed/queued: %s", nestedSmURL)
						}
					}
					sitemapLog.Infof("Queued %d nested sitemaps.", queuedCount)
					return // Return after processing index
				}

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

				sitemapLog.Infof("Parsed as URL Set, found %d URLs.", len(urlSet.URLs))
				queuedCount := 0
				dbErrorCount := 0
				for _, urlEntry := range urlSet.URLs {
					pageURL := urlEntry.Loc
					pageLastMod := urlEntry.LastMod

					if pageLastMod != "" {
						sitemapLog.Debugf("Found URL: %s (LastMod: %s)", pageURL, pageLastMod)
					} else {
						sitemapLog.Debugf("Found URL: %s (No LastMod specified)", pageURL)
					}

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

					normalizedPageURL, _, errNorm := parse.ParseAndNormalize(pageURL)
					if errNorm != nil {
						sitemapLog.Warnf("Sitemap URL normalize error: %v", errNorm)
						continue
					}

					added, visitErr := sp.store.MarkPageVisited(normalizedPageURL)
					if visitErr != nil {
						sitemapLog.Errorf("Sitemap URL DB mark error: %v", visitErr)
						dbErrorCount++
						continue
					}

					if added {
						sp.wg.Add(1)
						// Enqueue the normalized URL so WorkItem.URL and the DB
						// key agree; see process/links.go for the rationale.
						sitemapWorkItem := models.WorkItem{URL: normalizedPageURL, Depth: 0}
						sp.pq.Add(&sitemapWorkItem)
						queuedCount++
					}
				}

				if dbErrorCount > 0 {
					sitemapLog.Warnf("Finished URL Set. Queued %d new URLs, encountered %d DB errors.", queuedCount, dbErrorCount)
				} else {
					sitemapLog.Infof("Finished URL Set. Queued %d new URLs.", queuedCount)
				}

			}(sitemapURL)
		}
	}
}
