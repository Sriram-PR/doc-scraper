package process

import (
	"fmt"
	"net/url"
	"path"
	"regexp"
	"strings"
	"sync"

	"github.com/PuerkitoBio/goquery"
	"github.com/sirupsen/logrus"

	"github.com/Sriram-PR/doc-scraper/pkg/config"
	"github.com/Sriram-PR/doc-scraper/pkg/models"
	"github.com/Sriram-PR/doc-scraper/pkg/parse"
	"github.com/Sriram-PR/doc-scraper/pkg/queue"
	"github.com/Sriram-PR/doc-scraper/pkg/storage"
	"github.com/Sriram-PR/doc-scraper/pkg/utils"
)

// resourceExtensions is the set of file extensions that are never crawlable pages.
var resourceExtensions = map[string]struct{}{
	".svg": {}, ".png": {}, ".jpg": {}, ".jpeg": {}, ".gif": {}, ".webp": {}, ".ico": {}, ".bmp": {}, ".tiff": {}, ".avif": {},
	".woff": {}, ".woff2": {}, ".ttf": {}, ".eot": {}, ".otf": {},
	".css": {}, ".js": {}, ".mjs": {},
	".pdf": {}, ".zip": {}, ".tar": {}, ".gz": {}, ".tgz": {},
	".mp4": {}, ".mp3": {}, ".wav": {}, ".ogg": {}, ".webm": {},
}

func isResourceURL(u *url.URL) bool {
	ext := strings.ToLower(path.Ext(u.Path))
	_, ok := resourceExtensions[ext]
	return ok
}

// LinkProcessor handles extracting and queueing links found on a page.
type LinkProcessor struct {
	store                      storage.PageStore
	pq                         *queue.ThreadSafePriorityQueue
	compiledDisallowedPatterns []*regexp.Regexp
	log                        *logrus.Entry
}

// NewLinkProcessor creates a LinkProcessor.
func NewLinkProcessor(
	store storage.PageStore,
	pq *queue.ThreadSafePriorityQueue,
	compiledDisallowedPatterns []*regexp.Regexp,
	log *logrus.Entry,
) *LinkProcessor {
	return &LinkProcessor{
		store:                      store,
		pq:                         pq,
		compiledDisallowedPatterns: compiledDisallowedPatterns,
		log:                        log,
	}
}

// ExtractAndQueueLinks finds crawlable links in the original (pre-conversion) document, filters
// them against scope and disallowed patterns, and enqueues newly-seen URLs. Returns the count
// queued and the first non-fatal DB error encountered.
func (lp *LinkProcessor) ExtractAndQueueLinks( //nolint:gocyclo // link extraction with many filtering and normalization steps
	originalDoc *goquery.Document,
	finalURL *url.URL,
	currentDepth int,
	siteCfg *config.SiteConfig,
	wg *sync.WaitGroup,
	taskLog *logrus.Entry,
) (queuedCount int, err error) {

	nextDepth := currentDepth + 1
	taskLog = taskLog.WithField("next_depth", nextDepth)
	taskLog.Debug("Extracting and queueing links...")
	var firstDBError error

	if siteCfg.MaxDepth > 0 && nextDepth > siteCfg.MaxDepth {
		taskLog.Debugf("Max depth (%d) reached/exceeded for next level (%d), skipping link extraction.", siteCfg.MaxDepth, nextDepth)
		return 0, nil
	}

	foundLinks := make(map[string]string) // normalized URL -> original URL

	selectorsToSearch := siteCfg.LinkExtractionSelectors
	if len(selectorsToSearch) == 0 {
		selectorsToSearch = []string{"body"}
		taskLog.Debug("No link_extraction_selectors defined, defaulting to 'body'")
	} else {
		taskLog.Debugf("Using link_extraction_selectors: %v", selectorsToSearch)
	}

	for _, selector := range selectorsToSearch {
		taskLog.Debugf("Searching for links within selector: '%s'", selector)
		originalDoc.Find(selector).Find("a[href]").Each(func(index int, element *goquery.Selection) {
			href, exists := element.Attr("href")
			if !exists || href == "" {
				return
			}

			if siteCfg.RespectNofollow {
				if rel, _ := element.Attr("rel"); strings.Contains(strings.ToLower(rel), "nofollow") {
					taskLog.Debugf("Skipping nofollow link: %s", href)
					return
				}
			}

			linkURL, parseErr := finalURL.Parse(href)
			if parseErr != nil {
				taskLog.Warnf("Skipping invalid link href '%s' in selector '%s': %v", href, selector, parseErr)
				return
			}
			absoluteLinkURL := linkURL.String()

			if isResourceURL(linkURL) {
				return
			}
			if linkURL.Scheme != "http" && linkURL.Scheme != "https" {
				return
			}

			if linkURL.Hostname() != siteCfg.AllowedDomain {
				return
			}

			targetPath := linkURL.Path
			if targetPath == "" {
				targetPath = "/"
			}
			if !strings.HasPrefix(targetPath, siteCfg.AllowedPathPrefix) {
				return
			}

			isDisallowed := false
			for _, pattern := range lp.compiledDisallowedPatterns {
				if pattern.MatchString(linkURL.Path) {
					isDisallowed = true
					taskLog.Debugf("Link '%s' disallowed by pattern: %s", absoluteLinkURL, pattern.String())
					break
				}
			}
			if isDisallowed {
				return
			}

			// Normalize the valid, in-scope URL
			normalizedLink, _, errNorm := parse.ParseAndNormalize(absoluteLinkURL)
			if errNorm != nil {
				taskLog.Warnf("Cannot normalize extracted link '%s': %v", absoluteLinkURL, errNorm)
				return // Skip if normalization fails
			}

			// Add to map if not already present (using normalized as key)
			if _, found := foundLinks[normalizedLink]; !found {
				foundLinks[normalizedLink] = absoluteLinkURL
			}
		})
	}

	if len(foundLinks) > 0 {
		taskLog.Debugf("Found %d unique, valid, in-scope links across all specified selectors.", len(foundLinks))
		for normalizedLink, originalLinkURL := range foundLinks {
			added, visitErr := lp.store.MarkPageVisited(normalizedLink)
			if visitErr != nil {
				dbErr := fmt.Errorf("%w: checking/marking link '%s' visited: %w", utils.ErrDatabase, normalizedLink, visitErr)
				taskLog.Error(dbErr)
				if firstDBError == nil {
					firstDBError = dbErr
				}
				continue
			}

			if added {
				wg.Add(1)
				nextWorkItem := models.WorkItem{URL: originalLinkURL, Depth: nextDepth}
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
	return queuedCount, firstDBError
}
