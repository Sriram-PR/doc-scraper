package process

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"sync"

	"github.com/PuerkitoBio/goquery"
	"github.com/sirupsen/logrus"

	"github.com/piratf/doc-scraper/pkg/config"
	"github.com/piratf/doc-scraper/pkg/models"
	"github.com/piratf/doc-scraper/pkg/parse"
	"github.com/piratf/doc-scraper/pkg/queue"
	"github.com/piratf/doc-scraper/pkg/storage"
	"github.com/piratf/doc-scraper/pkg/utils"
)

// LinkProcessor handles extracting and queueing links found on a page
type LinkProcessor struct {
	store                      storage.PageStore              // To check/mark visited status
	pq                         *queue.ThreadSafePriorityQueue // To queue new work items
	compiledDisallowedPatterns []*regexp.Regexp               // Pre-compiled patterns
	log                        *logrus.Logger
}

// NewLinkProcessor creates a LinkProcessor
func NewLinkProcessor(
	store storage.PageStore,
	pq *queue.ThreadSafePriorityQueue,
	compiledDisallowedPatterns []*regexp.Regexp,
	log *logrus.Logger,
) *LinkProcessor {
	return &LinkProcessor{
		store:                      store,
		pq:                         pq,
		compiledDisallowedPatterns: compiledDisallowedPatterns,
		log:                        log,
	}
}

// ExtractAndQueueLinks finds crawlable links within the specified selectors of a document, filters them based on scope and rules, and adds new ones to the priority queue
// It takes the *original* document to ensure all potential links are considered, before the content might be modified by Markdown conversion etc
func (lp *LinkProcessor) ExtractAndQueueLinks(
	originalDoc *goquery.Document, // Use the original, unmodified document
	finalURL *url.URL, // The final URL of the page (after redirects) to use as base
	currentDepth int, // The depth of the current page
	siteCfg config.SiteConfig, // Need site config for rules (nofollow, selectors, scope)
	wg *sync.WaitGroup, // Need WaitGroup to increment for queued items
	taskLog *logrus.Entry,
) (queuedCount int, err error) { // Return non-fatal error for DB issues

	nextDepth := currentDepth + 1
	taskLog = taskLog.WithField("next_depth", nextDepth) // Add next depth to log context
	taskLog.Debug("Extracting and queueing links...")
	queuedCount = 0
	var firstDBError error = nil

	// Check Max Depth for *next* level before even starting extraction
	if siteCfg.MaxDepth > 0 && nextDepth > siteCfg.MaxDepth {
		taskLog.Debugf("Max depth (%d) reached/exceeded for next level (%d), skipping link extraction.", siteCfg.MaxDepth, nextDepth)
		return 0, nil // No error, just skip
	}

	// Use a map to store unique absolute normalized URLs found across all selectors for this page
	foundLinks := make(map[string]string) // Map normalized URL -> original URL (for queuing)

	// Determine which selectors to use for link extraction
	selectorsToSearch := siteCfg.LinkExtractionSelectors
	if len(selectorsToSearch) == 0 {
		// Default behavior: Search the whole document body
		selectorsToSearch = []string{"body"}
		taskLog.Debug("No link_extraction_selectors defined, defaulting to 'body'")
	} else {
		taskLog.Debugf("Using link_extraction_selectors: %v", selectorsToSearch)
	}

	// --- Loop through the specified selectors ---
	for _, selector := range selectorsToSearch {
		taskLog.Debugf("Searching for links within selector: '%s'", selector)
		// Find links within the specified selector in the *original* document
		originalDoc.Find(selector).Find("a[href]").Each(func(index int, element *goquery.Selection) {
			href, exists := element.Attr("href")
			if !exists || href == "" {
				return // Skip empty hrefs
			}

			// Check nofollow *before* resolving URL (slightly more efficient)
			if siteCfg.RespectNofollow {
				if rel, _ := element.Attr("rel"); strings.Contains(strings.ToLower(rel), "nofollow") {
					taskLog.Debugf("Skipping nofollow link: %s", href)
					return
				}
			}

			// Resolve URL relative to the page's final URL
			linkURL, parseErr := finalURL.Parse(href)
			if parseErr != nil {
				taskLog.Warnf("Skipping invalid link href '%s' in selector '%s': %v", href, selector, parseErr)
				return // Skip unparseable links
			}
			absoluteLinkURL := linkURL.String() // Get the absolute URL string

			// --- Apply Standard Filtering Logic ---
			// Scheme check
			if linkURL.Scheme != "http" && linkURL.Scheme != "https" {
				return // Skip non-http(s) links like mailto:, tel:, etc
			}
			// Basic skip patterns (fragments handled by normalization later)
			// TODO: Javascript check - more robust? Maybe check scheme directly

			// Scope: Domain
			if linkURL.Hostname() != siteCfg.AllowedDomain {
				return // Skip different domains
			}

			// Scope: Prefix
			// Normalize path for check (ensure leading slash)
			targetPath := linkURL.Path
			if targetPath == "" {
				targetPath = "/"
			}
			if !strings.HasPrefix(targetPath, siteCfg.AllowedPathPrefix) {
				return // Skip paths outside the allowed prefix
			}

			// Scope: Disallowed patterns (use pre-compiled regex from LinkProcessor)
			isDisallowed := false
			for _, pattern := range lp.compiledDisallowedPatterns {
				// Match against the path part of the URL
				if pattern.MatchString(linkURL.Path) {
					isDisallowed = true
					taskLog.Debugf("Link '%s' disallowed by pattern: %s", absoluteLinkURL, pattern.String())
					break
				}
			}
			if isDisallowed {
				return // Skip disallowed paths
			}
			// --- End Filtering ---

			// Normalize the valid, in-scope URL
			normalizedLink, _, errNorm := parse.ParseAndNormalize(absoluteLinkURL)
			if errNorm != nil {
				taskLog.Warnf("Cannot normalize extracted link '%s': %v", absoluteLinkURL, errNorm)
				return // Skip if normalization fails
			}

			// Add to map if not already present (using normalized as key)
			if _, found := foundLinks[normalizedLink]; !found {
				foundLinks[normalizedLink] = absoluteLinkURL // Store original URL for queueing
			}
		})
	}

	// --- Queue New Links (check DB before queueing) ---
	if len(foundLinks) > 0 {
		taskLog.Debugf("Found %d unique, valid, in-scope links across all specified selectors.", len(foundLinks))
		for normalizedLink, originalLinkURL := range foundLinks {
			// Check DB: Use MarkPageVisited which handles adding if not found
			// This prevents queueing if the link was *already* successfully processed or is currently pending from another source (sitemap, resume)
			added, visitErr := lp.store.MarkPageVisited(normalizedLink)
			if visitErr != nil {
				dbErr := fmt.Errorf("%w: checking/marking link '%s' visited: %w", utils.ErrDatabase, normalizedLink, visitErr)
				taskLog.Error(dbErr)
				if firstDBError == nil {
					firstDBError = dbErr
				} // Collect first DB error encountered
				continue // Skip this link if DB error occurs
			}

			// Only queue if MarkPageVisited returned true (meaning it was newly added)
			if added {
				wg.Add(1)                                                               // Increment WaitGroup *before* adding to queue
				nextWorkItem := models.WorkItem{URL: originalLinkURL, Depth: nextDepth} // Queue the *original* URL
				lp.pq.Add(&nextWorkItem)
				queuedCount++
				taskLog.Debugf("Queued new link: %s (Normalized: %s)", originalLinkURL, normalizedLink)
			} else {
				taskLog.Debugf("Link already visited/pending, skipping queue: %s", normalizedLink)
			}
		}
	} else {
		taskLog.Debug("No new valid links found to queue.")
	}

	taskLog.Infof("Finished link extraction. Queued %d NEW links.", queuedCount)
	return queuedCount, firstDBError // Return count and any non-fatal DB error encountered
}
